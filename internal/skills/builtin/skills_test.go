package builtin

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// allSchemas is every schema the registry exposes to the LLM.
func allSchemas() []Skill {
	return []Skill{
		WeatherSchema(), HttpRequestSchema(), WebScrapeSchema(), HttpHealthSchema(), IpInfoSchema(),
		DnsLookupSchema(), PingCheckSchema(), PortScanSchema(), WhoisLookupSchema(),
		TlsProbeSchema(), HttpHeadersSchema(), BannerGrabSchema(), PtrLookupSchema(), EmailAuthSchema(),
		Base64Schema(), HashSchema(), UuidGenSchema(), PasswordGenSchema(), CidrCalcSchema(), JwtInspectSchema(),
		DateTimeSchema(), MathEvalSchema(), CronScheduleSchema(), ReminderSchema(),
		ShellCommandSchema(), SshCommandSchema(), QrGenerateSchema(),
	}
}

// The schema is the tool's entire contract with the model: a missing
// description or an untyped parameter directly degrades tool selection.
func TestEverySchemaIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range allSchemas() {
		if s.Name == "" {
			t.Fatalf("schema with no name: %+v", s)
		}
		if seen[s.Name] {
			t.Fatalf("duplicate skill name %q", s.Name)
		}
		seen[s.Name] = true

		if s.Name != strings.ToLower(s.Name) || strings.ContainsAny(s.Name, " -.") {
			t.Fatalf("skill name %q is not a plain lowercase identifier", s.Name)
		}
		if len(s.Description) < 10 {
			t.Fatalf("skill %q has a uselessly short description: %q", s.Name, s.Description)
		}
		for pname, p := range s.Parameters {
			if pname == "" {
				t.Fatalf("skill %q has an unnamed parameter", s.Name)
			}
			if p.Type == "" {
				t.Fatalf("skill %q parameter %q has no type", s.Name, pname)
			}
			switch p.Type {
			case "string", "number", "integer", "boolean", "object", "array":
			default:
				t.Fatalf("skill %q parameter %q has non-JSON-Schema type %q", s.Name, pname, p.Type)
			}
			if p.Description == "" {
				t.Fatalf("skill %q parameter %q has no description", s.Name, pname)
			}
		}
	}
	if len(seen) != 27 {
		t.Fatalf("allSchemas covers %d skills; keep it in sync with registerBuiltins", len(seen))
	}
}

// Skills that cannot do anything useful without an argument must mark it
// required, or the model will call them empty and get an error instead.
func TestRequiredParametersAreDeclared(t *testing.T) {
	want := map[string]string{
		"weather": "city", "http_request": "url", "web_scrape": "url", "http_health": "url",
		"dns_lookup": "domain", "ping_check": "host", "port_scan": "host", "whois_lookup": "domain",
		"tls_probe": "host", "http_headers": "url", "banner_grab": "host", "ptr_lookup": "ip",
		"email_auth": "domain", "jwt_inspect": "token",
		"hash_skill": "text", "cidr_calc": "cidr", "math_eval": "expression",
		"cron_schedule": "expression", "reminder": "message", "qr_generate": "data",
		"shell_command": "command",
	}
	byName := map[string]Skill{}
	for _, s := range allSchemas() {
		byName[s.Name] = s
	}
	for skill, param := range want {
		s, ok := byName[skill]
		if !ok {
			t.Fatalf("no schema for %q", skill)
		}
		p, ok := s.Parameters[param]
		if !ok {
			t.Fatalf("skill %q has no %q parameter", skill, param)
		}
		if !p.Required {
			t.Fatalf("skill %q parameter %q is not marked required", skill, param)
		}
	}
}

func TestReminderEchoesMessage(t *testing.T) {
	res, err := Reminder()(map[string]any{"message": "stand-up in 5"})
	if err != nil {
		t.Fatalf("Reminder: %v", err)
	}
	m := res.(map[string]any)
	// The scheduler reads exactly this key to build "⏰ Reminder: ..." messages.
	if m["reminder"] != "stand-up in 5" {
		t.Fatalf("reminder = %v", m["reminder"])
	}

	res, _ = Reminder()(map[string]any{})
	if _, ok := res.(map[string]any)["error"]; !ok {
		t.Fatal("an empty reminder was accepted")
	}
}

