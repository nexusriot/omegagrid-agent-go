package builtin

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/nexusriot/omegagrid-agent-go/internal/skills/netguard"
)

func ShellCommandSchema() Skill {
	return Skill{Name: "shell_command", Description: "Execute a shell command on the local machine (requires SKILL_SHELL_ENABLED=true).",
		Parameters: map[string]Param{
			"command": {Type: "string", Description: "Shell command to run", Required: true},
			"timeout": {Type: "number", Description: "Timeout in seconds (default 30)", Required: false},
		}}
}

// blockedPatterns catches a few obviously destructive commands typed in
// their most common spelling. It is NOT a security boundary — quoting,
// variables, `$(...)`, a different binary or a script file all bypass it
// trivially. The boundary is SKILL_SHELL_ENABLED (off by default) and the
// privileges of the account the gateway runs as.
var blockedPatterns = []string{
	"rm -rf /", "mkfs", "dd if=", ":(){", "shutdown", "reboot", "halt", "poweroff",
}

// ShellCommand is ShellCommandContext without cancellation.
func ShellCommand(enabled bool) Executor {
	return withoutContext(ShellCommandContext(enabled))
}

// ShellCommandContext runs commands unsandboxed.
func ShellCommandContext(enabled bool) ContextExecutor {
	return ShellCommandWith(ShellConfig{Enabled: enabled})
}

// ShellConfig controls shell_command.
type ShellConfig struct {
	Enabled bool
	// Sandbox is "none" (or "") or "bwrap". With bwrap every command runs in
	// a bubblewrap jail: the whole filesystem read-only, a private /tmp, its
	// own PID/IPC/UTS/user namespaces and — unless SandboxNetwork — no
	// network at all. The blocklist below is no boundary; this is.
	Sandbox        string
	SandboxNetwork bool
	// SandboxWritable, when set, is bind-mounted read-write at the same path
	// and becomes the working directory (the default is the private /tmp).
	SandboxWritable string
}

// bwrapPath is the sandbox binary, resolved at call time (a seam for tests).
var bwrapPath = func() (string, error) { return exec.LookPath("bwrap") }

// shellCmd builds the process for command under cfg's sandbox.
func shellCmd(ctx context.Context, cfg ShellConfig, command string) (*exec.Cmd, error) {
	switch strings.ToLower(cfg.Sandbox) {
	case "", "none", "off":
		return exec.CommandContext(ctx, "sh", "-c", command), nil
	case "bwrap", "bubblewrap":
		bin, err := bwrapPath()
		if err != nil {
			// Fail closed: the operator asked for a sandbox.
			return nil, fmt.Errorf("SKILL_SHELL_SANDBOX=bwrap but bwrap is not installed: %w", err)
		}
		a := []string{
			"--ro-bind", "/", "/",
			"--dev", "/dev",
			"--proc", "/proc",
			"--tmpfs", "/tmp",
			"--unshare-all",
			"--die-with-parent",
			"--new-session",
		}
		if cfg.SandboxNetwork {
			a = append(a, "--share-net")
		}
		if cfg.SandboxWritable != "" {
			a = append(a, "--bind", cfg.SandboxWritable, cfg.SandboxWritable, "--chdir", cfg.SandboxWritable)
		} else {
			a = append(a, "--chdir", "/tmp")
		}
		a = append(a, "sh", "-c", command)
		return exec.CommandContext(ctx, bin, a...), nil
	default:
		return nil, fmt.Errorf("unknown SKILL_SHELL_SANDBOX %q (use none or bwrap)", cfg.Sandbox)
	}
}

