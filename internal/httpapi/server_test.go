package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nexusriot/omegagrid-agent-go/internal/config"
	"github.com/nexusriot/omegagrid-agent-go/internal/llm"
	"github.com/nexusriot/omegagrid-agent-go/internal/memory"
	"github.com/nexusriot/omegagrid-agent-go/internal/scheduler"
	"github.com/nexusriot/omegagrid-agent-go/internal/skills"
	"github.com/nexusriot/omegagrid-agent-go/internal/version"
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
		// httptest.NewRequest sends Host: example.com, which the
		// DNS-rebinding guard rejects on a token-less gateway.
		AllowedHosts: []string{"example.com"},
	}
	return newTestDepsWith(t, cfg)
}

// newTestDepsWith builds Deps from cfg against a fresh temp dir.
func newTestDepsWith(t *testing.T, cfg config.Config) Deps {
	t.Helper()
	dir := t.TempDir()
	cfg.DataDir = dir
	cfg.AgentDB = filepath.Join(dir, "agent.sqlite3")
	cfg.VectorDir = filepath.Join(dir, "chromem")
	cfg.SkillsDir = filepath.Join(dir, "skills")
	cfg.SchedulerDB = filepath.Join(dir, "sched.sqlite3")

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
	d := newTestDeps(t)
	d.Cfg.CORSAllowedOrigins = []string{"http://ui.example"}
	h := NewRouter(d)

	r := httptest.NewRequest("OPTIONS", "/api/query", nil)
	r.Header.Set("Origin", "http://ui.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight status %d, want 204", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://ui.example" {
		t.Fatalf("Allow-Origin = %q", got)
	}

	// An origin that is not configured gets no CORS grant.
	r = httptest.NewRequest("OPTIONS", "/api/query", nil)
	r.Header.Set("Origin", "http://evil.example")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unlisted origin granted CORS: %q", got)
	}
}

// A cross-site page can POST a text/plain "simple request" without any
// preflight; the origin guard must refuse it before the handler runs.
func TestOriginGuardRejectsForeignPost(t *testing.T) {
	h := NewRouter(newTestDeps(t))
	r := httptest.NewRequest("POST", "/api/sessions/new", nil)
	r.Header.Set("Origin", "http://evil.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("foreign-origin POST: status %d, want 403", w.Code)
	}

	// Same-origin (the nginx-served UI) and Origin-less clients still work.
	r = httptest.NewRequest("POST", "/api/sessions/new", nil)
	r.Host = "localhost:3000"
	r.Header.Set("Origin", "http://localhost:3000")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("same-origin POST: status %d, want 200 (%s)", w.Code, w.Body.String())
	}
}

// A DNS-rebinding page re-points its own name at the gateway, so its requests
// arrive with Host and Origin both naming the attacker's domain and sail past
// originGuard as "same-origin". Without a token only Host values no public
// resolver can hand out may reach the API.
func TestHostGuardBlocksDNSRebinding(t *testing.T) {
	d := newTestDeps(t)
	d.Cfg.AllowedHosts = nil
	h := NewRouter(d)

	send := func(method, path, host string) int {
		r := httptest.NewRequest(method, path, nil)
		r.Host = host
		r.Header.Set("Origin", "http://"+host)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}

	for _, host := range []string{"evil.example", "evil.example:8000", "EVIL.example.", "localhost.evil.example"} {
		if code := send("POST", "/api/sessions/new", host); code != http.StatusForbidden {
			t.Errorf("POST from rebinding host %q: status %d, want 403", host, code)
		}
		if code := send("GET", "/api/invocations", host); code != http.StatusForbidden {
			t.Errorf("GET from rebinding host %q: status %d, want 403 (reads leak the audit log)", host, code)
		}
	}
	for _, host := range []string{"localhost", "localhost:8000", "127.0.0.1:8000", "[::1]:8000", "gateway:8000", "ui.localhost", "192.168.1.20"} {
		if code := send("POST", "/api/sessions/new", host); code != http.StatusOK {
			t.Errorf("POST with trusted host %q: status %d, want 200", host, code)
		}
	}

	d.Cfg.AllowedHosts = []string{"agent.example.org"}
	h = NewRouter(d)
	if code := send("POST", "/api/sessions/new", "agent.example.org:80"); code != http.StatusOK {
		t.Errorf("allowlisted host: status %d, want 200", code)
	}

	// A token defeats rebinding on its own (the page cannot read it), so the
	// host check steps aside and a token-protected gateway keeps serving any
	// hostname.
	d.Cfg.AllowedHosts = nil
	d.Cfg.APIToken = "s3cret"
	h = NewRouter(d)
	r := httptest.NewRequest("GET", "/api/sessions", nil)
	r.Host = "agent.example.org"
	r.Header.Set("Authorization", "Bearer s3cret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("token + dotted host: status %d, want 200", w.Code)
	}
}

