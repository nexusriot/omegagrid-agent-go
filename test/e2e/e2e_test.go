//go:build e2e

// Package e2e drives a *running* gateway over HTTP. Nothing here imports the
// project's own packages: the suite only sees what a real client sees, so it
// covers the seams unit tests cannot — process boot from environment variables,
// SQLite and chromem on a real filesystem, the background scheduler goroutine,
// SSE framing over a socket, and the MCP endpoint.
//
// It is hermetic: the only "model" is test/e2e/mockllm, and the compose network
// has no route off the host (TestNetworkIsolation proves it).
//
// Run it with scripts/e2e.sh — see that script for the environment it sets.
package e2e

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

var client = &http.Client{Timeout: 60 * time.Second}

// persistenceMarker must stay constant: the post-restart phase looks for the
// memory the first phase stored.
const persistenceMarker = "omegagrid e2e persistence marker zircon"

func baseURL() string   { return envOr("E2E_BASE_URL", "http://gateway:8000") }
func mockURL() string   { return envOr("E2E_MOCK_URL", "http://mockllm:11434") }
func lockedURL() string { return os.Getenv("E2E_LOCKED_URL") }

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func TestMain(m *testing.M) {
	if err := waitReady(mockURL()+"/__control/health", 60*time.Second); err != nil {
		fmt.Fprintln(os.Stderr, "mockllm never became ready:", err)
		os.Exit(1)
	}
	if err := waitReady(baseURL()+"/health", 90*time.Second); err != nil {
		fmt.Fprintln(os.Stderr, "gateway never became ready:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func waitReady(url string, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	var last error
	probe := &http.Client{Timeout: 5 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := probe.Get(url)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			last = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("%s not ready: %v", url, last)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// req issues a JSON request and decodes the response into a map.
func req(t *testing.T, method, url string, body any) (int, map[string]any) {
	t.Helper()
	status, raw := reqRaw(t, method, url, body)
	out := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s %s: response is not a JSON object (%v): %s", method, url, err, truncate(raw))
		}
	}
	return status, out
}

func reqRaw(t *testing.T, method, url string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		r = bytes.NewReader(b)
	}
	httpReq, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, url, err)
	}
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, url, err)
	}
	return resp.StatusCode, raw
}

// reqList decodes a response whose top level is a JSON array.
func reqList(t *testing.T, method, url string, body any) (int, []any) {
	t.Helper()
	status, raw := reqRaw(t, method, url, body)
	var out []any
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s %s: response is not a JSON array (%v): %s", method, url, err, truncate(raw))
		}
	}
	return status, out
}

func mustOK(t *testing.T, status int, body map[string]any, what string) map[string]any {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("%s: status %d: %v", what, status, body)
	}
	return body
}

// script loads the mock's reply queue and clears its recorded traffic. Every
// test that drives the agent starts with one, so tests never inherit a
// half-drained queue from a neighbour.
func script(t *testing.T, replies ...string) {
	t.Helper()
	status, body := req(t, "POST", mockURL()+"/__control/script", map[string]any{"replies": replies})
	mustOK(t, status, body, "script mockllm")
}

// mockChats returns the chat requests the gateway made since the last script,
// flattened to "<role>: <content>" lines per request.
func mockChats(t *testing.T) [][]string {
	t.Helper()
	status, body := req(t, "GET", mockURL()+"/__control/requests", nil)
	mustOK(t, status, body, "read mock requests")
	chats, _ := body["chats"].([]any)
	out := make([][]string, 0, len(chats))
	for _, c := range chats {
		cm, _ := c.(map[string]any)
		msgs, _ := cm["messages"].([]any)
		lines := make([]string, 0, len(msgs))
		for _, m := range msgs {
			mm, _ := m.(map[string]any)
			lines = append(lines, fmt.Sprintf("%v: %v", mm["role"], mm["content"]))
		}
		out = append(out, lines)
	}
	return out
}

type sseEvent struct {
	Name string
	Data map[string]any
}

