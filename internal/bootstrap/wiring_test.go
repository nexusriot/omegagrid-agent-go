package bootstrap

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/nexusriot/omegagrid-agent-go/internal/agent"
	"github.com/nexusriot/omegagrid-agent-go/internal/config"
	"github.com/nexusriot/omegagrid-agent-go/internal/llm"
	"github.com/nexusriot/omegagrid-agent-go/internal/mcp"
	"github.com/nexusriot/omegagrid-agent-go/internal/skills"
)

func TestBuildChatOllamaDefault(t *testing.T) {
	c, err := BuildChat(config.Config{
		Provider:    "ollama",
		OllamaURL:   "http://127.0.0.1:11434",
		OllamaModel: "llama3:latest",
	})
	if err != nil {
		t.Fatalf("BuildChat: %v", err)
	}
	if _, ok := c.(*llm.OllamaChat); !ok {
		t.Fatalf("got %T, want *llm.OllamaChat", c)
	}
	if c.Model() != "llama3:latest" {
		t.Fatalf("Model = %q", c.Model())
	}

	// An unknown provider name falls back to Ollama rather than failing to boot.
	c, err = BuildChat(config.Config{Provider: "nonsense", OllamaModel: "m"})
	if err != nil {
		t.Fatalf("BuildChat(unknown provider): %v", err)
	}
	if _, ok := c.(*llm.OllamaChat); !ok {
		t.Fatalf("unknown provider gave %T, want *llm.OllamaChat", c)
	}
}

func TestBuildChatOpenAIVariants(t *testing.T) {
	for _, provider := range []string{"openai", "openai-codex", "codex"} {
		t.Run(provider, func(t *testing.T) {
			// A missing key must fail loudly at boot, not on the first query.
			if _, err := BuildChat(config.Config{Provider: provider}); err == nil {
				t.Fatal("expected an error without OPENAI_API_KEY")
			}

			c, err := BuildChat(config.Config{
				Provider:        provider,
				OpenAIAPIKey:    "sk-test",
				OpenAIBaseURL:   "https://api.openai.com/v1",
				OpenAIChatModel: "gpt-4o-mini",
				OpenAIAPIMode:   "chat_completions",
			})
			if err != nil {
				t.Fatalf("BuildChat: %v", err)
			}
			if _, ok := c.(*llm.OpenAIChat); !ok {
				t.Fatalf("got %T, want *llm.OpenAIChat", c)
			}
			if c.BaseURL() != "https://api.openai.com/v1" {
				t.Fatalf("BaseURL = %q", c.BaseURL())
			}
		})
	}
}

// DigitalOcean is OpenAI-compatible: same client, its own key/URL/model, and
// forced into chat_completions mode.
func TestBuildChatDigitalOcean(t *testing.T) {
	for _, provider := range []string{"digitalocean", "do"} {
		if _, err := BuildChat(config.Config{Provider: provider}); err == nil {
			t.Fatalf("%s: expected an error without DIGITALOCEAN_API_KEY", provider)
		}

		c, err := BuildChat(config.Config{
			Provider:              provider,
			DigitalOceanAPIKey:    "do-test",
			DigitalOceanBaseURL:   "https://inference.do-ai.run/v1",
			DigitalOceanChatModel: "meta-llama/Llama-3.3-70B-Instruct",
			// Must be ignored: DO speaks chat_completions, not /responses.
			OpenAIAPIMode: "responses",
			OpenAIAPIKey:  "sk-should-not-be-used",
		})
		if err != nil {
			t.Fatalf("%s: BuildChat: %v", provider, err)
		}
		if c.Model() != "meta-llama/Llama-3.3-70B-Instruct" {
			t.Fatalf("%s: Model = %q", provider, c.Model())
		}
		if c.BaseURL() != "https://inference.do-ai.run/v1" {
			t.Fatalf("%s: BaseURL = %q", provider, c.BaseURL())
		}
	}
}

