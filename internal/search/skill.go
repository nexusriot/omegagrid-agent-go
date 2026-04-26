package search

import (
	"fmt"
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

func (s *WebSearchSkill) Execute(args map[string]any) any {
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

	results, err := s.search(query, maxResults)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}

	return map[string]any{
		"query":   query,
		"count":   len(results),
		"results": results,
	}
}

func (s *WebSearchSkill) search(query string, maxResults int) ([]SearchResult, error) {
	reqURL := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query)

	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:125.0) Gecko/20100101 Firefox/125.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
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

func parseResults(html string, maxResults int) []SearchResult {
	titleMatches := titleRe.FindAllStringSubmatch(html, -1)
	snippetMatches := snippetRe.FindAllStringSubmatch(html, -1)

	var results []SearchResult
	si := 0 // snippet index (may lag if some titles have no snippet)

	for _, m := range titleMatches {
		if len(results) >= maxResults {
			break
		}

		href := m[1]
		realURL := extractRealURL(href)
		if realURL == "" || strings.Contains(realURL, "duckduckgo.com") {
			continue
		}

		title := cleanHTML(m[2])

		snippet := ""
		if si < len(snippetMatches) {
			snippet = cleanHTML(snippetMatches[si][1])
			si++
		}

		results = append(results, SearchResult{
			Title:   title,
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

	// DDG redirect: /l/?uddg=<encoded-url>
	if uddg := u.Query().Get("uddg"); uddg != "" {
		decoded, err := url.QueryUnescape(uddg)
		if err == nil {
			return decoded
		}
		return uddg
	}

	return href
}

func cleanHTML(s string) string {
	s = tagRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
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
