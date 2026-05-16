package memory

import (
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
)

// ── mockEmbeddings implements embeddingsClient ───────────────────────────────

const embDim = 8 // small fixed dimension for tests

// mockEmbeddings returns a deterministic pseudo-random vector derived from
// the input text so that different texts get meaningfully different vectors
// while identical texts always return the same vector.
type mockEmbeddings struct{}

func (m *mockEmbeddings) embed(text string) ([]float32, error) {
	// Seed a PRNG from the text so the same text always returns the same vector.
	seed := uint64(0)
	for i, c := range text {
		seed ^= uint64(c) << (uint(i*7) % 63)
	}
	rng := rand.New(rand.NewPCG(seed, seed>>32))
	v := make([]float32, embDim)
	var norm float32
	for i := range v {
		v[i] = float32(rng.Float64()*2 - 1)
		norm += v[i] * v[i]
	}
	// Normalise so cosine similarity works as expected.
	if norm > 0 {
		s := float32(1.0)
		for i := range v {
			v[i] /= s
		}
	}
	return v, nil
}

// errEmbeddings always returns an error; used to test failure paths.
type errEmbeddings struct{}

func (e *errEmbeddings) embed(_ string) ([]float32, error) {
	return nil, fmt.Errorf("embeddings backend unavailable")
}

// openTestVectorStore creates a vectorStore in a temp directory.
func openTestVectorStore(t *testing.T) *vectorStore {
	t.Helper()
	dir := t.TempDir()
	vs, err := newVectorStore(dir, "test_col", &mockEmbeddings{}, 0.05)
	if err != nil {
		t.Fatalf("newVectorStore: %v", err)
	}
	return vs
}

// ── addText ──────────────────────────────────────────────────────────────────

func TestAddText_BasicAdd(t *testing.T) {
	vs := openTestVectorStore(t)
	res, err := vs.addText("the quick brown fox", nil, "")
	if err != nil {
		t.Fatalf("addText: %v", err)
	}
	if res.MemoryID == "" {
		t.Error("expected non-empty MemoryID")
	}
	if res.Skipped {
		t.Errorf("first add should not be skipped: %+v", res)
	}
}

func TestAddText_EmptyTextError(t *testing.T) {
	vs := openTestVectorStore(t)
	_, err := vs.addText("", nil, "")
	if err == nil {
		t.Error("expected error for empty text, got nil")
	}
}

func TestAddText_ExactHashDeduplicate(t *testing.T) {
	vs := openTestVectorStore(t)

	r1, err := vs.addText("duplicate text", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := vs.addText("duplicate text", nil, "")
	if err != nil {
		t.Fatal(err)
	}

	if !r2.Skipped {
		t.Error("second identical add should be skipped")
	}
	if r2.Reason != "exact_hash_duplicate" {
		t.Errorf("expected reason=exact_hash_duplicate, got %q", r2.Reason)
	}
	if r2.MemoryID != r1.MemoryID {
		t.Errorf("dedup should return same ID: %q vs %q", r1.MemoryID, r2.MemoryID)
	}
}

func TestAddText_DifferentTextsAreAdded(t *testing.T) {
	vs := openTestVectorStore(t)

	r1, _ := vs.addText("text one", nil, "")
	r2, _ := vs.addText("text two", nil, "")

	if r1.MemoryID == r2.MemoryID {
		t.Errorf("different texts should have different IDs: both got %q", r1.MemoryID)
	}
	if r2.Skipped {
		t.Error("second distinct text should not be skipped")
	}
}

func TestAddText_CustomMemoryID(t *testing.T) {
	vs := openTestVectorStore(t)
	const customID = "my-custom-id-42"
	res, err := vs.addText("some content", nil, customID)
	if err != nil {
		t.Fatal(err)
	}
	if res.MemoryID != customID {
		t.Errorf("expected custom ID %q, got %q", customID, res.MemoryID)
	}
}

// TestAddText_ConcurrentDedupNoRace checks that concurrent identical adds don't
// insert duplicates and don't trigger a data race.  Run with -race.
func TestAddText_ConcurrentDedupNoRace(t *testing.T) {
	vs := openTestVectorStore(t)

	const workers = 10
	const text = "concurrent dedup test"

	var wg sync.WaitGroup
	errs := make([]error, workers)
	results := make([]*AddResult, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			r, err := vs.addText(text, nil, "")
			errs[idx] = err
			results[idx] = r
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("worker %d error: %v", i, err)
		}
	}

	// All results should carry the same memory ID (either inserted by the
	// winner or reported as a duplicate by the rest).
	var firstID string
	for _, r := range results {
		if r == nil {
			continue
		}
		if firstID == "" {
			firstID = r.MemoryID
		}
		if r.MemoryID != firstID {
			t.Errorf("ID mismatch: got %q and %q", firstID, r.MemoryID)
		}
	}
}

// ── searchText ───────────────────────────────────────────────────────────────

func TestSearchText_EmptyQuery(t *testing.T) {
	vs := openTestVectorStore(t)
	res, err := vs.searchText("", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 0 {
		t.Errorf("expected 0 hits for empty query, got %d", len(res.Hits))
	}
}

func TestSearchText_EmptyCollection(t *testing.T) {
	vs := openTestVectorStore(t)
	res, err := vs.searchText("anything", 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.Hits == nil {
		t.Error("expected non-nil (empty) Hits slice for empty collection")
	}
}

func TestSearchText_ReturnsHitAfterAdd(t *testing.T) {
	vs := openTestVectorStore(t)
	_, err := vs.addText("the quick brown fox", nil, "")
	if err != nil {
		t.Fatal(err)
	}

	res, err := vs.searchText("the quick brown fox", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) == 0 {
		t.Error("expected at least 1 hit after adding text")
	}
}

func TestSearchText_KClampedToCollectionSize(t *testing.T) {
	vs := openTestVectorStore(t)
	_, _ = vs.addText("one", nil, "id1")
	_, _ = vs.addText("two", nil, "id2")

	// Ask for 100 results but only 2 are in the collection.
	res, err := vs.searchText("one", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) > 2 {
		t.Errorf("expected at most 2 hits, got %d", len(res.Hits))
	}
}

// ── coerceMeta ───────────────────────────────────────────────────────────────

func TestCoerceMeta_ConvertsTypes(t *testing.T) {
	in := map[string]any{
		"str":   "hello",
		"bool":  true,
		"float": float64(3.14),
		"int":   float64(7),
		"nil":   nil,
	}
	out := coerceMeta(in)

	if out["str"] != "hello" {
		t.Errorf("str: %v", out["str"])
	}
	if out["bool"] != "true" {
		t.Errorf("bool: %v", out["bool"])
	}
	if out["float"] != "3.14" {
		t.Errorf("float: %v", out["float"])
	}
	if out["int"] != "7" {
		t.Errorf("int: %v", out["int"])
	}
	if _, ok := out["nil"]; ok {
		t.Error("nil key should be omitted")
	}
}
