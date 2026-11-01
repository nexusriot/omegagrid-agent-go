// Package memory provides in-process history (SQLite) and semantic vector
// (chromem-go) stores.  The public API is unchanged from the former HTTP-based
// client so all callers compile without modification.
package memory

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nexusriot/omegagrid-agent-go/internal/config"
)

// Client wraps the in-process history and vector stores.
type Client struct {
	hist         *historyStore
	vec          *vectorStore
	auditMaxBlob int
	defaultTTL   time.Duration
}

// New initialises SQLite history and chromem-go vector stores.
func New(cfg config.Config) (*Client, error) {
	hist, err := newHistoryStore(cfg.AgentDB)
	if err != nil {
		return nil, fmt.Errorf("history store: %w", err)
	}

	embed, err := buildEmbeddings(cfg)
	if err != nil {
		_ = hist.close()
		return nil, fmt.Errorf("embeddings client: %w", err)
	}

	vec, err := newVectorStore(cfg.VectorDir, cfg.VectorCollection, embed, cfg.DedupDistance)
	if err != nil {
		_ = hist.close()
		return nil, fmt.Errorf("vector store: %w", err)
	}

	return &Client{
		hist: hist, vec: vec, auditMaxBlob: cfg.AuditMaxBlobBytes,
		defaultTTL: time.Duration(cfg.MemoryDefaultTTLDays * float64(24*time.Hour)),
	}, nil
}

// buildEmbeddings picks the embeddings backend. It keys off EmbedProvider
// (EMBED_PROVIDER), which defaults to the chat provider — the split matters
// for chat providers that serve no embeddings of their own.
func buildEmbeddings(cfg config.Config) (embeddingsClient, error) {
	provider := strings.ToLower(cfg.EmbedProvider)
	if provider == "" {
		provider = strings.ToLower(cfg.Provider)
	}
	switch provider {
	case "openai", "openai-codex", "codex":
		if cfg.OpenAIAPIKey == "" {
			return nil, fmt.Errorf("OPENAI_API_KEY required for openai embeddings")
		}
		return newOpenAIEmbeddings(cfg.OpenAIBaseURL, cfg.OpenAIAPIKey, cfg.OpenAIEmbedModel, cfg.OpenAITimeoutSec), nil
	case "digitalocean", "do":
		if cfg.DigitalOceanAPIKey == "" {
			return nil, fmt.Errorf("DIGITALOCEAN_API_KEY required for digitalocean embeddings")
		}
		return newOpenAIEmbeddings(cfg.DigitalOceanBaseURL, cfg.DigitalOceanAPIKey, cfg.DigitalOceanEmbedModel, cfg.DigitalOceanTimeoutSec), nil
	case "opencode", "opencode-go", "opencode-zen", "zen":
		// The relay has no /embeddings endpoint (it answers the marketing page),
		// so vector memory falls back to Ollama. Set EMBED_PROVIDER=openai (or
		// digitalocean) with that provider's key to embed in the cloud instead.
		return newOllamaEmbeddings(cfg.OllamaURL, cfg.OllamaEmbedModel, cfg.OllamaTimeoutSec), nil
	default:
		return newOllamaEmbeddings(cfg.OllamaURL, cfg.OllamaEmbedModel, cfg.OllamaTimeoutSec), nil
	}
}

// Close releases database handles.
func (c *Client) Close() error { return c.hist.close() }

// EmbedHealthy returns nil when the configured embeddings backend responds
// correctly, or the error if it is unreachable / the model is not loaded.
// Safe to call on every /health poll — the probe embeds a single short word.
func (c *Client) EmbedHealthy() error { return c.vec.probeEmbed() }

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type SessionInfo struct {
	ID           int     `json:"id"`
	CreatedAt    float64 `json:"created_at"`
	MessageCount int     `json:"message_count"`
	Title        string  `json:"title"`
	Preview      string  `json:"preview"` // first user message, shortened
	LastAt       float64 `json:"last_at"`
}

