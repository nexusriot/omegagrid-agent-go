package builtin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// ─── uptime_check ────────────────────────────────────────────────────────────

func UptimeCheckSchema() Skill {
	return Skill{Name: "uptime_check", Description: "Check that a URL is up (status, latency, optional body text) and record the result, " +
		"returning uptime % over 24h / 7d / 30d, the last state change and recent history. Built for scheduled monitoring: " +
		"`changed` is true when the up/down state differs from the previous check. action=history reads without checking.",
		Parameters: map[string]Param{
			"url":             {Type: "string", Description: "http(s) URL to check", Required: true},
			"action":          {Type: "string", Description: "check (default) | history | forget", Required: false},
			"expected_status": {Type: "number", Description: "Status that counts as up (default: any 2xx/3xx)", Required: false},
			"body_contains":   {Type: "string", Description: "Text the response body must contain to count as up", Required: false},
			"max_latency_ms":  {Type: "number", Description: "Slower than this counts as down (default: no limit)", Required: false},
			"timeout":         {Type: "number", Description: "Seconds (default 10, max 60)", Required: false},
			"history":         {Type: "number", Description: "Recent results to return (default 10, max 100)", Required: false},
		}}
}

const uptimeSchema = `
CREATE TABLE IF NOT EXISTS uptime_checks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  url TEXT NOT NULL,
  ts INTEGER NOT NULL,
  up INTEGER NOT NULL,
  status INTEGER NOT NULL DEFAULT 0,
  latency_ms INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS uptime_checks_url_ts ON uptime_checks(url, ts);`

// uptimeRetention bounds the history kept per URL.
const uptimeRetention = 90 * 24 * time.Hour

type uptimeRow struct {
	TS        time.Time
	Up        bool
	Status    int
	LatencyMS int64
	Error     string
}

func UptimeCheckContext(db StateDB) ContextExecutor {
	return func(ctx context.Context, args map[string]any) (any, error) {
		raw := strings.TrimSpace(str(args, "url"))
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return map[string]any{"error": "url must be an http(s) URL"}, nil
		}
		key := u.String()
		conn, err := db.open(uptimeSchema)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		action := strings.ToLower(strings.TrimSpace(str(args, "action")))
		switch action {
		case "", "check":
		case "history":
			return uptimeReport(ctx, conn, key, nil, intOr(args, "history", 10))
		case "forget":
			res, err := conn.ExecContext(ctx, `DELETE FROM uptime_checks WHERE url = ?`, key)
			if err != nil {
				return map[string]any{"error": err.Error()}, nil
			}
			n, _ := res.RowsAffected()
			return map[string]any{"url": key, "deleted": n}, nil
		default:
			return map[string]any{"error": "action must be check, history or forget"}, nil
		}

		timeout := floatOr(args, "timeout", 10)
		if timeout <= 0 || timeout > 60 {
			timeout = 10
		}
		row := probeUptime(ctx, key, timeout, intOr(args, "expected_status", 0),
			str(args, "body_contains"), int64(intOr(args, "max_latency_ms", 0)))

		if _, err := conn.ExecContext(ctx, `INSERT INTO uptime_checks(url, ts, up, status, latency_ms, error) VALUES(?,?,?,?,?,?)`,
			key, row.TS.Unix(), boolInt(row.Up), row.Status, row.LatencyMS, row.Error); err != nil {
			return map[string]any{"error": "record result: " + err.Error()}, nil
		}
		_, _ = conn.ExecContext(ctx, `DELETE FROM uptime_checks WHERE url = ? AND ts < ?`, key, time.Now().Add(-uptimeRetention).Unix())
		return uptimeReport(ctx, conn, key, &row, intOr(args, "history", 10))
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func probeUptime(ctx context.Context, target string, timeout float64, expected int, contains string, maxLatency int64) uptimeRow {
	row := uptimeRow{TS: time.Now().UTC()}
	cl := httpClient(timeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		row.Error = err.Error()
		return row
	}
	req.Header.Set("User-Agent", "OmegaGridAgent-Uptime/1.0")
	t0 := time.Now()
	resp, err := cl.Do(req)
	if err != nil {
		row.LatencyMS = time.Since(t0).Milliseconds()
		row.Error = err.Error()
		return row
	}
	defer resp.Body.Close()
	var body []byte
	if contains != "" {
		body, _ = io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	} else {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 2<<20))
	}
	row.LatencyMS = time.Since(t0).Milliseconds()
	row.Status = resp.StatusCode
	switch {
	case expected > 0 && resp.StatusCode != expected:
		row.Error = fmt.Sprintf("status %d, expected %d", resp.StatusCode, expected)
	case expected == 0 && resp.StatusCode >= 400:
		row.Error = fmt.Sprintf("status %d", resp.StatusCode)
	case contains != "" && !strings.Contains(string(body), contains):
		row.Error = fmt.Sprintf("body does not contain %q", contains)
	case maxLatency > 0 && row.LatencyMS > maxLatency:
		row.Error = fmt.Sprintf("latency %dms over the %dms limit", row.LatencyMS, maxLatency)
	default:
		row.Up = true
	}
	return row
}

