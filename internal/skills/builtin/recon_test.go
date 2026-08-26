package builtin

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTlsProbeRequiresHost(t *testing.T) {
	m := runExec(t, TlsProbe(), map[string]any{})
	if m["error"] == nil {
		t.Fatal("missing host accepted")
	}
	m = runExec(t, TlsProbe(), map[string]any{"host": "-evil"})
	if m["error"] == nil {
		t.Fatal("invalid host accepted")
	}
}

func TestTlsProbeLocalServer(t *testing.T) {
	ln := startTLSListener(t)
	defer ln.Close()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	m := runExec(t, TlsProbe(), map[string]any{
		"host": "127.0.0.1", "port": port, "server_name": "tls-probe.test", "timeout": 3,
	})
	if m["error"] != nil {
		t.Fatalf("tls_probe error: %v", m["error"])
	}
	if m["tls_version"] == nil || m["tls_version"] == "" {
		t.Fatalf("no tls_version: %#v", m)
	}
	if m["cipher_suite"] == nil || m["cipher_suite"] == "" {
		t.Fatalf("no cipher_suite: %#v", m)
	}
	if n, _ := m["certificate_count"].(int); n < 1 {
		t.Fatalf("certificate_count = %v", m["certificate_count"])
	}
	sans, _ := m["leaf_dns_names"].([]string)
	found := false
	for _, s := range sans {
		if s == "tls-probe.test" {
			found = true
		}
	}
	if !found {
		t.Fatalf("leaf_dns_names missing tls-probe.test: %v", sans)
	}
	// Self-signed → verify should fail against system roots.
	if m["verify_ok"] != false {
		t.Fatalf("verify_ok = %v, want false for self-signed", m["verify_ok"])
	}
}

func TestHttpHeadersRequiresURL(t *testing.T) {
	m := runExec(t, HttpHeaders(), map[string]any{})
	if m["error"] == nil {
		t.Fatal("missing url accepted")
	}
}

func TestHttpHeadersRedirectAndSecurity(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "1", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/go", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	m := runExec(t, HttpHeaders(), map[string]any{"url": srv.URL + "/go", "timeout": 5})
	if m["error"] != nil {
		t.Fatalf("http_headers error: %v", m["error"])
	}
	if m["status_code"] != 200 {
		t.Fatalf("status_code = %v", m["status_code"])
	}
	if m["redirect_count"] != 1 {
		t.Fatalf("redirect_count = %v, want 1", m["redirect_count"])
	}
	if n := sliceLen(m["redirect_chain"]); n != 2 {
		t.Fatalf("redirect_chain len = %d (%T), want 2", n, m["redirect_chain"])
	}
	// Round-trip through JSON so nested hop structs are easy to inspect.
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	present, _ := decoded["security_present"].(map[string]any)
	if present["X-Frame-Options"] != "DENY" {
		t.Fatalf("security_present = %#v", present)
	}
	missing, _ := decoded["security_missing"].([]any)
	for _, name := range missing {
		if name == "X-Frame-Options" {
			t.Fatal("X-Frame-Options listed as missing")
		}
	}
	cookies, _ := decoded["cookies"].([]any)
	if len(cookies) != 1 {
		t.Fatalf("cookies = %#v", decoded["cookies"])
	}
	c0, _ := cookies[0].(map[string]any)
	if c0["name"] != "sid" || c0["http_only"] != true {
		t.Fatalf("cookie = %#v", c0)
	}
}

func TestHttpHeadersNoFollow(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusMovedPermanently)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	m := runExec(t, HttpHeaders(), map[string]any{
		"url": srv.URL + "/", "max_redirects": 0, "timeout": 5,
	})
	if m["error"] != nil {
		t.Fatalf("error: %v", m["error"])
	}
	if m["status_code"] != 301 {
		t.Fatalf("status_code = %v, want 301", m["status_code"])
	}
	if m["redirect_count"] != 0 {
		t.Fatalf("redirect_count = %v", m["redirect_count"])
	}
}