// streamQuery POSTs to /api/query/stream and returns the parsed SSE frames.
func streamQuery(t *testing.T, body any) []sseEvent {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	httpReq, err := http.NewRequest("POST", baseURL()+"/api/query/stream", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("build stream request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	resp, err := client.Do(httpReq)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("stream: status %d: %s", resp.StatusCode, truncate(raw))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("stream Content-Type = %q", ct)
	}

	var events []sseEvent
	var name string
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024) // attachments make frames big
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data := map[string]any{}
			payload := strings.TrimPrefix(line, "data: ")
			if err := json.Unmarshal([]byte(payload), &data); err != nil {
				t.Fatalf("event %q carries invalid JSON: %v: %s", name, err, payload)
			}
			events = append(events, sseEvent{Name: name, Data: data})
			name = ""
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read stream: %v", err)
	}
	return events
}

func eventNames(events []sseEvent) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Name)
	}
	return out
}

func lastEvent(t *testing.T, events []sseEvent, name string) map[string]any {
	t.Helper()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Name == name {
			return events[i].Data
		}
	}
	t.Fatalf("no %q event in stream: %v", name, eventNames(events))
	return nil
}

// invocations returns the audit rows recorded for one session.
func invocations(t *testing.T, sessionID int) []map[string]any {
	t.Helper()
	status, body := req(t, "GET",
		fmt.Sprintf("%s/api/invocations?session_id=%d&limit=50", baseURL(), sessionID), nil)
	mustOK(t, status, body, "list invocations")
	raw, _ := body["invocations"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		m, _ := r.(map[string]any)
		out = append(out, m)
	}
	return out
}

func sessionIDOf(t *testing.T, body map[string]any) int {
	t.Helper()
	sid, ok := body["session_id"].(float64)
	if !ok || sid == 0 {
		t.Fatalf("response carries no session_id: %v", body)
	}
	return int(sid)
}

func metaOf(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	meta, ok := body["meta"].(map[string]any)
	if !ok {
		t.Fatalf("response carries no meta: %v", body)
	}
	return meta
}

func jsonString(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func truncate(b []byte) string {
	if len(b) > 600 {
		return string(b[:600]) + "…"
	}
	return string(b)
}

func unique(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("%s-%d", strings.ToLower(t.Name()), time.Now().UnixNano())
}

// ---------------------------------------------------------------------------
// tests
// ---------------------------------------------------------------------------

// The gateway reports itself healthy only when it can reach the embeddings
// backend, so this also proves gateway → mockllm connectivity.
func TestHealth(t *testing.T) {
	status, body := req(t, "GET", baseURL()+"/health", nil)
	mustOK(t, status, body, "health")

	if body["ok"] != true || body["embed_ok"] != true {
		t.Fatalf("gateway unhealthy: %v", body)
	}
	if body["provider"] != "ollama" {
		t.Errorf("provider = %v, want ollama", body["provider"])
	}
	if s, _ := body["chat_model"].(string); s == "" {
		t.Error("chat_model is empty")
	}
	if s, _ := body["embed_error"].(string); s != "" {
		t.Errorf("embed_error = %q", s)
	}
}

// The whole non-streaming path: query in, tool executed, result fed back to the
// model, final answer out, audit row written.
func TestQueryRunsToolThenAnswers(t *testing.T) {
	script(t,
		`{"type":"tool_call","tool":"math_eval","args":{"expression":"2+2*3"},"why":"arithmetic"}`,
		`{"type":"final","answer":"The answer is 8."}`,
	)

	status, body := req(t, "POST", baseURL()+"/api/query", map[string]any{"query": "what is 2+2*3?"})
	mustOK(t, status, body, "query")

	if body["answer"] != "The answer is 8." {
		t.Fatalf("answer = %v", body["answer"])
	}
	meta := metaOf(t, body)
	if meta["step_count"] != float64(2) {
		t.Errorf("meta.step_count = %v, want 2", meta["step_count"])
	}
	if _, isFallback := meta["fallback"]; isFallback {
		t.Errorf("meta.fallback set on a clean run: %v", meta)
	}
	if s, _ := meta["model"].(string); s == "" {
		t.Error("meta.model is empty")
	}
	if s, _ := body["debug_log"].(string); !strings.Contains(s, "math_eval") {
		t.Errorf("debug_log does not mention the tool:\n%s", s)
	}

	// The audit log records the call with its arguments and timing.
	sid := sessionIDOf(t, body)
	recs := invocations(t, sid)
	if len(recs) != 1 {
		t.Fatalf("got %d audit rows, want 1: %v", len(recs), recs)
	}
	rec := recs[0]
	if rec["skill"] != "math_eval" || rec["kind"] != "skill" {
		t.Errorf("audit row = %v", rec)
	}
	if s := jsonString(t, rec["args"]); !strings.Contains(s, "2+2*3") {
		t.Errorf("audit args = %s", s)
	}
	if s := jsonString(t, rec["result"]); !strings.Contains(s, "8") {
		t.Errorf("audit result = %s", s)
	}

	// And the model really was handed the tool's output on the second turn.
	chats := mockChats(t)
	if len(chats) != 2 {
		t.Fatalf("mock saw %d chat requests, want 2", len(chats))
	}
	if !strings.Contains(strings.Join(chats[0], "\n"), "math_eval(") {
		t.Errorf("system prompt does not advertise math_eval:\n%s", strings.Join(chats[0], "\n"))
	}
	second := strings.Join(chats[1], "\n")
	if !strings.Contains(second, `"result":8`) {
		t.Errorf("second turn does not carry the tool result:\n%s", second)
	}
}

// Streaming must emit the same run as thinking → tool_call → tool_result →
// thinking → final, and the final frame must carry the answer and session.
func TestQueryStreamEmitsOrderedEvents(t *testing.T) {
	script(t,
		`{"type":"tool_call","tool":"datetime_skill","args":{},"why":"clock"}`,
		`{"type":"final","answer":"streamed answer"}`,
	)

	events := streamQuery(t, map[string]any{"query": "what time is it?"})
	got := eventNames(events)
	want := []string{"thinking", "tool_call", "tool_result", "thinking", "final"}
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}

	call := events[1].Data
	if call["tool"] != "datetime_skill" || call["why"] != "clock" {
		t.Errorf("tool_call event = %v", call)
	}
	result := events[2].Data
	if s, _ := result["result"].(string); !strings.Contains(s, "unix_timestamp") {
		t.Errorf("tool_result = %v", result)
	}
	final := events[4].Data
	if final["answer"] != "streamed answer" {
		t.Errorf("final answer = %v", final["answer"])
	}
	if sid, _ := final["session_id"].(float64); sid == 0 {
		t.Error("final event carries no session_id")
	}
}

