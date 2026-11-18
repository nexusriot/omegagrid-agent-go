package agent

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/nexusriot/omegagrid-agent-go/internal/llm"
	"github.com/nexusriot/omegagrid-agent-go/internal/memory"
)

// Run traces: one memory.RunRecord per run, so the Activity page can show
// what each run cost and whether tool retrieval offered the tools it needed.

// runTrace is the per-run counters; guarded by runState.mu because parallel
// batch calls update it.
type runTrace struct {
	started      time.Time
	query        string
	firstOffered []string // the prompt's skill list, before find_tools added any
	used         []string
	llmCalls     int
	promptTok    int64
	complTok     int64
	approvalWait time.Duration
}

func (st *runState) addDebug(line string) {
	st.mu.Lock()
	st.debug = append(st.debug, line)
	st.mu.Unlock()
}

func (st *runState) addApprovalWait(d time.Duration) {
	st.mu.Lock()
	st.trace.approvalWait += d
	st.mu.Unlock()
}

func (st *runState) addUsed(name string) {
	st.mu.Lock()
	st.trace.used = append(st.trace.used, name)
	st.mu.Unlock()
}

// llmContext counts one LLM call and returns ctx with a token-usage sink.
func (st *runState) llmContext(ctx context.Context) context.Context {
	st.mu.Lock()
	st.trace.llmCalls++
	st.mu.Unlock()
	return llm.WithUsageSink(ctx, func(u llm.Usage) {
		st.mu.Lock()
		st.trace.promptTok += u.PromptTokens
		st.trace.complTok += u.CompletionTokens
		st.mu.Unlock()
	})
}

// noteOffered snapshots the skills the prompt lists.
func (st *runState) noteOffered() {
	if st.offered == nil {
		return
	}
	names := make([]string, 0, len(st.offered))
	for n := range st.offered {
		if st.skillNames[n] {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	st.trace.firstOffered = names
}

// recordRun stores the trace. outcome is final | fallback | max_steps |
// error | cancelled; meta (may be nil) is the result's meta map.
func (s *Service) recordRun(st *runState, outcome string, steps int, runErr error) {
	if s.Memory == nil || st == nil {
		return
	}
	if runErr != nil && (errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded)) {
		outcome = "cancelled"
	}
	if outcome == "" {
		outcome = "cancelled" // the stream ended without a final event
	}
	st.mu.Lock()
	t := st.trace
	used := append([]string(nil), t.used...)
	st.mu.Unlock()
	rec := memory.RunRecord{
		SessionID:        st.sid,
		TS:               float64(t.started.UnixMilli()) / 1000,
		DurationMS:       time.Since(t.started).Milliseconds(),
		Query:            t.query,
		Namespace:        st.namespace,
		Model:            s.Chat.Model(),
		ToolMode:         "envelope",
		Retrieval:        nonempty(st.retrieval, "off"),
		Offered:          t.firstOffered,
		Used:             used,
		Steps:            steps,
		LLMCalls:         t.llmCalls,
		LLMMS:            int64(st.timings["llm_chat_s_total"] * 1000),
		ToolMS:           int64((st.timings["skill_s_total"] + st.timings["tool_s_total"]) * 1000),
		PromptTokens:     t.promptTok,
		CompletionTokens: t.complTok,
		ApprovalWaitMS:   t.approvalWait.Milliseconds(),
		Unattended:       st.unattended,
		Outcome:          outcome,
	}
	if st.native {
		rec.ToolMode = "native"
	}
	if runErr != nil {
		rec.Error = runErr.Error()
	}
	_, _ = s.Memory.AddRun(rec)
}

// outcomeOf classifies a finished run from its meta.
func outcomeOf(meta map[string]any) string {
	switch {
	case meta["max_steps_hit"] == true:
		return "max_steps"
	case meta["fallback"] == true:
		return "fallback"
	}
	return "final"
}
