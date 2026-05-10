package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

func runSkills(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: omega skills <list|run|describe> [flags]")
		os.Exit(1)
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list":
		skillsList(rest)
	case "run":
		skillsRun(rest)
	case "describe":
		skillsDescribe(rest)
	default:
		fatalf("skills: unknown subcommand %q", sub)
	}
}

func skillsList(args []string) {
	fs := flag.NewFlagSet("skills list", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "JSON output")
	fs.Parse(args)

	list, err := listSkills()
	if err != nil {
		fatalf("skills list: %v", err)
	}
	if *jsonOut {
		printJSON(list)
		return
	}
	for _, s := range list {
		var req, opt []string
		for name, p := range s.Parameters {
			if p.Required {
				req = append(req, name)
			} else {
				opt = append(opt, name)
			}
		}
		sig := strings.Join(req, ", ")
		if len(opt) > 0 {
			sig += " [" + strings.Join(opt, ", ") + "]"
		}
		fmt.Printf("%s(%s)\n  %s\n", cyan(s.Name), sig, grey(s.Description))
	}
}

func skillsDescribe(args []string) {
	fs := flag.NewFlagSet("skills describe", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "JSON output")
	fs.Parse(args)

	name := fs.Arg(0)
	if name == "" {
		fatalf("skills describe: skill name required")
	}
	list, err := listSkills()
	if err != nil {
		fatalf("skills describe: %v", err)
	}
	for _, s := range list {
		if s.Name == name {
			if *jsonOut {
				printJSON(s)
				return
			}
			fmt.Printf("%s\n  %s\n\nParameters:\n", cyan(s.Name), s.Description)
			for pname, p := range s.Parameters {
				req := grey("optional")
				if p.Required {
					req = yellow("required")
				}
				fmt.Printf("  %-20s %s  %s  — %s\n", pname, grey(p.Type), req, p.Description)
			}
			return
		}
	}
	fatalf("skill %q not found", name)
}

func skillsRun(args []string) {
	fs := flag.NewFlagSet("skills run", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "JSON output")
	var argPairs multiFlag
	fs.Var(&argPairs, "arg", "Argument in key=value form (repeatable)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: omega skills run NAME [--arg key=value ...] [--json]")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	name := fs.Arg(0)
	if name == "" {
		fs.Usage()
		os.Exit(1)
	}
	skillArgs, err := parseKeyValues(argPairs)
	if err != nil {
		fatalf("skills run: %v", err)
	}

	result, err := invokeSkill(name, skillArgs)
	if err != nil {
		fatalf("skills run: %v", err)
	}
	if *jsonOut {
		printJSON(result)
		return
	}
	// Pretty-print the result field if present, else the whole map.
	if r, ok := result["result"]; ok {
		printJSON(r)
	} else {
		printJSON(result)
	}
}

// multiFlag is a flag.Value that accumulates repeated --arg values.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ", ") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// parseKeyValues converts ["k=v", "k2=v2"] → map[string]any.
func parseKeyValues(pairs []string) (map[string]any, error) {
	out := make(map[string]any, len(pairs))
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			return nil, fmt.Errorf("invalid key=value pair %q", p)
		}
		out[k] = v
	}
	return out, nil
}
