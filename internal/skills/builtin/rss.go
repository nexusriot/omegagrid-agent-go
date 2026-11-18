package builtin

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

func RssFetchSchema() Skill {
	return Skill{Name: "rss_fetch", Description: "Fetch RSS or Atom feeds and return their newest items (title, link, date, summary). " +
		"Pass since (RFC3339, or a duration such as 24h) to keep only newer items — the building block for scheduled digests.",
		Parameters: map[string]Param{
			"urls":        {Type: "array", Description: "Feed URLs (http/https), up to 10", Required: true},
			"since":       {Type: "string", Description: "Only items published after this: RFC3339 time or a duration like 24h / 7d", Required: false},
			"limit":       {Type: "number", Description: "Max items in total, newest first (default 20, max 100)", Required: false},
			"summary_len": {Type: "number", Description: "Max characters of each item's summary (default 300, 0 = omit)", Required: false},
		}}
}

const maxFeeds = 10

// feedItem is one entry, normalised across RSS 2.0, RSS 1.0 (RDF) and Atom.
type feedItem struct {
	Feed      string `json:"feed"`
	Title     string `json:"title"`
	Link      string `json:"link"`
	Published string `json:"published,omitempty"`
	Summary   string `json:"summary,omitempty"`
	at        time.Time
}

// RssFetch is RssFetchContext without cancellation.
func RssFetch(timeoutSec float64) Executor {
	return withoutContext(RssFetchContext(timeoutSec))
}

func RssFetchContext(timeoutSec float64) ContextExecutor {
	cl := httpClient(timeoutSec)
	return func(ctx context.Context, args map[string]any) (any, error) {
		urls := strList(args, "urls")
		if len(urls) == 0 {
			if u := str(args, "url"); u != "" {
				urls = []string{u}
			}
		}
		if len(urls) == 0 {
			return map[string]any{"error": "urls is required"}, nil
		}
		if len(urls) > maxFeeds {
			return map[string]any{"error": fmt.Sprintf("at most %d feeds per call", maxFeeds)}, nil
		}
		since, err := parseSince(str(args, "since"), time.Now())
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		limit := min(max(intOr(args, "limit", 20), 1), 100)
		sumLen := max(intOr(args, "summary_len", 300), 0)

		var items []feedItem
		var feedErrors []map[string]any
		for _, u := range urls {
			got, err := fetchFeed(ctx, cl, u)
			if err != nil {
				feedErrors = append(feedErrors, map[string]any{"url": u, "error": err.Error()})
				continue
			}
			items = append(items, got...)
		}
		kept := items[:0]
		for _, it := range items {
			// An undated item cannot be shown to be old, so it stays.
			if !since.IsZero() && !it.at.IsZero() && !it.at.After(since) {
				continue
			}
			kept = append(kept, it)
		}
		sort.SliceStable(kept, func(i, j int) bool { return kept[i].at.After(kept[j].at) })
		if len(kept) > limit {
			kept = kept[:limit]
		}
		for i := range kept {
			if sumLen == 0 {
				kept[i].Summary = ""
			} else if r := []rune(kept[i].Summary); len(r) > sumLen {
				kept[i].Summary = string(r[:sumLen]) + "…"
			}
		}
		out := map[string]any{"count": len(kept), "items": kept}
		if !since.IsZero() {
			out["since"] = since.UTC().Format(time.RFC3339)
		}
		if len(feedErrors) > 0 {
			out["feed_errors"] = feedErrors
		}
		return out, nil
	}
}

