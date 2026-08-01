package memory

import (
	"fmt"
	"strconv"
	"testing"

	chromem "github.com/philippgille/chromem-go"
)

// chromemDoc builds a document with no "hash" metadata, mimicking a row written
// before the dedup index existed.
func chromemDoc(id, text string, emb []float32) chromem.Document {
	return chromem.Document{
		ID:        id,
		Metadata:  map[string]string{"created_at": "100.000"},
		Embedding: emb,
		Content:   text,
	}
}

func TestListAllEmptyCollection(t *testing.T) {
	vs := openTestVectorStore(t)
	hits, total, err := vs.listAll(100, 0)
	if err != nil {
		t.Fatalf("listAll: %v", err)
	}
	if total != 0 || len(hits) != 0 {
		t.Fatalf("listAll on an empty store = %d hits / total %d", len(hits), total)
	}
	// Must serialise as [] rather than null for the UI.
	if hits == nil {
		t.Fatal("listAll returned a nil slice; the API would emit null")
	}
}

// chromem-go has no list primitive, so listAll probes and sorts by the
// created_at metadata. Newest first is what the memory page renders.
func TestListAllSortsNewestFirst(t *testing.T) {
	vs := openTestVectorStore(t)
	// created_at is supplied explicitly (unix seconds) so ordering is exact
	// rather than dependent on insertion timing.
	for i, ts := range []string{"100.000", "300.000", "200.000"} {
		if _, err := vs.addText(fmt.Sprintf("memory %d", i), map[string]any{"created_at": ts}, ""); err != nil {
			t.Fatalf("addText: %v", err)
		}
	}

	hits, total, err := vs.listAll(0, 0)
	if err != nil {
		t.Fatalf("listAll: %v", err)
	}
	if total != 3 || len(hits) != 3 {
		t.Fatalf("listAll = %d hits / total %d, want 3/3", len(hits), total)
	}
	want := []string{"300.000", "200.000", "100.000"}
	for i, w := range want {
		if got := hits[i].Metadata["created_at"]; got != w {
			t.Fatalf("hit %d created_at = %v, want %v (order: %v)", i, got, w, hits)
		}
	}
}

