package builtin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const rss2 = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel><title>Blog</title>
<item><title>Old post</title><link>https://b.example/old</link><pubDate>Mon, 02 Jan 2006 15:04:05 +0000</pubDate><description>old</description></item>
<item><title>New &amp; shiny</title><link>https://b.example/new</link><pubDate>Fri, 30 Oct 2026 08:00:00 +0000</pubDate><description>&lt;p&gt;Hello &lt;b&gt;world&lt;/b&gt;&lt;/p&gt;</description></item>
</channel></rss>`

const atomFeed = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom"><title>Atom feed</title>
<entry><title>Atom entry</title><link rel="alternate" href="https://a.example/1"/><updated>2026-10-29T10:00:00Z</updated><summary>sum</summary></entry>
</feed>`

const latin1 = "<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><rss><channel><title>Caf\xe9</title><item><title>Cr\xe8me \x93quoted\x94</title><link>https://c.example</link></item></channel></rss>"

func feedServer(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rss":
			_, _ = w.Write([]byte(rss2))
		case "/atom":
			_, _ = w.Write([]byte(atomFeed))
		case "/latin1":
			_, _ = w.Write([]byte(latin1))
		case "/html":
			_, _ = w.Write([]byte("<html><body>not a feed</body></html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRssFetchMergesAndSorts(t *testing.T) {
	srv := feedServer(t)
	m := runExec(t, RssFetch(5), map[string]any{"urls": []any{srv.URL + "/rss", srv.URL + "/atom", srv.URL + "/missing"}})
	items := m["items"].([]feedItem)
	if len(items) != 3 {
		t.Fatalf("items = %+v", items)
	}
	if items[0].Title != "New & shiny" || items[1].Title != "Atom entry" || items[2].Title != "Old post" {
		t.Fatalf("order = %q, %q, %q", items[0].Title, items[1].Title, items[2].Title)
	}
	if items[0].Summary != "Hello world" || items[1].Link != "https://a.example/1" || items[0].Feed != "Blog" {
		t.Fatalf("normalisation = %+v", items[:2])
	}
	if errs, _ := m["feed_errors"].([]map[string]any); len(errs) != 1 || !strings.Contains(errs[0]["error"].(string), "404") {
		t.Fatalf("feed_errors = %v", m["feed_errors"])
	}
}

func TestRssFetchSinceAndLimits(t *testing.T) {
	srv := feedServer(t)
	m := runExec(t, RssFetch(5), map[string]any{"urls": srv.URL + "/rss," + srv.URL + "/atom", "since": "2026-10-29T12:00:00Z"})
	if items := m["items"].([]feedItem); len(items) != 1 || items[0].Title != "New & shiny" {
		t.Fatalf("since filter = %+v", items)
	}
	m = runExec(t, RssFetch(5), map[string]any{"urls": []any{srv.URL + "/rss"}, "limit": 1, "summary_len": 0})
	if items := m["items"].([]feedItem); len(items) != 1 || items[0].Summary != "" {
		t.Fatalf("limit/summary_len = %+v", items)
	}
	m = runExec(t, RssFetch(5), map[string]any{"urls": []any{srv.URL + "/html"}})
	if errs, _ := m["feed_errors"].([]map[string]any); len(errs) != 1 {
		t.Fatalf("html page accepted as a feed: %v", m)
	}
	if m := runExec(t, RssFetch(5), map[string]any{}); m["error"] == nil {
		t.Fatal("missing urls accepted")
	}
	if m := runExec(t, RssFetch(5), map[string]any{"urls": "file:///etc/passwd"}); m["feed_errors"] == nil {
		t.Fatalf("non-http url fetched: %v", m)
	}
}

func TestRssFetchLatin1(t *testing.T) {
	srv := feedServer(t)
	m := runExec(t, RssFetch(5), map[string]any{"urls": srv.URL + "/latin1"})
	items := m["items"].([]feedItem)
	if len(items) != 1 || items[0].Title != "Crème “quoted”" || items[0].Feed != "Café" {
		t.Fatalf("latin1 = %+v", items)
	}
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"24h":                  now.Add(-24 * time.Hour),
		"7d":                   now.Add(-7 * 24 * time.Hour),
		"1w":                   now.Add(-7 * 24 * time.Hour),
		"1d12h":                now.Add(-36 * time.Hour),
		"2026-10-01":           time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		"2026-10-01T05:00:00Z": time.Date(2026, 10, 1, 5, 0, 0, 0, time.UTC),
	} {
		got, err := parseSince(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("parseSince(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseSince("yesterday", now); err == nil {
		t.Error("garbage accepted")
	}
}
