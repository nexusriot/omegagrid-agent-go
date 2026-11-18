package agent

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestApprovalApprovedRunsTool(t *testing.T) {
	rec := &recordingSkill{result: map[string]any{"ok": true}}
	svc := newMinimalService(t, &mockLLM{responses: []string{
		`{"type":"tool_call","tool":"shell_command","args":{"x":"ls"},"why":"list"}`,
		`{"type":"final","answer":"done"}`,
	}})
	svc.NativeSkills = map[string]Skill{"shell_command": rec.native("shell_command")}
	svc.Approvals = &Approvals{Timeout: 5 * time.Second}

	out := make(chan Event, 32)
	go svc.RunStream(context.Background(), RunRequest{Query: "list files", MaxSteps: 4}, out)
	var sawResolved bool
	for ev := range out {
		switch ev.Event {
		case "approval_required":
			if ev.Approval == nil || ev.Approval.Tool != "shell_command" {
				t.Fatalf("bad approval event %+v", ev)
			}
			if len(svc.Approvals.List()) != 1 {
				t.Fatal("pending list empty")
			}
			if rec.count() != 0 {
				t.Fatal("tool ran before approval")
			}
			if err := svc.Approvals.Resolve(ev.Approval.ID, ApprovalDecision{Approved: true, By: "test"}); err != nil {
				t.Fatal(err)
			}
		case "approval_resolved":
			sawResolved = ev.Decision != nil && ev.Decision.Approved
		}
	}
	if !sawResolved || rec.count() != 1 {
		t.Fatalf("resolved=%v calls=%d", sawResolved, rec.count())
	}
	if len(svc.Approvals.List()) != 0 {
		t.Fatal("approval left pending")
	}
}

func TestApprovalDeniedSkipsTool(t *testing.T) {
	rec := &recordingSkill{result: "ran"}
	chat := &promptLLM{mockLLM: mockLLM{responses: []string{
		`{"type":"tool_call","tool":"shell_command","args":{"x":"rm -rf /"}}`,
		`{"type":"final","answer":"not run"}`,
	}}}
	svc := newMinimalService(t, chat)
	svc.NativeSkills = map[string]Skill{"shell_command": rec.native("shell_command")}
	svc.Approvals = &Approvals{Timeout: 5 * time.Second}
	go func() {
		for {
			if l := svc.Approvals.List(); len(l) > 0 {
				_ = svc.Approvals.Resolve(l[0].ID, ApprovalDecision{Approved: false, By: "test", Reason: "too risky"})
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	res, err := svc.Run(RunRequest{Query: "wipe", MaxSteps: 4})
	if err != nil {
		t.Fatal(err)
	}
	if rec.count() != 0 {
		t.Fatal("denied tool ran")
	}
	if res.Answer != "not run" {
		t.Fatalf("answer %q", res.Answer)
	}
	if !strings.Contains(res.DebugLog, "did not approve") {
		t.Fatalf("model was not told about the denial:\n%s", res.DebugLog)
	}
}

func TestApprovalTimeoutDenies(t *testing.T) {
	rec := &recordingSkill{result: "ran"}
	svc := newMinimalService(t, &mockLLM{responses: []string{
		`{"type":"tool_call","tool":"file_write","args":{"x":"a"}}`,
	}})
	svc.NativeSkills = map[string]Skill{"file_write": rec.native("file_write")}
	svc.Approvals = &Approvals{Timeout: 50 * time.Millisecond}
	if _, err := svc.Run(RunRequest{Query: "write", MaxSteps: 2}); err != nil {
		t.Fatal(err)
	}
	if rec.count() != 0 {
		t.Fatal("tool ran after timeout")
	}
}

func TestApprovalUnattendedPolicy(t *testing.T) {
	for _, allow := range []bool{false, true} {
		rec := &recordingSkill{result: "ran"}
		svc := newMinimalService(t, &mockLLM{responses: []string{
			`{"type":"tool_call","tool":"shell_command","args":{"x":"uptime"}}`,
		}})
		svc.NativeSkills = map[string]Skill{"shell_command": rec.native("shell_command")}
		svc.Approvals = &Approvals{Timeout: time.Hour}
		svc.UnattendedApprove = allow
		done := make(chan struct{})
		go func() {
			_, _ = svc.Run(RunRequest{Query: "uptime", MaxSteps: 2, Unattended: true})
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("unattended run waited for a human")
		}
		want := 0
		if allow {
			want = 1
		}
		if rec.count() != want {
			t.Fatalf("allow=%v calls=%d", allow, rec.count())
		}
	}
}

func TestApprovalNotRequiredForOtherSkills(t *testing.T) {
	rec := &recordingSkill{result: "ok"}
	svc := newMinimalService(t, &mockLLM{responses: []string{
		`{"type":"tool_call","tool":"weather","args":{"x":"Rome"}}`,
	}})
	svc.NativeSkills = map[string]Skill{"weather": rec.native("weather")}
	svc.Approvals = &Approvals{Timeout: time.Hour}
	if _, err := svc.Run(RunRequest{Query: "w", MaxSteps: 2}); err != nil {
		t.Fatal(err)
	}
	if rec.count() != 1 {
		t.Fatal("ungated skill did not run")
	}
}

func TestApprovalPrompt(t *testing.T) {
	a := &Approvals{Prompt: func(context.Context, ApprovalRequest) (ApprovalDecision, bool) {
		return ApprovalDecision{Approved: true, By: "cli"}, true
	}}
	req, wait := a.Open(ApprovalRequest{Tool: "x"})
	if d := wait(context.Background()); !d.Approved {
		t.Fatal(d)
	}
	if _, ok := a.Get(req.ID); ok {
		t.Fatal("still pending")
	}
	if err := a.Resolve(req.ID, ApprovalDecision{}); err != ErrApprovalNotFound {
		t.Fatal(err)
	}
}
