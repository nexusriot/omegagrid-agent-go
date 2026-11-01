package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"time"
)

func runMemory(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: omega memory <search|add|list|edit|delete|namespaces|export|import> [flags]")
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
	case "edit":
		memoryEdit(rest)
	case "namespaces", "ns":
		memoryNamespacesCmd(rest)
	case "export":
		memoryExport(rest)
	case "import":
		memoryImport(rest)
	default:
		fatalf("memory: unknown subcommand %q", sub)
	}
}

func memoryList(args []string) {
	fs := flag.NewFlagSet("memory list", flag.ExitOnError)
	limit := fs.Int("limit", 100, "Max results (0 = all)")
	offset := fs.Int("offset", 0, "Skip the first N results")
	jsonOut := fs.Bool("json", false, "JSON output")
	var namespace optionalString
	fs.Var(&namespace, "namespace", "Only this namespace (\"\" = the shared one); default all")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: omega memory list [-limit N] [-offset N] [--namespace NS] [--json]")
		fs.PrintDefaults()
	}
	parseArgs(fs, args)

	hits, total, err := listMemories(*limit, *offset, namespace.ptr())
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
	pos := parseArgs(fs, args)

	id := argAt(pos, 0)
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
	var namespaces multiFlag
	fs.Var(&namespaces, "namespace", "Search only this namespace (repeatable; \"\" = the shared one); default all")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: omega memory search \"query\" [-k N] [--namespace NS ...] [--json]")
		fs.PrintDefaults()
	}
	pos := parseArgs(fs, args)

	query := argAt(pos, 0)
	if query == "" {
		fs.Usage()
		os.Exit(1)
	}

	var ns []string
	if len(namespaces) > 0 {
		ns = namespaces
	}
	hits, err := searchMemory(query, *k, ns)
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
	namespace := fs.String("namespace", "", "Namespace to store in (default: the shared one)")
	ttl := fs.Duration("ttl", 0, "Forget after this long, e.g. 72h (default MEMORY_DEFAULT_TTL_DAYS; negative = never)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: omega memory add \"text\" [--meta key=value ...] [--namespace NS] [--ttl 72h] [--json]")
		fs.PrintDefaults()
	}
	pos := parseArgs(fs, args)

	text := argAt(pos, 0)
	if text == "" {
		fs.Usage()
		os.Exit(1)
	}
	meta, err := parseKeyValues(metaPairs)
	if err != nil {
		fatalf("memory add: %v", err)
	}

	res, err := addMemory(text, meta, *namespace, *ttl)
	if err != nil {
		fatalf("memory add: %v", err)
	}
	if *jsonOut {
		printJSON(res)
		return
	}
	if res.Skipped {
		fmt.Println(grey(fmt.Sprintf("skipped (%s): %s", res.Reason, res.MemoryID)))
		return
	}
	fmt.Println(green("stored " + res.MemoryID))
}

func memoryEdit(args []string) {
	fs := flag.NewFlagSet("memory edit", flag.ExitOnError)
	var text optionalString
	fs.Var(&text, "text", "New text (re-embedded)")
	var metaPairs multiFlag
	fs.Var(&metaPairs, "meta", "Set metadata key=value (repeatable); key= removes the key")
	var ttl optionalDuration
	fs.Var(&ttl, "ttl", "New lifetime from now, e.g. 720h; 0 removes the expiry")
	jsonOut := fs.Bool("json", false, "JSON output")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: omega memory edit ID [--text \"...\"] [--meta key=value ...] [--ttl 720h] [--json]")
		fs.PrintDefaults()
	}
	pos := parseArgs(fs, args)
	id := argAt(pos, 0)
	if id == "" || (text.ptr() == nil && len(metaPairs) == 0 && ttl.ptr() == nil) {
		fs.Usage()
		os.Exit(1)
	}
	meta, err := parseKeyValues(metaPairs)
	if err != nil {
		fatalf("memory edit: %v", err)
	}
	for k, v := range meta {
		if v == "" {
			meta[k] = nil
		}
	}
	hit, err := updateMemory(id, text.ptr(), meta, ttl.ptr())
	if err != nil {
		fatalf("memory edit: %v", err)
	}
	if *jsonOut {
		printJSON(hit)
		return
	}
	fmt.Println(green("updated " + id))
}

func memoryNamespacesCmd(args []string) {
	fs := flag.NewFlagSet("memory namespaces", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "JSON output")
	parseArgs(fs, args)
	counts, err := memoryNamespaces()
	if err != nil {
		fatalf("memory namespaces: %v", err)
	}
	if *jsonOut {
		printJSON(counts)
		return
	}
	names := make([]string, 0, len(counts))
	for ns := range counts {
		names = append(names, ns)
	}
	sort.Strings(names)
	for _, ns := range names {
		label := ns
		if label == "" {
			label = "(shared)"
		}
		fmt.Printf("%-40s %d\n", label, counts[ns])
	}
}

func memoryExport(args []string) {
	fs := flag.NewFlagSet("memory export", flag.ExitOnError)
	withEmb := fs.Bool("embeddings", false, "Include embeddings (reusable only with the same embeddings model)")
	out := fs.String("o", "", "Write to this file instead of stdout")
	parseArgs(fs, args)
	w := io.Writer(os.Stdout)
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			fatalf("memory export: %v", err)
		}
		defer f.Close()
		w = f
	}
	if err := exportMemories(w, *withEmb); err != nil {
		fatalf("memory export: %v", err)
	}
}

func memoryImport(args []string) {
	fs := flag.NewFlagSet("memory import", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "JSON output")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: omega memory import FILE.jsonl (\"-\" = stdin) [--json]")
		fs.PrintDefaults()
	}
	pos := parseArgs(fs, args)
	path := argAt(pos, 0)
	if path == "" {
		fs.Usage()
		os.Exit(1)
	}
	r := io.Reader(os.Stdin)
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			fatalf("memory import: %v", err)
		}
		defer f.Close()
		r = f
	}
	rep, err := importMemories(r)
	if err != nil {
		fatalf("memory import: %v", err)
	}
	if *jsonOut {
		printJSON(rep)
		return
	}
	fmt.Printf("imported %d, duplicates %d, already present %d, failed %d\n", rep.Imported, rep.Duplicates, rep.Existing, rep.Failed)
	for _, e := range rep.Errors {
		fmt.Println(grey("  " + e))
	}
}

// optionalString is a string flag that remembers whether it was given, so
// "--namespace ”" (the shared namespace) differs from no flag at all.
type optionalString struct {
	v   string
	set bool
}

func (o *optionalString) String() string { return o.v }
func (o *optionalString) Set(s string) error {
	o.v, o.set = s, true
	return nil
}
func (o *optionalString) ptr() *string {
	if !o.set {
		return nil
	}
	return &o.v
}

// optionalDuration is the same for a duration.
type optionalDuration struct {
	v   time.Duration
	set bool
}

func (o *optionalDuration) String() string { return o.v.String() }
func (o *optionalDuration) Set(s string) error {
	d, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	o.v, o.set = d, true
	return nil
}
func (o *optionalDuration) ptr() *time.Duration {
	if !o.set {
		return nil
	}
	return &o.v
}
