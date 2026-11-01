package agent

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/nexusriot/omegagrid-agent-go/internal/llm"
)

// nativeLLM is a ToolChatClient replaying canned completions. unsupported
// makes CompleteTools refuse like an Ollama model without tool support;
// envelope replies then come from text.
type nativeLLM struct {
	mu          sync.Mutex
	completions []llm.Completion
	text        []string
	unsupported bool
	toolCalls   int
	envCalls    int
	seen        [][]llm.Message
	specs       []llm.ToolSpec
}

func (n *nativeLLM) CompleteJSON(msgs []llm.Message) (string, float64, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.envCalls++
	n.seen = append(n.seen, msgs)
	if len(n.text) == 0 {
		return `{"type":"final","answer":"envelope done"}`, 0, nil
	}
	r := n.text[0]
	n.text = n.text[1:]
	return r, 0, nil
}

func (n *nativeLLM) CompleteTools(_ context.Context, msgs []llm.Message, specs []llm.ToolSpec) (llm.Completion, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.toolCalls++
	n.seen = append(n.seen, append([]llm.Message(nil), msgs...))
	n.specs = specs
	if n.unsupported {
		return llm.Completion{}, fmt.Errorf("%w: llama3 does not support tools", llm.ErrToolsUnsupported)
	}
	if len(n.completions) == 0 {
		return llm.Completion{Content: "native done"}, nil
	}
	c := n.completions[0]
	n.completions = n.completions[1:]
	return c, nil
}

func (n *nativeLLM) Model() string   { return "native-llm" }
func (n *nativeLLM) BaseURL() string { return "" }

func TestNativeToolCallRunsToolAndTiesResult(t *testing.T) {
	chat := &nativeLLM{completions: []llm.Completion{
		{ToolCalls: []llm.ToolCall{{ID: "call_a", Name: "echo", Args: map[string]any{"x": "hi"}}}},
		{Content: "The tool said hi."},
	}}
	svc := newMinimalService(t, chat)
	rec := &recordingSkill{result: map[string]any{"echoed": "hi"}}
	svc.NativeSkills = map[string]Skill{"echo": rec.native("echo")}

	res, err := svc.Run(RunRequest{Query: "say hi", MaxSteps: 4})
	if err != nil {
		t.Fatal(err)
	}
	if res.Answer != "The tool said hi." || res.Meta["tool_mode"] != "native" {
		t.Fatalf("answer=%q meta=%v", res.Answer, res.Meta)
	}
	if rec.count() != 1 || rec.calls[0]["x"] != "hi" {
		t.Fatalf("tool calls = %v", rec.calls)
	}
	second := chat.seen[1]
	var call, result *llm.Message
	for i := range second {
		if len(second[i].ToolCalls) > 0 {
			call = &second[i]
		}
		if second[i].Role == "tool" {
			result = &second[i]
		}
	}
	if call == nil || call.ToolCalls[0].ID != "call_a" || result == nil || result.ToolCallID != "call_a" || !strings.Contains(result.Content, "echoed") {
		t.Fatalf("second turn did not tie the result to the call: %+v", second)
	}
	var names []string
	var vectorAdd llm.ToolSpec
	for _, sp := range chat.specs {
		names = append(names, sp.Name)
		if sp.Name == "vector_add" {
			vectorAdd = sp
		}
	}
	if !sort.StringsAreSorted(names) || !slices.Contains(names, "echo") {
		t.Fatalf("tool specs = %v, want a sorted list including echo", names)
	}
	if req, _ := vectorAdd.Parameters["required"].([]string); len(req) != 1 || req[0] != "text" {
		t.Fatalf("vector_add schema = %v", vectorAdd.Parameters)
	}
}

func TestNativeBatchKeepsEachCallID(t *testing.T) {
	chat := &nativeLLM{completions: []llm.Completion{
		{ToolCalls: []llm.ToolCall{
			{ID: "c1", Name: "echo", Args: map[string]any{"x": "1"}},
			{ID: "c2", Name: "echo", Args: map[string]any{"x": "2"}},
		}},
		{Content: "both done"},
	}}
	svc := newMinimalService(t, chat)
	svc.ParallelEnabled = true
	rec := &recordingSkill{result: "ok"}
	svc.NativeSkills = map[string]Skill{"echo": rec.native("echo")}
	res, err := svc.Run(RunRequest{Query: "two", MaxSteps: 4})
	if err != nil || res.Answer != "both done" {
		t.Fatalf("res=%v err=%v", res, err)
	}
	var ids []string
	for _, m := range chat.seen[1] {
		if m.Role == "tool" {
			ids = append(ids, m.ToolCallID)
		}
	}
	if strings.Join(ids, ",") != "c1,c2" {
		t.Fatalf("tool result ids = %v, want c1,c2", ids)
	}
}

