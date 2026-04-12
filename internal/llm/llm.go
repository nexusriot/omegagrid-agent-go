// Package llm contains LLM chat client implementations.
//
// Every chat client implements ChatClient.CompleteJSON, which sends a list of
// {role, content} messages and returns the model's raw text response (expected
// to be a JSON object) along with the elapsed wall time in seconds. The agent
// loop is responsible for parsing the JSON.
package llm

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
