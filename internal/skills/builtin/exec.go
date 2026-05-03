package builtin

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// ── Shell Command ─────────────────────────────────────────────────────────

func ShellCommandSchema() Skill {
	return Skill{Name: "shell_command", Description: "Execute a shell command on the local machine (requires SKILL_SHELL_ENABLED=true).",
		Parameters: map[string]Param{
			"command": {Type: "string", Description: "Shell command to run", Required: true},
			"timeout": {Type: "number", Description: "Timeout in seconds (default 30)", Required: false},
		}}
}

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
		timeout := floatOr(args, "timeout", 30)
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout*float64(time.Second)))
		defer cancel()
		cmd := exec.CommandContext(ctx, "sh", "-c", command)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		exitCode := 0
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				exitCode = ee.ExitCode()
			} else {
				exitCode = -1
			}
		}
		return map[string]any{
			"exit_code": exitCode,
			"stdout":    stdout.String(),
			"stderr":    stderr.String(),
		}, nil
	}
}

// ── SSH Command ───────────────────────────────────────────────────────────

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
		timeout := floatOr(args, "timeout", 30)

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
		addr := fmt.Sprintf("%s:%d", host, port)
		deadline := time.Now().Add(time.Duration(timeout * float64(time.Second)))

		conn, err := ssh.Dial("tcp", addr, clientCfg)
		if err != nil {
			return map[string]any{"host": host, "command": command, "error": err.Error()}, nil
		}
		defer conn.Close()

		sess, err := conn.NewSession()
		if err != nil {
			return map[string]any{"host": host, "command": command, "error": err.Error()}, nil
		}
		defer sess.Close()

		var stdout, stderr bytes.Buffer
		sess.Stdout = &stdout
		sess.Stderr = &stderr
		_ = deadline

		exitCode := 0
		if err := sess.Run(command); err != nil {
			if ee, ok := err.(*ssh.ExitError); ok {
				exitCode = ee.ExitStatus()
			} else {
				exitCode = -1
			}
		}
		return map[string]any{
			"host":      host,
			"command":   command,
			"exit_code": exitCode,
			"stdout":    stdout.String(),
			"stderr":    stderr.String(),
		}, nil
	}
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