func TestAPITokenRequired(t *testing.T) {
	d := newTestDeps(t)
	d.Cfg.APIToken = "s3cret"
	h := NewRouter(d)

	if code, _ := doJSON(t, h, "GET", "/api/sessions", ""); code != http.StatusUnauthorized {
		t.Fatalf("no token: status %d, want 401", code)
	}
	r := httptest.NewRequest("GET", "/api/sessions", nil)
	r.Header.Set("Authorization", "Bearer s3cret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("valid token: status %d, want 200", w.Code)
	}
	// /health stays open for container healthchecks.
	r = httptest.NewRequest("GET", "/health", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code == http.StatusUnauthorized {
		t.Fatal("/health must not require the API token")
	}
}

func TestRequestBodyLimit(t *testing.T) {
	h := NewRouter(newTestDeps(t))
	big := `{"text":"` + strings.Repeat("a", maxRequestBodyBytes+1) + `"}`
	code, _ := doJSON(t, h, "POST", "/api/memory/add", big)
	if code < 400 || code >= 500 {
		t.Fatalf("oversized body: status %d, want 4xx", code)
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
	for _, field := range []string{"provider", "chat_base", "chat_model", "skills_dir", "scheduler_db", "embed_model", "embed_provider"} {
		if _, ok := body[field]; !ok {
			t.Fatalf("/health is missing the %q field: %v", field, body)
		}
	}
}

// /health reports the backend embeddings actually run on, which is not always
// the chat provider: opencode serves none, so it falls back to Ollama.
func TestHealthReportsEffectiveEmbedProvider(t *testing.T) {
	tests := []struct {
		provider      string
		embedProvider string
		wantProvider  string
		wantModel     string
	}{
		{provider: "ollama", wantProvider: "ollama", wantModel: "nomic-embed-text"},
		{provider: "opencode", wantProvider: "ollama", wantModel: "nomic-embed-text"},
		{provider: "openai", wantProvider: "openai", wantModel: "text-embedding-3-small"},
		{provider: "digitalocean", wantProvider: "digitalocean", wantModel: "do-embed"},
		{provider: "opencode", embedProvider: "openai", wantProvider: "openai", wantModel: "text-embedding-3-small"},
		{provider: "opencode", embedProvider: "digitalocean", wantProvider: "digitalocean", wantModel: "do-embed"},
	}
	for _, tt := range tests {
		t.Run(tt.provider+"/"+tt.embedProvider, func(t *testing.T) {
			d := newTestDeps(t)
			d.Chat = stubChat{}
			d.Cfg.Provider = tt.provider
			d.Cfg.EmbedProvider = tt.embedProvider
			d.Cfg.OllamaEmbedModel = "nomic-embed-text"
			d.Cfg.OpenAIEmbedModel = "text-embedding-3-small"
			d.Cfg.DigitalOceanEmbedModel = "do-embed"

			_, body := doJSON(t, NewRouter(d), "GET", "/health", "")
			if body["embed_provider"] != tt.wantProvider {
				t.Errorf("embed_provider = %v, want %q", body["embed_provider"], tt.wantProvider)
			}
			if body["embed_model"] != tt.wantModel {
				t.Errorf("embed_model = %v, want %q", body["embed_model"], tt.wantModel)
			}
			// The chat provider is still reported verbatim.
			if body["provider"] != tt.provider {
				t.Errorf("provider = %v, want %q", body["provider"], tt.provider)
			}
		})
	}
}

// stubChat satisfies llm.ChatClient without talking to a model; /health only
// reads its Model()/BaseURL().
type stubChat struct{}

func (stubChat) CompleteJSON([]llm.Message) (string, float64, error) { return "{}", 0, nil }
func (stubChat) Model() string                                       { return "stub-model" }
func (stubChat) BaseURL() string                                     { return "http://stub" }

func TestSchedulerExtendedRoutes(t *testing.T) {
	d := newTestDeps(t)
	h := NewRouter(d)
	code, body := doJSON(t, h, "POST", "/api/scheduler/tasks",
		`{"name":"digest","cron_expr":"0 8 * * *","prompt":"summarise","timezone":"Europe/Berlin","max_retries":2}`)
	if code != http.StatusOK || body["skill"] != "agent" || body["timezone"] != "Europe/Berlin" || body["max_retries"] != float64(2) {
		t.Fatalf("create prompt task = %d %v", code, body)
	}
	id := int(body["id"].(float64))
	if code, body := doJSON(t, h, "POST", "/api/scheduler/tasks", `{"name":"x","cron_expr":"0 8 * * *","skill":"weather","timezone":"Mars/Base"}`); code != http.StatusBadRequest {
		t.Fatalf("bad timezone = %d %v", code, body)
	}
	path := "/api/scheduler/tasks/" + strconv.Itoa(id)
	if code, _ := doJSON(t, h, "POST", path+"/run", ""); code != http.StatusServiceUnavailable {
		t.Fatalf("run without a runner = %d, want 503", code)
	}

	d.Runner = scheduler.NewRunner(d.Scheduler, nil, "", time.Hour)
	d.Runner.SetAgent(func(context.Context, string) (string, error) { return "digest ready", nil })
	h = NewRouter(d)
	code, body = doJSON(t, h, "POST", path+"/run", "")
	if code != http.StatusOK || body["ok"] != true || body["result"] != "digest ready" {
		t.Fatalf("run = %d %v", code, body)
	}
	r := httptest.NewRequest("GET", path+"/runs", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var runs []map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &runs)
	if w.Code != http.StatusOK || len(runs) != 1 || runs[0]["trigger"] != "manual" {
		t.Fatalf("runs = %d %s", w.Code, w.Body.String())
	}
	if code, _ := doJSON(t, h, "GET", "/api/scheduler/tasks/999/runs", ""); code != http.StatusNotFound {
		t.Fatalf("runs of a missing task = %d", code)
	}
	if code, _ := doJSON(t, h, "POST", "/api/scheduler/tasks/999/run", ""); code != http.StatusNotFound {
		t.Fatalf("run of a missing task = %d", code)
	}
}

// /health and the MCP handshake report the one release version.
func TestVersionIsReported(t *testing.T) {
	d := newTestDeps(t)
	d.Chat = stubChat{}
	d.Cfg.MCPServerEnabled = true
	d.ToolProvider = &stubProvider{}
	h := NewRouter(d)
	if _, body := doJSON(t, h, "GET", "/health", ""); body["version"] != version.Version {
		t.Fatalf("/health version = %v, want %s", body["version"], version.Version)
	}
	r := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), `"version":"`+version.Version+`"`) {
		t.Fatalf("MCP serverInfo does not carry %s: %s", version.Version, w.Body.String())
	}
}
