package builtin

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

func HtmlToMarkdownSchema() Skill {
	return Skill{Name: "html_to_markdown", Description: "Convert a web page (url) or an HTML string to readable Markdown — headings, links, lists, " +
		"code, tables — keeping the page's main content and dropping navigation, scripts and forms.",
		Parameters: map[string]Param{
			"url":       {Type: "string", Description: "Page to fetch (http/https)", Required: false},
			"html":      {Type: "string", Description: "HTML to convert instead of fetching", Required: false},
			"max_chars": {Type: "number", Description: "Max characters returned (default 8000, max 100000)", Required: false},
		}}
}

// HtmlToMarkdown is HtmlToMarkdownContext without cancellation.
func HtmlToMarkdown(timeoutSec float64) Executor {
	return withoutContext(HtmlToMarkdownContext(timeoutSec))
}

func HtmlToMarkdownContext(timeoutSec float64) ContextExecutor {
	cl := httpClient(timeoutSec)
	return func(ctx context.Context, args map[string]any) (any, error) {
		src := str(args, "html")
		pageURL := str(args, "url")
		var base *url.URL
		if src == "" {
			if pageURL == "" {
				return map[string]any{"error": "url or html is required"}, nil
			}
			if err := checkHTTPURL(pageURL); err != nil {
				return map[string]any{"error": err.Error()}, nil
			}
			resp, err := getWithRetry(ctx, cl, pageURL, http.Header{"User-Agent": {"OmegaGridAgent/1.0"}})
			if err != nil {
				return map[string]any{"url": pageURL, "error": err.Error()}, nil
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 400 {
				return map[string]any{"url": pageURL, "error": "HTTP " + resp.Status}, nil
			}
			raw, _, _ := readCapped(resp.Body, maxBodyBytes)
			src = string(trimPartialRune(raw))
			base = resp.Request.URL
		} else if pageURL != "" {
			base, _ = url.Parse(pageURL)
		}
		maxChars := min(max(intOr(args, "max_chars", 8000), 1), 100000)
		title, md := htmlToMarkdown(src, base)
		truncated := false
		if utf8.RuneCountInString(md) > maxChars {
			md = string([]rune(md)[:maxChars]) + "\n…"
			truncated = true
		}
		out := map[string]any{"title": title, "markdown": md, "truncated": truncated}
		if pageURL != "" {
			out["url"] = pageURL
		}
		return out, nil
	}
}

var blankLinesRE = regexp.MustCompile(`\n{3,}`)

// htmlToMarkdown renders the main content of doc. base resolves relative
// links and images.
func htmlToMarkdown(doc string, base *url.URL) (string, string) {
	root, err := html.Parse(strings.NewReader(doc))
	if err != nil {
		return "", ""
	}
	title := ""
	if t := findFirst(root, atom.Title); t != nil {
		title = strings.TrimSpace(textOf(t))
	}
	start := findFirst(root, atom.Main)
	if start == nil {
		start = findFirst(root, atom.Article)
	}
	if start == nil {
		start = findFirst(root, atom.Body)
	}
	if start == nil {
		start = root
	}
	c := &mdConv{base: base}
	c.children(start)
	md := strings.TrimSpace(blankLinesRE.ReplaceAllString(c.b.String(), "\n\n"))
	return title, md
}

func findFirst(n *html.Node, a atom.Atom) *html.Node {
	if n.Type == html.ElementNode && n.DataAtom == a {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := findFirst(c, a); f != nil {
			return f
		}
	}
	return nil
}

func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

type mdConv struct {
	b     strings.Builder
	base  *url.URL
	list  []int // ordered-list counters; -1 for unordered
	inPre bool
}

var mdSkip = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Noscript: true, atom.Nav: true, atom.Footer: true,
	atom.Header: true, atom.Form: true, atom.Svg: true, atom.Iframe: true, atom.Button: true, atom.Template: true, atom.Aside: true,
}

func (c *mdConv) children(n *html.Node) {
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		c.node(ch)
	}
}

func (c *mdConv) block(s string) {
	c.b.WriteString("\n\n")
	c.b.WriteString(s)
	c.b.WriteString("\n\n")
}

func (c *mdConv) resolve(href string) string {
	if c.base == nil || href == "" {
		return href
	}
	u, err := c.base.Parse(href)
	if err != nil {
		return href
	}
	return u.String()
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}

func (c *mdConv) inline(n *html.Node) string {
	sub := &mdConv{base: c.base, list: c.list}
	sub.children(n)
	return strings.TrimSpace(collapseWhitespace(sub.b.String()))
}

