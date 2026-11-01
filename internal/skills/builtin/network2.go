package builtin

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"net/textproto"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nexusriot/omegagrid-agent-go/internal/skills/netguard"
)

func HttpTimingSchema() Skill {
	return Skill{Name: "http_timing", Description: "Break one HTTP request down into DNS, TCP connect, TLS handshake, time to first byte and transfer, " +
		"to explain why a URL is slow. Redirects are not followed (the Location is reported).",
		Parameters: map[string]Param{
			"url":     {Type: "string", Description: "http(s) URL", Required: true},
			"method":  {Type: "string", Description: "GET (default) or HEAD", Required: false},
			"samples": {Type: "number", Description: "Requests to make, each on a fresh connection (default 1, max 5)", Required: false},
			"timeout": {Type: "number", Description: "Seconds per request (default 15, max 60)", Required: false},
		}}
}

// HttpTiming is HttpTimingContext without cancellation.
func HttpTiming() Executor { return withoutContext(HttpTimingContext()) }

func HttpTimingContext() ContextExecutor {
	return func(ctx context.Context, args map[string]any) (any, error) {
		rawURL := str(args, "url")
		if rawURL == "" {
			return map[string]any{"error": "url is required"}, nil
		}
		if err := checkHTTPURL(rawURL); err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		method := strings.ToUpper(strings.TrimSpace(str(args, "method")))
		if method == "" {
			method = http.MethodGet
		}
		if method != http.MethodGet && method != http.MethodHead {
			return map[string]any{"error": "method must be GET or HEAD"}, nil
		}
		samples := min(max(intOr(args, "samples", 1), 1), 5)
		timeout := clampTimeout(floatOr(args, "timeout", 15), 15, maxTimeoutSec)

		var runs []map[string]any
		var totals []float64
		for i := 0; i < samples; i++ {
			r := timeOneRequest(ctx, method, rawURL, time.Duration(timeout*float64(time.Second)))
			runs = append(runs, r)
			if t, ok := r["total_ms"].(float64); ok && r["error"] == nil {
				totals = append(totals, t)
			}
			if ctx.Err() != nil {
				break
			}
		}
		out := map[string]any{"url": rawURL, "method": method}
		if len(runs) == 1 {
			for k, v := range runs[0] {
				out[k] = v
			}
			return out, nil
		}
		out["samples"] = runs
		if len(totals) > 0 {
			sort.Float64s(totals)
			out["total_ms_min"] = totals[0]
			out["total_ms_median"] = totals[len(totals)/2]
			out["total_ms_max"] = totals[len(totals)-1]
		}
		return out, nil
	}
}

func msSince(a, b time.Time) float64 {
	if a.IsZero() || b.IsZero() {
		return 0
	}
	return float64(b.Sub(a).Microseconds()) / 1000
}

func timeOneRequest(ctx context.Context, method, rawURL string, timeout time.Duration) map[string]any {
	var dnsStart, dnsDone, connStart, connDone, tlsStart, tlsDone, wrote, firstByte time.Time
	var remote string
	trace := &httptrace.ClientTrace{
		DNSStart:             func(httptrace.DNSStartInfo) { dnsStart = time.Now() },
		DNSDone:              func(httptrace.DNSDoneInfo) { dnsDone = time.Now() },
		ConnectStart:         func(string, string) { connStart = time.Now() },
		ConnectDone:          func(_, addr string, _ error) { connDone = time.Now(); remote = addr },
		TLSHandshakeStart:    func() { tlsStart = time.Now() },
		TLSHandshakeDone:     func(tls.ConnectionState, error) { tlsDone = time.Now() },
		WroteRequest:         func(httptrace.WroteRequestInfo) { wrote = time.Now() },
		GotFirstResponseByte: func() { firstByte = time.Now() },
	}
	tr := netguard.Transport()
	tr.DisableKeepAlives = true
	defer tr.CloseIdleConnections()
	cl := &http.Client{Transport: tr, Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	start := time.Now()
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), method, rawURL, nil)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	req.Header.Set("User-Agent", "OmegaGridAgent/1.0")
	resp, err := cl.Do(req)
	if err != nil {
		return map[string]any{"error": err.Error(), "total_ms": msSince(start, time.Now())}
	}
	n, _ := io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyBytes))
	resp.Body.Close()
	end := time.Now()
	out := map[string]any{
		"status":       resp.StatusCode,
		"remote_addr":  remote,
		"dns_ms":       msSince(dnsStart, dnsDone),
		"connect_ms":   msSince(connStart, connDone),
		"tls_ms":       msSince(tlsStart, tlsDone),
		"ttfb_ms":      msSince(wrote, firstByte),
		"transfer_ms":  msSince(firstByte, end),
		"total_ms":     msSince(start, end),
		"bytes":        n,
		"http_version": resp.Proto,
	}
	if loc := resp.Header.Get("Location"); loc != "" {
		out["location"] = loc
	}
	return out
}

