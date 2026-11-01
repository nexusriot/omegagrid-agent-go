package builtin

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

func WikipediaSchema() Skill {
	return Skill{Name: "wikipedia", Description: "Look things up on Wikipedia: action=summary gives an article's lead summary, action=search finds articles.",
		Parameters: map[string]Param{
			"action": {Type: "string", Description: "summary (default) or search", Required: false},
			"query":  {Type: "string", Description: "Article title (summary) or search terms (search)", Required: true},
			"lang":   {Type: "string", Description: "Wikipedia language code (default en)", Required: false},
			"limit":  {Type: "number", Description: "search: max results (default 5, max 20)", Required: false},
		}}
}

// wikipediaBase returns a language edition's root, a seam for tests.
var wikipediaBase = func(lang string) string { return "https://" + lang + ".wikipedia.org" }

var langRE = regexp.MustCompile(`^[a-z]{2,3}(-[a-z]{2,8})?$`)

// Wikipedia is WikipediaContext without cancellation.
func Wikipedia(timeoutSec float64) Executor { return withoutContext(WikipediaContext(timeoutSec)) }

func WikipediaContext(timeoutSec float64) ContextExecutor {
	cl := httpClient(timeoutSec)
	return func(ctx context.Context, args map[string]any) (any, error) {
		q := strings.TrimSpace(str(args, "query"))
		if q == "" {
			return map[string]any{"error": "query is required"}, nil
		}
		lang := strings.ToLower(strings.TrimSpace(str(args, "lang")))
		if lang == "" {
			lang = "en"
		}
		if !langRE.MatchString(lang) {
			return map[string]any{"error": fmt.Sprintf("invalid language code %q", lang)}, nil
		}
		base := wikipediaBase(lang)
		hdr := http.Header{"User-Agent": {"OmegaGridAgent/1.0 (https://github.com/nexusriot/omegagrid-agent-go)"}, "Accept": {"application/json"}}
		switch strings.ToLower(strings.TrimSpace(str(args, "action"))) {
		case "", "summary":
			title := strings.ReplaceAll(q, " ", "_")
			resp, err := getWithRetry(ctx, cl, base+"/api/rest_v1/page/summary/"+url.PathEscape(title)+"?redirect=true", hdr)
			if err != nil {
				return map[string]any{"error": err.Error()}, nil
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound {
				return map[string]any{"query": q, "found": false, "hint": "no article with that title; try action=search"}, nil
			}
			var s struct {
				Title       string `json:"title"`
				Type        string `json:"type"`
				Description string `json:"description"`
				Extract     string `json:"extract"`
				ContentURLs struct {
					Desktop struct {
						Page string `json:"page"`
					} `json:"desktop"`
				} `json:"content_urls"`
				Timestamp string `json:"timestamp"`
			}
			if resp.StatusCode >= 400 || json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&s) != nil {
				return map[string]any{"query": q, "error": fmt.Sprintf("Wikipedia answered HTTP %d", resp.StatusCode)}, nil
			}
			out := map[string]any{"query": q, "found": true, "title": s.Title, "description": s.Description,
				"summary": s.Extract, "url": s.ContentURLs.Desktop.Page, "last_edited": s.Timestamp}
			if s.Type == "disambiguation" {
				out["disambiguation"] = true
				out["hint"] = "this title is ambiguous; try action=search for the specific article"
			}
			return out, nil
		case "search":
			limit := min(max(intOr(args, "limit", 5), 1), 20)
			target := fmt.Sprintf("%s/w/rest.php/v1/search/page?q=%s&limit=%d", base, url.QueryEscape(q), limit)
			resp, err := getWithRetry(ctx, cl, target, hdr)
			if err != nil {
				return map[string]any{"error": err.Error()}, nil
			}
			defer resp.Body.Close()
			var r struct {
				Pages []struct {
					Key         string `json:"key"`
					Title       string `json:"title"`
					Description string `json:"description"`
					Excerpt     string `json:"excerpt"`
				} `json:"pages"`
			}
			if resp.StatusCode >= 400 || json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&r) != nil {
				return map[string]any{"query": q, "error": fmt.Sprintf("Wikipedia answered HTTP %d", resp.StatusCode)}, nil
			}
			results := make([]map[string]any, 0, len(r.Pages))
			for _, p := range r.Pages {
				results = append(results, map[string]any{
					"title": p.Title, "description": p.Description,
					"snippet": strings.TrimSpace(collapseWhitespace(extractText(p.Excerpt))),
					"url":     base + "/wiki/" + url.PathEscape(p.Key),
				})
			}
			return map[string]any{"query": q, "count": len(results), "results": results}, nil
		}
		return map[string]any{"error": "action must be summary or search"}, nil
	}
}

