package bootstrap

import (
	"context"
	"log"
	"reflect"
	"sync"
	"time"

	"github.com/nexusriot/omegagrid-agent-go/internal/config"
	"github.com/nexusriot/omegagrid-agent-go/internal/mcp"
	"github.com/nexusriot/omegagrid-agent-go/internal/skills"
)

// mcpBackoff bounds the reconnect delay for a server that is down.
var (
	mcpBackoffMin = 5 * time.Second
	mcpBackoffMax = 5 * time.Minute
)

// mcpServer keeps one MCP_SERVERS entry's tools registered: it connects
// (retrying with backoff while the server is down), lists the tools, and
// re-lists them on a timer or when the server announces a change, adding,
// replacing and withdrawing skills to match.
type mcpServer struct {
	name   string
	url    string
	client *mcp.Client
	sk     *skills.Client
	native map[string]bool

	tools map[string]mcp.Tool // last registered definitions, by remote name

	// backoffMin/Max are copied from the package defaults when the server is
	// set up, so the goroutine never reads the (test-adjustable) globals.
	backoffMin, backoffMax time.Duration
}

// startMCPServers connects every configured server concurrently and returns
// once each has made its first attempt or cfg.MCPConnectWait has passed,
// whichever is first. Servers dialled one after another used to delay
// startup — and every local-mode omega command — by up to 30 s each, and one
// that was down at boot was never retried. ctx stops all background work.
func startMCPServers(ctx context.Context, cfg config.Config, sk *skills.Client, native map[string]bool) {
	var first sync.WaitGroup
	for i, entry := range cfg.MCPServers {
		name, url, headers, err := parseMCPEntry(entry)
		if err != nil {
			// Never log entry itself: it can carry "|Authorization: Bearer …".
			log.Printf("mcp client: skipping MCP_SERVERS entry #%d: %v", i+1, err)
			continue
		}
		s := &mcpServer{
			name: name, url: url, sk: sk, native: native,
			client:     mcp.NewClient(url, headers, 30*time.Second),
			tools:      map[string]mcp.Tool{},
			backoffMin: mcpBackoffMin,
			backoffMax: mcpBackoffMax,
		}
		first.Add(1)
		go s.run(ctx, &first, time.Duration(cfg.MCPRefreshSec*float64(time.Second)))
	}
	done := make(chan struct{})
	go func() { first.Wait(); close(done) }()
	wait := time.Duration(cfg.MCPConnectWait * float64(time.Second))
	if wait <= 0 {
		wait = 10 * time.Second
	}
	select {
	case <-done:
	case <-time.After(wait):
		log.Printf("mcp client: not every server answered within %s; they keep connecting in the background", wait)
	case <-ctx.Done():
	}
}

func (s *mcpServer) run(ctx context.Context, first *sync.WaitGroup, refresh time.Duration) {
	firstDone := sync.OnceFunc(first.Done)
	defer firstDone()
	backoff := s.backoffMin
	for {
		err := s.connectAndSync(ctx)
		firstDone()
		if err == nil {
			break
		}
		log.Printf("mcp client %q: %v; retrying in %s", s.name, err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, s.backoffMax)
	}
	var tick <-chan time.Time
	if refresh > 0 {
		t := time.NewTicker(refresh)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
		case <-s.client.ListChanged():
		}
		if err := s.connectAndSync(ctx); err != nil {
			// Keep the tools we have: a server that is briefly down still
			// answers once it is back, and CallTool reports the outage.
			log.Printf("mcp client %q: refresh failed: %v", s.name, err)
		}
	}
}

// connectAndSync (re)initializes the session and reconciles the registered
// tools with the server's current list.
func (s *mcpServer) connectAndSync(ctx context.Context) error {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := s.client.Initialize(cctx); err != nil {
		return err
	}
	tools, err := s.client.ListTools(cctx)
	if err != nil {
		return err
	}
	s.sync(tools)
	return nil
}

func (s *mcpServer) sync(tools []mcp.Tool) {
	seen := make(map[string]bool, len(tools))
	added, changed, removed := 0, 0, 0
	for _, t := range tools {
		seen[t.Name] = true
		old, had := s.tools[t.Name]
		if had && reflect.DeepEqual(old, t) {
			continue
		}
		if had {
			s.sk.Unregister(s.name + "_" + t.Name)
		}
		if err := registerRemoteTool(s.sk, s.client, s.name, t, s.native); err != nil {
			log.Printf("mcp client %q: skipping tool %q: %v", s.name, t.Name, err)
			delete(s.tools, t.Name)
			continue
		}
		s.tools[t.Name] = t
		if had {
			changed++
		} else {
			added++
		}
	}
	for name := range s.tools {
		if !seen[name] {
			s.sk.Unregister(s.name + "_" + name)
			delete(s.tools, name)
			removed++
		}
	}
	if added+changed+removed > 0 {
		log.Printf("mcp client %q: %d tool(s) registered from %s (+%d ~%d -%d)", s.name, len(s.tools), s.url, added, changed, removed)
	}
}