// Binary skill output must arrive as an attachment while the heavy base64 stays
// out of the model's context.
func TestImageAttachmentReachesTheClient(t *testing.T) {
	script(t,
		`{"type":"tool_call","tool":"qr_generate","args":{"data":"https://example.com","box_size":4},"why":"qr"}`,
		`{"type":"final","answer":"here is your QR code"}`,
	)

	events := streamQuery(t, map[string]any{"query": "make a qr code"})
	final := lastEvent(t, events, "final")

	atts, _ := final["attachments"].([]any)
	if len(atts) != 1 {
		t.Fatalf("attachments = %v, want 1", final["attachments"])
	}
	att, _ := atts[0].(map[string]any)
	if att["mime_type"] != "image/png" || att["type"] != "image" {
		t.Errorf("attachment = %v", att)
	}
	b64, _ := att["base64"].(string)
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("attachment is not valid base64: %v", err)
	}
	if !bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")) {
		t.Errorf("attachment is not a PNG (%d bytes)", len(raw))
	}

	// The tool_result the model saw must have the payload stripped.
	toolResult := lastEvent(t, events, "tool_result")
	s, _ := toolResult["result"].(string)
	if strings.Contains(s, b64[:32]) {
		t.Error("base64 payload leaked into the model's tool result")
	}
	if !strings.Contains(s, "_image_attached") {
		t.Errorf("tool result does not flag the attachment: %s", s)
	}
}

// A hallucinated tool name is a bad reply, not a broken run: the agent reports
// it and lets the model pick again.
func TestUnknownToolIsRecoverable(t *testing.T) {
	script(t,
		`{"type":"tool_call","tool":"definitely_not_a_tool","args":{},"why":"oops"}`,
		`{"type":"final","answer":"recovered"}`,
	)

	status, body := req(t, "POST", baseURL()+"/api/query", map[string]any{"query": "use a bogus tool"})
	mustOK(t, status, body, "query")
	if body["answer"] != "recovered" {
		t.Fatalf("answer = %v", body["answer"])
	}

	recs := invocations(t, sessionIDOf(t, body))
	if len(recs) != 1 || recs[0]["kind"] != "unknown" {
		t.Fatalf("audit rows = %v, want one row with kind=unknown", recs)
	}

	chats := mockChats(t)
	if len(chats) < 2 || !strings.Contains(strings.Join(chats[1], "\n"), "Unknown tool/skill") {
		t.Error("the model was not told the tool does not exist")
	}
}