func CurrencyConvertSchema() Skill {
	return Skill{Name: "currency_convert", Description: "Convert between currencies with the European Central Bank's daily reference rates " +
		"(about 30 major currencies, updated each working day around 16:00 CET; not for trading).",
		Parameters: map[string]Param{
			"amount": {Type: "number", Description: "Amount to convert (default 1)", Required: false},
			"from":   {Type: "string", Description: "ISO code, e.g. USD", Required: true},
			"to":     {Type: "array", Description: "Target ISO codes, e.g. [\"EUR\",\"JPY\"] (default EUR)", Required: false},
		}}
}

// ecbURL is the daily reference-rate feed, a seam for tests.
var ecbURL = "https://www.ecb.europa.eu/stats/eurofxref/eurofxref-daily.xml"

// ecbCache keeps the last rate table for an hour: it changes once a day.
var ecbCache struct {
	sync.Mutex
	rates   map[string]float64
	date    string
	fetched time.Time
}

// CurrencyConvert is CurrencyConvertContext without cancellation.
func CurrencyConvert(timeoutSec float64) Executor {
	return withoutContext(CurrencyConvertContext(timeoutSec))
}

func CurrencyConvertContext(timeoutSec float64) ContextExecutor {
	cl := httpClient(timeoutSec)
	return func(ctx context.Context, args map[string]any) (any, error) {
		amount, ok := numArg(args["amount"])
		if _, given := args["amount"]; !given {
			amount, ok = 1, true
		}
		if !ok || math.IsNaN(amount) || math.IsInf(amount, 0) {
			return map[string]any{"error": "amount must be a number"}, nil
		}
		from := strings.ToUpper(strings.TrimSpace(str(args, "from")))
		targets := strList(args, "to")
		if len(targets) == 0 {
			targets = []string{"EUR"}
		}
		rates, date, err := ecbRates(ctx, cl)
		if err != nil {
			return map[string]any{"error": "fetch ECB rates: " + err.Error()}, nil
		}
		fromRate, ok := rates[from]
		if !ok {
			return map[string]any{"error": fmt.Sprintf("unknown or unsupported currency %q", from), "supported": sortedRateCodes(rates)}, nil
		}
		var results []map[string]any
		for _, t := range targets {
			code := strings.ToUpper(strings.TrimSpace(t))
			r, ok := rates[code]
			if !ok {
				return map[string]any{"error": fmt.Sprintf("unknown or unsupported currency %q", code), "supported": sortedRateCodes(rates)}, nil
			}
			rate := r / fromRate
			results = append(results, map[string]any{"currency": code, "rate": round6(rate), "amount": math.Round(amount*rate*100) / 100})
		}
		return map[string]any{"amount": amount, "from": from, "results": results, "rates_date": date, "source": "ECB euro foreign exchange reference rates"}, nil
	}
}

func round6(f float64) float64 { return math.Round(f*1e6) / 1e6 }