type StoredMessage struct {
	ID        int     `json:"id"`
	SessionID int     `json:"session_id"`
	TS        float64 `json:"ts"`
	Role      string  `json:"role"`
	Content   string  `json:"content"`
}

func (c *Client) CreateSession() (int, error)                   { return c.hist.createSession() }
func (c *Client) ListSessions(limit int) ([]SessionInfo, error) { return c.hist.listSessions(limit) }

// SearchSessions lists sessions whose title or messages contain query.
func (c *Client) SearchSessions(limit int, query string) ([]SessionInfo, error) {
	return c.hist.searchSessions(limit, query)
}

// SearchMessages finds past user/assistant messages containing every word
// of query, newest first; excludeSession (0 = none) is left out.
func (c *Client) SearchMessages(query string, limit, excludeSession int) ([]MessageHit, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	return c.hist.searchMessages(query, limit, excludeSession)
}

// RenameSession sets a session title; false when the session does not exist.
func (c *Client) RenameSession(id int, title string) (bool, error) {
	return c.hist.renameSession(id, title)
}

// DeleteSession removes a session and its messages (the audit log stays).
func (c *Client) DeleteSession(id int) (bool, error) { return c.hist.deleteSession(id) }

// Usage reports activity over the last `days` days, bucketed by hour for a
// window of 2 days or less and by day otherwise.
func (c *Client) Usage(days int) (*Usage, error) {
	if days <= 0 {
		days = 7
	}
	if days > 90 {
		days = 90
	}
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	if days <= 2 {
		return c.hist.usage(since, 3600, "hour")
	}
	return c.hist.usage(since, 86400, "day")
}
func (c *Client) ListMessages(sessionID, limit, offset int) ([]StoredMessage, error) {
	return c.hist.listMessages(sessionID, limit, offset)
}
func (c *Client) LoadTail(sessionID, limit int) ([]Message, error) {
	return c.hist.loadTail(sessionID, limit)
}
func (c *Client) AddMessage(sessionID int, role string, content any) error {
	return c.hist.addMessage(sessionID, role, content)
}

type MemoryHit struct {
	ID       string         `json:"id"`
	Text     string         `json:"text"`
	Metadata map[string]any `json:"metadata"`
	Distance float64        `json:"distance,omitempty"`
}

