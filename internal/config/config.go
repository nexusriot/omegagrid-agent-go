package config

import (
	"os"
	"strconv"
	"strings"
)

// Per-provider default chat models. Relays retire model names on their own
// schedule — a deprecated one answers 410 ModelDeprecated — so these are kept
// together here, and CHAT_MODEL (or the provider's own *_CHAT_MODEL variable)
// overrides any of them without a rebuild.
const (
	defaultOpenAIModel       = "gpt-4o-mini"
	defaultCodexModel        = "gpt-5.3-codex"
	defaultDigitalOceanModel = "meta-llama/Llama-3.3-70B-Instruct"
	defaultOpenCodeZenModel  = "claude-sonnet-5"
	defaultOpenCodeGoModel   = "kimi-k2.7-code"
)

// Config holds runtime configuration loaded from environment variables.
// Mirrors the Python project's settings so docker-compose .env files
// can be reused with minimal changes.
type Config struct {
	// Gateway / data
	BackendPort int
	DataDir     string

	// LLM provider
	Provider         string // ollama, openai, openai-codex, digitalocean, opencode
	OllamaURL        string
	OllamaModel      string
	OllamaTimeoutSec float64
	OpenAIAPIKey     string
	OpenAIBaseURL    string
	OpenAIChatModel  string
	OpenAITimeoutSec float64
	OpenAIAPIMode    string // chat_completions or responses
	OpenAIReasoning  string
	// OpenAITemperature is sent on the chat_completions path. A nil value omits
	// temperature entirely, which reasoning models (o-series / gpt-5 family)
	// require since they reject any non-default temperature.
	OpenAITemperature *float64

	// DigitalOcean Serverless Inference (OpenAI-compatible).
	// https://docs.digitalocean.com/reference/api/reference/inference-apis/
	DigitalOceanAPIKey     string
	DigitalOceanBaseURL    string
	DigitalOceanChatModel  string
	DigitalOceanEmbedModel string
	DigitalOceanTimeoutSec float64

	// opencode Zen relay (OpenAI-compatible).
	// https://opencode.ai/docs/zen/ — the "go" tier is the $10/mo subscription,
	// the "zen" tier is pay-as-you-go credits. One key works on both; the base
	// URL selects the tier.
	OpenCodeAPIKey     string
	OpenCodeBaseURL    string
	OpenCodeChatModel  string
	OpenCodeSessionID  string
	OpenCodeTimeoutSec float64

	// Memory / vector store
	AgentDB          string
	VectorDir        string
	VectorCollection string
	DedupDistance    float64
	OllamaEmbedModel string
	OpenAIEmbedModel string
	// EmbedProvider selects the embeddings backend independently of Provider.
	// Defaults to Provider; set EMBED_PROVIDER when the chat provider has no
	// embeddings endpoint of its own (opencode) or you want a cheaper one.
	EmbedProvider string

	// Skills
	SkillsDir           string
	SkillHTTPTimeout    float64
	SkillShellEnabled   bool
	SkillSSHEnabled     bool
	SkillSSHPrivKey     string // PEM or base64-encoded PEM
	SkillSSHDefaultUser string
	SkillSSHIdentFile   string

	// Agent loop
	ContextTail        int
	MemoryHits         int
	AgentMaxSteps      int
	AgentParallelTools bool
	AgentMaxParallel   int

	// Auto-memory extraction (post-turn fact distillation into vector memory)
	AutoMemoryExtract      bool
	AutoMemoryMaxFacts     int
	AutoMemoryMinAnswerLen int

	// Skill playground
	PlaygroundEnabled bool

	// Scheduler
	SchedulerDB      string
	SchedulerTickSec int
	TelegramBotToken string

	// Audit log
	AuditMaxBlobBytes int // 0 = audit disabled

	// MCP (Model Context Protocol)
	MCPServerEnabled bool     // expose skills as MCP tools at /mcp
	MCPServers       []string // remote MCP servers to consume: "name=url" entries
}