func (c *mdConv) node(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		if c.inPre {
			c.b.WriteString(n.Data)
		} else {
			c.b.WriteString(collapseWhitespace(n.Data))
		}
		return
	case html.ElementNode:
	default:
		c.children(n)
		return
	}
	// hidden is a boolean attribute: present means hidden, whatever its value
	// (usually empty).
	if mdSkip[n.DataAtom] || hasAttr(n, "hidden") || attr(n, "aria-hidden") == "true" {
		return
	}
	switch n.DataAtom {
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		level := int(n.Data[1] - '0')
		if t := c.inline(n); t != "" {
			c.block(strings.Repeat("#", level) + " " + t)
		}
	case atom.P, atom.Div, atom.Section, atom.Figure, atom.Figcaption, atom.Dl:
		c.b.WriteString("\n\n")
		c.children(n)
		c.b.WriteString("\n\n")
	case atom.Br:
		c.b.WriteString("  \n")
	case atom.Hr:
		c.block("---")
	case atom.Strong, atom.B:
		if t := c.inline(n); t != "" {
			c.b.WriteString("**" + t + "**")
		}
	case atom.Em, atom.I:
		if t := c.inline(n); t != "" {
			c.b.WriteString("*" + t + "*")
		}
	case atom.Code:
		if c.inPre {
			c.children(n)
		} else if t := textOf(n); t != "" {
			c.b.WriteString("`" + strings.TrimSpace(t) + "`")
		}
	case atom.Pre:
		lang := ""
		if code := findFirst(n, atom.Code); code != nil {
			for _, cl := range strings.Fields(attr(code, "class")) {
				if l, ok := strings.CutPrefix(cl, "language-"); ok {
					lang = l
				}
			}
		}
		c.block("```" + lang + "\n" + strings.TrimRight(textOf(n), "\n") + "\n```")
	case atom.A:
		text := c.inline(n)
		href := attr(n, "href")
		switch {
		case text == "":
		case href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(strings.ToLower(href), "javascript:"):
			c.b.WriteString(text)
		default:
			c.b.WriteString("[" + text + "](" + c.resolve(href) + ")")
		}
	case atom.Img:
		if src := attr(n, "src"); src != "" && !strings.HasPrefix(src, "data:") {
			c.b.WriteString("![" + attr(n, "alt") + "](" + c.resolve(src) + ")")
		}
	case atom.Ul, atom.Ol:
		marker := -1
		if n.DataAtom == atom.Ol {
			marker = 0
		}
		c.list = append(c.list, marker)
		c.b.WriteString("\n\n")
		c.children(n)
		c.list = c.list[:len(c.list)-1]
		c.b.WriteString("\n\n")
	case atom.Li:
		depth := len(c.list)
		prefix := "- "
		if depth > 0 && c.list[depth-1] >= 0 {
			c.list[depth-1]++
			prefix = strconv.Itoa(c.list[depth-1]) + ". "
		}
		indent := strings.Repeat("  ", max(depth-1, 0))
		c.b.WriteString("\n" + indent + prefix + c.inline(n))
	case atom.Blockquote:
		t := c.inline(n)
		if t != "" {
			c.block("> " + t)
		}
	case atom.Dt:
		c.b.WriteString("\n**" + c.inline(n) + "**\n")
	case atom.Dd:
		c.b.WriteString(": " + c.inline(n) + "\n")
	case atom.Table:
		c.block(c.table(n))
	default:
		c.children(n)
	}
}

// table renders rows as a Markdown table; the first row is the header.
func (c *mdConv) table(n *html.Node) string {
	var rows [][]string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.DataAtom == atom.Tr {
			var cells []string
			for td := n.FirstChild; td != nil; td = td.NextSibling {
				if td.Type == html.ElementNode && (td.DataAtom == atom.Td || td.DataAtom == atom.Th) {
					cells = append(cells, strings.ReplaceAll(c.inline(td), "|", "\\|"))
				}
			}
			if len(cells) > 0 {
				rows = append(rows, cells)
			}
			return
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(n)
	if len(rows) == 0 {
		return ""
	}
	width := 0
	for _, r := range rows {
		width = max(width, len(r))
	}
	var b strings.Builder
	for i, r := range rows {
		for len(r) < width {
			r = append(r, "")
		}
		b.WriteString("| " + strings.Join(r, " | ") + " |\n")
		if i == 0 {
			b.WriteString("|" + strings.Repeat(" --- |", width) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