func TestDateTimeSkill(t *testing.T) {
	res, err := DateTime()(nil)
	if err != nil {
		t.Fatalf("DateTime: %v", err)
	}
	m := res.(map[string]any)
	for _, k := range []string{"date", "time", "day_of_week", "iso", "unix_timestamp"} {
		if m[k] == nil || m[k] == "" {
			t.Fatalf("datetime_skill field %q is empty: %v", k, m)
		}
	}
	if ts, _ := m["unix_timestamp"].(int64); ts <= 0 {
		t.Fatalf("unix_timestamp = %v", m["unix_timestamp"])
	}
}

func TestShellCommandDisabledByDefault(t *testing.T) {
	res, err := ShellCommand(false)(map[string]any{"command": "echo hi"})
	if err != nil {
		t.Fatalf("ShellCommand: %v", err)
	}
	msg, _ := res.(map[string]any)["error"].(string)
	if !strings.Contains(msg, "SKILL_SHELL_ENABLED") {
		t.Fatalf("disabled message does not name the env var: %q", msg)
	}
}

func TestShellCommandRuns(t *testing.T) {
	res, err := ShellCommand(true)(map[string]any{"command": "echo hello && echo oops >&2"})
	if err != nil {
		t.Fatalf("ShellCommand: %v", err)
	}
	m := res.(map[string]any)
	if m["exit_code"] != 0 {
		t.Fatalf("exit_code = %v", m["exit_code"])
	}
	if !strings.Contains(m["stdout"].(string), "hello") {
		t.Fatalf("stdout = %q", m["stdout"])
	}
	// stderr is captured separately so a failing command explains itself.
	if !strings.Contains(m["stderr"].(string), "oops") {
		t.Fatalf("stderr = %q", m["stderr"])
	}
}

func TestShellCommandNonZeroExit(t *testing.T) {
	res, err := ShellCommand(true)(map[string]any{"command": "exit 3"})
	if err != nil {
		t.Fatalf("ShellCommand: %v", err)
	}
	if code := res.(map[string]any)["exit_code"]; code != 3 {
		t.Fatalf("exit_code = %v, want 3", code)
	}
}

func TestShellCommandTimeout(t *testing.T) {
	res, err := ShellCommand(true)(map[string]any{"command": "sleep 5", "timeout": 0.1})
	if err != nil {
		t.Fatalf("ShellCommand: %v", err)
	}
	// A killed command reports a non-zero exit rather than hanging the agent.
	if code := res.(map[string]any)["exit_code"]; code == 0 {
		t.Fatalf("exit_code = %v, want non-zero for a timed-out command", code)
	}
}

func TestShellCommandBlockedPatterns(t *testing.T) {
	for _, cmd := range []string{"rm -rf /", "mkfs.ext4 /dev/sda", "dd if=/dev/zero of=/dev/sda", "shutdown -h now", "reboot"} {
		res, err := ShellCommand(true)(map[string]any{"command": cmd})
		if err != nil {
			t.Fatalf("ShellCommand(%q): %v", cmd, err)
		}
		msg, _ := res.(map[string]any)["error"].(string)
		if !strings.Contains(msg, "blocked") {
			t.Fatalf("command %q was not blocked: %v", cmd, res)
		}
	}
	if res, _ := ShellCommand(true)(map[string]any{}); res.(map[string]any)["error"] == nil {
		t.Fatal("an empty command was accepted")
	}
}

func TestSshCommandDisabledByDefault(t *testing.T) {
	res, err := SshCommand(SSHConfig{Enabled: false})(map[string]any{"host": "h", "command": "ls"})
	if err != nil {
		t.Fatalf("SshCommand: %v", err)
	}
	msg, _ := res.(map[string]any)["error"].(string)
	if !strings.Contains(msg, "SKILL_SSH_ENABLED") {
		t.Fatalf("disabled message does not name the env var: %q", msg)
	}
}

