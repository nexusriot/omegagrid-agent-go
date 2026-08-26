package builtin

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// tls_probe
// ---------------------------------------------------------------------------

func TlsProbeSchema() Skill {
	return Skill{Name: "tls_probe", Description: "Probe a TLS endpoint: certificate chain, SANs, expiry, protocol, cipher, and ALPN.",
		Parameters: map[string]Param{
			"host":        {Type: "string", Description: "Hostname or IP to dial", Required: true},
			"port":        {Type: "number", Description: "TCP port (default 443)", Required: false},
			"server_name": {Type: "string", Description: "SNI / ServerName (default: host when it is a name)", Required: false},
			"timeout":     {Type: "number", Description: "Dial+handshake timeout in seconds (default 10)", Required: false},
		}}
}

func TlsProbe() Executor {
	return func(args map[string]any) (any, error) {
		host := str(args, "host")
		if host == "" {
			return map[string]any{"error": "host is required"}, nil
		}
		if !isHostname(host) {
			return map[string]any{"error": fmt.Sprintf("invalid host: %q", host)}, nil
		}
		port := intOr(args, "port", 443)
		if port < 1 || port > 65535 {
			return map[string]any{"error": fmt.Sprintf("port %d outside 1-65535", port)}, nil
		}
		timeout := floatOr(args, "timeout", 10)
		if timeout <= 0 {
			timeout = 10
		}
		sni := str(args, "server_name")
		if sni == "" && net.ParseIP(host) == nil {
			sni = host
		}
		if sni != "" && !isHostname(sni) {
			return map[string]any{"error": fmt.Sprintf("invalid server_name: %q", sni)}, nil
		}

		addr := net.JoinHostPort(host, strconv.Itoa(port))
		dialer := &net.Dialer{Timeout: time.Duration(timeout * float64(time.Second))}
		// InsecureSkipVerify so we still return the peer cert on name/expiry
		// mismatch; verification status is reported separately.
		cfg := &tls.Config{
			ServerName:         sni,
			InsecureSkipVerify: true, //nolint:gosec
			MinVersion:         tls.VersionTLS10,
		}

		t0 := time.Now()
		raw, err := dialer.Dial("tcp", addr)
		if err != nil {
			return map[string]any{"host": host, "port": port, "error": err.Error()}, nil
		}
		conn := tls.Client(raw, cfg)
		_ = conn.SetDeadline(time.Now().Add(time.Duration(timeout * float64(time.Second))))
		if err := conn.Handshake(); err != nil {
			_ = raw.Close()
			return map[string]any{
				"host": host, "port": port, "server_name": sni,
				"error": err.Error(), "elapsed_ms": time.Since(t0).Milliseconds(),
			}, nil
		}
		defer conn.Close()
		elapsed := time.Since(t0).Milliseconds()
		st := conn.ConnectionState()

		certs := make([]map[string]any, 0, len(st.PeerCertificates))
		now := time.Now()
		for i, c := range st.PeerCertificates {
			entry := certSummary(c, now)
			entry["index"] = i
			certs = append(certs, entry)
		}

		out := map[string]any{
			"host":              host,
			"port":              port,
			"server_name":       sni,
			"remote_addr":       conn.RemoteAddr().String(),
			"tls_version":       tlsVersionName(st.Version),
			"cipher_suite":      tls.CipherSuiteName(st.CipherSuite),
			"alpn":              st.NegotiatedProtocol,
			"peer_certificates": certs,
			"certificate_count": len(certs),
			"elapsed_ms":        elapsed,
		}
		if len(st.PeerCertificates) > 0 {
			leaf := st.PeerCertificates[0]
			out["leaf_subject"] = leaf.Subject.String()
			out["leaf_issuer"] = leaf.Issuer.String()
			out["leaf_not_before"] = leaf.NotBefore.UTC().Format(time.RFC3339)
			out["leaf_not_after"] = leaf.NotAfter.UTC().Format(time.RFC3339)
			out["leaf_dns_names"] = leaf.DNSNames
			out["expired"] = now.After(leaf.NotAfter)
			out["not_yet_valid"] = now.Before(leaf.NotBefore)
			days := int(leaf.NotAfter.Sub(now).Hours() / 24)
			out["days_until_expiry"] = days
		}

		// Report what a normal verifier would say (with system roots + SNI).
		// When the target is an IP there is no SNI, and an empty DNSName makes
		// Verify skip name checking altogether — reporting verify_ok=true for a
		// certificate that does not cover the address at all. x509 accepts an IP
		// literal here and checks it against the IP SANs, which is what a real
		// client does.
		verifyName := sni
		if verifyName == "" {
			verifyName = host
		}
		if len(st.PeerCertificates) > 0 {
			opts := x509.VerifyOptions{DNSName: verifyName, Intermediates: x509.NewCertPool()}
			for _, c := range st.PeerCertificates[1:] {
				opts.Intermediates.AddCert(c)
			}
			out["verify_name"] = verifyName
			if _, err := st.PeerCertificates[0].Verify(opts); err != nil {
				out["verify_ok"] = false
				out["verify_error"] = err.Error()
			} else {
				out["verify_ok"] = true
			}
		}
		return out, nil
	}
}

