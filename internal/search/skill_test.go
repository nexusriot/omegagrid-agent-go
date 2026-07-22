package search

import (
	"reflect"
	"strings"
	"testing"
)

const twoResultsHTML = `
<div class="results">
  <div class="result">
    <a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Ffoo&rut=aaa">First &amp; Title</a>
    <a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Ffoo">This is the <b>first</b> snippet.</a>
  </div>
  <div class="result">
    <a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.org%2Fbar&rut=bbb">Second Title</a>
    <a class="result__snippet" href="#">Second snippet with &#39;quotes&#39;.</a>
  </div>
</div>
`

const threeResultsHTML = `
<a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fa.example%2F1&rut=1">Result One</a>
<a class="result__snippet" href="#">Snippet one.</a>
<a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fb.example%2F2&rut=2">Result Two</a>
<a class="result__snippet" href="#">Snippet two.</a>
<a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fc.example%2F3&rut=3">Result Three</a>
<a class="result__snippet" href="#">Snippet three.</a>
`

// bolded title plus a snippet wrapped in markup, exercising tag stripping.
const boldedResultHTML = `
<a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2F&rut=x">The <b>Go</b> Programming Language</a>
<a class="result__snippet" href="#">Build <b>fast</b>, reliable software.</a>
`

// One ad-style result whose target resolves back to duckduckgo.com (must be
// skipped) followed by a genuine result.
const adThenRealHTML = `
<a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fduckduckgo.com%2Fy.js&rut=ad">Sponsored Ad</a>
<a class="result__snippet" href="#">Ad snippet text.</a>
<a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Freal.example%2Fpage&rut=ok">Real Result</a>
<a class="result__snippet" href="#">Real snippet text.</a>
`

