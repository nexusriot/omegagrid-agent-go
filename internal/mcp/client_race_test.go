package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Remote MCP tools are registered as ordinary skills, so a parallel tool batch
// drives one Client from several goroutines. nextID and sessionID were read and
// written unsynchronised — run this with -race to see the difference.
func TestClientConcurrentCallsAreRaceFree(t *testing.T) {
	var mu sync.Mutex
	seenIDs := map[string]int{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		mu.Lock()
		seenIDs[string(req.ID)]++
		mu.Unlock()

		w.Header().Set("Mcp-Session-Id", "session-abc")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": "ok"}},
				"isError": false,
			},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, nil, 5*time.Second)

	const n = 24
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := c.CallTool(ctx, "echo", map[string]any{"x": 1}); err != nil {
				t.Errorf("CallTool: %v", err)
			}
		}()
	}
	wg.Wait()

	// Every concurrent call must have carried its own JSON-RPC id: a lost
	// increment would make two in-flight requests share one id, which is
	// exactly what a JSON-RPC peer is allowed to reject or mismatch.
	mu.Lock()
	defer mu.Unlock()
	if len(seenIDs) != n {
		t.Fatalf("%d distinct request ids for %d calls: %v", len(seenIDs), n, seenIDs)
	}
	for id, count := range seenIDs {
		if count != 1 {
			t.Fatalf("request id %s reused %d times", id, count)
		}
	}
	if got := c.getSessionID(); got != "session-abc" {
		t.Fatalf("sessionID = %q, want %q", got, "session-abc")
	}
}
