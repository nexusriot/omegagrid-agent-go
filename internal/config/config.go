package config

import (
	"os"
	"strconv"
	"strings"
)

// Config holds runtime configuration loaded from environment variables.
// Mirrors the Python project's settings so docker-compose .env files
// can be reused with minimal changes.
type Config struct {
	// Gateway / data
	BackendPort int
	DataDir     string

	// Sidecar (Python skills + memory)
	SidecarURL string

	// LLM provider
	Provider             string // ollama, openai, openai-codex
	OllamaURL            string
	OllamaModel          string
	OllamaTimeoutSec     float64
	OpenAIAPIKey         string
	OpenAIBaseURL        string
	OpenAIChatModel      string
	OpenAITimeoutSec     float64
	OpenAIAPIMode        string // chat_completions or responses
	OpenAIReasoning      string

	// Agent loop
	ContextTail int
	MemoryHits  int

	// Scheduler
	SchedulerDB         string
	SchedulerTickSec    int
	TelegramBotToken    string
}

func Load() Config {
	c := Config{
		BackendPort:      atoiOr(os.Getenv("BACKEND_PORT"), 8000),
		DataDir:          getOr(os.Getenv("DATA_DIR"), "/app/data"),
		SidecarURL:       strings.TrimRight(getOr(os.Getenv("SIDECAR_URL"), "http://127.0.0.1:8001"), "/"),
		Provider:         strings.ToLower(getOr(os.Getenv("LLM_PROVIDER"), "ollama")),
		OllamaURL:        strings.TrimRight(getOr(os.Getenv("OLLAMA_URL"), "http://127.0.0.1:11434"), "/"),
		OllamaModel:      getOr(os.Getenv("OLLAMA_MODEL"), "llama3:latest"),
		OllamaTimeoutSec: atofOr(os.Getenv("OLLAMA_TIMEOUT"), 120),
		OpenAIAPIKey:     os.Getenv("OPENAI_API_KEY"),
		OpenAIBaseURL:    strings.TrimRight(getOr(os.Getenv("OPENAI_BASE_URL"), "https://api.openai.com/v1"), "/"),
		OpenAIChatModel:  os.Getenv("OPENAI_CHAT_MODEL"),
		OpenAITimeoutSec: atofOr(os.Getenv("OPENAI_TIMEOUT"), 120),
		OpenAIAPIMode:    strings.ToLower(strings.TrimSpace(os.Getenv("OPENAI_API_MODE"))),
		OpenAIReasoning:  strings.ToLower(strings.TrimSpace(os.Getenv("OPENAI_REASONING_EFFORT"))),
		ContextTail:      atoiOr(os.Getenv("AGENT_CONTEXT_TAIL"), 30),
		MemoryHits:       atoiOr(os.Getenv("AGENT_MEMORY_HITS"), 5),
		SchedulerTickSec: atoiOr(os.Getenv("SCHEDULER_TICK_SEC"), 60),
		TelegramBotToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
	}
	c.SchedulerDB = getOr(os.Getenv("SCHEDULER_DB"), c.DataDir+"/scheduler.sqlite3")

	// Default chat model + api mode resolution
	switch c.Provider {
	case "openai":
		if c.OpenAIChatModel == "" {
			c.OpenAIChatModel = "gpt-4o-mini"
		}
	case "openai-codex", "codex":
		if c.OpenAIChatModel == "" {
			c.OpenAIChatModel = "gpt-5.3-codex"
		}
	}
	if c.OpenAIAPIMode == "" {
		if strings.Contains(strings.ToLower(c.OpenAIChatModel), "codex") {
			c.OpenAIAPIMode = "responses"
		} else {
			c.OpenAIAPIMode = "chat_completions"
		}
	}
	return c
}

func getOr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func atoiOr(v string, def int) int {
	if v == "" {
		return def
	}
	if n, err := strconv.Atoi(v); err == nil {
		return n
	}
	return def
}

func atofOr(v string, def float64) float64 {
	if v == "" {
		return def
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		return f
	}
	return def
}
