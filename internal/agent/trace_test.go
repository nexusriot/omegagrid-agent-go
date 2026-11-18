package agent

import (
	"context"
	"testing"

	"github.com/nexusriot/omegagrid-agent-go/internal/memory"
)

func TestRunTraceRecorded(t *testing.T) {
	rec := &recordingSkill{result: "ok"}
	svc := newMinimalService(t, &mockLLM{responses: []string{
		`{"type":"tool_call","tool":"probe","args":{"x":"a"}}`,
		`{"type":"final","answer":"done"}`,
	}})
	svc.NativeSkills = map[string]Skill{"probe": rec.native("probe")}
	res, err := svc.Run(RunRequest{Query: "check a", MaxSteps: 4})
	if err != nil {
		t.Fatal(err)
	}
	runs, total, err := svc.Memory.ListRuns(memory.RunFilter{})
	if err != nil || total != 1 {
		t.Fatalf("runs=%d err=%v", total, err)
	}
	r := runs[0]
	if r.SessionID != res.SessionID || r.Outcome != "final" || r.Steps != 2 || r.LLMCalls != 2 ||
		r.Query != "check a" || len(r.Used) != 1 || r.Used[0] != "probe" || r.Retrieval != "off" {
		t.Fatalf("trace %+v", r)
	}
	byTool, n, _ := svc.Memory.ListRuns(memory.RunFilter{Tool: "probe"})
	if n != 1 || len(byTool) != 1 {
		t.Fatalf("tool filter: %d", n)
	}
	if _, n, _ := svc.Memory.ListRuns(memory.RunFilter{Tool: "prob"}); n != 0 {
		t.Fatal("tool filter matched a prefix")
	}
}

func TestRunTraceStreamOutcomes(t *testing.T) {
	svc := newMinimalService(t, &mockLLM{responses: []string{
		`{"type":"tool_call","tool":"nope","args":{}}`,
		`{"type":"tool_call","tool":"nope","args":{}}`,
	}})
	out := make(chan Event, 32)
	go svc.RunStream(context.Background(), RunRequest{Query: "loop", MaxSteps: 2}, out)
	collectEvents(out)
	runs, _, _ := svc.Memory.ListRuns(memory.RunFilter{})
	if len(runs) != 1 || runs[0].Outcome != "max_steps" || runs[0].Steps != 2 {
		t.Fatalf("runs %+v", runs)
	}
	// An unknown tool is not "used".
	if len(runs[0].Used) != 0 {
		t.Fatalf("used %v", runs[0].Used)
	}
}

func TestRunTraceCancelledStream(t *testing.T) {
	svc := newMinimalService(t, &mockLLM{responses: []string{`{"type":"final","answer":"x"}`}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := make(chan Event) // unbuffered: nobody reads, so the run stops at once
	svc.RunStream(ctx, RunRequest{Query: "gone", MaxSteps: 2}, out)
	runs, _, _ := svc.Memory.ListRuns(memory.RunFilter{})
	if len(runs) != 1 || runs[0].Outcome != "cancelled" {
		t.Fatalf("runs %+v", runs)
	}
}
