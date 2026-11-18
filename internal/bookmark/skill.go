// Package bookmark is the native bookmark skill: it saves a web page into
// vector memory (title, description, the agent's summary, tags) so it can be
// found again by meaning ("that article about Go generics I saved") through
// the bookmark skill itself or through ordinary vector_search.
package bookmark

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"

	"github.com/nexusriot/omegagrid-agent-go/internal/memory"
	"github.com/nexusriot/omegagrid-agent-go/internal/skills/netguard"
)

// metaType tags bookmark memories.
const metaType = "bookmark"

// Store is the subset of memory.Client the skill uses.
type Store interface {
	AddMemoryOpts(text string, meta map[string]any, opts memory.AddOptions) (*memory.AddResult, error)
	SearchMemoryIn(query string, k int, namespaces []string) (*memory.SearchResult, error)
	ListMemoriesFilter(f memory.ListFilter, limit, offset int) ([]memory.MemoryHit, int, error)
	DeleteMemory(id string) (bool, error)
}

// Skill saves and finds bookmarks.
type Skill struct {
	Store Store
	// Fetch retrieves a page (tests replace it); nil uses the SSRF-guarded client.
	Fetch   func(ctx context.Context, u string) (title, description, excerpt string, err error)
	Timeout time.Duration
}

func (s *Skill) SkillSchema() map[string]any {
	return map[string]any{
		"name": "bookmark",
		"description": "Save web pages to remember and find them later by meaning. add fetches the page title and description " +
			"and stores them with your summary and tags in long-term memory; search finds saved pages by topic; list and delete manage them.",
		"parameters": map[string]any{
			"action":  map[string]any{"type": "string", "description": "add | search | list | delete", "required": true},
			"url":     map[string]any{"type": "string", "description": "Page URL (add/delete)", "required": false},
			"summary": map[string]any{"type": "string", "description": "add: your 1-3 sentence summary of the page (read it first with web_scrape/html_to_markdown for a good one)", "required": false},
			"tags":    map[string]any{"type": "array", "description": "add: tags; list: only bookmarks with this tag", "required": false},
			"note":    map[string]any{"type": "string", "description": "add: the user's own note (why it matters)", "required": false},
			"query":   map[string]any{"type": "string", "description": "search: what the page was about", "required": false},
			"limit":   map[string]any{"type": "number", "description": "search/list: max results (default 10)", "required": false},
		},
	}
}

func (s *Skill) Execute(ctx context.Context, args map[string]any) any {
	action := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	limit := asInt(args["limit"], 10)
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	switch action {
	case "add":
		return s.add(ctx, args)
	case "search":
		q := strings.TrimSpace(asString(args["query"]))
		if q == "" {
			return map[string]any{"error": "query is required"}
		}
		return s.search(q, limit)
	case "list":
		all, err := s.all()
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		tags := asStrings(args["tags"])
		var out []map[string]any
		for _, h := range all {
			if len(tags) > 0 && !hasTag(h, tags[0]) {
				continue
			}
			out = append(out, view(h))
			if len(out) == limit {
				break
			}
		}
		if out == nil {
			out = []map[string]any{}
		}
		return map[string]any{"count": len(out), "total": len(all), "bookmarks": out}
	case "delete":
		u, err := normURL(asString(args["url"]))
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		all, err := s.all()
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		n := 0
		for _, h := range all {
			if meta(h, "url") == u {
				if ok, _ := s.Store.DeleteMemory(h.ID); ok {
					n++
				}
			}
		}
		if n == 0 {
			return map[string]any{"error": "no bookmark for " + u}
		}
		return map[string]any{"deleted": n, "url": u}
	default:
		return map[string]any{"error": "action must be add, search, list or delete"}
	}
}

