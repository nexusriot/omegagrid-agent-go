package bootstrap

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nexusriot/omegagrid-agent-go/internal/config"
	"github.com/nexusriot/omegagrid-agent-go/internal/skills"
)

// fakeMCP is a JSON-RPC MCP server whose availability, latency and tool list
// a test can change while it runs.
type fakeMCP struct {
	mu        sync.Mutex
	tools     []string
	down      atomic.Bool
	delay     time.Duration
	announce  atomic.Bool // send tools/list_changed before the next tools/list reply
	listCalls atomic.Int32
	srv       *httptest.Server
}

func newFakeMCP(t *testing.T, tools ...string) *fakeMCP {
	f := &fakeMCP{tools: tools}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		time.Sleep(f.delay)
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}}
		case "tools/list":
			f.listCalls.Add(1)
			f.mu.Lock()
			list := make([]map[string]any, 0, len(f.tools))
			for _, n := range f.tools {
				list = append(list, map[string]any{"name": n, "description": "d", "inputSchema": map[string]any{"type": "object"}})
			}
			f.mu.Unlock()
			result = map[string]any{"tools": list}
		case "tools/call":
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}}
		default:
			w.WriteHeader(http.StatusAccepted)
			return
		}
		resp, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		if f.announce.Swap(false) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n"))
			_, _ = w.Write([]byte("data: " + string(resp) + "\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(resp)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeMCP) setTools(tools ...string) {
	f.mu.Lock()
	f.tools = tools
	f.mu.Unlock()
}

func registered(t *testing.T, sk *skills.Client, prefix string) []string {
	t.Helper()
	list, _ := sk.List()
	var out []string
	for _, s := range list {
		if strings.HasPrefix(s.Name, prefix) {
			out = append(out, s.Name)
		}
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func newSkills(t *testing.T) *skills.Client {
	sk, err := skills.New(config.Config{SkillsDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return sk
}

// Servers used to be dialled one after another.
func TestMCPServersConnectInParallel(t *testing.T) {
	a, b := newFakeMCP(t, "x"), newFakeMCP(t, "y")
	a.delay, b.delay = 400*time.Millisecond, 400*time.Millisecond
	sk := newSkills(t)
	t0 := time.Now()
	startMCPServers(t.Context(), config.Config{MCPServers: []string{"a=" + a.srv.URL, "b=" + b.srv.URL}, MCPConnectWait: 10}, sk, nativeSkillNames)
	// initialize + initialized + tools/list = 3 round trips of 400 ms each.
	if d := time.Since(t0); d > 2*time.Second {
		t.Fatalf("two servers took %v; they were dialled serially", d)
	}
	if got := registered(t, sk, "a_"); !slices.Equal(got, []string{"a_x"}) {
		t.Fatalf("a's tools = %v", got)
	}
}

// A server that is down at boot is retried in the background, and startup
// does not wait past MCP_CONNECT_WAIT_SEC for a hanging one.
func TestMCPServerDownAtBootIsRetried(t *testing.T) {
	saved := mcpBackoffMin
	mcpBackoffMin = 50 * time.Millisecond
	defer func() { mcpBackoffMin = saved }()

	f := newFakeMCP(t, "late")
	f.down.Store(true)
	sk := newSkills(t)
	startMCPServers(t.Context(), config.Config{MCPServers: []string{"r=" + f.srv.URL}, MCPConnectWait: 10}, sk, nativeSkillNames)
	if got := registered(t, sk, "r_"); len(got) != 0 {
		t.Fatalf("tools registered while down: %v", got)
	}
	f.down.Store(false)
	waitFor(t, "the late server's tool", func() bool { return len(registered(t, sk, "r_")) == 1 })

	hang := newFakeMCP(t, "h")
	hang.delay = time.Second
	t0 := time.Now()
	startMCPServers(t.Context(), config.Config{MCPServers: []string{"h=" + hang.srv.URL}, MCPConnectWait: 0.2}, newSkills(t), nativeSkillNames)
	if d := time.Since(t0); d > time.Second {
		t.Fatalf("startup waited %v for a hanging server, cap was 200ms", d)
	}
}

// Tools added or removed on the server reach the registry on the next
// refresh, or at once when the server announces tools/list_changed.
func TestMCPToolListIsRefreshed(t *testing.T) {
	f := newFakeMCP(t, "one", "two")
	sk := newSkills(t)
	startMCPServers(t.Context(), config.Config{MCPServers: []string{"s=" + f.srv.URL}, MCPConnectWait: 5, MCPRefreshSec: 0.1}, sk, nativeSkillNames)
	if got := registered(t, sk, "s_"); !slices.Equal(got, []string{"s_one", "s_two"}) {
		t.Fatalf("initial tools = %v", got)
	}
	f.setTools("two", "three")
	waitFor(t, "the refreshed tool list", func() bool {
		return slices.Equal(registered(t, sk, "s_"), []string{"s_three", "s_two"})
	})

	// No timer this time: only the server's announcement triggers a re-list.
	g := newFakeMCP(t, "a")
	sk2 := newSkills(t)
	startMCPServers(t.Context(), config.Config{MCPServers: []string{"g=" + g.srv.URL}, MCPConnectWait: 5}, sk2, nativeSkillNames)
	g.setTools("a", "b")
	g.announce.Store(true)
	// The announcement rides on the next response the client reads; a tool
	// call is the natural trigger.
	_, _ = sk2.Execute("g_a", map[string]any{})
	waitFor(t, "the announced tool", func() bool { return len(registered(t, sk2, "g_")) == 2 })
}

// Unregister only withdraws runtime registrations.
func TestUnregisterRefusesBuiltins(t *testing.T) {
	sk := newSkills(t)
	if sk.Unregister("http_request") || sk.Unregister("skill_creator") {
		t.Fatal("a built-in was unregistered")
	}
}