// A model that stops emitting JSON must degrade to a polite answer on both
// endpoints — they used to disagree, with the stream surfacing raw parser text.
func TestNonJSONReplyFallsBackOnBothEndpoints(t *testing.T) {
	const notJSON = "I'm afraid I can't do that, Dave."

	script(t, notJSON)
	status, body := req(t, "POST", baseURL()+"/api/query", map[string]any{"query": "hello"})
	mustOK(t, status, body, "query")
	answer, _ := body["answer"].(string)
	if answer == "" || strings.Contains(answer, "did not return JSON") {
		t.Fatalf("answer = %q, want the graceful fallback", answer)
	}
	if metaOf(t, body)["fallback"] != true {
		t.Errorf("meta.fallback = %v, want true", metaOf(t, body)["fallback"])
	}

	script(t, notJSON)
	events := streamQuery(t, map[string]any{"query": "hello"})
	if names := eventNames(events); contains(names, "error") {
		t.Fatalf("stream emitted an error event: %v", names)
	}
	final := lastEvent(t, events, "final")
	if final["answer"] != answer {
		t.Errorf("stream answer %q != non-streaming answer %q", final["answer"], answer)
	}
	meta, _ := final["meta"].(map[string]any)
	if meta["fallback"] != true {
		t.Errorf("stream meta.fallback = %v, want true", meta["fallback"])
	}
}

// A model that never finishes must be cut off at max_steps.
func TestMaxStepsIsEnforced(t *testing.T) {
	loop := `{"type":"tool_call","tool":"datetime_skill","args":{},"why":"again"}`
	script(t, loop, loop, loop, loop)

	status, body := req(t, "POST", baseURL()+"/api/query",
		map[string]any{"query": "loop forever", "max_steps": 2})
	mustOK(t, status, body, "query")

	if s, _ := body["answer"].(string); !strings.Contains(s, "could not finish") {
		t.Fatalf("answer = %v", body["answer"])
	}
	meta := metaOf(t, body)
	if meta["max_steps_hit"] != true {
		t.Errorf("meta.max_steps_hit = %v", meta["max_steps_hit"])
	}
	if meta["step_count"] != float64(2) {
		t.Errorf("meta.step_count = %v, want 2", meta["step_count"])
	}
}

// Vector memory through the REST API: store, find, list, dedup, delete.
func TestMemoryRoundTrip(t *testing.T) {
	text := "The e2e suite prefers " + unique(t) + " tea in the afternoon"

	status, body := req(t, "POST", baseURL()+"/api/memory/add",
		map[string]any{"text": text, "meta": map[string]any{"tag": "e2e"}})
	mustOK(t, status, body, "memory add")
	id, _ := body["memory_id"].(string)
	if id == "" || body["skipped"] != false {
		t.Fatalf("add = %v", body)
	}

	status, body = req(t, "POST", baseURL()+"/api/memory/search",
		map[string]any{"query": text, "k": 5})
	mustOK(t, status, body, "memory search")
	hits, _ := body["hits"].([]any)
	if len(hits) == 0 {
		t.Fatal("search returned no hits for the text just stored")
	}
	top, _ := hits[0].(map[string]any)
	if top["text"] != text {
		t.Errorf("top hit = %v, want the stored text", top["text"])
	}

	// Storing the identical text again is deduplicated, not duplicated.
	status, body = req(t, "POST", baseURL()+"/api/memory/add", map[string]any{"text": text})
	mustOK(t, status, body, "memory add (duplicate)")
	if body["skipped"] != true {
		t.Errorf("duplicate add was not skipped: %v", body)
	}

	status, body = req(t, "GET", baseURL()+"/api/memory/"+id, nil)
	mustOK(t, status, body, "memory get")

	status, body = req(t, "DELETE", baseURL()+"/api/memory/"+id, nil)
	mustOK(t, status, body, "memory delete")

	status, _ = req(t, "GET", baseURL()+"/api/memory/"+id, nil)
	if status != http.StatusNotFound {
		t.Errorf("get after delete: status %d, want 404", status)
	}
}

