package search

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// SearchResult is one web search hit returned to the agent.
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// maxResponseBytes caps how much of the results page is read.
const maxResponseBytes = 2 << 20

// WebSearchSkill scrapes DuckDuckGo HTML — no API key required.
type WebSearchSkill struct {
	client *http.Client
}

func NewWebSearchSkill() *WebSearchSkill {
	return &WebSearchSkill{
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (s *WebSearchSkill) SkillSchema() map[string]any {
	return map[string]any{
		"name":        "web_search",
		"description": "Search the internet via DuckDuckGo. Returns titles, URLs, and text snippets for the top results. Use when you need current information or facts from the web.",
		"parameters": map[string]any{
			"query":       map[string]any{"type": "string", "description": "Search query", "required": true},
			"max_results": map[string]any{"type": "integer", "description": "Number of results (default 5, max 10)", "required": false},
		},
	}
}

// Execute is ExecuteContext without cancellation.
func (s *WebSearchSkill) Execute(args map[string]any) any {
	return s.ExecuteContext(context.Background(), args)
}

// ExecuteContext runs a search; cancelling ctx aborts the request and the
// retry backoff.
func (s *WebSearchSkill) ExecuteContext(ctx context.Context, args map[string]any) any {
	query := asString(args["query"])
	if query == "" {
		return map[string]any{"error": "query is required"}
	}

	maxResults := 5
	if v, ok := args["max_results"]; ok && v != nil {
		if n := asInt(v); n > 0 {
			maxResults = n
		}
	}
	if maxResults > 10 {
		maxResults = 10
	}

	results, err := s.search(ctx, query, maxResults)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}

	return map[string]any{
		"query":   query,
		"count":   len(results),
		"results": results,
	}
}

func (s *WebSearchSkill) search(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	reqURL := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query)

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:125.0) Gecko/20100101 Firefox/125.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	// Retry transient transport errors (TLS handshake timeouts, connection
	// resets) — GET is idempotent and DDG occasionally drops connections.
	var resp *http.Response
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(attempt) * time.Second):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		resp, err = s.client.Do(req)
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("search request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search returned status %d", resp.StatusCode)
	}

	// Bounded: results sit near the top of the page, and an unbounded read
	// let a misbehaving (or spoofed) upstream exhaust memory.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	return parseResults(string(body), maxResults), nil
}

// DDG HTML structure (html.duckduckgo.com):
//
//	<a class="result__a" href="/l/?uddg=https%3A%2F%2Fexample.com&rut=...">Title</a>
//	<a class="result__snippet" href="...">Snippet text</a>
var (
	titleRe   = regexp.MustCompile(`<a[^>]+class="result__a"[^>]+href="([^"]+)"[^>]*>([\s\S]*?)</a>`)
	snippetRe = regexp.MustCompile(`<a[^>]+class="result__snippet"[^>]*>([\s\S]*?)</a>`)
	tagRe     = regexp.MustCompile(`<[^>]+>`)
)

func parseResults(page string, maxResults int) []SearchResult {
	titleMatches := titleRe.FindAllStringSubmatchIndex(page, -1)
	snippetMatches := snippetRe.FindAllStringSubmatchIndex(page, -1)

	var results []SearchResult

	for i, m := range titleMatches {
		if len(results) >= maxResults {
			break
		}

		href := page[m[2]:m[3]]
		realURL := extractRealURL(href)
		if realURL == "" || strings.Contains(realURL, "duckduckgo.com") {
			continue
		}

		// A result's snippet is the first one between its title and the next
		// title. Pairing by match index shifted every later snippet onto the
		// wrong result as soon as one result had no snippet.
		end := len(page)
		if i+1 < len(titleMatches) {
			end = titleMatches[i+1][0]
		}
		snippet := ""
		for _, sm := range snippetMatches {
			if sm[0] >= m[1] && sm[0] < end {
				snippet = cleanHTML(page[sm[2]:sm[3]])
				break
			}
		}

		results = append(results, SearchResult{
			Title:   cleanHTML(page[m[4]:m[5]]),
			URL:     realURL,
			Snippet: snippet,
		})
	}

	return results
}

// extractRealURL decodes DDG redirect URLs (/l/?uddg=...) to the real target.
func extractRealURL(href string) string {
	// Handle protocol-relative URLs
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}

	u, err := url.Parse(href)
	if err != nil {
		return href
	}

	// DDG redirect: /l/?uddg=<url-encoded target>. url.Query() already unescapes
	// the value exactly once; decoding it again would corrupt any target that
	// legitimately contains a percent-escape (e.g. %20 in the path).
	if uddg := u.Query().Get("uddg"); uddg != "" {
		return uddg
	}

	return href
}

// cleanHTML strips tags and decodes entities once. The hand-written
// replacement chain decoded "&amp;lt;" twice (to "<") and left "&#x27;" —
// which DDG emits for every apostrophe — undecoded.
func cleanHTML(s string) string {
	s = tagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "\u00a0", " ")
	return strings.TrimSpace(s)
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func asInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case string:
		var n int
		fmt.Sscanf(x, "%d", &n)
		return n
	}
	return 0
}