func TestBannerGrab(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = c.Write([]byte("220 mail.example.com ESMTP ready\r\n"))
	}()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())

	m := runExec(t, BannerGrab(), map[string]any{
		"host": "127.0.0.1", "port": portStr, "timeout": 2,
	})
	if m["error"] != nil {
		t.Fatalf("banner_grab error: %v", m["error"])
	}
	banner, _ := m["banner"].(string)
	if !strings.Contains(banner, "ESMTP") {
		t.Fatalf("banner = %q", banner)
	}
	if m["bytes_read"].(int) < 10 {
		t.Fatalf("bytes_read = %v", m["bytes_read"])
	}

	// Probe with send (HTTP-ish).
	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln2.Close()
	go func() {
		c, err := ln2.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 256)
		_, _ = c.Read(buf)
		_, _ = c.Write([]byte("HTTP/1.0 200 OK\r\nServer: test\r\n\r\n"))
	}()
	_, p2, _ := net.SplitHostPort(ln2.Addr().String())
	m = runExec(t, BannerGrab(), map[string]any{
		"host": "127.0.0.1", "port": p2, "timeout": 2,
		"send": "HEAD / HTTP/1.0\\r\\n\\r\\n",
	})
	if m["error"] != nil {
		t.Fatalf("banner with send: %v", m["error"])
	}
	if !strings.Contains(m["banner"].(string), "HTTP/1.0 200") {
		t.Fatalf("banner = %q", m["banner"])
	}

	m = runExec(t, BannerGrab(), map[string]any{"host": "127.0.0.1"})
	if m["error"] == nil {
		t.Fatal("missing port accepted")
	}
}

func TestPtrLookupValidation(t *testing.T) {
	m := runExec(t, PtrLookup(), map[string]any{})
	if m["error"] == nil {
		t.Fatal("missing ip accepted")
	}
	m = runExec(t, PtrLookup(), map[string]any{"ip": "not-an-ip"})
	if m["error"] == nil {
		t.Fatal("invalid ip accepted")
	}
	// 127.0.0.1 usually has a PTR in local resolver environments; either
	// names or a DNS error is fine — just must not panic and must be JSON-shaped.
	m = runExec(t, PtrLookup(), map[string]any{"ip": "127.0.0.1"})
	if m["ip"] != "127.0.0.1" {
		t.Fatalf("ip = %v", m["ip"])
	}
	if _, ok := m["count"]; !ok {
		t.Fatalf("missing count: %#v", m)
	}
}

func TestEmailAuthValidation(t *testing.T) {
	m := runExec(t, EmailAuth(), map[string]any{})
	if m["error"] == nil {
		t.Fatal("missing domain accepted")
	}
	m = runExec(t, EmailAuth(), map[string]any{"domain": "-bad"})
	if m["error"] == nil {
		t.Fatal("invalid domain accepted")
	}
}

func TestEmailAuthLookup(t *testing.T) {
	// Driven through the DNS seam rather than public DNS: the suite is meant to
	// run with no network at all (see DESIGN §10), and a resolver-dependent test
	// reports whatever the day's DNS says.
	withFakeTXT(t, func(_ context.Context, _ string) ([]string, error) {
		return nil, fmt.Errorf("no such host")
	})
	m := runExec(t, EmailAuth(), map[string]any{
		"domain": "example.com", "dkim_selectors": "default,google",
	})
	if m["error"] != nil {
		t.Fatalf("top-level error: %v", m["error"])
	}
	if m["domain"] != "example.com" {
		t.Fatalf("domain = %v", m["domain"])
	}
	spf, ok := m["spf"].(map[string]any)
	if !ok {
		t.Fatalf("spf = %#v", m["spf"])
	}
	if spf["found"] != false {
		t.Fatalf("spf.found = %v, want false when the domain does not resolve", spf["found"])
	}
	if spf["error"] == "" {
		t.Fatalf("spf.error should carry the resolver failure: %#v", spf)
	}
	dmarc, ok := m["dmarc"].(map[string]any)
	if !ok {
		t.Fatalf("dmarc = %#v", m["dmarc"])
	}
	if dmarc["name"] != "_dmarc.example.com" {
		t.Fatalf("dmarc.name = %v", dmarc["name"])
	}
	if n := sliceLen(m["dkim"]); n != 2 {
		t.Fatalf("dkim len = %d (%T)", n, m["dkim"])
	}
}