// vector_add / vector_search are agent-loop tools rather than registry skills;
// drive them the way the model does and check the hit comes back into context.
func TestVectorToolsInTheAgentLoop(t *testing.T) {
	fact := "Project " + unique(t) + " ships on Friday"
	script(t,
		fmt.Sprintf(`{"type":"tool_call","tool":"vector_add","args":{"text":%q},"why":"remember"}`, fact),
		fmt.Sprintf(`{"type":"tool_call","tool":"vector_search","args":{"query":%q,"k":3},"why":"recall"}`, fact),
		`{"type":"final","answer":"stored and recalled"}`,
	)

	status, body := req(t, "POST", baseURL()+"/api/query", map[string]any{"query": "remember this"})
	mustOK(t, status, body, "query")
	if body["answer"] != "stored and recalled" {
		t.Fatalf("answer = %v", body["answer"])
	}

	recs := invocations(t, sessionIDOf(t, body))
	if len(recs) != 2 {
		t.Fatalf("got %d audit rows, want 2: %v", len(recs), recs)
	}
	for _, r := range recs {
		if r["kind"] != "tool" {
			t.Errorf("vector_* should be audited as kind=tool, got %v", r["kind"])
		}
	}

	chats := mockChats(t)
	if len(chats) < 3 {
		t.Fatalf("mock saw %d chat requests, want 3", len(chats))
	}
	if !strings.Contains(strings.Join(chats[2], "\n"), fact) {
		t.Error("the search result never reached the model's context")
	}
}

// The scheduler is a background goroutine inside the gateway process — only an
// e2e run can prove a created task actually fires.
func TestSchedulerRunsTaskInBackground(t *testing.T) {
	message := "e2e reminder " + unique(t)
	status, task := req(t, "POST", baseURL()+"/api/scheduler/tasks", map[string]any{
		"name":      "e2e-task",
		"cron_expr": "* * * * *",
		"skill":     "reminder",
		"args":      map[string]any{"message": message},
	})
	mustOK(t, status, task, "create task")
	idF, _ := task["id"].(float64)
	if idF == 0 {
		t.Fatalf("created task has no id: %v", task)
	}
	id := int(idF)
	url := fmt.Sprintf("%s/api/scheduler/tasks/%d", baseURL(), id)
	t.Cleanup(func() { req(t, "DELETE", url, nil) })

	deadline := time.Now().Add(90 * time.Second)
	var latest map[string]any
	for time.Now().Before(deadline) {
		status, latest = req(t, "GET", url, nil)
		mustOK(t, status, latest, "get task")
		if n, _ := latest["run_count"].(float64); n >= 1 {
			break
		}
		time.Sleep(time.Second)
	}
	if n, _ := latest["run_count"].(float64); n < 1 {
		t.Fatalf("task never ran within the deadline: %v", latest)
	}
	if s, _ := latest["last_result"].(string); !strings.Contains(s, message) {
		t.Errorf("last_result = %q, want the reminder message", s)
	}

	// The list endpoint answers with a bare JSON array, not an envelope.
	status, tasks := reqList(t, "GET", baseURL()+"/api/scheduler/tasks", nil)
	if status != http.StatusOK {
		t.Fatalf("list tasks: status %d", status)
	}
	var listed bool
	for _, entry := range tasks {
		m, _ := entry.(map[string]any)
		if n, _ := m["id"].(float64); int(n) == id {
			listed = true
		}
	}
	if !listed {
		t.Errorf("task %d missing from the task list (%d tasks)", id, len(tasks))
	}

	status, body := req(t, "POST", url+"/disable", nil)
	mustOK(t, status, body, "disable task")
	status, latest = req(t, "GET", url, nil)
	mustOK(t, status, latest, "get task")
	if latest["enabled"] != false {
		t.Errorf("task still enabled after disable: %v", latest)
	}

	status, body = req(t, "DELETE", url, nil)
	mustOK(t, status, body, "delete task")
	if status, _ := req(t, "GET", url, nil); status != http.StatusNotFound {
		t.Errorf("get after delete: status %d, want 404", status)
	}
}