func TestListAllPagination(t *testing.T) {
	vs := openTestVectorStore(t)
	for i := 0; i < 5; i++ {
		ts := strconv.Itoa(100+i) + ".000"
		if _, err := vs.addText(fmt.Sprintf("m%d", i), map[string]any{"created_at": ts}, ""); err != nil {
			t.Fatalf("addText: %v", err)
		}
	}

	// limit<=0 means "everything from offset".
	hits, total, err := vs.listAll(0, 0)
	if err != nil || len(hits) != 5 || total != 5 {
		t.Fatalf("listAll(0,0) = %d/%d, %v", len(hits), total, err)
	}

	hits, total, err = vs.listAll(2, 0)
	if err != nil {
		t.Fatalf("listAll: %v", err)
	}
	if len(hits) != 2 || total != 5 {
		t.Fatalf("listAll(2,0) = %d hits / total %d, want 2/5", len(hits), total)
	}
	first := hits[0].ID

	// The offset page must not repeat the first page.
	hits, _, err = vs.listAll(2, 2)
	if err != nil {
		t.Fatalf("listAll: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("listAll(2,2) = %d hits, want 2", len(hits))
	}
	if hits[0].ID == first {
		t.Fatal("offset page repeated the first result")
	}

	// An offset past the end is empty but still reports the true total.
	hits, total, err = vs.listAll(2, 99)
	if err != nil {
		t.Fatalf("listAll: %v", err)
	}
	if len(hits) != 0 || total != 5 {
		t.Fatalf("listAll(2,99) = %d hits / total %d, want 0/5", len(hits), total)
	}

	// A negative offset is clamped rather than panicking on a bad slice bound.
	hits, _, err = vs.listAll(2, -5)
	if err != nil {
		t.Fatalf("listAll(2,-5): %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("listAll(2,-5) = %d hits, want 2", len(hits))
	}
}

func TestListAllEmbeddingsFailure(t *testing.T) {
	vs := openTestVectorStore(t)
	if _, err := vs.addText("something", nil, ""); err != nil {
		t.Fatalf("addText: %v", err)
	}
	// The probe query needs the embeddings backend; its failure must surface.
	vs.embedClient = &errEmbeddings{}
	if _, _, err := vs.listAll(10, 0); err == nil {
		t.Fatal("expected an error when the embeddings backend is down")
	}
}

func TestGetByIDRoundTrip(t *testing.T) {
	vs := openTestVectorStore(t)
	res, err := vs.addText("a durable fact", map[string]any{"tag": "preference"}, "")
	if err != nil {
		t.Fatalf("addText: %v", err)
	}

	hit, err := vs.getByID(res.MemoryID)
	if err != nil {
		t.Fatalf("getByID: %v", err)
	}
	if hit == nil {
		t.Fatal("getByID returned nil for a stored memory")
	}
	if hit.Text != "a durable fact" {
		t.Fatalf("text = %q", hit.Text)
	}
	if hit.Metadata["tag"] != "preference" {
		t.Fatalf("metadata = %v", hit.Metadata)
	}
	if hit.Metadata["hash"] == nil {
		t.Fatal("stored memory carries no hash metadata")
	}
}

// chromem returns an error for unknown IDs; the store must translate that into
// a not-found (nil, nil) so the API answers 404 instead of 500.
func TestGetByIDUnknown(t *testing.T) {
	vs := openTestVectorStore(t)
	hit, err := vs.getByID("does-not-exist")
	if err != nil {
		t.Fatalf("getByID returned an error for an unknown id: %v", err)
	}
	if hit != nil {
		t.Fatalf("getByID = %v, want nil", hit)
	}
}

func TestDeleteByID(t *testing.T) {
	vs := openTestVectorStore(t)
	res, err := vs.addText("delete me", nil, "")
	if err != nil {
		t.Fatalf("addText: %v", err)
	}

	existed, err := vs.deleteByID(res.MemoryID)
	if err != nil {
		t.Fatalf("deleteByID: %v", err)
	}
	if !existed {
		t.Fatal("deleteByID reported the memory as missing")
	}
	if hit, _ := vs.getByID(res.MemoryID); hit != nil {
		t.Fatal("memory still present after delete")
	}

	// Deleting twice is a not-found, not an error.
	existed, err = vs.deleteByID(res.MemoryID)
	if err != nil {
		t.Fatalf("second deleteByID: %v", err)
	}
	if existed {
		t.Fatal("second delete reported success")
	}
}

// Deleting must evict the SHA256 entry, or re-adding the same text would be
// rejected as an exact duplicate of a memory that no longer exists.
func TestDeleteByIDEvictsHashSoTextCanBeReAdded(t *testing.T) {
	vs := openTestVectorStore(t)
	const text = "vlad prefers tabs"

	first, err := vs.addText(text, nil, "")
	if err != nil {
		t.Fatalf("addText: %v", err)
	}
	if _, err := vs.deleteByID(first.MemoryID); err != nil {
		t.Fatalf("deleteByID: %v", err)
	}
	if len(vs.hashes) != 0 {
		t.Fatalf("dedup index still holds %d entries after delete", len(vs.hashes))
	}

	second, err := vs.addText(text, nil, "")
	if err != nil {
		t.Fatalf("re-addText: %v", err)
	}
	if second.Skipped {
		t.Fatalf("re-add was rejected as %q after the original was deleted", second.Reason)
	}
}

// A document written before hash metadata existed (e.g. imported by
// migrate-vector) still has to be evictable from the dedup index, via the
// reverse-scan fallback.
func TestDeleteByIDFallsBackToReverseScan(t *testing.T) {
	vs := openTestVectorStore(t)
	const text = "imported without a hash"

	emb, err := vs.embedClient.embed(text)
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	// Store the document directly, with no "hash" metadata at all...
	if err := vs.col.AddDocument(t.Context(), chromemDoc("legacy-id", text, emb)); err != nil {
		t.Fatalf("AddDocument: %v", err)
	}
	// ...while the in-memory index does know its hash.
	vs.hashes[sha256Text(text)] = "legacy-id"

	existed, err := vs.deleteByID("legacy-id")
	if err != nil {
		t.Fatalf("deleteByID: %v", err)
	}
	if !existed {
		t.Fatal("deleteByID reported the legacy document as missing")
	}
	if len(vs.hashes) != 0 {
		t.Fatalf("reverse scan did not evict the hash: %v", vs.hashes)
	}

	// And the text can be stored again afterwards.
	res, err := vs.addText(text, nil, "")
	if err != nil {
		t.Fatalf("addText: %v", err)
	}
	if res.Skipped {
		t.Fatalf("re-add rejected as %q", res.Reason)
	}
}

func TestProbeEmbedReportsBackendHealth(t *testing.T) {
	vs := openTestVectorStore(t)
	if err := vs.probeEmbed(); err != nil {
		t.Fatalf("probeEmbed with a working backend: %v", err)
	}
	vs.embedClient = &errEmbeddings{}
	if err := vs.probeEmbed(); err == nil {
		t.Fatal("probeEmbed did not report a broken backend")
	}
}

func TestAddTextEmbeddingsFailure(t *testing.T) {
	vs := openTestVectorStore(t)
	vs.embedClient = &errEmbeddings{}
	if _, err := vs.addText("x", nil, ""); err == nil {
		t.Fatal("expected an error when embeddings are unavailable")
	}
}

func TestSearchTextEmbeddingsFailure(t *testing.T) {
	vs := openTestVectorStore(t)
	if _, err := vs.addText("something", nil, ""); err != nil {
		t.Fatalf("addText: %v", err)
	}
	vs.embedClient = &errEmbeddings{}
	if _, err := vs.searchText("query", 5); err == nil {
		t.Fatal("expected an error when embeddings are unavailable")
	}
}

func TestCreatedAtParsing(t *testing.T) {
	if got := createdAt(map[string]any{"created_at": "1234.500"}); got != 1234.5 {
		t.Fatalf("createdAt = %v, want 1234.5", got)
	}
	// Missing or unparseable values sort last rather than blowing up.
	if got := createdAt(map[string]any{}); got != 0 {
		t.Fatalf("createdAt(missing) = %v, want 0", got)
	}
	if got := createdAt(map[string]any{"created_at": "not a number"}); got != 0 {
		t.Fatalf("createdAt(garbage) = %v, want 0", got)
	}
	if got := createdAt(map[string]any{"created_at": 1234.5}); got != 0 {
		t.Fatalf("createdAt(non-string) = %v, want 0", got)
	}
}

func TestSha256TextIsStable(t *testing.T) {
	a := sha256Text("hello")
	if a != sha256Text("hello") {
		t.Fatal("sha256Text is not deterministic")
	}
	if a == sha256Text("hello ") {
		t.Fatal("sha256Text collided on differing text")
	}
	if len(a) != 64 {
		t.Fatalf("hash length = %d, want 64 hex chars", len(a))
	}
}

// addText stamps created_at when the caller omits it, which is what listAll
// sorts on — without it every memory would sort as "oldest".
func TestAddTextStampsCreatedAt(t *testing.T) {
	vs := openTestVectorStore(t)
	res, err := vs.addText("stamp me", map[string]any{"tag": "x"}, "")
	if err != nil {
		t.Fatalf("addText: %v", err)
	}
	hit, err := vs.getByID(res.MemoryID)
	if err != nil || hit == nil {
		t.Fatalf("getByID: %v", err)
	}
	ts, _ := hit.Metadata["created_at"].(string)
	if ts == "" {
		t.Fatalf("created_at not stamped: %v", hit.Metadata)
	}
	if createdAt(hit.Metadata) <= 0 {
		t.Fatalf("created_at %q does not parse as a timestamp", ts)
	}
}