func DnsCompareSchema() Skill {
	return Skill{Name: "dns_compare", Description: "Resolve a name through several DNS resolvers (default Cloudflare, Google, Quad9, plus the domain's own " +
		"authoritative nameservers) and report where the answers differ — for propagation checks and split-horizon problems.",
		Parameters: map[string]Param{
			"name":          {Type: "string", Description: "Domain name to resolve", Required: true},
			"record_type":   {Type: "string", Description: "A, AAAA, MX, TXT, NS or CNAME (default A)", Required: false},
			"resolvers":     {Type: "array", Description: "Resolver IPs (optionally ip:port), up to 8; default 1.1.1.1, 8.8.8.8, 9.9.9.9", Required: false},
			"authoritative": {Type: "boolean", Description: "Also ask the domain's own nameservers (default true)", Required: false},
		}}
}

var defaultResolvers = []string{"1.1.1.1", "8.8.8.8", "9.9.9.9"}

// lookupNS is the authoritative-nameserver seam.
var lookupNS = func(ctx context.Context, name string) ([]*net.NS, error) {
	return net.DefaultResolver.LookupNS(ctx, name)
}

// DnsCompare is DnsCompareContext without cancellation.
func DnsCompare() Executor { return withoutContext(DnsCompareContext()) }

func DnsCompareContext() ContextExecutor {
	return func(ctx context.Context, args map[string]any) (any, error) {
		name := strings.TrimSuffix(strings.TrimSpace(str(args, "name")), ".")
		if name == "" || !isHostname(name) {
			return map[string]any{"error": fmt.Sprintf("invalid name %q", name)}, nil
		}
		rtype := strings.ToUpper(strings.TrimSpace(str(args, "record_type")))
		if rtype == "" {
			rtype = "A"
		}
		if !dnsRecordTypes[rtype] {
			return map[string]any{"error": "record_type must be A, AAAA, MX, TXT, NS or CNAME"}, nil
		}
		resolvers := strList(args, "resolvers")
		if len(resolvers) == 0 {
			resolvers = defaultResolvers
		}
		if len(resolvers) > 8 {
			return map[string]any{"error": "at most 8 resolvers"}, nil
		}
		for i, r := range resolvers {
			ap, err := resolverAddr(r)
			if err != nil {
				return map[string]any{"error": err.Error()}, nil
			}
			resolvers[i] = ap
		}
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()

		type target struct{ label, addr string }
		var targets []target
		for _, r := range resolvers {
			targets = append(targets, target{r, r})
		}
		if boolOr(args, "authoritative", true) {
			if nss, err := lookupNS(ctx, zoneGuess(name)); err == nil {
				for i, ns := range nss {
					if i == 2 {
						break
					}
					host := strings.TrimSuffix(ns.Host, ".")
					ips, err := net.DefaultResolver.LookupHost(ctx, host)
					if err == nil && len(ips) > 0 {
						targets = append(targets, target{host + " (authoritative)", net.JoinHostPort(ips[0], "53")})
					}
				}
			}
		}

		results := make([]map[string]any, len(targets))
		var wg sync.WaitGroup
		for i, tg := range targets {
			wg.Add(1)
			go func(i int, label, addr string) {
				defer wg.Done()
				recs, err := lookupVia(ctx, addr, name, rtype)
				r := map[string]any{"resolver": label}
				if err != nil {
					r["error"] = err.Error()
				} else {
					r["records"] = recs
				}
				results[i] = r
			}(i, tg.label, tg.addr)
		}
		wg.Wait()

		answers := map[string][]string{}
		for _, r := range results {
			if recs, ok := r["records"].([]string); ok {
				key := strings.Join(recs, " | ")
				answers[key] = append(answers[key], r["resolver"].(string))
			}
		}
		var distinct []map[string]any
		for k, who := range answers {
			distinct = append(distinct, map[string]any{"answer": k, "resolvers": who})
		}
		sort.Slice(distinct, func(i, j int) bool {
			return len(distinct[i]["resolvers"].([]string)) > len(distinct[j]["resolvers"].([]string))
		})
		return map[string]any{
			"name": name, "record_type": rtype, "results": results,
			"consistent": len(answers) == 1, "distinct_answers": distinct,
		}, nil
	}
}

