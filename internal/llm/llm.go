// Package llm contains LLM chat client implementations.
//
// Every chat client implements ChatClient.CompleteJSON, which sends a list of
// {role, content} messages and returns the model's raw text response (expected
// to be a JSON object) along with the elapsed wall time in seconds. The agent
// loop is responsible for parsing the JSON.
package llm

import "context"

// Message is a single chat turn.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
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
