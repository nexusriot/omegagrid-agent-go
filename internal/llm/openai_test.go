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

// capture records what a fake OpenAI endpoint received.
type capture struct {
	mu       sync.Mutex
	requests int
	paths    []string
	payloads []map[string]any
	auth     []string
}

func (c *capture) record(r *http.Request) {
	var payload map[string]any
	_ = json.NewDecoder(r.Body).Decode(&payload)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests++
	c.paths = append(c.paths, r.URL.Path)
	c.payloads = append(c.payloads, payload)
	c.auth = append(c.auth, r.Header.Get("Authorization"))
}

func (c *capture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requests
}

func (c *capture) last() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.payloads) == 0 {
		return nil
	}
	return c.payloads[len(c.payloads)-1]
}

// newTestChat wires an OpenAIChat at srv with instant retry backoff, and returns
// the recorded backoff delays so tests can assert the schedule.
func newTestChat(t *testing.T, srv *httptest.Server, temp *float64) (*OpenAIChat, *[]time.Duration) {
	t.Helper()
	c := NewOpenAIChat("test-key", srv.URL, "gpt-test", "chat_completions", "", temp, 5)
	var delays []time.Duration
	c.sleep = func(d time.Duration) { delays = append(delays, d) }
	return c, &delays
}

func TestNewOpenAIChatDefaults(t *testing.T) {
	c := NewOpenAIChat("k", "https://api.example.com/v1/", "m", "", "", nil, 30)
	if c.mode != "chat_completions" {
		t.Fatalf("mode = %q, want the chat_completions default", c.mode)
	}
	if c.BaseURL() != "https://api.example.com/v1" {
		t.Fatalf("BaseURL = %q, want the trailing slash trimmed", c.BaseURL())
	}
	if c.Model() != "m" {
		t.Fatalf("Model = %q", c.Model())
	}
	// Mode is matched lower-case throughout, so it must be normalised on the way in.
	if got := NewOpenAIChat("k", "u", "m", "RESPONSES", "", nil, 30).mode; got != "responses" {
		t.Fatalf("mode = %q, want %q", got, "responses")
	}
}

func TestChatCompletionsHappyPath(t *testing.T) {
	rec := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"type\":\"final\",\"answer\":\"hi\"}"}}]}`))
	}))
	defer srv.Close()

	temp := 0.7
	c, _ := newTestChat(t, srv, &temp)
	raw, elapsed, err := c.CompleteJSON([]Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hello"},
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
	if rec.paths[0] != "/chat/completions" {
		t.Fatalf("path = %q, want /chat/completions", rec.paths[0])
	}
	if rec.auth[0] != "Bearer test-key" {
		t.Fatalf("Authorization = %q", rec.auth[0])
	}

	p := rec.last()
	if p["model"] != "gpt-test" {
		t.Fatalf("model = %v", p["model"])
	}
	if p["temperature"] != 0.7 {
		t.Fatalf("temperature = %v, want 0.7", p["temperature"])
	}
	// JSON mode is what makes the agent's strict-JSON protocol work at all.
	rf, _ := p["response_format"].(map[string]any)
	if rf["type"] != "json_object" {
		t.Fatalf("response_format = %v, want json_object", p["response_format"])
	}
}

// A nil temperature must drop the field entirely — reasoning models reject any
// non-default value, so sending it at all makes them fail.
func TestChatCompletionsOmitsNilTemperature(t *testing.T) {
	rec := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]}`))
	}))
	defer srv.Close()

	c, _ := newTestChat(t, srv, nil)
	if _, _, err := c.CompleteJSON([]Message{{Role: "user", Content: "x"}}); err != nil {
		t.Fatalf("CompleteJSON: %v", err)
	}
	if _, present := rec.last()["temperature"]; present {
		t.Fatalf("temperature present with a nil setting: %v", rec.last())
	}
}

