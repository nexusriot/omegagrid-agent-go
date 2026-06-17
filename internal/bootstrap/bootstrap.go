// Package bootstrap wires all runtime services from a Config.  Both
// cmd/gateway and cmd/cli (local mode) call New() so service construction
// lives in exactly one place.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nexusriot/omegagrid-agent-go/internal/agent"
	"github.com/nexusriot/omegagrid-agent-go/internal/config"
	"github.com/nexusriot/omegagrid-agent-go/internal/llm"
	"github.com/nexusriot/omegagrid-agent-go/internal/mcp"
	"github.com/nexusriot/omegagrid-agent-go/internal/memory"
	"github.com/nexusriot/omegagrid-agent-go/internal/scheduler"
	"github.com/nexusriot/omegagrid-agent-go/internal/search"
	"github.com/nexusriot/omegagrid-agent-go/internal/skills"
)

// ensureDataDirs creates every on-disk directory the runtime writes to.
// SQLite drivers don't create parent directories, so a fresh clone with no
// existing DATA_DIR would otherwise fail on first start with "unable to open
// database file". Running this at bootstrap removes the need for users to
// `mkdir -p data/...` before launching the gateway.
func ensureDataDirs(cfg config.Config) error {
	dirs := []string{
		cfg.DataDir,
		filepath.Dir(cfg.AgentDB),
		cfg.VectorDir,
		cfg.SkillsDir,
		filepath.Dir(cfg.SchedulerDB),
	}
	for _, d := range dirs {
		if d == "" || d == "." {
			continue
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	return nil
}

// Services holds every initialised service.
type Services struct {
	Chat    llm.ChatClient
	Memory  *memory.Client
	Skills  *skills.Client
	Sched   *scheduler.Store
	Runner  *scheduler.Runner
	Agent   *agent.Service

	// Exposed so callers can build httpapi.Deps without re-importing agent.
	NativeSkills map[string]agent.Skill

	// ToolProvider unions registry skills + native skills behind the MCP
	// server's interface (also used to mount the MCP endpoint).
	ToolProvider mcp.ToolProvider
}

// toolProvider adapts the skill registry + native skills to mcp.ToolProvider.
type toolProvider struct {
	skills *skills.Client
	native map[string]agent.Skill
	exec   func(string, map[string]any) (any, error)
}

func (p *toolProvider) Tools() []mcp.Tool {
	var out []mcp.Tool
	if list, err := p.skills.List(); err == nil {
		for _, s := range list {
			out = append(out, skillToMCPTool(s))
		}
	}
	for _, n := range p.native {
		out = append(out, skillToMCPTool(n.Schema))
	}
	return out
}

func (p *toolProvider) Call(name string, args map[string]any) (any, error) {
	return p.exec(name, args)
}

func skillToMCPTool(s skills.Skill) mcp.Tool {
	params := make(map[string]mcp.Param, len(s.Parameters))
	for k, v := range s.Parameters {
		params[k] = mcp.Param{Type: v.Type, Description: v.Description, Required: v.Required}
	}
	return mcp.Tool{Name: s.Name, Description: s.Description, Parameters: params}
}

// New builds all services from cfg. The returned cleanup func must be called
// (e.g. via defer) to close database handles and stop background goroutines.
func New(cfg config.Config) (*Services, func(), error) {
	if err := ensureDataDirs(cfg); err != nil {
		return nil, nil, err
	}
	chat, err := BuildChat(cfg)
	if err != nil {
		return nil, nil, err
	}

	mem, err := memory.New(cfg)
	if err != nil {
		return nil, nil, err
	}

	sk, err := skills.New(cfg)
	if err != nil {
		mem.Close()
		return nil, nil, err
	}

	// Best-effort: connect to remote MCP servers and register their tools as
	// skills. A failing server is logged and skipped — it must not block startup.
	connectMCPServers(cfg, sk)

	store, err := scheduler.NewStore(cfg.SchedulerDB)
	if err != nil {
		mem.Close()
		return nil, nil, err
	}

	schedSkill := &scheduler.ScheduleTaskSkill{Store: store}
	searchSkill := search.NewWebSearchSkill()

	native := map[string]agent.Skill{
		"schedule_task": {
			Schema: SkillSchemaFromMap(schedSkill.SkillSchema()),
			Execute: func(args map[string]any) (any, error) {
				return schedSkill.Execute(args), nil
			},
		},
		"web_search": {
			Schema: SkillSchemaFromMap(searchSkill.SkillSchema()),
			Execute: func(args map[string]any) (any, error) {
				return searchSkill.Execute(args), nil
			},
		},
	}

	exec := func(name string, args map[string]any) (any, error) {
		if n, ok := native[name]; ok {
			return n.Execute(args)
		}
		return sk.Execute(name, args)
	}

	runner := scheduler.NewRunner(
		store, exec, cfg.TelegramBotToken,
		time.Duration(cfg.SchedulerTickSec)*time.Second,
	)
	runner.Start()

	ag := &agent.Service{
		Memory:         mem,
		Skills:         sk,
		Chat:           chat,
		NativeSkills:   native,
		ContextTail:    cfg.ContextTail,
		MemoryHits:     cfg.MemoryHits,
		ParallelEnabled: cfg.AgentParallelTools,
		MaxParallel:    cfg.AgentMaxParallel,

		AutoMemoryExtract:      cfg.AutoMemoryExtract,
		AutoMemoryMaxFacts:     cfg.AutoMemoryMaxFacts,
		AutoMemoryMinAnswerLen: cfg.AutoMemoryMinAnswerLen,
	}

	svc := &Services{
		Chat:         chat,
		Memory:       mem,
		Skills:       sk,
		Sched:        store,
		Runner:       runner,
		Agent:        ag,
		NativeSkills: native,
		ToolProvider: &toolProvider{skills: sk, native: native, exec: exec},
	}

	cleanup := func() {
		runner.Stop()
		store.Close()
		mem.Close()
	}

	return svc, cleanup, nil
}

// BuildChat constructs the LLM chat client from cfg.
func BuildChat(cfg config.Config) (llm.ChatClient, error) {
	switch cfg.Provider {
	case "openai", "openai-codex", "codex":
		if cfg.OpenAIAPIKey == "" {
			return nil, errors.New("OPENAI_API_KEY is required for openai providers")
		}
		return llm.NewOpenAIChat(
			cfg.OpenAIAPIKey, cfg.OpenAIBaseURL, cfg.OpenAIChatModel,
			cfg.OpenAIAPIMode, cfg.OpenAIReasoning, cfg.OpenAITimeoutSec,
		), nil
	case "digitalocean", "do":
		if cfg.DigitalOceanAPIKey == "" {
			return nil, errors.New("DIGITALOCEAN_API_KEY is required for digitalocean provider")
		}
		// DigitalOcean Serverless Inference is OpenAI-compatible (POST /v1/chat/completions,
		// bearer auth) so we reuse the OpenAI chat client.
		return llm.NewOpenAIChat(
			cfg.DigitalOceanAPIKey, cfg.DigitalOceanBaseURL, cfg.DigitalOceanChatModel,
			"chat_completions", "", cfg.DigitalOceanTimeoutSec,
		), nil
	default:
		return llm.NewOllamaChat(cfg.OllamaURL, cfg.OllamaModel, cfg.OllamaTimeoutSec), nil
	}
}

// SkillSchemaFromMap converts the loose map[string]any returned by native
// skills into the typed skills.Skill struct used by the agent.
func SkillSchemaFromMap(m map[string]any) skills.Skill {
	out := skills.Skill{
		Name:        getString(m, "name"),
		Description: getString(m, "description"),
		Parameters:  map[string]skills.Param{},
	}
	if params, ok := m["parameters"].(map[string]any); ok {
		for k, v := range params {
			pm, _ := v.(map[string]any)
			required, _ := pm["required"].(bool)
			out.Parameters[k] = skills.Param{
				Type:        getString(pm, "type"),
				Description: getString(pm, "description"),
				Required:    required,
			}
		}
	}
	return out
}

func getString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// connectMCPServers dials each configured remote MCP server, lists its tools,
// and registers them into the skill registry namespaced as "<server>_<tool>".
// Errors are logged and the server skipped; startup is never blocked.
func connectMCPServers(cfg config.Config, sk *skills.Client) {
	for _, entry := range cfg.MCPServers {
		name, url, headers, err := parseMCPEntry(entry)
		if err != nil {
			log.Printf("mcp client: skipping %q: %v", entry, err)
			continue
		}
		client := mcp.NewClient(name, url, headers, 30*time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := client.Initialize(ctx); err != nil {
			cancel()
			log.Printf("mcp client %q: initialize failed: %v", name, err)
			continue
		}
		tools, err := client.ListTools(ctx)
		cancel()
		if err != nil {
			log.Printf("mcp client %q: tools/list failed: %v", name, err)
			continue
		}
		for _, t := range tools {
			registerRemoteTool(sk, client, name, t)
		}
		log.Printf("mcp client %q: registered %d tool(s) from %s", name, len(tools), url)
	}
}

func registerRemoteTool(sk *skills.Client, client *mcp.Client, server string, t mcp.Tool) {
	skillName := server + "_" + t.Name
	desc := t.Description
	if desc == "" {
		desc = t.Name
	}
	desc = fmt.Sprintf("[MCP:%s] %s", server, desc)

	params := make(map[string]skills.Param, len(t.Parameters))
	for k, v := range t.Parameters {
		params[k] = skills.Param{Type: v.Type, Description: v.Description, Required: v.Required}
	}

	remoteName := t.Name
	sk.Register(skillName, desc, params, func(args map[string]any) (any, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		return client.CallTool(ctx, remoteName, args)
	})
}

// parseMCPEntry parses an MCP_SERVERS entry of the form:
//
//	name=https://host/mcp
//	name=https://host/mcp|Authorization: Bearer TOKEN
//
// The optional "|Header: Value" suffix adds one request header.
func parseMCPEntry(entry string) (name, url string, headers map[string]string, err error) {
	eq := strings.IndexByte(entry, '=')
	if eq <= 0 {
		return "", "", nil, fmt.Errorf("expected name=url")
	}
	name = strings.TrimSpace(entry[:eq])
	rest := strings.TrimSpace(entry[eq+1:])
	if name == "" || rest == "" {
		return "", "", nil, fmt.Errorf("expected name=url")
	}
	if pipe := strings.IndexByte(rest, '|'); pipe >= 0 {
		url = strings.TrimSpace(rest[:pipe])
		hdr := strings.TrimSpace(rest[pipe+1:])
		if colon := strings.IndexByte(hdr, ':'); colon > 0 {
			headers = map[string]string{
				strings.TrimSpace(hdr[:colon]): strings.TrimSpace(hdr[colon+1:]),
			}
		}
	} else {
		url = rest
	}
	if url == "" {
		return "", "", nil, fmt.Errorf("empty url")
	}
	return name, url, headers, nil
}
