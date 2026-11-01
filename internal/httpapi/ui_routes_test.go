package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestSessionSearchRenameDelete(t *testing.T) {
	d := newTestDeps(t)
	h := NewRouter(d)
	a, _ := d.Memory.CreateSession()
	b, _ := d.Memory.CreateSession()
	_ = d.Memory.AddMessage(a, "user", "how do I rotate the TLS cert?")
	_ = d.Memory.AddMessage(b, "user", "what is the weather 100% chance_of rain")

	_, body := doJSON(t, h, "GET", "/api/sessions?q=tls", "")
	got := body["sessions"].([]any)
	if len(got) != 1 || int(got[0].(map[string]any)["id"].(float64)) != a {
		t.Fatalf("search tls = %v", got)
	}
	if p := got[0].(map[string]any)["preview"]; !strings.Contains(p.(string), "rotate the TLS") {
		t.Fatalf("preview = %v", p)
	}
	// LIKE wildcards in the query are literals.
	if _, body := doJSON(t, h, "GET", "/api/sessions?q=100%25+chance_of", ""); len(body["sessions"].([]any)) != 1 {
		t.Fatalf("literal %% / _ search = %v", body)
	}
	if _, body := doJSON(t, h, "GET", "/api/sessions?q=_", ""); len(body["sessions"].([]any)) != 1 {
		t.Fatalf("'_' matched every session: %v", body)
	}

	path := "/api/sessions/" + strconv.Itoa(a)
	if code, body := doJSON(t, h, "PATCH", path, `{"title":"  Cert rotation  "}`); code != http.StatusOK || body["title"] != "Cert rotation" {
		t.Fatalf("rename = %d %v", code, body)
	}
	_, body = doJSON(t, h, "GET", "/api/sessions?q=rotation", "")
	if s := body["sessions"].([]any); len(s) != 1 || s[0].(map[string]any)["title"] != "Cert rotation" {
		t.Fatalf("search by title = %v", body)
	}
	if code, _ := doJSON(t, h, "PATCH", "/api/sessions/999", `{"title":"x"}`); code != http.StatusNotFound {
		t.Fatalf("rename missing = %d", code)
	}

	if code, _ := doJSON(t, h, "DELETE", path, ""); code != http.StatusOK {
		t.Fatalf("delete = %d", code)
	}
	if msgs, _ := d.Memory.ListMessages(a, 10, 0); len(msgs) != 0 {
		t.Fatalf("messages survived the delete: %v", msgs)
	}
	if code, _ := doJSON(t, h, "DELETE", path, ""); code != http.StatusNotFound {
		t.Fatalf("second delete = %d", code)
	}
}

func TestSkillSourceEditing(t *testing.T) {
	d := newTestDeps(t)
	h := NewRouter(d)
	src := "---\nname: greet_user\ndescription: says hello\nparameters:\n  who:\n    type: string\n    required: true\n---\n\nGreet {{who}} warmly.\n"
	body := `{"content":` + quote(src) + `}`

	_, out := doJSON(t, h, "POST", "/api/skills/validate", body)
	if out["ok"] != true || out["skill"].(map[string]any)["name"] != "greet_user" {
		t.Fatalf("validate = %v", out)
	}
	_, out = doJSON(t, h, "POST", "/api/skills/validate", `{"content":"---\nname: http_request\n---\n"}`)
	if out["ok"] != false || !strings.Contains(out["error"].(string), "built-in") {
		t.Fatalf("validate of a built-in name = %v", out)
	}
	_, out = doJSON(t, h, "POST", "/api/skills/validate", `{"content":"---\nname: [broken\n---\n"}`)
	if out["ok"] != false {
		t.Fatalf("validate of broken YAML = %v", out)
	}

	if code, out := doJSON(t, h, "POST", "/api/skills/source", body); code != http.StatusOK || out["created"] != true {
		t.Fatalf("save = %d %v", code, out)
	}
	_, out = doJSON(t, h, "GET", "/api/skills", "")
	var kind string
	for _, s := range out["skills"].([]any) {
		m := s.(map[string]any)
		if m["name"] == "greet_user" {
			kind, _ = m["kind"].(string)
		}
		if m["name"] == "http_request" && m["kind"] != "builtin" {
			t.Fatalf("http_request kind = %v", m["kind"])
		}
	}
	if kind != "markdown" {
		t.Fatalf("saved skill kind = %q", kind)
	}
	if _, out := doJSON(t, h, "GET", "/api/skills/greet_user/source", ""); out["content"] != src {
		t.Fatalf("source = %v", out)
	}
	if code, _ := doJSON(t, h, "GET", "/api/skills/http_request/source", ""); code != http.StatusNotFound {
		t.Fatalf("source of a built-in = %d", code)
	}
	if code, out := doJSON(t, h, "POST", "/api/skills/source", body); code != http.StatusOK || out["created"] != false {
		t.Fatalf("resave = %d %v", code, out)
	}
	if code, _ := doJSON(t, h, "DELETE", "/api/skills/greet_user", ""); code != http.StatusOK {
		t.Fatalf("delete = %d", code)
	}
	if code, _ := doJSON(t, h, "DELETE", "/api/skills/http_request", ""); code != http.StatusNotFound {
		t.Fatalf("delete of a built-in = %d", code)
	}

	d.Cfg.PlaygroundEnabled = false
	h = NewRouter(d)
	if code, _ := doJSON(t, h, "POST", "/api/skills/source", body); code != http.StatusForbidden {
		t.Fatalf("save with the playground off = %d", code)
	}
}

func TestUsageStats(t *testing.T) {
	d := newTestDeps(t)
	h := NewRouter(withAgent(d, &scriptedChat{replies: []string{
		`{"type":"tool_call","tool":"math_eval","args":{"expression":"1+1"}}`,
		`{"type":"final","answer":"2"}`,
	}}))
	doJSON(t, h, "POST", "/api/query", `{"query":"1+1?"}`)
	_, out := doJSON(t, h, "GET", "/api/stats/usage?days=1", "")
	if out["bucket"] != "hour" {
		t.Fatalf("bucket = %v", out["bucket"])
	}
	var queries, calls float64
	for _, b := range out["buckets"].([]any) {
		m := b.(map[string]any)
		queries += m["queries"].(float64)
		calls += m["tool_calls"].(float64)
	}
	if queries != 1 || calls != 1 {
		t.Fatalf("queries=%v calls=%v", queries, calls)
	}
	skills := out["skills"].([]any)
	if len(skills) != 1 || skills[0].(map[string]any)["skill"] != "math_eval" {
		t.Fatalf("skills = %v", skills)
	}
	if _, out := doJSON(t, h, "GET", "/api/stats/usage?days=30", ""); out["bucket"] != "day" || len(out["buckets"].([]any)) < 30 {
		t.Fatalf("30-day usage = %v", out["bucket"])
	}
}

func quote(s string) string {
	b := strings.Builder{}
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