// ShellCommandWith is shell_command under cfg.
func ShellCommandWith(cfg ShellConfig) ContextExecutor {
	enabled := cfg.Enabled
	return func(ctx context.Context, args map[string]any) (any, error) {
		if !enabled {
			return map[string]any{"error": "shell_command is disabled (set SKILL_SHELL_ENABLED=true to enable)"}, nil
		}
		command := str(args, "command")
		if command == "" {
			return map[string]any{"error": "command is required"}, nil
		}
		for _, pat := range blockedPatterns {
			if strings.Contains(command, pat) {
				return map[string]any{"error": fmt.Sprintf("blocked: command contains '%s'", pat)}, nil
			}
		}
		timeout := clampCommandTimeout(floatOr(args, "timeout", 30))
		ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout*float64(time.Second)))
		defer cancel()
		cmd, err := shellCmd(ctx, cfg, command)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		killProcessGroupOnCancel(cmd)
		// Bound how long Wait lingers on pipes a stray grandchild still holds
		// open after the kill.
		cmd.WaitDelay = 2 * time.Second
		stdout := &cappedBuffer{limit: maxCommandOutput}
		stderr := &cappedBuffer{limit: maxCommandOutput}
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		err = cmd.Run()
		exitCode := 0
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				exitCode = ee.ExitCode()
			} else {
				exitCode = -1
			}
		}
		sandbox := strings.ToLower(cfg.Sandbox)
		if sandbox == "" || sandbox == "off" {
			sandbox = "none"
		}
		out := map[string]any{
			"exit_code": exitCode,
			"stdout":    stdout.String(),
			"stderr":    stderr.String(),
			"sandbox":   sandbox,
		}
		if stdout.truncated || stderr.truncated {
			out["stdout_truncated"] = stdout.truncated
			out["stderr_truncated"] = stderr.truncated
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			out["error"] = fmt.Sprintf("command timed out after %gs", timeout)
		}
		return out, nil
	}
}

func SshCommandSchema() Skill {
	return Skill{Name: "ssh_command", Description: "Execute a command on a remote host via SSH (requires SKILL_SSH_ENABLED=true).",
		Parameters: map[string]Param{
			"host":          {Type: "string", Description: "Remote hostname or IP", Required: true},
			"command":       {Type: "string", Description: "Command to run remotely", Required: true},
			"user":          {Type: "string", Description: "SSH username (default from SKILL_SSH_DEFAULT_USER)", Required: false},
			"port":          {Type: "number", Description: "SSH port (default 22)", Required: false},
			"timeout":       {Type: "number", Description: "Timeout in seconds (default 30)", Required: false},
			"identity_file": {Type: "string", Description: "Path to private key file (overrides env)", Required: false},
		}}
}

type SSHConfig struct {
	Enabled     bool
	PrivKey     string // PEM or base64-encoded PEM
	DefaultUser string
	IdentFile   string
	// KnownHosts is the known_hosts file host keys are checked against.
	KnownHosts string
	// HostKeyPolicy is "accept-new" (default: trust a host's key the first
	// time and record it; refuse a changed key), "strict" (only hosts
	// already in KnownHosts) or "insecure" (no checking — the old behaviour,
	// which let anyone on the path impersonate the host and read the
	// command and the credentials it carries).
	HostKeyPolicy string
}

// knownHostsMu serialises appends to a known_hosts file.
var knownHostsMu sync.Mutex

