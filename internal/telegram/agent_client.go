package telegram

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// AgentClient is a tiny HTTP client for /api/query and /api/query/stream.
// The streaming variant emits Event values on the channel until it sees a
// final or error event.
type AgentClient struct {
	baseURL string
	http    *http.Client
}

func NewAgentClient(baseURL string) *AgentClient {
	return &AgentClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 5 * time.Minute},
	}
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
	resp, err := c.http.Post(c.baseURL+"/api/query", "application/json", bytes.NewReader(body))
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
	req, _ := http.NewRequest(http.MethodPost, c.baseURL+"/api/query/stream", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	// Use a long-running client without a global timeout — SSE may stay open.
	streamClient := &http.Client{Timeout: 0}
	resp, err := streamClient.Do(req)
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
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
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
