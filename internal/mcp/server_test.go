package mcp

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeProvider struct {
	tools []Tool
}

func (f fakeProvider) Tools() []Tool { return f.tools }

func (f fakeProvider) Call(name string, args map[string]any) (any, error) {
	if name == "boom" {
		return nil, errors.New("kaboom")
	}
	return "result-of-" + name, nil
}

type testRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

func newTestServer() *Server {
	provider := fakeProvider{tools: []Tool{{
		Name:        "greet",
		Description: "greets",
		Parameters:  map[string]Param{"who": {Type: "string", Description: "name", Required: true}},
	}}}
	return NewServer(provider, "test-server", "9.9.9")
}

func doPost(t *testing.T, h http.HandlerFunc, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func decodeRPC(t *testing.T, rec *httptest.ResponseRecorder) testRPCResponse {
	t.Helper()
	var resp testRPCResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body=%q)", err, rec.Body.String())
	}
	if resp.JSONRPC != "2.0" {
		t.Errorf("response jsonrpc = %q, want 2.0", resp.JSONRPC)
	}
	return resp
}

func TestServerHandler(t *testing.T) {
	h := newTestServer().Handler()

	t.Run("initialize echoes a supported protocol version + serverInfo", func(t *testing.T) {
		rec := doPost(t, h, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		resp := decodeRPC(t, rec)
		if string(resp.ID) != "1" {
			t.Errorf("id = %s, want 1", resp.ID)
		}
		var res struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		}
		if err := json.Unmarshal(resp.Result, &res); err != nil {
			t.Fatalf("decode result: %v", err)
		}
		if res.ProtocolVersion != "2025-03-26" {
			t.Errorf("protocolVersion = %q, want the echoed 2025-03-26", res.ProtocolVersion)
		}
		if res.ServerInfo.Name != "test-server" || res.ServerInfo.Version != "9.9.9" {
			t.Errorf("serverInfo = %+v, want {test-server 9.9.9}", res.ServerInfo)
		}
	})

	t.Run("initialize falls back to default version when unsupported", func(t *testing.T) {
		rec := doPost(t, h, `{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
		resp := decodeRPC(t, rec)
		var res struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if err := json.Unmarshal(resp.Result, &res); err != nil {
			t.Fatalf("decode result: %v", err)
		}
		if res.ProtocolVersion != ProtocolVersion {
			t.Errorf("protocolVersion = %q, want fallback %q", res.ProtocolVersion, ProtocolVersion)
		}
	})

	t.Run("initialize with no params uses default version", func(t *testing.T) {
		rec := doPost(t, h, `{"jsonrpc":"2.0","id":3,"method":"initialize"}`)
		resp := decodeRPC(t, rec)
		var res struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if err := json.Unmarshal(resp.Result, &res); err != nil {
			t.Fatalf("decode result: %v", err)
		}
		if res.ProtocolVersion != ProtocolVersion {
			t.Errorf("protocolVersion = %q, want default %q", res.ProtocolVersion, ProtocolVersion)
		}
	})

	t.Run("tools/list returns the fake tool", func(t *testing.T) {
		rec := doPost(t, h, `{"jsonrpc":"2.0","id":4,"method":"tools/list"}`)
		resp := decodeRPC(t, rec)
		var res struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(resp.Result, &res); err != nil {
			t.Fatalf("decode result: %v", err)
		}
		if len(res.Tools) != 1 {
			t.Fatalf("got %d tools, want 1", len(res.Tools))
		}
		tool := res.Tools[0]
		if tool.Name != "greet" || tool.Description != "greets" {
			t.Errorf("tool = {%q %q}, want {greet greets}", tool.Name, tool.Description)
		}
		var schema struct {
			Type       string `json:"type"`
			Properties map[string]struct {
				Type        string `json:"type"`
				Description string `json:"description"`
			} `json:"properties"`
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatalf("decode inputSchema: %v", err)
		}
		if schema.Type != "object" {
			t.Errorf("inputSchema type = %q, want object", schema.Type)
		}
		if schema.Properties["who"].Type != "string" || schema.Properties["who"].Description != "name" {
			t.Errorf("who property = %+v, want {string name}", schema.Properties["who"])
		}
		if len(schema.Required) != 1 || schema.Required[0] != "who" {
			t.Errorf("required = %v, want [who]", schema.Required)
		}
	})

	t.Run("tools/call returns the tool result as text content", func(t *testing.T) {
		rec := doPost(t, h, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"greet","arguments":{"who":"world"}}}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		resp := decodeRPC(t, rec)
		var res struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		}
		if err := json.Unmarshal(resp.Result, &res); err != nil {
			t.Fatalf("decode result: %v", err)
		}
		if res.IsError {
			t.Errorf("isError = true, want false")
		}
		if len(res.Content) != 1 || res.Content[0].Type != "text" || res.Content[0].Text != "result-of-greet" {
			t.Errorf("content = %+v, want one text block %q", res.Content, "result-of-greet")
		}
	})

	t.Run("tools/call skill error is in-band isError with HTTP 200", func(t *testing.T) {
		rec := doPost(t, h, `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"boom","arguments":{}}}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (errors are reported in-band)", rec.Code)
		}
		resp := decodeRPC(t, rec)
		if resp.Error != nil {
			t.Fatalf("got JSON-RPC error %+v, want in-band isError result", resp.Error)
		}
		var res struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		}
		if err := json.Unmarshal(resp.Result, &res); err != nil {
			t.Fatalf("decode result: %v", err)
		}
		if !res.IsError {
			t.Errorf("isError = false, want true")
		}
		if len(res.Content) != 1 || res.Content[0].Text != "kaboom" {
			t.Errorf("content = %+v, want error text %q", res.Content, "kaboom")
		}
	})

	t.Run("unknown method returns JSON-RPC error -32601", func(t *testing.T) {
		rec := doPost(t, h, `{"jsonrpc":"2.0","id":7,"method":"does/not/exist"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (JSON-RPC errors ride in the body)", rec.Code)
		}
		resp := decodeRPC(t, rec)
		if resp.Error == nil {
			t.Fatalf("error = nil, want method-not-found")
		}
		if resp.Error.Code != codeMethodNotFound {
			t.Errorf("error code = %d, want %d", resp.Error.Code, codeMethodNotFound)
		}
		if !strings.Contains(resp.Error.Message, "method not found") {
			t.Errorf("error message = %q, want it to mention 'method not found'", resp.Error.Message)
		}
	})

	t.Run("GET request returns HTTP 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405", rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != "POST" {
			t.Errorf("Allow header = %q, want POST", got)
		}
	})

	t.Run("notification gets HTTP 202 with no body", func(t *testing.T) {
		rec := doPost(t, h, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202", rec.Code)
		}
		if rec.Body.Len() != 0 {
			t.Errorf("body = %q, want empty", rec.Body.String())
		}
	})
}
