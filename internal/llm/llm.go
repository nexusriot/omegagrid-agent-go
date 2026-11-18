// Package llm contains LLM chat client implementations.
//
// Every chat client implements ChatClient.CompleteJSON, which sends a list of
// {role, content} messages and returns the model's raw text response (expected
// to be a JSON object) along with the elapsed wall time in seconds. The agent
// loop is responsible for parsing the JSON.
package llm

import (
	"context"
	"encoding/json"
)

// Message is a single chat turn. ToolCalls (on an assistant turn) and
// ToolCallID / Name (on a role="tool" turn) are set only in native
// tool-calling mode; they are not part of the plain JSON form, and each
// client renders them in its own provider's wire shape.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"-"`
	ToolCallID string     `json:"-"`
	Name       string     `json:"-"`
}

// ChatClient is implemented by every LLM backend.
type ChatClient interface {
	CompleteJSON(messages []Message) (raw string, elapsedSec float64, err error)
	Model() string
	BaseURL() string
}

// ContextChatClient is implemented by backends that can abort a completion
// when ctx is cancelled. The agent prefers it over CompleteJSON so a client
// that disconnects stops paying for LLM time.
type ContextChatClient interface {
	CompleteJSONContext(ctx context.Context, messages []Message) (raw string, elapsedSec float64, err error)
}

// Complete calls CompleteJSONContext when c supports it, else CompleteJSON.
func Complete(ctx context.Context, c ChatClient, messages []Message) (string, float64, error) {
	if cc, ok := c.(ContextChatClient); ok {
		return cc.CompleteJSONContext(ctx, messages)
	}
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	return c.CompleteJSON(messages)
}

// Usage is the token count of one completion, when the backend reports it.
type Usage struct {
	PromptTokens     int64
	CompletionTokens int64
}

type usageSinkKey struct{}

// WithUsageSink returns a context whose completions report their token usage
// to fn. The agent uses it to total a run's tokens without every client
// method growing another return value.
func WithUsageSink(ctx context.Context, fn func(Usage)) context.Context {
	return context.WithValue(ctx, usageSinkKey{}, fn)
}

// reportUsage reads the token counts from a response body — OpenAI chat
// completions (usage.prompt_tokens / completion_tokens), the Responses API
// (usage.input_tokens / output_tokens) or Ollama (prompt_eval_count /
// eval_count) — and hands them to ctx's sink, if it has one.
func reportUsage(ctx context.Context, raw []byte) {
	fn, _ := ctx.Value(usageSinkKey{}).(func(Usage))
	if fn == nil {
		return
	}
	var body struct {
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
			InputTokens      int64 `json:"input_tokens"`
			OutputTokens     int64 `json:"output_tokens"`
		} `json:"usage"`
		PromptEvalCount int64 `json:"prompt_eval_count"`
		EvalCount       int64 `json:"eval_count"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return
	}
	u := Usage{
		PromptTokens:     body.Usage.PromptTokens + body.Usage.InputTokens + body.PromptEvalCount,
		CompletionTokens: body.Usage.CompletionTokens + body.Usage.OutputTokens + body.EvalCount,
	}
	if u.PromptTokens > 0 || u.CompletionTokens > 0 {
		fn(u)
	}
}
