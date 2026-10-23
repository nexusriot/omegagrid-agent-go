package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// On the SSE transport a server may emit notifications before the response,
// and split one event's JSON across several data: lines. The client must skip
// the former, reassemble the latter, and match the response by id.
func TestClientSSESkipsNotificationsAndJoinsDataLines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n")
		fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\ndata: \"id\":%s,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"done\"}]}}\n\n", req.ID)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, nil, 5*time.Second)
	got, err := c.CallTool(context.Background(), "x", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if got != "done" {
		t.Fatalf("CallTool = %q, want %q", got, "done")
	}
}

// A response carrying neither result nor error must not read as success.
func TestClientRejectsEmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s}`, req.ID)
	}))
	defer srv.Close()

	if _, err := NewClient(srv.URL, nil, 5*time.Second).CallTool(context.Background(), "x", nil); err == nil {
		t.Fatal("expected an error for a response with no result")
	}
}

// A 404 for a stale Mcp-Session-Id means the server dropped the session; the
// client must re-initialize and replay instead of failing forever.
func TestClientReinitializesExpiredSession(t *testing.T) {
	var session atomic.Int32
	var inits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if len(req.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		cur := fmt.Sprint(session.Load())
		if req.Method == "initialize" {
			inits.Add(1)
			cur = fmt.Sprint(session.Add(1))
			w.Header().Set("Mcp-Session-Id", cur)
		} else if r.Header.Get("Mcp-Session-Id") != cur {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		result := `{"content":[{"type":"text","text":"ok"}]}`
		if req.Method == "initialize" {
			result = `{}`
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, req.ID, result)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, nil, 5*time.Second)
	if err := c.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	session.Add(1) // simulate a server restart invalidating the session
	got, err := c.CallTool(context.Background(), "x", nil)
	if err != nil || got != "ok" {
		t.Fatalf("CallTool after expiry = %q, %v", got, err)
	}
	if n := inits.Load(); n != 2 {
		t.Fatalf("initialize called %d times, want 2", n)
	}
}

// JSON-RPC 2.0 requires "id": null on a parse error.
func TestServerParseErrorHasNullID(t *testing.T) {
	h := NewServer(fakeProvider{}, "t", "1").Handler()
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{not json"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), `"id":null`) {
		t.Fatalf("parse-error response lacks id:null: %s", w.Body.String())
	}
}
