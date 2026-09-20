package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The relay rejects any request without x-opencode-session (400
// MissingSessionID), so the header must ride on every call — including the
// retries, which build a fresh *http.Request per attempt.
func TestOpenCodeSendsSessionHeaderOnEveryAttempt(t *testing.T) {
	var mu sync.Mutex
	var sessions, auth []string
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sessions = append(sessions, r.Header.Get(SessionHeader))
		auth = append(auth, r.Header.Get("Authorization"))
		attempts++
		first := attempts == 1
		mu.Unlock()
		if first {
			w.WriteHeader(http.StatusTooManyRequests) // force one retry
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"ok\":true}"}}]}`))
	}))
	defer srv.Close()

	c := NewOpenCodeChat("sk-test", srv.URL, "kimi-k2.6", "ses_fixed", nil, 5)
	c.sleep = func(time.Duration) {}

	raw, _, err := c.CompleteJSON([]Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("CompleteJSON: %v", err)
	}
	if raw != `{"ok":true}` {
		t.Fatalf("raw = %q", raw)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d requests, want 2 (one retried)", len(sessions))
	}
	for i, got := range sessions {
		if got != "ses_fixed" {
			t.Errorf("attempt %d %s = %q, want %q", i, SessionHeader, got, "ses_fixed")
		}
		if auth[i] != "Bearer sk-test" {
			t.Errorf("attempt %d Authorization = %q", i, auth[i])
		}
	}
}

// An empty session id must still produce one — otherwise every request 400s.
func TestOpenCodeMintsSessionIDWhenEmpty(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(SessionHeader)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]}`))
	}))
	defer srv.Close()

	c := NewOpenCodeChat("sk-test", srv.URL, "kimi-k2.6", "", nil, 5)
	if _, _, err := c.CompleteJSON([]Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("CompleteJSON: %v", err)
	}
	if !strings.HasPrefix(got, "ses_") || len(got) != len("ses_")+32 {
		t.Fatalf("%s = %q, want a ses_<32 hex> id", SessionHeader, got)
	}

	// The id is stable for the client's lifetime (prompt-cache affinity) but
	// differs between clients.
	other := NewOpenCodeChat("sk-test", srv.URL, "kimi-k2.6", "", nil, 5)
	if other.extraHeaders[SessionHeader] == got {
		t.Fatal("two clients minted the same session id")
	}
}

// The relay serves chat_completions only — it has no /responses endpoint — and
// it must get the json_object response format the agent loop depends on.
func TestOpenCodeUsesChatCompletionsWithJSONFormat(t *testing.T) {
	var path string
	var payload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&payload)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]}`))
	}))
	defer srv.Close()

	temp := 0.2
	c := NewOpenCodeChat("sk-test", srv.URL+"/", "glm-5.3", "ses_x", &temp, 5)
	if c.mode != "chat_completions" {
		t.Fatalf("mode = %q", c.mode)
	}
	if c.BaseURL() != srv.URL {
		t.Fatalf("BaseURL = %q, want the trailing slash trimmed", c.BaseURL())
	}
	if c.Model() != "glm-5.3" {
		t.Fatalf("Model = %q", c.Model())
	}
	if _, _, err := c.CompleteJSON([]Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("CompleteJSON: %v", err)
	}
	if path != "/chat/completions" {
		t.Fatalf("path = %q", path)
	}
	rf, _ := payload["response_format"].(map[string]any)
	if rf["type"] != "json_object" {
		t.Fatalf("response_format = %v", payload["response_format"])
	}
	if payload["temperature"] != 0.2 {
		t.Fatalf("temperature = %v", payload["temperature"])
	}
}

// WithHeader is generic: it is applied after the standard headers, so it can
// also override them.
func TestWithHeaderAppliesAndOverrides(t *testing.T) {
	var custom, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		custom = r.Header.Get("X-Custom")
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]}`))
	}))
	defer srv.Close()

	c := NewOpenAIChat("sk-test", srv.URL, "m", "", "", nil, 5,
		WithHeader("X-Custom", "yes"),
		WithHeader("Authorization", "Bearer override"),
	)
	if _, _, err := c.CompleteJSON([]Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("CompleteJSON: %v", err)
	}
	if custom != "yes" {
		t.Fatalf("X-Custom = %q", custom)
	}
	if auth != "Bearer override" {
		t.Fatalf("Authorization = %q, want the option to win", auth)
	}
}

// A client built with no options must not carry a header map at all — proving
// the existing OpenAI/DigitalOcean call sites are untouched by the option.
func TestNoOptionsMeansNoExtraHeaders(t *testing.T) {
	c := NewOpenAIChat("k", "https://api.example.com/v1", "m", "", "", nil, 30)
	if c.extraHeaders != nil {
		t.Fatalf("extraHeaders = %v, want nil", c.extraHeaders)
	}
}

func TestNewSessionIDIsUniqueAndShaped(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := NewSessionID()
		if !strings.HasPrefix(id, "ses_") || len(id) != len("ses_")+32 {
			t.Fatalf("NewSessionID = %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate session id %q", id)
		}
		seen[id] = true
	}
}
