package bookmark

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/nexusriot/omegagrid-agent-go/internal/memory"
)

// fakeStore keeps memories in a slice; search matches by substring.
type fakeStore struct {
	hits []memory.MemoryHit
	n    int
}

func (f *fakeStore) AddMemoryOpts(text string, meta map[string]any, _ memory.AddOptions) (*memory.AddResult, error) {
	f.n++
	id := fmt.Sprint(f.n)
	f.hits = append(f.hits, memory.MemoryHit{ID: id, Text: text, Metadata: meta})
	return &memory.AddResult{MemoryID: id}, nil
}

func (f *fakeStore) SearchMemoryIn(q string, k int, _ []string) (*memory.SearchResult, error) {
	var out []memory.MemoryHit
	for _, h := range f.hits {
		if strings.Contains(strings.ToLower(h.Text), strings.ToLower(q)) {
			out = append(out, h)
		}
	}
	// A non-bookmark memory competes for the slots.
	out = append(out, memory.MemoryHit{ID: "x", Text: q, Metadata: map[string]any{"type": "note"}})
	return &memory.SearchResult{Hits: out}, nil
}

func (f *fakeStore) ListMemoriesFilter(memory.ListFilter, int, int) ([]memory.MemoryHit, int, error) {
	return f.hits, len(f.hits), nil
}

func (f *fakeStore) DeleteMemory(id string) (bool, error) {
	for i, h := range f.hits {
		if h.ID == id {
			f.hits = append(f.hits[:i], f.hits[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func TestBookmarkLifecycle(t *testing.T) {
	st := &fakeStore{}
	s := &Skill{Store: st, Fetch: func(_ context.Context, u string) (string, string, string, error) {
		return "Go generics explained", "A tour of type parameters", "", nil
	}}
	ctx := context.Background()
	r := s.Execute(ctx, map[string]any{"action": "add", "url": "Go.dev/blog/generics#top", "tags": []any{"Go", "#lang"}, "summary": "Type parameters intro"}).(map[string]any)
	if r["saved"] != true || r["url"] != "https://go.dev/blog/generics" {
		t.Fatalf("add: %v", r)
	}
	if !strings.Contains(st.hits[0].Text, "About: Type parameters intro") || st.hits[0].Metadata["tags"] != ",go,lang," {
		t.Fatalf("stored: %+v", st.hits[0])
	}
	// Re-adding replaces rather than duplicates.
	r = s.Execute(ctx, map[string]any{"action": "add", "url": "https://go.dev/blog/generics"}).(map[string]any)
	if r["replaced"] != 1 || len(st.hits) != 1 {
		t.Fatalf("re-add: %v (%d stored)", r, len(st.hits))
	}
	if !strings.Contains(st.hits[0].Text, "A tour of type parameters") {
		t.Fatal("description not used when no summary")
	}
	sr := s.Execute(ctx, map[string]any{"action": "search", "query": "generics"}).(map[string]any)
	if sr["count"] != 1 {
		t.Fatalf("search: %v", sr)
	}
	l := s.Execute(ctx, map[string]any{"action": "list", "tags": "nope"}).(map[string]any)
	if l["count"] != 0 {
		t.Fatalf("tag filter: %v", l)
	}
	d := s.Execute(ctx, map[string]any{"action": "delete", "url": "https://go.dev/blog/generics"}).(map[string]any)
	if d["deleted"] != 1 || len(st.hits) != 0 {
		t.Fatalf("delete: %v", d)
	}
	if e := s.Execute(ctx, map[string]any{"action": "add", "url": "ftp://x"}).(map[string]any); e["error"] == nil {
		t.Fatal("ftp accepted")
	}
}