func TestEnsureDataDirs(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nested", "data")
	cfg := config.Config{
		DataDir:     root,
		AgentDB:     filepath.Join(root, "db", "agent.sqlite3"),
		VectorDir:   filepath.Join(root, "chromem"),
		SkillsDir:   filepath.Join(root, "skills"),
		SchedulerDB: filepath.Join(root, "sched", "scheduler.sqlite3"),
	}
	if err := ensureDataDirs(cfg); err != nil {
		t.Fatalf("ensureDataDirs: %v", err)
	}
	// SQLite will not create parent directories; a fresh clone must still start.
	for _, dir := range []string{root, filepath.Join(root, "db"), cfg.VectorDir, cfg.SkillsDir, filepath.Join(root, "sched")} {
		st, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if !st.IsDir() {
			t.Fatalf("%s is not a directory", dir)
		}
	}
	// Idempotent: every restart calls it again.
	if err := ensureDataDirs(cfg); err != nil {
		t.Fatalf("second ensureDataDirs: %v", err)
	}
}

func TestEnsureDataDirsSkipsEmptyPaths(t *testing.T) {
	// A zero-valued config must not try to create "" or ".".
	if err := ensureDataDirs(config.Config{}); err != nil {
		t.Fatalf("ensureDataDirs on a zero config: %v", err)
	}
}

func TestEnsureDataDirsReportsFailure(t *testing.T) {
	// A file where a directory is required.
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	err := ensureDataDirs(config.Config{DataDir: filepath.Join(file, "data")})
	if err == nil {
		t.Fatal("expected an error when the parent path is a file")
	}
	if !strings.Contains(err.Error(), "create ") {
		t.Fatalf("error does not name the failing directory: %v", err)
	}
}

func TestToolProviderUnionsSkillsAndNatives(t *testing.T) {
	sk, err := skills.New(config.Config{SkillsDir: t.TempDir()})
	if err != nil {
		t.Fatalf("skills.New: %v", err)
	}
	native := map[string]agent.Skill{
		"schedule_task": {
			Schema:  skills.Skill{Name: "schedule_task", Description: "cron"},
			Execute: func(map[string]any) (any, error) { return map[string]any{"native": true}, nil },
		},
	}
	exec := func(name string, args map[string]any) (any, error) {
		if n, ok := native[name]; ok {
			return n.Execute(args)
		}
		return sk.Execute(name, args)
	}
	p := &toolProvider{skills: sk, native: native, exec: exec}

	tools := p.Tools()
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.Name] = true
	}
	for _, want := range []string{"weather", "datetime_skill", "skill_creator", "schedule_task"} {
		if !names[want] {
			t.Fatalf("MCP tool list is missing %q: %v", want, names)
		}
	}
	// vector_add/vector_search are agent-loop tools, not registry skills.
	if names["vector_add"] {
		t.Fatal("vector_add must not be advertised as an MCP tool")
	}

	// A native skill overrides nothing but must be callable through Call.
	res, err := p.Call("schedule_task", map[string]any{})
	if err != nil {
		t.Fatalf("Call(schedule_task): %v", err)
	}
	if m, _ := res.(map[string]any); m["native"] != true {
		t.Fatalf("Call routed schedule_task to the registry instead of the native impl: %v", res)
	}

	// A registry skill routes to the registry.
	if _, err := p.Call("datetime_skill", map[string]any{}); err != nil {
		t.Fatalf("Call(datetime_skill): %v", err)
	}
	if _, err := p.Call("no_such_tool", map[string]any{}); err == nil {
		t.Fatal("Call accepted an unknown tool")
	}
}

func TestSkillToMCPToolCarriesParameters(t *testing.T) {
	got := skillToMCPTool(skills.Skill{
		Name:        "weather",
		Description: "current weather",
		Parameters: map[string]skills.Param{
			"city": {Type: "string", Description: "City name", Required: true},
			"unit": {Type: "string", Description: "C or F", Required: false},
		},
	})
	if got.Name != "weather" || got.Description != "current weather" {
		t.Fatalf("tool = %+v", got)
	}
	if len(got.Parameters) != 2 {
		t.Fatalf("parameters = %v", got.Parameters)
	}
	if !got.Parameters["city"].Required || got.Parameters["unit"].Required {
		t.Fatalf("required flags lost: %v", got.Parameters)
	}
	if got.Parameters["city"].Type != "string" {
		t.Fatalf("type lost: %v", got.Parameters["city"])
	}
}