// hostKeyCallback builds the HostKeyCallback for cfg's policy.
func hostKeyCallback(cfg SSHConfig) (ssh.HostKeyCallback, error) {
	policy := strings.ToLower(strings.TrimSpace(cfg.HostKeyPolicy))
	switch policy {
	case "insecure", "off", "none":
		return ssh.InsecureIgnoreHostKey(), nil //nolint:gosec
	case "", "accept-new", "tofu", "strict":
	default:
		return nil, fmt.Errorf("unknown SKILL_SSH_HOST_KEY_POLICY %q (use accept-new, strict or insecure)", cfg.HostKeyPolicy)
	}
	if cfg.KnownHosts == "" {
		return nil, errors.New("no known_hosts file configured (SKILL_SSH_KNOWN_HOSTS)")
	}
	if policy != "strict" {
		if err := os.MkdirAll(filepath.Dir(cfg.KnownHosts), 0o700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(cfg.KnownHosts, os.O_CREATE|os.O_RDONLY, 0o600)
		if err != nil {
			return nil, err
		}
		_ = f.Close()
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		knownHostsMu.Lock()
		defer knownHostsMu.Unlock()
		check, err := knownhosts.New(cfg.KnownHosts)
		if err != nil {
			return fmt.Errorf("read known_hosts: %w", err)
		}
		err = check(hostname, remote, key)
		var ke *knownhosts.KeyError
		if err == nil || !errors.As(err, &ke) {
			return err
		}
		if len(ke.Want) > 0 {
			return fmt.Errorf("host key for %s does not match %s — refusing to connect (possible man-in-the-middle; remove the old entry if the key really changed)", hostname, cfg.KnownHosts)
		}
		if policy == "strict" {
			return fmt.Errorf("host %s is not in %s (SKILL_SSH_HOST_KEY_POLICY=strict)", hostname, cfg.KnownHosts)
		}
		f, err := os.OpenFile(cfg.KnownHosts, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = fmt.Fprintln(f, knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key))
		return err
	}, nil
}

// SshCommand is SshCommandContext without cancellation.
func SshCommand(cfg SSHConfig) Executor {
	return withoutContext(SshCommandContext(cfg))
}

func SshCommandContext(cfg SSHConfig) ContextExecutor {
	return func(ctx context.Context, args map[string]any) (any, error) {
		if !cfg.Enabled {
			return map[string]any{"error": "ssh_command is disabled (set SKILL_SSH_ENABLED=true to enable)"}, nil
		}
		host := str(args, "host")
		command := str(args, "command")
		if host == "" || command == "" {
			return map[string]any{"error": "host and command are required"}, nil
		}
		user := str(args, "user")
		if user == "" {
			user = cfg.DefaultUser
		}
		if user == "" {
			user = "root"
		}
		port := intOr(args, "port", 22)
		if port < 1 || port > 65535 {
			return map[string]any{"error": fmt.Sprintf("port %d outside 1-65535", port)}, nil
		}
		timeout := clampCommandTimeout(floatOr(args, "timeout", 30))

		signer, err := loadSigner(str(args, "identity_file"), cfg.PrivKey, cfg.IdentFile)
		if err != nil {
			return map[string]any{"error": fmt.Sprintf("load SSH key: %v", err)}, nil
		}

		authMethods := []ssh.AuthMethod{}
		if signer != nil {
			authMethods = append(authMethods, ssh.PublicKeys(signer))
		}

		hostKeys, err := hostKeyCallback(cfg)
		if err != nil {
			return map[string]any{"host": host, "command": command, "error": err.Error()}, nil
		}
		clientCfg := &ssh.ClientConfig{
			User:            user,
			Auth:            authMethods,
			HostKeyCallback: hostKeys,
			Timeout:         10 * time.Second,
		}
		addr := net.JoinHostPort(host, strconv.Itoa(port))

		conn, err := sshDial(ctx, addr, clientCfg)
		if err != nil {
			return map[string]any{"host": host, "command": command, "error": err.Error()}, nil
		}
		defer conn.Close()

		sess, err := conn.NewSession()
		if err != nil {
			return map[string]any{"host": host, "command": command, "error": err.Error()}, nil
		}
		defer sess.Close()

		stdout := &cappedBuffer{limit: maxCommandOutput}
		stderr := &cappedBuffer{limit: maxCommandOutput}
		sess.Stdout = stdout
		sess.Stderr = stderr

		// Enforce the caller-supplied timeout on command execution. Without
		// this a hung remote command blocks this goroutine forever (only the
		// dial had a timeout before).
		runErr := make(chan error, 1)
		go func() { runErr <- sess.Run(command) }()

		exitCode := 0
		select {
		case err := <-runErr:
			if err != nil {
				if ee, ok := err.(*ssh.ExitError); ok {
					exitCode = ee.ExitStatus()
				} else {
					exitCode = -1
				}
			}
		case <-ctx.Done():
			_ = sess.Signal(ssh.SIGKILL)
			_ = sess.Close()
			return nil, ctx.Err()
		case <-time.After(time.Duration(timeout * float64(time.Second))):
			// Don't read stdout/stderr here: the Run goroutine may still be
			// writing to those buffers (bytes.Buffer is not concurrency-safe).
			_ = sess.Signal(ssh.SIGKILL)
			_ = sess.Close()
			return map[string]any{
				"host":      host,
				"command":   command,
				"exit_code": -1,
				"error":     fmt.Sprintf("command timed out after %gs", timeout),
			}, nil
		}
		out := map[string]any{
			"host":      host,
			"command":   command,
			"exit_code": exitCode,
			"stdout":    stdout.String(),
			"stderr":    stderr.String(),
		}
		if stdout.truncated || stderr.truncated {
			out["stdout_truncated"] = stdout.truncated
			out["stderr_truncated"] = stderr.truncated
		}
		return out, nil
	}
}

// sshDial is ssh.Dial with the handshake bounded. ssh.ClientConfig.Timeout
// covers only the TCP connect: a server that accepts and then stalls (or
// trickles) the version exchange or key exchange held the call forever. The
// connection also goes through the SSRF guard like every other skill dial.
func sshDial(ctx context.Context, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	conn, err := netguard.Dialer(cfg.Timeout).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(sshHandshakeTimeout))
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	// The command's own timeout governs from here on.
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(c, chans, reqs), nil
}

