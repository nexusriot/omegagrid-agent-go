package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// --arg / --meta are how every value reaches a skill from the command line.
func TestParseKeyValues(t *testing.T) {
	got, err := parseKeyValues([]string{"city=Berlin", "unit=celsius"})
	if err != nil {
		t.Fatalf("parseKeyValues: %v", err)
	}
	want := map[string]any{"city": "Berlin", "unit": "celsius"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	// Only the first "=" splits, so values may contain more of them — URLs,
	// base64 padding, cron expressions with "=" in them.
	got, err = parseKeyValues([]string{"url=https://x.test/?a=1&b=2", "token=abc=="})
	if err != nil {
		t.Fatalf("parseKeyValues: %v", err)
	}
	if got["url"] != "https://x.test/?a=1&b=2" {
		t.Fatalf("url = %v", got["url"])
	}
	if got["token"] != "abc==" {
		t.Fatalf("token = %v", got["token"])
	}

	// An empty value is legitimate; a missing "=" is not.
	if got, err = parseKeyValues([]string{"empty="}); err != nil || got["empty"] != "" {
		t.Fatalf("empty value: got %v, err %v", got, err)
	}
	if _, err := parseKeyValues([]string{"no-equals-sign"}); err == nil {
		t.Fatal("a pair without '=' was accepted")
	}

	got, err = parseKeyValues(nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("nil pairs: got %v, err %v", got, err)
	}
}

// A later repeat of the same key wins, matching how flags usually behave.
func TestParseKeyValuesDuplicateKey(t *testing.T) {
	got, err := parseKeyValues([]string{"k=first", "k=second"})
	if err != nil {
		t.Fatalf("parseKeyValues: %v", err)
	}
	if got["k"] != "second" {
		t.Fatalf("k = %v, want the last value", got["k"])
	}
}

func TestMultiFlagAccumulates(t *testing.T) {
	var m multiFlag
	for _, v := range []string{"a=1", "b=2"} {
		if err := m.Set(v); err != nil {
			t.Fatalf("Set(%q): %v", v, err)
		}
	}
	if len(m) != 2 || m[0] != "a=1" || m[1] != "b=2" {
		t.Fatalf("multiFlag = %v", m)
	}
	if got := m.String(); got != "a=1, b=2" {
		t.Fatalf("String() = %q", got)
	}
	var empty multiFlag
	if got := empty.String(); got != "" {
		t.Fatalf("empty String() = %q", got)
	}
}

func TestRemoteBaseTrimsSlash(t *testing.T) {
	t.Setenv("OMEGA_REMOTE", "")
	if isRemote() {
		t.Fatal("isRemote() is true with OMEGA_REMOTE unset")
	}
	if got := remoteBase(); got != "" {
		t.Fatalf("remoteBase() = %q", got)
	}

	t.Setenv("OMEGA_REMOTE", "http://localhost:8000/")
	if !isRemote() {
		t.Fatal("isRemote() is false with OMEGA_REMOTE set")
	}
	// The trailing slash must go, or every path becomes "//api/...".
	if got := remoteBase(); got != "http://localhost:8000" {
		t.Fatalf("remoteBase() = %q", got)
	}
}

func TestHttpJSONRoundTrip(t *testing.T) {
	var gotMethod, gotPath, gotCT, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotCT = r.Method, r.URL.Path, r.Header.Get("Content-Type")
		raw := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(raw)
		gotBody = string(raw)
		_, _ = w.Write([]byte(`{"ok":true,"answer":"hi"}`))
	}))
	defer srv.Close()
	t.Setenv("OMEGA_REMOTE", srv.URL)

	var out map[string]any
	if err := httpJSON("POST", "/api/query", map[string]any{"query": "q"}, &out); err != nil {
		t.Fatalf("httpJSON: %v", err)
	}
	if out["answer"] != "hi" {
		t.Fatalf("decoded %v", out)
	}
	if gotMethod != "POST" || gotPath != "/api/query" {
		t.Fatalf("%s %s", gotMethod, gotPath)
	}
	if gotCT != "application/json" {
		t.Fatalf("Content-Type = %q", gotCT)
	}
	if !strings.Contains(gotBody, `"query":"q"`) {
		t.Fatalf("body = %q", gotBody)
	}
}

// A GET with no request body must not advertise a JSON content type, and a nil
// out pointer must not try to decode.
func TestHttpJSONNoBodyNoOut(t *testing.T) {
	var gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("OMEGA_REMOTE", srv.URL)

	if err := httpJSON("DELETE", "/api/memory/abc", nil, nil); err != nil {
		t.Fatalf("httpJSON: %v", err)
	}
	if gotCT != "" {
		t.Fatalf("Content-Type = %q on a bodyless request", gotCT)
	}
}

func TestHttpJSONSurfacesServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"query is required"}`))
	}))
	defer srv.Close()
	t.Setenv("OMEGA_REMOTE", srv.URL)

	err := httpJSON("POST", "/api/query", map[string]any{}, &map[string]any{})
	if err == nil {
		t.Fatal("expected an error for HTTP 400")
	}
	// The operator needs the server's explanation, not just a status code.
	if !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "query is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestHttpJSONUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	t.Setenv("OMEGA_REMOTE", url)

	if err := httpJSON("GET", "/api/skills", nil, &map[string]any{}); err == nil {
		t.Fatal("expected a transport error")
	}
}

// The remote helpers must return the decoded payload, not the zero value they
// started with.
func TestRemoteHelpersDecodePayloads(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/skills":
			_, _ = w.Write([]byte(`{"skills":[{"name":"weather","description":"w"},{"name":"ping_check"}]}`))
		case r.URL.Path == "/api/memory":
			_, _ = w.Write([]byte(`{"hits":[{"id":"m1","text":"a fact"}],"total":7}`))
		case r.URL.Path == "/api/memory/search":
			_, _ = w.Write([]byte(`{"hits":[{"id":"m2","text":"searched"}]}`))
		case r.URL.Path == "/api/sessions":
			_, _ = w.Write([]byte(`{"sessions":[{"id":3,"message_count":4}]}`))
		case r.URL.Path == "/api/scheduler/tasks":
			_, _ = w.Write([]byte(`[{"id":9,"name":"nightly","skill":"reminder"}]`))
		case strings.HasSuffix(r.URL.Path, "/messages"):
			_, _ = w.Write([]byte(`{"messages":[{"id":1,"role":"user","content":"hi"}]}`))
		case strings.HasSuffix(r.URL.Path, "/invoke"):
			_, _ = w.Write([]byte(`{"name":"weather","result":{"temperature_c":15}}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()
	t.Setenv("OMEGA_REMOTE", srv.URL)

	skills, err := listSkills()
	if err != nil {
		t.Fatalf("listSkills: %v", err)
	}
	if len(skills) != 2 || skills[0].Name != "weather" {
		t.Fatalf("listSkills = %v", skills)
	}

	hits, total, err := listMemories(10, 0)
	if err != nil {
		t.Fatalf("listMemories: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "m1" || total != 7 {
		t.Fatalf("listMemories = %v, total %d", hits, total)
	}

	hits, err = searchMemory("q", 5)
	if err != nil {
		t.Fatalf("searchMemory: %v", err)
	}
	if len(hits) != 1 || hits[0].Text != "searched" {
		t.Fatalf("searchMemory = %v", hits)
	}

	sessions, err := listSessions()
	if err != nil {
		t.Fatalf("listSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != 3 {
		t.Fatalf("listSessions = %v", sessions)
	}

	tasks, err := listSchedule()
	if err != nil {
		t.Fatalf("listSchedule: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Name != "nightly" {
		t.Fatalf("listSchedule = %v", tasks)
	}

	msgs, err := exportSession(3)
	if err != nil {
		t.Fatalf("exportSession: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "hi" {
		t.Fatalf("exportSession = %v", msgs)
	}

	result, err := invokeSkill("weather", map[string]any{"city": "Berlin"})
	if err != nil {
		t.Fatalf("invokeSkill: %v", err)
	}
	if result["name"] != "weather" {
		t.Fatalf("invokeSkill = %v", result)
	}
}

func TestRemoteWriteHelpers(t *testing.T) {
	var seen []string
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(raw)
		seen = append(seen, r.Method+" "+r.URL.Path)
		bodies = append(bodies, string(raw))
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("OMEGA_REMOTE", srv.URL)

	if err := addMemory("a durable fact", map[string]any{"tag": "note"}); err != nil {
		t.Fatalf("addMemory: %v", err)
	}
	if err := deleteMemory("m1"); err != nil {
		t.Fatalf("deleteMemory: %v", err)
	}
	if err := createScheduleTask("nightly", "0 3 * * *", "reminder", map[string]any{"message": "x"}, true); err != nil {
		t.Fatalf("createScheduleTask: %v", err)
	}
	if err := deleteScheduleTask(9); err != nil {
		t.Fatalf("deleteScheduleTask: %v", err)
	}

	want := []string{
		"POST /api/memory/add",
		"DELETE /api/memory/m1",
		"POST /api/scheduler/tasks",
		"DELETE /api/scheduler/tasks/9",
	}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("requests = %v, want %v", seen, want)
	}

	// one_shot must reach the gateway, or "remind me at 3pm" repeats daily.
	var task map[string]any
	if err := json.Unmarshal([]byte(bodies[2]), &task); err != nil {
		t.Fatalf("task body: %v", err)
	}
	if task["one_shot"] != true {
		t.Fatalf("one_shot = %v", task["one_shot"])
	}
	if task["cron_expr"] != "0 3 * * *" {
		t.Fatalf("cron_expr = %v", task["cron_expr"])
	}
}

func TestHttpStreamParsesSSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "text/event-stream" {
			t.Errorf("Accept = %q", got)
		}
		_, _ = w.Write([]byte(strings.Join([]string{
			"event: thinking",
			`data: {"step":1}`,
			"",
			"event: final",
			`data: {"answer":"done"}`,
			"",
			"",
		}, "\n")))
	}))
	defer srv.Close()
	t.Setenv("OMEGA_REMOTE", srv.URL)

	var got [][2]string
	err := httpStream("/api/query/stream", map[string]any{"query": "q"}, func(event, data string) {
		got = append(got, [2]string{event, data})
	})
	if err != nil {
		t.Fatalf("httpStream: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("events = %v", got)
	}
	if got[0][0] != "thinking" || got[0][1] != `{"step":1}` {
		t.Fatalf("event 0 = %v", got[0])
	}
	if got[1][0] != "final" || got[1][1] != `{"answer":"done"}` {
		t.Fatalf("event 1 = %v", got[1])
	}
}

func TestColorHelpersPlainWhenPiped(t *testing.T) {
	// Under `go test` stdout is not a TTY, so output must stay unescaped —
	// this is what makes `omega ask … | jq` work.
	for _, fn := range []func(string) string{grey, cyan, green, yellow} {
		if got := fn("text"); got != "text" {
			t.Fatalf("colour helper emitted escapes when piped: %q", got)
		}
	}
	if isTTY() {
		t.Fatal("isTTY() is true under go test")
	}
}
