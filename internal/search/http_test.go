package search

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// roundTripFunc lets a test stand in for the DuckDuckGo endpoint without
// changing the skill's hardcoded URL.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func respond(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

const ddgHTML = `
<div class="result">
  <a class="result__a" href="/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc%2F&amp;rut=x">The <b>Go</b> docs</a>
  <a class="result__snippet">Official <b>Go</b> documentation &amp; tutorials</a>
</div>
<div class="result">
  <a class="result__a" href="/l/?uddg=https%3A%2F%2Fpkg.go.dev%2F">pkg.go.dev</a>
  <a class="result__snippet">Package index</a>
</div>`

func TestSearchParsesResults(t *testing.T) {
	var gotURL, gotUA string
	s := NewWebSearchSkill()
	s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		gotUA = r.Header.Get("User-Agent")
		return respond(http.StatusOK, ddgHTML), nil
	})

	res := s.Execute(map[string]any{"query": "go docs"}).(map[string]any)
	if res["error"] != nil {
		t.Fatalf("search failed: %v", res["error"])
	}
	if res["count"] != 2 {
		t.Fatalf("count = %v, want 2", res["count"])
	}

	results, _ := res["results"].([]SearchResult)
	if len(results) != 2 {
		t.Fatalf("results = %v", res["results"])
	}
	// The redirect wrapper must be decoded to the real target...
	if results[0].URL != "https://go.dev/doc/" {
		t.Fatalf("url = %q, want the decoded uddg target", results[0].URL)
	}
	// ...and HTML tags and entities stripped from the text.
	if results[0].Title != "The Go docs" {
		t.Fatalf("title = %q", results[0].Title)
	}
	if results[0].Snippet != "Official Go documentation & tutorials" {
		t.Fatalf("snippet = %q", results[0].Snippet)
	}
	if results[1].URL != "https://pkg.go.dev/" {
		t.Fatalf("url = %q", results[1].URL)
	}

	if !strings.Contains(gotURL, "q=go+docs") {
		t.Fatalf("request URL = %q, want the query escaped into it", gotURL)
	}
	// DDG serves a different page to unknown agents, so a browser UA matters.
	if !strings.Contains(gotUA, "Mozilla") {
		t.Fatalf("User-Agent = %q", gotUA)
	}
}

func TestSearchRespectsMaxResults(t *testing.T) {
	s := NewWebSearchSkill()
	s.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return respond(http.StatusOK, ddgHTML), nil
	})

	res := s.Execute(map[string]any{"query": "q", "max_results": 1}).(map[string]any)
	if res["count"] != 1 {
		t.Fatalf("count = %v, want 1", res["count"])
	}
	// Models often send the number as a string.
	res = s.Execute(map[string]any{"query": "q", "max_results": "1"}).(map[string]any)
	if res["count"] != 1 {
		t.Fatalf("count = %v with a string max_results, want 1", res["count"])
	}
	// The cap protects the context window.
	res = s.Execute(map[string]any{"query": "q", "max_results": 999}).(map[string]any)
	if res["count"] != 2 { // only two in the fixture, but the cap is 10
		t.Fatalf("count = %v", res["count"])
	}
}

func TestSearchNonOKStatus(t *testing.T) {
	s := NewWebSearchSkill()
	s.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return respond(http.StatusServiceUnavailable, "rate limited"), nil
	})

	res := s.Execute(map[string]any{"query": "q"}).(map[string]any)
	msg, _ := res["error"].(string)
	if !strings.Contains(msg, "503") {
		t.Fatalf("error = %v, want it to name the status", res["error"])
	}
}

// DDG occasionally drops connections; GET is idempotent, so a transport error
// is retried rather than surfaced to the model.
func TestSearchRetriesTransportError(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	s := NewWebSearchSkill()
	s.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()
		if n == 1 {
			return nil, errTransport{}
		}
		return respond(http.StatusOK, ddgHTML), nil
	})

	res := s.Execute(map[string]any{"query": "q"}).(map[string]any)
	if res["error"] != nil {
		t.Fatalf("search failed despite a retry being available: %v", res["error"])
	}
	if attempts != 2 {
		t.Fatalf("made %d attempts, want 2", attempts)
	}
}

type errTransport struct{}

func (errTransport) Error() string { return "connection reset by peer" }

func TestSearchEmptyResultPage(t *testing.T) {
	s := NewWebSearchSkill()
	s.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return respond(http.StatusOK, "<html><body>no results</body></html>"), nil
	})

	res := s.Execute(map[string]any{"query": "q"}).(map[string]any)
	if res["error"] != nil {
		t.Fatalf("an empty result page is not an error: %v", res["error"])
	}
	if res["count"] != 0 {
		t.Fatalf("count = %v, want 0", res["count"])
	}
}

// DDG-internal links (ads, "more results") must be skipped without shifting the
// snippets onto the wrong results.
func TestSearchSkipsInternalLinks(t *testing.T) {
	html := `
<a class="result__a" href="https://duckduckgo.com/y.js?ad=1">Sponsored</a>
<a class="result__snippet">an advert</a>
<a class="result__a" href="/l/?uddg=https%3A%2F%2Freal.example.com%2F">Real result</a>
<a class="result__snippet">the real snippet</a>`

	s := NewWebSearchSkill()
	s.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return respond(http.StatusOK, html), nil
	})

	res := s.Execute(map[string]any{"query": "q"}).(map[string]any)
	results, _ := res["results"].([]SearchResult)
	if len(results) != 1 {
		t.Fatalf("results = %v, want just the real one", results)
	}
	if results[0].URL != "https://real.example.com/" {
		t.Fatalf("url = %q", results[0].URL)
	}
	if results[0].Snippet != "the real snippet" {
		t.Fatalf("snippet = %q, want the one paired with this result", results[0].Snippet)
	}
}
