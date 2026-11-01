package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"
)

// ToolSpec is one tool offered to the model through the provider's native
// function-calling interface. Parameters is a JSON Schema object.
type ToolSpec struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// ToolCall is one function call the model asked for. ID ties the call to its
// result message; providers that issue no ids (Ollama) get generated ones.
type ToolCall struct {
	ID   string
	Name string
	Args map[string]any
}

// Completion is a native tool-calling reply: text, tool calls, or both.
type Completion struct {
	Content    string
	ToolCalls  []ToolCall
	ElapsedSec float64
}

// ToolChatClient is implemented by backends that support native tool
// calling. The agent prefers it (AGENT_TOOL_MODE=auto|native) over asking
// the model to hand-write a JSON envelope, which small and narrating models
// get wrong often enough to fail whole turns.
type ToolChatClient interface {
	CompleteTools(ctx context.Context, messages []Message, tools []ToolSpec) (Completion, error)
}

// ErrToolsUnsupported is returned (wrapped) when the backend rejects the
// tools parameter — an Ollama model without tool support, or a relay that
// does not implement it. In auto mode the agent falls back to the envelope
// protocol for the rest of the process.
var ErrToolsUnsupported = errors.New("model does not support native tool calling")

// toolsUnsupportedRE recognises the 400 bodies providers send for that case
// ("llama3 does not support tools", "tool use is not supported", "tools is
// not supported for this model").
var toolsUnsupportedRE = regexp.MustCompile(`(?i)(does not support|not supported|unsupported)[^"]{0,40}\btools?\b|\btools?\b[^"]{0,40}(not supported|unsupported)`)

// decodeArgs parses a tool call's arguments. OpenAI sends a JSON string,
// Ollama an object; a model that emits invalid JSON gets an empty map plus
// the raw text so the tool can report what it received.
func decodeArgs(v any) map[string]any {
	switch a := v.(type) {
	case map[string]any:
		return a
	case string:
		if a == "" {
			return map[string]any{}
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(a), &m); err == nil && m != nil {
			return m
		}
		return map[string]any{"_raw_arguments": a}
	}
	return map[string]any{}
}