// parseSince reads "since" as an RFC3339 time, a date, or a duration ago
// ("24h", "90m", "7d").
func parseSince(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	if d, err := parseLongDuration(s); err == nil && d > 0 {
		return now.Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("since must be RFC3339, YYYY-MM-DD or a duration like 24h or 7d, got %q", s)
}

// parseLongDuration is time.ParseDuration plus a "d" (day) and "w" (week)
// unit, e.g. "7d", "2w", "1d12h".
func parseLongDuration(s string) (time.Duration, error) {
	var total time.Duration
	rest := strings.TrimSpace(s)
	neg := strings.HasPrefix(rest, "-")
	rest = strings.TrimPrefix(rest, "-")
	for _, unit := range []struct {
		suffix string
		d      time.Duration
	}{{"w", 7 * 24 * time.Hour}, {"d", 24 * time.Hour}} {
		if i := strings.Index(rest, unit.suffix); i > 0 {
			var n float64
			if _, err := fmt.Sscanf(rest[:i], "%g", &n); err != nil {
				return 0, fmt.Errorf("bad duration %q", s)
			}
			total += time.Duration(n * float64(unit.d))
			rest = rest[i+1:]
		}
	}
	if rest != "" {
		d, err := time.ParseDuration(rest)
		if err != nil {
			return 0, err
		}
		total += d
	}
	if neg {
		total = -total
	}
	return total, nil
}

func fetchFeed(ctx context.Context, cl *http.Client, rawURL string) ([]feedItem, error) {
	if err := checkHTTPURL(rawURL); err != nil {
		return nil, err
	}
	resp, err := getWithRetry(ctx, cl, rawURL, http.Header{
		"User-Agent": {"OmegaGridAgent/1.0"},
		"Accept":     {"application/rss+xml, application/atom+xml, application/xml;q=0.9, */*;q=0.5"},
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	raw, _, err := readCapped(resp.Body, maxBodyBytes)
	if err != nil {
		return nil, err
	}
	return parseFeed(raw, rawURL)
}

type xmlFeed struct {
	XMLName xml.Name
	Title   string `xml:"title"`
	// RSS 2.0
	Channel struct {
		Title string    `xml:"title"`
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
	// RSS 1.0 (RDF) puts items beside the channel.
	RDFItems []rssItem `xml:"item"`
	// Atom
	Entries []atomEntry `xml:"entry"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	PubDate     string `xml:"pubDate"`
	DCDate      string `xml:"date"`
	Description string `xml:"description"`
	Content     string `xml:"encoded"`
}

type atomEntry struct {
	Title string `xml:"title"`
	Links []struct {
		Href string `xml:"href,attr"`
		Rel  string `xml:"rel,attr"`
	} `xml:"link"`
	Published string `xml:"published"`
	Updated   string `xml:"updated"`
	Summary   string `xml:"summary"`
	Content   string `xml:"content"`
}

func parseFeed(raw []byte, source string) ([]feedItem, error) {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.CharsetReader = feedCharsetReader
	dec.Strict = false
	var f xmlFeed
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("not an RSS/Atom feed: %w", err)
	}
	var out []feedItem
	feedTitle := strings.TrimSpace(f.Channel.Title)
	if feedTitle == "" {
		feedTitle = strings.TrimSpace(f.Title)
	}
	if feedTitle == "" {
		feedTitle = source
	}
	for _, it := range append(f.Channel.Items, f.RDFItems...) {
		date := firstNonEmpty(it.PubDate, it.DCDate)
		link := strings.TrimSpace(it.Link)
		if link == "" && strings.HasPrefix(it.GUID, "http") {
			link = strings.TrimSpace(it.GUID)
		}
		out = append(out, newFeedItem(feedTitle, it.Title, link, date, firstNonEmpty(it.Description, it.Content)))
	}
	for _, e := range f.Entries {
		link := ""
		for _, l := range e.Links {
			if l.Rel == "" || l.Rel == "alternate" {
				link = l.Href
				break
			}
		}
		if link == "" && len(e.Links) > 0 {
			link = e.Links[0].Href
		}
		out = append(out, newFeedItem(feedTitle, e.Title, link, firstNonEmpty(e.Published, e.Updated), firstNonEmpty(e.Summary, e.Content)))
	}
	if len(out) == 0 && f.XMLName.Local != "rss" && f.XMLName.Local != "feed" && f.XMLName.Local != "RDF" {
		return nil, fmt.Errorf("not an RSS/Atom feed (root element <%s>)", f.XMLName.Local)
	}
	return out, nil
}

func newFeedItem(feed, title, link, date, summary string) feedItem {
	it := feedItem{
		Feed:    feed,
		Title:   strings.TrimSpace(collapseWhitespace(extractText(title))),
		Link:    strings.TrimSpace(link),
		Summary: strings.TrimSpace(collapseWhitespace(extractText(summary))),
	}
	if t, ok := parseFeedDate(date); ok {
		it.at = t
		it.Published = t.UTC().Format(time.RFC3339)
	}
	return it
}

var feedDateLayouts = []string{
	time.RFC1123Z, time.RFC1123, time.RFC3339, time.RFC3339Nano, time.RFC822Z, time.RFC822,
	"Mon, 2 Jan 2006 15:04:05 -0700", "Mon, 2 Jan 2006 15:04:05 MST", "2 Jan 2006 15:04:05 -0700",
	"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02",
}

func parseFeedDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, l := range feedDateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// feedCharsetReader decodes a feed's declared encoding (any WHATWG label,
// see decodeCharset); an unknown one is refused rather than garbled.
func feedCharsetReader(label string, in io.Reader) (io.Reader, error) {
	r, err := decodeCharset(label, io.LimitReader(in, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("unsupported feed encoding %q", label)
	}
	return r, nil
}
