package builtin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

func PrometheusQuerySchema() Skill {
	return Skill{Name: "prometheus_query", Description: "Query Prometheus monitoring metrics (PromQL) — CPU, memory, disk, network, " +
		"request rate, latency and other host / service / container metrics — on a configured server (SKILL_PROMETHEUS): " +
		"action=query for current values, range for a time series over the last range (min/max/avg/last per series; " +
		"chart=true graphs it as a line chart image), alerts for firing alerts, targets for scrape targets that are down, " +
		"metrics to find metric names.",
		Parameters: map[string]Param{
			"action": {Type: "string", Description: "query (default) | range | alerts | targets | metrics", Required: false},
			"query":  {Type: "string", Description: "PromQL, e.g. 100 - avg by(instance)(rate(node_cpu_seconds_total{mode=\"idle\"}[5m]))*100", Required: false},
			"range":  {Type: "string", Description: "range: how far back, e.g. 30m, 6h, 2d (default 1h)", Required: false},
			"step":   {Type: "string", Description: "range: resolution, e.g. 1m (default: range/120)", Required: false},
			"chart":  {Type: "boolean", Description: "range: also render a line chart image", Required: false},
			"match":  {Type: "string", Description: "metrics: substring the metric name must contain, e.g. disk", Required: false},
			"server": {Type: "string", Description: "Configured server name; default: the first one", Required: false},
		}}
}

// PrometheusConfig lists the servers prometheus_query may read.
type PrometheusConfig struct {
	Named map[string]string // name → base URL (user:pass@ in the URL = basic auth)
	Order []string
	Token string // bearer token sent to every server (SKILL_PROMETHEUS_TOKEN)
}

const (
	promMaxSeries = 50
	promMaxPoints = 120
	promMaxBody   = 16 << 20
)

var promDurRE = regexp.MustCompile(`^(\d+(?:\.\d+)?)(ms|s|m|h|d|w)$`)

// parsePromDuration reads "90s", "15m", "6h", "2d", "1w".
func parsePromDuration(s string) (time.Duration, error) {
	m := promDurRE.FindStringSubmatch(strings.TrimSpace(strings.ToLower(s)))
	if m == nil {
		return 0, fmt.Errorf("bad duration %q (use e.g. 30m, 6h, 2d)", s)
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	unit := map[string]time.Duration{"ms": time.Millisecond, "s": time.Second, "m": time.Minute,
		"h": time.Hour, "d": 24 * time.Hour, "w": 7 * 24 * time.Hour}[m[2]]
	return time.Duration(v * float64(unit)), nil
}

type promClient struct {
	base  *url.URL
	token string
	http  *http.Client
}

func (c PrometheusConfig) client(name string, timeout time.Duration) (*promClient, string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		if len(c.Order) == 0 {
			return nil, "", fmt.Errorf("no Prometheus server is configured (set SKILL_PROMETHEUS=name=http://prometheus:9090)")
		}
		name = c.Order[0]
	}
	raw, ok := c.Named[name]
	if !ok {
		return nil, "", fmt.Errorf("unknown server %q (configured: %s)", name, strings.Join(c.Order, ", "))
	}
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, "", fmt.Errorf("server %q: %q is not an http(s) URL", name, raw)
	}
	// The server is operator-configured (like a SQL DSN), so it is reached
	// directly even when SKILL_BLOCK_PRIVATE_NETWORKS is on: Prometheus
	// nearly always lives on a private network.
	return &promClient{base: u, token: c.Token, http: &http.Client{Timeout: timeout}}, name, nil
}