// Auto mode: a backend that rejects tools switches the process to the
// envelope protocol — this run and every later one — instead of failing.
func TestAutoModeFallsBackWhenToolsUnsupported(t *testing.T) {
	chat := &nativeLLM{unsupported: true, text: []string{`{"type":"final","answer":"from envelope"}`}}
	svc := newMinimalService(t, chat)
	res, err := svc.Run(RunRequest{Query: "q", MaxSteps: 3})
	if err != nil || res.Answer != "from envelope" {
		t.Fatalf("res=%v err=%v", res, err)
	}
	if res.Meta["tool_mode"] != "envelope" {
		t.Fatalf("tool_mode = %v", res.Meta["tool_mode"])
	}
	if strings.Contains(chat.seen[len(chat.seen)-1][0].Content, "NATIVE TOOLS") {
		t.Fatal("envelope retry still carries the native prompt addendum")
	}
	if _, err := svc.Run(RunRequest{Query: "q2", MaxSteps: 3}); err != nil {
		t.Fatal(err)
	}
	if chat.toolCalls != 1 {
		t.Fatalf("CompleteTools called %d times; the fallback must stick", chat.toolCalls)
	}

	// Forced native mode surfaces the refusal instead.
	strict := newMinimalService(t, &nativeLLM{unsupported: true})
	strict.ToolMode = "native"
	if _, err := strict.Run(RunRequest{Query: "q", MaxSteps: 3}); err == nil {
		t.Fatal("AGENT_TOOL_MODE=native hid a tools-unsupported error")
	}
}

func TestEnvelopeModeIgnoresToolSupport(t *testing.T) {
	chat := &nativeLLM{text: []string{`{"type":"final","answer":"env"}`}}
	svc := newMinimalService(t, chat)
	svc.ToolMode = "envelope"
	res, err := svc.Run(RunRequest{Query: "q", MaxSteps: 3})
	if err != nil || res.Answer != "env" || chat.toolCalls != 0 {
		t.Fatalf("res=%v err=%v toolCalls=%d", res, err, chat.toolCalls)
	}
}

func TestEnvelopeFromCompletion(t *testing.T) {
	cases := []struct {
		in       llm.Completion
		wantType string
		want     string
	}{
		{llm.Completion{Content: "plain words"}, "final", "plain words"},
		{llm.Completion{Content: `{"type":"tool_call","tool":"t","args":{}}`}, "tool_call", ""},
		{llm.Completion{Content: "ignored", ToolCalls: []llm.ToolCall{{ID: "x", Name: "t"}}}, "tool_call", ""},
	}
	for _, c := range cases {
		data, _ := envelopeFromCompletion(c.in)
		if data["type"] != c.wantType {
			t.Errorf("%+v → %v", c.in, data)
		}
		if c.want != "" && data["answer"] != c.want {
			t.Errorf("answer = %v", data["answer"])
		}
	}
	if data, _ := envelopeFromCompletion(llm.Completion{Content: "  "}); data != nil {
		t.Errorf("empty reply → %v, want nil (unparseable)", data)
	}
}

// Native turns left in the history when auto mode falls back mid-run must
// reach the envelope protocol as envelope text, not as empty assistant turns.
func TestEnvelopeMessagesFlattensNativeTurns(t *testing.T) {
	in := []llm.Message{
		{Role: "user", Content: "q"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "a", Name: "t", Args: map[string]any{"k": "v"}}}},
		{Role: "tool", ToolCallID: "a", Content: `{"r":1}`},
	}
	out := envelopeMessages(in)
	if !strings.Contains(out[1].Content, `"tool":"t"`) || out[2].Content != `{"r":1}` {
		t.Fatalf("flattened = %+v", out)
	}
}
