package telegram

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sseServer serves a fixed SSE body on /api/query/stream.
func sseServer(t *testing.T, body string) (*httptest.Server, *string) {
	t.Helper()
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/query/stream" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		raw := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(raw)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	return srv, &gotBody
}

func drain(ch <-chan Event) []Event {
	var out []Event
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

func TestQueryStreamParsesEvents(t *testing.T) {
	srv, gotBody := sseServer(t, strings.Join([]string{
		"event: thinking",
		`data: {"event":"thinking","step":1}`,
		"",
		"event: tool_call",
		`data: {"event":"tool_call","step":1,"tool":"weather","why":"need the forecast"}`,
		"",
		"event: tool_result",
		`data: {"event":"tool_result","step":1,"tool":"weather","elapsed_s":0.42}`,
		"",
		"event: final",
		`data: {"event":"final","session_id":42,"answer":"It is 15C.","meta":{"model":"m","step_count":2}}`,
		"",
		"",
	}, "\n"))
	defer srv.Close()

	c := NewAgentClient(srv.URL)
	events := make(chan Event, 16)
	errCh := make(chan error, 1)
	go func() { errCh <- c.QueryStream("weather in London?", 777, 42, events) }()

	evs := drain(events)
	if err := <-errCh; err != nil {
		t.Fatalf("QueryStream: %v", err)
	}
	if len(evs) != 4 {
		t.Fatalf("got %d events, want 4: %+v", len(evs), evs)
	}
	if evs[0].Event != "thinking" || evs[0].Step != 1 {
		t.Fatalf("event 0 = %+v", evs[0])
	}
	if evs[1].Tool != "weather" || evs[1].Why != "need the forecast" {
		t.Fatalf("event 1 = %+v", evs[1])
	}
	if evs[2].ElapsedS != 0.42 {
		t.Fatalf("event 2 elapsed = %v", evs[2].ElapsedS)
	}
	if evs[3].Event != "final" || evs[3].SessionID != 42 || evs[3].Answer != "It is 15C." {
		t.Fatalf("event 3 = %+v", evs[3])
	}

	// The chat id must reach the gateway: it is what schedule_task uses for
	// "notify me on Telegram".
	var sent map[string]any
	if err := json.Unmarshal([]byte(*gotBody), &sent); err != nil {
		t.Fatalf("request body is not JSON: %q", *gotBody)
	}
	if sent["telegram_chat_id"] != float64(777) {
		t.Fatalf("telegram_chat_id = %v, want 777", sent["telegram_chat_id"])
	}
	if sent["session_id"] != float64(42) {
		t.Fatalf("session_id = %v, want 42", sent["session_id"])
	}
	if sent["query"] != "weather in London?" {
		t.Fatalf("query = %v", sent["query"])
	}
}

// A session_id of 0 means "start a new session" and must be omitted so the
// gateway does not try to resume session zero.
func TestQueryStreamOmitsZeroSession(t *testing.T) {
	srv, gotBody := sseServer(t, "event: final\ndata: {\"event\":\"final\",\"answer\":\"x\"}\n\n")
	defer srv.Close()

	c := NewAgentClient(srv.URL)
	events := make(chan Event, 4)
	go func() { _ = c.QueryStream("q", 1, 0, events) }()
	drain(events)

	if strings.Contains(*gotBody, "session_id") {
		t.Fatalf("session_id sent for a fresh conversation: %q", *gotBody)
	}
}

// The channel must close even when the stream ends without a final event,
// otherwise the bot's range loop hangs forever.
func TestQueryStreamClosesChannelOnTruncatedStream(t *testing.T) {
	srv, _ := sseServer(t, "event: thinking\ndata: {\"event\":\"thinking\",\"step\":1}\n\n")
	defer srv.Close()

	c := NewAgentClient(srv.URL)
	events := make(chan Event, 4)
	errCh := make(chan error, 1)
	go func() { errCh <- c.QueryStream("q", 1, 0, events) }()

	evs := drain(events) // returns only once the channel is closed
	if err := <-errCh; err != nil {
		t.Fatalf("QueryStream: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1", len(evs))
	}
}

func TestQueryStreamReturnsGatewayError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"agent exploded"}`))
	}))
	defer srv.Close()

	c := NewAgentClient(srv.URL)
	events := make(chan Event, 4)
	errCh := make(chan error, 1)
	go func() { errCh <- c.QueryStream("q", 1, 0, events) }()

	if evs := drain(events); len(evs) != 0 {
		t.Fatalf("got events from a failed stream: %+v", evs)
	}
	err := <-errCh
	if err == nil {
		t.Fatal("expected an error for HTTP 500")
	}
	// The bot logs this before falling back to the sync endpoint.
	if !strings.Contains(err.Error(), "agent exploded") {
		t.Fatalf("error lost the gateway's explanation: %v", err)
	}
}

// A malformed GATEWAY_URL must be reported, not panic on a nil request.
func TestQueryStreamMalformedBaseURL(t *testing.T) {
	c := NewAgentClient("http://bad host:8000")
	events := make(chan Event, 4)
	errCh := make(chan error, 1)
	go func() { errCh <- c.QueryStream("q", 1, 0, events) }()

	drain(events)
	if err := <-errCh; err == nil {
		t.Fatal("expected an error for an unparseable base URL")
	}
}

// Undecodable data lines are skipped rather than aborting the stream.
func TestQueryStreamSkipsUnparseableEvents(t *testing.T) {
	srv, _ := sseServer(t, strings.Join([]string{
		"event: thinking",
		"data: {this is not json}",
		"",
		"event: final",
		`data: {"event":"final","answer":"survived"}`,
		"",
		"",
	}, "\n"))
	defer srv.Close()

	c := NewAgentClient(srv.URL)
	events := make(chan Event, 8)
	go func() { _ = c.QueryStream("q", 1, 0, events) }()

	evs := drain(events)
	if len(evs) != 1 || evs[0].Answer != "survived" {
		t.Fatalf("events = %+v, want just the final one", evs)
	}
}

func TestQueryFallbackEndpoint(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"session_id":9,"answer":"sync answer","meta":{"model":"m"}}`))
	}))
	defer srv.Close()

	c := NewAgentClient(srv.URL + "/")
	resp, err := c.Query("q", 5, 0)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if gotPath != "/api/query" {
		t.Fatalf("path = %q, want /api/query", gotPath)
	}
	if resp.SessionID != 9 || resp.Answer != "sync answer" {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Meta["model"] != "m" {
		t.Fatalf("meta = %v", resp.Meta)
	}
}

func TestQueryReportsGatewayError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"query is required"}`))
	}))
	defer srv.Close()

	_, err := NewAgentClient(srv.URL).Query("", 1, 0)
	if err == nil {
		t.Fatal("expected an error for HTTP 400")
	}
	if !strings.Contains(err.Error(), "query is required") {
		t.Fatalf("error lost the gateway's explanation: %v", err)
	}
}

func TestQueryUnreachableGateway(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	if _, err := NewAgentClient(url).Query("q", 1, 0); err == nil {
		t.Fatal("expected a transport error")
	}
}