// resolverAddr accepts "ip" or "ip:port" (a resolver must be an address: a
// hostname would itself need resolving through some other resolver).
func resolverAddr(s string) (string, error) {
	s = strings.TrimSpace(s)
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.String(), nil
	}
	if ip, err := netip.ParseAddr(strings.Trim(s, "[]")); err == nil {
		return net.JoinHostPort(ip.String(), "53"), nil
	}
	return "", fmt.Errorf("resolver %q must be an IP address (optionally with :port)", s)
}

// zoneGuess drops leading labels until two remain — good enough to find the
// nameservers of www.example.com; for multi-label suffixes the lookup of
// the full name is tried first.
func zoneGuess(name string) string {
	parts := strings.Split(name, ".")
	if len(parts) <= 2 {
		return name
	}
	return strings.Join(parts[len(parts)-2:], ".")
}

// lookupVia resolves name with the Go resolver pointed at one server.
func lookupVia(ctx context.Context, server, name, rtype string) ([]string, error) {
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return netguard.Dialer(5*time.Second).DialContext(ctx, network, server)
	}}
	var out []string
	switch rtype {
	case "A", "AAAA":
		netw := "ip4"
		if rtype == "AAAA" {
			netw = "ip6"
		}
		ips, err := r.LookupIP(ctx, netw, name)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			out = append(out, ip.String())
		}
	case "MX":
		mxs, err := r.LookupMX(ctx, name)
		if err != nil {
			return nil, err
		}
		for _, mx := range mxs {
			out = append(out, fmt.Sprintf("%d %s", mx.Pref, mx.Host))
		}
	case "TXT":
		txts, err := r.LookupTXT(ctx, name)
		if err != nil {
			return nil, err
		}
		out = txts
	case "NS":
		nss, err := r.LookupNS(ctx, name)
		if err != nil {
			return nil, err
		}
		for _, ns := range nss {
			out = append(out, ns.Host)
		}
	case "CNAME":
		c, err := r.LookupCNAME(ctx, name)
		if err != nil {
			return nil, err
		}
		out = []string{c}
	}
	sort.Strings(out)
	return out, nil
}

func RdapLookupSchema() Skill {
	return Skill{Name: "rdap_lookup", Description: "Registration data via RDAP (the structured successor to WHOIS) for a domain, " +
		"an IP address or an AS number: registrar, status, creation/expiry dates, nameservers, network range, country.",
		Parameters: map[string]Param{
			"query": {Type: "string", Description: "Domain (example.com), IP (8.8.8.8, 2001:db8::1) or ASN (AS15169)", Required: true},
		}}
}

// rdapBase is the RDAP bootstrap redirector, a seam for tests.
var rdapBase = "https://rdap.org"

var asnRE = regexp.MustCompile(`^(?i)AS?(\d{1,10})$`)

// RdapLookup is RdapLookupContext without cancellation.
func RdapLookup(timeoutSec float64) Executor { return withoutContext(RdapLookupContext(timeoutSec)) }

