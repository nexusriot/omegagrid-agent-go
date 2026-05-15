package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/nexusriot/omegagrid-agent-go/internal/agent"
)

func runAsk(args []string) {
	fs := flag.NewFlagSet("ask", flag.ExitOnError)
	stream := fs.Bool("stream", false, "Stream events in real time")
	sessionID := fs.Int("session", 0, "Reuse existing session ID")
	maxSteps := fs.Int("max-steps", 0, "Override max agent steps")
	jsonOut := fs.Bool("json", false, "Output full JSON result")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: omega ask [flags] \"question\"")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	query := fs.Arg(0)
	if query == "" {
		fs.Usage()
		os.Exit(1)
	}

	if isRemote() {
		runAskRemote(query, *sessionID, *maxSteps, *stream, *jsonOut)
	} else {
		runAskLocal(query, *sessionID, *maxSteps, *stream, *jsonOut)
	}
}

func runAskLocal(query string, sessionID, maxSteps int, stream, jsonOut bool) {
	defer closeLocal()
	svc := getLocal()

	steps := maxSteps
	if steps == 0 {
		steps = 25
	}

	req := agent.RunRequest{
		Query:     query,
		SessionID: sessionID,
		Remember:  true,
		MaxSteps:  steps,
	}

	if stream {
		ch := make(chan agent.Event, 32)
		go svc.Agent.RunStream(req, ch)
		printStreamEvents(ch, jsonOut)
		return
	}

	result, err := svc.Agent.Run(req)
	if err != nil {
		fatalf("agent: %v", err)
	}
	if jsonOut {
		printJSON(result)
		return
	}
	fmt.Println(result.Answer)
}

func runAskRemote(query string, sessionID, maxSteps int, stream, jsonOut bool) {
	body := map[string]any{
		"query":      query,
		"session_id": sessionID,
		"remember":   true,
	}
	if maxSteps > 0 {
		body["max_steps"] = maxSteps
	}

	if stream {
		err := httpStream("/api/query/stream", body, func(event, data string) {
			printSSEEvent(event, data, jsonOut)
		})
		if err != nil {
			fatalf("stream: %v", err)
		}
		return
	}

	var result map[string]any
	if err := httpJSON("POST", "/api/query", body, &result); err != nil {
		fatalf("query: %v", err)
	}
	if jsonOut {
		printJSON(result)
		return
	}
	if a, ok := result["answer"].(string); ok {
		fmt.Println(a)
	}
}

func printStreamEvents(ch <-chan agent.Event, jsonOut bool) {
	for ev := range ch {
		if jsonOut {
			b, _ := json.Marshal(ev)
			fmt.Println(string(b))
			continue
		}
		switch ev.Event {
		case "thinking":
			fmt.Fprintln(os.Stderr, grey(fmt.Sprintf("  [step %d] thinking…", ev.Step)))
		case "tool_call":
			argsJSON, _ := json.Marshal(ev.Args)
			fmt.Fprintln(os.Stderr, cyan(fmt.Sprintf("  → %s(%s)", ev.Tool, string(argsJSON))))
		case "tool_result":
			fmt.Fprintln(os.Stderr, grey(fmt.Sprintf("    ← %s (%.3fs)", ev.Result, ev.ElapsedS)))
		case "final":
			fmt.Println(green(ev.Answer))
		case "error":
			fmt.Fprintln(os.Stderr, yellow("error: "+ev.Error))
		}
	}
}

func printSSEEvent(event, data string, jsonOut bool) {
	if jsonOut {
		fmt.Printf("%s %s\n", event, data)
		return
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return
	}
	switch event {
	case "thinking":
		step, _ := ev["step"].(float64)
		fmt.Fprintf(os.Stderr, "%s\n", grey(fmt.Sprintf("  [step %.0f] thinking…", step)))
	case "tool_call":
		tool, _ := ev["tool"].(string)
		argsJSON, _ := json.Marshal(ev["args"])
		fmt.Fprintf(os.Stderr, "%s\n", cyan(fmt.Sprintf("  → %s(%s)", tool, string(argsJSON))))
	case "tool_result":
		tool, _ := ev["tool"].(string)
		result, _ := ev["result"].(string)
		elapsed, _ := ev["elapsed_s"].(float64)
		fmt.Fprintf(os.Stderr, "%s\n", grey(fmt.Sprintf("    ← %s: %s (%.3fs)", tool, result, elapsed)))
	case "final":
		answer, _ := ev["answer"].(string)
		fmt.Println(green(answer))
	case "error":
		msg, _ := ev["error"].(string)
		fmt.Fprintln(os.Stderr, yellow("error: "+msg))
	}
}