// sshHandshakeTimeout bounds the SSH version + key exchange + auth.
var sshHandshakeTimeout = 15 * time.Second

// maxCommandTimeoutSec bounds shell_command / ssh_command runtimes.
const maxCommandTimeoutSec = 300

// clampCommandTimeout bounds a command timeout to [1, 300] seconds.
func clampCommandTimeout(v float64) float64 {
	v = clampTimeout(v, 30, maxCommandTimeoutSec)
	if v < 1 {
		return 1
	}
	return v
}

// maxCommandOutput caps each of stdout and stderr. `cat /dev/urandom` or a
// chatty build would otherwise grow the buffer without bound and then be
// shipped whole to the model.
const maxCommandOutput = 256 << 10

// cappedBuffer keeps the first limit bytes written and discards the rest,
// still reporting every write as successful so the producer is not killed by
// a short write (SIGPIPE / EPIPE) mid-run.
type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.limit - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
			c.truncated = true
		} else {
			c.buf.Write(p)
		}
	} else if len(p) > 0 {
		c.truncated = true
	}
	return len(p), nil
}

// String returns the kept output, dropping a rune the cap cut in half.
func (c *cappedBuffer) String() string {
	if c.truncated {
		return string(trimPartialRune(c.buf.Bytes()))
	}
	return c.buf.String()
}

// maxKeyFileBytes is far above any real private key (an RSA-16384 PEM is
// ~13 KB).
const maxKeyFileBytes = 64 << 10

// readKeyFile reads a model-supplied identity_file. os.ReadFile on
// /dev/zero grew its buffer until the process ran out of memory, and on a
// FIFO or /dev/stdin it blocked the call forever, before any timeout applied;
// only bounded regular files are accepted.
func readKeyFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("identity_file %q is not a regular file", path)
	}
	if st.Size() > maxKeyFileBytes {
		return nil, fmt.Errorf("identity_file %q is %d bytes, over the %d-byte limit", path, st.Size(), maxKeyFileBytes)
	}
	return io.ReadAll(io.LimitReader(f, maxKeyFileBytes+1))
}

func loadSigner(identFileArg, privKeyEnv, identFileEnv string) (ssh.Signer, error) {
	// Priority: arg identity_file > env SKILL_SSH_PRIVATE_KEY > env SKILL_SSH_IDENTITY_FILE
	if identFileArg != "" {
		pem, err := readKeyFile(identFileArg)
		if err != nil {
			return nil, err
		}
		return ssh.ParsePrivateKey(pem)
	}
	if privKeyEnv != "" {
		pem := []byte(privKeyEnv)
		// try base64 decode first
		if decoded, err := base64.StdEncoding.DecodeString(privKeyEnv); err == nil {
			pem = decoded
		}
		return ssh.ParsePrivateKey(pem)
	}
	if identFileEnv != "" {
		pem, err := os.ReadFile(identFileEnv)
		if err != nil {
			return nil, err
		}
		return ssh.ParsePrivateKey(pem)
	}
	return nil, nil
}
