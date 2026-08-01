package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nexusriot/omegagrid-agent-go/internal/config"
	"github.com/nexusriot/omegagrid-agent-go/internal/llm"
	"github.com/nexusriot/omegagrid-agent-go/internal/memory"
	"github.com/nexusriot/omegagrid-agent-go/internal/scheduler"
	"github.com/nexusriot/omegagrid-agent-go/internal/skills"
)

// newTestDeps wires real stores against temp files. The embeddings backend is
// deliberately unreachable, so vector calls fail while history and scheduler
// endpoints stay fully exercisable.
func newTestDeps(t *testing.T) Deps {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{
		DataDir:           dir,
		Provider:          "ollama",
		AgentDB:           filepath.Join(dir, "agent.sqlite3"),
		VectorDir:         filepath.Join(dir, "chromem"),
		VectorCollection:  "test",
		OllamaURL:         "http://127.0.0.1:19999",
		OllamaEmbedModel:  "nomic-embed-text",
		OllamaTimeoutSec:  0.1,
		DedupDistance:     0.05,
		AuditMaxBlobBytes: 65536,
		SkillsDir:         filepath.Join(dir, "skills"),
		SchedulerDB:       filepath.Join(dir, "sched.sqlite3"),
		PlaygroundEnabled: true,
		AgentMaxSteps:     25,
	}

	mem, err := memory.New(cfg)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })

	sk, err := skills.New(cfg)
	if err != nil {
		t.Fatalf("skills.New: %v", err)
	}

	store, err := scheduler.NewStore(cfg.SchedulerDB)
	if err != nil {
		t.Fatalf("scheduler.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	return Deps{Cfg: cfg, Memory: mem, Skills: sk, Scheduler: store}
}

func doJSON(t *testing.T, h http.Handler, method, path, body string) (int, map[string]any) {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	var out map[string]any
	if w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s %s: response is not a JSON object: %s", method, path, w.Body.String())
		}
	}
	return w.Code, out
}

func TestSessionsRoundTrip(t *testing.T) {
	h := NewRouter(newTestDeps(t))

	code, body := doJSON(t, h, "POST", "/api/sessions/new", "")
	if code != http.StatusOK {
		t.Fatalf("new session: status %d (%v)", code, body)
	}
	sid, ok := body["session_id"].(float64)
	if !ok || sid == 0 {
		t.Fatalf("new session returned no id: %v", body)
	}

	code, body = doJSON(t, h, "GET", "/api/sessions", "")
	if code != http.StatusOK {
		t.Fatalf("list sessions: status %d", code)
	}
	sessions, _ := body["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %v", body["sessions"])
	}
}

// ?limit=0 used to reach SQLite as "LIMIT 0" and return nothing, while
// ?limit=-1 meant "no limit"; both now fall back to the endpoint default.
func TestSessionListLimitEdgeCases(t *testing.T) {
	h := NewRouter(newTestDeps(t))
	for i := 0; i < 3; i++ {
		if code, _ := doJSON(t, h, "POST", "/api/sessions/new", ""); code != http.StatusOK {
			t.Fatalf("new session %d: status %d", i, code)
		}
	}

	for _, q := range []string{"", "?limit=0", "?limit=-5", "?limit=abc"} {
		code, body := doJSON(t, h, "GET", "/api/sessions"+q, "")
		if code != http.StatusOK {
			t.Fatalf("GET /api/sessions%s: status %d", q, code)
		}
		sessions, _ := body["sessions"].([]any)
		if len(sessions) != 3 {
			t.Fatalf("GET /api/sessions%s returned %d sessions, want 3", q, len(sessions))
		}
	}

	// An explicit positive limit is still honoured.
	code, body := doJSON(t, h, "GET", "/api/sessions?limit=2", "")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if sessions, _ := body["sessions"].([]any); len(sessions) != 2 {
		t.Fatalf("limit=2 returned %d sessions", len(sessions))
	}
}

func TestSessionMessagesLimitEdgeCases(t *testing.T) {
	d := newTestDeps(t)
	h := NewRouter(d)

	sid, err := d.Memory.CreateSession()
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	for _, msg := range []string{"one", "two", "three"} {
		if err := d.Memory.AddMessage(sid, "user", msg); err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
	}

	for _, q := range []string{"", "?limit=0", "?limit=-1", "?offset=-3"} {
		code, body := doJSON(t, h, "GET", "/api/sessions/1/messages"+q, "")
		if code != http.StatusOK {
			t.Fatalf("messages%s: status %d (%v)", q, code, body)
		}
		msgs, _ := body["messages"].([]any)
		if len(msgs) != 3 {
			t.Fatalf("messages%s returned %d, want 3", q, len(msgs))
		}
	}
}

func TestSessionMessagesRejectsBadID(t *testing.T) {
	h := NewRouter(newTestDeps(t))
	code, body := doJSON(t, h, "GET", "/api/sessions/not-a-number/messages", "")
	if code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 (%v)", code, body)
	}
}

