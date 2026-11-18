package agent

import (
	"context"
	"testing"
	"time"

	"github.com/nexusriot/omegagrid-agent-go/internal/memory"
)

func TestParseAllowRules(t *testing.T) {
	rules, err := ParseAllowRules(`shell_command: command=df( -h)?|uptime|free -m ;
		ssh_command: host=web[0-9]+ && command=uptime;
		github_*: action=list_issues;
		file_write: path=notes/[a-z]+\.md && content=a\;b`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 4 {
		t.Fatalf("got %d rules", len(rules))
	}
	cases := []struct {
		tool string
		args map[string]any
		want bool
	}{
		{"shell_command", map[string]any{"command": "df -h"}, true},
		{"shell_command", map[string]any{"command": "uptime"}, true},
		{"shell_command", map[string]any{"command": "free -m"}, true},
		{"shell_command", map[string]any{"command": "dfx"}, false},              // whole-value match
		{"shell_command", map[string]any{"command": "uptime; rm -rf ~"}, false}, // metacharacter
		{"shell_command", map[string]any{"command": "df -h && id"}, false},
		{"shell_command", map[string]any{"command": "uptime\nid"}, false},
		{"shell_command", map[string]any{"command": "$(id)"}, false},
		{"shell_command", map[string]any{}, false},
		{"shell_command", map[string]any{"command": []any{"uptime"}}, false},
		{"ssh_command", map[string]any{"host": "web12", "command": "uptime"}, true},
		{"ssh_command", map[string]any{"host": "db1", "command": "uptime"}, false},
		{"ssh_command", map[string]any{"command": "uptime"}, false},
		{"github_issues", map[string]any{"action": "list_issues"}, true},
		{"gitlab", map[string]any{"action": "list_issues"}, false},
		{"file_write", map[string]any{"path": "notes/todo.md", "content": "a;b"}, false}, // ";" is never allowed
	}
	for _, c := range cases {
		got := false
		for _, r := range rules {
			if r.Match(c.tool, c.args) {
				got = true
			}
		}
		if got != c.want {
			t.Errorf("%s %v: got %v want %v", c.tool, c.args, got, c.want)
		}
	}
	for _, bad := range []string{"shell_command", "shell_command:", "shell_command: command", "a b: x=1", "x: y=("} {
		if _, err := ParseAllowRules(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func TestApprovalRulePreapprovesWithoutAsking(t *testing.T) {
	rec := &recordingSkill{result: "ok"}
	svc := newMinimalService(t, &mockLLM{responses: []string{
		`{"type":"tool_call","tool":"shell_command","args":{"x":"uptime"}}`,
		`{"type":"final","answer":"done"}`,
	}})
	svc.NativeSkills = map[string]Skill{"shell_command": rec.native("shell_command")}
	rules, err := ParseAllowRules("shell_command: x=uptime")
	if err != nil {
		t.Fatal(err)
	}
	svc.Approvals = &Approvals{Timeout: time.Second, Rules: rules}

	out := make(chan Event, 32)
	go svc.RunStream(context.Background(), RunRequest{Query: "load?", MaxSteps: 3}, out)
	for ev := range out {
		if ev.Event == "approval_required" {
			t.Fatal("a rule-covered call asked for approval")
		}
	}
	if rec.count() != 1 {
		t.Fatalf("tool ran %d times", rec.count())
	}
	log, _, err := svc.Memory.ListApprovals(memory.ApprovalFilter{})
	if err != nil || len(log) != 1 || !log[0].Approved || log[0].By != "rule" || log[0].Reason != "shell_command: x=uptime" {
		t.Fatalf("approval log %+v %v", log, err)
	}
}

func TestApprovalRuleAppliesToUnattendedRuns(t *testing.T) {
	rec := &recordingSkill{result: "ok"}
	svc := newMinimalService(t, &mockLLM{responses: []string{
		`{"type":"tool_call","tool":"shell_command","args":{"x":"uptime"}}`,
		`{"type":"tool_call","tool":"shell_command","args":{"x":"reboot"}}`,
		`{"type":"final","answer":"done"}`,
	}})
	svc.NativeSkills = map[string]Skill{"shell_command": rec.native("shell_command")}
	rules, _ := ParseAllowRules("shell_command: x=uptime")
	svc.Approvals = &Approvals{Timeout: time.Second, Rules: rules}
	if _, err := svc.Run(RunRequest{Query: "cron", MaxSteps: 4, Unattended: true}); err != nil {
		t.Fatal(err)
	}
	if rec.count() != 1 || rec.calls[0]["x"] != "uptime" {
		t.Fatalf("calls %v", rec.calls)
	}
	log, _, _ := svc.Memory.ListApprovals(memory.ApprovalFilter{OnlyDenied: true})
	if len(log) != 1 || log[0].By != "policy" {
		t.Fatalf("denied log %+v", log)
	}
}

func TestApprovalSessionGrant(t *testing.T) {
	rec := &recordingSkill{result: "ok"}
	svc := newMinimalService(t, &mockLLM{responses: []string{
		`{"type":"tool_call","tool":"shell_command","args":{"x":"ls"}}`,
		`{"type":"tool_call","tool":"shell_command","args":{"x":"ls -la"}}`,
		`{"type":"final","answer":"done"}`,
	}})
	svc.NativeSkills = map[string]Skill{"shell_command": rec.native("shell_command")}
	svc.Approvals = &Approvals{Timeout: 5 * time.Second}

	out := make(chan Event, 32)
	go svc.RunStream(context.Background(), RunRequest{Query: "files", MaxSteps: 4}, out)
	asked := 0
	sid := 0
	for ev := range out {
		if ev.Event == "approval_required" {
			asked++
			sid = ev.Approval.SessionID
			if err := svc.Approvals.Resolve(ev.Approval.ID, ApprovalDecision{Approved: true, By: "test", Scope: ScopeSession}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if asked != 1 || rec.count() != 2 {
		t.Fatalf("asked=%d ran=%d", asked, rec.count())
	}
	g := svc.Approvals.Grants()
	if len(g) != 1 || g[0].SessionID != sid || g[0].Tool != "shell_command" {
		t.Fatalf("grants %+v", g)
	}
	// Another session is not covered.
	if _, ok := svc.Approvals.preapproved(sid+1, "shell_command", nil); ok {
		t.Fatal("grant leaked to another session")
	}
	if n := svc.Approvals.Revoke(sid); n != 1 {
		t.Fatalf("revoked %d", n)
	}
	if _, ok := svc.Approvals.preapproved(sid, "shell_command", nil); ok {
		t.Fatal("grant survived revoke")
	}
	log, _, _ := svc.Memory.ListApprovals(memory.ApprovalFilter{SessionID: sid})
	if len(log) != 2 || log[0].By != "session" || log[1].By != "test" || log[1].Scope != ScopeSession {
		t.Fatalf("log %+v", log)
	}
}

func TestApprovalGrantExpires(t *testing.T) {
	a := &Approvals{GrantTTL: time.Millisecond}
	a.grant(ApprovalRequest{SessionID: 7, Tool: "file_write"})
	time.Sleep(5 * time.Millisecond)
	if _, ok := a.preapproved(7, "file_write", nil); ok {
		t.Fatal("expired grant still approves")
	}
	if len(a.Grants()) != 0 {
		t.Fatal("expired grant listed")
	}
}

func TestOnceApprovalGrantsNothing(t *testing.T) {
	a := &Approvals{Timeout: time.Second}
	req, wait := a.Open(ApprovalRequest{SessionID: 3, Tool: "shell_command"})
	go func() { _ = a.Resolve(req.ID, ApprovalDecision{Approved: true}) }()
	if d := wait(context.Background()); !d.Approved {
		t.Fatal("not approved")
	}
	if _, ok := a.preapproved(3, "shell_command", nil); ok {
		t.Fatal("a one-off approval created a grant")
	}
}
