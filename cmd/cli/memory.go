package main

import (
	"flag"
	"fmt"
	"os"
)

func runMemory(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: omega memory <search|add|list|delete> [flags]")
		os.Exit(1)
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "search":
		memorySearch(rest)
	case "add":
		memoryAdd(rest)
	case "list":
		memoryList(rest)
	case "delete", "rm":
		memoryDelete(rest)
	default:
		fatalf("memory: unknown subcommand %q", sub)
	}
}

func memoryList(args []string) {
	fs := flag.NewFlagSet("memory list", flag.ExitOnError)
	limit := fs.Int("limit", 100, "Max results (0 = all)")
	offset := fs.Int("offset", 0, "Skip the first N results")
	jsonOut := fs.Bool("json", false, "JSON output")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: omega memory list [-limit N] [-offset N] [--json]")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	hits, total, err := listMemories(*limit, *offset)
	if err != nil {
		fatalf("memory list: %v", err)
	}
	if *jsonOut {
		printJSON(map[string]any{"hits": hits, "total": total})
		return
	}
	if len(hits) == 0 {
		fmt.Println(grey("(no memories)"))
		return
	}
	for _, h := range hits {
		fmt.Printf("%s  %s\n", cyan(h.ID), h.Text)
	}
	fmt.Println(grey(fmt.Sprintf("— %d shown of %d total", len(hits), total)))
}

func memoryDelete(args []string) {
	fs := flag.NewFlagSet("memory delete", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "JSON output")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: omega memory delete <id> [--json]")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	id := fs.Arg(0)
	if id == "" {
		fs.Usage()
		os.Exit(1)
	}
	if err := deleteMemory(id); err != nil {
		fatalf("memory delete: %v", err)
	}
	if *jsonOut {
		printJSON(map[string]any{"ok": true, "deleted": id})
		return
	}
	fmt.Println(green("deleted " + id))
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
