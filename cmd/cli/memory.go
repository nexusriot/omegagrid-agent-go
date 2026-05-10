package main

import (
	"flag"
	"fmt"
	"os"
)

func runMemory(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: omega memory <search|add> [flags]")
		os.Exit(1)
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "search":
		memorySearch(rest)
	case "add":
		memoryAdd(rest)
	default:
		fatalf("memory: unknown subcommand %q", sub)
	}
}

func memorySearch(args []string) {
	fs := flag.NewFlagSet("memory search", flag.ExitOnError)
	k := fs.Int("k", 10, "Max results")
	jsonOut := fs.Bool("json", false, "JSON output")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: omega memory search \"query\" [-k N] [--json]")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	query := fs.Arg(0)
	if query == "" {
		fs.Usage()
		os.Exit(1)
	}

	hits, err := searchMemory(query, *k)
	if err != nil {
		fatalf("memory search: %v", err)
	}
	if *jsonOut {
		printJSON(hits)
		return
	}
	if len(hits) == 0 {
		fmt.Println(grey("(no results)"))
		return
	}
	for i, h := range hits {
		fmt.Printf("%s  dist=%.4f\n  %s\n",
			cyan(fmt.Sprintf("[%d]", i+1)), h.Distance, h.Text)
	}
}

func memoryAdd(args []string) {
	fs := flag.NewFlagSet("memory add", flag.ExitOnError)
	var metaPairs multiFlag
	fs.Var(&metaPairs, "meta", "Metadata in key=value form (repeatable)")
	jsonOut := fs.Bool("json", false, "JSON output")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: omega memory add \"text\" [--meta key=value ...] [--json]")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	text := fs.Arg(0)
	if text == "" {
		fs.Usage()
		os.Exit(1)
	}
	meta, err := parseKeyValues(metaPairs)
	if err != nil {
		fatalf("memory add: %v", err)
	}

	if err := addMemory(text, meta); err != nil {
		fatalf("memory add: %v", err)
	}
	if *jsonOut {
		printJSON(map[string]any{"ok": true})
		return
	}
	fmt.Println(green("stored"))
}
