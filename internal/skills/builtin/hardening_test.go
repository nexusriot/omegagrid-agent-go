package builtin

import (
	"crypto/rand"
	"errors"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nexusriot/omegagrid-agent-go/internal/skills/netguard"
)

func withPrivateNetworks(t *testing.T, allow bool) {
	t.Helper()
	prev := netguard.AllowPrivate()
	netguard.SetAllowPrivate(allow)
	t.Cleanup(func() { netguard.SetAllowPrivate(prev) })
}

func errOf(m map[string]any) string { s, _ := m["error"].(string); return s }

func TestHttpSkillsRejectNonHTTPSchemes(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", "gopher://127.0.0.1:6379/_x", "ftp://example.com/", "//example.com"} {
		if e := errOf(runExec(t, HttpRequest(5), map[string]any{"url": u})); e == "" {
			t.Errorf("http_request accepted %q", u)
		}
		if e := errOf(runExec(t, WebScrape(5), map[string]any{"url": u})); e == "" {
			t.Errorf("web_scrape accepted %q", u)
		}
		if e := errOf(runExec(t, HttpHealth(), map[string]any{"url": u})); e == "" {
			t.Errorf("http_health accepted %q", u)
		}
	}
}

// Cloud metadata is refused even when private networks are allowed, including
// when reached through a redirect from an allowed URL.
func TestHttpRequestBlocksMetadataViaRedirect(t *testing.T) {
	withPrivateNetworks(t, true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer srv.Close()

	if e := errOf(runExec(t, HttpRequest(5), map[string]any{"url": srv.URL})); !strings.Contains(e, "blocked") {
		t.Fatalf("http_request followed redirect to metadata: error=%q", e)
	}
	if e := errOf(runExec(t, HttpHeaders(), map[string]any{"url": "http://169.254.169.254/"})); !strings.Contains(e, "blocked") {
		t.Fatalf("http_headers reached metadata: error=%q", e)
	}
}

func TestOutboundSkillsHonourPrivateNetworkPolicy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer srv.Close()
	host, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	withPrivateNetworks(t, true)
	if m := runExec(t, HttpRequest(5), map[string]any{"url": srv.URL}); m["status_code"] != 200 {
		t.Fatalf("loopback blocked with private networks allowed: %#v", m)
	}

	netguard.SetAllowPrivate(false)
	if e := errOf(runExec(t, HttpRequest(5), map[string]any{"url": srv.URL})); !strings.Contains(e, "blocked") {
		t.Errorf("http_request reached loopback: %q", e)
	}
	if e := errOf(runExec(t, WebScrape(5), map[string]any{"url": srv.URL})); !strings.Contains(e, "blocked") {
		t.Errorf("web_scrape reached loopback: %q", e)
	}
	if e := errOf(runExec(t, PortScan(), map[string]any{"host": host, "ports": port})); !strings.Contains(e, "blocked") {
		t.Errorf("port_scan scanned loopback: %q", e)
	}
	if m := runExec(t, PingCheck(), map[string]any{"host": host, "port": port}); m["reachable"] != false || !strings.Contains(errOf(m), "blocked") {
		t.Errorf("ping_check reached loopback: %#v", m)
	}
	if e := errOf(runExec(t, BannerGrab(), map[string]any{"host": host, "port": port})); !strings.Contains(e, "blocked") {
		t.Errorf("banner_grab reached loopback: %q", e)
	}
	if e := errOf(runExec(t, TlsProbe(), map[string]any{"host": host, "port": port})); !strings.Contains(e, "blocked") {
		t.Errorf("tls_probe reached loopback: %q", e)
	}
}

func TestHttpRequestCapsResponseBody(t *testing.T) {
	withPrivateNetworks(t, true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("я", maxBodyBytes))) // 2 bytes per rune: 2x the cap
	}))
	defer srv.Close()

	m := runExec(t, HttpRequest(10), map[string]any{"url": srv.URL})
	body, _ := m["body"].(string)
	if m["truncated"] != true || len(body) > maxBodyBytes || !utf8.ValidString(body) {
		t.Fatalf("truncated=%v len=%d valid=%v", m["truncated"], len(body), utf8.ValidString(body))
	}
	h := runExec(t, HttpHealth(), map[string]any{"url": srv.URL})
	if h["body_truncated"] != true || h["content_length"].(int) > maxBodyBytes {
		t.Fatalf("http_health read uncapped: %#v", h)
	}
}