func Load() Config {
	dataDir := getOr(os.Getenv("DATA_DIR"), "/app/data")
	c := Config{
		BackendPort:            atoiOr(os.Getenv("BACKEND_PORT"), 8000),
		DataDir:                dataDir,
		Provider:               strings.ToLower(getOr(os.Getenv("LLM_PROVIDER"), "ollama")),
		OllamaURL:              strings.TrimRight(getOr(os.Getenv("OLLAMA_URL"), "http://127.0.0.1:11434"), "/"),
		OllamaModel:            getOr(os.Getenv("OLLAMA_MODEL"), "llama3:latest"),
		OllamaTimeoutSec:       atofOr(os.Getenv("OLLAMA_TIMEOUT"), 120),
		OpenAIAPIKey:           os.Getenv("OPENAI_API_KEY"),
		OpenAIBaseURL:          strings.TrimRight(getOr(os.Getenv("OPENAI_BASE_URL"), "https://api.openai.com/v1"), "/"),
		OpenAIChatModel:        os.Getenv("OPENAI_CHAT_MODEL"),
		OpenAITimeoutSec:       atofOr(os.Getenv("OPENAI_TIMEOUT"), 120),
		OpenAIAPIMode:          strings.ToLower(strings.TrimSpace(os.Getenv("OPENAI_API_MODE"))),
		OpenAIReasoning:        strings.ToLower(strings.TrimSpace(os.Getenv("OPENAI_REASONING_EFFORT"))),
		VectorCollection:       getOr(os.Getenv("AGENT_VECTOR_COLLECTION"), "memories"),
		DedupDistance:          atofOr(os.Getenv("AGENT_DEDUP_DISTANCE"), 0.08),
		OllamaEmbedModel:       getOr(os.Getenv("OLLAMA_EMBED_MODEL"), "nomic-embed-text"),
		OpenAIEmbedModel:       getOr(os.Getenv("OPENAI_EMBED_MODEL"), "text-embedding-3-small"),
		DigitalOceanAPIKey:     os.Getenv("DIGITALOCEAN_API_KEY"),
		DigitalOceanBaseURL:    strings.TrimRight(getOr(os.Getenv("DIGITALOCEAN_BASE_URL"), "https://inference.do-ai.run/v1"), "/"),
		DigitalOceanChatModel:  getOr(os.Getenv("DIGITALOCEAN_CHAT_MODEL"), defaultDigitalOceanModel),
		DigitalOceanEmbedModel: getOr(os.Getenv("DIGITALOCEAN_EMBED_MODEL"), "qwen3-embedding-0.6b"),
		DigitalOceanTimeoutSec: atofOr(os.Getenv("DIGITALOCEAN_TIMEOUT"), 120),
		OpenCodeAPIKey:         firstOf(os.Getenv("OPENCODE_API_KEY"), os.Getenv("OPENCODE_GO_API_KEY"), os.Getenv("OPENCODE_ZEN_API_KEY")),
		OpenCodeChatModel:      os.Getenv("OPENCODE_CHAT_MODEL"),
		OpenCodeSessionID:      strings.TrimSpace(os.Getenv("OPENCODE_SESSION_ID")),
		OpenCodeTimeoutSec:     atofOr(os.Getenv("OPENCODE_TIMEOUT"), 120),
		EmbedProvider:          strings.ToLower(strings.TrimSpace(os.Getenv("EMBED_PROVIDER"))),
		SkillHTTPTimeout:       atofOr(os.Getenv("SKILL_HTTP_TIMEOUT"), 30),
		SkillShellEnabled:      isTruthy(os.Getenv("SKILL_SHELL_ENABLED")),
		SkillSSHEnabled:        isTruthy(os.Getenv("SKILL_SSH_ENABLED")),
		SkillSSHPrivKey:        os.Getenv("SKILL_SSH_PRIVATE_KEY"),
		SkillSSHDefaultUser:    getOr(os.Getenv("SKILL_SSH_DEFAULT_USER"), "root"),
		SkillSSHIdentFile:      os.Getenv("SKILL_SSH_IDENTITY_FILE"),
		ContextTail:            atoiOr(os.Getenv("AGENT_CONTEXT_TAIL"), 30),
		MemoryHits:             atoiOr(os.Getenv("AGENT_MEMORY_HITS"), 5),
		AgentMaxSteps:          atoiOr(os.Getenv("AGENT_MAX_STEPS"), 25),
		AgentParallelTools:     isTruthy(os.Getenv("AGENT_PARALLEL_TOOLS")),
		AgentMaxParallel:       atoiOr(os.Getenv("AGENT_MAX_PARALLEL"), 4),
		AutoMemoryExtract:      isTruthy(os.Getenv("AUTO_MEMORY_EXTRACT")),
		AutoMemoryMaxFacts:     atoiOr(os.Getenv("AUTO_MEMORY_MAX_FACTS"), 5),
		AutoMemoryMinAnswerLen: atoiOr(os.Getenv("AUTO_MEMORY_MIN_ANSWER_LEN"), 80),
		PlaygroundEnabled:      !isTruthy(os.Getenv("PLAYGROUND_DISABLED")),
		SchedulerTickSec:       atoiOr(os.Getenv("SCHEDULER_TICK_SEC"), 60),
		TelegramBotToken:       os.Getenv("TELEGRAM_BOT_TOKEN"),
		AuditMaxBlobBytes:      atoiOr(os.Getenv("AUDIT_MAX_BLOB_BYTES"), 65536),
		MCPServerEnabled:       !isTruthy(os.Getenv("MCP_SERVER_DISABLED")),
		MCPServers:             splitCSV(os.Getenv("MCP_SERVERS")),
	}
	c.AgentDB = getOr(os.Getenv("AGENT_DB"), dataDir+"/agent_memory.sqlite3")
	c.VectorDir = getOr(os.Getenv("AGENT_VECTOR_DIR"), dataDir+"/chromem")
	c.SkillsDir = getOr(os.Getenv("SKILLS_DIR"), dataDir+"/skills")
	c.SchedulerDB = getOr(os.Getenv("SCHEDULER_DB"), dataDir+"/scheduler.sqlite3")
	c.OpenAITemperature = parseTemperature(os.Getenv("OPENAI_TEMPERATURE"))

	// Default chat model + api mode resolution
	switch c.Provider {
	case "openai":
		if c.OpenAIChatModel == "" {
			c.OpenAIChatModel = defaultOpenAIModel
		}
	case "openai-codex", "codex":
		if c.OpenAIChatModel == "" {
			c.OpenAIChatModel = defaultCodexModel
		}
	case "opencode-zen", "zen":
		if c.OpenCodeChatModel == "" {
			c.OpenCodeChatModel = defaultOpenCodeZenModel
		}
	case "opencode", "opencode-go":
		if c.OpenCodeChatModel == "" {
			c.OpenCodeChatModel = defaultOpenCodeGoModel
		}
	}
	// CHAT_MODEL is a provider-agnostic override: it saves callers from having
	// to know which of the per-provider variables the active provider reads,
	// and it wins over the defaults resolved just above (but not over the
	// provider's own variable, which is the more specific setting).
	if m := strings.TrimSpace(os.Getenv("CHAT_MODEL")); m != "" {
		switch c.Provider {
		case "openai", "openai-codex", "codex":
			c.OpenAIChatModel = getOr(os.Getenv("OPENAI_CHAT_MODEL"), m)
		case "digitalocean", "do":
			c.DigitalOceanChatModel = getOr(os.Getenv("DIGITALOCEAN_CHAT_MODEL"), m)
		case "opencode", "opencode-go", "opencode-zen", "zen":
			c.OpenCodeChatModel = getOr(os.Getenv("OPENCODE_CHAT_MODEL"), m)
		default:
			c.OllamaModel = getOr(os.Getenv("OLLAMA_MODEL"), m)
		}
	}
	// One opencode key serves two tiers hosted on different paths of the same
	// relay, so the provider alias — not a separate variable — picks which.
	openCodeBase := "https://opencode.ai/zen/go/v1"
	if c.Provider == "opencode-zen" || c.Provider == "zen" {
		openCodeBase = "https://opencode.ai/zen/v1"
	}
	c.OpenCodeBaseURL = strings.TrimRight(getOr(os.Getenv("OPENCODE_BASE_URL"), openCodeBase), "/")

	if c.EmbedProvider == "" {
		c.EmbedProvider = c.Provider
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

// splitCSV splits a comma-separated env value into trimmed, non-empty entries.
func splitCSV(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// parseTemperature reads OPENAI_TEMPERATURE. Empty (unset) → default 0.2.
// "none"/"omit"/"off" → nil, which tells the chat client to leave temperature
// out of the request entirely (reasoning models reject a non-default value).
// An unparseable value falls back to the 0.2 default.
func parseTemperature(v string) *float64 {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "none", "omit", "off":
		return nil
	case "":
		def := 0.2
		return &def
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		return &f
	}
	def := 0.2
	return &def
}

// firstOf returns the first non-empty value, for settings that accept more
// than one env name.
func firstOf(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
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
