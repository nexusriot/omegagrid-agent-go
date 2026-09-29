package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// OpenAIChat speaks to either /chat/completions (regular OpenAI/Azure/etc.)
// or /responses (Codex-style models). Mode is selected at construction time.
type OpenAIChat struct {
	apiKey  string
	baseURL string
	// mu guards model, which a 410 ModelDeprecated reply rewrites in place
	// while other goroutines of a long-lived gateway share this client.
	mu        sync.RWMutex
	model     string
	mode      string // "chat_completions" or "responses"
	reasoning string
	// temperature is nil when the request should omit the field entirely,
	// which is what reasoning models that accept only their own default
	// demand. A 400 naming temperature rewrites this to nil in place.
	temperature *float64
	// extraHeaders are sent on every request, for relays that demand headers
	// beyond bearer auth (opencode's x-opencode-session).
	extraHeaders map[string]string
	client       *http.Client

	// sleep is the retry backoff, injectable so tests can assert the schedule
	// without actually waiting seconds for it.
	sleep func(time.Duration)
}

// Option customises an OpenAIChat at construction time.
type Option func(*OpenAIChat)

// WithHeader adds one header to every request the client sends. Applied after
// the standard Authorization / Content-Type headers, so it can override them.
func WithHeader(key, value string) Option {
	return func(o *OpenAIChat) {
		if o.extraHeaders == nil {
			o.extraHeaders = map[string]string{}
		}
		o.extraHeaders[key] = value
	}
}

func NewOpenAIChat(apiKey, baseURL, model, mode, reasoning string, temperature *float64, timeoutSec float64, opts ...Option) *OpenAIChat {
	if mode == "" {
		mode = "chat_completions"
	}
	c := &OpenAIChat{
		apiKey:      apiKey,
		baseURL:     strings.TrimRight(baseURL, "/"),
		model:       model,
		mode:        strings.ToLower(mode),
		reasoning:   reasoning,
		temperature: temperature,
		client:      &http.Client{Timeout: time.Duration(timeoutSec * float64(time.Second))},
		sleep:       time.Sleep,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (o *OpenAIChat) Model() string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.model
}

func (o *OpenAIChat) BaseURL() string { return o.baseURL }

// temp returns the temperature to send, or nil to omit the field.
func (o *OpenAIChat) temp() *float64 {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.temperature
}

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

// sleepFor waits out the retry backoff, tolerating a zero-valued client built
// without NewOpenAIChat.
func (o *OpenAIChat) sleepFor(d time.Duration) {
	if o.sleep != nil {
		o.sleep(d)
		return
	}
	time.Sleep(d)
}

func (o *OpenAIChat) authHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range o.extraHeaders {
		req.Header.Set(k, v)
	}
}

// postWithRetry POSTs body to url, retrying transient failures up to 3
// attempts total: HTTP 429 (rate limit / "platform overloaded"), 5xx, and
// transport-level errors such as connection resets. Client timeouts are NOT
// retried — each attempt already waited the full configured timeout, and
// stacking more multi-minute waits would make the agent look frozen.
func (o *OpenAIChat) postWithRetry(url string, body []byte) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			o.sleepFor(time.Duration(1<<(attempt-1)) * 2 * time.Second) // 2s, 4s
		}
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		o.authHeaders(req)
		resp, err := o.client.Do(req)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return nil, err
			}
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
			resp.Body.Close()
			lastErr = fmt.Errorf("openai status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
			continue
		}
		return resp, nil
	}
	return nil, fmt.Errorf("%w (after 3 attempts)", lastErr)
}