func certSummary(c *x509.Certificate, now time.Time) map[string]any {
	ips := make([]string, 0, len(c.IPAddresses))
	for _, ip := range c.IPAddresses {
		ips = append(ips, ip.String())
	}
	return map[string]any{
		"subject":              c.Subject.String(),
		"issuer":               c.Issuer.String(),
		"serial":               c.SerialNumber.Text(16),
		"not_before":           c.NotBefore.UTC().Format(time.RFC3339),
		"not_after":            c.NotAfter.UTC().Format(time.RFC3339),
		"dns_names":            c.DNSNames,
		"email_addresses":      c.EmailAddresses,
		"ip_addresses":         ips,
		"is_ca":                c.IsCA,
		"signature_algorithm":  c.SignatureAlgorithm.String(),
		"public_key_algorithm": c.PublicKeyAlgorithm.String(),
		"expired":              now.After(c.NotAfter),
	}
}

func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS10:
		return "TLS1.0"
	case tls.VersionTLS11:
		return "TLS1.1"
	case tls.VersionTLS12:
		return "TLS1.2"
	case tls.VersionTLS13:
		return "TLS1.3"
	default:
		return fmt.Sprintf("0x%04x", v)
	}
}

// ---------------------------------------------------------------------------
// http_headers
// ---------------------------------------------------------------------------

var securityHeaderNames = []string{
	"Strict-Transport-Security",
	"Content-Security-Policy",
	"Content-Security-Policy-Report-Only",
	"X-Content-Type-Options",
	"X-Frame-Options",
	"X-XSS-Protection",
	"Referrer-Policy",
	"Permissions-Policy",
	"Cross-Origin-Opener-Policy",
	"Cross-Origin-Resource-Policy",
	"Cross-Origin-Embedder-Policy",
	"Access-Control-Allow-Origin",
	"Access-Control-Allow-Credentials",
	"Access-Control-Allow-Methods",
	"Access-Control-Allow-Headers",
	"Server",
	"X-Powered-By",
}

func HttpHeadersSchema() Skill {
	return Skill{Name: "http_headers", Description: "Fetch an HTTP(S) URL and report status, redirect chain, response headers, cookie flags, and security-header posture.",
		Parameters: map[string]Param{
			"url":           {Type: "string", Description: "URL to request", Required: true},
			"method":        {Type: "string", Description: "HTTP method (default GET)", Required: false},
			"timeout":       {Type: "number", Description: "Timeout in seconds (default 15)", Required: false},
			"max_redirects": {Type: "number", Description: "Max redirects to follow (default 10, 0 = do not follow)", Required: false},
			"headers":       {Type: "object", Description: "Extra request headers", Required: false},
		}}
}

