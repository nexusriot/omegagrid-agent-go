package builtin

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func requireBwrap(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap not installed")
	}
	if err := exec.Command("bwrap", "--ro-bind", "/", "/", "--unshare-all", "true").Run(); err != nil {
		t.Skipf("bwrap cannot create namespaces here: %v", err)
	}
}

func runShell(t *testing.T, cfg ShellConfig, command string) map[string]any {
	t.Helper()
	res, err := ShellCommandWith(cfg)(context.Background(), map[string]any{"command": command, "timeout": 20})
	if err != nil {
		t.Fatal(err)
	}
	return res.(map[string]any)
}

func TestBwrapSandboxIsolatesTheCommand(t *testing.T) {
	requireBwrap(t)
	cfg := ShellConfig{Enabled: true, Sandbox: "bwrap"}

	// The host filesystem is read-only…
	probe := filepath.Join(t.TempDir(), "escape")
	out := runShell(t, cfg, "echo pwned > "+probe+"; echo done")
	if _, err := os.Stat(probe); err == nil {
		t.Fatal("a sandboxed command wrote to the host filesystem")
	}
	if out["sandbox"] != "bwrap" {
		t.Fatalf("result does not report the sandbox: %v", out)
	}
	// …and there is no network: the jail's own namespace has only a
	// loopback interface (/proc is mounted fresh, so /proc/net is its view).
	out = runShell(t, cfg, "tail -n +3 /proc/net/dev | cut -d: -f1 | tr -d ' '")
	if ifaces := strings.Fields(out["stdout"].(string)); len(ifaces) != 1 || ifaces[0] != "lo" {
		t.Fatalf("sandboxed command sees interfaces %v, want only lo", ifaces)
	}
	cfg.SandboxNetwork = true
	out = runShell(t, cfg, "tail -n +3 /proc/net/dev | wc -l")
	if strings.TrimSpace(out["stdout"].(string)) == "1" {
		if hostOut, _ := exec.Command("sh", "-c", "tail -n +3 /proc/net/dev | wc -l").Output(); strings.TrimSpace(string(hostOut)) != "1" {
			t.Fatalf("SandboxNetwork=true still hid the host's interfaces")
		}
	}
	cfg.SandboxNetwork = false
	// A private /tmp is writable.
	out = runShell(t, cfg, "echo ok > /tmp/x && cat /tmp/x")
	if strings.TrimSpace(out["stdout"].(string)) != "ok" {
		t.Fatalf("private /tmp not writable: %v", out)
	}

	// SandboxWritable is bind-mounted read-write and is the working dir.
	work := t.TempDir()
	cfg.SandboxWritable = work
	out = runShell(t, cfg, "pwd; echo kept > result.txt")
	if strings.TrimSpace(out["stdout"].(string)) != work {
		t.Fatalf("cwd = %v, want %s", out["stdout"], work)
	}
	if b, _ := os.ReadFile(filepath.Join(work, "result.txt")); strings.TrimSpace(string(b)) != "kept" {
		t.Fatal("the writable dir did not receive the file")
	}
}

func TestBwrapSandboxFailsClosed(t *testing.T) {
	saved := bwrapPath
	bwrapPath = func() (string, error) { return "", errors.New("not found") }
	defer func() { bwrapPath = saved }()
	out := runShell(t, ShellConfig{Enabled: true, Sandbox: "bwrap"}, "echo should-not-run")
	if msg, _ := out["error"].(string); !strings.Contains(msg, "bwrap is not installed") {
		t.Fatalf("missing bwrap must refuse to run: %v", out)
	}
	out = runShell(t, ShellConfig{Enabled: true, Sandbox: "docker"}, "true")
	if msg, _ := out["error"].(string); !strings.Contains(msg, "unknown SKILL_SHELL_SANDBOX") {
		t.Fatalf("unknown sandbox = %v", out)
	}
}

func newHostKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// The host key used to be ignored (InsecureIgnoreHostKey), so anyone on the
// path could impersonate the target and read the command and its secrets.
func TestHostKeyPolicies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "known_hosts")
	addr := &net.TCPAddr{IP: net.IPv4(10, 0, 0, 5), Port: 22}
	key, other := newHostKey(t), newHostKey(t)

	cb, err := hostKeyCallback(SSHConfig{KnownHosts: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := cb("box:22", addr, key); err != nil {
		t.Fatalf("accept-new refused a first-seen host: %v", err)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "box") {
		t.Fatalf("first-seen key not recorded: %q", b)
	}
	if err := cb("box:22", addr, key); err != nil {
		t.Fatalf("the recorded key was refused: %v", err)
	}
	if err := cb("box:22", addr, other); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("a changed host key was accepted: %v", err)
	}

	strict, err := hostKeyCallback(SSHConfig{KnownHosts: path, HostKeyPolicy: "strict"})
	if err != nil {
		t.Fatal(err)
	}
	if err := strict("box:22", addr, key); err != nil {
		t.Fatalf("strict refused a known host: %v", err)
	}
	if err := strict("newhost:22", addr, key); err == nil {
		t.Fatal("strict accepted an unknown host")
	}

	insecure, _ := hostKeyCallback(SSHConfig{HostKeyPolicy: "insecure"})
	if err := insecure("anything:22", addr, other); err != nil {
		t.Fatalf("insecure policy checked the key: %v", err)
	}
	if _, err := hostKeyCallback(SSHConfig{KnownHosts: path, HostKeyPolicy: "maybe"}); err == nil {
		t.Fatal("an unknown policy was accepted")
	}
	if _, err := hostKeyCallback(SSHConfig{}); err == nil {
		t.Fatal("accept-new without a known_hosts path was accepted")
	}
}
