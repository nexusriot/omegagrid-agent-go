// Package bootstrap wires all runtime services from a Config.  Both
// cmd/gateway and cmd/cli (local mode) call New() so service construction
// lives in exactly one place.
package bootstrap

import (
	"errors"
	"time"

	"github.com/nexusriot/omegagrid-agent-go/internal/agent"
	"github.com/nexusriot/omegagrid-agent-go/internal/config"
	"github.com/nexusriot/omegagrid-agent-go/internal/llm"
	"github.com/nexusriot/omegagrid-agent-go/internal/memory"
	"github.com/nexusriot/omegagrid-agent-go/internal/scheduler"
	"github.com/nexusriot/omegagrid-agent-go/internal/search"
	"github.com/nexusriot/omegagrid-agent-go/internal/skills"
)

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
}

// New builds all services from cfg. The returned cleanup func must be called
// (e.g. via defer) to close database handles and stop background goroutines.
func New(cfg config.Config) (*Services, func(), error) {
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
	}

	svc := &Services{
		Chat:         chat,
		Memory:       mem,
		Skills:       sk,
		Sched:        store,
		Runner:       runner,
		Agent:        ag,
		NativeSkills: native,
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
