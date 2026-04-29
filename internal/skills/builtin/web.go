// Package builtin contains Go implementations of every Python sidecar skill.
package builtin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"
)

func httpClient(timeoutSec float64) *http.Client {
	return &http.Client{Timeout: time.Duration(timeoutSec * float64(time.Second))}
}

// ── Weather ──────────────────────────────────────────────────────────────────

func WeatherSchema() Skill {
	return Skill{Name: "weather", Description: "Get the current weather for a city using the Open-Meteo API (no API key required).",
		Parameters: map[string]Param{
			"city": {Type: "string", Description: "City name", Required: true},
		}}
}

func Weather(timeoutSec float64) Executor {
	cl := httpClient(timeoutSec)
	return func(args map[string]any) (any, error) {
		city := str(args, "city")
		if city == "" {
			return map[string]any{"error": "city is required"}, nil
		}
		// geocode
		geoURL := fmt.Sprintf("https://geocoding-api.open-meteo.com/v1/search?name=%s&count=1&language=en&format=json", url.QueryEscape(city))
		geoResp, err := cl.Get(geoURL)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		defer geoResp.Body.Close()
		var geo struct {
			Results []struct {
				Name      string  `json:"name"`
				Country   string  `json:"country"`
				Latitude  float64 `json:"latitude"`
				Longitude float64 `json:"longitude"`
			} `json:"results"`
		}
		if err := json.NewDecoder(geoResp.Body).Decode(&geo); err != nil || len(geo.Results) == 0 {
			return map[string]any{"error": fmt.Sprintf("city '%s' not found", city)}, nil
		}
		r := geo.Results[0]
		wURL := fmt.Sprintf(
			"https://api.open-meteo.com/v1/forecast?latitude=%.4f&longitude=%.4f"+
				"&current=temperature_2m,relative_humidity_2m,windspeed_10m,winddirection_10m,weathercode"+
				"&timezone=UTC&forecast_days=1",
			r.Latitude, r.Longitude,
		)
		wResp, err := cl.Get(wURL)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		defer wResp.Body.Close()
		var w struct {
			Current struct {
				Time             string  `json:"time"`
				Temperature      float64 `json:"temperature_2m"`
				Humidity         float64 `json:"relative_humidity_2m"`
				Windspeed        float64 `json:"windspeed_10m"`
				WindDirection    float64 `json:"winddirection_10m"`
				Weathercode      int     `json:"weathercode"`
			} `json:"current"`
		}
		if err := json.NewDecoder(wResp.Body).Decode(&w); err != nil {
			return map[string]any{"error": "weather API error"}, nil
		}
		return map[string]any{
			"city": r.Name, "country": r.Country,
			"latitude": r.Latitude, "longitude": r.Longitude,
			"temperature_c":       w.Current.Temperature,
			"humidity_percent":     w.Current.Humidity,
			"windspeed_kmh":        w.Current.Windspeed,
			"wind_direction_deg":   w.Current.WindDirection,
			"weather_code":         w.Current.Weathercode,
			"time":                 w.Current.Time,
		}, nil
	}
}

// ── HTTP Request ─────────────────────────────────────────────────────────────

func HttpRequestSchema() Skill {
	return Skill{Name: "http_request", Description: "Make an HTTP GET or POST request to any URL.",
		Parameters: map[string]Param{
			"url":     {Type: "string", Description: "Target URL", Required: true},
			"method":  {Type: "string", Description: "GET or POST (default GET)", Required: false},
			"body":    {Type: "object", Description: "JSON body for POST requests", Required: false},
			"headers": {Type: "object", Description: "Extra request headers", Required: false},
		}}
}

func HttpRequest(timeoutSec float64) Executor {
	cl := httpClient(timeoutSec)
	return func(args map[string]any) (any, error) {
		rawURL := str(args, "url")
		if rawURL == "" {
			return map[string]any{"error": "url is required"}, nil
		}
		method := strings.ToUpper(str(args, "method"))
		if method == "" {
			method = "GET"
		}
		var bodyReader io.Reader
		if method == "POST" {
			if b, ok := args["body"]; ok {
				enc, _ := json.Marshal(b)
				bodyReader = strings.NewReader(string(enc))
			}
		}
		req, err := http.NewRequest(method, rawURL, bodyReader)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		if method == "POST" {
			req.Header.Set("Content-Type", "application/json")
		}
		if hdrs, ok := args["headers"].(map[string]any); ok {
			for k, v := range hdrs {
				req.Header.Set(k, fmt.Sprintf("%v", v))
			}
		}
		resp, err := cl.Do(req)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var body any
		if err := json.Unmarshal(raw, &body); err != nil {
			body = string(raw)
		}
		return map[string]any{"status_code": resp.StatusCode, "body": body}, nil
	}
}

// ── Web Scrape ───────────────────────────────────────────────────────────────

