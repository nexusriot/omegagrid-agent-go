// Command omega is a CLI for the omegagrid-agent.
//
// Usage:
//
//	omega ask "question" [--stream] [--session N] [--max-steps N]
//	omega skills list [--json]
//	omega skills run NAME [--arg key=value ...] [--json]
//	omega memory search "query" [-k N] [--json]
//	omega memory add "text" [--meta key=value ...] [--json]
//	omega schedule list [--json]
//	omega schedule create --name X --cron "..." --skill Y [--arg key=value ...]
//	omega schedule delete ID
//	omega session list [--json]
//	omega session export ID
//
// By default the CLI wires services in-process (local mode).  Set OMEGA_REMOTE
// or pass --remote <url> to hit a running gateway instead.
package main

import (
	"fmt"
	"os"
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
  omega ask "question" [--stream] [--session N] [--max-steps N]
  omega skills list [--json]
  omega skills run NAME [--arg key=value ...] [--json]
  omega memory search "query" [-k N] [--json]
  omega memory add "text" [--meta key=value ...] [--json]
  omega schedule list [--json]
  omega schedule create --name NAME --cron EXPR --skill SKILL [--arg k=v ...]
  omega schedule delete ID
  omega session list [--json]
  omega session export ID

Environment:
  OMEGA_REMOTE   Base URL of a running gateway (e.g. http://localhost:8000).
                 When set, all commands hit the remote API instead of
                 wiring services in-process.
  All other env vars (LLM_PROVIDER, DATA_DIR, etc.) configure local mode.
`)
}
