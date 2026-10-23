package agent

import (
	"context"
	"strings"
	"testing"
)

// {"type":"<tool>","args":{...}} is a common malformation; the args object
// must be used as-is, not nested under an "args" key.
func TestNormalizeToolCall_ToolNameTypeWithArgsObject(t *testing.T) {
	in := map[string]any{"type": "weather", "args": map[string]any{"city": "London"}}
	out := normalizeToolCall(in, map[string]bool{"weather": true})
	args, _ := out["args"].(map[string]any)
	if args["city"] != "London" {
		t.Fatalf("args = %v, want city=London at top level", args)
	}
}

// Stream events must not share maps with the tool: the SSE handler encodes
// the event while the tool runs, and concurrent map access is fatal.
func TestEventArgsIsDeepCopy(t *testing.T) {
	meta := map[string]any{"tag": "pref"}
	args := map[string]any{"text": "x", "meta": meta}
	out := eventArgs("vector_add", args)
	meta["session_id"] = 1
	if _, leaked := out["meta"].(map[string]any)["session_id"]; leaked {
		t.Fatal("eventArgs shares nested maps with the live args")
	}
	if got := eventArgs("jwt_inspect", map[string]any{"token": "eyJ..."}); got["token"] != nil {
		t.Fatalf("jwt_inspect args leaked into the event: %v", got)
	}
}

func TestCapToolContent(t *testing.T) {
	small := "abc"
	if capToolContent(small) != small {
		t.Fatal("small content altered")
	}
	big := strings.Repeat("x", maxToolContentBytes*2)
	got := capToolContent(big)
	if len(got) > maxToolContentBytes+64 || !strings.Contains(got, "truncated") {
		t.Fatalf("big content not capped: len=%d", len(got))
	}
}

// A cancelled context stops the loop before any LLM call is made.
func TestRunContextCancelled(t *testing.T) {
	m := &mockLLM{responses: []string{`{"type":"final","answer":"hi"}`}}
	svc := newMinimalService(t, m)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.RunContext(ctx, RunRequest{Query: "q", MaxSteps: 3}); err == nil {
		t.Fatal("RunContext with a cancelled ctx returned no error")
	}
}

// password_gen output must not surface in the debug log returned by /api/query.
func TestDebugLogRedactsPasswordGen(t *testing.T) {
	svc := newMinimalService(t, &mockLLM{responses: []string{
		`{"type":"tool_call","tool":"password_gen","args":{"x":"1"}}`,
		`{"type":"final","answer":"done"}`,
	}})
	rec := &recordingSkill{result: map[string]any{"password": "hunter2-SECRET"}}
	svc.NativeSkills = map[string]Skill{"password_gen": rec.native("password_gen")}
	res, err := svc.Run(RunRequest{Query: "pw", MaxSteps: 3})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.DebugLog, "hunter2-SECRET") {
		t.Fatal("password leaked into the debug log")
	}
}
