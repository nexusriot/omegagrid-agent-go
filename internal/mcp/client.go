package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Client speaks MCP to a single remote server over streamable HTTP. It is the
// counterpart of Server: it initializes a session, lists the remote tools, and
// proxies tools/call. Responses are accepted as either application/json or
// text/event-stream (single-event SSE), covering the common server variants.
type Client struct {
	url     string
	headers map[string]string
	http    *http.Client

	// Remote MCP tools are registered as ordinary skills, so a parallel tool
	// batch can drive one Client from several goroutines at once. mu guards the
	// request counter and the server-assigned session id, which were previously
	// read and written unsynchronised (duplicate JSON-RPC ids, torn session id).
	mu        sync.Mutex
	sessionID string
	nextID    int
}

// NewClient builds an MCP client for url. headers are sent on every request
// (e.g. an Authorization bearer token). The server's local namespace is the
// caller's business — bootstrap prefixes tool names with it when registering.
func NewClient(url string, headers map[string]string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		url:     url,
		headers: headers,
		http:    &http.Client{Timeout: timeout},
	}
}

// Initialize performs the MCP handshake: initialize request + initialized
// notification. It records the server-assigned session ID (if any) for reuse.
func (c *Client) Initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "omegagrid-agent-go", "version": "1.0.0"},
	}
	if _, err := c.call(ctx, "initialize", params); err != nil {
		return err
	}
	// Best-effort initialized notification (no response expected).
	_ = c.notify(ctx, "notifications/initialized", nil)
	return nil
}

// ListTools returns the remote server's tools, converting each inputSchema
// JSON Schema into the local Tool/Param shape.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	raw, err := c.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var res struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("decode tools/list: %w", err)
	}
	out := make([]Tool, 0, len(res.Tools))
	for _, t := range res.Tools {
		out = append(out, Tool{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  paramsFromSchema(t.InputSchema),
		})
	}
	return out, nil
}

// CallTool invokes a remote tool and returns its text content. A tool that
// reports isError=true is surfaced as a Go error so the agent loop treats it
// as a failed tool call.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	if args == nil {
		args = map[string]any{}
	}
	raw, err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", err
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("decode tools/call: %w", err)
	}
	var sb strings.Builder
	for _, ct := range res.Content {
		if ct.Text != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(ct.Text)
		}
	}
	text := sb.String()
	if res.IsError {
		return "", fmt.Errorf("remote tool error: %s", text)
	}
	return text, nil
}

// call sends a JSON-RPC request and returns the raw result payload.
func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	idBytes, _ := json.Marshal(c.takeID())
	body := rpcRequest{JSONRPC: "2.0", ID: idBytes, Method: method}
	if params != nil {
		pb, _ := json.Marshal(params)
		body.Params = pb
	}

	resp, err := c.post(ctx, body)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	rb, _ := json.Marshal(resp.Result)
	return rb, nil
}

// notify sends a JSON-RPC notification (no id, no response read).
func (c *Client) notify(ctx context.Context, method string, params any) error {
	body := rpcRequest{JSONRPC: "2.0", Method: method}
	if params != nil {
		pb, _ := json.Marshal(params)
		body.Params = pb
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	c.setHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return nil
}

func (c *Client) post(ctx context.Context, body rpcRequest) (*rpcResponse, error) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.setSessionID(sid)
	}
	if resp.StatusCode >= 400 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	payload, err := readResponseBody(resp)
	if err != nil {
		return nil, err
	}
	var out rpcResponse
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sid := c.getSessionID(); sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
}

// takeID returns the next JSON-RPC request id.
func (c *Client) takeID() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	return c.nextID
}

func (c *Client) getSessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID
}

func (c *Client) setSessionID(sid string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessionID = sid
}

// readResponseBody extracts the JSON-RPC payload from either a plain JSON
// response or a single-event SSE stream (the two streamable-HTTP variants).
func readResponseBody(resp *http.Response) ([]byte, error) {
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if data, ok := strings.CutPrefix(line, "data:"); ok {
				return []byte(strings.TrimSpace(data)), nil
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("no data event in SSE response")
	}
	return io.ReadAll(resp.Body)
}

// paramsFromSchema converts a tool's inputSchema JSON Schema into a Param map.
func paramsFromSchema(raw json.RawMessage) map[string]Param {
	if len(raw) == 0 {
		return nil
	}
	var schema struct {
		Properties map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil
	}
	reqSet := make(map[string]bool, len(schema.Required))
	for _, r := range schema.Required {
		reqSet[r] = true
	}
	out := make(map[string]Param, len(schema.Properties))
	for name, p := range schema.Properties {
		typ := p.Type
		if typ == "" {
			typ = "string"
		}
		out[name] = Param{Type: typ, Description: p.Description, Required: reqSet[name]}
	}
	return out
}