func HttpHeaders() Executor {
	return func(args map[string]any) (any, error) {
		rawURL := str(args, "url")
		if rawURL == "" {
			return map[string]any{"error": "url is required"}, nil
		}
		if _, err := url.ParseRequestURI(rawURL); err != nil {
			return map[string]any{"error": fmt.Sprintf("invalid url: %v", err)}, nil
		}
		method := strings.ToUpper(str(args, "method"))
		if method == "" {
			method = http.MethodGet
		}
		timeout := floatOr(args, "timeout", 15)
		if timeout <= 0 {
			timeout = 15
		}
		maxRedir := intOr(args, "max_redirects", 10)
		if maxRedir < 0 {
			maxRedir = 0
		}
		if maxRedir > 20 {
			maxRedir = 20
		}

		type hop struct {
			URL        string `json:"url"`
			Status     int    `json:"status"`
			Location   string `json:"location,omitempty"`
			StatusText string `json:"status_text,omitempty"`
		}
		// Manual redirect loop so every hop's status/Location is recorded.
		// http.Client's automatic follow only exposes the final response.
		deadline := time.Now().Add(time.Duration(timeout * float64(time.Second)))
		transport := http.DefaultTransport
		cl := &http.Client{
			Timeout: time.Until(deadline),
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Transport: transport,
		}

		current := rawURL
		var chain []hop
		var final *http.Response
		redirectsFollowed := 0

		extra := http.Header{}
		extra.Set("User-Agent", "OmegaGridAgent/1.0")
		if hdrs, ok := args["headers"].(map[string]any); ok {
			for k, v := range hdrs {
				extra.Set(k, fmt.Sprintf("%v", v))
			}
		}

		t0 := time.Now()
		for {
			if time.Now().After(deadline) {
				return map[string]any{
					"url": rawURL, "error": "timeout", "elapsed_ms": time.Since(t0).Milliseconds(),
					"redirect_chain": chain,
				}, nil
			}
			req, err := http.NewRequest(method, current, nil)
			if err != nil {
				return map[string]any{"url": rawURL, "error": err.Error(), "redirect_chain": chain}, nil
			}
			for k, vs := range extra {
				req.Header[k] = append([]string(nil), vs...)
			}
			// After the first hop, redirects are always GET per common client behaviour
			// for 301/302/303; keep method for 307/308.
			resp, err := cl.Do(req)
			if err != nil {
				return map[string]any{
					"url": rawURL, "error": err.Error(), "elapsed_ms": time.Since(t0).Milliseconds(),
					"redirect_chain": chain,
				}, nil
			}

			loc := resp.Header.Get("Location")
			chain = append(chain, hop{
				URL: current, Status: resp.StatusCode, Location: loc, StatusText: resp.Status,
			})

			if !isRedirect(resp.StatusCode) || loc == "" || redirectsFollowed >= maxRedir {
				final = resp
				break
			}
			// Consume body before next hop.
			_, _ = io.CopyN(io.Discard, resp.Body, 64*1024)
			_ = resp.Body.Close()

			next, err := resp.Request.URL.Parse(loc)
			if err != nil {
				final = resp
				// re-open? body already drained; return what we have
				return map[string]any{
					"url": rawURL, "error": "bad redirect location: " + err.Error(),
					"redirect_chain": chain, "elapsed_ms": time.Since(t0).Milliseconds(),
				}, nil
			}
			current = next.String()
			redirectsFollowed++
			if resp.StatusCode == http.StatusMovedPermanently ||
				resp.StatusCode == http.StatusFound ||
				resp.StatusCode == http.StatusSeeOther {
				method = http.MethodGet
			}
		}
		if final == nil {
			return map[string]any{"url": rawURL, "error": "no response", "redirect_chain": chain}, nil
		}
		defer final.Body.Close()
		_, _ = io.CopyN(io.Discard, final.Body, 64*1024)
		elapsed := time.Since(t0).Milliseconds()

		finalURL := current
		if final.Request != nil && final.Request.URL != nil {
			finalURL = final.Request.URL.String()
		}

		headers := flattenHeaders(final.Header)
		secPresent := map[string]string{}
		secMissing := make([]string, 0)
		for _, name := range securityHeaderNames {
			if v := final.Header.Get(name); v != "" {
				secPresent[name] = v
			} else {
				switch name {
				case "Strict-Transport-Security", "Content-Security-Policy",
					"X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy",
					"Permissions-Policy":
					secMissing = append(secMissing, name)
				}
			}
		}

		cookies := make([]map[string]any, 0)
		for _, c := range final.Cookies() {
			cookies = append(cookies, map[string]any{
				"name":      c.Name,
				"domain":    c.Domain,
				"path":      c.Path,
				"secure":    c.Secure,
				"http_only": c.HttpOnly,
				"same_site": sameSiteName(c.SameSite),
				"max_age":   c.MaxAge,
				"expires":   cookieExpires(c),
			})
		}

		return map[string]any{
			"url":              rawURL,
			"final_url":        finalURL,
			"status_code":      final.StatusCode,
			"proto":            final.Proto,
			"headers":          headers,
			"security_present": secPresent,
			"security_missing": secMissing,
			"cookies":          cookies,
			"redirect_count":   redirectsFollowed,
			"redirect_chain":   chain,
			"elapsed_ms":       elapsed,
			"content_type":     final.Header.Get("Content-Type"),
			"content_length":   final.Header.Get("Content-Length"),
		}, nil
	}
}

