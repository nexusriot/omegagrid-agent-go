package markdown

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	return path
}

// Skill endpoints come from markdown frontmatter with {{placeholders}} that may
// resolve to anything — or stay unresolved. http.NewRequest returns a nil
// request for a URL it cannot parse, and the old `req, _ :=` sites then panicked
// on req.Header.
func TestExecSingleMalformedEndpointDoesNotPanic(t *testing.T) {
	for _, endpoint := range []string{
		"http://exa mple.com/api",
		"ht!tp://example.com",
		"http://[::1",
	} {
		t.Run(endpoint, func(t *testing.T) {
			s, err := Load(writeSkill(t, "---\nname: t\ndescription: d\nendpoint: "+endpoint+"\n---\n"))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			res, err := s.Execute(map[string]any{"q": "x"}, nil)
			if err != nil {
				t.Fatalf("Execute returned a hard error: %v", err)
			}
			m := res.(map[string]any)
			if msg, _ := m["error"].(string); msg == "" {
				t.Fatalf("no error reported for %q: %v", endpoint, m)
			}
		})
	}
}

func TestExecPipelineMalformedEndpointDoesNotPanic(t *testing.T) {
	s, err := Load(writeSkill(t, `---
name: t
description: d
steps:
  - name: broken
    endpoint: 'http://exa mple.com/{{q}}'
---
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	res, err := s.Execute(map[string]any{"q": "x"}, nil)
	if err != nil {
		t.Fatalf("Execute returned a hard error: %v", err)
	}
	results, _ := res.(map[string]any)["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("expected 1 step result, got %v", results)
	}
	step := results[0].(map[string]any)
	body, _ := step["body"].(map[string]any)
	if msg, _ := body["error"].(string); msg == "" {
		t.Fatalf("step did not report the bad endpoint: %v", step)
	}
}

// The refactor to a shared doRequest must preserve the GET/POST semantics:
// GET steps pass args as query parameters, POST sends them as a JSON body.
func TestExecSingleGetSendsArgsAsQuery(t *testing.T) {
	var gotQuery, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		gotUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	s, err := Load(writeSkill(t, "---\nname: t\ndescription: d\nendpoint: "+srv.URL+"\n---\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := s.Execute(map[string]any{"city": "Berlin"}, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotQuery != "city=Berlin" {
		t.Fatalf("query = %q, want %q", gotQuery, "city=Berlin")
	}
	if gotUA != "OmegaGridAgent/1.0" {
		t.Fatalf("User-Agent = %q", gotUA)
	}
}

func TestExecSinglePostSendsArgsAsBody(t *testing.T) {
	var gotBody, gotCT, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		gotBody = string(b)
		gotCT = r.Header.Get("Content-Type")
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	s, err := Load(writeSkill(t, "---\nname: t\ndescription: d\nmethod: POST\nendpoint: "+srv.URL+"\n---\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := s.Execute(map[string]any{"city": "Berlin"}, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(gotBody, `"city":"Berlin"`) {
		t.Fatalf("body = %q, want the args JSON", gotBody)
	}
	if gotCT != "application/json" {
		t.Fatalf("Content-Type = %q", gotCT)
	}
	if gotQuery != "" {
		t.Fatalf("POST must not put args in the query string, got %q", gotQuery)
	}
}

// A pipeline HTTP step keeps its own headers and merges kwargs with step params.
func TestExecPipelineStepHeadersAndParams(t *testing.T) {
	var gotQuery, gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Encode()
		gotHeader = r.Header.Get("X-Token")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	s, err := Load(writeSkill(t, `---
name: t
description: d
steps:
  - name: one
    endpoint: `+srv.URL+`
    headers:
      X-Token: secret
    params:
      extra: yes
---
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := s.Execute(map[string]any{"city": "Berlin"}, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotHeader != "secret" {
		t.Fatalf("X-Token = %q, want %q", gotHeader, "secret")
	}
	if !strings.Contains(gotQuery, "city=Berlin") || !strings.Contains(gotQuery, "extra=") {
		t.Fatalf("query = %q, want both the kwarg and the step param", gotQuery)
	}
}