func (s *Skill) add(ctx context.Context, args map[string]any) any {
	u, err := normURL(asString(args["url"]))
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	fetch := s.Fetch
	if fetch == nil {
		fetch = s.fetchPage
	}
	title, desc, excerpt, ferr := fetch(ctx, u)
	if title == "" {
		title = u
	}
	summary := strings.TrimSpace(asString(args["summary"]))
	note := strings.TrimSpace(asString(args["note"]))
	tags := asStrings(args["tags"])
	for i, t := range tags {
		tags[i] = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(t), "#"))
	}

	// One memory per URL: re-adding replaces the old entry.
	replaced := 0
	if all, err := s.all(); err == nil {
		for _, h := range all {
			if meta(h, "url") == u {
				if ok, _ := s.Store.DeleteMemory(h.ID); ok {
					replaced++
				}
			}
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Bookmark: %s\nURL: %s\n", title, u)
	about := summary
	if about == "" {
		about = desc
	}
	if about == "" {
		about = excerpt
	}
	if about != "" {
		fmt.Fprintf(&b, "About: %s\n", clip(about, 1200))
	}
	if note != "" {
		fmt.Fprintf(&b, "Note: %s\n", clip(note, 500))
	}
	if len(tags) > 0 {
		fmt.Fprintf(&b, "Tags: %s\n", strings.Join(tags, ", "))
	}
	md := map[string]any{"type": metaType, "url": u, "title": clip(title, 300),
		"saved_at": time.Now().UTC().Format(time.RFC3339)}
	if len(tags) > 0 {
		md["tags"] = "," + strings.Join(tags, ",") + ","
	}
	res, err := s.Store.AddMemoryOpts(strings.TrimSpace(b.String()), md, memory.AddOptions{TTL: -1})
	if err != nil {
		return map[string]any{"error": "store bookmark: " + err.Error()}
	}
	out := map[string]any{"saved": !res.Skipped, "url": u, "title": title, "memory_id": res.MemoryID}
	if res.Skipped {
		out["reason"] = res.Reason
	}
	if replaced > 0 {
		out["replaced"] = replaced
	}
	if ferr != nil {
		out["fetch_warning"] = "could not read the page (" + ferr.Error() + "); saved with the details given"
	}
	if summary == "" && desc == "" {
		out["hint"] = "No summary was stored; re-add with summary=... for better search."
	}
	return out
}

func (s *Skill) search(q string, limit int) any {
	// Over-fetch: other memories compete for the top slots.
	res, err := s.Store.SearchMemoryIn(q, limit*4, []string{""})
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	var out []map[string]any
	for _, h := range res.Hits {
		if meta(h, "type") != metaType {
			continue
		}
		v := view(h)
		v["distance"] = h.Distance
		out = append(out, v)
		if len(out) == limit {
			break
		}
	}
	if out == nil {
		out = []map[string]any{}
	}
	return map[string]any{"query": q, "count": len(out), "bookmarks": out}
}

// all returns every bookmark memory, newest first.
func (s *Skill) all() ([]memory.MemoryHit, error) {
	ns := ""
	hits, _, err := s.Store.ListMemoriesFilter(memory.ListFilter{Namespace: &ns}, 0, 0)
	if err != nil {
		return nil, err
	}
	var out []memory.MemoryHit
	for _, h := range hits {
		if meta(h, "type") == metaType {
			out = append(out, h)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return meta(out[i], "saved_at") > meta(out[j], "saved_at") })
	return out, nil
}

func view(h memory.MemoryHit) map[string]any {
	v := map[string]any{"url": meta(h, "url"), "title": meta(h, "title"), "saved_at": meta(h, "saved_at"), "text": h.Text}
	if t := strings.Trim(meta(h, "tags"), ","); t != "" {
		v["tags"] = strings.Split(t, ",")
	}
	return v
}

func hasTag(h memory.MemoryHit, tag string) bool {
	tag = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(tag), "#"))
	return strings.Contains(meta(h, "tags"), ","+tag+",")
}

func meta(h memory.MemoryHit, k string) string {
	if v, ok := h.Metadata[k]; ok {
		return fmt.Sprint(v)
	}
	return ""
}

func normURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("url is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("url must be an http(s) URL")
	}
	u.Fragment = ""
	u.Host = strings.ToLower(u.Host)
	return u.String(), nil
}

func (s *Skill) fetchPage(ctx context.Context, u string) (title, desc, excerpt string, err error) {
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; OmegaGridAgent/1.0)")
	resp, err := netguard.Client(timeout).Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", "", "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(ct, "html") {
		return "", "", "", nil
	}
	doc, err := html.Parse(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", "", "", err
	}
	var text strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "nav", "footer", "header", "noscript":
				return
			case "title":
				if title == "" && n.FirstChild != nil {
					title = strings.TrimSpace(n.FirstChild.Data)
				}
			case "meta":
				var name, content string
				for _, a := range n.Attr {
					switch strings.ToLower(a.Key) {
					case "name", "property":
						name = strings.ToLower(a.Val)
					case "content":
						content = a.Val
					}
				}
				switch name {
				case "og:title":
					if content != "" {
						title = strings.TrimSpace(content)
					}
				case "description", "og:description":
					if desc == "" {
						desc = strings.TrimSpace(content)
					}
				}
			}
		}
		if n.Type == html.TextNode && text.Len() < 1500 {
			if t := strings.TrimSpace(n.Data); t != "" {
				text.WriteString(t)
				text.WriteString(" ")
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return clip(title, 300), clip(desc, 600), clip(strings.TrimSpace(text.String()), 600), nil
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func asString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

func asInt(v any, def int) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case string:
		var n int
		if _, err := fmt.Sscan(x, &n); err == nil {
			return n
		}
	}
	return def
}

func asStrings(v any) []string {
	var raw []string
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			if s, ok := e.(string); ok {
				raw = append(raw, s)
			}
		}
	case []string:
		raw = x
	case string:
		raw = strings.Split(x, ",")
	}
	out := raw[:0]
	for _, s := range raw {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
