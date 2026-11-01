package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	chromem "github.com/philippgille/chromem-go"
)

type vectorStore struct {
	col         *chromem.Collection
	embedClient embeddingsClient
	dedupDist   float32

	// mu serialises addText so the dedup pipeline (hash check → embed →
	// semantic NN check → upsert → hash store) is atomic. Without it two
	// concurrent identical vector_add calls (e.g. a parallel tool batch)
	// both pass the hash check and both get inserted, defeating dedup.
	mu     sync.Mutex
	hashes map[string]string // sha256hex → memoryID; rebuilt at runtime, not persisted

	// shrink is held for writing while documents are deleted and for reading
	// around every query that sizes k from Count(). chromem refuses
	// k > Count(), so a delete landing between the two failed the query.
	shrink sync.RWMutex

	// indexed reports whether hashes reflects the documents on disk. The
	// index lives only in memory; it is rebuilt at open time when the
	// embedding width is known, otherwise on the first add (which embeds
	// anyway and so learns the width). Guarded by mu.
	indexed bool
	// dim is the embedding width, persisted in dimPath. Knowing it lets the
	// store enumerate documents with a fixed probe vector instead of an
	// embeddings call, so listing and the index rebuild work while the
	// embeddings backend is down. Guarded by mu.
	dim     int
	dimPath string
}

// commonEmbedDims are tried, in order, to enumerate a collection written
// before the width was recorded. A wrong width fails fast with a length
// mismatch; the first one that works is recorded.
var commonEmbedDims = []int{768, 1024, 1536, 384, 3072, 512, 4096, 2048, 2560, 1280, 256, 64}

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
	v := &vectorStore{
		col:         col,
		embedClient: embed,
		dedupDist:   float32(dedupDist),
		hashes:      map[string]string{},
		dimPath:     filepath.Join(dir, "omegagrid-"+safeFileName(collection)+".dim.json"),
	}
	v.dim = v.loadDim()
	v.mu.Lock()
	if err := v.rebuildIndexLocked(); err != nil {
		log.Printf("memory: dedup index not rebuilt at open (%v); it will be on the first add", err)
	}
	v.mu.Unlock()
	return v, nil
}

func safeFileName(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == 0 || r == ':' {
			return '_'
		}
		return r
	}, s)
}

func (v *vectorStore) loadDim() int {
	b, err := os.ReadFile(v.dimPath)
	if err != nil {
		return 0
	}
	var rec struct {
		Dim int `json:"dim"`
	}
	if json.Unmarshal(b, &rec) != nil || rec.Dim <= 0 {
		return 0
	}
	return rec.Dim
}

// noteDimLocked records the embedding width the first time it is seen.
func (v *vectorStore) noteDimLocked(n int) {
	if n <= 0 || v.dim == n {
		return
	}
	v.dim = n
	b, _ := json.Marshal(map[string]int{"dim": n})
	if err := os.WriteFile(v.dimPath, b, 0o644); err != nil {
		log.Printf("memory: cannot record embedding width: %v", err)
	}
}

// probeVector is a unit vector of the collection's width. Similarity
// against it is meaningless; it exists only to enumerate documents.
func probeVector(dim int) []float32 {
	p := make([]float32, dim)
	p[0] = 1
	return p
}

// enumerateLocked returns every document without an embeddings call, or an
// error when the width is unknown and no common width fits. Caller holds mu.
func (v *vectorStore) enumerateLocked() ([]chromem.Result, error) {
	count := v.col.Count()
	if count == 0 {
		return nil, nil
	}
	if v.dim > 0 {
		return v.queryClamped(probeVector(v.dim), count)
	}
	for _, d := range commonEmbedDims {
		docs, err := v.queryClamped(probeVector(d), count)
		if err == nil {
			v.noteDimLocked(d)
			return docs, nil
		}
	}
	return nil, errors.New("embedding width unknown")
}

// rebuildIndexLocked repopulates hashes from the stored documents. Before
// it existed the index started empty on every restart, so an exact re-add
// cost an embed call and was caught only by the semantic check.
func (v *vectorStore) rebuildIndexLocked() error {
	if v.col.Count() == 0 {
		v.indexed = true
		return nil
	}
	docs, err := v.enumerateLocked()
	if err != nil {
		return err
	}
	now := time.Now()
	for _, d := range docs {
		if expired(d.Metadata, now) {
			continue
		}
		h := d.Metadata["hash"]
		if h == "" {
			h = hashKey(d.Metadata["namespace"], d.Content)
		}
		v.hashes[h] = d.ID
	}
	v.indexed = true
	return nil
}