// encodeArgs renders args as the JSON string OpenAI expects.
func encodeArgs(args map[string]any) string {
	if args == nil {
		return "{}"
	}
	b, err := json.Marshal(args)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// callID returns c.ID, or a stable stand-in for providers that issue none.
func callID(c ToolCall, i int) string {
	if c.ID != "" {
		return c.ID
	}
	return fmt.Sprintf("call_%d", i)
}

// CompleteTools implements ToolChatClient for Ollama's /api/chat tools API.
// format=json is not sent: it forces a JSON text reply and suppresses tool
// calls on several models.
func (o *OllamaChat) CompleteTools(ctx context.Context, messages []Message, tools []ToolSpec) (Completion, error) {
	t0 := time.Now()
	wire := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		w := map[string]any{"role": m.Role, "content": m.Content}
		if len(m.ToolCalls) > 0 {
			calls := make([]map[string]any, 0, len(m.ToolCalls))
			for _, c := range m.ToolCalls {
				calls = append(calls, map[string]any{"function": map[string]any{"name": c.Name, "arguments": c.Args}})
			}
			w["tool_calls"] = calls
		}
		if m.Role == "tool" && m.Name != "" {
			w["tool_name"] = m.Name
		}
		wire = append(wire, w)
	}
	specs := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		specs = append(specs, map[string]any{"type": "function", "function": map[string]any{
			"name": t.Name, "description": t.Description, "parameters": t.Parameters,
		}})
	}
	payload := map[string]any{
		"model":    o.model,
		"messages": wire,
		"tools":    specs,
		"stream":   false,
		"options":  map[string]any{"temperature": 0.2},
	}
	raw, status, err := o.postChat(ctx, payload)
	if err != nil {
		return Completion{ElapsedSec: time.Since(t0).Seconds()}, err
	}
	if status >= 400 {
		err := fmt.Errorf("ollama status %d: %s", status, truncate(string(raw), 300))
		if status == http.StatusBadRequest && toolsUnsupportedRE.Match(raw) {
			err = fmt.Errorf("%w: %v", ErrToolsUnsupported, err)
		}
		return Completion{ElapsedSec: time.Since(t0).Seconds()}, err
	}
	var out struct {
		Message struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments any    `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Completion{ElapsedSec: time.Since(t0).Seconds()}, fmt.Errorf("ollama decode: %w", err)
	}
	comp := Completion{Content: out.Message.Content, ElapsedSec: time.Since(t0).Seconds()}
	for i, c := range out.Message.ToolCalls {
		tc := ToolCall{ID: c.ID, Name: c.Function.Name, Args: decodeArgs(c.Function.Arguments)}
		tc.ID = callID(tc, i)
		comp.ToolCalls = append(comp.ToolCalls, tc)
	}
	return comp, nil
}

// CompleteTools implements ToolChatClient for OpenAI-compatible backends, in
// whichever API mode the client was built for.
func (o *OpenAIChat) CompleteTools(ctx context.Context, messages []Message, tools []ToolSpec) (Completion, error) {
	if o.mode == "responses" {
		return o.completeResponsesTools(ctx, messages, tools)
	}
	t0 := time.Now()
	wire := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		switch {
		case len(m.ToolCalls) > 0:
			calls := make([]map[string]any, 0, len(m.ToolCalls))
			for i, c := range m.ToolCalls {
				calls = append(calls, map[string]any{
					"id": callID(c, i), "type": "function",
					"function": map[string]any{"name": c.Name, "arguments": encodeArgs(c.Args)},
				})
			}
			w := map[string]any{"role": "assistant", "tool_calls": calls}
			if m.Content != "" {
				w["content"] = m.Content
			}
			wire = append(wire, w)
		case m.Role == "tool" && m.ToolCallID != "":
			wire = append(wire, map[string]any{"role": "tool", "tool_call_id": m.ToolCallID, "content": m.Content})
		case m.Role == "tool":
			wire = append(wire, map[string]any{"role": "user", "content": "[Tool result]: " + m.Content})
		default:
			wire = append(wire, map[string]any{"role": m.Role, "content": m.Content})
		}
	}
	specs := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		specs = append(specs, map[string]any{"type": "function", "function": map[string]any{
			"name": t.Name, "description": t.Description, "parameters": t.Parameters,
		}})
	}
	raw, err := o.post(ctx, "/chat/completions", "openai", func() map[string]any {
		payload := map[string]any{"model": o.Model(), "messages": wire, "tools": specs}
		if t := o.temp(); t != nil {
			payload["temperature"] = *t
		}
		return payload
	})
	if err != nil {
		return Completion{ElapsedSec: time.Since(t0).Seconds()}, err
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content   *string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Completion{ElapsedSec: time.Since(t0).Seconds()}, fmt.Errorf("openai decode: %w", err)
	}
	comp := Completion{ElapsedSec: time.Since(t0).Seconds()}
	if len(out.Choices) == 0 {
		return comp, nil
	}
	msg := out.Choices[0].Message
	if msg.Content != nil {
		comp.Content = *msg.Content
	}
	for i, c := range msg.ToolCalls {
		tc := ToolCall{ID: c.ID, Name: c.Function.Name, Args: decodeArgs(c.Function.Arguments)}
		tc.ID = callID(tc, i)
		comp.ToolCalls = append(comp.ToolCalls, tc)
	}
	return comp, nil
}

func (o *OpenAIChat) completeResponsesTools(ctx context.Context, messages []Message, tools []ToolSpec) (Completion, error) {
	t0 := time.Now()
	input := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		switch {
		case len(m.ToolCalls) > 0:
			if m.Content != "" {
				input = append(input, map[string]any{"role": "assistant", "content": m.Content})
			}
			for i, c := range m.ToolCalls {
				input = append(input, map[string]any{
					"type": "function_call", "call_id": callID(c, i), "name": c.Name, "arguments": encodeArgs(c.Args),
				})
			}
		case m.Role == "tool" && m.ToolCallID != "":
			input = append(input, map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": m.Content})
		case m.Role == "tool":
			input = append(input, map[string]any{"role": "user", "content": "[Tool result]: " + m.Content})
		default:
			input = append(input, map[string]any{"role": m.Role, "content": m.Content})
		}
	}
	specs := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		specs = append(specs, map[string]any{"type": "function", "name": t.Name, "description": t.Description, "parameters": t.Parameters})
	}
	raw, err := o.post(ctx, "/responses", "openai responses", func() map[string]any {
		payload := map[string]any{"model": o.Model(), "input": input, "store": false, "tools": specs}
		if o.reasoning != "" {
			payload["reasoning"] = map[string]string{"effort": o.reasoning}
		}
		return payload
	})
	if err != nil {
		return Completion{ElapsedSec: time.Since(t0).Seconds()}, err
	}
	text, calls, err := decodeResponsesOutput(raw)
	if err != nil {
		return Completion{ElapsedSec: time.Since(t0).Seconds()}, err
	}
	for i := range calls {
		calls[i].ID = callID(calls[i], i)
	}
	return Completion{Content: text, ToolCalls: calls, ElapsedSec: time.Since(t0).Seconds()}, nil
}
