package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	chromem "github.com/philippgille/chromem-go"
)

type vectorStore struct {
	col         *chromem.Collection
	embedClient embeddingsClient
	dedupDist   float32
	hashes      sync.Map // sha256hex → memoryID; rebuilt at runtime, not persisted
}

func newVectorStore(dir, collection string, embed embeddingsClient, dedupDist float64) (*vectorStore, error) {
	db, err := chromem.NewPersistentDB(dir, false)
	if err != nil {
		return nil, fmt.Errorf("open chromem at %s: %w", dir, err)
	}

	embedFn := func(ctx context.Context, text string) ([]float32, error) {
		return embed.embed(text)
	}
	col, err := db.GetOrCreateCollection(collection, map[string]string{"hnsw:space": "cosine"}, embedFn)
	if err != nil {
		return nil, fmt.Errorf("get/create collection %q: %w", collection, err)
	}
	return &vectorStore{col: col, embedClient: embed, dedupDist: float32(dedupDist)}, nil
}

// addText mirrors Python VectorStore.add_text: SHA256 dedup → embed → cosine NN dedup → upsert.
func (v *vectorStore) addText(text string, meta map[string]any, memoryID string) (*AddResult, error) {
	if text == "" {
		return nil, errors.New("text is empty")
	}

	h := sha256Text(text)

	// exact hash dedup (in-memory, rebuilt at runtime)
	if existing, ok := v.hashes.Load(h); ok {
		return &AddResult{MemoryID: existing.(string), Skipped: true, Reason: "exact_hash_duplicate"}, nil
	}

	emb, err := v.embedClient.embed(text)
	if err != nil {
		return nil, fmt.Errorf("embed: %w", err)
	}

	// semantic NN dedup (only if collection non-empty)
	if v.col.Count() > 0 {
		hits, err := v.col.QueryEmbedding(context.Background(), emb, 1, nil, nil)
		if err == nil && len(hits) > 0 {
			dist := float32(1.0 - hits[0].Similarity)
			if dist <= v.dedupDist {
				return &AddResult{MemoryID: hits[0].ID, Skipped: true, Reason: "semantic_duplicate"}, nil
			}
		}
	}

	if memoryID == "" {
		ts := strconv.FormatInt(time.Now().UnixNano(), 10)
		sum := sha256.Sum256([]byte(text + ts))
		memoryID = hex.EncodeToString(sum[:16])
	}

	sm := coerceMeta(meta)
	sm["hash"] = h
	if _, ok := sm["created_at"]; !ok {
		sm["created_at"] = strconv.FormatFloat(float64(time.Now().UnixMilli())/1000.0, 'f', 3, 64)
	}

	err = v.col.AddDocument(context.Background(), chromem.Document{
		ID:        memoryID,
		Metadata:  sm,
		Embedding: emb,
		Content:   text,
	})
	if err != nil {
		return nil, fmt.Errorf("chromem add: %w", err)
	}
	v.hashes.Store(h, memoryID)
	return &AddResult{MemoryID: memoryID, Skipped: false}, nil
}

func (v *vectorStore) searchText(query string, k int) (*SearchResult, error) {
	if query == "" {
		return &SearchResult{}, nil
	}
	if k <= 0 {
		k = 5
	}
	if count := v.col.Count(); count == 0 {
		return &SearchResult{Hits: []MemoryHit{}}, nil
	} else if k > count {
		k = count // chromem: nResults must be ≤ collection size
	}

	emb, err := v.embedClient.embed(query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	docs, err := v.col.QueryEmbedding(context.Background(), emb, k, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("chromem query: %w", err)
	}

	hits := make([]MemoryHit, 0, len(docs))
	for _, d := range docs {
		m := make(map[string]any, len(d.Metadata))
		for k, v := range d.Metadata {
			m[k] = v
		}
		hits = append(hits, MemoryHit{
			ID:       d.ID,
			Text:     d.Content,
			Metadata: m,
			Distance: float64(1.0 - d.Similarity),
		})
	}
	return &SearchResult{Hits: hits}, nil
}

func coerceMeta(m map[string]any) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		switch x := v.(type) {
		case nil:
			continue
		case string:
			out[k] = x
		case bool:
			out[k] = strconv.FormatBool(x)
		case float64:
			if x == float64(int64(x)) {
				out[k] = strconv.FormatInt(int64(x), 10)
			} else {
				out[k] = strconv.FormatFloat(x, 'f', -1, 64)
			}
		default:
			out[k] = fmt.Sprintf("%v", v)
		}
	}
	return out
}

// probeEmbed makes a minimal embed call to verify the embeddings backend is
// reachable and the configured model is loaded.  Called by Client.EmbedHealthy.
func (v *vectorStore) probeEmbed() error {
	_, err := v.embedClient.embed("probe")
	return err
}

func sha256Text(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
