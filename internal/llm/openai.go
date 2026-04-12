package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAIChat speaks to either /chat/completions (regular OpenAI/Azure/etc.)
// or /responses (Codex-style models). Mode is selected at construction time.
type OpenAIChat struct {
	apiKey    string
	baseURL   string
	model     string
	mode      string // "chat_completions" or "responses"
	reasoning string
	client    *http.Client
}

func NewOpenAIChat(apiKey, baseURL, model, mode, reasoning string, timeoutSec float64) *OpenAIChat {
	if mode == "" {
		mode = "chat_completions"
	}
	return &OpenAIChat{
		apiKey:    apiKey,
		baseURL:   strings.TrimRight(baseURL, "/"),
		model:     model,
		mode:      strings.ToLower(mode),
		reasoning: reasoning,
		client:    &http.Client{Timeout: time.Duration(timeoutSec * float64(time.Second))},
	}
}

func (o *OpenAIChat) Model() string   { return o.model }
func (o *OpenAIChat) BaseURL() string { return o.baseURL }

func (o *OpenAIChat) CompleteJSON(messages []Message) (string, float64, error) {
	if o.mode == "responses" {
		return o.completeResponses(messages)
	}
	return o.completeChatCompletions(messages)
}

// mapMessages converts internal Message records into OpenAI's accepted role
// set.  The agent loop emits role="tool" for tool results, but OpenAI's chat
// API only knows assistant/user/system, so we re-tag tool messages as user
// messages with a "[Tool result]:" prefix (matches the Python implementation).
func (o *OpenAIChat) mapMessages(messages []Message) []map[string]string {
	out := make([]map[string]string, 0, len(messages))
	for _, m := range messages {
		if m.Role == "tool" {
			out = append(out, map[string]string{"role": "user", "content": "[Tool result]: " + m.Content})
		} else {
			out = append(out, map[string]string{"role": m.Role, "content": m.Content})
		}
	}
	return out
}

func (o *OpenAIChat) authHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
	req.Header.Set("Content-Type", "application/json")
}

func (o *OpenAIChat) completeChatCompletions(messages []Message) (string, float64, error) {
	t0 := time.Now()
	payload := map[string]any{
		"model":           o.model,
		"messages":        o.mapMessages(messages),
		"temperature":     0.2,
		"response_format": map[string]string{"type": "json_object"},
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, o.baseURL+"/chat/completions", bytes.NewReader(body))
	o.authHeaders(req)
	resp, err := o.client.Do(req)
	if err != nil {
		return "", time.Since(t0).Seconds(), fmt.Errorf("openai post: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", time.Since(t0).Seconds(), fmt.Errorf("openai status %d: %s", resp.StatusCode, truncate(string(raw), 400))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", time.Since(t0).Seconds(), fmt.Errorf("openai decode: %w", err)
	}
	if len(out.Choices) == 0 {
		return "{}", time.Since(t0).Seconds(), nil
	}
	return out.Choices[0].Message.Content, time.Since(t0).Seconds(), nil
}

func (o *OpenAIChat) completeResponses(messages []Message) (string, float64, error) {
	t0 := time.Now()
	payload := map[string]any{
		"model": o.model,
		"input": o.mapMessages(messages),
		"store": false,
		"text": map[string]any{
			"format": map[string]string{"type": "json_object"},
		},
	}
	if o.reasoning != "" {
		payload["reasoning"] = map[string]string{"effort": o.reasoning}
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, o.baseURL+"/responses", bytes.NewReader(body))
	o.authHeaders(req)
	resp, err := o.client.Do(req)
	if err != nil {
		return "", time.Since(t0).Seconds(), fmt.Errorf("openai responses post: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", time.Since(t0).Seconds(), fmt.Errorf("openai responses status %d: %s", resp.StatusCode, truncate(string(raw), 400))
	}
	// The /responses endpoint returns either "output_text" directly or a
	// structured "output" array with nested content blocks.  Try the simple
	// path first, then fall back to walking the array.
	var out struct {
		OutputText string `json:"output_text"`
		Output     []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", time.Since(t0).Seconds(), fmt.Errorf("openai responses decode: %w", err)
	}
	if out.OutputText != "" {
		return out.OutputText, time.Since(t0).Seconds(), nil
	}
	var sb strings.Builder
	for _, item := range out.Output {
		for _, block := range item.Content {
			sb.WriteString(block.Text)
		}
	}
	if sb.Len() == 0 {
		return "{}", time.Since(t0).Seconds(), nil
	}
	return sb.String(), time.Since(t0).Seconds(), nil
}