func RdapLookupContext(timeoutSec float64) ContextExecutor {
	cl := httpClient(timeoutSec)
	return func(ctx context.Context, args map[string]any) (any, error) {
		q := strings.TrimSpace(str(args, "query"))
		var kind, key string
		switch {
		case q == "":
			return map[string]any{"error": "query is required"}, nil
		case asnRE.MatchString(q):
			kind, key = "autnum", asnRE.FindStringSubmatch(q)[1]
		case net.ParseIP(q) != nil:
			kind, key = "ip", q
		case isHostname(q) && strings.Contains(q, "."):
			kind, key = "domain", strings.TrimSuffix(strings.ToLower(q), ".")
		default:
			return map[string]any{"error": fmt.Sprintf("%q is not a domain, IP address or AS number", q)}, nil
		}
		target := rdapBase + "/" + kind + "/" + url.PathEscape(key)
		resp, err := getWithRetry(ctx, cl, target, http.Header{"Accept": {"application/rdap+json, application/json"}})
		if err != nil {
			return map[string]any{"query": q, "error": err.Error()}, nil
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return map[string]any{"query": q, "error": "not found in RDAP"}, nil
		}
		var doc map[string]any
		if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&doc); err != nil {
			return map[string]any{"query": q, "error": fmt.Sprintf("HTTP %d: not an RDAP response", resp.StatusCode)}, nil
		}
		if resp.StatusCode >= 400 {
			return map[string]any{"query": q, "error": fmt.Sprintf("HTTP %d: %v", resp.StatusCode, doc["title"])}, nil
		}
		out := summarizeRDAP(doc)
		out["query"] = q
		out["kind"] = kind
		out["source"] = resp.Request.URL.String()
		return out, nil
	}
}

func summarizeRDAP(doc map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"objectClassName", "handle", "ldhName", "unicodeName", "name", "type", "country",
		"startAddress", "endAddress", "ipVersion", "startAutnum", "endAutnum", "port43", "status"} {
		if v, ok := doc[k]; ok && v != nil && v != "" {
			out[k] = v
		}
	}
	if evs, ok := doc["events"].([]any); ok {
		events := map[string]any{}
		for _, e := range evs {
			if m, ok := e.(map[string]any); ok {
				if a, _ := m["eventAction"].(string); a != "" {
					events[a] = m["eventDate"]
				}
			}
		}
		if len(events) > 0 {
			out["events"] = events
		}
	}
	if nss, ok := doc["nameservers"].([]any); ok {
		var names []string
		for _, n := range nss {
			if m, ok := n.(map[string]any); ok {
				if s, _ := m["ldhName"].(string); s != "" {
					names = append(names, strings.ToLower(s))
				}
			}
		}
		if len(names) > 0 {
			out["nameservers"] = names
		}
	}
	if ents, ok := doc["entities"].([]any); ok {
		var list []map[string]any
		for _, e := range ents {
			if m, ok := e.(map[string]any); ok {
				ent := map[string]any{"roles": m["roles"]}
				if fn := vcardName(m["vcardArray"]); fn != "" {
					ent["name"] = fn
				}
				if h, _ := m["handle"].(string); h != "" {
					ent["handle"] = h
				}
				list = append(list, ent)
			}
		}
		if len(list) > 0 {
			out["entities"] = list
		}
	}
	return out
}

// vcardName pulls "fn" out of a jCard ["vcard", [["fn", {}, "text", "Name"], ...]].
func vcardName(v any) string {
	arr, ok := v.([]any)
	if !ok || len(arr) < 2 {
		return ""
	}
	props, ok := arr[1].([]any)
	if !ok {
		return ""
	}
	for _, p := range props {
		if pp, ok := p.([]any); ok && len(pp) >= 4 && pp[0] == "fn" {
			s, _ := pp[3].(string)
			return s
		}
	}
	return ""
}

func SmtpCheckSchema() Skill {
	return Skill{Name: "smtp_check", Description: "Check a domain's mail servers: connect to its MX hosts (or one given host), read the banner, " +
		"list ESMTP extensions, and test STARTTLS with the certificate's validity and expiry.",
		Parameters: map[string]Param{
			"domain":    {Type: "string", Description: "Domain whose MX hosts to check", Required: false},
			"host":      {Type: "string", Description: "Check this mail host directly instead of the MX records", Required: false},
			"port":      {Type: "number", Description: "Port (default 25)", Required: false},
			"max_hosts": {Type: "number", Description: "MX hosts to check (default 2, max 5)", Required: false},
			"timeout":   {Type: "number", Description: "Seconds per host (default 15, max 60)", Required: false},
		}}
}

// lookupMX is the MX seam.
var lookupMX = func(ctx context.Context, name string) ([]*net.MX, error) {
	return net.DefaultResolver.LookupMX(ctx, name)
}