// uptimeReport summarises the stored history of key. current is the check
// just made (nil for action=history).
func uptimeReport(ctx context.Context, conn *sql.DB, key string, current *uptimeRow, n int) (any, error) {
	if n <= 0 {
		n = 10
	}
	if n > 100 {
		n = 100
	}
	rows, err := conn.QueryContext(ctx, `SELECT ts, up, status, latency_ms, error FROM uptime_checks WHERE url = ? AND ts >= ? ORDER BY ts DESC, id DESC`,
		key, time.Now().Add(-30*24*time.Hour).Unix())
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	defer rows.Close()
	var hist []uptimeRow
	for rows.Next() {
		var r uptimeRow
		var ts int64
		var up int
		if err := rows.Scan(&ts, &up, &r.Status, &r.LatencyMS, &r.Error); err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		r.TS, r.Up = time.Unix(ts, 0).UTC(), up == 1
		hist = append(hist, r)
	}
	if err := rows.Err(); err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	out := map[string]any{"url": key, "checks_30d": len(hist)}
	if len(hist) == 0 {
		out["note"] = "no checks recorded yet for this URL"
		return out, nil
	}

	now := time.Now()
	pct := func(d time.Duration) any {
		var total, up int
		for _, r := range hist {
			if now.Sub(r.TS) <= d {
				total++
				if r.Up {
					up++
				}
			}
		}
		if total == 0 {
			return nil
		}
		return round2(100 * float64(up) / float64(total))
	}
	out["uptime_pct"] = map[string]any{"24h": pct(24 * time.Hour), "7d": pct(7 * 24 * time.Hour), "30d": pct(30 * 24 * time.Hour)}

	// Latency of successful checks in the last 24h.
	var lat []int64
	for _, r := range hist {
		if r.Up && now.Sub(r.TS) <= 24*time.Hour {
			lat = append(lat, r.LatencyMS)
		}
	}
	if len(lat) > 0 {
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		var sum int64
		for _, v := range lat {
			sum += v
		}
		out["latency_ms_24h"] = map[string]any{"avg": sum / int64(len(lat)), "p50": lat[len(lat)/2], "p95": lat[(len(lat)*95)/100], "max": lat[len(lat)-1]}
	}

	latest := hist[0]
	state := "down"
	if latest.Up {
		state = "up"
	}
	out["state"] = state
	// Since when has it been in this state?
	since := latest.TS
	for _, r := range hist[1:] {
		if r.Up != latest.Up {
			break
		}
		since = r.TS
	}
	out["state_since"] = since.Format(time.RFC3339)
	if current != nil {
		out["checked_at"] = current.TS.Format(time.RFC3339)
		out["up"] = current.Up
		out["status"] = current.Status
		out["latency_ms"] = current.LatencyMS
		if current.Error != "" {
			out["reason"] = current.Error
		}
		out["changed"] = len(hist) > 1 && hist[1].Up != current.Up
		out["first_check"] = len(hist) == 1
	}
	recent := make([]map[string]any, 0, n)
	for i, r := range hist {
		if i == n {
			break
		}
		e := map[string]any{"at": r.TS.Format(time.RFC3339), "up": r.Up, "status": r.Status, "latency_ms": r.LatencyMS}
		if r.Error != "" {
			e["error"] = r.Error
		}
		recent = append(recent, e)
	}
	out["recent"] = recent
	return out, nil
}

func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

// ─── ssl_labs_grade ──────────────────────────────────────────────────────────

func SslLabsGradeSchema() Skill {
	return Skill{Name: "ssl_labs_grade", Description: "Get the Qualys SSL Labs grade (A+ … F) of a public HTTPS site, per endpoint, with the main warnings. " +
		"Uses cached assessments when available; a fresh assessment can take minutes — if it is still running, call again later.",
		Parameters: map[string]Param{
			"host":     {Type: "string", Description: "Public hostname, e.g. example.com", Required: true},
			"fresh":    {Type: "boolean", Description: "Start a new assessment instead of using a cached one (default false)", Required: false},
			"max_age":  {Type: "number", Description: "Accept cached results up to this many hours old (default 24)", Required: false},
			"wait_sec": {Type: "number", Description: "How long to wait for a running assessment (default 60, max 240)", Required: false},
		}}
}

