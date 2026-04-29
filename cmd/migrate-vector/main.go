// migrate-vector imports a JSONL dump produced by ./export.py into a
// chromem-go persistent database.
//
// The export side preserves embeddings verbatim, so this tool never calls
// out to Ollama/OpenAI; the noop embedding function below would fail loudly
// if an embedding were ever needed during import (it isn't).
//
// Usage:
//
//	# 1. From inside the sidecar container (or any env with chromadb==0.5.5):
//	python3 cmd/migrate-vector/export.py \
//	    --persist data/vector_db \
//	    --collection memories \
//	    --out data/vector_db.jsonl
//
//	# 2. From the host:
//	go run ./cmd/migrate-vector \
//	    --in data/vector_db.jsonl \
//	    --db data/chromem \
//	    --collection memories
//
// Run with --dry-run first to validate the JSONL without writing anything.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"

	"github.com/philippgille/chromem-go"
)

type record struct {
	ID        string         `json:"id"`
	Document  string         `json:"document"`
	Metadata  map[string]any `json:"metadata"`
	Embedding []float32      `json:"embedding"`
}

// chromem-go stores metadata as map[string]string. Chroma supports
// str|int|float|bool|None, so coerce. The Python sidecar already does
// the same thing for non-primitives via VectorStore._sanitize_meta.
func coerceMetadata(m map[string]any) map[string]string {
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
			b, err := json.Marshal(v)
			if err != nil {
				out[k] = fmt.Sprintf("%v", v)
			} else {
				out[k] = string(b)
			}
		}
	}
	return out
}

func noopEmbed(_ context.Context, _ string) ([]float32, error) {
	return nil, errors.New("embedding func not configured: import does not need to embed")
}

func parseJSONL(path string) ([]chromem.Document, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 1<<20), 64<<20)

	var docs []chromem.Document
	dim := 0
	line := 0
	for scan.Scan() {
		line++
		raw := scan.Bytes()
		if len(raw) == 0 {
			continue
		}
		var r record
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, 0, fmt.Errorf("line %d: %w", line, err)
		}
		if r.ID == "" {
			return nil, 0, fmt.Errorf("line %d: missing id", line)
		}
		if len(r.Embedding) == 0 {
			return nil, 0, fmt.Errorf("line %d: empty embedding for id=%s", line, r.ID)
		}
		if dim == 0 {
			dim = len(r.Embedding)
		} else if len(r.Embedding) != dim {
			return nil, 0, fmt.Errorf("line %d: embedding dim mismatch (got %d, expected %d)", line, len(r.Embedding), dim)
		}
		docs = append(docs, chromem.Document{
			ID:        r.ID,
			Metadata:  coerceMetadata(r.Metadata),
			Embedding: r.Embedding,
			Content:   r.Document,
		})
	}
	if err := scan.Err(); err != nil {
		return nil, 0, err
	}
	return docs, dim, nil
}

func main() {
	in := flag.String("in", "", "JSONL produced by export.py (required)")
	dst := flag.String("db", "data/chromem", "destination chromem-go persist directory")
	colName := flag.String("collection", "memories", "collection name")
	concurrency := flag.Int("concurrency", 4, "AddDocuments concurrency")
	dryRun := flag.Bool("dry-run", false, "parse & validate JSONL, do not write")
	flag.Parse()

	if *in == "" {
		log.Fatal("--in is required")
	}

	docs, dim, err := parseJSONL(*in)
	if err != nil {
		log.Fatalf("parse: %v", err)
	}
	fmt.Printf("parsed %d records, embedding dim=%d\n", len(docs), dim)

	if *dryRun || len(docs) == 0 {
		if len(docs) == 0 {
			fmt.Println("source collection is empty — nothing to import")
		}
		return
	}

	if err := os.MkdirAll(*dst, 0o755); err != nil {
		log.Fatalf("mkdir %s: %v", *dst, err)
	}

	db, err := chromem.NewPersistentDB(*dst, false)
	if err != nil {
		log.Fatalf("open chromem db at %s: %v", *dst, err)
	}

	collection, err := db.GetOrCreateCollection(
		*colName,
		map[string]string{"hnsw:space": "cosine"},
		noopEmbed,
	)
	if err != nil {
		log.Fatalf("create collection: %v", err)
	}

	if existing := collection.Count(); existing > 0 {
		log.Fatalf("collection %q at %s already has %d records; refusing to merge. Remove the directory and re-run.", *colName, *dst, existing)
	}

	ctx := context.Background()
	if err := collection.AddDocuments(ctx, docs, *concurrency); err != nil {
		log.Fatalf("add documents: %v", err)
	}

	fmt.Printf("imported %d records into %s/%s (dim=%d)\n", len(docs), *dst, *colName, dim)
}
