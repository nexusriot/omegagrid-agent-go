package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nexusriot/omegagrid-agent-go/internal/scheduler"
)

func runSchedule(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: omega schedule <list|create|enable|disable|run|history|delete> [flags]")
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
	case "enable":
		scheduleToggle(rest, true)
	case "disable":
		scheduleToggle(rest, false)
	case "run":
		scheduleRun(rest)
	case "history", "runs":
		scheduleRuns(rest)
	default:
		fatalf("schedule: unknown subcommand %q", sub)
	}
}

func scheduleList(args []string) {
	fs := flag.NewFlagSet("schedule list", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "JSON output")
	parseArgs(fs, args)

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
		fmt.Printf("   %s\n", grey(taskDetail(t)))
		if t.Prompt != "" {
			fmt.Printf("   prompt: %s\n", firstLine(t.Prompt, 80))
		}
	}
}

// taskDetail is the second line of a task in `schedule list`: schedule, zone
// and the options that change when or how often it runs.
func taskDetail(t *scheduler.Task) string {
	tz := t.Timezone
	if tz == "" {
		tz = "UTC"
	}
	parts := []string{"cron: " + t.CronExpr + " (" + tz + ")"}
	if t.OneShot {
		parts = append(parts, "one-shot")
	}
	if t.MaxRetries > 0 {
		parts = append(parts, fmt.Sprintf("retries: %d every %ds+", t.MaxRetries, t.RetryDelaySec))
	}
	if t.NextRetryAt != nil {
		parts = append(parts, "retry #"+strconv.Itoa(t.RetryAttempt)+" at "+
			time.Unix(int64(*t.NextRetryAt), 0).Format("2006-01-02 15:04:05"))
	}
	return strings.Join(parts, "  ")
}

func scheduleCreate(args []string) {
	fs := flag.NewFlagSet("schedule create", flag.ExitOnError)
	name := fs.String("name", "", "Task name (required)")
	cron := fs.String("cron", "", "5-field cron expression (required)")
	skill := fs.String("skill", "", "Skill to run (this or --prompt)")
	prompt := fs.String("prompt", "", "Agent prompt to answer on each run (this or --skill)")
	tz := fs.String("tz", "", "IANA time zone the cron is in, e.g. Europe/Berlin (default UTC)")
	retries := fs.Int("retries", 0, "Re-run a failed run up to N times (0-10)")
	retryDelay := fs.Int("retry-delay", 60, "Seconds before the first retry, doubling after")
	oneShot := fs.Bool("one-shot", false, "Run once then auto-disable")
	var argPairs multiFlag
	fs.Var(&argPairs, "arg", "Skill argument in key=value form (repeatable)")
	jsonOut := fs.Bool("json", false, "JSON output")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: omega schedule create --name NAME --cron EXPR (--skill SKILL [--arg k=v ...] | --prompt TEXT) [--tz ZONE] [--retries N] [--retry-delay SEC] [--one-shot]")
		fs.PrintDefaults()
	}
	parseArgs(fs, args)

	if *name == "" || *cron == "" || (*skill == "" && *prompt == "") {
		fs.Usage()
		os.Exit(1)
	}
	skillArgs, err := parseKeyValues(argPairs)
	if err != nil {
		fatalf("schedule create: %v", err)
	}

	spec := scheduler.TaskSpec{
		Name: *name, CronExpr: *cron, Skill: *skill, Prompt: *prompt, Args: skillArgs,
		OneShot: *oneShot, Timezone: *tz, MaxRetries: *retries, RetryDelaySec: *retryDelay,
	}
	if err := createScheduleTask(spec); err != nil {
		fatalf("schedule create: %v", err)
	}
	if *jsonOut {
		printJSON(map[string]any{"ok": true})
		return
	}
	fmt.Println(green("task created"))
}

// taskIDArg parses the single task-ID positional of a schedule subcommand.
func taskIDArg(fs *flag.FlagSet, args []string, what string) int64 {
	pos := parseArgs(fs, args)
	idStr := argAt(pos, 0)
	if idStr == "" {
		fatalf("schedule %s: task ID required", what)
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		fatalf("schedule %s: invalid ID %q", what, idStr)
	}
	return id
}

func scheduleToggle(args []string, enabled bool) {
	what := "disable"
	if enabled {
		what = "enable"
	}
	fs := flag.NewFlagSet("schedule "+what, flag.ExitOnError)
	id := taskIDArg(fs, args, what)
	if err := setScheduleEnabled(id, enabled); err != nil {
		fatalf("schedule %s: %v", what, err)
	}
	fmt.Println(green(fmt.Sprintf("task %d %sd", id, what)))
}

func scheduleRun(args []string) {
	fs := flag.NewFlagSet("schedule run", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "JSON output")
	id := taskIDArg(fs, args, "run")
	if !isRemote() {
		defer closeLocal()
	}
	run, err := runScheduleTask(id)
	if err != nil {
		fatalf("schedule run: %v", err)
	}
	if *jsonOut {
		printJSON(run)
		return
	}
	status := green("ok")
	if !run.OK {
		status = red("failed")
	}
	fmt.Printf("%s in %.2fs\n%s\n", status, run.FinishedAt-run.StartedAt, run.Result)
	if !run.OK {
		os.Exit(1)
	}
}

func scheduleRuns(args []string) {
	fs := flag.NewFlagSet("schedule history", flag.ExitOnError)
	limit := fs.Int("limit", 20, "Max runs to show")
	jsonOut := fs.Bool("json", false, "JSON output")
	id := taskIDArg(fs, args, "history")
	runs, err := scheduleHistory(id, *limit)
	if err != nil {
		fatalf("schedule history: %v", err)
	}
	if *jsonOut {
		printJSON(runs)
		return
	}
	if len(runs) == 0 {
		fmt.Println(grey("(no runs yet)"))
		return
	}
	for _, r := range runs {
		status := green("ok    ")
		if !r.OK {
			status = red("failed")
		}
		started := time.Unix(int64(r.StartedAt), 0).Format("2006-01-02 15:04:05")
		fmt.Printf("%s  %s  %-6s #%d  %s\n", started, status, r.Trigger, r.Attempt, firstLine(r.Result, 80))
	}
}

func firstLine(s string, n int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	if r := []rune(s); len(r) > n {
		s = string(r[:n]) + "…"
	}
	return s
}

func scheduleDelete(args []string) {
	fs := flag.NewFlagSet("schedule delete", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "JSON output")
	pos := parseArgs(fs, args)

	idStr := argAt(pos, 0)
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
