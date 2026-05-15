package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"
)

func runSession(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: omega session <list|export> [flags]")
		os.Exit(1)
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list":
		sessionList(rest)
	case "export":
		sessionExport(rest)
	default:
		fatalf("session: unknown subcommand %q", sub)
	}
}

func sessionList(args []string) {
	fs := flag.NewFlagSet("session list", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "JSON output")
	fs.Parse(args)

	sessions, err := listSessions()
	if err != nil {
		fatalf("session list: %v", err)
	}
	if *jsonOut {
		printJSON(sessions)
		return
	}
	if len(sessions) == 0 {
		fmt.Println(grey("(no sessions)"))
		return
	}
	for _, s := range sessions {
		ts := time.Unix(int64(s.CreatedAt), 0).Format("2006-01-02 15:04:05")
		fmt.Printf("%s  created=%s  messages=%d\n",
			cyan(fmt.Sprintf("[%d]", s.ID)), ts, s.MessageCount)
	}
}

func sessionExport(args []string) {
	fs := flag.NewFlagSet("session export", flag.ExitOnError)
	jsonOut := fs.Bool("json", true, "JSON output (default true)")
	fs.Parse(args)
	_ = jsonOut

	idStr := fs.Arg(0)
	if idStr == "" {
		fatalf("session export: session ID required")
	}
	id, err := strconv.Atoi(idStr)
	if err != nil {
		fatalf("session export: invalid ID %q", idStr)
	}

	msgs, err := exportSession(id)
	if err != nil {
		fatalf("session export: %v", err)
	}
	// Always print as JSON — this output is meant for piping.
	printJSON(map[string]any{
		"session_id": id,
		"messages":   msgs,
		"exported_at": time.Now().UTC().Format(time.RFC3339),
	})
}

// suppress unused import warning
var _ = os.Stderr
