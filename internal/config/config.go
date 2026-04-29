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

	// LLM provider
	Provider         string // ollama, openai, openai-codex
	OllamaURL        string
	OllamaModel      string
	OllamaTimeoutSec float64
	OpenAIAPIKey     string
	OpenAIBaseURL    string
	OpenAIChatModel  string
	OpenAITimeoutSec float64
	OpenAIAPIMode    string // chat_completions or responses
	OpenAIReasoning  string

	// Memory / vector store
	AgentDB          string
	VectorDir        string
	VectorCollection string
	DedupDistance    float64
	OllamaEmbedModel string
	OpenAIEmbedModel string

	// Skills
	SkillsDir          string
	SkillHTTPTimeout   float64
	SkillShellEnabled  bool
	SkillSSHEnabled    bool
	SkillSSHPrivKey    string // PEM or base64-encoded PEM
	SkillSSHDefaultUser string
	SkillSSHIdentFile  string

	// Agent loop
	ContextTail   int
	MemoryHits    int
	AgentMaxSteps int

	// Scheduler
	SchedulerDB      string
	SchedulerTickSec int
	TelegramBotToken string
}

func Load() Config {
	dataDir := getOr(os.Getenv("DATA_DIR"), "/app/data")
	c := Config{
		BackendPort:         atoiOr(os.Getenv("BACKEND_PORT"), 8000),
		DataDir:             dataDir,
		Provider:            strings.ToLower(getOr(os.Getenv("LLM_PROVIDER"), "ollama")),
		OllamaURL:           strings.TrimRight(getOr(os.Getenv("OLLAMA_URL"), "http://127.0.0.1:11434"), "/"),
		OllamaModel:         getOr(os.Getenv("OLLAMA_MODEL"), "llama3:latest"),
		OllamaTimeoutSec:    atofOr(os.Getenv("OLLAMA_TIMEOUT"), 120),
		OpenAIAPIKey:        os.Getenv("OPENAI_API_KEY"),
		OpenAIBaseURL:       strings.TrimRight(getOr(os.Getenv("OPENAI_BASE_URL"), "https://api.openai.com/v1"), "/"),
		OpenAIChatModel:     os.Getenv("OPENAI_CHAT_MODEL"),
		OpenAITimeoutSec:    atofOr(os.Getenv("OPENAI_TIMEOUT"), 120),
		OpenAIAPIMode:       strings.ToLower(strings.TrimSpace(os.Getenv("OPENAI_API_MODE"))),
		OpenAIReasoning:     strings.ToLower(strings.TrimSpace(os.Getenv("OPENAI_REASONING_EFFORT"))),
		VectorCollection:    getOr(os.Getenv("AGENT_VECTOR_COLLECTION"), "memories"),
		DedupDistance:       atofOr(os.Getenv("AGENT_DEDUP_DISTANCE"), 0.08),
		OllamaEmbedModel:    getOr(os.Getenv("OLLAMA_EMBED_MODEL"), "nomic-embed-text"),
		OpenAIEmbedModel:    getOr(os.Getenv("OPENAI_EMBED_MODEL"), "text-embedding-3-small"),
		SkillHTTPTimeout:    atofOr(os.Getenv("SKILL_HTTP_TIMEOUT"), 30),
		SkillShellEnabled:   isTruthy(os.Getenv("SKILL_SHELL_ENABLED")),
		SkillSSHEnabled:     isTruthy(os.Getenv("SKILL_SSH_ENABLED")),
		SkillSSHPrivKey:     os.Getenv("SKILL_SSH_PRIVATE_KEY"),
		SkillSSHDefaultUser: getOr(os.Getenv("SKILL_SSH_DEFAULT_USER"), "root"),
		SkillSSHIdentFile:   os.Getenv("SKILL_SSH_IDENTITY_FILE"),
		ContextTail:         atoiOr(os.Getenv("AGENT_CONTEXT_TAIL"), 30),
		MemoryHits:          atoiOr(os.Getenv("AGENT_MEMORY_HITS"), 5),
		AgentMaxSteps:       atoiOr(os.Getenv("AGENT_MAX_STEPS"), 25),
		SchedulerTickSec:    atoiOr(os.Getenv("SCHEDULER_TICK_SEC"), 60),
		TelegramBotToken:    os.Getenv("TELEGRAM_BOT_TOKEN"),
	}
	c.AgentDB = getOr(os.Getenv("AGENT_DB"), dataDir+"/agent_memory.sqlite3")
	c.VectorDir = getOr(os.Getenv("AGENT_VECTOR_DIR"), dataDir+"/chromem")
	c.SkillsDir = getOr(os.Getenv("SKILLS_DIR"), dataDir+"/skills")
	c.SchedulerDB = getOr(os.Getenv("SCHEDULER_DB"), dataDir+"/scheduler.sqlite3")

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

func isTruthy(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v == "true" || v == "1" || v == "yes"
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
