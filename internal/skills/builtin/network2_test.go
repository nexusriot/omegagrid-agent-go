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
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/textproto"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestTracerouteLoopback(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}
	m := runExec(t, Traceroute(), map[string]any{"host": "127.0.0.1", "max_hops": 3})
	if m["error"] != nil {
		t.Fatalf("traceroute = %v", m)
	}
	hops := m["hops"].([]hopResult)
	if m["reached"] != true || len(hops) != 1 || hops[0].Address != "127.0.0.1" {
		t.Fatalf("loopback trace = %+v", m)
	}
	if m := runExec(t, Traceroute(), map[string]any{"host": "-f/etc/passwd"}); m["error"] == nil {
		t.Fatal("option-looking host accepted")
	}
	withPrivateNetworks(t, false)
	if m := runExec(t, Traceroute(), map[string]any{"host": "127.0.0.1"}); m["error"] == nil {
		t.Fatal("loopback traced with private networks blocked")
	}
}

func TestHttpTiming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redir" {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
			return
		}
		time.Sleep(30 * time.Millisecond)
		_, _ = w.Write([]byte(strings.Repeat("x", 1000)))
	}))
	defer srv.Close()
	m := runExec(t, HttpTiming(), map[string]any{"url": srv.URL + "/"})
	if m["status"] != 200 || m["bytes"].(int64) != 1000 {
		t.Fatalf("timing = %v", m)
	}
	if ttfb := m["ttfb_ms"].(float64); ttfb < 25 {
		t.Fatalf("ttfb_ms = %v, the server slept 30ms", ttfb)
	}
	if m["connect_ms"].(float64) <= 0 || m["total_ms"].(float64) < m["ttfb_ms"].(float64) {
		t.Fatalf("phases = %v", m)
	}
	m = runExec(t, HttpTiming(), map[string]any{"url": srv.URL + "/redir"})
	if m["status"] != 302 || m["location"] != "/elsewhere" {
		t.Fatalf("redirect not reported: %v", m)
	}
	m = runExec(t, HttpTiming(), map[string]any{"url": srv.URL + "/", "samples": 3})
	if len(m["samples"].([]map[string]any)) != 3 || m["total_ms_median"] == nil {
		t.Fatalf("samples = %v", m)
	}
	if m := runExec(t, HttpTiming(), map[string]any{"url": srv.URL, "method": "POST"}); m["error"] == nil {
		t.Fatal("POST accepted")
	}
}

// fakeDNS answers A queries from table over UDP on 127.0.0.1.
func fakeDNS(t *testing.T, table map[string]string) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var p dnsmessage.Parser
			h, err := p.Start(buf[:n])
			if err != nil {
				continue
			}
			q, err := p.Question()
			if err != nil {
				continue
			}
			b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, RecursionAvailable: true})
			_ = b.StartQuestions()
			_ = b.Question(q)
			_ = b.StartAnswers()
			if ip, ok := table[q.Name.String()]; ok && q.Type == dnsmessage.TypeA {
				a := netip.MustParseAddr(ip).As4()
				_ = b.AResource(dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, TTL: 60}, dnsmessage.AResource{A: a})
			}
			msg, _ := b.Finish()
			_, _ = pc.WriteTo(msg, addr)
		}
	}()
	return pc.LocalAddr().String()
}

