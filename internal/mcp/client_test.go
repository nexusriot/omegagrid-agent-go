package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// rpcEcho serves JSON-RPC replies produced by fn, recording every method seen.
func rpcEcho(t *testing.T, methods *[]string, fn func(method string, params json.RawMessage) (any, *rpcError)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		*methods = append(*methods, req.Method)

		// Notifications carry no id and expect no body.
		if len(req.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		result, rerr := fn(req.Method, req.Params)
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if rerr != nil {
			resp["error"] = rerr
		} else {
			resp["result"] = result
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestClientInitializeAndListTools(t *testing.T) {
	var methods []string
	srv := rpcEcho(t, &methods, func(method string, _ json.RawMessage) (any, *rpcError) {
		switch method {
		case "initialize":
			return map[string]any{"protocolVersion": ProtocolVersion}, nil
		case "tools/list":
			return map[string]any{"tools": []map[string]any{{
				"name":        "search",
				"description": "Search the web",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{"type": "string", "description": "what to search"},
						"limit": map[string]any{"type": "integer", "description": "how many"},
					},
					"required": []string{"query"},
				},
			}}}, nil
		}
		return nil, &rpcError{Code: codeMethodNotFound, Message: "unknown"}
	})
	defer srv.Close()

	c := NewClient(srv.URL, nil, 5*time.Second)
	ctx := context.Background()

	if err := c.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	// The handshake is initialize followed by the initialized notification.
	if len(methods) != 2 || methods[0] != "initialize" || methods[1] != "notifications/initialized" {
		t.Fatalf("handshake = %v", methods)
	}

	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("tools = %v", tools)
	}
	if tools[0].Name != "search" || tools[0].Description != "Search the web" {
		t.Fatalf("tool = %+v", tools[0])
	}
	if p := tools[0].Parameters["query"]; p.Type != "string" || !p.Required {
		t.Fatalf("query param = %+v, want a required string", p)
	}
	if p := tools[0].Parameters["limit"]; p.Type != "integer" || p.Required {
		t.Fatalf("limit param = %+v, want an optional integer", p)
	}
}

func TestClientCallToolSuccess(t *testing.T) {
	var methods []string
	var gotArgs map[string]any
	srv := rpcEcho(t, &methods, func(_ string, params json.RawMessage) (any, *rpcError) {
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(params, &p)
		gotArgs = p.Arguments
		return map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": "first"},
				{"type": "text", "text": "second"},
			},
			"isError": false,
		}, nil
	})
	defer srv.Close()

	c := NewClient(srv.URL, nil, 5*time.Second)
	got, err := c.CallTool(context.Background(), "search", map[string]any{"query": "go"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	// Multiple content blocks are joined so nothing is silently dropped.
	if got != "first\nsecond" {
		t.Fatalf("result = %q", got)
	}
	if gotArgs["query"] != "go" {
		t.Fatalf("arguments = %v", gotArgs)
	}
}

// A tool reporting isError must surface as a Go error so the agent loop treats
// it as a failed call and tells the user, rather than reporting the error text
// as a successful result.
func TestClientCallToolIsErrorBecomesError(t *testing.T) {
	var methods []string
	srv := rpcEcho(t, &methods, func(string, json.RawMessage) (any, *rpcError) {
		return map[string]any{
			"content": []map[string]any{{"type": "text", "text": "upstream refused"}},
			"isError": true,
		}, nil
	})
	defer srv.Close()

	c := NewClient(srv.URL, nil, 5*time.Second)
	_, err := c.CallTool(context.Background(), "search", nil)
	if err == nil {
		t.Fatal("isError result did not become an error")
	}
	if !strings.Contains(err.Error(), "upstream refused") {
		t.Fatalf("error lost the remote message: %v", err)
	}
}

func TestClientPropagatesRPCError(t *testing.T) {
	var methods []string
	srv := rpcEcho(t, &methods, func(string, json.RawMessage) (any, *rpcError) {
		return nil, &rpcError{Code: codeInvalidRequest, Message: "bad params"}
	})
	defer srv.Close()

	c := NewClient(srv.URL, nil, 5*time.Second)
	_, err := c.ListTools(context.Background())
	if err == nil {
		t.Fatal("expected an error for a JSON-RPC error response")
	}
	if !strings.Contains(err.Error(), "bad params") {
		t.Fatalf("error = %v", err)
	}
}

func TestClientHTTPErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("missing bearer token"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, nil, 5*time.Second)
	err := c.Initialize(context.Background())
	if err == nil {
		t.Fatal("expected an error for HTTP 403")
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "missing bearer token") {
		t.Fatalf("error = %v", err)
	}
}