func isRedirect(code int) bool {
	switch code {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

func flattenHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out[k] = strings.Join(h.Values(k), ", ")
	}
	return out
}

func sameSiteName(s http.SameSite) string {
	switch s {
	case http.SameSiteDefaultMode:
		return "Default"
	case http.SameSiteLaxMode:
		return "Lax"
	case http.SameSiteStrictMode:
		return "Strict"
	case http.SameSiteNoneMode:
		return "None"
	default:
		return fmt.Sprintf("%d", s)
	}
}

func cookieExpires(c *http.Cookie) string {
	if c.Expires.IsZero() {
		return ""
	}
	return c.Expires.UTC().Format(time.RFC3339)
}

// ---------------------------------------------------------------------------
// banner_grab
// ---------------------------------------------------------------------------

func BannerGrabSchema() Skill {
	return Skill{Name: "banner_grab", Description: "TCP-connect to a host:port and read the service banner (optional probe bytes / TLS).",
		Parameters: map[string]Param{
			"host":      {Type: "string", Description: "Hostname or IP", Required: true},
			"port":      {Type: "number", Description: "TCP port", Required: true},
			"timeout":   {Type: "number", Description: "Timeout in seconds (default 5)", Required: false},
			"max_bytes": {Type: "number", Description: "Max bytes to read (default 1024, max 8192)", Required: false},
			"send":      {Type: "string", Description: "Optional bytes to send before reading (use \\r\\n for CRLF)", Required: false},
			"tls":       {Type: "boolean", Description: "Wrap the connection in TLS (default false)", Required: false},
		}}
}

func BannerGrab() Executor {
	return func(args map[string]any) (any, error) {
		host := str(args, "host")
		if host == "" {
			return map[string]any{"error": "host is required"}, nil
		}
		if !isHostname(host) {
			return map[string]any{"error": fmt.Sprintf("invalid host: %q", host)}, nil
		}
		if _, ok := numArg(args["port"]); !ok && str(args, "port") == "" {
			return map[string]any{"error": "port is required"}, nil
		}
		port := intOr(args, "port", 0)
		if port < 1 || port > 65535 {
			return map[string]any{"error": fmt.Sprintf("port %d outside 1-65535", port)}, nil
		}
		timeout := floatOr(args, "timeout", 5)
		if timeout <= 0 {
			timeout = 5
		}
		maxBytes := intOr(args, "max_bytes", 1024)
		if maxBytes <= 0 {
			maxBytes = 1024
		}
		if maxBytes > 8192 {
			maxBytes = 8192
		}
		useTLS := boolOr(args, "tls", false)
		send := expandCRLF(str(args, "send"))

		addr := net.JoinHostPort(host, strconv.Itoa(port))
		d := &net.Dialer{Timeout: time.Duration(timeout * float64(time.Second))}
		t0 := time.Now()
		raw, err := d.Dial("tcp", addr)
		if err != nil {
			return map[string]any{"host": host, "port": port, "error": err.Error()}, nil
		}
		var conn net.Conn = raw
		if useTLS {
			sni := host
			if net.ParseIP(host) != nil {
				sni = ""
			}
			tc := tls.Client(raw, &tls.Config{
				ServerName:         sni,
				InsecureSkipVerify: true, //nolint:gosec
				MinVersion:         tls.VersionTLS10,
			})
			_ = tc.SetDeadline(time.Now().Add(time.Duration(timeout * float64(time.Second))))
			if err := tc.Handshake(); err != nil {
				_ = raw.Close()
				return map[string]any{
					"host": host, "port": port, "tls": true,
					"error": err.Error(), "elapsed_ms": time.Since(t0).Milliseconds(),
				}, nil
			}
			conn = tc
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(time.Duration(timeout * float64(time.Second))))

		if send != "" {
			if _, err := io.WriteString(conn, send); err != nil {
				return map[string]any{
					"host": host, "port": port, "tls": useTLS,
					"error": "write: " + err.Error(), "elapsed_ms": time.Since(t0).Milliseconds(),
				}, nil
			}
		}

		buf := make([]byte, maxBytes)
		n, err := conn.Read(buf)
		elapsed := time.Since(t0).Milliseconds()
		if n == 0 && err != nil {
			return map[string]any{
				"host": host, "port": port, "tls": useTLS,
				"error": err.Error(), "elapsed_ms": elapsed, "bytes_read": 0,
			}, nil
		}
		data := buf[:n]
		return map[string]any{
			"host":       host,
			"port":       port,
			"tls":        useTLS,
			"banner":     sanitizeBanner(data),
			"banner_hex": hex.EncodeToString(data),
			"bytes_read": n,
			"elapsed_ms": elapsed,
			"truncated":  n == maxBytes,
		}, nil
	}
}

