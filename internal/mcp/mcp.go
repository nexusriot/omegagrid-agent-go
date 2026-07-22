// Package mcp implements a minimal, hand-rolled Model Context Protocol layer:
//
//   - Server: exposes the gateway's skills as MCP tools over a single HTTP
//     endpoint (JSON-RPC 2.0, "streamable HTTP" style — every request is a
//     plain JSON POST, the response is plain JSON). This lets external MCP
//     clients (Claude Desktop, Cursor, …) call OmegaGrid skills.
//   - Client: connects to remote MCP servers over the same transport and
//     registers their tools back into the local skill registry.
//
// No SDK dependency — the wire format is small enough to implement directly,
// keeping the project's pure-Go, lean-go.mod ethos.
package mcp

import "encoding/json"

// ProtocolVersion is the MCP revision this implementation speaks. The server
// echoes back a client's requested version when it is one we understand,
// otherwise it advertises this one.
const ProtocolVersion = "2025-06-18"

// supportedVersions lists protocol revisions the server will echo back to a
// client verbatim during initialize (kept small but covering common clients).
var supportedVersions = map[string]bool{
	"2024-11-05": true,
	"2025-03-26": true,
	"2025-06-18": true,
}

// Param describes one tool parameter. Mirrors skills.Param so the bootstrap
// adapter is a trivial field copy without importing the skills package here.
type Param struct {
	Type        string
	Description string
	Required    bool
}

// Tool is a provider-agnostic tool definition.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]Param
}

// ToolProvider is the surface the MCP server needs from the rest of the app:
// enumerate tools and invoke one by name. The bootstrap layer adapts the skill
// registry + native skills to this interface.
type ToolProvider interface {
	Tools() []Tool
	Call(name string, args map[string]any) (any, error)
}

// --- JSON-RPC 2.0 envelope ---

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// JSON-RPC standard error codes used here.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
)

// isNotification reports whether a parsed request carries no ID (JSON-RPC
// notifications must not be answered with a response object).
func (r *rpcRequest) isNotification() bool {
	return len(r.ID) == 0 || string(r.ID) == "null"
}

// jsonSchema converts a tool's parameter map into a JSON Schema object, the
// shape MCP clients expect for inputSchema.
func jsonSchema(params map[string]Param) map[string]any {
	props := make(map[string]any, len(params))
	var required []string
	for name, p := range params {
		typ := p.Type
		if typ == "" {
			typ = "string"
		}
		props[name] = map[string]any{"type": typ, "description": p.Description}
		if p.Required {
			required = append(required, name)
		}
	}
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}
