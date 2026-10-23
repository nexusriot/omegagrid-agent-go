package httpapi

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/nexusriot/omegagrid-agent-go/internal/memory"
)

// Each /health hit used to make a real embedding call, so a docker
// healthcheck or uptime monitor polling every few seconds turned into a
// steady stream of paid (or GPU-bound) embed requests, and a slow backend made
// /health itself hang. The probe result is now cached per memory client and
// the probe is bounded.
const (
	embedProbeTTL     = 30 * time.Second
	embedProbeTimeout = 5 * time.Second
)

var errEmbedProbeTimeout = errors.New("embedding probe timed out")

type embedProbe struct {
	mu       sync.Mutex
	at       time.Time
	err      error
	inflight chan struct{} // non-nil while a probe runs; closed when it ends
}

var embedProbes sync.Map // *memory.Client -> *embedProbe

// cachedEmbedHealth returns the embed backend's health, probing at most once
// per embedProbeTTL. Concurrent callers share one in-flight probe, and none
// waits longer than embedProbeTimeout — a probe that overruns keeps going in
// the background and its result is cached when it lands.
func cachedEmbedHealth(m *memory.Client) error {
	v, _ := embedProbes.LoadOrStore(m, &embedProbe{})
	p := v.(*embedProbe)

	p.mu.Lock()
	if !p.at.IsZero() && time.Since(p.at) < embedProbeTTL {
		err := p.err
		p.mu.Unlock()
		return err
	}
	ch := p.inflight
	if ch == nil {
		ch = make(chan struct{})
		p.inflight = ch
		go func() {
			err := m.EmbedHealthy()
			p.mu.Lock()
			p.at, p.err, p.inflight = time.Now(), err, nil
			p.mu.Unlock()
			close(ch)
		}()
	}
	p.mu.Unlock()

	select {
	case <-ch:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.err
	case <-time.After(embedProbeTimeout):
		return errEmbedProbeTimeout
	}
}

func (d *Deps) handleHealth(w http.ResponseWriter, _ *http.Request) {
	embedErr := cachedEmbedHealth(d.Memory)
	embedOK := embedErr == nil
	embedErrStr := ""
	if !embedOK {
		embedErrStr = embedErr.Error()
	}
	// Mirrors memory.buildEmbeddings: EMBED_PROVIDER wins, else the chat provider.
	embedProvider := d.Cfg.EmbedProvider
	if embedProvider == "" {
		embedProvider = d.Cfg.Provider
	}
	embedModel := d.Cfg.OllamaEmbedModel
	switch embedProvider {
	case "openai", "openai-codex", "codex":
		embedModel = d.Cfg.OpenAIEmbedModel
	case "digitalocean", "do":
		embedModel = d.Cfg.DigitalOceanEmbedModel
	default:
		// Everything else — including opencode, which serves no embeddings —
		// falls back to Ollama, so report the backend actually in use rather
		// than the chat provider's name.
		embedProvider = "ollama"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             embedOK, // false when vector memory is broken
		"provider":       d.Cfg.Provider,
		"chat_base":      d.Chat.BaseURL(),
		"chat_model":     d.Chat.Model(),
		"skills_dir":     d.Cfg.SkillsDir,
		"scheduler_db":   d.Cfg.SchedulerDB,
		"embed_model":    embedModel,
		"embed_provider": embedProvider,
		"embed_ok":       embedOK,
		"embed_error":    embedErrStr,
	})
}
