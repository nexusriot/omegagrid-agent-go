// Package memory is a thin HTTP client to the Python sidecar's memory + history
// service.  All persistence (sqlite history, ChromaDB vector store) lives in
// Python; Go just calls these endpoints.
package memory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

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

func (c *Client) CreateSession() (int, error) {
	var out struct {
		SessionID int `json:"session_id"`
	}
	if err := c.postJSON("/sessions/new", nil, &out); err != nil {
		return 0, err
	}
	return out.SessionID, nil
}

func (c *Client) ListSessions(limit int) ([]SessionInfo, error) {
	var out struct {
		Sessions []SessionInfo `json:"sessions"`
	}
	if err := c.getJSON(fmt.Sprintf("/sessions?limit=%d", limit), &out); err != nil {
		return nil, err
	}
	return out.Sessions, nil
}

func (c *Client) ListMessages(sessionID, limit, offset int) ([]StoredMessage, error) {
	var out struct {
		Messages []StoredMessage `json:"messages"`
	}
	path := fmt.Sprintf("/sessions/%d/messages?limit=%d&offset=%d", sessionID, limit, offset)
	if err := c.getJSON(path, &out); err != nil {
		return nil, err
	}
	return out.Messages, nil
}

func (c *Client) LoadTail(sessionID, limit int) ([]Message, error) {
	var out struct {
		Messages []Message `json:"messages"`
	}
	path := fmt.Sprintf("/sessions/%d/tail?limit=%d", sessionID, limit)
	if err := c.getJSON(path, &out); err != nil {
		return nil, err
	}
	return out.Messages, nil
}

// AddMessage stores a single conversation message.  content may be a string
// (plain text) or any JSON-serializable value (used for tool results).
func (c *Client) AddMessage(sessionID int, role string, content any) error {
	body := map[string]any{"role": role, "content": content}
	return c.postJSON(fmt.Sprintf("/sessions/%d/messages", sessionID), body, nil)
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

func (c *Client) AddMemory(text string, meta map[string]any) (*AddResult, error) {
	var out AddResult
	body := map[string]any{"text": text, "meta": meta}
	if err := c.postJSON("/memory/add", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type SearchResult struct {
	Hits    []MemoryHit        `json:"hits"`
	Timings map[string]float64 `json:"timings"`
}

func (c *Client) SearchMemory(query string, k int) (*SearchResult, error) {
	var out SearchResult
	body := map[string]any{"query": query, "k": k}
	if err := c.postJSON("/memory/search", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) postJSON(path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		buf, _ := json.Marshal(body)
		rdr = bytes.NewReader(buf)
	}
	req, _ := http.NewRequest(http.MethodPost, c.baseURL+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

func (c *Client) getJSON(path string, out any) error {
	full := c.baseURL + path
	if _, err := url.Parse(full); err != nil {
		return err
	}
	req, _ := http.NewRequest(http.MethodGet, full, nil)
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("sidecar %s %s: %d %s", req.Method, req.URL.Path, resp.StatusCode, truncate(string(raw), 200))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