// The agent loop emits role="tool"; the OpenAI chat API only knows
// system/user/assistant, so tool turns must be re-tagged or the call 400s.
func TestChatCompletionsRemapsToolRole(t *testing.T) {
	rec := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]}`))
	}))
	defer srv.Close()

	c, _ := newTestChat(t, srv, nil)
	if _, _, err := c.CompleteJSON([]Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "q"},
		{Role: "assistant", Content: `{"type":"tool_call"}`},
		{Role: "tool", Content: `{"temp":20}`},
	}); err != nil {
		t.Fatalf("CompleteJSON: %v", err)
	}

	msgs, _ := rec.last()["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("sent %d messages, want 4", len(msgs))
	}
	got := msgs[3].(map[string]any)
	if got["role"] != "user" {
		t.Fatalf("tool turn sent with role %q, want user", got["role"])
	}
	if !strings.HasPrefix(got["content"].(string), "[Tool result]: ") {
		t.Fatalf("tool content lost its marker: %q", got["content"])
	}
	for i, want := range []string{"system", "user", "assistant"} {
		if role := msgs[i].(map[string]any)["role"]; role != want {
			t.Fatalf("message %d role = %v, want %v", i, role, want)
		}
	}
}

func TestChatCompletionsEmptyChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()

	c, _ := newTestChat(t, srv, nil)
	raw, _, err := c.CompleteJSON([]Message{{Role: "user", Content: "x"}})
	if err != nil {
		t.Fatalf("CompleteJSON: %v", err)
	}
	// "{}" keeps the agent's parser on its normal path instead of erroring out.
	if raw != "{}" {
		t.Fatalf("raw = %q, want %q", raw, "{}")
	}
}

func TestChatCompletionsClientErrorNotRetried(t *testing.T) {
	rec := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer srv.Close()

	c, delays := newTestChat(t, srv, nil)
	_, _, err := c.CompleteJSON([]Message{{Role: "user", Content: "x"}})
	if err == nil {
		t.Fatal("expected an error for HTTP 401")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "invalid api key") {
		t.Fatalf("error lost the server's explanation: %v", err)
	}
	// A bad key will not fix itself; retrying only delays the failure.
	if rec.count() != 1 {
		t.Fatalf("made %d attempts for a 401, want 1", rec.count())
	}
	if len(*delays) != 0 {
		t.Fatalf("backed off %v for a non-retryable status", *delays)
	}
}

func TestChatCompletionsDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json at all`))
	}))
	defer srv.Close()

	c, _ := newTestChat(t, srv, nil)
	if _, _, err := c.CompleteJSON([]Message{{Role: "user", Content: "x"}}); err == nil {
		t.Fatal("expected a decode error")
	}
}

func TestRetriesRateLimitThenSucceeds(t *testing.T) {
	rec := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		if rec.count() == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`rate limited`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"ok\":true}"}}]}`))
	}))
	defer srv.Close()

	c, delays := newTestChat(t, srv, nil)
	raw, _, err := c.CompleteJSON([]Message{{Role: "user", Content: "x"}})
	if err != nil {
		t.Fatalf("CompleteJSON: %v", err)
	}
	if raw != `{"ok":true}` {
		t.Fatalf("raw = %q", raw)
	}
	if rec.count() != 2 {
		t.Fatalf("made %d attempts, want 2", rec.count())
	}
	if len(*delays) != 1 || (*delays)[0] != 2*time.Second {
		t.Fatalf("backoff = %v, want [2s]", *delays)
	}
}

func TestRetriesServerErrorsUntilExhausted(t *testing.T) {
	rec := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`upstream is down`))
	}))
	defer srv.Close()

	c, delays := newTestChat(t, srv, nil)
	_, _, err := c.CompleteJSON([]Message{{Role: "user", Content: "x"}})
	if err == nil {
		t.Fatal("expected an error after exhausting retries")
	}
	if !strings.Contains(err.Error(), "after 3 attempts") {
		t.Fatalf("error does not report the retry exhaustion: %v", err)
	}
	if !strings.Contains(err.Error(), "502") {
		t.Fatalf("error lost the last status: %v", err)
	}
	if rec.count() != 3 {
		t.Fatalf("made %d attempts, want 3", rec.count())
	}
	// Documented schedule: 2s then 4s.
	if len(*delays) != 2 || (*delays)[0] != 2*time.Second || (*delays)[1] != 4*time.Second {
		t.Fatalf("backoff = %v, want [2s 4s]", *delays)
	}
}

// Each attempt already waited the full configured timeout; stacking two more
// multi-minute waits would just make the agent look frozen.
func TestClientTimeoutIsNotRetried(t *testing.T) {
	rec := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := NewOpenAIChat("k", srv.URL, "m", "chat_completions", "", nil, 0.05)
	var delays []time.Duration
	c.sleep = func(d time.Duration) { delays = append(delays, d) }

	if _, _, err := c.CompleteJSON([]Message{{Role: "user", Content: "x"}}); err == nil {
		t.Fatal("expected a timeout error")
	}
	if rec.count() != 1 {
		t.Fatalf("made %d attempts after a client timeout, want 1", rec.count())
	}
	if len(delays) != 0 {
		t.Fatalf("backed off %v after a timeout", delays)
	}
}

func TestRetriesTransportErrors(t *testing.T) {
	// A closed server yields connection-refused: a transport error, which is
	// retryable (unlike a timeout).
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	c := NewOpenAIChat("k", url, "m", "chat_completions", "", nil, 5)
	var delays []time.Duration
	c.sleep = func(d time.Duration) { delays = append(delays, d) }

	if _, _, err := c.CompleteJSON([]Message{{Role: "user", Content: "x"}}); err == nil {
		t.Fatal("expected a transport error")
	}
	if len(delays) != 2 {
		t.Fatalf("backoff = %v, want two retries", delays)
	}
}

func TestResponsesModeHappyPath(t *testing.T) {
	rec := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		_, _ = w.Write([]byte(`{"output_text":"{\"type\":\"final\",\"answer\":\"ok\"}"}`))
	}))
	defer srv.Close()

	c := NewOpenAIChat("k", srv.URL, "gpt-5.3-codex", "responses", "high", nil, 5)
	c.sleep = func(time.Duration) {}

	raw, _, err := c.CompleteJSON([]Message{{Role: "user", Content: "q"}})
	if err != nil {
		t.Fatalf("CompleteJSON: %v", err)
	}
	if raw != `{"type":"final","answer":"ok"}` {
		t.Fatalf("raw = %q", raw)
	}
	if rec.paths[0] != "/responses" {
		t.Fatalf("path = %q, want /responses", rec.paths[0])
	}

	p := rec.last()
	if _, ok := p["input"]; !ok {
		t.Fatalf("/responses payload has no input field: %v", p)
	}
	if _, ok := p["messages"]; ok {
		t.Fatalf("/responses payload must not use the chat_completions messages field: %v", p)
	}
	if p["store"] != false {
		t.Fatalf("store = %v, want false", p["store"])
	}
	reasoning, _ := p["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" {
		t.Fatalf("reasoning = %v, want effort=high", p["reasoning"])
	}
	text, _ := p["text"].(map[string]any)
	format, _ := text["format"].(map[string]any)
	if format["type"] != "json_object" {
		t.Fatalf("text.format = %v, want json_object", text["format"])
	}
}

func TestResponsesModeOmitsEmptyReasoning(t *testing.T) {
	rec := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		_, _ = w.Write([]byte(`{"output_text":"{}"}`))
	}))
	defer srv.Close()

	c := NewOpenAIChat("k", srv.URL, "m", "responses", "", nil, 5)
	c.sleep = func(time.Duration) {}
	if _, _, err := c.CompleteJSON([]Message{{Role: "user", Content: "q"}}); err != nil {
		t.Fatalf("CompleteJSON: %v", err)
	}
	if _, present := rec.last()["reasoning"]; present {
		t.Fatalf("reasoning sent despite being unset: %v", rec.last())
	}
}

// /responses may answer with a structured output array instead of output_text.
func TestResponsesModeWalksOutputArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"output":[
			{"content":[{"text":"{\"type\":"}]},
			{"content":[{"text":"\"final\","},{"text":"\"answer\":\"joined\"}"}]}
		]}`))
	}))
	defer srv.Close()

	c := NewOpenAIChat("k", srv.URL, "m", "responses", "", nil, 5)
	c.sleep = func(time.Duration) {}
	raw, _, err := c.CompleteJSON([]Message{{Role: "user", Content: "q"}})
	if err != nil {
		t.Fatalf("CompleteJSON: %v", err)
	}
	if raw != `{"type":"final","answer":"joined"}` {
		t.Fatalf("raw = %q, want the concatenated blocks", raw)
	}
}

func TestResponsesModeEmptyOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"output":[]}`))
	}))
	defer srv.Close()

	c := NewOpenAIChat("k", srv.URL, "m", "responses", "", nil, 5)
	c.sleep = func(time.Duration) {}
	raw, _, err := c.CompleteJSON([]Message{{Role: "user", Content: "q"}})
	if err != nil {
		t.Fatalf("CompleteJSON: %v", err)
	}
	if raw != "{}" {
		t.Fatalf("raw = %q, want %q", raw, "{}")
	}
}

func TestResponsesModeErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`unsupported parameter: temperature`))
	}))
	defer srv.Close()

	c := NewOpenAIChat("k", srv.URL, "m", "responses", "", nil, 5)
	c.sleep = func(time.Duration) {}
	_, _, err := c.CompleteJSON([]Message{{Role: "user", Content: "q"}})
	if err == nil {
		t.Fatal("expected an error for HTTP 400")
	}
	if !strings.Contains(err.Error(), "unsupported parameter") {
		t.Fatalf("error lost the server's explanation: %v", err)
	}
}

func TestErrorBodyIsTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(strings.Repeat("x", 5000)))
	}))
	defer srv.Close()

	c, _ := newTestChat(t, srv, nil)
	_, _, err := c.CompleteJSON([]Message{{Role: "user", Content: "q"}})
	if err == nil {
		t.Fatal("expected an error")
	}
	// A multi-megabyte HTML error page must not end up in the agent's debug log.
	if len(err.Error()) > 600 {
		t.Fatalf("error message is %d bytes, want it truncated", len(err.Error()))
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("abcdef", 3); got != "abc" {
		t.Fatalf("truncate = %q, want %q", got, "abc")
	}
	if got := truncate("ab", 5); got != "ab" {
		t.Fatalf("truncate = %q, want %q", got, "ab")
	}
}

// A client built as a zero value (not via NewOpenAIChat) must not nil-panic in
// the retry path.
func TestZeroValueClientSleepFallback(t *testing.T) {
	c := &OpenAIChat{}
	done := make(chan struct{})
	go func() { c.sleepFor(0); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sleepFor hung on a zero-value client")
	}
}
