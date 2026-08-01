package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewOllamaChatTrimsBaseURL(t *testing.T) {
	c := NewOllamaChat("http://127.0.0.1:11434/", "llama3:latest", 30)
	if c.BaseURL() != "http://127.0.0.1:11434" {
		t.Fatalf("BaseURL = %q, want the trailing slash trimmed", c.BaseURL())
	}
	if c.Model() != "llama3:latest" {
		t.Fatalf("Model = %q", c.Model())
	}
}

func TestOllamaCompleteJSON(t *testing.T) {
	var gotPath string
	var payload map[string]any
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&payload)
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"{\"type\":\"final\",\"answer\":\"hi\"}"}}`))
	}))
	defer srv.Close()

	c := NewOllamaChat(srv.URL, "llama3:latest", 5)
	raw, elapsed, err := c.CompleteJSON([]Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hello"},
		{Role: "tool", Content: `{"temp":20}`},
	})
	if err != nil {
		t.Fatalf("CompleteJSON: %v", err)
	}
	if raw != `{"type":"final","answer":"hi"}` {
		t.Fatalf("raw = %q", raw)
	}
	if elapsed < 0 {
		t.Fatalf("elapsed = %v", elapsed)
	}
	if gotPath != "/api/chat" {
		t.Fatalf("path = %q, want /api/chat", gotPath)
	}
	if calls != 1 {
		t.Fatalf("made %d requests, want 1 (the Ollama client does not retry)", calls)
	}

	if payload["model"] != "llama3:latest" {
		t.Fatalf("model = %v", payload["model"])
	}
	// format=json is what keeps a local model on the strict-JSON protocol.
	if payload["format"] != "json" {
		t.Fatalf("format = %v, want json", payload["format"])
	}
	if payload["stream"] != false {
		t.Fatalf("stream = %v, want false", payload["stream"])
	}
	opts, _ := payload["options"].(map[string]any)
	if opts["temperature"] != 0.2 {
		t.Fatalf("options.temperature = %v, want 0.2", opts["temperature"])
	}

	// Unlike the OpenAI client, Ollama accepts role="tool" verbatim — no remap.
	msgs, _ := payload["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("sent %d messages, want 3", len(msgs))
	}
	toolMsg := msgs[2].(map[string]any)
	if toolMsg["role"] != "tool" {
		t.Fatalf("tool role = %v, want it passed through unchanged", toolMsg["role"])
	}
	if toolMsg["content"] != `{"temp":20}` {
		t.Fatalf("tool content = %v, want it unprefixed", toolMsg["content"])
	}
}

func TestOllamaErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"model 'llama3:latest' not found, try pulling it first"}`))
	}))
	defer srv.Close()

	c := NewOllamaChat(srv.URL, "llama3:latest", 5)
	_, _, err := c.CompleteJSON([]Message{{Role: "user", Content: "x"}})
	if err == nil {
		t.Fatal("expected an error for HTTP 404")
	}
	// The "pull the model first" hint is the whole diagnosis — it must survive.
	if !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error lost the server's explanation: %v", err)
	}
}

func TestOllamaDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html>not ollama</html>`))
	}))
	defer srv.Close()

	c := NewOllamaChat(srv.URL, "m", 5)
	if _, _, err := c.CompleteJSON([]Message{{Role: "user", Content: "x"}}); err == nil {
		t.Fatal("expected a decode error")
	}
}

func TestOllamaUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	c := NewOllamaChat(url, "m", 5)
	_, elapsed, err := c.CompleteJSON([]Message{{Role: "user", Content: "x"}})
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if elapsed < 0 {
		t.Fatalf("elapsed = %v, want the time spent even on failure", elapsed)
	}
}

// A missing message field yields empty content, not an error — the agent's
// parser then produces its own fallback answer.
func TestOllamaEmptyMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"done":true}`))
	}))
	defer srv.Close()

	c := NewOllamaChat(srv.URL, "m", 5)
	raw, _, err := c.CompleteJSON([]Message{{Role: "user", Content: "x"}})
	if err != nil {
		t.Fatalf("CompleteJSON: %v", err)
	}
	if raw != "" {
		t.Fatalf("raw = %q, want empty", raw)
	}
}

// Both clients satisfy ChatClient — the agent depends only on that.
func TestClientsImplementChatClient(t *testing.T) {
	var _ ChatClient = NewOllamaChat("u", "m", 1)
	var _ ChatClient = NewOpenAIChat("k", "u", "m", "", "", nil, 1)
}
