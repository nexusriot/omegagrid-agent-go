package httpapi

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/nexusriot/omegagrid-agent-go/internal/config"
	"github.com/nexusriot/omegagrid-agent-go/internal/memory"
)

// /health used to make a real embedding call on every hit; a healthcheck
// polling it turned into a steady stream of embed requests.
func TestHealthCachesTheEmbedProbe(t *testing.T) {
	var embeds atomic.Int32
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		embeds.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"embeddings":[[0.1,0.2,0.3]]}`))
	}))
	defer ollama.Close()

	dir := t.TempDir()
	mem, err := memory.New(config.Config{
		DataDir:           dir,
		Provider:          "ollama",
		AgentDB:           filepath.Join(dir, "agent.sqlite3"),
		VectorDir:         filepath.Join(dir, "chromem"),
		VectorCollection:  "test",
		OllamaURL:         ollama.URL,
		OllamaEmbedModel:  "nomic-embed-text",
		OllamaTimeoutSec:  2,
		DedupDistance:     0.05,
		AuditMaxBlobBytes: 65536,
	})
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })

	baseline := embeds.Load()
	for i := 0; i < 5; i++ {
		if err := cachedEmbedHealth(mem); err != nil {
			t.Fatalf("probe %d: %v", i, err)
		}
	}
	if n := embeds.Load() - baseline; n != 1 {
		t.Fatalf("5 health probes made %d embed calls, want 1 (cached)", n)
	}

	// Once the cache entry expires the next probe hits the backend again.
	v, _ := embedProbes.Load(mem)
	p := v.(*embedProbe)
	p.mu.Lock()
	p.at = p.at.Add(-2 * embedProbeTTL)
	p.mu.Unlock()
	if err := cachedEmbedHealth(mem); err != nil {
		t.Fatalf("probe after expiry: %v", err)
	}
	if n := embeds.Load() - baseline; n != 2 {
		t.Fatalf("expired cache made %d total embed calls, want 2", n)
	}
}
