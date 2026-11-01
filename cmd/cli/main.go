// Command omega is a CLI for the omegagrid-agent.
//
// Usage:
//
//	omega version
//	omega ask "question" [--stream] [--session N] [--max-steps N] [--namespace NS] [--json]
//	omega skills list [--json]
//	omega skills describe NAME [--json]
//	omega skills run NAME [--arg key=value ...] [--json]
//	omega memory search "query" [-k N] [--namespace NS ...] [--json]
//	omega memory add "text" [--meta key=value ...] [--namespace NS] [--ttl 72h] [--json]
//	omega memory list [-limit N] [-offset N] [--namespace NS] [--json]
//	omega memory edit ID [--text "..."] [--meta key=value ...] [--ttl 720h] [--json]
//	omega memory delete ID [--json]
//	omega memory namespaces [--json]
//	omega memory export [-o FILE] [--embeddings]
//	omega memory import FILE|-
//	omega schedule list [--json]
//	omega schedule create --name X --cron "..." (--skill Y [--arg key=value ...] | --prompt "...") [--tz ZONE] [--retries N] [--retry-delay SEC] [--one-shot]
//	omega schedule enable|disable|run|history ID
//	omega schedule delete ID
//	omega session list [--json]
//	omega session export ID
//
// Flags may come before or after the positional arguments.
//
// By default the CLI wires services in-process (local mode).  Set OMEGA_REMOTE
// to a gateway base URL to hit a running gateway over HTTP instead.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/nexusriot/omegagrid-agent-go/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	sub := os.Args[1]
	args := os.Args[2:]

	switch sub {
	case "ask":
		runAsk(args)
	case "skills":
		runSkills(args)
	case "memory":
		runMemory(args)
	case "schedule":
		runSchedule(args)
	case "session":
		runSession(args)
	case "version", "--version", "-v":
		fmt.Println("omega " + version.Version)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "omega: unknown command %q\n\n", sub)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Print(`omega — omegagrid-agent CLI

Usage:
  omega version
  omega ask "question" [--stream] [--session N] [--max-steps N] [--namespace NS] [--json]
  omega skills list [--json]
  omega skills describe NAME [--json]
  omega skills run NAME [--arg key=value ...] [--json]
  omega memory search "query" [-k N] [--namespace NS ...] [--json]
  omega memory add "text" [--meta key=value ...] [--namespace NS] [--ttl 72h] [--json]
  omega memory list [-limit N] [-offset N] [--namespace NS] [--json]
  omega memory edit ID [--text "..."] [--meta key=value ...] [--ttl 720h] [--json]
  omega memory delete ID [--json]
  omega memory namespaces [--json]
  omega memory export [-o FILE] [--embeddings]
  omega memory import FILE|-
  omega schedule list [--json]
  omega schedule create --name NAME --cron EXPR (--skill SKILL [--arg k=v ...] | --prompt TEXT)
                        [--tz ZONE] [--retries N] [--retry-delay SEC] [--one-shot]
  omega schedule enable|disable|run|history ID
  omega schedule delete ID
  omega session list [--json]
  omega session export ID

Flags may come before or after the positional arguments.

Environment:
  OMEGA_REMOTE   Base URL of a running gateway (e.g. http://localhost:8000).
                 When set, all commands hit the remote API instead of
                 wiring services in-process.
  GATEWAY_API_TOKEN  Sent as "Authorization: Bearer <token>" in remote mode;
                 required when the gateway has it configured.
  All other env vars (LLM_PROVIDER, DATA_DIR, etc.) configure local mode.
`)
}

// parseArgs parses fs and returns the positional arguments, accepting flags
// on either side of them. Go's flag package stops at the first non-flag, so
// the documented `omega ask "q" --session 7` and `omega skills run NAME --arg
// k=v` silently dropped every flag after the positional and still exited 0.
// A literal "--" ends flag parsing as usual.
func parseArgs(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		_ = fs.Parse(args) // ExitOnError: a bad flag exits before returning
		rest := fs.Args()
		if consumed := args[:len(args)-len(rest)]; len(consumed) > 0 && consumed[len(consumed)-1] == "--" {
			return append(pos, rest...)
		}
		if len(rest) == 0 {
			return pos
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

// argAt returns the i-th positional argument, or "" when there is none.
func argAt(pos []string, i int) string {
	if i < len(pos) {
		return pos[i]
	}
	return ""
}

// interruptContext is cancelled by Ctrl-C / SIGTERM, so a local-mode command
// stops its agent run and any running skill (a shell command, an SSH session,
// a port scan) instead of leaving it to finish after the CLI has gone.
func interruptContext() context.Context {
	ctx, _ := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	return ctx
}