func TestSanitizeBannerAndExpandCRLF(t *testing.T) {
	if got := expandCRLF(`HEAD / HTTP/1.0\r\n\r\n`); got != "HEAD / HTTP/1.0\r\n\r\n" {
		t.Fatalf("expandCRLF = %q", got)
	}
	raw := []byte("hi\x00\xffthere")
	s := sanitizeBanner(raw)
	if !strings.Contains(s, "hi") || !strings.Contains(s, "there") {
		t.Fatalf("sanitize = %q", s)
	}
	if strings.ContainsRune(s, 0) {
		t.Fatal("null byte survived sanitize")
	}
}

func sliceLen(v any) int {
	if v == nil {
		return 0
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice {
		return -1
	}
	return rv.Len()
}

// startTLSListener returns a TLS listener serving a short-lived self-signed
// cert for tls-probe.test.
func startTLSListener(t *testing.T) net.Listener {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "tls-probe.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(2 * time.Hour),
		DNSNames:     []string{"tls-probe.test"},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				if tc, ok := c.(*tls.Conn); ok {
					_ = tc.Handshake()
				}
				time.Sleep(50 * time.Millisecond)
			}(c)
		}
	}()
	return ln
}

// withFakeTXT swaps the DNS seam for the duration of a test so record parsing
// is covered without touching the network.
func withFakeTXT(t *testing.T, fn func(ctx context.Context, name string) ([]string, error)) {
	t.Helper()
	prev := lookupTXT
	lookupTXT = fn
	t.Cleanup(func() { lookupTXT = prev })
}

func TestEmailAuthParsesRecords(t *testing.T) {
	withFakeTXT(t, func(_ context.Context, name string) ([]string, error) {
		switch name {
		case "example.com":
			return []string{"some-verification=abc", "v=spf1 include:_spf.example.net ~all"}, nil
		case "_dmarc.example.com":
			return []string{"v=DMARC1; p=reject; rua=mailto:d@example.com"}, nil
		case "sel1._domainkey.example.com":
			return []string{"v=DKIM1; k=rsa; p=MIIBIj"}, nil
		case "s1.mail._domainkey.example.com":
			// Publisher omitting the v= tag — accepted via the p= fallback.
			return []string{"k=rsa; p=MIGfMA0G"}, nil
		}
		return nil, fmt.Errorf("no such host")
	})

	m := runExec(t, EmailAuth(), map[string]any{
		"domain": "example.com", "dkim_selectors": "sel1,s1.mail,missing",
	})

	spf, _ := m["spf"].(map[string]any)
	if spf["found"] != true {
		t.Fatalf("spf = %#v", spf)
	}
	if recs, _ := spf["records"].([]string); len(recs) != 1 || !strings.HasPrefix(recs[0], "v=spf1") {
		t.Fatalf("spf records = %#v (the non-SPF TXT must be filtered out)", spf["records"])
	}
	dmarc, _ := m["dmarc"].(map[string]any)
	if dmarc["found"] != true {
		t.Fatalf("dmarc = %#v", dmarc)
	}

	dkim, _ := m["dkim"].([]map[string]any)
	if len(dkim) != 3 {
		t.Fatalf("dkim = %#v, want 3 entries", dkim)
	}
	byName := map[string]map[string]any{}
	for _, d := range dkim {
		byName[d["selector"].(string)] = d
	}
	// A dotted selector is legal per RFC 6376 and used to be dropped silently.
	if d := byName["s1.mail"]; d == nil || d["found"] != true {
		t.Fatalf("dotted selector not probed: %#v", byName)
	}
	if d := byName["sel1"]; d == nil || d["found"] != true {
		t.Fatalf("sel1 = %#v", d)
	}
	if d := byName["missing"]; d == nil || d["found"] != false {
		t.Fatalf("missing selector should report found=false: %#v", d)
	}
	if _, ok := m["dkim_skipped"]; ok {
		t.Fatalf("nothing should have been skipped: %v", m["dkim_skipped"])
	}
}