// SmtpCheck is SmtpCheckContext without cancellation.
func SmtpCheck() Executor { return withoutContext(SmtpCheckContext()) }

func SmtpCheckContext() ContextExecutor {
	return func(ctx context.Context, args map[string]any) (any, error) {
		domain := strings.TrimSuffix(strings.TrimSpace(str(args, "domain")), ".")
		host := strings.TrimSuffix(strings.TrimSpace(str(args, "host")), ".")
		port := intOr(args, "port", 25)
		if port < 1 || port > 65535 {
			return map[string]any{"error": "port must be 1-65535"}, nil
		}
		timeout := time.Duration(clampTimeout(floatOr(args, "timeout", 15), 15, maxTimeoutSec) * float64(time.Second))
		maxHosts := min(max(intOr(args, "max_hosts", 2), 1), 5)

		var hosts []string
		var mxList []string
		switch {
		case host != "":
			if !isHostname(host) {
				return map[string]any{"error": fmt.Sprintf("invalid host %q", host)}, nil
			}
			hosts = []string{host}
		case domain != "":
			if !isHostname(domain) {
				return map[string]any{"error": fmt.Sprintf("invalid domain %q", domain)}, nil
			}
			mxs, err := lookupMX(ctx, domain)
			if err != nil || len(mxs) == 0 {
				// RFC 5321 §5.1: no MX means the domain itself is the mail host.
				hosts = []string{domain}
				mxList = []string{"(no MX; implicit " + domain + ")"}
			}
			sort.SliceStable(mxs, func(i, j int) bool { return mxs[i].Pref < mxs[j].Pref })
			for _, mx := range mxs {
				h := strings.TrimSuffix(mx.Host, ".")
				mxList = append(mxList, fmt.Sprintf("%d %s", mx.Pref, h))
				if h != "" && len(hosts) < maxHosts {
					hosts = append(hosts, h)
				}
			}
		default:
			return map[string]any{"error": "domain or host is required"}, nil
		}
		results := make([]map[string]any, len(hosts))
		for i, h := range hosts {
			results[i] = probeSMTP(ctx, h, port, timeout)
		}
		out := map[string]any{"hosts": results}
		if domain != "" {
			out["domain"] = domain
			out["mx"] = mxList
		}
		return out, nil
	}
}

func probeSMTP(ctx context.Context, host string, port int, timeout time.Duration) map[string]any {
	out := map[string]any{"host": host, "port": port}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	t0 := time.Now()
	conn, err := netguard.Dialer(timeout).DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		out["error"] = err.Error()
		return out
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	out["connect_ms"] = time.Since(t0).Milliseconds()
	tp := textproto.NewConn(conn)
	_, banner, err := tp.ReadResponse(220)
	if err != nil {
		out["error"] = "banner: " + err.Error()
		return out
	}
	out["banner"] = sanitizeBanner([]byte(banner))
	ext, err := smtpEHLO(tp)
	if err != nil {
		out["error"] = "EHLO: " + err.Error()
		return out
	}
	out["extensions"] = ext
	hasTLS := slices.ContainsFunc(ext, func(e string) bool { return strings.EqualFold(strings.Fields(e)[0], "STARTTLS") })
	out["starttls"] = hasTLS
	if !hasTLS {
		_ = tp.PrintfLine("QUIT")
		return out
	}
	if err := tp.PrintfLine("STARTTLS"); err != nil {
		out["tls_error"] = err.Error()
		return out
	}
	if _, _, err := tp.ReadResponse(220); err != nil {
		out["tls_error"] = "STARTTLS refused: " + err.Error()
		return out
	}
	tc := tls.Client(conn, &tls.Config{ServerName: host, InsecureSkipVerify: true, MinVersion: tls.VersionTLS10}) //nolint:gosec // verified below, to report the reason
	if err := tc.HandshakeContext(ctx); err != nil {
		out["tls_error"] = "handshake: " + err.Error()
		return out
	}
	st := tc.ConnectionState()
	out["tls_version"] = tls.VersionName(st.Version)
	out["cipher"] = tls.CipherSuiteName(st.CipherSuite)
	if len(st.PeerCertificates) > 0 {
		leaf := st.PeerCertificates[0]
		out["cert_subject"] = leaf.Subject.CommonName
		out["cert_issuer"] = leaf.Issuer.CommonName
		out["cert_names"] = leaf.DNSNames
		out["cert_not_after"] = leaf.NotAfter.UTC().Format(time.RFC3339)
		out["cert_days_left"] = int(time.Until(leaf.NotAfter).Hours() / 24)
		inter := x509.NewCertPool()
		for _, c := range st.PeerCertificates[1:] {
			inter.AddCert(c)
		}
		_, verr := leaf.Verify(x509.VerifyOptions{DNSName: host, Intermediates: inter})
		out["cert_verify_ok"] = verr == nil
		if verr != nil {
			out["cert_verify_error"] = verr.Error()
		}
	}
	tlsTP := textproto.NewConn(tc)
	_ = tlsTP.PrintfLine("QUIT")
	return out
}