// The playground runs a skill outside the agent loop; skill_creator writes a
// file into SKILLS_DIR and hot-registers it, so this covers the container's
// filesystem and the frontmatter round-trip in one go.
func TestPlaygroundAndSkillCreator(t *testing.T) {
	status, body := req(t, "POST", baseURL()+"/api/skills/math_eval/invoke",
		map[string]any{"args": map[string]any{"expression": "6*7"}})
	mustOK(t, status, body, "invoke math_eval")
	if body["error"] != nil {
		t.Fatalf("math_eval error: %v", body["error"])
	}
	result, _ := body["result"].(map[string]any)
	if result["result"] != float64(42) {
		t.Fatalf("math_eval result = %v", result)
	}

	// A description full of YAML indicators: the generated frontmatter has to
	// survive being read back, or the skill would be written but unloadable.
	const name = "e2e_created_skill"
	const desc = "*/5 * * * * report - see docs"
	status, body = req(t, "POST", baseURL()+"/api/skills/skill_creator/invoke", map[string]any{
		"args": map[string]any{
			"action":       "create",
			"name":         name,
			"description":  desc,
			"instructions": "Summarise the numbers you were given.",
		},
	})
	mustOK(t, status, body, "invoke skill_creator")
	created, _ := body["result"].(map[string]any)
	if created["error"] != nil {
		t.Fatalf("skill_creator: %v", created["error"])
	}
	if s, _ := created["status"].(string); s != "created" && s != "updated" {
		t.Fatalf("skill_creator status = %v", created)
	}

	status, body = req(t, "GET", baseURL()+"/api/skills", nil)
	mustOK(t, status, body, "list skills")
	skills, _ := body["skills"].([]any)
	var found map[string]any
	for _, s := range skills {
		m, _ := s.(map[string]any)
		if m["name"] == name {
			found = m
		}
	}
	if found == nil {
		t.Fatalf("%s was not hot-registered (%d skills listed)", name, len(skills))
	}
	if found["description"] != desc {
		t.Errorf("description round-trip = %q, want %q", found["description"], desc)
	}

	status, body = req(t, "POST", baseURL()+"/api/skills/"+name+"/invoke",
		map[string]any{"args": map[string]any{}})
	mustOK(t, status, body, "invoke created skill")
	res, _ := body["result"].(map[string]any)
	if res["skill_type"] != "prompt_only" {
		t.Errorf("created skill result = %v", res)
	}
}

// An HTTP skill pointed at the mock proves outbound requests work inside the
// isolated network — without reaching the internet.
func TestHTTPSkillReachesMockInsideNetwork(t *testing.T) {
	status, body := req(t, "POST", baseURL()+"/api/skills/http_health/invoke",
		map[string]any{"args": map[string]any{"url": mockURL() + "/__control/health"}})
	mustOK(t, status, body, "invoke http_health")
	result, _ := body["result"].(map[string]any)
	if result["ok"] != true || result["status_code"] != float64(200) {
		t.Fatalf("http_health against the mock = %v", result)
	}
}

// Credential-handling skills must not write their arguments into the audit log,
// which /api/invocations serves without authentication.
func TestSensitiveSkillIsRedactedInAudit(t *testing.T) {
	// header {"alg":"HS256"} payload {"sub":"e2e-secret-subject"}
	const token = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJlMmUtc2VjcmV0LXN1YmplY3QifQ.signature"
	script(t,
		fmt.Sprintf(`{"type":"tool_call","tool":"jwt_inspect","args":{"token":%q},"why":"decode"}`, token),
		`{"type":"final","answer":"decoded"}`,
	)

	status, body := req(t, "POST", baseURL()+"/api/query", map[string]any{"query": "decode this token"})
	mustOK(t, status, body, "query")

	recs := invocations(t, sessionIDOf(t, body))
	if len(recs) != 1 {
		t.Fatalf("got %d audit rows, want 1", len(recs))
	}
	blob := jsonString(t, recs[0])
	if strings.Contains(blob, token) || strings.Contains(blob, "e2e-secret-subject") {
		t.Errorf("the token or its claims leaked into the audit log: %s", blob)
	}
	if !strings.Contains(blob, "redacted") {
		t.Errorf("audit row is not marked redacted: %s", blob)
	}
}