// get calls /api/v1/<path> and returns the "data" member.
func (p *promClient) get(ctx context.Context, path string, q url.Values) (json.RawMessage, error) {
	u := *p.base
	u.User = nil
	u.Path = strings.TrimRight(u.Path, "/") + "/api/v1/" + path
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if pw, ok := p.base.User.Password(); ok {
		req.SetBasicAuth(p.base.User.Username(), pw)
	} else if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, promMaxBody+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > promMaxBody {
		return nil, fmt.Errorf("response larger than %d MB; narrow the query", promMaxBody>>20)
	}
	var env struct {
		Status    string          `json:"status"`
		Data      json.RawMessage `json:"data"`
		ErrorType string          `json:"errorType"`
		Error     string          `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("HTTP %d: not a Prometheus API response", resp.StatusCode)
	}
	if env.Status != "success" {
		return nil, fmt.Errorf("%s: %s", env.ErrorType, env.Error)
	}
	return env.Data, nil
}

type promSample struct {
	Metric map[string]string `json:"metric"`
	Value  [2]any            `json:"value"`
	Values [][2]any          `json:"values"`
}

func promFloat(v any) (float64, bool) {
	s, ok := v.(string)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

func promTime(v any) time.Time {
	f, _ := v.(float64)
	sec, frac := math.Modf(f)
	return time.Unix(int64(sec), int64(frac*1e9)).UTC()
}

// metricLabel renders {__name__="up", job="node"} as up{job="node"}.
func metricLabel(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		if k != "__name__" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%q", k, m[k])
	}
	s := m["__name__"]
	if len(parts) > 0 || s == "" {
		s += "{" + strings.Join(parts, ", ") + "}"
	}
	return s
}

func roundSig(f float64) float64 {
	if f == 0 || math.Abs(f) >= 1e15 {
		return f
	}
	p := math.Pow(10, 4-math.Floor(math.Log10(math.Abs(f))))
	return math.Round(f*p) / p
}

func PrometheusQueryContext(c PrometheusConfig, timeoutSec float64) ContextExecutor {
	timeout := time.Duration(timeoutSec * float64(time.Second))
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return func(ctx context.Context, args map[string]any) (any, error) {
		pc, server, err := c.client(str(args, "server"), timeout)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		action := strings.ToLower(strings.TrimSpace(str(args, "action")))
		query := strings.TrimSpace(str(args, "query"))
		out := func(m map[string]any) (any, error) {
			m["server"] = server
			return m, nil
		}
		fail := func(err error) (any, error) { return map[string]any{"error": err.Error(), "server": server}, nil }

		switch action {
		case "", "query":
			if query == "" {
				return map[string]any{"error": "query is required"}, nil
			}
			data, err := pc.get(ctx, "query", url.Values{"query": {query}})
			if err != nil {
				return fail(err)
			}
			return out(promInstant(data))
		case "range":
			if query == "" {
				return map[string]any{"error": "query is required"}, nil
			}
			return promRange(ctx, pc, query, args, out, fail)
		case "alerts":
			data, err := pc.get(ctx, "alerts", nil)
			if err != nil {
				return fail(err)
			}
			return out(promAlerts(data))
		case "targets":
			data, err := pc.get(ctx, "targets", url.Values{"state": {"active"}})
			if err != nil {
				return fail(err)
			}
			return out(promTargets(data))
		case "metrics":
			data, err := pc.get(ctx, "label/__name__/values", nil)
			if err != nil {
				return fail(err)
			}
			var names []string
			if err := json.Unmarshal(data, &names); err != nil {
				return fail(err)
			}
			match := strings.ToLower(strings.TrimSpace(str(args, "match")))
			var hit []string
			for _, n := range names {
				if match == "" || strings.Contains(strings.ToLower(n), match) {
					hit = append(hit, n)
				}
			}
			res := map[string]any{"count": len(hit)}
			if len(hit) > 200 {
				res["truncated"] = true
				hit = hit[:200]
			}
			res["metrics"] = hit
			return out(res)
		}
		return map[string]any{"error": "action must be query, range, alerts, targets or metrics"}, nil
	}
}

func promInstant(data json.RawMessage) map[string]any {
	var r struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return map[string]any{"error": err.Error()}
	}
	switch r.ResultType {
	case "scalar", "string":
		var v [2]any
		_ = json.Unmarshal(r.Result, &v)
		f, ok := promFloat(v[1])
		if !ok {
			return map[string]any{"result_type": r.ResultType, "value": v[1]}
		}
		return map[string]any{"result_type": r.ResultType, "value": roundSig(f)}
	}
	var samples []promSample
	if err := json.Unmarshal(r.Result, &samples); err != nil {
		return map[string]any{"error": err.Error()}
	}
	series := make([]map[string]any, 0, len(samples))
	for _, s := range samples {
		e := map[string]any{"metric": metricLabel(s.Metric)}
		if f, ok := promFloat(s.Value[1]); ok {
			e["value"] = roundSig(f)
		} else {
			e["value"] = s.Value[1]
		}
		series = append(series, e)
	}
	// Largest first: "which host has the highest X" is the usual question.
	sort.SliceStable(series, func(i, j int) bool {
		a, aok := series[i]["value"].(float64)
		b, bok := series[j]["value"].(float64)
		return aok && (!bok || a > b)
	})
	res := map[string]any{"result_type": r.ResultType, "series_count": len(series)}
	if len(series) > promMaxSeries {
		series = series[:promMaxSeries]
		res["truncated"] = true
	}
	res["series"] = series
	return res
}

func promRange(ctx context.Context, pc *promClient, query string, args map[string]any,
	out func(map[string]any) (any, error), fail func(error) (any, error)) (any, error) {
	rng := time.Hour
	if s := str(args, "range"); s != "" {
		d, err := parsePromDuration(s)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		rng = d
	}
	if rng > 90*24*time.Hour {
		return map[string]any{"error": "range is limited to 90d"}, nil
	}
	step := rng / promMaxPoints
	if s := str(args, "step"); s != "" {
		d, err := parsePromDuration(s)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		step = d
	}
	if step < time.Second {
		step = time.Second
	}
	if rng/step > 11000 { // Prometheus refuses more than 11 000 points per series
		step = rng / 11000
	}
	end := time.Now().UTC()
	start := end.Add(-rng)
	data, err := pc.get(ctx, "query_range", url.Values{
		"query": {query},
		"start": {strconv.FormatInt(start.Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
		"step":  {strconv.FormatFloat(step.Seconds(), 'f', -1, 64)},
	})
	if err != nil {
		return fail(err)
	}
	var r struct {
		Result []promSample `json:"result"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return fail(err)
	}
	type series struct {
		label string
		ts    []time.Time
		vals  []float64
		last  float64
	}
	var all []series
	for _, s := range r.Result {
		se := series{label: metricLabel(s.Metric)}
		for _, v := range s.Values {
			if f, ok := promFloat(v[1]); ok {
				se.ts = append(se.ts, promTime(v[0]))
				se.vals = append(se.vals, f)
			}
		}
		if len(se.vals) > 0 {
			se.last = se.vals[len(se.vals)-1]
			all = append(all, se)
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].last > all[j].last })
	res := map[string]any{
		"query": query, "start": start.Format(time.RFC3339), "end": end.Format(time.RFC3339),
		"step": step.String(), "series_count": len(all),
	}
	if len(all) > promMaxSeries {
		all = all[:promMaxSeries]
		res["truncated"] = true
	}
	list := make([]map[string]any, 0, len(all))
	for _, se := range all {
		lo, hi, sum := se.vals[0], se.vals[0], 0.0
		for _, v := range se.vals {
			lo, hi, sum = math.Min(lo, v), math.Max(hi, v), sum+v
		}
		// Downsample by averaging into at most promMaxPoints buckets.
		n := len(se.vals)
		buckets := min(n, promMaxPoints)
		pts := make([][2]any, 0, buckets)
		for b := 0; b < buckets; b++ {
			i0, i1 := b*n/buckets, (b+1)*n/buckets
			acc := 0.0
			for _, v := range se.vals[i0:i1] {
				acc += v
			}
			pts = append(pts, [2]any{se.ts[i0].Format(time.RFC3339), roundSig(acc / float64(i1-i0))})
		}
		list = append(list, map[string]any{
			"metric": se.label, "min": roundSig(lo), "max": roundSig(hi),
			"avg": roundSig(sum / float64(n)), "last": roundSig(se.last), "points": pts,
		})
	}
	res["series"] = list
	if boolOr(args, "chart", false) && len(all) > 0 {
		if img, err := promChart(query, all[0].ts, func(yield func(string, []float64)) {
			for i, se := range all {
				if i == 8 {
					return
				}
				yield(se.label, se.vals)
			}
		}, rng); err == nil {
			for k, v := range img {
				res[k] = v
			}
		} else {
			res["chart_error"] = err.Error()
		}
	}
	return out(res)
}

