package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/nexusriot/omegagrid-agent-go/internal/llm"
)

// nativeAddendum is appended to the system prompt when the run uses the
// provider's function-calling API. The envelope rules above stay in place:
// a model that writes the JSON envelope as text anyway is still understood.
const nativeAddendum = `

NATIVE TOOLS: every tool and skill above is also offered through the
function-calling interface. Call tools that way. When you have the answer,
reply with the plain-text answer itself (the JSON final form is also accepted).`

// nativeWanted reports whether runs should try native tool calling.
func (s *Service) nativeWanted() bool {
	switch strings.ToLower(strings.TrimSpace(s.ToolMode)) {
	case "envelope", "json":
		return false
	case "native":
		return true
	default: // auto
		return !s.nativeOff.Load()
	}
}

// toolSpecs renders the run's tool table as native tool definitions, sorted
// by name so the request (and provider-side prompt caching) is stable.
func toolSpecs(tools map[string]Skill) []llm.ToolSpec {
	names := make([]string, 0, len(tools))
	for n := range tools {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]llm.ToolSpec, 0, len(names))
	for _, n := range names {
		sch := tools[n].Schema
		props := map[string]any{}
		var required []string
		pnames := make([]string, 0, len(sch.Parameters))
		for pn := range sch.Parameters {
			pnames = append(pnames, pn)
		}
		sort.Strings(pnames)
		for _, pn := range pnames {
			p := sch.Parameters[pn]
			props[pn] = map[string]any{"type": jsonSchemaType(p.Type), "description": p.Description}
			if p.Required {
				required = append(required, pn)
			}
		}
		params := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			params["required"] = required
		}
		desc := sch.Description
		if desc == "" {
			desc = n
		}
		out = append(out, llm.ToolSpec{Name: n, Description: desc, Parameters: params})
	}
	return out
}

// jsonSchemaType maps the loose type names skills declare onto JSON Schema.
func jsonSchemaType(t string) string {
	switch strings.ToLower(t) {
	case "number", "float", "double":
		return "number"
	case "integer", "int":
		return "integer"
	case "boolean", "bool":
		return "boolean"
	case "object", "dict", "map":
		return "object"
	case "array", "list":
		return "array"
	default:
		return "string"
	}
}

// nextMove asks the model for its next step and returns it as an envelope
// map ("tool_call", "tool_calls" or "final"). data is nil when the reply
// could not be understood at all; raw is the reply text for the debug log.
func (s *Service) nextMove(ctx context.Context, state *runState) (data map[string]any, raw string, llmS float64, err error) {
	if state.native {
		tc := s.Chat.(llm.ToolChatClient)
		comp, err := tc.CompleteTools(ctx, state.messages, state.toolSpecs)
		switch {
		case err == nil:
			data, raw := envelopeFromCompletion(comp)
			if data == nil {
				state.debug = append(state.debug, "[llm] native reply had neither tool calls nor text")
			}
			return data, raw, comp.ElapsedSec, nil
		case errors.Is(err, llm.ErrToolsUnsupported) && !strings.EqualFold(strings.TrimSpace(s.ToolMode), "native"):
			if !s.nativeOff.Swap(true) {
				log.Printf("agent: %s rejected native tools (%v); using the JSON envelope from now on", s.Chat.Model(), err)
			}
			state.debug = append(state.debug, "[llm] native tools unsupported; falling back to the JSON envelope")
			state.native = false
			state.messages[0].Content = strings.TrimSuffix(state.messages[0].Content, nativeAddendum)
		default:
			return nil, "", comp.ElapsedSec, err
		}
	}
	raw, llmS, err = llm.Complete(ctx, s.Chat, envelopeMessages(state.messages))
	if err != nil {
		return nil, raw, llmS, err
	}
	data, perr := parseJSONSafely(raw)
	if perr != nil {
		state.debug = append(state.debug, "[llm] unparseable reply: "+truncate(perr.Error(), 200))
		return nil, raw, llmS, nil
	}
	return data, raw, llmS, nil
}

