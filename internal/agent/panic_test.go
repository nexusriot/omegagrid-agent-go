package agent

import (
	"context"
	"strings"
	"testing"
)

// A panicking tool used to take the whole process down: executeBatch runs each
// call on its own goroutine, where the HTTP router's recover middleware cannot
// reach it. The panic must surface as an ordinary tool error instead.
func TestPanickingToolInParallelBatchDoesNotCrash(t *testing.T) {
	svc := newMinimalService(t, &mockLLM{responses: []string{
		`{"type":"tool_calls","calls":[{"tool":"boom","args":{}},{"tool":"fine","args":{}}]}`,
		`{"type":"final","answer":"recovered"}`,
	}})
	svc.ParallelEnabled = true
	svc.MaxParallel = 4
	svc.NativeSkills = map[string]Skill{
		"boom": {Execute: func(map[string]any) (any, error) {
			var s []int
			_ = s[5]
			return nil, nil
		}},
		"fine": {Execute: func(map[string]any) (any, error) { return "ok", nil }},
	}

	res, err := svc.Run(RunRequest{Query: "hi", MaxSteps: 3})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Answer != "recovered" {
		t.Errorf("answer = %q, want %q", res.Answer, "recovered")
	}
	if !strings.Contains(res.DebugLog, "crashed") {
		t.Errorf("debug log should record the crashed tool, got:\n%s", res.DebugLog)
	}
	// The sibling call in the same batch must still have run.
	if !strings.Contains(res.DebugLog, "fine") {
		t.Errorf("sibling call missing from debug log:\n%s", res.DebugLog)
	}
}

// The same protection is needed on the single-call path: the gateway starts
// RunStream on its own goroutine too.
func TestPanickingToolInSingleCallDoesNotCrash(t *testing.T) {
	svc := newMinimalService(t, &mockLLM{responses: []string{
		`{"type":"tool_call","tool":"boom","args":{},"why":"test"}`,
		`{"type":"final","answer":"done"}`,
	}})
	svc.NativeSkills = map[string]Skill{
		"boom": {Execute: func(map[string]any) (any, error) {
			panic("kaboom")
		}},
	}

	out := make(chan Event, 32)
	go svc.RunStream(context.Background(), RunRequest{Query: "hi", MaxSteps: 3}, out)
	evs := collectEvents(out)

	var sawToolResult, sawFinal bool
	for _, ev := range evs {
		if ev.Event == "tool_result" && strings.Contains(ev.Result, "crashed") {
			sawToolResult = true
		}
		if ev.Event == "final" && ev.Answer == "done" {
			sawFinal = true
		}
	}
	if !sawToolResult {
		t.Errorf("expected a tool_result reporting the crash, got %+v", evs)
	}
	if !sawFinal {
		t.Errorf("expected the run to continue to a final answer, got %+v", evs)
	}
}

// safeExecute must leave normal results and normal errors untouched.
func TestSafeExecutePassesThrough(t *testing.T) {
	res, err := safeExecute(func(map[string]any) (any, error) { return "value", nil }, "t", nil)
	if err != nil || res != "value" {
		t.Fatalf("got (%v, %v), want (value, nil)", res, err)
	}
	_, err = safeExecute(func(map[string]any) (any, error) { return nil, context.Canceled }, "t", nil)
	if err != context.Canceled {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}