// The selector list comes from the model: it must be capped, and anything left
// out must be reported instead of silently disappearing.
func TestEmailAuthCapsAndReportsSelectors(t *testing.T) {
	var queried int
	withFakeTXT(t, func(_ context.Context, name string) ([]string, error) {
		if strings.Contains(name, "_domainkey") {
			queried++
		}
		return nil, fmt.Errorf("no such host")
	})

	var sels []string
	for i := 0; i < maxDKIMSelectors+5; i++ {
		sels = append(sels, fmt.Sprintf("sel%d", i))
	}
	sels = append(sels, "bad selector!")

	m := runExec(t, EmailAuth(), map[string]any{
		"domain": "example.com", "dkim_selectors": strings.Join(sels, ","),
	})

	dkim, _ := m["dkim"].([]map[string]any)
	if len(dkim) != maxDKIMSelectors {
		t.Fatalf("probed %d selectors, want the cap of %d", len(dkim), maxDKIMSelectors)
	}
	skipped, _ := m["dkim_skipped"].([]map[string]any)
	if len(skipped) != 6 {
		t.Fatalf("dkim_skipped = %#v, want 5 over-limit + 1 invalid", skipped)
	}
	var sawInvalid, sawLimit bool
	for _, s := range skipped {
		reason, _ := s["reason"].(string)
		if strings.Contains(reason, "invalid") {
			sawInvalid = true
		}
		if strings.Contains(reason, "limit") {
			sawLimit = true
		}
	}
	if !sawInvalid || !sawLimit {
		t.Fatalf("skip reasons incomplete: %#v", skipped)
	}
	// Two lookups per selector at most (v=DKIM1 filter, then the p= fallback).
	if queried > 2*maxDKIMSelectors {
		t.Fatalf("%d DKIM queries issued, want at most %d", queried, 2*maxDKIMSelectors)
	}
}

// Every lookup shares one deadline, so the whole skill is bounded no matter how
// slow the resolver is.
func TestEmailAuthHonoursTimeout(t *testing.T) {
	withFakeTXT(t, func(ctx context.Context, _ string) ([]string, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
			return nil, fmt.Errorf("slow resolver")
		}
	})

	t0 := time.Now()
	m := runExec(t, EmailAuth(), map[string]any{
		"domain": "example.com", "dkim_selectors": "a,b,c,d,e", "timeout": 0.5,
	})
	elapsed := time.Since(t0)
	if elapsed > 3*time.Second {
		t.Fatalf("email_auth ran for %v with a 0.5s timeout", elapsed)
	}
	if m["domain"] != "example.com" {
		t.Fatalf("domain = %v", m["domain"])
	}
}

func TestIsDKIMSelector(t *testing.T) {
	valid := []string{"default", "google", "s1.mail", "selector1", "k1_2", "a-b"}
	for _, s := range valid {
		if !isDKIMSelector(s) {
			t.Errorf("isDKIMSelector(%q) = false, want true", s)
		}
	}
	invalid := []string{"", ".", "a..b", "a.", ".a", "bad selector", "a/b", "a$b", strings.Repeat("x", 64)}
	for _, s := range invalid {
		if isDKIMSelector(s) {
			t.Errorf("isDKIMSelector(%q) = true, want false", s)
		}
	}
}

// Probing an IP with no SNI must still name-check the certificate against that
// IP; an empty DNSName made Verify skip the check and report verify_ok for a
// certificate covering an unrelated host.
func TestTlsProbeVerifiesAgainstTheIPWhenThereIsNoSNI(t *testing.T) {
	ln := startTLSListener(t)
	defer ln.Close()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	m := runExec(t, TlsProbe(), map[string]any{"host": "127.0.0.1", "port": port, "timeout": 3})
	if m["error"] != nil {
		t.Fatalf("tls_probe error: %v", m["error"])
	}
	if m["verify_name"] != "127.0.0.1" {
		t.Fatalf("verify_name = %v, want the dialed IP", m["verify_name"])
	}
	if m["verify_ok"] != false {
		t.Fatalf("verify_ok = %v: the test cert carries no IP SAN for 127.0.0.1", m["verify_ok"])
	}
	if s, _ := m["verify_error"].(string); s == "" {
		t.Fatal("verify_error should explain the failure")
	}
}