// Streamable HTTP servers may answer with a single-event SSE stream instead of
// plain JSON; both must work.
func TestClientReadsSSEResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message\ndata: " +
			`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{"tools":[{"name":"sse_tool","description":"d"}]}}` +
			"\n\n"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, nil, 5*time.Second)
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools over SSE: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "sse_tool" {
		t.Fatalf("tools = %v", tools)
	}
}

func TestClientSSEWithoutDataEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(": keep-alive comment only\n\n"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, nil, 5*time.Second)
	if _, err := c.ListTools(context.Background()); err == nil {
		t.Fatal("expected an error when the SSE stream carries no data event")
	}
}

// The session id the server hands out must be echoed on every later request.
func TestClientReusesSessionID(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Mcp-Session-Id"))
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Mcp-Session-Id", "sess-1")
		w.Header().Set("Content-Type", "application/json")
		if len(req.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"tools": []any{}},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, nil, 5*time.Second)
	if err := c.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := c.ListTools(context.Background()); err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(seen) < 2 {
		t.Fatalf("only %d requests recorded", len(seen))
	}
	if seen[0] != "" {
		t.Fatalf("first request already carried a session id: %q", seen[0])
	}
	if seen[len(seen)-1] != "sess-1" {
		t.Fatalf("later request session id = %q, want sess-1", seen[len(seen)-1])
	}
}

func TestClientSendsConfiguredHeaders(t *testing.T) {
	var auth, accept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		accept = r.Header.Get("Accept")
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"tools": []any{}},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, map[string]string{"Authorization": "Bearer tok"}, 5*time.Second)
	if _, err := c.ListTools(context.Background()); err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if auth != "Bearer tok" {
		t.Fatalf("Authorization = %q", auth)
	}
	// Both response encodings are advertised.
	if !strings.Contains(accept, "application/json") || !strings.Contains(accept, "text/event-stream") {
		t.Fatalf("Accept = %q", accept)
	}
}

func TestClientUnreachableServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	c := NewClient(url, nil, time.Second)
	if err := c.Initialize(context.Background()); err == nil {
		t.Fatal("expected a transport error")
	}
}

func TestNewClientDefaultsTimeout(t *testing.T) {
	if got := NewClient("u", nil, 0).http.Timeout; got != 30*time.Second {
		t.Fatalf("timeout = %v, want the 30s default", got)
	}
	if got := NewClient("u", nil, 2*time.Second).http.Timeout; got != 2*time.Second {
		t.Fatalf("timeout = %v", got)
	}
}

func TestClientCallToolNilArgs(t *testing.T) {
	var methods []string
	var raw json.RawMessage
	srv := rpcEcho(t, &methods, func(_ string, params json.RawMessage) (any, *rpcError) {
		raw = params
		return map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}}, nil
	})
	defer srv.Close()

	c := NewClient(srv.URL, nil, 5*time.Second)
	if _, err := c.CallTool(context.Background(), "t", nil); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	// nil must become {} — some servers reject a null arguments field.
	if !strings.Contains(string(raw), `"arguments":{}`) {
		t.Fatalf("params = %s, want an empty object for arguments", raw)
	}
}