// hashKey is the exact-dedup key. The default namespace keeps the plain
// SHA256 of the text, so documents written before namespaces existed keep
// matching.
func hashKey(namespace, text string) string {
	if namespace == "" {
		return sha256Text(text)
	}
	return sha256Text(namespace + "\x00" + text)
}

// addText mirrors Python VectorStore.add_text: SHA256 dedup → embed → cosine
// NN dedup → upsert. meta may carry "namespace" (dedup is per namespace) and
// "expires_at" (unix seconds; the janitor deletes the memory after it).
func (v *vectorStore) addText(text string, meta map[string]any, memoryID string) (*AddResult, error) {
	return v.add(text, meta, memoryID, nil)
}

// add is addText with an optional precomputed embedding (import).
func (v *vectorStore) add(text string, meta map[string]any, memoryID string, emb []float32) (*AddResult, error) {
	if text == "" {
		return nil, errors.New("text is empty")
	}

	// Serialise the whole dedup pipeline: the hash check below and the
	// hash store at the end must be atomic relative to other adds.
	v.mu.Lock()
	defer v.mu.Unlock()

	sm := coerceMeta(meta)
	ns := sm["namespace"]
	if ns == "" {
		delete(sm, "namespace")
	}
	h := hashKey(ns, text)

	if !v.indexed && emb == nil {
		// Learn the width from this add's own embedding, then rebuild, so
		// the exact-hash check below sees the documents already on disk.
		e, err := v.embedClient.embed(text)
		if err != nil {
			return nil, fmt.Errorf("embed: %w", err)
		}
		emb = e
		v.noteDimLocked(len(emb))
		if err := v.rebuildIndexLocked(); err != nil {
			log.Printf("memory: dedup index rebuild failed: %v", err)
		}
	}

	// exact hash dedup (in-memory, rebuilt from disk)
	if existing, ok := v.liveHashLocked(h); ok {
		return &AddResult{MemoryID: existing, Skipped: true, Reason: "exact_hash_duplicate"}, nil
	}

	if emb == nil {
		e, err := v.embedClient.embed(text)
		if err != nil {
			return nil, fmt.Errorf("embed: %w", err)
		}
		emb = e
	}
	v.noteDimLocked(len(emb))

	// semantic NN dedup, within the namespace (a no-op on an empty one)
	if hits, err := v.queryLiveWhere(emb, 1, map[string]string{"namespace": ns}); err == nil && len(hits) > 0 {
		dist := float32(1.0 - hits[0].Similarity)
		if dist <= v.dedupDist {
			return &AddResult{MemoryID: hits[0].ID, Skipped: true, Reason: "semantic_duplicate"}, nil
		}
	}

	if memoryID == "" {
		ts := strconv.FormatInt(time.Now().UnixNano(), 10)
		sum := sha256.Sum256([]byte(text + ts))
		memoryID = hex.EncodeToString(sum[:16])
	}

	sm["hash"] = h
	if _, ok := sm["created_at"]; !ok {
		sm["created_at"] = strconv.FormatFloat(float64(time.Now().UnixMilli())/1000.0, 'f', 3, 64)
	}

	// AddDocument is an upsert. Keep the dedup index consistent when an
	// explicit ID replaces different content or moves namespaces.
	old, _ := v.col.GetByID(context.Background(), memoryID)
	err := v.col.AddDocument(context.Background(), chromem.Document{
		ID:        memoryID,
		Metadata:  sm,
		Embedding: emb,
		Content:   text,
	})
	if err != nil {
		return nil, fmt.Errorf("chromem add: %w", err)
	}
	if old.ID != "" {
		oldHash := hashKey(old.Metadata["namespace"], old.Content)
		if oldHash != h && v.hashes[oldHash] == memoryID {
			delete(v.hashes, oldHash)
		}
	}
	v.hashes[h] = memoryID
	return &AddResult{MemoryID: memoryID, Skipped: false}, nil
}

func (v *vectorStore) searchText(query string, k int) (*SearchResult, error) {
	return v.searchIn(query, k, nil)
}