func TestDnsCompare(t *testing.T) {
	a := fakeDNS(t, map[string]string{"app.example.com.": "10.0.0.1"})
	b := fakeDNS(t, map[string]string{"app.example.com.": "10.0.0.2"})
	c := fakeDNS(t, map[string]string{"app.example.com.": "10.0.0.1"})
	saved := lookupNS
	lookupNS = func(context.Context, string) ([]*net.NS, error) { return nil, nil }
	defer func() { lookupNS = saved }()

	m := runExec(t, DnsCompare(), map[string]any{"name": "app.example.com", "resolvers": []any{a, b, c}})
	if m["consistent"] != false {
		t.Fatalf("disagreement not flagged: %v", m)
	}
	distinct := m["distinct_answers"].([]map[string]any)
	if len(distinct) != 2 || distinct[0]["answer"] != "10.0.0.1" || len(distinct[0]["resolvers"].([]string)) != 2 {
		t.Fatalf("distinct = %v", distinct)
	}
	m = runExec(t, DnsCompare(), map[string]any{"name": "app.example.com", "resolvers": []any{a, c}})
	if m["consistent"] != true {
		t.Fatalf("agreeing resolvers flagged: %v", m)
	}
	for _, bad := range []map[string]any{
		{"name": "x.example", "resolvers": []any{"dns.google"}},
		{"name": "x.example", "record_type": "AXFR"},
		{"name": "@evil"},
	} {
		if m := runExec(t, DnsCompare(), bad); m["error"] == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}

func TestRdapLookup(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch {
		case strings.HasPrefix(r.URL.Path, "/domain/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"objectClassName": "domain", "ldhName": "EXAMPLE.COM", "status": []string{"active"},
				"events":      []map[string]any{{"eventAction": "registration", "eventDate": "1995-08-14T04:00:00Z"}, {"eventAction": "expiration", "eventDate": "2027-08-13T04:00:00Z"}},
				"nameservers": []map[string]any{{"ldhName": "A.IANA-SERVERS.NET"}},
				"entities":    []map[string]any{{"roles": []string{"registrar"}, "vcardArray": []any{"vcard", []any{[]any{"version", map[string]any{}, "text", "4.0"}, []any{"fn", map[string]any{}, "text", "RESERVED-IANA"}}}}},
			})
		case strings.HasPrefix(r.URL.Path, "/ip/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"objectClassName": "ip network", "startAddress": "8.8.8.0", "endAddress": "8.8.8.255", "country": "US"})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	saved := rdapBase
	rdapBase = srv.URL
	defer func() { rdapBase = saved }()

	m := runExec(t, RdapLookup(5), map[string]any{"query": "Example.com"})
	ev := m["events"].(map[string]any)
	ents := m["entities"].([]map[string]any)
	if m["kind"] != "domain" || ev["expiration"] != "2027-08-13T04:00:00Z" || m["nameservers"].([]string)[0] != "a.iana-servers.net" || ents[0]["name"] != "RESERVED-IANA" {
		t.Fatalf("domain = %v", m)
	}
	if m := runExec(t, RdapLookup(5), map[string]any{"query": "8.8.8.8"}); m["country"] != "US" {
		t.Fatalf("ip = %v", m)
	}
	if m := runExec(t, RdapLookup(5), map[string]any{"query": "AS15169"}); !strings.Contains(m["error"].(string), "not found") {
		t.Fatalf("asn 404 = %v", m)
	}
	if paths[2] != "/autnum/15169" {
		t.Fatalf("asn path = %v", paths)
	}
	if m := runExec(t, RdapLookup(5), map[string]any{"query": "../../etc"}); m["error"] == nil {
		t.Fatal("garbage query accepted")
	}
}

func selfSignedTLS(t *testing.T, name string) tls.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(30 * 24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// fakeMX speaks just enough ESMTP for smtp_check, with optional STARTTLS.
func fakeMX(t *testing.T, starttls bool) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	cert := selfSignedTLS(t, "mx.test")
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				tp := textproto.NewConn(c)
				_ = tp.PrintfLine("220 mx.test ESMTP ready")
				for {
					line, err := tp.ReadLine()
					if err != nil {
						return
					}
					switch strings.ToUpper(strings.Fields(line + " x")[0]) {
					case "EHLO":
						if starttls {
							_ = tp.PrintfLine("250-mx.test\r\n250-SIZE 1000\r\n250 STARTTLS")
						} else {
							_ = tp.PrintfLine("250-mx.test\r\n250 SIZE 1000")
						}
					case "STARTTLS":
						_ = tp.PrintfLine("220 go ahead")
						tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{cert}})
						_ = tc.Handshake()
						_, _ = textproto.NewConn(tc).ReadLine()
						return
					default:
						_ = tp.PrintfLine("221 bye")
						return
					}
				}
			}(c)
		}
	}()
	_, p, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(p)
	return port
}