// envelopeFromCompletion maps a native reply onto the agent's envelope
// vocabulary. Tool calls win over text; text that is itself an envelope is
// honoured (models trained on the prompt sometimes write one); any other
// non-empty text is the final answer — in envelope mode the same reply would
// have been an unparseable turn and a "please rephrase" fallback.
func envelopeFromCompletion(c llm.Completion) (map[string]any, string) {
	switch len(c.ToolCalls) {
	case 0:
	case 1:
		tc := c.ToolCalls[0]
		return map[string]any{"type": "tool_call", "tool": tc.Name, "args": tc.Args, "id": tc.ID},
			fmt.Sprintf("[native] tool_call %s", tc.Name)
	default:
		calls := make([]any, 0, len(c.ToolCalls))
		names := make([]string, 0, len(c.ToolCalls))
		for _, tc := range c.ToolCalls {
			calls = append(calls, map[string]any{"tool": tc.Name, "args": tc.Args, "id": tc.ID})
			names = append(names, tc.Name)
		}
		return map[string]any{"type": "tool_calls", "calls": calls},
			"[native] tool_calls " + strings.Join(names, ",")
	}
	text := strings.TrimSpace(c.Content)
	if text == "" {
		return nil, ""
	}
	if data, err := parseJSONSafely(text); err == nil {
		if t, _ := data["type"].(string); t != "" {
			return data, text
		}
	}
	return map[string]any{"type": "final", "answer": text}, text
}

// toolTurn records one executed step — the model's request and the results —
// in the message list, in the shape the current mode needs. Envelope mode
// replays the envelope JSON and one tool message; native mode replays the
// call ids so each result is tied to its call, which OpenAI-style APIs
// require.
func toolTurn(state *runState, data map[string]any, results []batchResult) []llm.Message {
	if !state.native {
		assistantJSON, err := json.Marshal(data)
		if err != nil {
			assistantJSON = []byte(`{"type":"tool_call"}`)
		}
		var payload any
		if t, _ := data["type"].(string); t == "tool_call" && len(results) == 1 {
			payload = results[0].Result
		} else {
			all := make([]any, 0, len(results))
			for _, r := range results {
				all = append(all, r.Result)
			}
			payload = all
		}
		toolJSON, err := json.Marshal(payload)
		if err != nil {
			toolJSON = []byte(`{"error":"marshal failed"}`)
		}
		return []llm.Message{
			{Role: "assistant", Content: string(assistantJSON)},
			{Role: "tool", Content: capToolContent(string(toolJSON))},
		}
	}
	calls := make([]llm.ToolCall, len(results))
	out := make([]llm.Message, 0, len(results)+1)
	out = append(out, llm.Message{Role: "assistant"})
	for i, r := range results {
		id := r.Call.ID
		if id == "" {
			id = fmt.Sprintf("call_%d_%d", r.Call.Step, i)
		}
		calls[i] = llm.ToolCall{ID: id, Name: r.Call.Name, Args: r.Call.Args}
		b, err := json.Marshal(r.Result)
		if err != nil {
			b = []byte(`{"error":"marshal failed"}`)
		}
		out = append(out, llm.Message{Role: "tool", ToolCallID: id, Name: r.Call.Name, Content: capToolContent(string(b))})
	}
	out[0].ToolCalls = calls
	return out
}

// envelopeMessages renders native-shaped turns (left behind when auto mode
// fell back mid-run) as the envelope text the JSON protocol expects.
func envelopeMessages(msgs []llm.Message) []llm.Message {
	native := false
	for _, m := range msgs {
		if len(m.ToolCalls) > 0 {
			native = true
			break
		}
	}
	if !native {
		return msgs
	}
	out := make([]llm.Message, 0, len(msgs))
	for _, m := range msgs {
		if len(m.ToolCalls) == 0 {
			out = append(out, llm.Message{Role: m.Role, Content: m.Content})
			continue
		}
		var env map[string]any
		if len(m.ToolCalls) == 1 {
			env = map[string]any{"type": "tool_call", "tool": m.ToolCalls[0].Name, "args": m.ToolCalls[0].Args}
		} else {
			calls := make([]any, 0, len(m.ToolCalls))
			for _, c := range m.ToolCalls {
				calls = append(calls, map[string]any{"tool": c.Name, "args": c.Args})
			}
			env = map[string]any{"type": "tool_calls", "calls": calls}
		}
		b, _ := json.Marshal(env)
		out = append(out, llm.Message{Role: "assistant", Content: string(b)})
	}
	return out
}
