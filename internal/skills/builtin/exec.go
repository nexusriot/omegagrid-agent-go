package builtin

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

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

func ShellCommand(enabled bool) Executor {
	return func(args map[string]any) (any, error) {
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
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout*float64(time.Second)))
		defer cancel()
		cmd := exec.CommandContext(ctx, "sh", "-c", command)
		killProcessGroupOnCancel(cmd)
		// Bound how long Wait lingers on pipes a stray grandchild still holds
		// open after the kill.
		cmd.WaitDelay = 2 * time.Second
		stdout := &cappedBuffer{limit: maxCommandOutput}
		stderr := &cappedBuffer{limit: maxCommandOutput}
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		err := cmd.Run()
		exitCode := 0
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				exitCode = ee.ExitCode()
			} else {
				exitCode = -1
			}
		}
		out := map[string]any{
			"exit_code": exitCode,
			"stdout":    stdout.String(),
			"stderr":    stderr.String(),
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
}

func SshCommand(cfg SSHConfig) Executor {
	return func(args map[string]any) (any, error) {
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

		clientCfg := &ssh.ClientConfig{
			User:            user,
			Auth:            authMethods,
			HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
			Timeout:         10 * time.Second,
		}
		addr := net.JoinHostPort(host, strconv.Itoa(port))

		conn, err := sshDial(addr, clientCfg)
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
func sshDial(addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	conn, err := netguard.DialTimeout("tcp", addr, cfg.Timeout)
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

func loadSigner(identFileArg, privKeyEnv, identFileEnv string) (ssh.Signer, error) {
	// Priority: arg identity_file > env SKILL_SSH_PRIVATE_KEY > env SKILL_SSH_IDENTITY_FILE
	if identFileArg != "" {
		pem, err := os.ReadFile(identFileArg)
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
