package builtin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWikipedia(t *testing.T) {
	var lastPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastPath = r.URL.RequestURI()
		switch {
		case strings.Contains(r.URL.Path, "/summary/Go_(programming_language)"):
			_, _ = w.Write([]byte(`{"title":"Go (programming language)","type":"standard","description":"Programming language","extract":"Go is a language.","content_urls":{"desktop":{"page":"https://en.wikipedia.org/wiki/Go"}}}`))
		case strings.Contains(r.URL.Path, "/summary/Mercury"):
			_, _ = w.Write([]byte(`{"title":"Mercury","type":"disambiguation","extract":"Mercury may refer to:"}`))
		case strings.HasSuffix(r.URL.Path, "/search/page"):
			_, _ = w.Write([]byte(`{"pages":[{"key":"Go_(game)","title":"Go (game)","description":"Board game","excerpt":"<span class=\"searchmatch\">Go</span> is a game"}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	saved := wikipediaBase
	wikipediaBase = func(lang string) string { return srv.URL + "/" + lang }
	defer func() { wikipediaBase = saved }()

	m := runExec(t, Wikipedia(5), map[string]any{"query": "Go (programming language)"})
	if m["summary"] != "Go is a language." || m["found"] != true {
		t.Fatalf("summary = %v", m)
	}
	if !strings.HasPrefix(lastPath, "/en/api/rest_v1/page/summary/Go_%28programming_language%29") {
		t.Fatalf("path = %s", lastPath)
	}
	if m := runExec(t, Wikipedia(5), map[string]any{"query": "Mercury", "lang": "de"}); m["disambiguation"] != true || !strings.HasPrefix(lastPath, "/de/") {
		t.Fatalf("disambiguation = %v (%s)", m, lastPath)
	}
	if m := runExec(t, Wikipedia(5), map[string]any{"query": "Nothing Here"}); m["found"] != false {
		t.Fatalf("missing = %v", m)
	}
	m = runExec(t, Wikipedia(5), map[string]any{"action": "search", "query": "go"})
	res := m["results"].([]map[string]any)
	if len(res) != 1 || res[0]["snippet"] != "Go is a game" || !strings.HasSuffix(res[0]["url"].(string), "/wiki/Go_%28game%29") {
		t.Fatalf("search = %v", m)
	}
	if m := runExec(t, Wikipedia(5), map[string]any{"query": "x", "lang": "../evil"}); m["error"] == nil {
		t.Fatal("bad lang accepted")
	}
}

const ecbXML = `<?xml version="1.0" encoding="UTF-8"?>
<gesmes:Envelope xmlns:gesmes="http://www.gesmes.org/xml/2002-08-01" xmlns="http://www.ecb.int/vocabulary/2002-08-01/eurofxref">
<Cube><Cube time="2026-10-30"><Cube currency="USD" rate="1.0800"/><Cube currency="JPY" rate="162.00"/><Cube currency="GBP" rate="0.8400"/></Cube></Cube></gesmes:Envelope>`

func TestCurrencyConvert(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; _, _ = w.Write([]byte(ecbXML)) }))
	defer srv.Close()
	saved := ecbURL
	ecbURL = srv.URL
	ecbCache.Lock()
	ecbCache.rates = nil
	ecbCache.Unlock()
	defer func() { ecbURL = saved }()

	m := runExec(t, CurrencyConvert(5), map[string]any{"amount": 100, "from": "usd", "to": []any{"EUR", "JPY", "GBP"}})
	r := m["results"].([]map[string]any)
	if r[0]["amount"] != 92.59 || r[1]["amount"] != 15000.0 || r[2]["amount"] != 77.78 || m["rates_date"] != "2026-10-30" {
		t.Fatalf("convert = %v", m)
	}
	runExec(t, CurrencyConvert(5), map[string]any{"from": "EUR", "to": "USD"})
	if hits != 1 {
		t.Fatalf("rates fetched %d times; they are cached for an hour", hits)
	}
	if m := runExec(t, CurrencyConvert(5), map[string]any{"from": "XYZ"}); m["error"] == nil || m["supported"] == nil {
		t.Fatalf("unknown currency = %v", m)
	}
}

func TestGithub(t *testing.T) {
	var auth, lastURI string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, lastURI = r.Header.Get("Authorization"), r.URL.RequestURI()
		switch {
		case r.URL.Path == "/repos/golang/go":
			_ = json.NewEncoder(w).Encode(map[string]any{"full_name": "golang/go", "stargazers_count": 120000, "license": map[string]any{"spdx_id": "BSD-3-Clause"}, "owner": map[string]any{"login": "golang"}, "ignored": "x"})
		case r.URL.Path == "/repos/golang/go/issues":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"number": 1, "title": "bug", "user": map[string]any{"login": "a"}},
				{"number": 2, "title": "a PR", "pull_request": map[string]any{}},
				{"number": 3, "title": "other bug"},
			})
		case r.URL.Path == "/search/repositories":
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 1, "items": []map[string]any{{"full_name": "x/y"}}})
		case r.URL.Path == "/repos/no/such":
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
		}
	}))
	defer srv.Close()
	saved := githubAPI
	githubAPI = srv.URL
	defer func() { githubAPI = saved }()

	m := runExec(t, Github("", 5), map[string]any{"action": "repo", "repo": "https://github.com/golang/go"})
	if m["stargazers_count"] != 120000.0 || m["license_spdx_id"] != "BSD-3-Clause" || m["ignored"] != nil || auth != "" {
		t.Fatalf("repo = %v (auth %q)", m, auth)
	}
	m = runExec(t, Github("tok", 5), map[string]any{"action": "issues", "repo": "golang/go", "limit": 2})
	issues := m["issues"].([]map[string]any)
	if len(issues) != 2 || issues[1]["number"] != 3.0 || issues[0]["user_login"] != "a" || auth != "Bearer tok" {
		t.Fatalf("issues = %v (auth %q)", m, auth)
	}
	m = runExec(t, Github("", 5), map[string]any{"action": "search", "query": "language:go agent"})
	if m["count"] != 1 || !strings.Contains(lastURI, "q=language%3Ago+agent") {
		t.Fatalf("search = %v (%s)", m, lastURI)
	}
	if m := runExec(t, Github("", 5), map[string]any{"action": "repo", "repo": "no/such"}); !strings.Contains(m["hint"].(string), "SKILL_GITHUB_TOKEN") {
		t.Fatalf("rate limit = %v", m)
	}
	for _, bad := range []map[string]any{{"action": "repo", "repo": "../../etc"}, {"action": "nuke"}, {"action": "search"}, {"action": "issues", "repo": "a/b", "state": "x"}} {
		if m := runExec(t, Github("", 5), bad); m["error"] == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}