func (o *OpenAIChat) completeChatCompletions(messages []Message) (string, float64, error) {
	t0 := time.Now()
	mapped := o.mapMessages(messages)
	var raw []byte
	for attempt := 0; ; attempt++ {
		payload := map[string]any{
			"model":           o.Model(),
			"messages":        mapped,
			"response_format": map[string]string{"type": "json_object"},
		}
		if t := o.temp(); t != nil {
			payload["temperature"] = *t
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return "", 0, fmt.Errorf("marshal request: %w", err)
		}
		resp, err := o.postWithRetry(o.baseURL+"/chat/completions", body)
		if err != nil {
			return "", time.Since(t0).Seconds(), fmt.Errorf("openai post: %w", err)
		}
		raw, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode < 400 {
			break
		}
		if attempt < maxSelfHeal && o.healAfterError(resp.StatusCode, raw) {
			continue
		}
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
	mapped := o.mapMessages(messages)
	var raw []byte
	for attempt := 0; ; attempt++ {
		payload := map[string]any{
			"model": o.Model(),
			"input": mapped,
			"store": false,
			"text": map[string]any{
				"format": map[string]string{"type": "json_object"},
			},
		}
		if o.reasoning != "" {
			payload["reasoning"] = map[string]string{"effort": o.reasoning}
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return "", 0, fmt.Errorf("marshal request: %w", err)
		}
		resp, err := o.postWithRetry(o.baseURL+"/responses", body)
		if err != nil {
			return "", time.Since(t0).Seconds(), fmt.Errorf("openai responses post: %w", err)
		}
		raw, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode < 400 {
			break
		}
		if attempt < maxSelfHeal && o.healAfterError(resp.StatusCode, raw) {
			continue
		}
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

// deprecatedModelHint matches the replacement named in a ModelDeprecated
// message ("Model kimi-k2.6 has been deprecated. Use kimi-k2.7-code
// instead."), for relays that word the error but omit the metadata block.
var deprecatedModelHint = regexp.MustCompile(`[Uu]se ` + "`?" + `([A-Za-z0-9._:/-]+)` + "`?" + ` instead`)

// maxSelfHeal bounds how many times one call may rewrite its own request and
// replay it. Every heal below refuses to repeat itself, so this is a backstop
// against a relay that rejects a request for a reason we keep misreading —
// not a retry budget to be spent.
const maxSelfHeal = 2

// healAfterError inspects a rejected request for a fault the relay described
// precisely enough to fix, applies one such fix, and reports whether the call
// is worth replaying. Anything it does not recognise returns false and lets
// the original error reach the caller unchanged.
func (o *OpenAIChat) healAfterError(status int, raw []byte) bool {
	return o.adoptReplacementModel(status, raw) || o.dropTemperature(status, raw)
}

// dropTemperature stops sending temperature after a model rejects the value.
// Omitting the field leaves the model on its own default, which is by
// definition the one value such a model allows, so this needs no guess at
// what that default is.
//
// Returns false once temperature is already omitted, which keeps a 400 that
// merely happens to mention the word from costing a second round trip.
func (o *OpenAIChat) dropTemperature(status int, raw []byte) bool {
	if status != http.StatusBadRequest && status != http.StatusUnprocessableEntity {
		return false
	}
	// Only the chat_completions payload carries temperature, so a /responses
	// rejection that happens to mention the word is about something else.
	if o.mode == "responses" {
		return false
	}
	// Relays word this refusal in at least two ways — "invalid temperature:
	// only 1 is allowed for this model" and OpenAI's "'temperature' does not
	// support 0.2 with this model" — so we key off the field name rather than
	// the phrasing, and fix it by omitting the field rather than by parsing
	// out the one value they would have accepted.
	if !strings.Contains(strings.ToLower(string(raw)), "temperature") {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.temperature == nil {
		return false
	}
	log.Printf("llm: model %q rejected temperature %v, retrying without it (set OPENAI_TEMPERATURE=none to silence this)", o.model, *o.temperature)
	o.temperature = nil
	return true
}

// adoptReplacementModel inspects a failed response for a model-deprecation
// notice and, when the relay names a successor, switches this client over to
// it so the caller's request can simply be sent again. Anything else — a
// different 4xx, a 410 with no replacement, or a replacement equal to what we
// already send — returns false and lets the original error surface.
//
// The switch lasts for the lifetime of the client only; set the provider's
// chat-model option (e.g. OPENCODE_CHAT_MODEL) to make it permanent.
func (o *OpenAIChat) adoptReplacementModel(status int, raw []byte) bool {
	replacement := replacementModel(status, raw)
	if replacement == "" {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if replacement == o.model {
		return false
	}
	log.Printf("llm: model %q is deprecated, retrying with %q (set the chat-model option to silence this)", o.model, replacement)
	o.model = replacement
	return true
}

// replacementModel extracts the successor model from a 410 Gone body, first
// from the structured metadata.replacement field and then from the prose of
// the error message.
func replacementModel(status int, raw []byte) string {
	if status != http.StatusGone {
		return ""
	}
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Metadata struct {
			Replacement string `json:"replacement"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return ""
	}
	if r := strings.TrimSpace(body.Metadata.Replacement); r != "" {
		return r
	}
	if m := deprecatedModelHint.FindStringSubmatch(body.Error.Message); len(m) == 2 {
		return strings.Trim(m[1], ".")
	}
	return ""
}