func TestSkillsListIsSortedAndStable(t *testing.T) {
	h := NewRouter(newTestDeps(t))

	names := func() []string {
		code, body := doJSON(t, h, "GET", "/api/skills", "")
		if code != http.StatusOK {
			t.Fatalf("status %d", code)
		}
		list, _ := body["skills"].([]any)
		if len(list) == 0 {
			t.Fatal("no skills returned")
		}
		out := make([]string, 0, len(list))
		for _, s := range list {
			m, _ := s.(map[string]any)
			n, _ := m["name"].(string)
			out = append(out, n)
		}
		return out
	}

	first := names()
	for i := 1; i < len(first); i++ {
		if first[i-1] > first[i] {
			t.Fatalf("/api/skills is not sorted: %v", first)
		}
	}
	// The UI re-fetches this list; map order used to reshuffle it every time.
	for i := 0; i < 5; i++ {
		again := names()
		for j := range again {
			if again[j] != first[j] {
				t.Fatalf("/api/skills order changed at %d: %q vs %q", j, again[j], first[j])
			}
		}
	}
}

func TestSchedulerCRUD(t *testing.T) {
	h := NewRouter(newTestDeps(t))

	code, body := doJSON(t, h, "POST", "/api/scheduler/tasks",
		`{"name":"nightly","cron_expr":"0 3 * * *","skill":"reminder","args":{"message":"hi"}}`)
	if code != http.StatusOK {
		t.Fatalf("create: status %d (%v)", code, body)
	}
	id, _ := body["id"].(float64)
	if id == 0 {
		t.Fatalf("create returned no id: %v", body)
	}

	if code, body = doJSON(t, h, "POST", "/api/scheduler/tasks/1/disable", ""); code != http.StatusOK {
		t.Fatalf("disable: status %d (%v)", code, body)
	}
	if body["enabled"] != false {
		t.Fatalf("disable did not report enabled=false: %v", body)
	}

	if code, _ = doJSON(t, h, "DELETE", "/api/scheduler/tasks/1", ""); code != http.StatusOK {
		t.Fatalf("delete: status %d", code)
	}
	// Acting on a task that no longer exists must be a 404, not a silent OK.
	for _, path := range []string{
		"/api/scheduler/tasks/1/enable",
		"/api/scheduler/tasks/1/disable",
	} {
		if code, _ = doJSON(t, h, "POST", path, ""); code != http.StatusNotFound {
			t.Fatalf("POST %s after delete: status %d, want 404", path, code)
		}
	}
	if code, _ = doJSON(t, h, "DELETE", "/api/scheduler/tasks/1", ""); code != http.StatusNotFound {
		t.Fatalf("second delete: status %d, want 404", code)
	}
}

func TestSchedulerRejectsInvalidCron(t *testing.T) {
	h := NewRouter(newTestDeps(t))
	// An unvalidated expression would create a task that silently never fires.
	for _, expr := range []string{"not a cron", "99 * * * *", "* * *"} {
		code, body := doJSON(t, h, "POST", "/api/scheduler/tasks",
			`{"name":"t","cron_expr":"`+expr+`","skill":"reminder"}`)
		if code != http.StatusBadRequest {
			t.Fatalf("cron %q: status %d, want 400 (%v)", expr, code, body)
		}
	}
}