func TestSshCommandRequiresHostAndCommand(t *testing.T) {
	exec := SshCommand(SSHConfig{Enabled: true})
	for _, args := range []map[string]any{{}, {"host": "h"}, {"command": "ls"}} {
		res, err := exec(args)
		if err != nil {
			t.Fatalf("SshCommand(%v): %v", args, err)
		}
		if res.(map[string]any)["error"] == nil {
			t.Fatalf("SshCommand(%v) accepted incomplete arguments", args)
		}
	}
}

// newTestKey writes an ed25519 private key in OpenSSH PEM form.
func newTestKey(t *testing.T) (pemBytes []byte, path string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("MarshalPrivateKey: %v", err)
	}
	pemBytes = pem.EncodeToMemory(block)
	path = filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return pemBytes, path
}

// Documented precedence: identity_file arg > SKILL_SSH_PRIVATE_KEY > SKILL_SSH_IDENTITY_FILE.
func TestLoadSignerPrecedence(t *testing.T) {
	pemBytes, path := newTestKey(t)

	// No source configured at all: no signer, no error (agent may still try
	// agent-forwarding-free auth and get a clean failure from the server).
	signer, err := loadSigner("", "", "")
	if err != nil || signer != nil {
		t.Fatalf("loadSigner(empty) = %v, %v; want nil, nil", signer, err)
	}

	if signer, err = loadSigner(path, "", ""); err != nil || signer == nil {
		t.Fatalf("loadSigner(arg file) = %v, %v", signer, err)
	}
	if signer, err = loadSigner("", string(pemBytes), ""); err != nil || signer == nil {
		t.Fatalf("loadSigner(env PEM) = %v, %v", signer, err)
	}
	// The env var may hold base64-wrapped PEM, which is how it survives docker-compose.
	b64 := base64.StdEncoding.EncodeToString(pemBytes)
	if signer, err = loadSigner("", b64, ""); err != nil || signer == nil {
		t.Fatalf("loadSigner(base64 env PEM) = %v, %v", signer, err)
	}
	if signer, err = loadSigner("", "", path); err != nil || signer == nil {
		t.Fatalf("loadSigner(env file) = %v, %v", signer, err)
	}

	// The argument wins over both env sources, even when they are unusable.
	if signer, err = loadSigner(path, "garbage", "/nonexistent"); err != nil || signer == nil {
		t.Fatalf("loadSigner did not prefer the argument: %v, %v", signer, err)
	}
}

func TestLoadSignerErrors(t *testing.T) {
	if _, err := loadSigner("/nonexistent/key", "", ""); err == nil {
		t.Fatal("expected an error for a missing identity file")
	}
	if _, err := loadSigner("", "not a key", ""); err == nil {
		t.Fatal("expected an error for an unparseable private key")
	}
	if _, err := loadSigner("", "", "/nonexistent/key"); err == nil {
		t.Fatal("expected an error for a missing SKILL_SSH_IDENTITY_FILE")
	}
}

func TestSshCommandReportsUnreachableHost(t *testing.T) {
	_, path := newTestKey(t)
	// Port 1 on localhost refuses connections immediately.
	res, err := SshCommand(SSHConfig{Enabled: true, IdentFile: path})(map[string]any{
		"host": "127.0.0.1", "port": 1, "command": "ls", "timeout": 1,
	})
	if err != nil {
		t.Fatalf("SshCommand: %v", err)
	}
	m := res.(map[string]any)
	if m["error"] == nil {
		t.Fatalf("no error for an unreachable host: %v", m)
	}
	if m["host"] != "127.0.0.1" {
		t.Fatalf("error result does not echo the host: %v", m)
	}
}

func TestHttpRequestGet(t *testing.T) {
	var gotMethod, gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotHeader = r.Header.Get("X-Token")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hello":"world"}`))
	}))
	defer srv.Close()

	res, err := HttpRequest(5)(map[string]any{
		"url":     srv.URL,
		"headers": map[string]any{"X-Token": "abc"},
	})
	if err != nil {
		t.Fatalf("HttpRequest: %v", err)
	}
	m := res.(map[string]any)
	if m["status_code"] != 200 {
		t.Fatalf("status_code = %v", m["status_code"])
	}
	if gotMethod != "GET" {
		t.Fatalf("method = %q, want the GET default", gotMethod)
	}
	if gotHeader != "abc" {
		t.Fatalf("X-Token = %q", gotHeader)
	}
	// A JSON body is decoded so the model sees structure, not a quoted string.
	body, ok := m["body"].(map[string]any)
	if !ok || body["hello"] != "world" {
		t.Fatalf("body = %#v, want decoded JSON", m["body"])
	}
}

