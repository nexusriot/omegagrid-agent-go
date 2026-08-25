package httpapi

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/nexusriot/omegagrid-agent-go/internal/agent"
	"github.com/nexusriot/omegagrid-agent-go/internal/llm"
	"github.com/nexusriot/omegagrid-agent-go/internal/mcp"
)

// scriptedChat replays canned model replies, so the query endpoints can be
// driven end-to-end through the real agent loop without an LLM.
type scriptedChat struct {
	mu        sync.Mutex
	replies   []string
	idx       int
	seenTurns [][]llm.Message
}

func (s *scriptedChat) CompleteJSON(msgs []llm.Message) (string, float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seenTurns = append(s.seenTurns, msgs)
	if s.idx >= len(s.replies) {
		return `{"type":"final","answer":"done"}`, 0, nil
	}
	r := s.replies[s.idx]
	s.idx++
	return r, 0, nil
}

func (s *scriptedChat) Model() string   { return "scripted-model" }
func (s *scriptedChat) BaseURL() string { return "http://scripted" }

func (s *scriptedChat) turns() [][]llm.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seenTurns
}

// withAgent attaches a real agent.Service driven by chat to d.
func withAgent(d Deps, chat llm.ChatClient) Deps {
	d.Chat = chat
	d.Agent = &agent.Service{
		Memory:       d.Memory,
		Skills:       d.Skills,
		Chat:         chat,
		NativeSkills: map[string]agent.Skill{},
		ContextTail:  10,
		MemoryHits:   0,
		MaxParallel:  4,
	}
	return d
}

func TestQueryEndpointReturnsFinalAnswer(t *testing.T) {
	chat := &scriptedChat{replies: []string{`{"type":"final","answer":"It is 15C."}`}}
	h := NewRouter(withAgent(newTestDeps(t), chat))

	code, body := doJSON(t, h, "POST", "/api/query", `{"query":"weather?"}`)
	if code != http.StatusOK {
		t.Fatalf("status %d (%v)", code, body)
	}
	if body["answer"] != "It is 15C." {
		t.Fatalf("answer = %v", body["answer"])
	}
	if sid, _ := body["session_id"].(float64); sid == 0 {
		t.Fatalf("session_id = %v, want a created session", body["session_id"])
	}
	meta, _ := body["meta"].(map[string]any)
	if meta["model"] != "scripted-model" {
		t.Fatalf("meta.model = %v", meta["model"])
	}
	if meta["step_count"] != float64(1) {
		t.Fatalf("meta.step_count = %v, want 1", meta["step_count"])
	}
	if _, ok := body["debug_log"]; !ok {
		t.Fatalf("response carries no debug_log: %v", body)
	}
}

// A tool call must actually execute the registry skill and feed its result back.
func TestQueryEndpointRunsToolCall(t *testing.T) {
	chat := &scriptedChat{replies: []string{
		`{"type":"tool_call","tool":"datetime_skill","args":{},"why":"need the date"}`,
		`{"type":"final","answer":"Today is a good day."}`,
	}}
	d := withAgent(newTestDeps(t), chat)
	h := NewRouter(d)

	code, body := doJSON(t, h, "POST", "/api/query", `{"query":"what day is it?"}`)
	if code != http.StatusOK {
		t.Fatalf("status %d (%v)", code, body)
	}
	if body["answer"] != "Today is a good day." {
		t.Fatalf("answer = %v", body["answer"])
	}
	if meta, _ := body["meta"].(map[string]any); meta["step_count"] != float64(2) {
		t.Fatalf("step_count = %v, want 2", meta["step_count"])
	}

	// The second turn must carry the tool result back to the model.
	turns := chat.turns()
	if len(turns) != 2 {
		t.Fatalf("model called %d times, want 2", len(turns))
	}
	var sawToolResult bool
	for _, m := range turns[1] {
		if m.Role == "tool" && strings.Contains(m.Content, "unix_timestamp") {
			sawToolResult = true
		}
	}
	if !sawToolResult {
		t.Fatalf("second turn has no tool result: %+v", turns[1])
	}

	// And the call must be in the audit log.
	code, body = doJSON(t, h, "GET", "/api/invocations", "")
	if code != http.StatusOK {
		t.Fatalf("invocations status %d", code)
	}
	invs, _ := body["invocations"].([]any)
	if len(invs) != 1 {
		t.Fatalf("audit log has %d rows, want 1", len(invs))
	}
	rec, _ := invs[0].(map[string]any)
	if rec["skill"] != "datetime_skill" || rec["kind"] != "skill" {
		t.Fatalf("audit row = %v", rec)
	}
	if rec["why"] != "need the date" {
		t.Fatalf("audit row lost the reason: %v", rec["why"])
	}
}

