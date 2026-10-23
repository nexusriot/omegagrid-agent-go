package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// call sends a JSON-RPC request and returns the raw result payload. When the
// server reports our session as gone (HTTP 404 on a request that carried a
// session id — e.g. the remote restarted) the client re-initializes once and
// replays the request, instead of failing every call until a gateway restart.
func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	raw, err := c.callOnce(ctx, method, params)
	if errors.Is(err, errSessionExpired) && method != "initialize" {
		if ierr := c.Initialize(ctx); ierr != nil {
			return nil, fmt.Errorf("re-initialize after expired session: %w", ierr)
		}
		raw, err = c.callOnce(ctx, method, params)
	}
	return raw, err
}

func (c *Client) callOnce(ctx context.Context, method string, params any) (json.RawMessage, error) {
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
	if len(resp.Result) == 0 || string(resp.Result) == "null" {
		return nil, fmt.Errorf("rpc response for %s has neither result nor error", method)
	}
	return resp.Result, nil
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

// errSessionExpired marks a 404 for a request sent with a session id.
var errSessionExpired = errors.New("mcp session expired")

// maxResponseBytes caps a remote server's reply; the remote is not trusted.
const maxResponseBytes = 16 << 20

// clientResponse is rpcResponse as decoded on the client side: Result stays
// raw so an absent result is distinguishable from an empty one.
type clientResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

func (c *Client) post(ctx context.Context, body rpcRequest) (*clientResponse, error) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	sentSession := req.Header.Get("Mcp-Session-Id") != ""

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound && sentSession {
		c.setSessionID("")
		return nil, errSessionExpired
	}
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.setSessionID(sid)
	}
	if resp.StatusCode >= 400 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	return readResponse(resp, body.ID)
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

// readResponse extracts the JSON-RPC response to request wantID from either a
// plain JSON body or an SSE stream. On the SSE variant the server may send
// notifications (progress, log messages) or its own requests before the
// response, and one event's JSON may span several data: lines, so events are
// reassembled and the first message whose id matches is returned.
func readResponse(resp *http.Response, wantID json.RawMessage) (*clientResponse, error) {
	body := io.LimitReader(resp.Body, maxResponseBytes)
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		payload, err := io.ReadAll(body)
		if err != nil {
			return nil, err
		}
		var out clientResponse
		if err := json.Unmarshal(payload, &out); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}
		return &out, nil
	}

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxResponseBytes)
	var data []string
	flush := func() (*clientResponse, bool) {
		if len(data) == 0 {
			return nil, false
		}
		payload := strings.Join(data, "\n")
		data = data[:0]
		var out clientResponse
		if json.Unmarshal([]byte(payload), &out) != nil {
			return nil, false
		}
		if !bytes.Equal(bytes.TrimSpace(out.ID), bytes.TrimSpace(wantID)) {
			return nil, false // a notification or server-initiated request
		}
		return &out, true
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if out, ok := flush(); ok {
				return out, nil
			}
			continue
		}
		if d, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimPrefix(d, " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if out, ok := flush(); ok { // stream ended without a trailing blank line
		return out, nil
	}
	return nil, fmt.Errorf("no response with id %s in SSE stream", wantID)
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