func expandCRLF(s string) string {
	if s == "" {
		return s
	}
	s = strings.ReplaceAll(s, `\r\n`, "\r\n")
	s = strings.ReplaceAll(s, `\n`, "\n")
	s = strings.ReplaceAll(s, `\r`, "\r")
	s = strings.ReplaceAll(s, `\t`, "\t")
	return s
}

// sanitizeBanner keeps printable text readable for the model; non-printables
// become '.' so binary protocols still show structure without breaking JSON.
func sanitizeBanner(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for _, c := range b {
		switch {
		case c == '\n' || c == '\r' || c == '\t':
			sb.WriteByte(c)
		case c >= 32 && c < 127:
			sb.WriteByte(c)
		default:
			sb.WriteByte('.')
		}
	}
	out := sb.String()
	if !utf8.ValidString(out) {
		return strings.ToValidUTF8(out, ".")
	}
	return out
}

// ---------------------------------------------------------------------------
// ptr_lookup
// ---------------------------------------------------------------------------

func PtrLookupSchema() Skill {
	return Skill{Name: "ptr_lookup", Description: "Reverse DNS (PTR) lookup for an IPv4 or IPv6 address.",
		Parameters: map[string]Param{
			"ip": {Type: "string", Description: "IPv4 or IPv6 address", Required: true},
		}}
}

func PtrLookup() Executor {
	return func(args map[string]any) (any, error) {
		ip := strings.TrimSpace(str(args, "ip"))
		if ip == "" {
			return map[string]any{"error": "ip is required"}, nil
		}
		if net.ParseIP(ip) == nil {
			return map[string]any{"error": fmt.Sprintf("invalid ip: %q", ip)}, nil
		}
		names, err := net.LookupAddr(ip)
		if err != nil {
			return map[string]any{"ip": ip, "names": []string{}, "count": 0, "error": err.Error()}, nil
		}
		clean := make([]string, 0, len(names))
		for _, n := range names {
			clean = append(clean, strings.TrimSuffix(n, "."))
		}
		return map[string]any{"ip": ip, "names": clean, "count": len(clean)}, nil
	}
}

// ---------------------------------------------------------------------------
// email_auth
// ---------------------------------------------------------------------------

func EmailAuthSchema() Skill {
	return Skill{Name: "email_auth", Description: "Look up SPF, DMARC, and optional DKIM TXT records for a domain.",
		Parameters: map[string]Param{
			"domain":         {Type: "string", Description: "Email domain (e.g. example.com)", Required: true},
			"dkim_selectors": {Type: "string", Description: fmt.Sprintf("Comma-separated DKIM selectors to probe, at most %d (e.g. default,google,selector1)", maxDKIMSelectors), Required: false},
			"timeout":        {Type: "number", Description: "Total DNS timeout in seconds for the whole lookup (default 10)", Required: false},
		}}
}

// maxDKIMSelectors bounds the DNS fan-out of a single call. The selector list
// comes from the model, and every selector costs one or two *serial* lookups —
// 60 selectors took 9s even with no network reachable at all, and minutes
// against a slow or blackholed resolver, blocking the agent step (and one of
// AGENT_MAX_PARALLEL slots) for the duration. Selectors past the cap are
// reported back rather than silently ignored.
const maxDKIMSelectors = 10

// lookupTXT is the DNS seam. net.LookupTXT takes no context, so nothing
// bounded how long email_auth could block; every lookup now shares the call's
// deadline. Tests replace it to exercise record filtering without a network.
var lookupTXT = func(ctx context.Context, name string) ([]string, error) {
	return net.DefaultResolver.LookupTXT(ctx, name)
}