// The MCP endpoint is a separate protocol surface on the same binary.
func TestMCPEndpoint(t *testing.T) {
	rpc := func(id int, method string, params any) map[string]any {
		payload := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
		if params != nil {
			payload["params"] = params
		}
		status, body := req(t, "POST", baseURL()+"/mcp", payload)
		mustOK(t, status, body, "mcp "+method)
		return body
	}

	init := rpc(1, "initialize", map[string]any{"protocolVersion": "2025-06-18"})
	result, _ := init["result"].(map[string]any)
	if result["protocolVersion"] != "2025-06-18" {
		t.Errorf("initialize = %v", result)
	}
	info, _ := result["serverInfo"].(map[string]any)
	if s, _ := info["name"].(string); s == "" {
		t.Errorf("serverInfo = %v", info)
	}

	list := rpc(2, "tools/list", nil)
	result, _ = list["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("tools/list returned nothing")
	}
	var names []string
	for _, tl := range tools {
		m, _ := tl.(map[string]any)
		n, _ := m["name"].(string)
		names = append(names, n)
	}
	for _, want := range []string{"math_eval", "schedule_task", "web_search"} {
		if !contains(names, want) {
			t.Errorf("tools/list missing %q: %v", want, names)
		}
	}
	// Stable ordering: MCP clients cache this list.
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("tools/list is not name-sorted: %v", names)
			break
		}
	}

	call := rpc(3, "tools/call", map[string]any{
		"name": "math_eval", "arguments": map[string]any{"expression": "20+22"},
	})
	result, _ = call["result"].(map[string]any)
	if result["isError"] != false {
		t.Errorf("tools/call isError = %v", result["isError"])
	}
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("tools/call returned no content: %v", result)
	}
	first, _ := content[0].(map[string]any)
	if s, _ := first["text"].(string); !strings.Contains(s, "42") {
		t.Errorf("tools/call text = %v", first["text"])
	}

	bad := rpc(4, "no/such/method", nil)
	rpcErr, _ := bad["error"].(map[string]any)
	if code, _ := rpcErr["code"].(float64); code != -32601 {
		t.Errorf("unknown method error = %v, want code -32601", rpcErr)
	}
}

