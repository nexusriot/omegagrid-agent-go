package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nexusriot/omegagrid-agent-go/internal/config"
	"github.com/nexusriot/omegagrid-agent-go/internal/memory"
	"github.com/nexusriot/omegagrid-agent-go/internal/skills"
)

// newEmbedMemory is newTestMemory with a working (fake) Ollama embeddings
// endpoint, so memories can actually be stored. calls counts embeddings.
func newEmbedMemory(t *testing.T) (*memory.Client, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		text := fmt.Sprint(req["input"], req["prompt"])
		calls.Add(1)
		v := make([]float64, 16)
		for _, w := range strings.Fields(strings.ToLower(text)) {
			h := fnv.New32a()
			_, _ = h.Write([]byte(w))
			v[h.Sum32()%16] += 1
		}
		v[0] += 0.01
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float64{v}})
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	mem, err := memory.New(config.Config{
		AgentDB: filepath.Join(dir, "a.sqlite3"), VectorDir: filepath.Join(dir, "v"), VectorCollection: "t",
		OllamaURL: srv.URL, OllamaEmbedModel: "e", OllamaTimeoutSec: 5, DedupDistance: 0.0001, AuditMaxBlobBytes: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	return mem, &calls
}

func TestAutoMemoryExtractionStoresFacts(t *testing.T) {
	mem, _ := newEmbedMemory(t)
	svc := &Service{
		Memory: mem,
		Chat: &mockLLM{responses: []string{
			`noise {"facts": ["User's server is web1 in Frankfurt", "  ", "User prefers metric units", "third", "fourth"]}`,
		}},
		AutoMemoryExtract:      true,
		AutoMemoryMaxFacts:     2,
		AutoMemoryMinAnswerLen: 10,
	}
	// Too short: no pass.
	svc.maybeExtractMemories(1, "ops", "q", "ok")
	svc.maybeExtractMemories(1, "ops", "where is my server?", "Your server web1 runs in Frankfurt.")

	ops := "ops"
	var hits []memory.MemoryHit
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		hits, _, _ = mem.ListMemoriesFilter(memory.ListFilter{Namespace: &ops}, 10, 0)
		if len(hits) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(hits) != 2 {
		t.Fatalf("stored %d facts, want 2 (max_facts): %+v", len(hits), hits)
	}
	for _, h := range hits {
		if h.Metadata["source"] != "auto-extract" {
			t.Fatalf("meta %v", h.Metadata)
		}
	}
}

func TestAutoMemoryOffOrFailingIsSilent(t *testing.T) {
	mem, calls := newEmbedMemory(t)
	svc := &Service{Memory: mem, Chat: &mockLLM{responses: []string{"no facts here"}}, AutoMemoryExtract: false}
	svc.maybeExtractMemories(1, "", "q", strings.Repeat("long answer ", 20))
	svc.AutoMemoryExtract = true
	svc.extractAndStoreMemories(1, "", "q", "answer") // unparseable reply: nothing stored
	time.Sleep(50 * time.Millisecond)
	if n, _, _ := mem.ListMemoriesFilter(memory.ListFilter{}, 10, 0); len(n) != 0 || calls.Load() != 0 {
		t.Fatalf("stored %d, embed calls %d", len(n), calls.Load())
	}
	if got := parseExtractedFacts(`["a", " b "]`); strings.Join(got, "|") != "a|b" {
		t.Fatalf("bare array: %v", got)
	}
}

func TestWarmToolIndexEmbedsCatalogueOnce(t *testing.T) {
	mem, calls := newEmbedMemory(t)
	svc := &Service{
		Memory: mem, Skills: newTestSkills(t), ToolTopK: 5,
		Index:        &ToolIndex{Embed: mem.Embed},
		NativeSkills: map[string]Skill{"probe": {Schema: skills.Skill{Name: "probe", Description: "probe"}}},
	}
	svc.WarmToolIndex(context.Background())
	first := calls.Load()
	list, _ := svc.Skills.List()
	if first < int64(len(list)) {
		t.Fatalf("warm embedded %d texts for %d skills", first, len(list))
	}
	// Ranking now embeds only the query.
	r, err := svc.RankTools(context.Background(), "tls certificate expiry", 3)
	if err != nil || r.Mode != "embed+lexical" {
		t.Fatalf("rank %+v %v", r, err)
	}
	if extra := calls.Load() - first; extra != 1 {
		t.Fatalf("ranking embedded %d texts, want 1 (the query)", extra)
	}

	// Retrieval off (top-k 0) or no index: warm is a no-op.
	before := calls.Load()
	(&Service{Skills: svc.Skills, Index: svc.Index}).WarmToolIndex(context.Background())
	(&Service{Skills: svc.Skills, ToolTopK: 5}).WarmToolIndex(context.Background())
	var nilIx *ToolIndex
	nilIx.Warm(context.Background(), list)
	if calls.Load() != before {
		t.Fatal("warm ran with retrieval off")
	}
}
