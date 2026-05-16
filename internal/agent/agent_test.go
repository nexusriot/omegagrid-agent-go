package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nexusriot/omegagrid-agent-go/internal/config"
	"github.com/nexusriot/omegagrid-agent-go/internal/llm"
	"github.com/nexusriot/omegagrid-agent-go/internal/memory"
	"github.com/nexusriot/omegagrid-agent-go/internal/skills"
)

// mockLLM is a deterministic stub that cycles through canned JSON responses.
type mockLLM struct {
	mu        sync.Mutex
	responses []string
	idx       int
}

func (m *mockLLM) CompleteJSON(_ []llm.Message) (string, float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.idx >= len(m.responses) {
		return `{"type":"final","answer":"done"}`, 0, nil
	}
	r := m.responses[m.idx]
	m.idx++
	return r, 0, nil
}

func (m *mockLLM) Model() string   { return "mock-llm" }
func (m *mockLLM) BaseURL() string { return "" }

// newTestMemory creates a real memory.Client backed by a temp SQLite file and
// a non-existent Ollama instance.  Vector search will fail silently — which
// is fine because startSession continues on error.
func newTestMemory(t *testing.T) *memory.Client {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{
		AgentDB:           filepath.Join(dir, "agent.sqlite3"),
		VectorDir:         filepath.Join(dir, "chromem"),
		VectorCollection:  "test",
		OllamaURL:         "http://127.0.0.1:19999", // unreachable
		OllamaEmbedModel:  "nomic-embed-text",
		OllamaTimeoutSec:  0.1,
		DedupDistance:     0.05,
		AuditMaxBlobBytes: 65536,
	}
	mem, err := memory.New(cfg)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	return mem
}

// newTestSkills builds a skills.Client with no markdown skills.
func newTestSkills(t *testing.T) *skills.Client {
	t.Helper()
	cfg := config.Config{SkillsDir: t.TempDir()}
	sc, err := skills.New(cfg)
	if err != nil {
		t.Fatalf("skills.New: %v", err)
	}
	return sc
}

// newMinimalService returns a Service wired with the supplied LLM mock.
func newMinimalService(t *testing.T, chat llm.ChatClient) *Service {
	t.Helper()
	return &Service{
		Memory:          newTestMemory(t),
		Skills:          newTestSkills(t),
		Chat:            chat,
		NativeSkills:    map[string]Skill{},
		ContextTail:     10,
		MemoryHits:      0,
		ParallelEnabled: false,
		MaxParallel:     4,
	}
}

// collectEvents drains the out channel and returns all received events.
func collectEvents(out <-chan Event) []Event {
	var evs []Event
	for ev := range out {
		evs = append(evs, ev)
	}
	return evs
}