// searchIn is a semantic search restricted to namespaces (nil = all of
// them). Expired memories are never returned.
func (v *vectorStore) searchIn(query string, k int, namespaces []string) (*SearchResult, error) {
	if query == "" {
		return &SearchResult{}, nil
	}
	if k <= 0 {
		k = 5
	}
	if v.col.Count() == 0 {
		return &SearchResult{Hits: []MemoryHit{}}, nil // skip the embed call
	}

	emb, err := v.embedClient.embed(query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	// chromem's where filter is an exact-match AND, so a union of
	// namespaces is one query per namespace, merged by similarity. Queries
	// expand past expired documents until enough live results are found.
	var docs []chromem.Result
	if namespaces == nil {
		docs, err = v.queryLiveWhere(emb, k, nil)
	} else {
		for _, ns := range uniqueStrings(namespaces) {
			part, qerr := v.queryLiveWhere(emb, k, map[string]string{"namespace": ns})
			if qerr != nil {
				return nil, fmt.Errorf("chromem query: %w", qerr)
			}
			docs = append(docs, part...)
		}
		sort.SliceStable(docs, func(i, j int) bool { return docs[i].Similarity > docs[j].Similarity })
	}
	if err != nil {
		return nil, fmt.Errorf("chromem query: %w", err)
	}

	now := time.Now()
	hits := make([]MemoryHit, 0, min(k, len(docs)))
	for _, d := range docs {
		if len(hits) == k {
			break
		}
		if expired(d.Metadata, now) {
			continue
		}
		hit := toHit(d.ID, d.Content, d.Metadata)
		hit.Distance = float64(1.0 - d.Similarity)
		hits = append(hits, hit)
	}
	return &SearchResult{Hits: hits}, nil
}

func uniqueStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func toHit(id, text string, meta map[string]string) MemoryHit {
	m := make(map[string]any, len(meta))
	for k, val := range meta {
		m[k] = val
	}
	return MemoryHit{ID: id, Text: text, Metadata: m}
}

// expired reports whether a document's expires_at (unix seconds) has passed.
func expired(meta map[string]string, now time.Time) bool {
	s := meta["expires_at"]
	if s == "" {
		return false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return false
	}
	return now.After(time.Unix(0, int64(f*1e9)))
}

// queryClamped runs a nearest-neighbour query for up to k results, clamping
// k to the collection size under the shrink lock so no delete can make it
// stale. It returns nil for an empty collection.
func (v *vectorStore) queryClamped(emb []float32, k int) ([]chromem.Result, error) {
	return v.queryClampedWhere(emb, k, nil)
}

// queryClampedWhere is queryClamped with a metadata filter. k is clamped to
// the whole collection, which chromem then caps at the filtered size itself.
func (v *vectorStore) queryClampedWhere(emb []float32, k int, where map[string]string) ([]chromem.Result, error) {
	v.shrink.RLock()
	defer v.shrink.RUnlock()
	if count := v.col.Count(); count == 0 {
		return nil, nil
	} else if k > count {
		k = count
	}
	return v.col.QueryEmbedding(context.Background(), emb, k, where, nil)
}

// queryLiveWhere widens the nearest-neighbour window when expired documents
// occupy the top results. A fixed overfetch can hide every live memory.
func (v *vectorStore) queryLiveWhere(emb []float32, k int, where map[string]string) ([]chromem.Result, error) {
	count := v.col.Count()
	if count == 0 {
		return nil, nil
	}
	limit := min(k, count)
	now := time.Now()
	for {
		docs, err := v.queryClampedWhere(emb, limit, where)
		if err != nil {
			return nil, err
		}
		live := make([]chromem.Result, 0, min(k, len(docs)))
		for _, d := range docs {
			if !expired(d.Metadata, now) {
				live = append(live, d)
				if len(live) == k {
					return live, nil
				}
			}
		}
		if len(docs) < limit || limit >= count {
			return live, nil
		}
		limit += min(limit, count-limit)
	}
}

// liveHashLocked ignores expired entries even before the janitor removes
// them. The caller holds mu, as with all dedup index updates.
func (v *vectorStore) liveHashLocked(h string) (string, bool) {
	id, ok := v.hashes[h]
	if !ok {
		return "", false
	}
	doc, err := v.col.GetByID(context.Background(), id)
	if err != nil || expired(doc.Metadata, time.Now()) {
		delete(v.hashes, h)
		return "", false
	}
	return id, true
}

// ListFilter narrows listAll. Namespace nil means every namespace.
type ListFilter struct {
	Namespace      *string
	IncludeExpired bool
}

// listAll enumerates every stored memory, newest first by created_at, then
// paginates. limit<=0 means "no limit". chromem-go v0.7.0 has no list API,
// so enumeration is a nearest-neighbour query for the whole collection
// against a fixed probe vector — no embeddings call once the width is known
// (before that, the probe is embedded once, which also records the width).
func (v *vectorStore) listAll(limit, offset int) ([]MemoryHit, int, error) {
	return v.list(ListFilter{}, limit, offset)
}

func (v *vectorStore) list(f ListFilter, limit, offset int) ([]MemoryHit, int, error) {
	docs, err := v.enumerate()
	if err != nil {
		return nil, 0, err
	}
	now := time.Now()
	all := make([]MemoryHit, 0, len(docs))
	for _, d := range docs {
		if f.Namespace != nil && d.Metadata["namespace"] != *f.Namespace {
			continue
		}
		if !f.IncludeExpired && expired(d.Metadata, now) {
			continue
		}
		all = append(all, toHit(d.ID, d.Content, d.Metadata))
	}

	sort.SliceStable(all, func(i, j int) bool {
		return createdAt(all[i].Metadata) > createdAt(all[j].Metadata)
	})

	total := len(all)
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return []MemoryHit{}, total, nil
	}
	all = all[offset:]
	if limit > 0 && limit < len(all) {
		all = all[:limit]
	}
	return all, total, nil
}

