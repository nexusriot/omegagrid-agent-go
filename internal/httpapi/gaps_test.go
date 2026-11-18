package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nexusriot/omegagrid-agent-go/internal/memory"
)

func TestSchedulerListRoute(t *testing.T) {
	d := newTestDeps(t)
	h := NewRouter(d)
	if _, err := d.Scheduler.Create("ping", "*/5 * * * *", "datetime_skill", nil, nil, false); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/scheduler/tasks", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"ping"`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
}

func TestReplayRerunsSkillAndRecordsIt(t *testing.T) {
	d := newTestDeps(t)
	h := NewRouter(d)
	if err := d.Memory.AddInvocation(memory.AuditRecord{SessionID: 3, Step: 1, Skill: "base64_skill", Kind: "skill",
		Args: map[string]any{"action": "encode", "text": "hi"}}); err != nil {
		t.Fatal(err)
	}
	code, body := doJSON(t, h, http.MethodPost, "/api/invocations/1/replay", "")
	if code != http.StatusOK || body["skill"] != "base64_skill" || body["error"] != "" {
		t.Fatalf("replay: %d %v", code, body)
	}
	if res, _ := body["result"].(map[string]any); res["output"] != "aGk=" {
		t.Fatalf("result %v", body["result"])
	}
	recs, total, _ := d.Memory.ListInvocations(memory.AuditFilter{})
	if total != 2 || recs[0].Kind != "replay" || recs[0].ReplayedFrom == nil || *recs[0].ReplayedFrom != 1 {
		t.Fatalf("replay row %+v", recs)
	}
	for path, want := range map[string]int{"/api/invocations/x/replay": http.StatusBadRequest, "/api/invocations/99/replay": http.StatusNotFound} {
		if code, _ := doJSON(t, h, http.MethodPost, path, ""); code != want {
			t.Errorf("%s: %d want %d", path, code, want)
		}
	}
}
