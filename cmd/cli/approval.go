package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/nexusriot/omegagrid-agent-go/internal/agent"
)

// stdinIsTTY reports whether stdin is a terminal someone can answer on.
func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// askApproval prints a pending tool call on stderr and reads y/N/a from
// stdin ("a" = approve this tool for the rest of the session). answered is
// false when stdin ended without a line (it is not really a terminal, e.g.
// /dev/null, which also reports as a character device); ctx cancellation
// (Ctrl-C) is an answered "no".
func askApproval(ctx context.Context, tool string, args map[string]any, why string) (approved bool, scope string, answered bool) {
	a, _ := json.MarshalIndent(args, "    ", "  ")
	fmt.Fprintln(os.Stderr, yellow(fmt.Sprintf("  ? approval needed: %s", tool)))
	fmt.Fprintf(os.Stderr, "    %s\n", a)
	if why != "" {
		fmt.Fprintf(os.Stderr, "    why: %s\n", why)
	}
	fmt.Fprint(os.Stderr, yellow("    run it? [y]es / [N]o / [a]lways this session: "))
	type reply struct {
		s   string
		eof bool
	}
	line := make(chan reply, 1)
	go func() {
		s, err := bufio.NewReader(os.Stdin).ReadString('\n')
		line <- reply{s, err != nil && s == ""}
	}()
	select {
	case r := <-line:
		if r.eof {
			fmt.Fprintln(os.Stderr)
			return false, "", false
		}
		switch strings.ToLower(strings.TrimSpace(r.s)) {
		case "y", "yes":
			return true, "", true
		case "a", "always":
			return true, agent.ScopeSession, true
		}
		return false, "", true
	case <-ctx.Done():
		fmt.Fprintln(os.Stderr)
		return false, "", true
	}
}

// localApprovalPrompt answers approvals for an in-process run: on a terminal
// it asks; without one nobody could answer, so the call is denied at once
// instead of hanging until the timeout.
func localApprovalPrompt(ctx context.Context, req agent.ApprovalRequest) (agent.ApprovalDecision, bool) {
	if !stdinIsTTY() {
		return agent.ApprovalDecision{Approved: false, By: "policy",
			Reason: "no terminal to ask for approval (omega local mode without a TTY)"}, true
	}
	ok, scope, answered := askApproval(ctx, req.Tool, req.Args, req.Why)
	if !answered {
		return agent.ApprovalDecision{Approved: false, By: "policy", Reason: "no answer on stdin"}, true
	}
	return agent.ApprovalDecision{Approved: ok, By: "cli", Scope: scope}, true
}

// resolveRemoteApproval handles an approval_required SSE event in remote
// mode: ask on the terminal and post the answer to the gateway.
func resolveRemoteApproval(data string) {
	var ev struct {
		Approval *agent.ApprovalRequest `json:"approval"`
	}
	if json.Unmarshal([]byte(data), &ev) != nil || ev.Approval == nil {
		return
	}
	hint := func() {
		fmt.Fprintln(os.Stderr, yellow(fmt.Sprintf("  ? %s needs approval (id %s): approve it in the web UI, Telegram or POST /api/approvals/%s",
			ev.Approval.Tool, ev.Approval.ID, ev.Approval.ID)))
	}
	if !stdinIsTTY() {
		hint()
		return
	}
	ok, scope, answered := askApproval(interruptContext(), ev.Approval.Tool, ev.Approval.Args, ev.Approval.Why)
	if !answered {
		hint()
		return
	}
	body := map[string]any{"approved": ok, "by": "cli"}
	if scope != "" {
		body["scope"] = scope
	}
	var out map[string]any
	if err := httpJSON("POST", "/api/approvals/"+ev.Approval.ID, body, &out); err != nil {
		fmt.Fprintln(os.Stderr, yellow("  approval: "+err.Error()))
	}
}