func smtpEHLO(tp *textproto.Conn) ([]string, error) {
	if err := tp.PrintfLine("EHLO omegagrid.invalid"); err != nil {
		return nil, err
	}
	_, msg, err := tp.ReadResponse(250)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(msg, "\n")
	if len(lines) <= 1 {
		return []string{}, nil
	}
	ext := make([]string, 0, len(lines)-1)
	for _, l := range lines[1:] {
		if l = strings.TrimSpace(l); l != "" {
			ext = append(ext, l)
		}
	}
	return ext, nil
}

func WakeOnLanSchema() Skill {
	return Skill{Name: "wake_on_lan", Description: "Send a Wake-on-LAN magic packet to wake a machine on the local network.",
		Parameters: map[string]Param{
			"mac":       {Type: "string", Description: "Target MAC address, e.g. 00:11:22:33:44:55", Required: true},
			"broadcast": {Type: "string", Description: "Broadcast or LAN address to send to (default 255.255.255.255)", Required: false},
			"port":      {Type: "number", Description: "UDP port (default 9)", Required: false},
		}}
}

// WakeOnLan is WakeOnLanContext without cancellation.
func WakeOnLan() Executor { return withoutContext(WakeOnLanContext()) }

func WakeOnLanContext() ContextExecutor {
	return func(ctx context.Context, args map[string]any) (any, error) {
		mac, err := net.ParseMAC(strings.TrimSpace(str(args, "mac")))
		if err != nil || len(mac) != 6 {
			return map[string]any{"error": "mac must be a 6-byte MAC address such as 00:11:22:33:44:55"}, nil
		}
		if !netguard.AllowPrivate() {
			return map[string]any{"error": "wake_on_lan reaches the local network, which SKILL_ALLOW_PRIVATE_NETWORKS=false forbids"}, nil
		}
		bcast := strings.TrimSpace(str(args, "broadcast"))
		if bcast == "" {
			bcast = "255.255.255.255"
		}
		ip, err := netip.ParseAddr(bcast)
		if err != nil || !ip.Is4() {
			return map[string]any{"error": "broadcast must be an IPv4 address"}, nil
		}
		// A magic packet has no business leaving the LAN.
		if !(ip.IsPrivate() || ip.IsLoopback() || ip == netip.AddrFrom4([4]byte{255, 255, 255, 255})) {
			return map[string]any{"error": fmt.Sprintf("%s is not a local or broadcast address", ip)}, nil
		}
		port := intOr(args, "port", 9)
		if port < 1 || port > 65535 {
			return map[string]any{"error": "port must be 1-65535"}, nil
		}
		packet := make([]byte, 0, 102)
		for i := 0; i < 6; i++ {
			packet = append(packet, 0xff)
		}
		for i := 0; i < 16; i++ {
			packet = append(packet, mac...)
		}
		lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error { return enableBroadcast(c) }}
		pc, err := lc.ListenPacket(ctx, "udp4", ":0")
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		defer pc.Close()
		dst := &net.UDPAddr{IP: ip.AsSlice(), Port: port}
		if _, err := pc.WriteTo(packet, dst); err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		return map[string]any{"sent": true, "mac": mac.String(), "to": dst.String(), "bytes": len(packet)}, nil
	}
}

