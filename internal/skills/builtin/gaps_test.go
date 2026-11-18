package builtin

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDecodeCharset(t *testing.T) {
	cases := []struct {
		label string
		in    []byte
		want  string
	}{
		{"windows-1252", []byte{0x93, 'h', 'i', 0x94, ' ', 0x80, '5', ' ', 0x97}, "“hi” €5 —"},
		{"ISO-8859-1", []byte{'c', 'a', 'f', 0xe9, 0x96}, "café–"}, // latin-1 labels decode as cp1252, like browsers
		{"windows-1251", []byte{0xcf, 0xf0, 0xe8, 0xe2, 0xe5, 0xf2}, "Привет"},
		{"koi8-r", []byte{0xf0, 0xd2, 0xc9, 0xd7, 0xc5, 0xd4}, "Привет"},
		{"UTF-8", []byte("ünïcode"), "ünïcode"},
		{"", []byte("plain"), "plain"},
	}
	for _, c := range cases {
		r, err := decodeCharset(c.label, strings.NewReader(string(c.in)))
		if err != nil {
			t.Fatalf("%s: %v", c.label, err)
		}
		b, _ := io.ReadAll(r)
		if string(b) != c.want {
			t.Errorf("%s: %q want %q", c.label, b, c.want)
		}
	}
	if _, err := decodeCharset("x-klingon", strings.NewReader("")); err == nil {
		t.Error("unknown charset accepted")
	}
	// IMAP keeps an unknown part as is; RSS refuses it.
	r, err := charsetReader("x-klingon", strings.NewReader("raw"))
	if b, _ := io.ReadAll(r); err != nil || string(b) != "raw" {
		t.Errorf("imap passthrough %q %v", b, err)
	}
	if _, err := feedCharsetReader("x-klingon", strings.NewReader("")); err == nil {
		t.Error("feed accepted an unknown encoding")
	}
	if got := redactSecret("login user hunter2", "hunter2"); got != "login user <redacted>" {
		t.Errorf("redact %q", got)
	}
	if got := redactSecret("x", ""); got != "x" {
		t.Errorf("empty secret %q", got)
	}
}

func TestSqlCellConversions(t *testing.T) {
	ts := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	cases := []struct {
		in, want any
	}{
		{nil, nil},
		{[]byte("text"), "text"},
		{[]byte{0xff, 0x00, 0xfe}, "<3 bytes binary>"},
		{strings.Repeat("é", sqlMaxCellRunes+5), strings.Repeat("é", sqlMaxCellRunes) + "…"},
		{ts, "2025-01-02T03:04:05Z"},
		{int64(7), int64(7)},
		{true, true},
		{map[string]any{"a": 1.0}, map[string]any{"a": 1.0}}, // JSON-able driver types keep their shape
		{struct{ X int }{3}, map[string]any{"X": 3.0}},
	}
	for _, c := range cases {
		got := sqlCell(c.in)
		gj, _ := json.Marshal(got)
		wj, _ := json.Marshal(c.want)
		if string(gj) != string(wj) {
			t.Errorf("sqlCell(%T) = %s want %s", c.in, gj, wj)
		}
	}
}

func TestYamlQueryWorkspaceFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "values.yaml"), []byte("replicas: 3\nimage: {tag: v1}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	q := YamlQuery(WorkspaceConfig{Dir: dir})
	res, err := q(map[string]any{"file": "values.yaml", "query": ".image.tag"})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(res); !strings.Contains(string(b), `"v1"`) {
		t.Fatalf("file query %s", b)
	}
	for _, bad := range []map[string]any{
		{"file": "../outside.yaml", "query": "."},
		{"file": "missing.yaml", "query": "."},
	} {
		res, _ := q(bad)
		if m, _ := res.(map[string]any); m["error"] == nil {
			t.Errorf("%v: expected an error, got %v", bad, res)
		}
	}
	if _, err := readWorkspaceFile(WorkspaceConfig{}, "x.yaml", 10); err == nil {
		t.Error("read without a workspace")
	}
}

