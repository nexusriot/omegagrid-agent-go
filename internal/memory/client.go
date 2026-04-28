// Package memory provides in-process history (SQLite) and semantic vector
// (chromem-go) stores.  The public API is unchanged from the former HTTP-based
// client so all callers compile without modification.
package memory

import (
	"fmt"
	"strings"

	"github.com/nexusriot/omegagrid-agent-go/internal/config"
)

// Client wraps the in-process history and vector stores.
type Client struct {
	hist *historyStore
	vec  *vectorStore
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

	return &Client{hist: hist, vec: vec}, nil
}

func buildEmbeddings(cfg config.Config) (embeddingsClient, error) {
	switch strings.ToLower(cfg.Provider) {
	case "openai", "openai-codex", "codex":
		if cfg.OpenAIAPIKey == "" {
			return nil, fmt.Errorf("OPENAI_API_KEY required for openai embeddings")
		}
		return newOpenAIEmbeddings(cfg.OpenAIBaseURL, cfg.OpenAIAPIKey, cfg.OpenAIEmbedModel, cfg.OpenAITimeoutSec), nil
	default:
		return newOllamaEmbeddings(cfg.OllamaURL, cfg.OllamaEmbedModel, cfg.OllamaTimeoutSec), nil
	}
}

// Close releases database handles.
func (c *Client) Close() error { return c.hist.close() }

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type SessionInfo struct {
	ID           int     `json:"id"`
	CreatedAt    float64 `json:"created_at"`
	MessageCount int     `json:"message_count"`
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
	Distance float64        `json:"distance"`
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

func (c *Client) AddMemory(text string, meta map[string]any) (*AddResult, error) {
	return c.vec.addText(text, meta, "")
}

func (c *Client) SearchMemory(query string, k int) (*SearchResult, error) {
	return c.vec.searchText(query, k)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