// promChart renders up to 8 series as a line chart; series that are
// shorter than the first are padded with their last value.
func promChart(title string, ts []time.Time, seq func(func(string, []float64)), rng time.Duration) (map[string]any, error) {
	n := len(ts)
	if n > maxChartPoints {
		n = maxChartPoints
	}
	labels := make([]string, n)
	layout := "15:04"
	if rng > 36*time.Hour {
		layout = "01-02 15:04"
	}
	for i := 0; i < n; i++ {
		labels[i] = ts[i*len(ts)/n].Local().Format(layout)
	}
	var series []chartSeries
	seq(func(name string, vals []float64) {
		v := make([]float64, n)
		for i := 0; i < n; i++ {
			j := i * len(vals) / n
			if j >= len(vals) {
				j = len(vals) - 1
			}
			v[i] = vals[j]
		}
		series = append(series, chartSeries{Name: fitLabel(name, 60), Values: v})
	})
	c := newCanvas(1000, 520)
	c.axesChart("line", fitLabel(title, 110), labels, series, "", "")
	var buf bytes.Buffer
	if err := png.Encode(&buf, c.img); err != nil {
		return nil, err
	}
	return map[string]any{
		"image_base64": base64.StdEncoding.EncodeToString(buf.Bytes()),
		"image_format": "png", "size_bytes": buf.Len(),
	}, nil
}