// A follow-up query on the same session must see the earlier turns.
func TestQueryEndpointContinuesSession(t *testing.T) {
	chat := &scriptedChat{replies: []string{
		`{"type":"final","answer":"first"}`,
		`{"type":"final","answer":"second"}`,
	}}
	h := NewRouter(withAgent(newTestDeps(t), chat))

	_, body := doJSON(t, h, "POST", "/api/query", `{"query":"remember apples"}`)
	sid := int(body["session_id"].(float64))

	code, body := doJSON(t, h, "POST", "/api/query",
		`{"query":"what did I say?","session_id":`+strconv.Itoa(sid)+`}`)
	if code != http.StatusOK {
		t.Fatalf("status %d (%v)", code, body)
	}
	if got := int(body["session_id"].(float64)); got != sid {
		t.Fatalf("session_id = %d, want the same session %d", got, sid)
	}

	// The second turn's message list must contain the first exchange.
	second := chat.turns()[1]
	var joined strings.Builder
	for _, m := range second {
		joined.WriteString(m.Role + ":" + m.Content + "\n")
	}
	if !strings.Contains(joined.String(), "remember apples") {
		t.Fatalf("history tail missing from the follow-up turn:\n%s", joined.String())
	}
	// The current query must appear exactly once — the tail is loaded before the
	// query is stored precisely so it is not sent twice.
	if n := strings.Count(joined.String(), "what did I say?"); n != 1 {
		t.Fatalf("current query appears %d times in the prompt, want 1", n)
	}

	// Both turns are persisted and readable back.
	code, body = doJSON(t, h, "GET", "/api/sessions/"+strconv.Itoa(sid)+"/messages", "")
	if code != http.StatusOK {
		t.Fatalf("messages status %d", code)
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 4 { // user, assistant, user, assistant
		t.Fatalf("session has %d messages, want 4: %v", len(msgs), msgs)
	}
}

func TestQueryEndpointRejectsBadJSON(t *testing.T) {
	h := NewRouter(newTestDeps(t))
	for _, path := range []string{"/api/query", "/api/query/stream"} {
		code, _ := doJSON(t, h, "POST", path, `{"query":`)
		if code != http.StatusBadRequest {
			t.Fatalf("POST %s with malformed JSON: status %d, want 400", path, code)
		}
	}
}

// A hard agent failure (no session store) must be a 500 with a hint, not a panic.
func TestQueryEndpointAgentFailure(t *testing.T) {
	d := newTestDeps(t)
	chat := &scriptedChat{}
	d = withAgent(d, chat)
	// Close the history store so session creation fails.
	if err := d.Memory.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	h := NewRouter(d)

	code, body := doJSON(t, h, "POST", "/api/query", `{"query":"x"}`)
	if code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500 (%v)", code, body)
	}
	if body["hint"] == nil {
		t.Fatalf("500 response carries no operator hint: %v", body)
	}
}

// readSSE collects (event, data) pairs from an SSE response body.
func readSSE(t *testing.T, body string) [][2]string {
	t.Helper()
	var out [][2]string
	var event string
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			out = append(out, [2]string{event, strings.TrimPrefix(line, "data: ")})
		}
	}
	return out
}