// A second gateway runs with the playground and MCP switched off, so the config
// flags are proven to reach the router — not just the config struct.
func TestDisabledFeaturesAreEnforced(t *testing.T) {
	if lockedURL() == "" {
		t.Skip("E2E_LOCKED_URL not set")
	}
	if err := waitReady(lockedURL()+"/health", 60*time.Second); err != nil {
		t.Fatalf("locked gateway not ready: %v", err)
	}

	status, body := req(t, "POST", lockedURL()+"/api/skills/math_eval/invoke",
		map[string]any{"args": map[string]any{"expression": "1+1"}})
	if status != http.StatusForbidden {
		t.Errorf("playground invoke: status %d, want 403 (%v)", status, body)
	}

	// The route is not mounted at all, so chi answers with its plain-text 404 —
	// read it raw rather than through the JSON helper.
	status, raw := reqRaw(t, "POST", lockedURL()+"/mcp",
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
	if status != http.StatusNotFound {
		t.Errorf("/mcp: status %d, want 404 when MCP_SERVER_DISABLED=true (body: %s)",
			status, truncate(raw))
	}

	// The core API still works on the locked instance.
	status, body = req(t, "GET", lockedURL()+"/api/skills", nil)
	mustOK(t, status, body, "locked /api/skills")
}

// Sessions persist the conversation, and a reused session id continues it.
func TestSessionHistoryIsRecorded(t *testing.T) {
	status, body := req(t, "POST", baseURL()+"/api/sessions/new", nil)
	mustOK(t, status, body, "new session")
	sid := sessionIDOf(t, body)

	const question = "what is the capital of France?"
	script(t, `{"type":"final","answer":"Paris."}`)
	status, body = req(t, "POST", baseURL()+"/api/query",
		map[string]any{"query": question, "session_id": sid})
	mustOK(t, status, body, "query")
	if got := sessionIDOf(t, body); got != sid {
		t.Fatalf("session_id = %d, want the one supplied (%d)", got, sid)
	}

	status, body = req(t, "GET", fmt.Sprintf("%s/api/sessions/%d/messages", baseURL(), sid), nil)
	mustOK(t, status, body, "session messages")
	msgs, _ := body["messages"].([]any)
	if len(msgs) < 2 {
		t.Fatalf("session has %d messages, want at least user+assistant", len(msgs))
	}
	first, _ := msgs[0].(map[string]any)
	if first["role"] != "user" || first["content"] != question {
		t.Errorf("first message = %v", first)
	}
	last, _ := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "assistant" || last["content"] != "Paris." {
		t.Errorf("last message = %v", last)
	}

	// A follow-up on the same session replays the tail to the model.
	script(t, `{"type":"final","answer":"It has about 2.1 million people."}`)
	status, body = req(t, "POST", baseURL()+"/api/query",
		map[string]any{"query": "how many people live there?", "session_id": sid})
	mustOK(t, status, body, "follow-up query")
	chats := mockChats(t)
	if len(chats) == 0 {
		t.Fatal("mock saw no chat request")
	}
	if !strings.Contains(strings.Join(chats[0], "\n"), question) {
		t.Error("the follow-up turn did not include the earlier question in context")
	}
}

// The stack must not be able to reach the internet: if this ever passes, the
// suite has stopped being hermetic and its results can silently depend on the
// outside world.
func TestNetworkIsolation(t *testing.T) {
	if os.Getenv("E2E_EXPECT_ISOLATED") != "1" {
		t.Skip("E2E_EXPECT_ISOLATED != 1 (host mode is not network-isolated)")
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	for _, addr := range []string{"example.com:443", "1.1.1.1:443"} {
		conn, err := d.Dial("tcp", addr)
		if err == nil {
			conn.Close()
			t.Fatalf("reached %s from inside the e2e network — the stack is not isolated", addr)
		}
		t.Logf("%s unreachable as expected: %v", addr, err)
	}
}

// Seeds state that TestPersistenceSurvivesRestart looks for after the gateway
// process has been restarted by scripts/e2e.sh.
func TestPersistenceSeed(t *testing.T) {
	status, body := req(t, "POST", baseURL()+"/api/memory/add",
		map[string]any{"text": persistenceMarker, "meta": map[string]any{"tag": "e2e-persistence"}})
	mustOK(t, status, body, "seed memory")
	if body["memory_id"] == "" {
		t.Fatalf("seed add = %v", body)
	}

	script(t, `{"type":"final","answer":"seeded"}`)
	status, body = req(t, "POST", baseURL()+"/api/query",
		map[string]any{"query": persistenceMarker})
	mustOK(t, status, body, "seed session")
}

// SQLite history and the chromem collection live on a volume; a restarted
// gateway must come back to the same data.
func TestPersistenceSurvivesRestart(t *testing.T) {
	if os.Getenv("E2E_PHASE") != "post-restart" {
		t.Skip("only meaningful after scripts/e2e.sh restarts the gateway")
	}

	status, body := req(t, "POST", baseURL()+"/api/memory/search",
		map[string]any{"query": persistenceMarker, "k": 5})
	mustOK(t, status, body, "search after restart")
	hits, _ := body["hits"].([]any)
	var found bool
	for _, h := range hits {
		m, _ := h.(map[string]any)
		if m["text"] == persistenceMarker {
			found = true
		}
	}
	if !found {
		t.Fatalf("the seeded memory did not survive the restart: %v", hits)
	}

	// Re-adding the same text is still deduplicated after a restart. The
	// SHA256 index is in-memory only and starts empty, so this goes through the
	// semantic check against the vectors loaded from disk — which is exactly
	// what proves the collection was persisted and reopened.
	status, body = req(t, "POST", baseURL()+"/api/memory/add",
		map[string]any{"text": persistenceMarker})
	mustOK(t, status, body, "re-add after restart")
	if body["skipped"] != true {
		t.Errorf("re-adding the seeded text was not deduplicated: %v", body)
	}
	t.Logf("dedup reason after restart: %v", body["reason"])

	status, body = req(t, "GET", baseURL()+"/api/sessions?limit=50", nil)
	mustOK(t, status, body, "sessions after restart")
	sessions, _ := body["sessions"].([]any)
	if len(sessions) == 0 {
		t.Fatal("no sessions survived the restart")
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
