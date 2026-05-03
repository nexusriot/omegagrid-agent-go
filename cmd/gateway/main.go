package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nexusriot/omegagrid-agent-go/internal/agent"
	"github.com/nexusriot/omegagrid-agent-go/internal/config"
	"github.com/nexusriot/omegagrid-agent-go/internal/httpapi"
	"github.com/nexusriot/omegagrid-agent-go/internal/llm"
	"github.com/nexusriot/omegagrid-agent-go/internal/memory"
	"github.com/nexusriot/omegagrid-agent-go/internal/scheduler"
	"github.com/nexusriot/omegagrid-agent-go/internal/search"
	"github.com/nexusriot/omegagrid-agent-go/internal/skills"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	cfg := config.Load()

	chat, err := buildChat(cfg)
	if err != nil {
		log.Fatalf("llm init: %v", err)
	}

	// In-process memory store (SQLite history + chromem-go vector).
	mem, err := memory.New(cfg)
	if err != nil {
		log.Fatalf("memory init: %v", err)
	}
	defer mem.Close()

	// In-process skill registry (all 21 built-ins + markdown skills).
	sk, err := skills.New(cfg)
	if err != nil {
		log.Fatalf("skills init: %v", err)
	}

	store, err := scheduler.NewStore(cfg.SchedulerDB)
	if err != nil {
		log.Fatalf("scheduler store: %v", err)
	}
	defer store.Close()

	// Native schedule_task skill (talks to the Go scheduler store directly).
	scheduleSkill := &scheduler.ScheduleTaskSkill{Store: store}
	scheduleSchema := scheduleSkill.SkillSchema()

	// Native web_search skill (DuckDuckGo HTML — no API key required).
	searchSkill := search.NewWebSearchSkill()
	searchSchema := searchSkill.SkillSchema()

	nativeSkills := map[string]agent.Skill{
		"schedule_task": {
			Schema: skillSchemaFromMap(scheduleSchema),
			Execute: func(args map[string]any) (any, error) {
				return scheduleSkill.Execute(args), nil
			},
		},
		"web_search": {
			Schema: skillSchemaFromMap(searchSchema),
			Execute: func(args map[string]any) (any, error) {
				return searchSkill.Execute(args), nil
			},
		},
	}

	// Scheduler runner can invoke both native and registered skills.
	exec := func(name string, args map[string]any) (any, error) {
		if native, ok := nativeSkills[name]; ok {
			return native.Execute(args)
		}
		return sk.Execute(name, args)
	}
	runner := scheduler.NewRunner(store, exec, cfg.TelegramBotToken, time.Duration(cfg.SchedulerTickSec)*time.Second)
	runner.Start()
	defer runner.Stop()

	ag := &agent.Service{
		Memory:       mem,
		Skills:       sk,
		Chat:         chat,
		NativeSkills: nativeSkills,
		ContextTail:  cfg.ContextTail,
		MemoryHits:   cfg.MemoryHits,
	}

	deps := httpapi.Deps{
		Cfg:       cfg,
		Agent:     ag,
		Memory:    mem,
		Skills:    sk,
		Chat:      chat,
		Scheduler: store,
	}

	addr := fmt.Sprintf(":%d", cfg.BackendPort)
	srv := &http.Server{
		Addr:              addr,
		Handler:           httpapi.NewRouter(deps),
		ReadHeaderTimeout: 30 * time.Second,
	}

	go func() {
		log.Printf("gateway listening on %s (provider=%s, model=%s, skills-dir=%s)",
			addr, cfg.Provider, chat.Model(), cfg.SkillsDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func buildChat(cfg config.Config) (llm.ChatClient, error) {
	switch cfg.Provider {
	case "openai", "openai-codex", "codex":
		if cfg.OpenAIAPIKey == "" {
			return nil, errors.New("OPENAI_API_KEY is required for openai providers")
		}
		return llm.NewOpenAIChat(cfg.OpenAIAPIKey, cfg.OpenAIBaseURL, cfg.OpenAIChatModel, cfg.OpenAIAPIMode, cfg.OpenAIReasoning, cfg.OpenAITimeoutSec), nil
	default:
		return llm.NewOllamaChat(cfg.OllamaURL, cfg.OllamaModel, cfg.OllamaTimeoutSec), nil
	}
}

// skillSchemaFromMap converts the loose map[string]any returned by native
// skills into the typed skills.Skill struct used by the agent.
func skillSchemaFromMap(m map[string]any) skills.Skill {
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
