package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Server exposes a ToolProvider as MCP tools over HTTP.
type Server struct {
	provider ToolProvider
	name     string
	version  string
}

// NewServer builds an MCP server backed by provider.
func NewServer(provider ToolProvider, name, version string) *Server {
	return &Server{provider: provider, name: name, version: version}
}

// Handler returns an http.HandlerFunc to mount at the MCP endpoint (e.g. /mcp).
// It accepts a single JSON-RPC request or notification per POST and replies
// with a JSON-RPC response (or 202 for notifications). GET returns 405 — we do
// not implement the optional server-initiated SSE stream.
func (s *Server) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeRPC(w, &rpcResponse{
				JSONRPC: "2.0",
				Error:   &rpcError{Code: codeParseError, Message: "parse error: " + err.Error()},
			})
			return
		}
		if req.JSONRPC != "2.0" {
			writeRPC(w, &rpcResponse{
				JSONRPC: "2.0", ID: req.ID,
				Error: &rpcError{Code: codeInvalidRequest, Message: "jsonrpc must be \"2.0\""},
			})
			return
		}

		resp := s.dispatch(&req)
		if resp == nil { // notification — no response body
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeRPC(w, resp)
	}
}

func (s *Server) dispatch(req *rpcRequest) *rpcResponse {
	ok := func(result any) *rpcResponse {
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result}
	}
	fail := func(code int, msg string) *rpcResponse {
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: code, Message: msg}}
	}

	switch req.Method {
	case "initialize":
		return ok(s.initializeResult(req.Params))

	case "notifications/initialized", "notifications/cancelled":
		return nil // notifications get no response

	case "ping":
		return ok(map[string]any{})

	case "tools/list":
		return ok(map[string]any{"tools": s.toolList()})

	case "tools/call":
		if req.isNotification() {
			return nil
		}
		return s.toolsCall(req, ok, fail)

	default:
		if req.isNotification() {
			return nil
		}
		return fail(codeMethodNotFound, "method not found: "+req.Method)
	}
}

func (s *Server) initializeResult(params json.RawMessage) map[string]any {
	version := ProtocolVersion
	if len(params) > 0 {
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if err := json.Unmarshal(params, &p); err == nil && supportedVersions[p.ProtocolVersion] {
			version = p.ProtocolVersion
		}
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      map[string]any{"name": s.name, "version": s.version},
	}
}

func (s *Server) toolList() []map[string]any {
	tools := s.provider.Tools()
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": jsonSchema(t.Parameters),
		})
	}
	return out
}

func (s *Server) toolsCall(req *rpcRequest, ok func(any) *rpcResponse, fail func(int, string) *rpcResponse) *rpcResponse {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return fail(codeInvalidRequest, "invalid params: "+err.Error())
	}
	if p.Name == "" {
		return fail(codeInvalidRequest, "tool name is required")
	}
	if p.Arguments == nil {
		p.Arguments = map[string]any{}
	}

	result, err := s.provider.Call(p.Name, p.Arguments)
	if err != nil {
		// Tool execution errors are reported in-band (isError) so the calling
		// model can read and react to them, per MCP convention.
		return ok(map[string]any{
			"content": []map[string]any{{"type": "text", "text": err.Error()}},
			"isError": true,
		})
	}
	return ok(map[string]any{
		"content": []map[string]any{{"type": "text", "text": resultText(result)}},
		"isError": false,
	})
}

// resultText renders a skill result as MCP text content: strings pass through,
// everything else is pretty-printed JSON.
func resultText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func writeRPC(w http.ResponseWriter, resp *rpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
