package memory

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nexusriot/omegagrid-agent-go/internal/config"
)

func newRunsClient(t *testing.T) *Client {
	t.Helper()
	hist, err := newHistoryStore(t.TempDir() + "/h.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hist.close() })
	return &Client{hist: hist, auditMaxBlob: 4096}
}

func TestRunStats(t *testing.T) {
	c := newRunsClient(t)
	add := func(r RunRecord) {
		if _, err := c.AddRun(r); err != nil {
			t.Fatal(err)
		}
	}
	add(RunRecord{SessionID: 1, Query: "tls", Retrieval: "lexical", Offered: []string{"tls_probe", "dns_lookup"},
		Used: []string{"tls_probe", "tls_probe"}, Outcome: "final", DurationMS: 100, LLMCalls: 2, PromptTokens: 50, CompletionTokens: 5})
	add(RunRecord{SessionID: 2, Query: "chart", Retrieval: "lexical", Offered: []string{"dns_lookup"},
		Used: []string{"find_tools", "chart_render"}, Outcome: "final", DurationMS: 300, LLMCalls: 3})
	add(RunRecord{SessionID: 3, Query: "x", Retrieval: "off", Used: []string{"chart_render"}, Outcome: "error", Error: "boom", DurationMS: 200, LLMCalls: 1})

	st, err := c.RunStats(0)
	if err != nil {
		t.Fatal(err)
	}
	if st.Runs != 3 || st.Outcomes["final"] != 2 || st.Outcomes["error"] != 1 || st.RetrievalRuns != 2 ||
		st.PromptTokens != 50 || st.AvgDurationMS != 200 || st.P95DurationMS != 300 || st.AvgLLMCalls != 2 {
		t.Fatalf("stats %+v", st)
	}
	if st.MissRate != 0.5 {
		t.Fatalf("miss rate %v", st.MissRate)
	}
	tools := map[string]ToolRunStats{}
	for _, tr := range st.Tools {
		tools[tr.Tool] = tr
	}
	if tools["chart_render"].Used != 2 || tools["chart_render"].UsedNotOffered != 1 ||
		tools["tls_probe"].Used != 1 || tools["tls_probe"].Offered != 1 || tools["dns_lookup"].Offered != 2 {
		t.Fatalf("tools %+v", tools)
	}
	if st.Tools[0].UsedNotOffered == 0 {
		t.Fatal("misses should sort first")
	}
	r, err := c.GetRun(3)
	if err != nil || r == nil || r.Error != "boom" || len(r.Offered) != 0 {
		t.Fatalf("get %+v %v", r, err)
	}
	if r, _ := c.GetRun(99); r != nil {
		t.Fatal("missing run returned")
	}
}

func TestApprovalLogFilters(t *testing.T) {
	c := newRunsClient(t)
	for i, by := range []string{"web", "rule", "timeout"} {
		if err := c.AddApproval(ApprovalRecord{SessionID: 1 + i%2, Tool: "shell_command", Args: map[string]any{"command": "ls"},
			Approved: by != "timeout", By: by, WaitMS: int64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	all, total, _ := c.ListApprovals(ApprovalFilter{})
	if total != 3 || all[0].By != "timeout" {
		t.Fatalf("all %+v", all)
	}
	if args, _ := all[0].Args.(map[string]any); args["command"] != "ls" {
		t.Fatalf("args %v", all[0].Args)
	}
	denied, _, _ := c.ListApprovals(ApprovalFilter{OnlyDenied: true})
	if len(denied) != 1 || denied[0].Approved {
		t.Fatalf("denied %+v", denied)
	}
	if _, n, _ := c.ListApprovals(ApprovalFilter{SessionID: 2}); n != 1 {
		t.Fatalf("session filter %d", n)
	}
	if _, n, _ := c.ListApprovals(ApprovalFilter{By: "rule"}); n != 1 {
		t.Fatalf("by filter %d", n)
	}
}

func TestSnippetAround(t *testing.T) {
	long := strings.Repeat("alpha ", 40) + "NEEDLE in   the\nhaystack " + strings.Repeat("omega ", 40)
	got := snippetAround(long, "needle", 60)
	if !strings.Contains(got, "NEEDLE in the haystack") || !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") {
		t.Fatalf("snippet %q", got)
	}
	if got := snippetAround("short  text", "x", 60); got != "short text" {
		t.Fatalf("short %q", got)
	}
	// Not found: the start of the text.
	if got := snippetAround(strings.Repeat("ab ", 50), "zz", 20); strings.HasPrefix(got, "…") {
		t.Fatalf("missing word %q", got)
	}
}

func TestRunJanitorStopsWithContext(t *testing.T) {
	c, err := New(config.Config{
		AgentDB: filepath.Join(t.TempDir(), "a.sqlite3"), VectorDir: filepath.Join(t.TempDir(), "v"),
		VectorCollection: "t", OllamaURL: "http://127.0.0.1:1", OllamaEmbedModel: "e", OllamaTimeoutSec: 0.1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.RunJanitor(ctx, time.Millisecond); close(done) }()
	time.Sleep(10 * time.Millisecond) // a few ticks
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("janitor did not stop")
	}
	if s, err := c.ListSessions(5); err != nil || len(s) != 0 {
		t.Fatalf("ListSessions %v %v", s, err)
	}
}