// enumerate returns every document, falling back to an embedded probe when
// the width is unknown and none of the common widths fit.
func (v *vectorStore) enumerate() ([]chromem.Result, error) {
	if v.col.Count() == 0 {
		return nil, nil
	}
	v.mu.Lock()
	docs, err := v.enumerateLocked()
	v.mu.Unlock()
	if err == nil {
		return docs, nil
	}
	emb, err := v.embedClient.embed("probe")
	if err != nil {
		return nil, fmt.Errorf("embed probe: %w", err)
	}
	v.mu.Lock()
	v.noteDimLocked(len(emb))
	v.mu.Unlock()
	docs, err = v.queryClamped(emb, v.col.Count())
	if err != nil {
		return nil, fmt.Errorf("chromem enumerate: %w", err)
	}
	return docs, nil
}

// namespaces counts live memories per namespace ("" is the default one).
func (v *vectorStore) namespaces() (map[string]int, error) {
	docs, err := v.enumerate()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := map[string]int{}
	for _, d := range docs {
		if !expired(d.Metadata, now) {
			out[d.Metadata["namespace"]]++
		}
	}
	return out, nil
}

// purgeExpired deletes every memory whose expires_at has passed.
func (v *vectorStore) purgeExpired(now time.Time) (int, error) {
	docs, err := v.enumerate()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, d := range docs {
		if expired(d.Metadata, now) {
			if ok, err := v.deleteByID(d.ID); err != nil {
				return n, err
			} else if ok {
				n++
			}
		}
	}
	return n, nil
}

// update rewrites a memory in place, keeping its id and created_at. A new
// text is re-embedded and re-hashed; meta keys are merged, and a key whose
// value is nil is removed. It fails when the new text would duplicate a
// different memory in the same namespace.
func (v *vectorStore) update(id string, text *string, meta map[string]any) (*MemoryHit, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	doc, err := v.col.GetByID(context.Background(), id)
	if err != nil {
		return nil, nil
	}
	newText := doc.Content
	if text != nil {
		newText = strings.TrimSpace(*text)
		if newText == "" {
			return nil, errors.New("text is empty")
		}
	}
	sm := make(map[string]string, len(doc.Metadata)+2)
	for k, val := range doc.Metadata {
		sm[k] = val
	}
	for k, val := range meta {
		if val == nil {
			delete(sm, k)
		}
	}
	for k, val := range coerceMeta(meta) {
		sm[k] = val
	}
	if sm["namespace"] == "" {
		delete(sm, "namespace")
	}
	h := hashKey(sm["namespace"], newText)
	if other, ok := v.liveHashLocked(h); ok && other != id {
		return nil, fmt.Errorf("an identical memory already exists: %s", other)
	}
	emb := doc.Embedding
	if newText != doc.Content {
		if emb, err = v.embedClient.embed(newText); err != nil {
			return nil, fmt.Errorf("embed: %w", err)
		}
		v.noteDimLocked(len(emb))
	}
	sm["hash"] = h
	sm["updated_at"] = strconv.FormatFloat(float64(time.Now().UnixMilli())/1000.0, 'f', 3, 64)
	if err := v.col.AddDocument(context.Background(), chromem.Document{
		ID: id, Metadata: sm, Embedding: emb, Content: newText,
	}); err != nil {
		return nil, fmt.Errorf("chromem update: %w", err)
	}
	if old := doc.Metadata["hash"]; old != "" && old != h && v.hashes[old] == id {
		delete(v.hashes, old)
	}
	v.hashes[h] = id
	hit := toHit(id, newText, sm)
	return &hit, nil
}

