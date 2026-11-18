package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/nexusriot/omegagrid-agent-go/internal/agent"
	"github.com/nexusriot/omegagrid-agent-go/internal/memory"
)

func TestApprovalsAPI(t *testing.T) {
	d := newTestDeps(t)
	d.Agent = &agent.Service{Approvals: &agent.Approvals{Timeout: 5 * time.Second}}
	h := NewRouter(d)

	req, wait := d.Agent.Approvals.Open(agent.ApprovalRequest{Tool: "shell_command", Args: map[string]any{"command": "ls"}})
	got := make(chan agent.ApprovalDecision, 1)
	go func() { got <- wait(context.Background()) }()

	code, body := doJSON(t, h, http.MethodGet, "/api/approvals", "")
	if code != http.StatusOK || body["enabled"] != true {
		t.Fatalf("list: %d %v", code, body)
	}
	if p, _ := body["pending"].([]any); len(p) != 1 {
		t.Fatalf("pending = %v", body["pending"])
	}
	if code, _ := doJSON(t, h, http.MethodGet, "/api/approvals/"+req.ID, ""); code != http.StatusOK {
		t.Fatalf("get: %d", code)
	}
	if code, _ := doJSON(t, h, http.MethodPost, "/api/approvals/"+req.ID, `{}`); code != http.StatusBadRequest {
		t.Fatalf("missing approved: %d", code)
	}
	if code, _ := doJSON(t, h, http.MethodPost, "/api/approvals/"+req.ID, `{"approved":true,"by":"web"}`); code != http.StatusOK {
		t.Fatalf("resolve: %d", code)
	}
	if d := <-got; !d.Approved || d.By != "web" {
		t.Fatalf("decision = %+v", d)
	}
	if code, _ := doJSON(t, h, http.MethodPost, "/api/approvals/"+req.ID, `{"approved":false}`); code != http.StatusNotFound {
		t.Fatalf("second resolve: %d", code)
	}
}

func TestApprovalsDisabled(t *testing.T) {
	d := newTestDeps(t)
	d.Agent = &agent.Service{}
	code, body := doJSON(t, NewRouter(d), http.MethodGet, "/api/approvals", "")
	if code != http.StatusOK || body["enabled"] != false {
		t.Fatalf("%d %v", code, body)
	}
}

func TestApprovalSessionScopeAndGrantsAPI(t *testing.T) {
	d := newTestDeps(t)
	rules, _ := agent.ParseAllowRules("shell_command: command=uptime")
	d.Agent = &agent.Service{Approvals: &agent.Approvals{Timeout: 5 * time.Second, Rules: rules}}
	h := NewRouter(d)

	req, wait := d.Agent.Approvals.Open(agent.ApprovalRequest{SessionID: 4, Tool: "file_write"})
	got := make(chan agent.ApprovalDecision, 1)
	go func() { got <- wait(context.Background()) }()
	if code, _ := doJSON(t, h, http.MethodPost, "/api/approvals/"+req.ID, `{"approved":true,"scope":"forever"}`); code != http.StatusBadRequest {
		t.Fatalf("bad scope: %d", code)
	}
	code, body := doJSON(t, h, http.MethodPost, "/api/approvals/"+req.ID, `{"approved":true,"scope":"session"}`)
	if code != http.StatusOK || body["scope"] != "session" {
		t.Fatalf("resolve: %d %v", code, body)
	}
	if d := <-got; d.Scope != agent.ScopeSession {
		t.Fatalf("decision %+v", d)
	}
	code, body = doJSON(t, h, http.MethodGet, "/api/approvals/grants", "")
	grants, _ := body["grants"].([]any)
	rulesOut, _ := body["rules"].([]any)
	if code != http.StatusOK || len(grants) != 1 || len(rulesOut) != 1 {
		t.Fatalf("grants: %d %v", code, body)
	}
	code, body = doJSON(t, h, http.MethodDelete, "/api/approvals/grants?session_id=4", "")
	if code != http.StatusOK || body["revoked"] != float64(1) {
		t.Fatalf("revoke: %d %v", code, body)
	}
}

func TestApprovalHistoryAndRunsAPI(t *testing.T) {
	d := newTestDeps(t)
	h := NewRouter(d)
	if err := d.Memory.AddApproval(memory.ApprovalRecord{SessionID: 1, Tool: "shell_command", Args: map[string]any{"command": "ls"}, Approved: false, By: "web"}); err != nil {
		t.Fatal(err)
	}
	code, body := doJSON(t, h, http.MethodGet, "/api/approvals/history?only_denied=1", "")
	if list, _ := body["approvals"].([]any); code != http.StatusOK || len(list) != 1 {
		t.Fatalf("history: %d %v", code, body)
	}
	id, err := d.Memory.AddRun(memory.RunRecord{SessionID: 1, Query: "q", Used: []string{"dns_lookup"}, Outcome: "final", DurationMS: 5})
	if err != nil {
		t.Fatal(err)
	}
	code, body = doJSON(t, h, http.MethodGet, "/api/runs?tool=dns_lookup", "")
	if code != http.StatusOK || body["total"] != float64(1) {
		t.Fatalf("runs: %d %v", code, body)
	}
	if code, _ := doJSON(t, h, http.MethodGet, "/api/runs/"+strconv.FormatInt(id, 10), ""); code != http.StatusOK {
		t.Fatalf("run: %d", code)
	}
	if code, _ := doJSON(t, h, http.MethodGet, "/api/runs/999", ""); code != http.StatusNotFound {
		t.Fatalf("missing run: %d", code)
	}
	code, body = doJSON(t, h, http.MethodGet, "/api/runs/stats?days=1", "")
	if code != http.StatusOK || body["runs"] != float64(1) {
		t.Fatalf("stats: %d %v", code, body)
	}
}

func TestRankToolsAPI(t *testing.T) {
	d := newTestDeps(t)
	d.Agent = &agent.Service{Skills: d.Skills, ToolTopK: 5}
	h := NewRouter(d)
	if code, _ := doJSON(t, h, http.MethodGet, "/api/tools/rank", ""); code != http.StatusBadRequest {
		t.Fatalf("no q: %d", code)
	}
	code, body := doJSON(t, h, http.MethodGet, "/api/tools/rank?q=tls+certificate+expiry&limit=3", "")
	ranked, _ := body["ranked"].([]any)
	if code != http.StatusOK || len(ranked) != 3 {
		t.Fatalf("rank: %d %v", code, body)
	}
	if first, _ := ranked[0].(map[string]any); first["name"] != "tls_probe" || first["offered"] != true {
		t.Fatalf("top = %v", ranked[0])
	}
}