func TestHttpRequestPostSendsJSONBody(t *testing.T) {
	var gotBody, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(raw)
		gotBody = string(raw)
		gotCT = r.Header.Get("Content-Type")
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	res, err := HttpRequest(5)(map[string]any{
		"url":    srv.URL,
		"method": "post", // lower case must still work
		"body":   map[string]any{"name": "vlad"},
	})
	if err != nil {
		t.Fatalf("HttpRequest: %v", err)
	}
	if !strings.Contains(gotBody, `"name":"vlad"`) {
		t.Fatalf("body = %q", gotBody)
	}
	if gotCT != "application/json" {
		t.Fatalf("Content-Type = %q", gotCT)
	}
	// A non-JSON response comes back as a plain string rather than an error.
	if body := res.(map[string]any)["body"]; body != "not json" {
		t.Fatalf("body = %#v", body)
	}
}

func TestHttpRequestErrors(t *testing.T) {
	if res, _ := HttpRequest(5)(map[string]any{}); res.(map[string]any)["error"] == nil {
		t.Fatal("a missing url was accepted")
	}
	// A URL that cannot be parsed is reported, not panicked on.
	res, err := HttpRequest(5)(map[string]any{"url": "http://exa mple.com"})
	if err != nil {
		t.Fatalf("HttpRequest: %v", err)
	}
	if res.(map[string]any)["error"] == nil {
		t.Fatalf("an unparseable URL was accepted: %v", res)
	}
}

func TestPingCheckReachableAndNot(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	res, err := PingCheck()(map[string]any{"host": "127.0.0.1", "port": port, "timeout": 2})
	if err != nil {
		t.Fatalf("PingCheck: %v", err)
	}
	m := res.(map[string]any)
	if m["reachable"] != true {
		t.Fatalf("open port reported unreachable: %v", m)
	}
	if m["resolved_ip"] != "127.0.0.1" || m["port"] != port {
		t.Fatalf("result = %v", m)
	}
	if _, ok := m["total_ms"]; !ok {
		t.Fatalf("no timing reported: %v", m)
	}

	// A closed port is a clean "unreachable", not an error.
	ln.Close()
	res, err = PingCheck()(map[string]any{"host": "127.0.0.1", "port": port, "timeout": 1})
	if err != nil {
		t.Fatalf("PingCheck: %v", err)
	}
	if res.(map[string]any)["reachable"] != false {
		t.Fatalf("closed port reported reachable: %v", res)
	}

	if res, _ := PingCheck()(map[string]any{}); res.(map[string]any)["error"] == nil {
		t.Fatal("a missing host was accepted")
	}
}

func TestPingCheckUnresolvableHost(t *testing.T) {
	res, err := PingCheck()(map[string]any{"host": "no-such-host.invalid", "timeout": 1})
	if err != nil {
		t.Fatalf("PingCheck: %v", err)
	}
	m := res.(map[string]any)
	if m["reachable"] != false || m["error"] == nil {
		t.Fatalf("result = %v, want an unreachable result with an error", m)
	}
}

func TestPortScanFindsOpenPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	res, err := PortScan()(map[string]any{
		"host":    "127.0.0.1",
		"ports":   strconv.Itoa(port) + ",1",
		"timeout": 1,
	})
	if err != nil {
		t.Fatalf("PortScan: %v", err)
	}
	m := res.(map[string]any)
	open, _ := m["open_ports"].([]int)
	if len(open) != 1 || open[0] != port {
		t.Fatalf("open_ports = %v, want [%d]", m["open_ports"], port)
	}
	if m["scanned_count"] != 2 || m["closed_count"] != 1 {
		t.Fatalf("counts = scanned %v / closed %v", m["scanned_count"], m["closed_count"])
	}

	if res, _ := PortScan()(map[string]any{}); res.(map[string]any)["error"] == nil {
		t.Fatal("a missing host was accepted")
	}
	if res, _ := PortScan()(map[string]any{"host": "h", "ports": "abc"}); res.(map[string]any)["error"] == nil {
		t.Fatal("an unparseable port list was accepted")
	}
}