func TestSmtpCheck(t *testing.T) {
	port := fakeMX(t, true)
	m := runExec(t, SmtpCheck(), map[string]any{"host": "127.0.0.1", "port": port})
	h := m["hosts"].([]map[string]any)[0]
	if h["banner"] != "mx.test ESMTP ready" || h["starttls"] != true || h["tls_version"] == nil || h["cert_subject"] != "mx.test" {
		t.Fatalf("starttls host = %v", h)
	}
	if h["cert_verify_ok"] != false || h["cert_verify_error"] == nil || h["cert_days_left"].(int) < 28 {
		t.Fatalf("self-signed cert must fail verification and report expiry: %v", h)
	}

	plain := fakeMX(t, false)
	savedMX := lookupMX
	lookupMX = func(context.Context, string) ([]*net.MX, error) {
		return []*net.MX{{Host: "127.0.0.1.", Pref: 20}, {Host: "localhost.", Pref: 10}}, nil
	}
	defer func() { lookupMX = savedMX }()
	m = runExec(t, SmtpCheck(), map[string]any{"domain": "example.com", "port": plain, "max_hosts": 1})
	hosts := m["hosts"].([]map[string]any)
	if len(hosts) != 1 || hosts[0]["host"] != "localhost" || hosts[0]["starttls"] != false {
		t.Fatalf("MX path = %v", m)
	}
	if mx := m["mx"].([]string); mx[0] != "10 localhost" {
		t.Fatalf("mx sorted by preference = %v", mx)
	}
	if m := runExec(t, SmtpCheck(), map[string]any{}); m["error"] == nil {
		t.Fatal("no target accepted")
	}
}

func TestWakeOnLan(t *testing.T) {
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	port := pc.LocalAddr().(*net.UDPAddr).Port
	m := runExec(t, WakeOnLan(), map[string]any{"mac": "00:11:22:33:44:55", "broadcast": "127.0.0.1", "port": port})
	if m["sent"] != true {
		t.Fatalf("wol = %v", m)
	}
	buf := make([]byte, 200)
	_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := pc.ReadFrom(buf)
	if err != nil || n != 102 {
		t.Fatalf("read %d bytes: %v", n, err)
	}
	if string(buf[:6]) != "\xff\xff\xff\xff\xff\xff" || string(buf[6:12]) != "\x00\x11\x22\x33\x44\x55" || string(buf[96:102]) != "\x00\x11\x22\x33\x44\x55" {
		t.Fatalf("magic packet = %x", buf[:n])
	}
	for _, bad := range []map[string]any{{"mac": "nope"}, {"mac": "00:11:22:33:44:55", "broadcast": "8.8.8.8"}} {
		if m := runExec(t, WakeOnLan(), bad); m["error"] == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	withPrivateNetworks(t, false)
	if m := runExec(t, WakeOnLan(), map[string]any{"mac": "00:11:22:33:44:55"}); m["error"] == nil {
		t.Fatal("sent with private networks blocked")
	}
}

func TestSubnetSweep(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	m := runExec(t, SubnetSweep(), map[string]any{"cidr": "127.0.0.0/30", "ports": port})
	alive := m["alive"]
	b, _ := json.Marshal(alive)
	if m["scanned"] != 2 || !strings.Contains(string(b), `"ip":"127.0.0.1","open_ports":[`+port+`]`) {
		t.Fatalf("sweep = %v", m)
	}
	for _, bad := range []map[string]any{{"cidr": "10.0.0.0/16"}, {"cidr": "nope"}, {"cidr": "10.0.0.0/30", "ports": "1-20"}} {
		if m := runExec(t, SubnetSweep(), bad); m["error"] == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	withPrivateNetworks(t, false)
	if m := runExec(t, SubnetSweep(), map[string]any{"cidr": "127.0.0.0/30", "ports": port}); m["alive_count"] != 0 || m["blocked"] != 2 {
		t.Fatalf("policy not applied: %v", m)
	}
}
