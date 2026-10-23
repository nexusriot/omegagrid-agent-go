package telegram

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// agentRequestTimeout bounds one agent run (sync or streamed). Long enough for
// a multi-step tool-using run, short enough that a gateway that accepted the
// request and then hung cannot pin a handler goroutine — and its chat's lock —
// forever.
const agentRequestTimeout = 15 * time.Minute

// sseMaxEventBytes caps one SSE line. The final event inlines base64
// attachments (images), which blew through the old 1 MiB cap and surfaced as
// "token too long" — after the agent had already finished.
const sseMaxEventBytes = 32 << 20

// AgentClient is a tiny HTTP client for /api/query and /api/query/stream.
// The streaming variant emits Event values on the channel until it sees a
// final or error event.
type AgentClient struct {
	baseURL string
	// apiToken is sent as "Authorization: Bearer" when non-empty; the gateway
	// requires it once GATEWAY_API_TOKEN is configured there.
	apiToken string
	timeout  time.Duration
	http     *http.Client
}

func NewAgentClient(baseURL, apiToken string) *AgentClient {
	return &AgentClient{
		baseURL:  strings.TrimRight(baseURL, "/"),
		apiToken: strings.TrimSpace(apiToken),
		timeout:  agentRequestTimeout,
		// No client-wide Timeout: it would also cut off a healthy SSE stream.
		// Every request carries its own context deadline instead.
		http: &http.Client{},
	}
}

// newRequest builds a gateway request with the auth header attached.
func (c *AgentClient) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if c.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiToken)
	}
	return req, nil
}

// skillsTimeout bounds the /skills listing — a plain metadata read.
const skillsTimeout = 30 * time.Second

// ListSkillsRaw fetches GET /api/skills and returns the raw JSON body.
func (c *AgentClient) ListSkillsRaw() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), skillsTimeout)
	defer cancel()
	req, err := c.newRequest(ctx, http.MethodGet, "/api/skills", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("gateway %d: %s", resp.StatusCode, string(raw))
	}
	return raw, nil
}

// Attachment mirrors agent.Attachment.
type Attachment struct {
	Type     string `json:"type"`
	Filename string `json:"filename"`
	MimeType string `json:"mime_type"`
	Base64   string `json:"base64"`
}

// Event mirrors agent.Event for the few fields the bot actually displays.
type Event struct {
	Event       string         `json:"event"`
	Step        int            `json:"step"`
	Tool        string         `json:"tool"`
	Why         string         `json:"why"`
	Result      string         `json:"result"`
	ElapsedS    float64        `json:"elapsed_s"`
	SessionID   int            `json:"session_id"`
	Answer      string         `json:"answer"`
	Meta        map[string]any `json:"meta"`
	Error       string         `json:"error"`
	Attachments []Attachment   `json:"attachments,omitempty"`
}

type queryRequest struct {
	Query          string `json:"query"`
	SessionID      int    `json:"session_id,omitempty"`
	TelegramChatID int64  `json:"telegram_chat_id"`
}

type queryResponse struct {
	SessionID int            `json:"session_id"`
	Answer    string         `json:"answer"`
	Meta      map[string]any `json:"meta"`
}

// Query is the non-streaming endpoint, used as a fallback when streaming
// breaks (e.g. proxy issues).
func (c *AgentClient) Query(text string, chatID int64, sessionID int) (*queryResponse, error) {
	body, _ := json.Marshal(queryRequest{Query: text, SessionID: sessionID, TelegramChatID: chatID})
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	req, err := c.newRequest(ctx, http.MethodPost, "/api/query", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build query request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("gateway %d: %s", resp.StatusCode, string(raw))
	}
	var out queryResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// QueryStream consumes the SSE stream and pushes parsed events to out.
// The function blocks until the stream ends.  Closes out on return.
func (c *AgentClient) QueryStream(text string, chatID int64, sessionID int, out chan<- Event) error {
	defer close(out)
	body, _ := json.Marshal(queryRequest{Query: text, SessionID: sessionID, TelegramChatID: chatID})
	// A malformed GATEWAY_URL makes NewRequest return a nil request together
	// with its error; setting a header on it panicked instead of reporting the
	// misconfiguration.
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	req, err := c.newRequest(ctx, http.MethodPost, "/api/query/stream", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build stream request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("stream gateway %d: %s", resp.StatusCode, string(raw))
	}

	// Parse event-stream framing.  Each event is one or more `key: value`
	// lines terminated by a blank line.  We only care about the `data:` line.
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), sseMaxEventBytes)
	var dataBuf strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if dataBuf.Len() == 0 {
				continue
			}
			var ev Event
			if err := json.Unmarshal([]byte(dataBuf.String()), &ev); err == nil {
				out <- ev
				if ev.Event == "final" || ev.Event == "error" {
					return nil
				}
			}
			dataBuf.Reset()
		case strings.HasPrefix(line, "data:"):
			dataBuf.WriteString(strings.TrimSpace(line[5:]))
		}
	}
	return scanner.Err()
}