// exportAll returns every memory (expired ones included) with metadata and,
// optionally, its embedding.
func (v *vectorStore) exportAll(withEmbeddings bool) ([]ExportRecord, error) {
	docs, err := v.enumerate()
	if err != nil {
		return nil, err
	}
	out := make([]ExportRecord, 0, len(docs))
	for _, d := range docs {
		meta := make(map[string]string, len(d.Metadata))
		for k, val := range d.Metadata {
			meta[k] = val
		}
		rec := ExportRecord{ID: d.ID, Text: d.Content, Metadata: meta}
		if withEmbeddings {
			rec.Embedding = d.Embedding
		}
		out = append(out, rec)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return createdAtString(out[i].Metadata["created_at"]) < createdAtString(out[j].Metadata["created_at"])
	})
	return out, nil
}

// importRecord adds one exported memory. Its id is kept unless taken; an
// embedding is reused only when it has the collection's width (an export
// from a different embeddings model is re-embedded instead).
func (v *vectorStore) importRecord(rec ExportRecord) (string, error) {
	if strings.TrimSpace(rec.Text) == "" {
		return "", errors.New("text is empty")
	}
	id := rec.ID
	if id != "" {
		if _, err := v.col.GetByID(context.Background(), id); err == nil {
			return "exists", nil
		}
	}
	meta := make(map[string]any, len(rec.Metadata))
	for k, val := range rec.Metadata {
		if k != "hash" {
			meta[k] = val
		}
	}
	var emb []float32
	v.mu.Lock()
	dim := v.dim
	v.mu.Unlock()
	if len(rec.Embedding) > 0 && (dim == 0 || len(rec.Embedding) == dim) {
		emb = rec.Embedding
	}
	res, err := v.add(rec.Text, meta, id, emb)
	if err != nil {
		return "", err
	}
	if res.Skipped {
		return res.Reason, nil
	}
	return "imported", nil
}

func createdAtString(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

// getByID returns a single stored memory, or nil when it does not exist.
func (v *vectorStore) getByID(id string) (*MemoryHit, error) {
	doc, err := v.col.GetByID(context.Background(), id)
	if err != nil {
		return nil, nil // chromem returns an error for unknown IDs; treat as not-found
	}
	hit := toHit(doc.ID, doc.Content, doc.Metadata)
	return &hit, nil
}

// deleteByID removes a memory from the collection and evicts its hash from the
// in-memory dedup index so an identical text can be re-added afterwards.
func (v *vectorStore) deleteByID(id string) (bool, error) {
	doc, err := v.col.GetByID(context.Background(), id)
	if err != nil {
		return false, nil // not found
	}
	v.shrink.Lock()
	err = v.col.Delete(context.Background(), nil, nil, id)
	v.shrink.Unlock()
	if err != nil {
		return false, fmt.Errorf("chromem delete: %w", err)
	}
	v.mu.Lock()
	if h, ok := doc.Metadata["hash"]; ok {
		// Only evict the hash while it still names this document; a re-add
		// of the same text may already have pointed it at a newer id.
		if v.hashes[h] == id {
			delete(v.hashes, h)
		}
	} else {
		// Fall back to a reverse scan if the doc carried no hash metadata.
		for hk, mid := range v.hashes {
			if mid == id {
				delete(v.hashes, hk)
				break
			}
		}
	}
	v.mu.Unlock()
	return true, nil
}

// createdAt parses the created_at metadata (unix seconds, may be fractional)
// into a float for sorting. Missing/invalid values sort last (0).
func createdAt(m map[string]any) float64 {
	s, ok := m["created_at"].(string)
	if !ok {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
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