// ExportRecord is one memory in an export file (JSON Lines, one per line).
type ExportRecord struct {
	ID        string            `json:"id"`
	Text      string            `json:"text"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Embedding []float32         `json:"embedding,omitempty"`
}

// ImportReport counts what an import did with each record.
type ImportReport struct {
	Imported   int      `json:"imported"`
	Duplicates int      `json:"duplicates"`
	Existing   int      `json:"existing"`
	Failed     int      `json:"failed"`
	Errors     []string `json:"errors,omitempty"`
}

// AddOptions are the namespaced / expiring variants of AddMemory.
type AddOptions struct {
	Namespace string        // "" is the default namespace
	TTL       time.Duration // 0 = Client default (MEMORY_DEFAULT_TTL_DAYS), <0 = never
}

type AddResult struct {
	MemoryID string `json:"memory_id"`
	Skipped  bool   `json:"skipped"`
	Reason   string `json:"reason"`
}

type SearchResult struct {
	Hits    []MemoryHit        `json:"hits"`
	Timings map[string]float64 `json:"timings,omitempty"`
}

// AddInvocation persists one skill/tool call to the audit log.
// It is a no-op when AUDIT_MAX_BLOB_BYTES=0.
func (c *Client) AddInvocation(r AuditRecord) error {
	if c.auditMaxBlob == 0 {
		return nil
	}
	return c.hist.recordInvocation(r, c.auditMaxBlob)
}

// GetInvocation returns one audit record by ID, or nil if not found.
func (c *Client) GetInvocation(id int64) (*AuditRecord, error) {
	return c.hist.getInvocation(id)
}

// ListInvocations returns a filtered page of audit records and the total count.
func (c *Client) ListInvocations(f AuditFilter) ([]AuditRecord, int, error) {
	return c.hist.listInvocations(f)
}

func (c *Client) AddMemory(text string, meta map[string]any) (*AddResult, error) {
	return c.AddMemoryOpts(text, meta, AddOptions{Namespace: metaString(meta, "namespace")})
}

// AddMemoryOpts stores a memory in a namespace with an optional lifetime.
// An explicit expires_at in meta wins over the TTL.
func (c *Client) AddMemoryOpts(text string, meta map[string]any, opts AddOptions) (*AddResult, error) {
	m := make(map[string]any, len(meta)+2)
	for k, v := range meta {
		m[k] = v
	}
	if opts.Namespace != "" {
		m["namespace"] = opts.Namespace
	}
	ttl := opts.TTL
	if ttl == 0 {
		ttl = c.defaultTTL
	}
	if _, set := m["expires_at"]; !set && ttl > 0 {
		m["expires_at"] = strconv.FormatFloat(float64(time.Now().Add(ttl).UnixMilli())/1000.0, 'f', 3, 64)
	}
	return c.vec.addText(text, m, "")
}

func metaString(meta map[string]any, key string) string {
	s, _ := meta[key].(string)
	return s
}

func (c *Client) SearchMemory(query string, k int) (*SearchResult, error) {
	return c.vec.searchText(query, k)
}

// SearchMemoryIn searches only the given namespaces ("" is the default one).
func (c *Client) SearchMemoryIn(query string, k int, namespaces []string) (*SearchResult, error) {
	return c.vec.searchIn(query, k, namespaces)
}

// ListMemoriesFilter is ListMemories narrowed by namespace / expiry.
func (c *Client) ListMemoriesFilter(f ListFilter, limit, offset int) ([]MemoryHit, int, error) {
	return c.vec.list(f, limit, offset)
}

// UpdateMemory edits a memory in place; nil when it does not exist.
func (c *Client) UpdateMemory(id string, text *string, meta map[string]any) (*MemoryHit, error) {
	return c.vec.update(id, text, meta)
}

// Namespaces counts live memories per namespace.
func (c *Client) Namespaces() (map[string]int, error) { return c.vec.namespaces() }

// PurgeExpired deletes memories whose expires_at has passed.
func (c *Client) PurgeExpired() (int, error) { return c.vec.purgeExpired(time.Now()) }

// RunJanitor purges expired memories every interval until ctx is done.
func (c *Client) RunJanitor(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if n, err := c.PurgeExpired(); err != nil {
			log.Printf("memory janitor: %v", err)
		} else if n > 0 {
			log.Printf("memory janitor: removed %d expired memor(ies)", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ExportMemories returns every memory, oldest first.
func (c *Client) ExportMemories(withEmbeddings bool) ([]ExportRecord, error) {
	return c.vec.exportAll(withEmbeddings)
}

// ImportMemories adds exported records, deduplicating like any other add.
func (c *Client) ImportMemories(recs []ExportRecord) ImportReport {
	var rep ImportReport
	for i, r := range recs {
		outcome, err := c.vec.importRecord(r)
		switch {
		case err != nil:
			rep.Failed++
			if len(rep.Errors) < 20 {
				rep.Errors = append(rep.Errors, fmt.Sprintf("record %d: %v", i+1, err))
			}
		case outcome == "imported":
			rep.Imported++
		case outcome == "exists":
			rep.Existing++
		default:
			rep.Duplicates++
		}
	}
	return rep
}

// ListMemories returns a newest-first page of stored memories and the total
// count. limit<=0 returns all memories from the given offset.
func (c *Client) ListMemories(limit, offset int) ([]MemoryHit, int, error) {
	return c.vec.listAll(limit, offset)
}

// GetMemory returns one stored memory by ID, or nil when it does not exist.
func (c *Client) GetMemory(id string) (*MemoryHit, error) {
	return c.vec.getByID(id)
}

// DeleteMemory removes a memory by ID. The bool reports whether it existed.
func (c *Client) DeleteMemory(id string) (bool, error) {
	return c.vec.deleteByID(id)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Back off to a rune boundary: the result lands in JSON error bodies and
	// Telegram messages, which reject or mangle a split UTF-8 sequence.
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