func sortedRateCodes(rates map[string]float64) []string {
	out := make([]string, 0, len(rates))
	for k := range rates {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func ecbRates(ctx context.Context, cl *http.Client) (map[string]float64, string, error) {
	ecbCache.Lock()
	defer ecbCache.Unlock()
	if ecbCache.rates != nil && time.Since(ecbCache.fetched) < time.Hour {
		return ecbCache.rates, ecbCache.date, nil
	}
	resp, err := getWithRetry(ctx, cl, ecbURL, http.Header{"User-Agent": {"OmegaGridAgent/1.0"}})
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var doc struct {
		Cube struct {
			Cube struct {
				Time  string `xml:"time,attr"`
				Rates []struct {
					Currency string  `xml:"currency,attr"`
					Rate     float64 `xml:"rate,attr"`
				} `xml:"Cube"`
			} `xml:"Cube"`
		} `xml:"Cube"`
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&doc); err != nil {
		return nil, "", fmt.Errorf("decode: %w", err)
	}
	rates := map[string]float64{"EUR": 1}
	for _, r := range doc.Cube.Cube.Rates {
		if r.Rate > 0 {
			rates[strings.ToUpper(r.Currency)] = r.Rate
		}
	}
	if len(rates) < 2 {
		return nil, "", fmt.Errorf("no rates in the ECB feed")
	}
	ecbCache.rates, ecbCache.date, ecbCache.fetched = rates, doc.Cube.Cube.Time, time.Now()
	return rates, ecbCache.date, nil
}

func GithubSchema() Skill {
	return Skill{Name: "github", Description: "Public GitHub data: repo (stars, forks, language, license, last push), releases, issues, pulls, " +
		"or search (repositories).",
		Parameters: map[string]Param{
			"action": {Type: "string", Description: "repo | releases | issues | pulls | search", Required: true},
			"repo":   {Type: "string", Description: "owner/name (for repo, releases, issues, pulls)", Required: false},
			"query":  {Type: "string", Description: "search: GitHub search query, e.g. 'language:go stars:>1000 agent'", Required: false},
			"state":  {Type: "string", Description: "issues/pulls: open (default), closed or all", Required: false},
			"limit":  {Type: "number", Description: "Max items (default 10, max 50)", Required: false},
		}}
}

// githubAPI is the REST API root, a seam for tests.
var githubAPI = "https://api.github.com"

var repoRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})/[A-Za-z0-9._-]{1,100}$`)

// Github is GithubContext without cancellation.
func Github(token string, timeoutSec float64) Executor {
	return withoutContext(GithubContext(token, timeoutSec))
}

func GithubContext(token string, timeoutSec float64) ContextExecutor {
	cl := httpClient(timeoutSec)
	return func(ctx context.Context, args map[string]any) (any, error) {
		action := strings.ToLower(strings.TrimSpace(str(args, "action")))
		repo := strings.Trim(strings.TrimSpace(str(args, "repo")), "/")
		repo = strings.TrimPrefix(strings.TrimPrefix(repo, "https://github.com/"), "github.com/")
		limit := min(max(intOr(args, "limit", 10), 1), 50)
		needRepo := action == "repo" || action == "releases" || action == "issues" || action == "pulls"
		if needRepo && !repoRE.MatchString(repo) {
			return map[string]any{"error": fmt.Sprintf("repo must be owner/name, got %q", repo)}, nil
		}
		state := strings.ToLower(strings.TrimSpace(str(args, "state")))
		if state == "" {
			state = "open"
		}
		if state != "open" && state != "closed" && state != "all" {
			return map[string]any{"error": "state must be open, closed or all"}, nil
		}
		var path string
		switch action {
		case "repo":
			path = "/repos/" + repo
		case "releases":
			path = fmt.Sprintf("/repos/%s/releases?per_page=%d", repo, limit)
		case "issues":
			// The issues endpoint returns pull requests too; ask for more and
			// filter them out below.
			path = fmt.Sprintf("/repos/%s/issues?state=%s&per_page=%d", repo, state, min(limit*2, 100))
		case "pulls":
			path = fmt.Sprintf("/repos/%s/pulls?state=%s&per_page=%d", repo, state, limit)
		case "search":
			q := strings.TrimSpace(str(args, "query"))
			if q == "" {
				return map[string]any{"error": "query is required for search"}, nil
			}
			path = fmt.Sprintf("/search/repositories?q=%s&per_page=%d", url.QueryEscape(q), limit)
		default:
			return map[string]any{"error": "action must be repo, releases, issues, pulls or search"}, nil
		}
		hdr := http.Header{"Accept": {"application/vnd.github+json"}, "User-Agent": {"OmegaGridAgent/1.0"}, "X-GitHub-Api-Version": {"2022-11-28"}}
		if token != "" {
			hdr.Set("Authorization", "Bearer "+token)
		}
		resp, err := getWithRetry(ctx, cl, githubAPI+path, hdr)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		defer resp.Body.Close()
		var body any
		_ = json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&body)
		if resp.StatusCode >= 400 {
			msg, _ := mapOf(body)["message"].(string)
			out := map[string]any{"error": fmt.Sprintf("GitHub HTTP %d: %s", resp.StatusCode, nonEmpty(msg, resp.Status))}
			if resp.Header.Get("X-RateLimit-Remaining") == "0" {
				out["hint"] = "rate limited; set SKILL_GITHUB_TOKEN for a higher limit"
			}
			return out, nil
		}
		switch action {
		case "repo":
			m := mapOf(body)
			return pick(m, "full_name", "description", "html_url", "homepage", "language", "stargazers_count", "forks_count",
				"open_issues_count", "watchers_count", "default_branch", "archived", "fork", "created_at", "pushed_at", "topics",
				"license.spdx_id", "owner.login"), nil
		case "releases":
			items := listOf(body, func(m map[string]any) map[string]any {
				return pick(m, "tag_name", "name", "published_at", "prerelease", "draft", "html_url")
			}, limit)
			return map[string]any{"repo": repo, "count": len(items), "releases": items}, nil
		case "issues":
			items := listOf(body, func(m map[string]any) map[string]any {
				if _, isPR := m["pull_request"]; isPR {
					return nil
				}
				return pick(m, "number", "title", "state", "user.login", "comments", "created_at", "updated_at", "html_url")
			}, limit)
			return map[string]any{"repo": repo, "state": state, "count": len(items), "issues": items}, nil
		case "pulls":
			items := listOf(body, func(m map[string]any) map[string]any {
				return pick(m, "number", "title", "state", "user.login", "draft", "created_at", "updated_at", "merged_at", "html_url")
			}, limit)
			return map[string]any{"repo": repo, "state": state, "count": len(items), "pulls": items}, nil
		default:
			m := mapOf(body)
			items := listOf(m["items"], func(m map[string]any) map[string]any {
				return pick(m, "full_name", "description", "stargazers_count", "language", "html_url", "pushed_at")
			}, limit)
			return map[string]any{"query": str(args, "query"), "total_count": m["total_count"], "count": len(items), "results": items}, nil
		}
	}
}

func mapOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// listOf maps each object of an array through f, dropping nils, up to limit.
func listOf(v any, f func(map[string]any) map[string]any, limit int) []map[string]any {
	out := []map[string]any{}
	arr, _ := v.([]any)
	for _, e := range arr {
		if len(out) == limit {
			break
		}
		if m, ok := e.(map[string]any); ok {
			if r := f(m); r != nil {
				out = append(out, r)
			}
		}
	}
	return out
}

// pick copies the named fields; "a.b" reads a nested field into key "a_b".
func pick(m map[string]any, keys ...string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		cur := any(m)
		for _, part := range strings.Split(k, ".") {
			cur = mapOf(cur)[part]
		}
		if cur != nil {
			out[strings.ReplaceAll(k, ".", "_")] = cur
		}
	}
	return out
}