func TestClampTimeout(t *testing.T) {
	cases := []struct{ in, want float64 }{
		{5, 5}, {0, 10}, {-3, 10}, {math.NaN(), 10}, {1e9, maxTimeoutSec}, {math.Inf(1), maxTimeoutSec},
	}
	for _, c := range cases {
		if got := clampTimeout(c.in, 10, maxTimeoutSec); got != c.want {
			t.Errorf("clampTimeout(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	for in, want := range map[float64]float64{0: 30, 0.2: 1, 5: 5, 1e6: maxCommandTimeoutSec} {
		if got := clampCommandTimeout(in); got != want {
			t.Errorf("clampCommandTimeout(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestTrimPartialRune(t *testing.T) {
	b := []byte("aé€😀")
	for cut := 0; cut <= len(b); cut++ {
		got := trimPartialRune(b[:cut])
		if !utf8.Valid(got) {
			t.Errorf("cut %d: %q not valid UTF-8", cut, got)
		}
		if utf8.Valid(b[:cut]) && len(got) != cut {
			t.Errorf("cut %d: trimmed a complete string to %q", cut, got)
		}
	}
}

// The timeout must kill the whole process group: `sleep | cat` used to leave
// sleep holding stdout open, so Run never returned.
func TestShellCommandTimeoutKillsProcessGroup(t *testing.T) {
	start := time.Now()
	m := runExec(t, ShellCommand(true), map[string]any{"command": "sleep 30 | cat", "timeout": 1})
	if el := time.Since(start); el > 10*time.Second {
		t.Fatalf("shell_command took %v despite timeout=1", el)
	}
	if !strings.Contains(errOf(m), "timed out") {
		t.Errorf("no timeout reported: %#v", m)
	}
}

func TestShellCommandCapsOutput(t *testing.T) {
	m := runExec(t, ShellCommand(true), map[string]any{"command": "head -c 1000000 /dev/zero | tr '\\0' a"})
	out, _ := m["stdout"].(string)
	if len(out) != maxCommandOutput || m["stdout_truncated"] != true {
		t.Fatalf("stdout len=%d truncated=%v", len(out), m["stdout_truncated"])
	}
	if m["exit_code"] != 0 {
		t.Errorf("capped writer broke the producer: exit_code=%v", m["exit_code"])
	}
}

// A server that accepts TCP and never speaks SSH used to hold ssh_command
// forever: ClientConfig.Timeout covers only the connect.
func TestSshCommandHandshakeDeadline(t *testing.T) {
	withPrivateNetworks(t, true)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close() // hold it open, say nothing
		}
	}()
	prev := sshHandshakeTimeout
	sshHandshakeTimeout = 300 * time.Millisecond
	t.Cleanup(func() { sshHandshakeTimeout = prev })

	host, port, _ := net.SplitHostPort(ln.Addr().String())
	start := time.Now()
	m := runExec(t, SshCommand(SSHConfig{Enabled: true}), map[string]any{"host": host, "port": port, "command": "true"})
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("ssh handshake not bounded: took %v", el)
	}
	if errOf(m) == "" {
		t.Fatalf("expected handshake error, got %#v", m)
	}
}

func TestQrGenerateCapsImageSize(t *testing.T) {
	m := runExec(t, QrGenerate(), map[string]any{
		"data": strings.Repeat("x", 1200), "box_size": 40, "border": 20, "error_correction": "H",
	})
	if errOf(m) != "" {
		t.Fatal(errOf(m))
	}
	if px := m["image_size_px"].(int); px > maxQRImagePx {
		t.Fatalf("image_size_px = %d, want <= %d", px, maxQRImagePx)
	}
	if bs := m["box_size"].(int); bs >= 40 {
		t.Errorf("box_size not reduced: %d", bs)
	}
}

func TestWhoisRejectsInjection(t *testing.T) {
	for _, d := range []string{"example.com\r\nhelp", "example.com\n", "-h example.com", "a b.com"} {
		if errOf(runExec(t, WhoisLookup(), map[string]any{"domain": d})) == "" {
			t.Errorf("whois accepted %q", d)
		}
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("entropy unavailable") }

func TestPasswordGenRandomFailure(t *testing.T) {
	prev := rand.Reader
	rand.Reader = failingReader{}
	t.Cleanup(func() { rand.Reader = prev })
	m := runExec(t, PasswordGen(), map[string]any{})
	if !strings.Contains(errOf(m), "random") {
		t.Fatalf("expected random-source error, got %#v", m)
	}
}