// mcpTestServer answers initialize / tools/list / tools/call for one tool.
func mcpTestServer(t *testing.T, calls *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		*calls = append(*calls, req.Method)

		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": mcp.ProtocolVersion, "serverInfo": map[string]any{"name": "remote"}}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{
				"name":        "echo",
				"description": "Echo the input",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"text": map[string]any{"type": "string", "description": "what to echo"}},
					"required":   []string{"text"},
				},
			}}}
		case "tools/call":
			result = map[string]any{
				"content": []map[string]any{{"type": "text", "text": "echoed: " + req.Params.Arguments["text"].(string)}},
				"isError": false,
			}
		default:
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
}

// End to end: a configured remote MCP server's tools become callable skills,
// namespaced <server>_<tool>.
func TestConnectMCPServersRegistersRemoteTools(t *testing.T) {
	var calls []string
	srv := mcpTestServer(t, &calls)
	defer srv.Close()

	sk, err := skills.New(config.Config{SkillsDir: t.TempDir()})
	if err != nil {
		t.Fatalf("skills.New: %v", err)
	}
	connectMCPServers(config.Config{MCPServers: []string{"remote=" + srv.URL}}, sk)

	list, err := sk.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var found *skills.Skill
	for i := range list {
		if list[i].Name == "remote_echo" {
			found = &list[i]
		}
	}
	if found == nil {
		t.Fatal("remote tool was not registered as remote_echo")
	}
	// The description is tagged so an operator can tell where a skill came from.
	if !strings.HasPrefix(found.Description, "[MCP:remote]") {
		t.Fatalf("description = %q, want an [MCP:remote] prefix", found.Description)
	}
	if p, ok := found.Parameters["text"]; !ok || !p.Required {
		t.Fatalf("inputSchema did not convert: %v", found.Parameters)
	}

	res, err := sk.Execute("remote_echo", map[string]any{"text": "hi"})
	if err != nil {
		t.Fatalf("Execute(remote_echo): %v", err)
	}
	if res != "echoed: hi" {
		t.Fatalf("result = %v", res)
	}

	// The handshake must happen before tools/list.
	if len(calls) < 2 || calls[0] != "initialize" {
		t.Fatalf("call sequence = %v, want initialize first", calls)
	}
}

// An unreachable or malformed server is logged and skipped — it must never
// block startup.
func TestConnectMCPServersToleratesBadEntries(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	sk, err := skills.New(config.Config{SkillsDir: t.TempDir()})
	if err != nil {
		t.Fatalf("skills.New: %v", err)
	}
	before, _ := sk.List()

	connectMCPServers(config.Config{MCPServers: []string{
		"no-equals-sign",
		"=missing-name",
		"empty-url=",
		"dead=" + deadURL,
	}}, sk)

	after, _ := sk.List()
	if len(after) != len(before) {
		t.Fatalf("skill count changed from %d to %d despite every server failing", len(before), len(after))
	}
}

func TestConnectMCPServersSendsAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gotAuth == "" {
			gotAuth = r.Header.Get("Authorization")
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": req.ID,
			"result": map[string]any{"tools": []any{}},
		})
	}))
	defer srv.Close()

	sk, err := skills.New(config.Config{SkillsDir: t.TempDir()})
	if err != nil {
		t.Fatalf("skills.New: %v", err)
	}
	connectMCPServers(config.Config{
		MCPServers: []string{"remote=" + srv.URL + "|Authorization: Bearer tok123"},
	}, sk)

	if gotAuth != "Bearer tok123" {
		t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer tok123")
	}
}