func TestSmallHelpers(t *testing.T) {
	for v, want := range map[any]int64{1.7e9: 1700000000, 5: 5, int64(6): 6, json.Number("7"): 7, " 8 ": 8} {
		if got, ok := jwtNumericClaim(v); !ok || got != want {
			t.Errorf("jwtNumericClaim(%v) = %d %v", v, got, ok)
		}
	}
	if _, ok := jwtNumericClaim("soon"); ok {
		t.Error("non-numeric claim accepted")
	}
	for v, want := range map[string]any{"null": nil, "boolean": true, "number": 1.0, "string": "s", "array": []any{}, "object": map[string]any{}} {
		if got := jqType(want); got != v {
			t.Errorf("jqType(%v) = %s want %s", want, got, v)
		}
	}
	if jqType(json.Number("1")) != "number" || jqType(struct{}{}) != "struct {}" {
		t.Error("jqType fallbacks")
	}
	names := map[uint16]string{tls.VersionTLS10: "TLS1.0", tls.VersionTLS11: "TLS1.1", tls.VersionTLS12: "TLS1.2", tls.VersionTLS13: "TLS1.3", 0x9999: "0x9999"}
	for v, want := range names {
		if got := tlsVersionName(v); got != want {
			t.Errorf("tlsVersionName(%x) = %s", v, got)
		}
	}
	ss := map[http.SameSite]string{http.SameSiteDefaultMode: "Default", http.SameSiteLaxMode: "Lax", http.SameSiteStrictMode: "Strict", http.SameSiteNoneMode: "None", 9: "9"}
	for v, want := range ss {
		if got := sameSiteName(v); got != want {
			t.Errorf("sameSiteName(%d) = %s", v, got)
		}
	}
	days := []byDay{{wd: time.Monday}, {wd: time.Friday}}
	if !weekdayIn(time.Friday, days) || weekdayIn(time.Sunday, days) {
		t.Error("weekdayIn")
	}
	c := newCanvas(300, 200)
	if got := fitText(c, "short", 500); got != "short" {
		t.Errorf("fitText kept %q", got)
	}
	if got := fitText(c, strings.Repeat("long label ", 20), 60); !strings.HasSuffix(got, "...") || c.textWidth(got) > 60 {
		t.Errorf("fitText %q (%dpx)", got, c.textWidth(got))
	}
}

func TestOpenAPISpecSources(t *testing.T) {
	spec := `{"openapi":"3.0.0","info":{"title":"T","version":"1"},"servers":[{"url":"/api/v2"}],
	  "paths":{"/ping":{"get":{"operationId":"ping","responses":{"200":{"description":"ok"}}}}}}`
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/specs/t.json":
			hits++
			_, _ = io.WriteString(w, spec)
		case "/specs/gone.json":
			http.Error(w, "gone", http.StatusGone)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// A spec fetched by URL resolves a relative server URL against it, and is cached.
	cfg := OpenAPIConfig{Specs: map[string]string{"t": srv.URL + "/specs/t.json", "gone": srv.URL + "/specs/gone.json"}, Order: []string{"t", "gone"}}
	for i := 0; i < 2; i++ {
		s, err := cfg.spec(context.Background(), "t")
		if err != nil {
			t.Fatal(err)
		}
		if s.BaseURL != srv.URL+"/api/v2" {
			t.Fatalf("base %q", s.BaseURL)
		}
	}
	if hits != 1 {
		t.Fatalf("spec fetched %d times, want 1 (cached)", hits)
	}
	if _, err := cfg.spec(context.Background(), "gone"); err == nil || !strings.Contains(err.Error(), "410") {
		t.Fatalf("HTTP error not reported: %v", err)
	}

	// An absolute-path spec with a relative server needs BASE_URL.
	abs := filepath.Join(t.TempDir(), "abs.json")
	if err := os.WriteFile(abs, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	fileCfg := OpenAPIConfig{Specs: map[string]string{"f": abs}, Order: []string{"f"}}
	if _, err := fileCfg.spec(context.Background(), "f"); err == nil || !strings.Contains(err.Error(), "SKILL_OPENAPI_F_BASE_URL") {
		t.Fatalf("missing base URL not explained: %v", err)
	}
	fileCfg.BaseURL = map[string]string{"f": "https://api.example.com/"}
	s, err := fileCfg.spec(context.Background(), "f")
	if err != nil || s.BaseURL != "https://api.example.com" {
		t.Fatalf("override: %+v %v", s, err)
	}
	// A relative path without a workspace fails cleanly.
	if _, err := (OpenAPIConfig{Specs: map[string]string{"w": "specs/x.json"}}).spec(context.Background(), "w"); err == nil {
		t.Fatal("workspace spec read without a workspace")
	}

	for in, want := range map[[2]string]string{
		{"https://h.example/specs/a.json", "/v1"}:           "https://h.example/v1",
		{"https://h.example/specs/a.json", "v1"}:            "https://h.example/specs/v1",
		{"https://h.example/a.json", "http://other.test/x"}: "http://other.test/x",
		{"specs/a.json", "/v1"}:                             "",
	} {
		if got := resolveAgainst(in[0], in[1]); got != want {
			t.Errorf("resolveAgainst(%s, %s) = %q want %q", in[0], in[1], got, want)
		}
	}
	if got := redactQuery("https://x.test/p?api_key=s&token=t&q=ok"); strings.Contains(got, "=s") || strings.Contains(got, "=t&") || !strings.Contains(got, "q=ok") {
		t.Errorf("redactQuery %s", got)
	}
}