// SslLabsConfig selects the API: v3 needs nothing; v4 (Qualys' registered
// API) needs the e-mail address an account was registered with.
type SslLabsConfig struct {
	BaseURL string // default https://api.ssllabs.com/api/v3 (v4 when Email is set)
	Email   string
	// PollEvery is the wait between status polls (default 10s).
	PollEvery time.Duration
}

func (c SslLabsConfig) base() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	if c.Email != "" {
		return "https://api.ssllabs.com/api/v4"
	}
	return "https://api.ssllabs.com/api/v3"
}

func SslLabsGradeContext(c SslLabsConfig, timeoutSec float64) ContextExecutor {
	return func(ctx context.Context, args map[string]any) (any, error) {
		host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(str(args, "host")), "."))
		host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
		host = strings.SplitN(host, "/", 2)[0]
		if !isHostname(host) || net.ParseIP(host) != nil {
			return map[string]any{"error": "host must be a public hostname (SSL Labs does not assess IP addresses)"}, nil
		}
		wait := floatOr(args, "wait_sec", 60)
		if wait < 0 {
			wait = 0
		}
		if wait > 240 {
			wait = 240
		}
		maxAge := intOr(args, "max_age", 24)
		if maxAge <= 0 {
			maxAge = 24
		}
		poll := c.PollEvery
		if poll <= 0 {
			poll = 10 * time.Second
		}
		cl := httpClient(timeoutSec)
		deadline := time.Now().Add(time.Duration(wait * float64(time.Second)))

		q := url.Values{"host": {host}, "all": {"done"}}
		if boolOr(args, "fresh", false) {
			q.Set("startNew", "on")
		} else {
			q.Set("fromCache", "on")
			q.Set("maxAge", fmt.Sprint(maxAge))
		}
		for attempt := 0; ; attempt++ {
			var rep sslLabsReport
			if err := c.get(ctx, cl, q, &rep); err != nil {
				return map[string]any{"error": err.Error(), "host": host}, nil
			}
			switch rep.Status {
			case "READY":
				return rep.summary(), nil
			case "ERROR":
				return map[string]any{"error": "SSL Labs: " + nonEmpty(rep.StatusMessage, "assessment failed"), "host": host}, nil
			}
			if time.Now().Add(poll).After(deadline) {
				out := map[string]any{"host": host, "status": nonEmpty(rep.Status, "IN_PROGRESS"),
					"note": "The assessment is still running; call ssl_labs_grade again in a minute or two (without fresh=true)."}
				if p := rep.progress(); p >= 0 {
					out["progress_pct"] = p
				}
				return out, nil
			}
			// Polling must not restart the assessment.
			q.Del("startNew")
			select {
			case <-time.After(poll):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
}

func (c SslLabsConfig) get(ctx context.Context, cl *http.Client, q url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+"/analyze?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "OmegaGridAgent/1.0")
	if c.Email != "" {
		req.Header.Set("email", c.Email)
	}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	switch {
	case resp.StatusCode == 429 || resp.StatusCode == 529:
		return fmt.Errorf("SSL Labs is rate-limiting or overloaded (HTTP %d); try again in a few minutes", resp.StatusCode)
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return fmt.Errorf("SSL Labs refused the request (HTTP %d); the v4 API needs SKILL_SSLLABS_EMAIL set to a registered address", resp.StatusCode)
	case resp.StatusCode >= 400:
		return fmt.Errorf("SSL Labs HTTP %d: %s", resp.StatusCode, truncateStr(string(raw), 300))
	}
	return json.Unmarshal(raw, out)
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

type sslLabsReport struct {
	Host          string `json:"host"`
	Status        string `json:"status"`
	StatusMessage string `json:"statusMessage"`
	TestTime      int64  `json:"testTime"`
	Endpoints     []struct {
		IPAddress         string `json:"ipAddress"`
		ServerName        string `json:"serverName"`
		StatusMessage     string `json:"statusMessage"`
		Grade             string `json:"grade"`
		GradeTrustIgnored string `json:"gradeTrustIgnored"`
		HasWarnings       bool   `json:"hasWarnings"`
		Progress          int    `json:"progress"`
		Details           *struct {
			Protocols []struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"protocols"`
			VulnBeast      bool `json:"vulnBeast"`
			Heartbleed     bool `json:"heartbleed"`
			Poodle         bool `json:"poodle"`
			PoodleTLS      int  `json:"poodleTls"`
			SupportsRc4    bool `json:"supportsRc4"`
			ForwardSecrecy int  `json:"forwardSecrecy"`
			HstsPolicy     *struct {
				Status string `json:"status"`
				MaxAge int64  `json:"maxAge"`
			} `json:"hstsPolicy"`
			CertChains []struct {
				Issues int `json:"issues"`
			} `json:"certChains"`
		} `json:"details"`
	} `json:"endpoints"`
}

func (r *sslLabsReport) progress() int {
	if len(r.Endpoints) == 0 {
		return -1
	}
	sum := 0
	for _, e := range r.Endpoints {
		if e.Progress > 0 {
			sum += e.Progress
		}
	}
	return sum / len(r.Endpoints)
}

// gradeRank orders grades worst-first so the summary can report the worst.
var gradeRank = map[string]int{"T": 0, "M": 0, "F": 1, "E": 2, "D": 3, "C": 4, "B": 5, "A-": 6, "A": 7, "A+": 8}

func (r *sslLabsReport) summary() map[string]any {
	out := map[string]any{"host": r.Host, "status": "READY"}
	if r.TestTime > 0 {
		out["tested_at"] = time.UnixMilli(r.TestTime).UTC().Format(time.RFC3339)
	}
	worst := ""
	var eps []map[string]any
	for _, e := range r.Endpoints {
		ep := map[string]any{"ip": e.IPAddress, "grade": nonEmpty(e.Grade, "-")}
		if e.ServerName != "" {
			ep["server_name"] = e.ServerName
		}
		if e.Grade == "" {
			ep["message"] = e.StatusMessage
		} else if worst == "" || gradeRank[e.Grade] < gradeRank[worst] {
			worst = e.Grade
		}
		if e.GradeTrustIgnored != "" && e.GradeTrustIgnored != e.Grade {
			ep["grade_trust_ignored"] = e.GradeTrustIgnored
		}
		if d := e.Details; d != nil {
			var protos []string
			for _, p := range d.Protocols {
				protos = append(protos, p.Name+" "+p.Version)
			}
			ep["protocols"] = protos
			var warn []string
			if d.Heartbleed {
				warn = append(warn, "vulnerable to Heartbleed")
			}
			if d.Poodle || d.PoodleTLS == 2 {
				warn = append(warn, "vulnerable to POODLE")
			}
			if d.SupportsRc4 {
				warn = append(warn, "supports RC4")
			}
			if d.VulnBeast {
				warn = append(warn, "BEAST-susceptible")
			}
			if d.ForwardSecrecy == 0 {
				warn = append(warn, "no forward secrecy")
			}
			for _, p := range d.Protocols {
				if p.Name == "TLS" && (p.Version == "1.0" || p.Version == "1.1") || p.Name == "SSL" {
					warn = append(warn, "legacy protocol "+p.Name+" "+p.Version+" enabled")
				}
			}
			if d.HstsPolicy != nil {
				ep["hsts"] = d.HstsPolicy.Status
			}
			for _, cc := range d.CertChains {
				if cc.Issues != 0 {
					warn = append(warn, "certificate chain issues")
					break
				}
			}
			if len(warn) > 0 {
				ep["warnings"] = warn
			}
		}
		eps = append(eps, ep)
	}
	out["endpoints"] = eps
	if worst != "" {
		out["grade"] = worst
	}
	return out
}

// ─── dns_propagation ─────────────────────────────────────────────────────────

func DnsPropagationSchema() Skill {
	return Skill{Name: "dns_propagation", Description: "Check DNS propagation: resolve a record through ~16 public resolvers worldwide " +
		"and report which ones return the expected (or majority) answer, as a percentage.",
		Parameters: map[string]Param{
			"name":        {Type: "string", Description: "Domain name", Required: true},
			"record_type": {Type: "string", Description: "A, AAAA, MX, TXT, NS or CNAME (default A)", Required: false},
			"expected":    {Type: "string", Description: "Value the record should now have (e.g. the new IP); default: the majority answer", Required: false},
		}}
}

type publicResolver struct{ Addr, Label string }

// propagationResolvers is a spread of large anycast and regional resolvers.
var propagationResolvers = []publicResolver{
	{"1.1.1.1", "Cloudflare (anycast)"},
	{"8.8.8.8", "Google (anycast)"},
	{"9.9.9.9", "Quad9 (anycast, CH)"},
	{"208.67.222.222", "OpenDNS / Cisco (US)"},
	{"94.140.14.14", "AdGuard (CY)"},
	{"185.228.168.9", "CleanBrowsing (US)"},
	{"76.76.2.0", "Control D (CA)"},
	{"149.112.112.112", "Quad9 secondary"},
	{"8.26.56.26", "Comodo Secure (US)"},
	{"64.6.64.6", "UltraDNS / Vercara (US)"},
	{"84.200.69.80", "DNS.Watch (DE)"},
	{"77.88.8.8", "Yandex (RU)"},
	{"223.5.5.5", "AliDNS (CN)"},
	{"119.29.29.29", "DNSPod / Tencent (CN)"},
	{"168.126.63.1", "KT (KR)"},
	{"1.0.0.1", "Cloudflare secondary"},
}

// propagationLookup is the per-resolver lookup seam (tests replace it).
var propagationLookup = func(ctx context.Context, server, name, rtype string) ([]string, error) {
	return lookupVia(ctx, server, name, rtype)
}

func DnsPropagationContext() ContextExecutor {
	return func(ctx context.Context, args map[string]any) (any, error) {
		name := strings.TrimSuffix(strings.TrimSpace(str(args, "name")), ".")
		if !isHostname(name) {
			return map[string]any{"error": fmt.Sprintf("invalid name %q", name)}, nil
		}
		rtype := strings.ToUpper(strings.TrimSpace(str(args, "record_type")))
		if rtype == "" {
			rtype = "A"
		}
		if !dnsRecordTypes[rtype] {
			return map[string]any{"error": "record_type must be A, AAAA, MX, TXT, NS or CNAME"}, nil
		}
		expected := strings.TrimSpace(str(args, "expected"))

		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		type res struct {
			answer []string
			err    error
		}
		results := make([]res, len(propagationResolvers))
		var wg sync.WaitGroup
		for i, r := range propagationResolvers {
			wg.Add(1)
			go func(i int, addr string) {
				defer wg.Done()
				ans, err := propagationLookup(ctx, net.JoinHostPort(addr, "53"), name, rtype)
				norm := make([]string, 0, len(ans))
				for _, a := range ans {
					norm = append(norm, strings.TrimSuffix(strings.ToLower(a), "."))
				}
				sort.Strings(norm)
				results[i] = res{norm, err}
			}(i, r.Addr)
		}
		wg.Wait()

		// Group identical answer sets.
		groups := map[string]int{}
		for _, r := range results {
			if r.err == nil && len(r.answer) > 0 {
				groups[strings.Join(r.answer, ", ")]++
			}
		}
		majority, best := "", 0
		for k, n := range groups {
			if n > best || (n == best && k < majority) {
				majority, best = k, n
			}
		}
		matches := func(answer []string) bool {
			if expected == "" {
				return strings.Join(answer, ", ") == majority
			}
			want := strings.TrimSuffix(strings.ToLower(expected), ".")
			for _, a := range answer {
				if a == want || strings.Contains(a, want) {
					return true
				}
			}
			return false
		}
		var rows []map[string]any
		answered, matched := 0, 0
		for i, r := range results {
			row := map[string]any{"resolver": propagationResolvers[i].Label, "ip": propagationResolvers[i].Addr}
			switch {
			case r.err != nil:
				row["error"] = shortDNSErr(r.err)
			case len(r.answer) == 0:
				row["answer"] = []string{}
				answered++
			default:
				answered++
				row["answer"] = r.answer
				if matches(r.answer) {
					row["match"] = true
					matched++
				} else {
					row["match"] = false
				}
			}
			rows = append(rows, row)
		}
		out := map[string]any{
			"name": name, "record_type": rtype,
			"resolvers": len(results), "answered": answered, "matching": matched,
			"results": rows,
		}
		if answered > 0 {
			out["propagation_pct"] = round2(100 * float64(matched) / float64(answered))
		}
		if expected != "" {
			out["expected"] = expected
		} else if majority != "" {
			out["majority_answer"] = majority
		}
		distinct := make([]map[string]any, 0, len(groups))
		for k, n := range groups {
			distinct = append(distinct, map[string]any{"answer": k, "resolvers": n})
		}
		sort.Slice(distinct, func(i, j int) bool { return distinct[i]["resolvers"].(int) > distinct[j]["resolvers"].(int) })
		out["distinct_answers"] = distinct
		return out, nil
	}
}

func shortDNSErr(err error) string {
	var de *net.DNSError
	if e, ok := err.(*net.DNSError); ok {
		de = e
	}
	if de != nil {
		switch {
		case de.IsNotFound:
			return "NXDOMAIN / no such record"
		case de.IsTimeout:
			return "timeout"
		}
	}
	return truncateStr(err.Error(), 120)
}
