package markdown

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// The closing delimiter is a line of its own; a "---" inside a value used to
// end the header early.
func TestParseFrontmatterDelimiterMustBeOwnLine(t *testing.T) {
	fm, body, err := parseFrontmatter("---\nname: dash\ndescription: 'a --- b'\n---\nthe body\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if fm.Name != "dash" || fm.Description != "a --- b" {
		t.Errorf("fm = %+v, want name=dash description='a --- b'", fm)
	}
	if body != "the body" {
		t.Errorf("body = %q, want %q", body, "the body")
	}

	// Closing delimiter at EOF, and CRLF line endings.
	fm, body, err = parseFrontmatter("---\r\nname: crlf\r\n---")
	if err != nil || fm.Name != "crlf" || body != "" {
		t.Errorf("EOF delimiter: fm=%+v body=%q err=%v", fm, body, err)
	}

	// "----" is not a delimiter: no closing line means no frontmatter.
	fm, _, _ = parseFrontmatter("---\nname: x\n----\nbody")
	if fm.Name != "" {
		t.Errorf("'----' accepted as closing delimiter: %+v", fm)
	}
}

func TestExecuteAtRefusesPipelineBeyondMaxDepth(t *testing.T) {
	s, err := Load(writeSkill(t, "---\nname: p\ndescription: d\nsteps:\n  - name: s\n    skill: other\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	called := false
	exec := func(string, map[string]any) (any, error) { called = true; return "ok", nil }

	res, _ := s.ExecuteAt(map[string]any{}, exec, MaxPipelineDepth)
	if msg, _ := res.(map[string]any)["error"].(string); !strings.Contains(msg, "nesting depth") || called {
		t.Fatalf("depth %d: res=%#v called=%v, want refusal without running steps", MaxPipelineDepth, res, called)
	}
	res, _ = s.ExecuteAt(map[string]any{}, exec, MaxPipelineDepth-1)
	if _, isErr := res.(map[string]any)["error"]; isErr || !called {
		t.Fatalf("depth %d: res=%#v called=%v, want pipeline to run", MaxPipelineDepth-1, res, called)
	}
}

// A substituted value must not be able to move the request to another host or
// rewrite the path/query.
func TestPipelineEndpointPlaceholdersAreEscaped(t *testing.T) {
	var gotPath, gotRawQuery, gotHost string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotRawQuery, gotHost = r.URL.EscapedPath(), r.URL.RawQuery, r.Host
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	s, err := Load(writeSkill(t, "---\nname: t\ndescription: d\nsteps:\n  - name: s\n    endpoint: '"+
		srv.URL+"/items/{{id}}'\n    method: POST\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	res, _ := s.Execute(map[string]any{"id": "../admin?x=1&y=2#frag @evil.example:80"}, nil)
	step := res.(map[string]any)["results"].([]any)[0].(map[string]any)
	if step["status"] != 200 {
		t.Fatalf("request did not reach the intended server: %#v", step)
	}
	if !strings.HasPrefix(gotHost, "127.0.0.1:") {
		t.Errorf("host = %q", gotHost)
	}
	if gotRawQuery != "" {
		t.Errorf("value injected a query string: %q", gotRawQuery)
	}
	if !strings.HasPrefix(gotPath, "/items/") || strings.Count(gotPath, "/") != 2 {
		t.Errorf("value changed the path structure: %q", gotPath)
	}

	// Host-position injection: "@" must be encoded so it cannot become userinfo.
	if got := resolveStrEscaped("https://api.example.com{{p}}", map[string]any{"p": "@evil.example"}, nil, escapeURLValue); got != "https://api.example.com%40evil.example" {
		t.Errorf("escaped endpoint = %q", got)
	}
	// Unresolved placeholders stay as written.
	if got := resolveStrEscaped("http://h/{{missing}}", nil, nil, escapeURLValue); got != "http://h/{{missing}}" {
		t.Errorf("unresolved placeholder = %q", got)
	}
}

func TestReadBodyCapsAndKeepsUTF8(t *testing.T) {
	// Non-JSON text is cut to maxTextBody bytes without splitting a rune.
	text := strings.Repeat("й", maxTextBody) // 2 bytes each
	body, truncated := readBody(strings.NewReader("x" + text))
	s := body.(string)
	if !truncated || len(s) > maxTextBody || !utf8.ValidString(s) {
		t.Fatalf("len=%d truncated=%v valid=%v", len(s), truncated, utf8.ValidString(s))
	}

	// Oversized bodies are capped at the read limit and reported truncated,
	// even when they would be valid JSON.
	huge := `"` + strings.Repeat("a", maxBodyBytes+10) + `"`
	body, truncated = readBody(strings.NewReader(huge))
	if !truncated || len(body.(string)) > maxTextBody {
		t.Fatalf("huge body: truncated=%v len=%d", truncated, len(body.(string)))
	}

	body, truncated = readBody(strings.NewReader(`{"a":1}`))
	if truncated || body.(map[string]any)["a"] != 1.0 {
		t.Fatalf("small JSON: %#v truncated=%v", body, truncated)
	}
}
