// Package skills is a thin HTTP client to the Python sidecar's skill registry.
// All skill code (and the markdown skill loader / skill_creator) lives in
// Python; the Go gateway just discovers and invokes them by name.
package skills

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Param describes a single skill parameter as exposed by the sidecar.
type Param struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// Skill is the public schema returned by GET /skills.
type Skill struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Parameters  map[string]Param `json:"parameters"`
	Body        string           `json:"body,omitempty"` // free-text prompt instructions for markdown skills
}

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 5 * time.Minute}, // skills like web_scrape can be slow
	}
}

// List returns the current set of skills.  This is called on every agent run
// (rather than cached) so that skills created at runtime via skill_creator
// become visible immediately.
func (c *Client) List() ([]Skill, error) {
	resp, err := c.http.Get(c.baseURL + "/skills")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("sidecar /skills: %d %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var out struct {
		Skills []Skill `json:"skills"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode skills: %w", err)
	}
	return out.Skills, nil
}

// Execute calls one skill by name with the given args.  The result is whatever
// JSON the skill returned (we keep it as a generic any so the agent can pass
// it back into the LLM context unchanged).
func (c *Client) Execute(name string, args map[string]any) (any, error) {
	body, _ := json.Marshal(map[string]any{"name": name, "args": args})
	req, _ := http.NewRequest(http.MethodPost, c.baseURL+"/skills/execute", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("sidecar /skills/execute %s: %d %s", name, resp.StatusCode, truncate(string(raw), 200))
	}
	var out struct {
		Result any `json:"result"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode skill result: %w", err)
	}
	return out.Result, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