func TestQueryStreamEmitsSSE(t *testing.T) {
	chat := &scriptedChat{replies: []string{
		`{"type":"tool_call","tool":"datetime_skill","args":{},"why":"date"}`,
		`{"type":"final","answer":"streamed"}`,
	}}
	h := NewRouter(withAgent(newTestDeps(t), chat))

	r := httptest.NewRequest("POST", "/api/query/stream", strings.NewReader(`{"query":"q"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	// nginx buffers SSE without this header, so the UI would show nothing until
	// the run finished.
	if w.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want no", w.Header().Get("X-Accel-Buffering"))
	}

	events := readSSE(t, w.Body.String())
	var names []string
	for _, e := range events {
		names = append(names, e[0])
	}
	want := []string{"thinking", "tool_call", "tool_result", "thinking", "final"}
	if len(names) != len(want) {
		t.Fatalf("events = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("event %d = %q, want %q (all: %v)", i, names[i], want[i], names)
		}
	}

	// Every data line must be a JSON object whose "event" matches its SSE name.
	for _, e := range events {
		var payload map[string]any
		if err := json.Unmarshal([]byte(e[1]), &payload); err != nil {
			t.Fatalf("event %q data is not JSON: %s", e[0], e[1])
		}
		if payload["event"] != e[0] {
			t.Fatalf("SSE name %q != payload event %v", e[0], payload["event"])
		}
	}

	var final map[string]any
	_ = json.Unmarshal([]byte(events[len(events)-1][1]), &final)
	if final["answer"] != "streamed" {
		t.Fatalf("final answer = %v", final["answer"])
	}
	if sid, _ := final["session_id"].(float64); sid == 0 {
		t.Fatal("final event carries no session_id")
	}
}

// An unparseable model reply is delivered as a graceful final, matching the
// non-streaming endpoint, not as an error event.
func TestQueryStreamUnparseableReplyIsFinal(t *testing.T) {
	chat := &scriptedChat{replies: []string{"I am not JSON"}}
	h := NewRouter(withAgent(newTestDeps(t), chat))

	r := httptest.NewRequest("POST", "/api/query/stream", strings.NewReader(`{"query":"q"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	events := readSSE(t, w.Body.String())
	if len(events) == 0 {
		t.Fatal("no events emitted")
	}
	last := events[len(events)-1]
	if last[0] != "final" {
		t.Fatalf("last event = %q, want final (all: %v)", last[0], events)
	}
	for _, e := range events {
		if e[0] == "error" {
			t.Fatalf("unexpected error event: %s", e[1])
		}
	}
	var payload map[string]any
	_ = json.Unmarshal([]byte(last[1]), &payload)
	meta, _ := payload["meta"].(map[string]any)
	if meta["fallback"] != true {
		t.Fatalf("meta.fallback = %v, want true", meta["fallback"])
	}
}

func TestMCPEndpointMountedAndGated(t *testing.T) {
	d := newTestDeps(t)
	d.Cfg.MCPServerEnabled = true
	d.ToolProvider = &stubProvider{}
	h := NewRouter(d)

	code, body := doJSON(t, h, "POST", "/mcp",
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if code != http.StatusOK {
		t.Fatalf("status %d (%v)", code, body)
	}
	result, _ := body["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v", result["tools"])
	}

	// Disabled by config → route not mounted at all.
	d.Cfg.MCPServerEnabled = false
	if code := postStatus(NewRouter(d), "/mcp"); code != http.StatusNotFound {
		t.Fatalf("status %d with MCP disabled, want 404", code)
	}

	// No provider → also not mounted, even when enabled.
	d.Cfg.MCPServerEnabled = true
	d.ToolProvider = nil
	if code := postStatus(NewRouter(d), "/mcp"); code != http.StatusNotFound {
		t.Fatalf("status %d with no tool provider, want 404", code)
	}
}

// postStatus returns just the status code, for routes whose error bodies are
// not JSON (chi’s own 404/405 pages).
func postStatus(h http.Handler, path string) int {
	r := httptest.NewRequest("POST", path, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

type stubProvider struct{}

func (stubProvider) Tools() []mcp.Tool {
	return []mcp.Tool{{Name: "echo", Description: "echo", Parameters: map[string]mcp.Param{
		"text": {Type: "string", Description: "text", Required: true},
	}}}
}

func (stubProvider) Call(name string, args map[string]any) (any, error) {
	return map[string]any{"echoed": args["text"], "tool": name}, nil
}

func TestMemoryEndpointsRequireFields(t *testing.T) {
	h := NewRouter(newTestDeps(t))

	if code, _ := doJSON(t, h, "POST", "/api/memory/add", `{"meta":{}}`); code != http.StatusBadRequest {
		t.Fatalf("memory/add without text: status %d, want 400", code)
	}
	if code, _ := doJSON(t, h, "POST", "/api/memory/search", `{"k":5}`); code != http.StatusBadRequest {
		t.Fatalf("memory/search without query: status %d, want 400", code)
	}
	// The embeddings backend is unreachable in tests, so a well-formed add is
	// reported as a client-visible failure rather than a panic or a hang.
	if code, _ := doJSON(t, h, "POST", "/api/memory/add", `{"text":"x"}`); code != http.StatusBadRequest {
		t.Fatalf("memory/add with a dead backend: status %d, want 400", code)
	}
}

func TestMemoryGetDeleteUnknownID(t *testing.T) {
	h := NewRouter(newTestDeps(t))
	if code, _ := doJSON(t, h, "GET", "/api/memory/nope", ""); code != http.StatusNotFound {
		t.Fatalf("GET unknown memory: status %d, want 404", code)
	}
	if code, _ := doJSON(t, h, "DELETE", "/api/memory/nope", ""); code != http.StatusNotFound {
		t.Fatalf("DELETE unknown memory: status %d, want 404", code)
	}
}

func TestToolsEndpointDocumentsVectorTools(t *testing.T) {
	h := NewRouter(newTestDeps(t))
	code, body := doJSON(t, h, "GET", "/api/tools", "")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	tools, _ := body["tools"].([]any)
	names := map[string]bool{}
	for _, tl := range tools {
		m, _ := tl.(map[string]any)
		names[m["name"].(string)] = true
	}
	// These are agent-loop tools, absent from /api/skills, so /api/tools is the
	// only place they are described.
	for _, want := range []string{"vector_add", "vector_search"} {
		if !names[want] {
			t.Fatalf("/api/tools is missing %q: %v", want, names)
		}
	}
}

func TestSkillInvokeUnknownSkill(t *testing.T) {
	h := NewRouter(newTestDeps(t))
	code, body := doJSON(t, h, "POST", "/api/skills/no_such_skill/invoke", `{"args":{}}`)
	// The playground reports execution failures in-band with 200 + error.
	if code != http.StatusOK {
		t.Fatalf("status %d, want 200 (%v)", code, body)
	}
	if body["error"] == nil {
		t.Fatalf("no error reported for an unknown skill: %v", body)
	}
}

func TestSkillInvokeRejectsBadBody(t *testing.T) {
	h := NewRouter(newTestDeps(t))
	if code, _ := doJSON(t, h, "POST", "/api/skills/datetime_skill/invoke", `{`); code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", code)
	}
}

// A skill returning an image must have its base64 lifted into attachments so
// the payload the model sees stays small.
func TestSkillInvokeExtractsAttachments(t *testing.T) {
	h := NewRouter(newTestDeps(t))
	code, body := doJSON(t, h, "POST", "/api/skills/qr_generate/invoke",
		`{"args":{"data":"https://example.com"}}`)
	if code != http.StatusOK {
		t.Fatalf("status %d (%v)", code, body)
	}
	atts, _ := body["attachments"].([]any)
	if len(atts) != 1 {
		t.Fatalf("attachments = %v, want 1", body["attachments"])
	}
	att, _ := atts[0].(map[string]any)
	if att["mime_type"] != "image/png" || att["base64"] == "" {
		t.Fatalf("attachment = %v", att)
	}
	result, _ := body["result"].(map[string]any)
	if _, present := result["image_base64"]; present {
		t.Fatalf("heavy base64 left in the result payload: %v", result)
	}
	if result["_image_attached"] != true {
		t.Fatalf("result carries no attachment marker: %v", result)
	}
}

// The agent loop runs on a goroutine of its own for the streaming endpoint, so
// chi's Recoverer — which wraps only the handler goroutine — cannot catch a
// panic raised inside it and the whole gateway used to die. The tool crash must
// come back as an ordinary tool result and the run must continue.
func TestQueryStreamSurvivesToolPanic(t *testing.T) {
	chat := &scriptedChat{replies: []string{
		`{"type":"tool_call","tool":"boom","args":{},"why":"crash"}`,
		`{"type":"final","answer":"still here"}`,
	}}
	d := withAgent(newTestDeps(t), chat)
	d.Agent.NativeSkills = map[string]agent.Skill{
		"boom": {Execute: func(map[string]any) (any, error) { panic("stream boom") }},
	}
	h := NewRouter(d)

	r := httptest.NewRequest("POST", "/api/query/stream", strings.NewReader(`{"query":"q"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	events := readSSE(t, w.Body.String())
	var sawCrash bool
	var final map[string]any
	for _, e := range events {
		var payload map[string]any
		_ = json.Unmarshal([]byte(e[1]), &payload)
		if e[0] == "tool_result" && strings.Contains(payload["result"].(string), "crashed") {
			sawCrash = true
		}
		if e[0] == "final" {
			final = payload
		}
	}
	if !sawCrash {
		t.Errorf("no tool_result reported the crash: %s", w.Body.String())
	}
	if final == nil || final["answer"] != "still here" {
		t.Errorf("run did not reach a final answer: %v", final)
	}
}