// New() is the single wiring point shared by the gateway and the CLI's local
// mode. It must produce a fully-formed service set — and must succeed even
// though the embeddings backend here is unreachable, because the gateway is
// expected to start and report the failure via /health rather than refuse to boot.
func TestNewWiresEveryService(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		DataDir:            dir,
		Provider:           "ollama",
		OllamaURL:          "http://127.0.0.1:19999", // unreachable on purpose
		OllamaModel:        "llama3:latest",
		OllamaEmbedModel:   "nomic-embed-text",
		OllamaTimeoutSec:   0.1,
		AgentDB:            filepath.Join(dir, "agent.sqlite3"),
		VectorDir:          filepath.Join(dir, "chromem"),
		VectorCollection:   "memories",
		SkillsDir:          filepath.Join(dir, "skills"),
		SchedulerDB:        filepath.Join(dir, "scheduler.sqlite3"),
		DedupDistance:      0.08,
		ContextTail:        30,
		MemoryHits:         5,
		AgentParallelTools: true,
		AgentMaxParallel:   3,
		SchedulerTickSec:   3600, // don't fire during the test
		AuditMaxBlobBytes:  65536,
	}

	svc, cleanup, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer cleanup()

	for name, got := range map[string]any{
		"Chat": svc.Chat, "Memory": svc.Memory, "Skills": svc.Skills,
		"Sched": svc.Sched, "Runner": svc.Runner, "Agent": svc.Agent,
		"ToolProvider": svc.ToolProvider,
	} {
		if got == nil {
			t.Fatalf("Services.%s is nil", name)
		}
	}

	// Agent config must be threaded through, not left at zero values.
	if svc.Agent.ContextTail != 30 || svc.Agent.MemoryHits != 5 {
		t.Fatalf("agent context/memory config not wired: %+v", svc.Agent)
	}
	if !svc.Agent.ParallelEnabled || svc.Agent.MaxParallel != 3 {
		t.Fatalf("parallel-tool config not wired: %+v", svc.Agent)
	}

	// Both native skills are registered and reachable from the agent's table.
	for _, name := range []string{"schedule_task", "web_search"} {
		if _, ok := svc.NativeSkills[name]; !ok {
			t.Fatalf("native skill %q missing: %v", name, svc.NativeSkills)
		}
	}

	// The data directories were created even though none existed.
	for _, d := range []string{cfg.VectorDir, cfg.SkillsDir} {
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			t.Fatalf("%s was not created: %v", d, err)
		}
	}

	// schedule_task talks to the same store the gateway exposes over HTTP.
	if _, err := svc.NativeSkills["schedule_task"].Execute(map[string]any{
		"action": "create", "cron_expr": "0 9 * * *", "skill": "reminder",
		"args": map[string]any{"message": "standup"},
	}); err != nil {
		t.Fatalf("schedule_task create: %v", err)
	}
	tasks, err := svc.Sched.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("scheduler has %d tasks, want 1 — the native skill is not sharing the store", len(tasks))
	}
}

func TestNewFailsOnBadProviderConfig(t *testing.T) {
	dir := t.TempDir()
	// An openai provider with no key must fail before any store is opened.
	_, cleanup, err := New(config.Config{
		DataDir:     dir,
		Provider:    "openai",
		AgentDB:     filepath.Join(dir, "agent.sqlite3"),
		VectorDir:   filepath.Join(dir, "chromem"),
		SkillsDir:   filepath.Join(dir, "skills"),
		SchedulerDB: filepath.Join(dir, "scheduler.sqlite3"),
	})
	if err == nil {
		if cleanup != nil {
			cleanup()
		}
		t.Fatal("expected an error for openai without an API key")
	}
	if !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatalf("error does not name the missing setting: %v", err)
	}
}

// tools/list is served straight to MCP clients, so its order must not depend on
// Go's map iteration: the native tools used to land in a different position on
// every call.
func TestToolProviderOrderIsStable(t *testing.T) {
	sk, err := skills.New(config.Config{SkillsDir: t.TempDir()})
	if err != nil {
		t.Fatalf("skills.New: %v", err)
	}
	native := map[string]agent.Skill{
		"schedule_task": {Schema: skills.Skill{Name: "schedule_task"}},
		"web_search":    {Schema: skills.Skill{Name: "web_search"}},
		"aaa_native":    {Schema: skills.Skill{Name: "aaa_native"}},
	}
	p := &toolProvider{skills: sk, native: native}

	first := p.Tools()
	if !sort.SliceIsSorted(first, func(i, j int) bool { return first[i].Name < first[j].Name }) {
		t.Fatalf("tool list is not name-sorted: %v", toolNames(first))
	}
	for i := 0; i < 20; i++ {
		got := p.Tools()
		if len(got) != len(first) {
			t.Fatalf("tool count changed: %d != %d", len(got), len(first))
		}
		for j := range got {
			if got[j].Name != first[j].Name {
				t.Fatalf("tool order changed at %d: %q != %q", j, got[j].Name, first[j].Name)
			}
		}
	}
}

func toolNames(tools []mcp.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}