func TestSchedulerCreateRequiresFields(t *testing.T) {
	h := NewRouter(newTestDeps(t))
	code, _ := doJSON(t, h, "POST", "/api/scheduler/tasks", `{"name":"t"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", code)
	}
}

func TestQueryRequiresQuery(t *testing.T) {
	h := NewRouter(newTestDeps(t))
	for _, path := range []string{"/api/query", "/api/query/stream"} {
		code, body := doJSON(t, h, "POST", path, `{"query":""}`)
		if code != http.StatusBadRequest {
			t.Fatalf("POST %s: status %d, want 400 (%v)", path, code, body)
		}
	}
}

func TestMaxStepsClamped(t *testing.T) {
	req := queryRequest{Query: "x", MaxSteps: 10000}
	if got := req.toAgentReq(25).MaxSteps; got != maxStepsHardLimit {
		t.Fatalf("MaxSteps = %d, want the hard limit %d", got, maxStepsHardLimit)
	}
	req = queryRequest{Query: "x", MaxSteps: 0}
	if got := req.toAgentReq(25).MaxSteps; got != 25 {
		t.Fatalf("MaxSteps = %d, want the configured default 25", got)
	}
	req = queryRequest{Query: "x", MaxSteps: -3}
	if got := req.toAgentReq(25).MaxSteps; got != 25 {
		t.Fatalf("negative MaxSteps = %d, want the configured default 25", got)
	}
	// A misconfigured AGENT_MAX_STEPS=0 must not turn every query into an
	// instant "could not finish within max_steps".
	if got := req.toAgentReq(0).MaxSteps; got < maxStepsFloor {
		t.Fatalf("MaxSteps = %d with a zero default, want at least %d", got, maxStepsFloor)
	}
	if got := (queryRequest{Query: "x"}).toAgentReq(-5).MaxSteps; got < maxStepsFloor {
		t.Fatalf("MaxSteps = %d with a negative default, want at least %d", got, maxStepsFloor)
	}
}

// Both replay and the playground re-execute a skill outside the agent loop, so
// both must stay behind PLAYGROUND_DISABLED.
func TestPlaygroundGating(t *testing.T) {
	d := newTestDeps(t)
	d.Cfg.PlaygroundEnabled = false
	h := NewRouter(d)

	code, _ := doJSON(t, h, "POST", "/api/skills/datetime_skill/invoke", `{"args":{}}`)
	if code != http.StatusForbidden {
		t.Fatalf("invoke with playground disabled: status %d, want 403", code)
	}
	code, _ = doJSON(t, h, "POST", "/api/invocations/1/replay", "")
	if code != http.StatusForbidden {
		t.Fatalf("replay with playground disabled: status %d, want 403", code)
	}
}

func TestSkillInvokeExecutesSkill(t *testing.T) {
	h := NewRouter(newTestDeps(t))
	code, body := doJSON(t, h, "POST", "/api/skills/datetime_skill/invoke", `{"args":{}}`)
	if code != http.StatusOK {
		t.Fatalf("status %d (%v)", code, body)
	}
	if body["error"] != nil {
		t.Fatalf("skill reported an error: %v", body["error"])
	}
	result, _ := body["result"].(map[string]any)
	if result["iso"] == nil {
		t.Fatalf("datetime_skill returned no iso field: %v", body["result"])
	}
}

func TestReplayRejectsNonSkillKinds(t *testing.T) {
	d := newTestDeps(t)
	h := NewRouter(d)

	// vector_add is an agent tool, not a registry skill — not replayable.
	if err := d.Memory.AddInvocation(memory.AuditRecord{
		SessionID: 1, Step: 1, Skill: "vector_add", Kind: "tool",
		Args: map[string]any{"text": "x"},
	}); err != nil {
		t.Fatalf("AddInvocation: %v", err)
	}

	code, body := doJSON(t, h, "POST", "/api/invocations/1/replay", "")
	if code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 (%v)", code, body)
	}
}

func TestInvocationsListShape(t *testing.T) {
	d := newTestDeps(t)
	h := NewRouter(d)

	code, body := doJSON(t, h, "GET", "/api/invocations", "")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	// Must serialise as [] rather than null so the UI can iterate it.
	if _, ok := body["invocations"].([]any); !ok {
		t.Fatalf("invocations = %#v, want an array", body["invocations"])
	}
	if body["limit"] != float64(50) {
		t.Fatalf("limit = %v, want the default 50", body["limit"])
	}
}

func TestNotFoundPaths(t *testing.T) {
	h := NewRouter(newTestDeps(t))
	cases := []struct {
		method, path string
		want         int
	}{
		{"GET", "/api/scheduler/tasks/999", http.StatusNotFound},
		{"GET", "/api/scheduler/tasks/abc", http.StatusBadRequest},
		{"GET", "/api/invocations/999", http.StatusNotFound},
		{"GET", "/api/invocations/abc", http.StatusBadRequest},
	}
	for _, c := range cases {
		if code, _ := doJSON(t, h, c.method, c.path, ""); code != c.want {
			t.Fatalf("%s %s: status %d, want %d", c.method, c.path, code, c.want)
		}
	}
}

func TestCORSPreflight(t *testing.T) {
	h := NewRouter(newTestDeps(t))
	r := httptest.NewRequest("OPTIONS", "/api/query", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight status %d, want 204", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Allow-Origin = %q", got)
	}
}

// /health must answer even when the embeddings backend is down, reporting the
// failure rather than erroring out — the gateway is expected to start regardless.
func TestHealthReportsEmbedFailure(t *testing.T) {
	d := newTestDeps(t)
	d.Chat = stubChat{}
	h := NewRouter(d)

	code, body := doJSON(t, h, "GET", "/health", "")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if body["embed_ok"] != false || body["ok"] != false {
		t.Fatalf("expected embed_ok=false with an unreachable backend: %v", body)
	}
	if msg, _ := body["embed_error"].(string); msg == "" {
		t.Fatal("no embed_error reported")
	}
	for _, field := range []string{"provider", "chat_base", "chat_model", "skills_dir", "scheduler_db", "embed_model"} {
		if _, ok := body[field]; !ok {
			t.Fatalf("/health is missing the %q field: %v", field, body)
		}
	}
}

// stubChat satisfies llm.ChatClient without talking to a model; /health only
// reads its Model()/BaseURL().
type stubChat struct{}

func (stubChat) CompleteJSON([]llm.Message) (string, float64, error) { return "{}", 0, nil }
func (stubChat) Model() string                                       { return "stub-model" }
func (stubChat) BaseURL() string                                     { return "http://stub" }
