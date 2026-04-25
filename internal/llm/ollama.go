package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OllamaChat speaks to a local Ollama server's /api/chat endpoint and forces
// JSON-mode output (format=json).
type OllamaChat struct {
	baseURL string
	model   string
	client  *http.Client
}

func NewOllamaChat(baseURL, model string, timeoutSec float64) *OllamaChat {
	return &OllamaChat{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		client:  &http.Client{Timeout: time.Duration(timeoutSec * float64(time.Second))},
	}
}

func (o *OllamaChat) Model() string   { return o.model }
func (o *OllamaChat) BaseURL() string { return o.baseURL }

func (o *OllamaChat) CompleteJSON(messages []Message) (string, float64, error) {
	t0 := time.Now()
	payload := map[string]any{
		"model":    o.model,
		"messages": messages,
		"stream":   false,
		"format":   "json",
		"options":  map[string]any{"temperature": 0.2},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", 0, fmt.Errorf("marshal request: %w", err)
	}
	resp, err := o.client.Post(o.baseURL+"/api/chat", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", time.Since(t0).Seconds(), fmt.Errorf("ollama post: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", time.Since(t0).Seconds(), fmt.Errorf("ollama status %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	var out struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", time.Since(t0).Seconds(), fmt.Errorf("ollama decode: %w", err)
	}
	return out.Message.Content, time.Since(t0).Seconds(), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