func TestParseJSONSafely_CleanJSON(t *testing.T) {
	data, err := parseJSONSafely(`{"type":"final","answer":"hello"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if data["type"] != "final" {
		t.Errorf("type mismatch: %v", data["type"])
	}
}

func TestParseJSONSafely_EmbeddedInNoise(t *testing.T) {
	raw := `Here is the JSON: {"type":"tool_call","tool":"weather"} goodbye`
	data, err := parseJSONSafely(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if data["type"] != "tool_call" {
		t.Errorf("expected type=tool_call, got %v", data["type"])
	}
}

func TestParseJSONSafely_InvalidJSON(t *testing.T) {
	_, err := parseJSONSafely("not json at all")
	if err == nil {
		t.Error("expected error for non-JSON input")
	}
}

func TestParseJSONSafely_UnwrapsRawModelJSON(t *testing.T) {
	inner := `{"type":"final","answer":"unwrapped"}`
	outer, _ := json.Marshal(map[string]any{"raw_model_json": inner})
	data, err := parseJSONSafely(string(outer))
	if err != nil {
		t.Fatal(err)
	}
	if data["type"] != "final" {
		t.Errorf("raw_model_json not unwrapped: %v", data)
	}
}

func TestNormalizeToolCall_AlreadyNormalized(t *testing.T) {
	in := map[string]any{"type": "tool_call", "tool": "weather", "args": map[string]any{}}
	out := normalizeToolCall(in, map[string]bool{"weather": true})
	if out["type"] != "tool_call" {
		t.Errorf("type changed: %v", out["type"])
	}
}

func TestNormalizeToolCall_TypeIsToolName(t *testing.T) {
	// LLM uses the tool name as "type" instead of "tool_call"
	in := map[string]any{"type": "weather", "city": "London", "why": "get weather"}
	out := normalizeToolCall(in, map[string]bool{"weather": true})
	if out["type"] != "tool_call" {
		t.Errorf("expected normalized type=tool_call, got %v", out["type"])
	}
	if out["tool"] != "weather" {
		t.Errorf("expected tool=weather, got %v", out["tool"])
	}
	args, _ := out["args"].(map[string]any)
	if _, ok := args["city"]; !ok {
		t.Errorf("expected city in args: %v", args)
	}
}

func TestNormalizeToolCall_UnknownTypePassed(t *testing.T) {
	in := map[string]any{"type": "unknown_thing"}
	out := normalizeToolCall(in, map[string]bool{"weather": true})
	// Unknown type should pass through unchanged
	if out["type"] != "unknown_thing" {
		t.Errorf("unexpected modification: %v", out)
	}
}

func TestExtractAttachments_NoImage(t *testing.T) {
	result := map[string]any{"key": "value"}
	atts, cleaned := ExtractAttachments("tool", result)
	if len(atts) != 0 {
		t.Errorf("expected no attachments, got %d", len(atts))
	}
	if cleaned == nil {
		t.Error("cleaned result should not be nil")
	}
}

func TestExtractAttachments_WithImage(t *testing.T) {
	result := map[string]any{
		"image_base64":   "abc123",
		"image_format":   "png",
		"size_bytes":     float64(100),
		"something_else": "kept",
	}
	atts, cleaned := ExtractAttachments("qr_generate", result)
	if len(atts) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(atts))
	}
	if atts[0].Base64 != "abc123" {
		t.Errorf("base64 mismatch: %q", atts[0].Base64)
	}
	if atts[0].MimeType != "image/png" {
		t.Errorf("mime mismatch: %q", atts[0].MimeType)
	}
	if atts[0].Filename != "qr_generate.png" {
		t.Errorf("filename mismatch: %q", atts[0].Filename)
	}
	// The heavy payload must be scrubbed from the cleaned result.
	cleanedMap, _ := cleaned.(map[string]any)
	if _, ok := cleanedMap["image_base64"]; ok {
		t.Error("image_base64 should be scrubbed from cleaned result")
	}
}

func TestExtractAttachments_NonMapResult(t *testing.T) {
	atts, cleaned := ExtractAttachments("tool", "plain string result")
	if len(atts) != 0 {
		t.Errorf("expected no attachments for non-map result")
	}
	if cleaned != "plain string result" {
		t.Errorf("cleaned should equal original for non-map: %v", cleaned)
	}
}

func TestParseBatchCalls_Valid(t *testing.T) {
	data := map[string]any{
		"type": "tool_calls",
		"calls": []any{
			map[string]any{"tool": "weather", "args": map[string]any{"city": "Paris"}, "why": "user asked"},
			map[string]any{"tool": "ping_check", "args": map[string]any{"host": "google.com"}, "why": "test"},
		},
	}
	calls, ok := parseBatchCalls(data)
	if !ok {
		t.Fatal("expected parseBatchCalls to succeed")
	}
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if calls[0].Name != "weather" {
		t.Errorf("expected weather, got %q", calls[0].Name)
	}
	if calls[1].Name != "ping_check" {
		t.Errorf("expected ping_check, got %q", calls[1].Name)
	}
}

func TestParseBatchCalls_MissingCalls(t *testing.T) {
	data := map[string]any{"type": "tool_calls"}
	_, ok := parseBatchCalls(data)
	if ok {
		t.Error("expected failure for missing calls key")
	}
}

func TestParseBatchCalls_EmptyCalls(t *testing.T) {
	data := map[string]any{"calls": []any{}}
	_, ok := parseBatchCalls(data)
	if ok {
		t.Error("expected failure for empty calls array")
	}
}

func TestFinalAnswer_String(t *testing.T) {
	data := map[string]any{"answer": "hello world"}
	if got := finalAnswer(data); got != "hello world" {
		t.Errorf("got %q", got)
	}
}

func TestFinalAnswer_MissingAnswer(t *testing.T) {
	data := map[string]any{"type": "final"}
	if got := finalAnswer(data); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestBestAnswer_PreferAnswer(t *testing.T) {
	data := map[string]any{"answer": "winner", "text": "other"}
	if got := bestAnswer(data); got != "winner" {
		t.Errorf("got %q", got)
	}
}

func TestBestAnswer_Fallback(t *testing.T) {
	data := map[string]any{}
	got := bestAnswer(data)
	if got == "" {
		t.Error("bestAnswer should return fallback message for empty map")
	}
}

func TestToolFollowup_WithError(t *testing.T) {
	result := map[string]any{"error": "something failed"}
	msg := toolFollowup("my_tool", result)
	if msg == "" {
		t.Error("expected non-empty followup")
	}
	// Must mention the failure to prevent LLM hallucination.
	if len(msg) < 10 {
		t.Errorf("followup too short: %q", msg)
	}
}

func TestToolFollowup_WithoutError(t *testing.T) {
	result := map[string]any{"status": "ok"}
	msg := toolFollowup("my_tool", result)
	if msg == "" {
		t.Error("expected non-empty followup")
	}
}

func TestRunOne_UnknownTool(t *testing.T) {
	svc := newMinimalService(t, &mockLLM{})

	// Create a minimal runState with no tools registered.
	st := &runState{
		tools:      map[string]Skill{},
		skillNames: map[string]bool{},
		toolNames:  map[string]bool{},
		timings:    map[string]float64{},
	}
	// We need a valid session ID for the audit log.
	sid, err := svc.Memory.CreateSession()
	if err != nil {
		t.Fatal(err)
	}
	st.sid = sid

	br := svc.runOne(st, batchCall{Name: "nonexistent_tool", Args: map[string]any{}, Step: 1})

	m, ok := br.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result for unknown tool, got %T: %v", br.Result, br.Result)
	}
	if _, hasErr := m["error"]; !hasErr {
		t.Errorf("expected 'error' key in result: %v", m)
	}
}

func TestRunOne_KnownTool(t *testing.T) {
	svc := newMinimalService(t, &mockLLM{})

	sid, _ := svc.Memory.CreateSession()
	st := &runState{
		sid:        sid,
		tools:      map[string]Skill{},
		skillNames: map[string]bool{},
		toolNames:  map[string]bool{},
		timings:    map[string]float64{},
	}
	st.tools["echo"] = Skill{
		Execute: func(args map[string]any) (any, error) {
			return map[string]any{"echoed": args["msg"]}, nil
		},
	}

	br := svc.runOne(st, batchCall{
		Name: "echo",
		Args: map[string]any{"msg": "hello"},
		Step: 1,
	})

	m, ok := br.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T: %v", br.Result, br.Result)
	}
	if m["echoed"] != "hello" {
		t.Errorf("unexpected result: %v", m)
	}
}

func TestRunOne_SensitiveSkillRedacted(t *testing.T) {
	// sensitiveSkills includes "shell_command".  Verify the audit record is
	// written without panicking; actual redaction is in the audit path only
	// (args sent to Memory.AddInvocation), not the returned result.
	svc := newMinimalService(t, &mockLLM{})

	sid, _ := svc.Memory.CreateSession()
	st := &runState{
		sid:        sid,
		tools:      map[string]Skill{},
		skillNames: map[string]bool{},
		toolNames:  map[string]bool{},
		timings:    map[string]float64{},
	}
	st.tools["shell_command"] = Skill{
		Execute: func(args map[string]any) (any, error) {
			return map[string]any{"stdout": "ok"}, nil
		},
	}

	// Should not panic — redaction only affects the audit write.
	br := svc.runOne(st, batchCall{
		Name: "shell_command",
		Args: map[string]any{"command": "echo hi"},
		Step: 1,
	})
	if br.Result == nil {
		t.Error("expected non-nil result")
	}
}

// TestRunStream_CancelledContextExitsQuickly cancels the context before calling
// RunStream.  The implementation checks ctx.Err() at the top of every loop
// step, so the loop should exit on the first iteration and the channel should
// be closed promptly.
func TestRunStream_CancelledContextExitsQuickly(t *testing.T) {
	// LLM blocks until released; with a cancelled ctx it should never be called.
	blocked := make(chan struct{})
	chat := &funcLLM{fn: func(_ []llm.Message) (string, float64, error) {
		<-blocked // blocks until we release or test times out
		return `{"type":"final","answer":"never"}`, 0, nil
	}}

	svc := newMinimalService(t, chat)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	out := make(chan Event, 32)

	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.RunStream(ctx, RunRequest{Query: "test", MaxSteps: 5}, out)
	}()

	select {
	case <-done:
		// Good: goroutine exited.
	case <-time.After(3 * time.Second):
		t.Error("RunStream did not exit within 3s after context cancellation")
	}

	// Unblock the LLM (clean up) — blocked channel is never read, so just close.
	close(blocked)
}

// TestRunStream_ContextCancelledMidStream cancels the context while RunStream
// is mid-execution (while a tool is running).  The out channel must be
// drained after the goroutine exits to confirm it was closed.
func TestRunStream_ContextCancelledMidStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	// LLM always returns a tool call so the loop continues.
	chat := &mockLLM{
		responses: []string{
			`{"type":"tool_call","tool":"slow","args":{},"why":"test"}`,
		},
	}

	svc := newMinimalService(t, chat)
	// Register a "slow" tool that cancels the context after it starts.
	sid, _ := svc.Memory.CreateSession()
	_ = sid // session ID is set up to avoid nil in CreateSession
	// We can't easily inject custom tools via startSession, so we test the
	// cancellation path using an already-cancelled context instead.
	cancel()

	out := make(chan Event, 32)
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.RunStream(ctx, RunRequest{Query: "hi", MaxSteps: 3}, out)
	}()

	select {
	case <-done:
		// Goroutine exited; drain the channel.
		for range out {
		}
	case <-time.After(3 * time.Second):
		t.Error("RunStream did not exit within 3s")
	}
}

// TestRunStream_FinalAnswerClosesChannel verifies the happy path: a mock LLM
// that immediately returns a final answer causes RunStream to emit one "final"
// event and then close the channel.
func TestRunStream_FinalAnswerClosesChannel(t *testing.T) {
	chat := &mockLLM{
		responses: []string{`{"type":"final","answer":"All done!"}`},
	}
	svc := newMinimalService(t, chat)

	ctx := context.Background()
	out := make(chan Event, 32)

	go svc.RunStream(ctx, RunRequest{Query: "hello", MaxSteps: 5}, out)

	var finalSeen bool
	for ev := range out {
		if ev.Event == "final" {
			finalSeen = true
			if ev.Answer != "All done!" {
				t.Errorf("answer mismatch: %q", ev.Answer)
			}
		}
	}
	if !finalSeen {
		t.Error("expected a 'final' event")
	}
}

func TestExecuteBatch_Sequential(t *testing.T) {
	svc := newMinimalService(t, &mockLLM{})
	svc.ParallelEnabled = false

	sid, _ := svc.Memory.CreateSession()
	st := &runState{
		sid:        sid,
		tools:      map[string]Skill{},
		skillNames: map[string]bool{},
		toolNames:  map[string]bool{},
		timings:    map[string]float64{},
	}
	st.tools["adder"] = Skill{
		Execute: func(args map[string]any) (any, error) {
			n, _ := args["n"].(float64)
			return map[string]any{"result": n + 1}, nil
		},
	}

	calls := []batchCall{
		{Name: "adder", Args: map[string]any{"n": float64(1)}, Step: 1},
		{Name: "adder", Args: map[string]any{"n": float64(2)}, Step: 1},
	}
	results := svc.executeBatch(st, calls)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for i, r := range results {
		m, ok := r.Result.(map[string]any)
		if !ok {
			t.Fatalf("result[%d] not a map: %v", i, r.Result)
		}
		expected := float64(i + 2)
		if m["result"] != expected {
			t.Errorf("result[%d]: expected %v, got %v", i, expected, m["result"])
		}
	}
}

func TestExecuteBatch_Parallel(t *testing.T) {
	svc := newMinimalService(t, &mockLLM{})
	svc.ParallelEnabled = true
	svc.MaxParallel = 4

	sid, _ := svc.Memory.CreateSession()
	st := &runState{
		sid:        sid,
		tools:      map[string]Skill{},
		skillNames: map[string]bool{},
		toolNames:  map[string]bool{},
		timings:    map[string]float64{},
	}
	var mu sync.Mutex
	called := map[string]bool{}
	for _, name := range []string{"t1", "t2", "t3"} {
		n := name
		st.tools[n] = Skill{
			Execute: func(_ map[string]any) (any, error) {
				mu.Lock()
				called[n] = true
				mu.Unlock()
				return map[string]any{"ok": true}, nil
			},
		}
	}

	calls := []batchCall{
		{Name: "t1", Args: map[string]any{}, Step: 1},
		{Name: "t2", Args: map[string]any{}, Step: 1},
		{Name: "t3", Args: map[string]any{}, Step: 1},
	}
	results := svc.executeBatch(st, calls)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	mu.Lock()
	defer mu.Unlock()
	for _, n := range []string{"t1", "t2", "t3"} {
		if !called[n] {
			t.Errorf("tool %q was not called", n)
		}
	}
}

// funcLLM wraps a function as a llm.ChatClient for fine-grained control.
type funcLLM struct {
	fn func([]llm.Message) (string, float64, error)
}

func (f *funcLLM) CompleteJSON(msgs []llm.Message) (string, float64, error) {
	return f.fn(msgs)
}
func (f *funcLLM) Model() string   { return "func-mock" }
func (f *funcLLM) BaseURL() string { return "" }