func TestCleanHTML(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain passthrough", "hello world", "hello world"},
		{"trims whitespace", "  padded  ", "padded"},
		{"ampersand entity", "Tom &amp; Jerry", "Tom & Jerry"},
		{"numeric apostrophe", "it&#39;s here", "it's here"},
		{"lt and gt entities", "a &lt;tag&gt; b", "a <tag> b"},
		{"quot entity", "say &quot;hi&quot;", `say "hi"`},
		{"nbsp entity", "a&nbsp;b", "a b"},
		{"strips tags", "<p>Hello <b>world</b></p>", "Hello world"},
		{"tags and entities", `<a href="x">Go &amp; <b>Rust</b></a>`, "Go & Rust"},
		// NOTE: entities are unescaped in fixed order with &amp; first, so an
		// author-escaped literal like "&amp;lt;" (intended to display "&lt;")
		// is over-unescaped to "<". cleanHTML is lossy for pre-escaped text.
		{"double unescape quirk", "&amp;lt;", "<"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cleanHTML(tt.in); got != tt.want {
				t.Errorf("cleanHTML(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestExtractRealURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "protocol-relative redirect decodes uddg",
			in:   "//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpath&rut=abc",
			want: "https://example.com/path",
		},
		{
			name: "relative redirect decodes uddg",
			in:   "/l/?uddg=https%3A%2F%2Fexample.org&rut=x",
			want: "https://example.org",
		},
		{
			name: "plain https passes through unchanged",
			in:   "https://example.com/page?a=1",
			want: "https://example.com/page?a=1",
		},
		{
			name: "protocol-relative plain gains https scheme",
			in:   "//example.com/page",
			want: "https://example.com/page",
		},
		{
			name: "empty string stays empty",
			in:   "",
			want: "",
		},
		// uddg is decoded exactly once (by url.Query().Get). A target that itself
		// contains a percent-escape (here %20, encoded in the param as %2520) is
		// preserved rather than double-decoded into a literal space.
		{
			name: "percent-escaped target is preserved (decoded once)",
			in:   "//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa%2520b&rut=q",
			want: "https://example.com/a%20b",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractRealURL(tt.in); got != tt.want {
				t.Errorf("extractRealURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseResults(t *testing.T) {
	tests := []struct {
		name       string
		html       string
		maxResults int
		want       []SearchResult
	}{
		{
			name:       "empty string yields no results",
			html:       "",
			maxResults: 5,
			want:       nil,
		},
		{
			name:       "garbage yields no results",
			html:       "<<<>>> not really html &&& ???",
			maxResults: 5,
			want:       nil,
		},
		{
			name:       "markup without result classes yields no results",
			html:       `<html><body><a href="https://x.example">plain link</a></body></html>`,
			maxResults: 5,
			want:       nil,
		},
		{
			name:       "two results extract title, url, snippet",
			html:       twoResultsHTML,
			maxResults: 5,
			want: []SearchResult{
				{Title: "First & Title", URL: "https://example.com/foo", Snippet: "This is the first snippet."},
				{Title: "Second Title", URL: "https://example.org/bar", Snippet: "Second snippet with 'quotes'."},
			},
		},
		{
			name:       "bolded title and snippet markup stripped",
			html:       boldedResultHTML,
			maxResults: 5,
			want: []SearchResult{
				{Title: "The Go Programming Language", URL: "https://go.dev/", Snippet: "Build fast, reliable software."},
			},
		},
		{
			name:       "duckduckgo target is skipped",
			html:       `<a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fduckduckgo.com%2Fabout&rut=z">DDG About</a>`,
			maxResults: 5,
			want:       nil,
		},
		// Snippets are paired by document position, so skipping the ad result
		// (title index 0) does not shift its snippet onto the genuine result —
		// the real result keeps its own snippet.
		{
			name:       "skipped result keeps following snippet aligned",
			html:       adThenRealHTML,
			maxResults: 5,
			want: []SearchResult{
				{Title: "Real Result", URL: "https://real.example/page", Snippet: "Real snippet text."},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseResults(tt.html, tt.maxResults)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseResults() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParseResultsMaxResults(t *testing.T) {
	tests := []struct {
		name       string
		maxResults int
		wantLen    int
	}{
		{"zero caps to nothing", 0, 0},
		{"one", 1, 1},
		{"two", 2, 2},
		{"exact", 3, 3},
		{"more than available", 10, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseResults(threeResultsHTML, tt.maxResults)
			if len(got) != tt.wantLen {
				t.Errorf("parseResults(maxResults=%d) len = %d, want %d", tt.maxResults, len(got), tt.wantLen)
			}
		})
	}
}

func TestSkillSchema(t *testing.T) {
	schema := NewWebSearchSkill().SkillSchema()

	t.Run("name is web_search", func(t *testing.T) {
		if got := schema["name"]; got != "web_search" {
			t.Errorf("name = %v, want web_search", got)
		}
	})

	t.Run("description is non-empty string", func(t *testing.T) {
		desc, ok := schema["description"].(string)
		if !ok || desc == "" {
			t.Errorf("description = %v, want non-empty string", schema["description"])
		}
	})

	params, ok := schema["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("parameters is %T, want map[string]any", schema["parameters"])
	}

	t.Run("declares query and max_results parameters", func(t *testing.T) {
		if _, ok := params["query"]; !ok {
			t.Errorf("parameters missing query")
		}
		if _, ok := params["max_results"]; !ok {
			t.Errorf("parameters missing max_results")
		}
	})

	t.Run("max_results documents a cap of 10", func(t *testing.T) {
		mr, ok := params["max_results"].(map[string]any)
		if !ok {
			t.Fatalf("max_results is %T, want map[string]any", params["max_results"])
		}
		desc, _ := mr["description"].(string)
		if !strings.Contains(desc, "10") {
			t.Errorf("max_results description = %q, want it to mention the cap of 10", desc)
		}
	})
}

func TestExecuteRequiresQuery(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
	}{
		{"missing query key", map[string]any{}},
		{"empty query", map[string]any{"query": ""}},
		{"nil query", map[string]any{"query": nil}},
	}
	skill := NewWebSearchSkill()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, ok := skill.Execute(tt.args).(map[string]any)
			if !ok {
				t.Fatalf("Execute returned %T, want map[string]any", skill.Execute(tt.args))
			}
			if res["error"] != "query is required" {
				t.Errorf("error = %v, want %q", res["error"], "query is required")
			}
		})
	}
}

func TestAsString(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"nil", nil, ""},
		{"string passthrough", "hi", "hi"},
		{"int", 42, "42"},
		{"bool", true, "true"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := asString(tt.in); got != tt.want {
				t.Errorf("asString(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestAsInt(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want int
	}{
		{"int", 5, 5},
		{"int64", int64(7), 7},
		{"float64 truncates", 3.9, 3},
		{"numeric string", "12", 12},
		{"non-numeric string", "abc", 0},
		{"nil", nil, 0},
		{"unsupported type", true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := asInt(tt.in); got != tt.want {
				t.Errorf("asInt(%v) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}
