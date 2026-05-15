package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"
)

func runSchedule(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: omega schedule <list|create|delete> [flags]")
		os.Exit(1)
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list":
		scheduleList(rest)
	case "create":
		scheduleCreate(rest)
	case "delete", "rm":
		scheduleDelete(rest)
	default:
		fatalf("schedule: unknown subcommand %q", sub)
	}
}

func scheduleList(args []string) {
	fs := flag.NewFlagSet("schedule list", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "JSON output")
	fs.Parse(args)

	tasks, err := listSchedule()
	if err != nil {
		fatalf("schedule list: %v", err)
	}
	if *jsonOut {
		printJSON(tasks)
		return
	}
	if len(tasks) == 0 {
		fmt.Println(grey("(no tasks)"))
		return
	}
	for _, t := range tasks {
		status := green("enabled")
		if !t.Enabled {
			status = grey("disabled")
		}
		var lastRun string
		if t.LastRunAt != nil {
			lastRun = time.Unix(int64(*t.LastRunAt), 0).Format("2006-01-02 15:04:05")
		}
		fmt.Printf("%s  %s  %s  skill=%s  runs=%d  last=%s\n",
			cyan(fmt.Sprintf("[%d]", t.ID)), t.Name, status, t.Skill, t.RunCount, lastRun)
		fmt.Printf("   cron: %s\n", grey(t.CronExpr))
	}
}

func scheduleCreate(args []string) {
	fs := flag.NewFlagSet("schedule create", flag.ExitOnError)
	name := fs.String("name", "", "Task name (required)")
	cron := fs.String("cron", "", "5-field cron expression (required)")
	skill := fs.String("skill", "", "Skill to run (required)")
	var argPairs multiFlag
	fs.Var(&argPairs, "arg", "Skill argument in key=value form (repeatable)")
	jsonOut := fs.Bool("json", false, "JSON output")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: omega schedule create --name NAME --cron EXPR --skill SKILL [--arg k=v ...]")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	if *name == "" || *cron == "" || *skill == "" {
		fs.Usage()
		os.Exit(1)
	}
	skillArgs, err := parseKeyValues(argPairs)
	if err != nil {
		fatalf("schedule create: %v", err)
	}

	if err := createScheduleTask(*name, *cron, *skill, skillArgs); err != nil {
		fatalf("schedule create: %v", err)
	}
	if *jsonOut {
		printJSON(map[string]any{"ok": true})
		return
	}
	fmt.Println(green("task created"))
}

func scheduleDelete(args []string) {
	fs := flag.NewFlagSet("schedule delete", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "JSON output")
	fs.Parse(args)

	idStr := fs.Arg(0)
	if idStr == "" {
		fatalf("schedule delete: task ID required")
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		fatalf("schedule delete: invalid ID %q", idStr)
	}
	if err := deleteScheduleTask(id); err != nil {
		fatalf("schedule delete: %v", err)
	}
	if *jsonOut {
		printJSON(map[string]any{"ok": true})
		return
	}
	fmt.Println(green(fmt.Sprintf("task %d deleted", id)))
}
