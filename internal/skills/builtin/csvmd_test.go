package builtin

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const salesCSV = "region,rep,amount,status\nEU,ann,120,open\nUS,bob,80,closed\nEU,cid,\"1,000\",open\nUS,dee,300,open\nEU,eve,45,closed\n"

func TestCsvQuery(t *testing.T) {
	w := WorkspaceConfig{}
	m := runExec(t, CsvQuery(w), map[string]any{"csv": salesCSV, "where": []any{"status == open", "amount > 100"}, "sort_by": "amount", "desc": true, "columns": "rep,amount"})
	rows, _ := m["rows"].([]map[string]string)
	if m["matched_rows"] != 3 || len(rows) != 3 || rows[0]["rep"] != "cid" || rows[1]["rep"] != "dee" || len(rows[0]) != 2 {
		t.Fatalf("filter/sort (\"1,000\" is numeric) = %v", m)
	}
	m = runExec(t, CsvQuery(w), map[string]any{"csv": salesCSV, "group_by": "region", "aggregate": []any{"count", "sum:amount", "avg:amount"}})
	groups := m["groups"].([]map[string]any)
	if len(groups) != 2 || groups[0]["group"] != "EU" || groups[0]["count"] != 3 || groups[0]["sum:amount"] != 1165.0 || groups[1]["sum:amount"] != 380.0 {
		t.Fatalf("group = %v", groups)
	}
	m = runExec(t, CsvQuery(w), map[string]any{"csv": "a;b\n1;x\n2;y\n", "aggregate": "max:a"})
	if g := m["groups"].([]map[string]any); g[0]["max:a"] != 2.0 {
		t.Fatalf("auto-detected ';' and whole-table aggregate = %v", m)
	}
	if m := runExec(t, CsvQuery(w), map[string]any{"csv": salesCSV, "where": "rep contains E"}); m["matched_rows"] != 2 {
		t.Fatalf("contains is case-insensitive: %v", m)
	}
	if m := runExec(t, CsvQuery(w), map[string]any{"csv": "\ufeffname\tn\nx\t1\n", "delimiter": "tab"}); m["columns"].([]string)[0] != "name" {
		t.Fatalf("BOM / tab = %v", m)
	}

	ws, dir := newWorkspace(t)
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "s.csv"), []byte(salesCSV), 0o644)
	if m := runExec(t, CsvQuery(ws), map[string]any{"path": "s.csv", "limit": 1}); m["returned"] != 1 || m["matched_rows"] != 5 {
		t.Fatalf("workspace file = %v", m)
	}
	for _, bad := range []map[string]any{
		{"csv": salesCSV, "where": "nope == 1"}, {"csv": salesCSV, "sort_by": "nope"}, {"csv": salesCSV, "aggregate": "median:amount"},
		{"csv": salesCSV, "where": "garbage"}, {"csv": salesCSV, "delimiter": "x"}, {}, {"path": "../etc/passwd"},
	} {
		if m := runExec(t, CsvQuery(ws), bad); m["error"] == nil {
			t.Errorf("accepted %v: %v", bad, m)
		}
	}
}

const docPage = `<html><head><title>Docs</title></head><body>
<nav><a href="/">Home</a></nav>
<main><h1>Install</h1><p>Run <code>make</code> then read <a href="/guide">the guide</a> or <a href="#x">here</a>.</p>
<ul><li>one</li><li><b>two</b></li></ul><ol><li>a</li><li>b</li><li>c</li><li>d</li><li>e</li><li>f</li><li>g</li><li>h</li><li>i</li><li>j</li></ol>
<pre><code class="language-go">fmt.Println("hi")
</code></pre>
<blockquote>Quoted <em>text</em></blockquote><hr>
<table><tr><th>K</th><th>V</th></tr><tr><td>a|b</td><td>1</td></tr></table>
<img src="logo.png" alt="logo"><script>alert(1)</script><div hidden>secret</div></main><footer>(c)</footer></body></html>`

func TestHtmlToMarkdown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(docPage)) }))
	defer srv.Close()
	m := runExec(t, HtmlToMarkdown(5), map[string]any{"url": srv.URL + "/docs/install"})
	md, _ := m["markdown"].(string)
	for _, want := range []string{
		"# Install", "Run `make` then read [the guide](" + srv.URL + "/guide) or here.",
		"- one", "- **two**", "1. a", "10. j", "```go\nfmt.Println(\"hi\")\n```", "> Quoted *text*", "---",
		"| K | V |", "| --- | --- |", `| a\|b | 1 |`, "![logo](" + srv.URL + "/docs/logo.png)",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	for _, unwanted := range []string{"Home", "alert", "(c)", "secret"} {
		if strings.Contains(md, unwanted) {
			t.Errorf("markdown kept %q:\n%s", unwanted, md)
		}
	}
	if m["title"] != "Docs" {
		t.Errorf("title = %v", m["title"])
	}
	m = runExec(t, HtmlToMarkdown(5), map[string]any{"html": "<p>" + strings.Repeat("word ", 100) + "</p>", "max_chars": 20})
	if m["truncated"] != true {
		t.Errorf("max_chars = %v", m)
	}
	for _, bad := range []map[string]any{{}, {"url": "ftp://x"}} {
		if m := runExec(t, HtmlToMarkdown(5), bad); m["error"] == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}