func fitLabel(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

func promAlerts(data json.RawMessage) map[string]any {
	var r struct {
		Alerts []struct {
			Labels      map[string]string `json:"labels"`
			Annotations map[string]string `json:"annotations"`
			State       string            `json:"state"`
			ActiveAt    string            `json:"activeAt"`
			Value       string            `json:"value"`
		} `json:"alerts"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return map[string]any{"error": err.Error()}
	}
	firing, pending := []map[string]any{}, 0
	for _, a := range r.Alerts {
		if a.State != "firing" {
			pending++
			continue
		}
		e := map[string]any{"alert": a.Labels["alertname"], "severity": a.Labels["severity"], "since": a.ActiveAt}
		labels := map[string]string{}
		for k, v := range a.Labels {
			if k != "alertname" && k != "severity" {
				labels[k] = v
			}
		}
		if len(labels) > 0 {
			e["labels"] = labels
		}
		if s := firstNonEmpty(a.Annotations["summary"], a.Annotations["description"], a.Annotations["message"]); s != "" {
			e["summary"] = clipRunes(s, 300)
		}
		firing = append(firing, e)
	}
	sort.SliceStable(firing, func(i, j int) bool {
		return fmt.Sprint(firing[i]["since"]) < fmt.Sprint(firing[j]["since"])
	})
	res := map[string]any{"firing_count": len(firing), "pending_count": pending}
	if len(firing) > 100 {
		firing = firing[:100]
		res["truncated"] = true
	}
	res["firing"] = firing
	return res
}

func promTargets(data json.RawMessage) map[string]any {
	var r struct {
		ActiveTargets []struct {
			Labels     map[string]string `json:"labels"`
			ScrapePool string            `json:"scrapePool"`
			ScrapeURL  string            `json:"scrapeUrl"`
			Health     string            `json:"health"`
			LastError  string            `json:"lastError"`
			LastScrape string            `json:"lastScrape"`
		} `json:"activeTargets"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return map[string]any{"error": err.Error()}
	}
	byJob := map[string]map[string]int{}
	down := []map[string]any{}
	for _, t := range r.ActiveTargets {
		job := firstNonEmpty(t.Labels["job"], t.ScrapePool)
		if byJob[job] == nil {
			byJob[job] = map[string]int{}
		}
		byJob[job][t.Health]++
		if t.Health != "up" {
			down = append(down, map[string]any{"job": job, "instance": t.Labels["instance"], "url": t.ScrapeURL,
				"health": t.Health, "error": clipRunes(t.LastError, 300), "last_scrape": t.LastScrape})
		}
	}
	res := map[string]any{"targets": len(r.ActiveTargets), "down_count": len(down), "jobs": byJob}
	if len(down) > 100 {
		down = down[:100]
		res["truncated"] = true
	}
	res["down"] = down
	return res
}