func EmailAuth() Executor {
	return func(args map[string]any) (any, error) {
		domain := strings.TrimSpace(str(args, "domain"))
		if domain == "" {
			return map[string]any{"error": "domain is required"}, nil
		}
		if !isHostname(domain) {
			return map[string]any{"error": fmt.Sprintf("invalid domain: %q", domain)}, nil
		}
		domain = strings.TrimSuffix(strings.ToLower(domain), ".")

		timeout := floatOr(args, "timeout", 10)
		if timeout <= 0 {
			timeout = 10
		}
		if timeout > 60 {
			timeout = 60
		}
		// One deadline for the whole skill: SPF, DMARC and every DKIM probe
		// share it, so the call can never outlast what the caller asked for.
		ctx, cancel := context.WithTimeout(context.Background(),
			time.Duration(timeout*float64(time.Second)))
		defer cancel()

		spf, spfErr := lookupTXTFiltered(ctx, domain, "v=spf1")
		dmarcName := "_dmarc." + domain
		dmarc, dmarcErr := lookupTXTFiltered(ctx, dmarcName, "v=dmarc1")

		out := map[string]any{
			"domain": domain,
			"spf": map[string]any{
				"records": spf,
				"found":   len(spf) > 0,
				"error":   errString(spfErr),
			},
			"dmarc": map[string]any{
				"name":    dmarcName,
				"records": dmarc,
				"found":   len(dmarc) > 0,
				"error":   errString(dmarcErr),
			},
		}

		selStr := str(args, "dkim_selectors")
		if selStr != "" {
			selectors, skipped := parseDKIMSelectors(selStr)
			dkim := make([]map[string]any, 0, len(selectors))
			for _, sel := range selectors {
				name := sel + "._domainkey." + domain
				recs, err := lookupTXTFiltered(ctx, name, "v=dkim1")
				// Some publishers omit the v= tag; also accept p=.
				if len(recs) == 0 {
					all, err2 := lookupTXT(ctx, name)
					if err2 == nil {
						for _, r := range all {
							if strings.Contains(strings.ToLower(r), "p=") {
								recs = append(recs, r)
							}
						}
					}
					if err == nil {
						err = err2
					}
				}
				dkim = append(dkim, map[string]any{
					"selector": sel,
					"name":     name,
					"records":  recs,
					"found":    len(recs) > 0,
					"error":    errString(err),
				})
			}
			out["dkim"] = dkim
			if len(skipped) > 0 {
				out["dkim_skipped"] = skipped
			}
		}
		return out, nil
	}
}

func lookupTXTFiltered(ctx context.Context, name, prefix string) ([]string, error) {
	all, err := lookupTXT(ctx, name)
	if err != nil {
		return nil, err
	}
	prefix = strings.ToLower(prefix)
	var out []string
	for _, r := range all {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(r)), prefix) {
			out = append(out, r)
		}
	}
	return out, nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// parseDKIMSelectors splits the comma-separated selector argument, returning
// the selectors to probe and a reasoned list of the ones left out. Skipped
// selectors used to vanish without a trace, so the model saw fewer results than
// it asked for with nothing explaining the gap.
func parseDKIMSelectors(selStr string) (selectors []string, skipped []map[string]any) {
	skipped = make([]map[string]any, 0)
	for _, s := range strings.Split(selStr, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !isDKIMSelector(s) {
			skipped = append(skipped, map[string]any{"selector": s, "reason": "invalid selector"})
			continue
		}
		if len(selectors) >= maxDKIMSelectors {
			skipped = append(skipped, map[string]any{
				"selector": s,
				"reason":   fmt.Sprintf("over the limit of %d selectors per call", maxDKIMSelectors),
			})
			continue
		}
		selectors = append(selectors, s)
	}
	return selectors, skipped
}

// isDKIMSelector reports whether s is usable as the leftmost part of a
// "<selector>._domainkey.<domain>" query. RFC 6376 defines a selector as a
// sequence of DNS labels, so dots are legal: "s1.mail" is a valid selector and
// used to be rejected outright.
func isDKIMSelector(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
				(c >= '0' && c <= '9') || c == '-' || c == '_'
			if !ok {
				return false
			}
		}
	}
	return true
}