func TestSortInts(t *testing.T) {
	a := []int{9, 1, 5, 1, 3}
	sortInts(a)
	for i := 1; i < len(a); i++ {
		if a[i-1] > a[i] {
			t.Fatalf("not sorted: %v", a)
		}
	}
	sortInts(nil)      // must not panic
	sortInts([]int{})  //
	sortInts([]int{1}) //
}

func TestDnsLookupResolvesLocalhost(t *testing.T) {
	res, err := DnsLookup()(map[string]any{"domain": "localhost"})
	if err != nil {
		t.Fatalf("DnsLookup: %v", err)
	}
	m := res.(map[string]any)
	if m["error"] != nil {
		t.Fatalf("localhost lookup failed: %v", m)
	}
	if m["type"] != "A" {
		t.Fatalf("record_type default = %v, want A", m["type"])
	}
	if m["method"] == nil {
		t.Fatalf("no resolution method reported: %v", m)
	}
	if res, _ := DnsLookup()(map[string]any{}); res.(map[string]any)["error"] == nil {
		t.Fatal("a missing domain was accepted")
	}
}

func TestWhoisLookupRequiresDomain(t *testing.T) {
	res, err := WhoisLookup()(map[string]any{})
	if err != nil {
		t.Fatalf("WhoisLookup: %v", err)
	}
	if res.(map[string]any)["error"] == nil {
		t.Fatal("a missing domain was accepted")
	}
}

func TestHashKnownVectors(t *testing.T) {
	// Known vectors: hashing must not silently change algorithm.
	cases := map[string]string{
		"md5":    "5d41402abc4b2a76b9719d911017c592",
		"sha1":   "aaf4c61ddcc5e8a2dabede0f3b482cd9aea9434d",
		"sha256": "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824",
	}
	for algo, want := range cases {
		res, err := Hash()(map[string]any{"text": "hello", "algorithm": algo})
		if err != nil {
			t.Fatalf("Hash(%s): %v", algo, err)
		}
		m := res.(map[string]any)
		if m["hash"] != want {
			t.Fatalf("%s(hello) = %v, want %v", algo, m["hash"], want)
		}
		if m["input_length"] != 5 {
			t.Fatalf("input_length = %v", m["input_length"])
		}
	}

	// Case-insensitive, since models capitalise inconsistently.
	res, _ := Hash()(map[string]any{"text": "hello", "algorithm": "SHA256"})
	if res.(map[string]any)["hash"] != cases["sha256"] {
		t.Fatalf("SHA256 (upper case) was not accepted: %v", res)
	}
}

func TestJSONSerialisableResults(t *testing.T) {
	// Every result goes through json.Marshal on its way to the model and the
	// audit log; a non-serialisable value would break the whole turn.
	results := []any{}
	for _, call := range []func() (any, error){
		func() (any, error) { return DateTime()(nil) },
		func() (any, error) { return Reminder()(map[string]any{"message": "m"}) },
		func() (any, error) { return UuidGen()(map[string]any{}) },
		func() (any, error) { return Hash()(map[string]any{"text": "x"}) },
		func() (any, error) { return CidrCalc()(map[string]any{"cidr": "10.0.0.0/8"}) },
		func() (any, error) { return MathEval()(map[string]any{"expression": "2+2"}) },
		func() (any, error) { return CronSchedule()(map[string]any{"expression": "0 9 * * *"}) },
		func() (any, error) { return ShellCommand(false)(map[string]any{"command": "x"}) },
	} {
		r, err := call()
		if err != nil {
			t.Fatalf("skill call: %v", err)
		}
		results = append(results, r)
	}
	for i, r := range results {
		if _, err := json.Marshal(r); err != nil {
			t.Fatalf("result %d is not JSON-serialisable: %v", i, err)
		}
	}
}