func SubnetSweepSchema() Skill {
	return Skill{Name: "subnet_sweep", Description: "Find live hosts in a CIDR range (at most 256 addresses) by TCP connect probes; " +
		"a host that accepts or actively refuses a connection is up. Reports open probe ports per host.",
		Parameters: map[string]Param{
			"cidr":    {Type: "string", Description: "Range, e.g. 192.168.1.0/24 (IPv4 /24 or smaller, IPv6 /120 or smaller)", Required: true},
			"ports":   {Type: "string", Description: "Probe ports (default 22,80,443,445,3389,8080; max 10)", Required: false},
			"timeout": {Type: "number", Description: "Seconds per probe (default 1, max 5)", Required: false},
		}}
}

const maxSweepHosts = 256

// SubnetSweep is SubnetSweepContext without cancellation.
func SubnetSweep() Executor { return withoutContext(SubnetSweepContext()) }

func SubnetSweepContext() ContextExecutor {
	return func(ctx context.Context, args map[string]any) (any, error) {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(str(args, "cidr")))
		if err != nil {
			return map[string]any{"error": "cidr must be a range such as 192.168.1.0/24"}, nil
		}
		prefix = prefix.Masked()
		hostBits := prefix.Addr().BitLen() - prefix.Bits()
		if hostBits > 8 {
			return map[string]any{"error": fmt.Sprintf("range too large: at most %d addresses (/24 for IPv4, /120 for IPv6)", maxSweepHosts)}, nil
		}
		portsStr := str(args, "ports")
		if portsStr == "" {
			portsStr = "22,80,443,445,3389,8080"
		}
		ports, err := parsePorts(portsStr)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		if len(ports) > 10 {
			return map[string]any{"error": "at most 10 probe ports"}, nil
		}
		timeout := time.Duration(clampTimeout(floatOr(args, "timeout", 1), 1, 5) * float64(time.Second))

		var addrs []netip.Addr
		for a := prefix.Addr(); prefix.Contains(a); a = a.Next() {
			addrs = append(addrs, a)
			if !a.Next().IsValid() {
				break
			}
		}
		if prefix.Addr().Is4() && hostBits >= 2 {
			addrs = addrs[1 : len(addrs)-1] // network and broadcast addresses
		}

		type hostResult struct {
			IP        string `json:"ip"`
			OpenPorts []int  `json:"open_ports"`
			RTTms     int64  `json:"rtt_ms"`
		}
		var (
			mu      sync.Mutex
			alive   []hostResult
			blocked int
		)
		sem := make(chan struct{}, 64)
		var wg sync.WaitGroup
		for _, a := range addrs {
			if netguard.CheckAddr(a) != nil {
				blocked++
				continue
			}
			wg.Add(1)
			go func(a netip.Addr) {
				defer wg.Done()
				up := false
				var open []int
				var best time.Duration
				for _, p := range ports {
					sem <- struct{}{}
					t0 := time.Now()
					c, err := netguard.Dialer(timeout).DialContext(ctx, "tcp", netip.AddrPortFrom(a, uint16(p)).String())
					rtt := time.Since(t0)
					<-sem
					if err == nil {
						c.Close()
						up = true
						open = append(open, p)
					} else if errors.Is(err, syscall.ECONNREFUSED) {
						up = true
					} else {
						continue
					}
					if best == 0 || rtt < best {
						best = rtt
					}
				}
				if up {
					if open == nil {
						open = []int{}
					}
					mu.Lock()
					alive = append(alive, hostResult{IP: a.String(), OpenPorts: open, RTTms: best.Milliseconds()})
					mu.Unlock()
				}
			}(a)
		}
		wg.Wait()
		sort.Slice(alive, func(i, j int) bool {
			ai, _ := netip.ParseAddr(alive[i].IP)
			aj, _ := netip.ParseAddr(alive[j].IP)
			return ai.Less(aj)
		})
		if alive == nil {
			alive = []hostResult{}
		}
		out := map[string]any{"cidr": prefix.String(), "scanned": len(addrs) - blocked, "alive": alive, "alive_count": len(alive), "probe_ports": ports}
		if blocked > 0 {
			out["blocked"] = blocked
			out["note"] = "some addresses are refused by the network policy (SKILL_ALLOW_PRIVATE_NETWORKS / link-local)"
		}
		return out, nil
	}
}
