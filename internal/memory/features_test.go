package memory

import (
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

type countingEmbeddings struct {
	inner mockEmbeddings
	calls atomic.Int32
}

func (c *countingEmbeddings) embed(text string) ([]float32, error) {
	c.calls.Add(1)
	return c.inner.embed(text)
}

// The SHA256 dedup index used to start empty on every restart, so an exact
// re-add cost an embed call and was caught only by the semantic check.
func TestDedupIndexIsRebuiltOnOpen(t *testing.T) {
	dir := t.TempDir()
	vs, err := newVectorStore(dir, "c", &mockEmbeddings{}, 0.0)
	if err != nil {
		t.Fatal(err)
	}
	first, err := vs.addText("remember the milk", nil, "")
	if err != nil {
		t.Fatal(err)
	}

	emb := &countingEmbeddings{}
	reopened, err := newVectorStore(dir, "c", emb, 0.0)
	if err != nil {
		t.Fatal(err)
	}
	res, err := reopened.addText("remember the milk", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skipped || res.Reason != "exact_hash_duplicate" || res.MemoryID != first.MemoryID {
		t.Fatalf("re-add after restart = %+v, want an exact-hash skip of %s", res, first.MemoryID)
	}
	if n := emb.calls.Load(); n != 0 {
		t.Fatalf("re-add after restart made %d embed call(s), want 0", n)
	}
}

// A collection written before the width was recorded is still indexed: the
// first add learns the width from its own embedding.
func TestIndexRebuildWithoutRecordedWidth(t *testing.T) {
	dir := t.TempDir()
	vs, _ := newVectorStore(dir, "c", &mockEmbeddings{}, 0.0)
	first, _ := vs.addText("legacy fact", nil, "")
	if err := removeFile(vs.dimPath); err != nil {
		t.Fatal(err)
	}
	saved := commonEmbedDims
	commonEmbedDims = nil // pretend the width is exotic
	defer func() { commonEmbedDims = saved }()

	reopened, err := newVectorStore(dir, "c", &mockEmbeddings{}, 0.0)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.indexed {
		t.Fatal("index claimed built with no way to enumerate")
	}
	res, err := reopened.addText("legacy fact", nil, "")
	if err != nil || res.Reason != "exact_hash_duplicate" || res.MemoryID != first.MemoryID {
		t.Fatalf("first add after open = %+v, %v", res, err)
	}
}

func TestNamespacesIsolateSearchAndDedup(t *testing.T) {
	c := newTestClient(t)
	a, _ := c.AddMemoryOpts("likes green tea", nil, AddOptions{Namespace: "user:a"})
	b, _ := c.AddMemoryOpts("likes green tea", nil, AddOptions{Namespace: "user:b"})
	if a.Skipped || b.Skipped || a.MemoryID == b.MemoryID {
		t.Fatalf("same text in two namespaces must both be stored: %+v %+v", a, b)
	}
	if _, err := c.AddMemory("the office wifi is guest-5", nil); err != nil {
		t.Fatal(err)
	}

	res, err := c.SearchMemoryIn("green tea", 10, []string{"user:a", ""})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range res.Hits {
		if h.Metadata["namespace"] == "user:b" {
			t.Fatalf("user:a's search returned user:b's memory: %v", res.Hits)
		}
	}
	if len(res.Hits) != 2 {
		t.Fatalf("hits = %d, want user:a's memory plus the shared one", len(res.Hits))
	}
	ns, _ := c.Namespaces()
	if ns["user:a"] != 1 || ns["user:b"] != 1 || ns[""] != 1 {
		t.Fatalf("namespaces = %v", ns)
	}
	only := "user:b"
	hits, total, _ := c.ListMemoriesFilter(ListFilter{Namespace: &only}, 0, 0)
	if total != 1 || hits[0].ID != b.MemoryID {
		t.Fatalf("list user:b = %v", hits)
	}
}

func TestExpiredMemoriesAreHiddenAndPurged(t *testing.T) {
	c := newTestClient(t)
	past := strconv.FormatFloat(float64(time.Now().Add(-time.Minute).Unix()), 'f', 0, 64)
	old, _ := c.AddMemory("stale deploy window", map[string]any{"expires_at": past})
	keep, _ := c.AddMemoryOpts("fresh deploy window", nil, AddOptions{TTL: time.Hour})

	if hit, _ := c.GetMemory(keep.MemoryID); hit == nil || hit.Metadata["expires_at"] == "" {
		t.Fatalf("TTL did not set expires_at: %v", hit)
	}
	res, _ := c.SearchMemory("deploy window", 10)
	for _, h := range res.Hits {
		if h.ID == old.MemoryID {
			t.Fatal("search returned an expired memory")
		}
	}
	if _, total, _ := c.ListMemories(0, 0); total != 1 {
		t.Fatalf("list total = %d, want 1 (expired hidden)", total)
	}
	if n, err := c.PurgeExpired(); err != nil || n != 1 {
		t.Fatalf("purge = %d, %v", n, err)
	}
	if hit, _ := c.GetMemory(old.MemoryID); hit != nil {
		t.Fatal("expired memory survived the purge")
	}
}

func TestDefaultTTLApplies(t *testing.T) {
	c := newTestClient(t)
	c.defaultTTL = 24 * time.Hour
	res, _ := c.AddMemory("temporary", nil)
	hit, _ := c.GetMemory(res.MemoryID)
	if hit.Metadata["expires_at"] == "" {
		t.Fatal("default TTL not applied")
	}
	forever, _ := c.AddMemoryOpts("permanent", nil, AddOptions{TTL: -1})
	hit, _ = c.GetMemory(forever.MemoryID)
	if hit.Metadata["expires_at"] != nil {
		t.Fatal("TTL<0 must mean never expire")
	}
}

func TestUpdateMemoryEditsInPlace(t *testing.T) {
	c := newTestClient(t)
	a, _ := c.AddMemory("server is in rack 4", map[string]any{"tag": "infra", "note": "x"})
	before, _ := c.GetMemory(a.MemoryID)
	text := "server moved to rack 7"
	hit, err := c.UpdateMemory(a.MemoryID, &text, map[string]any{"note": nil, "owner": "ops"})
	if err != nil || hit == nil {
		t.Fatalf("update = %v, %v", hit, err)
	}
	got, _ := c.GetMemory(a.MemoryID)
	if got.Text != text || got.Metadata["tag"] != "infra" || got.Metadata["owner"] != "ops" || got.Metadata["note"] != nil {
		t.Fatalf("after update = %+v", got)
	}
	if got.Metadata["created_at"] != before.Metadata["created_at"] || got.Metadata["updated_at"] == nil {
		t.Fatalf("created_at must survive and updated_at appear: %v", got.Metadata)
	}
	// The old text is no longer a duplicate; the new one is.
	if r, _ := c.AddMemory("server is in rack 4", nil); r.Skipped && r.Reason == "exact_hash_duplicate" {
		t.Fatal("old text still deduplicated against the edited memory")
	}
	if r, _ := c.AddMemory(text, nil); !r.Skipped {
		t.Fatal("edited text not deduplicated")
	}
	other, _ := c.AddMemory("unrelated fact", nil)
	if _, err := c.UpdateMemory(other.MemoryID, &text, nil); err == nil {
		t.Fatal("update to a duplicate text was accepted")
	}
	if hit, err := c.UpdateMemory("nope", &text, nil); hit != nil || err != nil {
		t.Fatalf("update of a missing id = %v, %v", hit, err)
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	src := newTestClient(t)
	_, _ = src.AddMemory("alpha", map[string]any{"tag": "a"})
	_, _ = src.AddMemoryOpts("beta", nil, AddOptions{Namespace: "team"})
	recs, err := src.ExportMemories(true)
	if err != nil || len(recs) != 2 || len(recs[0].Embedding) == 0 {
		t.Fatalf("export = %v, %v", recs, err)
	}

	dst := newTestClient(t)
	emb := &countingEmbeddings{}
	dst.vec.embedClient = emb
	rep := dst.ImportMemories(recs)
	if rep.Imported != 2 || rep.Failed != 0 {
		t.Fatalf("import report = %+v", rep)
	}
	if n := emb.calls.Load(); n > 1 {
		t.Fatalf("import re-embedded %d records despite carrying embeddings", n)
	}
	ns, _ := dst.Namespaces()
	if ns["team"] != 1 {
		t.Fatalf("namespace lost on import: %v", ns)
	}
	if rep := dst.ImportMemories(recs); rep.Existing != 2 || rep.Imported != 0 {
		t.Fatalf("re-import = %+v, want both reported existing", rep)
	}
	if rep := dst.ImportMemories([]ExportRecord{{Text: ""}}); rep.Failed != 1 {
		t.Fatalf("empty record = %+v", rep)
	}
}

func newTestClient(t *testing.T) *Client {
	t.Helper()
	vs, err := newVectorStore(filepath.Join(t.TempDir(), "vec"), "c", &mockEmbeddings{}, 0.0)
	if err != nil {
		t.Fatal(err)
	}
	return &Client{vec: vs}
}

func removeFile(p string) error { return os.Remove(p) }