func WebScrapeSchema() Skill {
	return Skill{Name: "web_scrape", Description: "Fetch a URL and return its text content.",
		Parameters: map[string]Param{
			"url":       {Type: "string", Description: "URL to scrape", Required: true},
			"max_chars": {Type: "number", Description: "Max characters to return (default 4000)", Required: false},
		}}
}

func WebScrape(timeoutSec float64) Executor {
	cl := httpClient(timeoutSec)
	return func(args map[string]any) (any, error) {
		rawURL := str(args, "url")
		if rawURL == "" {
			return map[string]any{"error": "url is required"}, nil
		}
		maxChars := intOr(args, "max_chars", 4000)

		req, _ := http.NewRequest(http.MethodGet, rawURL, nil)
		req.Header.Set("User-Agent", "OmegaGridAgent/1.0")
		resp, err := cl.Do(req)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		defer resp.Body.Close()
		ct := resp.Header.Get("Content-Type")
		raw, _ := io.ReadAll(resp.Body)

		var text string
		if strings.Contains(ct, "text/html") {
			text = extractText(string(raw))
		} else {
			text = string(raw)
		}
		text = strings.TrimSpace(collapseWhitespace(text))
		truncated := false
		if len(text) > maxChars {
			text = text[:maxChars]
			truncated = true
		}
		return map[string]any{
			"url":          rawURL,
			"content_type": ct,
			"text":         text,
			"truncated":    truncated,
		}, nil
	}
}

var wsRE = regexp.MustCompile(`\s+`)

func collapseWhitespace(s string) string { return wsRE.ReplaceAllString(s, " ") }

func extractText(htmlStr string) string {
	doc, err := html.Parse(strings.NewReader(htmlStr))
	if err != nil {
		return htmlStr
	}
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "head", "noscript":
				return
			}
		}
		if n.Type == html.TextNode {
			t := strings.TrimSpace(n.Data)
			if t != "" {
				sb.WriteString(t)
				sb.WriteString(" ")
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return sb.String()
}

// ── HTTP Health ──────────────────────────────────────────────────────────────

func HttpHealthSchema() Skill {
	return Skill{Name: "http_health", Description: "Check if an HTTP endpoint is healthy.",
		Parameters: map[string]Param{
			"url":             {Type: "string", Description: "URL to check", Required: true},
			"method":          {Type: "string", Description: "GET or HEAD (default GET)", Required: false},
			"expected_status": {Type: "number", Description: "Expected HTTP status (default 200)", Required: false},
			"body_contains":   {Type: "string", Description: "String that must appear in the response body", Required: false},
			"timeout":         {Type: "number", Description: "Timeout in seconds (default 10)", Required: false},
		}}
}

func HttpHealth() Executor {
	return func(args map[string]any) (any, error) {
		rawURL := str(args, "url")
		if rawURL == "" {
			return map[string]any{"error": "url is required"}, nil
		}
		method := strings.ToUpper(str(args, "method"))
		if method == "" {
			method = "GET"
		}
		expected := intOr(args, "expected_status", 200)
		bodyContains := str(args, "body_contains")
		timeout := floatOr(args, "timeout", 10)
		cl := httpClient(timeout)

		start := time.Now()
		req, _ := http.NewRequest(method, rawURL, nil)
		req.Header.Set("User-Agent", "OmegaGridAgent/1.0")
		resp, err := cl.Do(req)
		elapsed := time.Since(start).Milliseconds()
		if err != nil {
			return map[string]any{"url": rawURL, "ok": false, "error": err.Error(), "response_time_ms": elapsed}, nil
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		bodyMatch := bodyContains == "" || strings.Contains(string(raw), bodyContains)
		ok := resp.StatusCode == expected && bodyMatch
		return map[string]any{
			"url":              rawURL,
			"status_code":      resp.StatusCode,
			"ok":               ok,
			"response_time_ms": elapsed,
			"body_match":       bodyMatch,
			"content_length":   len(raw),
		}, nil
	}
}

// ── IP Info ──────────────────────────────────────────────────────────────────

func IpInfoSchema() Skill {
	return Skill{Name: "ip_info", Description: "Get geolocation and ISP info for an IP address (uses ip-api.com).",
		Parameters: map[string]Param{
			"ip": {Type: "string", Description: "IPv4/IPv6 address (omit for caller's public IP)", Required: false},
		}}
}

func IpInfo(timeoutSec float64) Executor {
	cl := httpClient(timeoutSec)
	return func(args map[string]any) (any, error) {
		ip := str(args, "ip")
		target := "https://ip-api.com/json/" + url.QueryEscape(ip)
		target += "?fields=status,message,country,countryCode,region,regionName,city,zip,lat,lon,timezone,isp,org,as,reverse,mobile,proxy,hosting,query"
		resp, err := cl.Get(target)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		defer resp.Body.Close()
		var m map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
			return map[string]any{"error": "ip-api decode error"}, nil
		}
		return m, nil
	}
}
