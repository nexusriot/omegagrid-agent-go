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
	AgentDB              string
	VectorDir            string
	VectorCollection     string
	MemoryDefaultTTLDays float64 // 0 = memories never expire unless asked to
	DedupDistance        float64
	OllamaEmbedModel     string
	OpenAIEmbedModel     string
	// EmbedProvider selects the embeddings backend independently of Provider.
	// Defaults to Provider; set EMBED_PROVIDER when the chat provider has no
	// embeddings endpoint of its own (opencode) or you want a cheaper one.
	EmbedProvider string

	// Skills
	SkillsDir                 string
	SkillHTTPTimeout          float64
	SkillShellEnabled         bool
	SkillShellSandbox         string // none | bwrap
	SkillShellSandboxNetwork  bool
	SkillShellSandboxWritable string
	SkillSSHKnownHosts        string
	SkillWorkspaceDir         string  // file_* / pdf_text root
	SkillWorkspaceMaxMB       float64 // total size cap for file_write
	SkillGitHubToken          string  // optional, raises the github skill's rate limit

	// notify skill: every channel is off until configured, and recipients are
	// allowlisted — the model picks among them, never an arbitrary address.
	NotifyTelegramChats       []string // allowed chat ids
	NotifyTelegramDefaultChat string
	NotifyEmailTo             []string // allowed addresses, or "@domain"
	NotifySMTPHost            string
	NotifySMTPPort            int
	NotifySMTPUser            string
	NotifySMTPPassword        string
	NotifySMTPFrom            string
	NotifyWebhooks            []string // "name=url" entries
	SkillSSHHostKeyPolicy     string   // accept-new | strict | insecure
	SkillSSHEnabled           bool
	SkillSSHPrivKey           string // PEM or base64-encoded PEM
	SkillSSHDefaultUser       string
	SkillSSHIdentFile         string
	// SkillBlockPrivateNetworks stops outbound skills (http_request,
	// port_scan, markdown endpoints, ...) from reaching loopback, RFC1918/ULA
	// and CGNAT addresses. It is the inverse of SKILL_ALLOW_PRIVATE_NETWORKS,
	// which defaults to true because the agent is used for LAN administration;
	// the inversion keeps a zero Config permissive. Link-local and cloud
	// metadata addresses are refused regardless.
	SkillBlockPrivateNetworks bool

	// Stateful / account-backed skills
	SkillStateDB        string   // SKILL_STATE_DB: uptime_check history, todo
	SkillSSLLabsEmail   string   // SKILL_SSLLABS_EMAIL: switches ssl_labs_grade to the v4 API
	SkillCalendars      []string // SKILL_CALENDARS: "name=https://….ics,…"
	SkillIMAPHost       string
	SkillIMAPPort       int
	SkillIMAPUser       string
	SkillIMAPPassword   string
	SkillIMAPInsecure   bool
	SkillSQLDatabases   []string // SKILL_SQL_DATABASES: "name=postgres://…,name2=sqlite:/path"
	SkillExamplesSeeded bool     // SKILLS_INSTALL_EXAMPLES (default true)
	SkillPrometheus     []string // SKILL_PROMETHEUS: "name=http://prometheus:9090,…"
	SkillPrometheusTok  string   // SKILL_PROMETHEUS_TOKEN: bearer token for every server
	// SKILL_OPENAPI: "name=https://…/openapi.json,name2=specs/api.yaml"; per
	// API, SKILL_OPENAPI_<NAME>_AUTH ("Bearer x" or "Header: value") and
	// SKILL_OPENAPI_<NAME>_BASE_URL (overrides the spec's server).
	SkillOpenAPI        []string
	SkillOpenAPIAuth    map[string]string
	SkillOpenAPIBaseURL map[string]string

	// Agent loop
	ContextTail        int
	MemoryHits         int
	AgentMaxSteps      int
	AgentParallelTools bool
	AgentMaxParallel   int
	AgentToolMode      string // auto | native | envelope

	// Tool retrieval (AGENT_TOOL_TOP_K, AGENT_TOOLS_PINNED)
	AgentToolTopK    int
	AgentToolsPinned []string // nil = agent.DefaultPinnedTools

	// Approval gate (APPROVAL_SKILLS, APPROVAL_TIMEOUT_SEC, APPROVAL_UNATTENDED)
	ApprovalSkills     []string // nil = skills.DefaultApprovalSkills; ["off"] disables
	ApprovalTimeoutSec float64
	ApprovalUnattended string // deny | allow
	// ApprovalAllow pre-approves matching calls: "TOOL: ARG=REGEX [&& ...]",
	// rules separated by ";" (APPROVAL_ALLOW).
	ApprovalAllow string
	// ApprovalGrantTTLHours bounds "approve for this session" (default 12).
	ApprovalGrantTTLHours float64

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
	MCPConnectWait   float64  // seconds startup waits for MCP_SERVERS to connect
	MCPRefreshSec    float64  // re-list remote tools this often; 0 = never

	// HTTP API access control
	APIToken           string   // when set, /api/* and /mcp require "Authorization: Bearer <token>"
	CORSAllowedOrigins []string // cross-origin callers allowed to use the API; empty = same-origin only
	AllowedHosts       []string // extra dotted Host names accepted while APIToken is empty (DNS-rebinding guard)
}

func Load() Config {
	dataDir := getOr(os.Getenv("DATA_DIR"), "/app/data")
	c := Config{
		BackendPort:               atoiOr(os.Getenv("BACKEND_PORT"), 8000),
		DataDir:                   dataDir,
		Provider:                  strings.ToLower(getOr(os.Getenv("LLM_PROVIDER"), "ollama")),
		OllamaURL:                 strings.TrimRight(getOr(os.Getenv("OLLAMA_URL"), "http://127.0.0.1:11434"), "/"),
		OllamaModel:               getOr(os.Getenv("OLLAMA_MODEL"), "llama3:latest"),
		OllamaTimeoutSec:          atofOr(os.Getenv("OLLAMA_TIMEOUT"), 120),
		OpenAIAPIKey:              os.Getenv("OPENAI_API_KEY"),
		OpenAIBaseURL:             strings.TrimRight(getOr(os.Getenv("OPENAI_BASE_URL"), "https://api.openai.com/v1"), "/"),
		OpenAIChatModel:           os.Getenv("OPENAI_CHAT_MODEL"),
		OpenAITimeoutSec:          atofOr(os.Getenv("OPENAI_TIMEOUT"), 120),
		OpenAIAPIMode:             strings.ToLower(strings.TrimSpace(os.Getenv("OPENAI_API_MODE"))),
		OpenAIReasoning:           strings.ToLower(strings.TrimSpace(os.Getenv("OPENAI_REASONING_EFFORT"))),
		VectorCollection:          getOr(os.Getenv("AGENT_VECTOR_COLLECTION"), "memories"),
		DedupDistance:             atofOr(os.Getenv("AGENT_DEDUP_DISTANCE"), 0.08),
		MemoryDefaultTTLDays:      atofOr(os.Getenv("MEMORY_DEFAULT_TTL_DAYS"), 0),
		OllamaEmbedModel:          getOr(os.Getenv("OLLAMA_EMBED_MODEL"), "nomic-embed-text"),
		OpenAIEmbedModel:          getOr(os.Getenv("OPENAI_EMBED_MODEL"), "text-embedding-3-small"),
		DigitalOceanAPIKey:        os.Getenv("DIGITALOCEAN_API_KEY"),
		DigitalOceanBaseURL:       strings.TrimRight(getOr(os.Getenv("DIGITALOCEAN_BASE_URL"), "https://inference.do-ai.run/v1"), "/"),
		DigitalOceanChatModel:     getOr(os.Getenv("DIGITALOCEAN_CHAT_MODEL"), defaultDigitalOceanModel),
		DigitalOceanEmbedModel:    getOr(os.Getenv("DIGITALOCEAN_EMBED_MODEL"), "qwen3-embedding-0.6b"),
		DigitalOceanTimeoutSec:    atofOr(os.Getenv("DIGITALOCEAN_TIMEOUT"), 120),
		OpenCodeAPIKey:            firstOf(os.Getenv("OPENCODE_API_KEY"), os.Getenv("OPENCODE_GO_API_KEY"), os.Getenv("OPENCODE_ZEN_API_KEY")),
		OpenCodeChatModel:         os.Getenv("OPENCODE_CHAT_MODEL"),
		OpenCodeSessionID:         strings.TrimSpace(os.Getenv("OPENCODE_SESSION_ID")),
		OpenCodeTimeoutSec:        atofOr(os.Getenv("OPENCODE_TIMEOUT"), 120),
		EmbedProvider:             strings.ToLower(strings.TrimSpace(os.Getenv("EMBED_PROVIDER"))),
		SkillHTTPTimeout:          atofOr(os.Getenv("SKILL_HTTP_TIMEOUT"), 30),
		SkillShellEnabled:         isTruthy(os.Getenv("SKILL_SHELL_ENABLED")),
		SkillShellSandbox:         strings.ToLower(getOr(strings.TrimSpace(os.Getenv("SKILL_SHELL_SANDBOX")), "none")),
		SkillShellSandboxNetwork:  isTruthy(os.Getenv("SKILL_SHELL_SANDBOX_NETWORK")),
		SkillShellSandboxWritable: strings.TrimSpace(os.Getenv("SKILL_SHELL_SANDBOX_WRITABLE")),
		SkillSSHHostKeyPolicy:     strings.ToLower(getOr(strings.TrimSpace(os.Getenv("SKILL_SSH_HOST_KEY_POLICY")), "accept-new")),
		SkillSSHEnabled:           isTruthy(os.Getenv("SKILL_SSH_ENABLED")),
		SkillSSHPrivKey:           os.Getenv("SKILL_SSH_PRIVATE_KEY"),
		SkillSSHDefaultUser:       getOr(os.Getenv("SKILL_SSH_DEFAULT_USER"), "root"),
		SkillSSHIdentFile:         os.Getenv("SKILL_SSH_IDENTITY_FILE"),
		SkillBlockPrivateNetworks: isFalsy(os.Getenv("SKILL_ALLOW_PRIVATE_NETWORKS")),
		ContextTail:               atoiOr(os.Getenv("AGENT_CONTEXT_TAIL"), 30),
		MemoryHits:                atoiOr(os.Getenv("AGENT_MEMORY_HITS"), 5),
		AgentMaxSteps:             atoiOr(os.Getenv("AGENT_MAX_STEPS"), 25),
		AgentParallelTools:        isTruthy(os.Getenv("AGENT_PARALLEL_TOOLS")),
		AgentMaxParallel:          atoiOr(os.Getenv("AGENT_MAX_PARALLEL"), 4),
		AgentToolMode:             strings.ToLower(getOr(strings.TrimSpace(os.Getenv("AGENT_TOOL_MODE")), "auto")),
		AgentToolTopK:             atoiOr(os.Getenv("AGENT_TOOL_TOP_K"), 12),
		SkillSSLLabsEmail:         strings.TrimSpace(os.Getenv("SKILL_SSLLABS_EMAIL")),
		SkillCalendars:            splitCSV(os.Getenv("SKILL_CALENDARS")),
		SkillIMAPHost:             strings.TrimSpace(os.Getenv("SKILL_IMAP_HOST")),
		SkillIMAPPort:             atoiOr(os.Getenv("SKILL_IMAP_PORT"), 993),
		SkillIMAPUser:             os.Getenv("SKILL_IMAP_USER"),
		SkillIMAPPassword:         os.Getenv("SKILL_IMAP_PASSWORD"),
		SkillIMAPInsecure:         isTruthy(os.Getenv("SKILL_IMAP_INSECURE_SKIP_VERIFY")),
		SkillSQLDatabases:         splitCSV(os.Getenv("SKILL_SQL_DATABASES")),
		SkillExamplesSeeded:       !isFalsy(os.Getenv("SKILLS_INSTALL_EXAMPLES")),
		AgentToolsPinned:          splitCSV(os.Getenv("AGENT_TOOLS_PINNED")),
		ApprovalSkills:            splitCSV(os.Getenv("APPROVAL_SKILLS")),
		ApprovalTimeoutSec:        atofOr(os.Getenv("APPROVAL_TIMEOUT_SEC"), 300),
		ApprovalUnattended:        strings.ToLower(getOr(strings.TrimSpace(os.Getenv("APPROVAL_UNATTENDED")), "deny")),
		ApprovalAllow:             strings.TrimSpace(os.Getenv("APPROVAL_ALLOW")),
		SkillPrometheus:           splitCSV(os.Getenv("SKILL_PROMETHEUS")),
		SkillPrometheusTok:        strings.TrimSpace(os.Getenv("SKILL_PROMETHEUS_TOKEN")),
		SkillOpenAPI:              splitCSV(os.Getenv("SKILL_OPENAPI")),
		ApprovalGrantTTLHours:     atofOr(os.Getenv("APPROVAL_GRANT_TTL_HOURS"), 12),
		AutoMemoryExtract:         isTruthy(os.Getenv("AUTO_MEMORY_EXTRACT")),
		AutoMemoryMaxFacts:        atoiOr(os.Getenv("AUTO_MEMORY_MAX_FACTS"), 5),
		AutoMemoryMinAnswerLen:    atoiOr(os.Getenv("AUTO_MEMORY_MIN_ANSWER_LEN"), 80),
		PlaygroundEnabled:         !isTruthy(os.Getenv("PLAYGROUND_DISABLED")),
		SchedulerTickSec:          atoiOr(os.Getenv("SCHEDULER_TICK_SEC"), 60),
		TelegramBotToken:          os.Getenv("TELEGRAM_BOT_TOKEN"),
		AuditMaxBlobBytes:         atoiOr(os.Getenv("AUDIT_MAX_BLOB_BYTES"), 65536),
		MCPServerEnabled:          !isTruthy(os.Getenv("MCP_SERVER_DISABLED")),
		MCPServers:                splitCSV(os.Getenv("MCP_SERVERS")),
		MCPConnectWait:            atofOr(os.Getenv("MCP_CONNECT_WAIT_SEC"), 10),
		MCPRefreshSec:             atofOr(os.Getenv("MCP_REFRESH_SEC"), 300),
		APIToken:                  strings.TrimSpace(os.Getenv("GATEWAY_API_TOKEN")),
		CORSAllowedOrigins:        splitCSV(os.Getenv("CORS_ALLOWED_ORIGINS")),
		AllowedHosts:              splitCSV(os.Getenv("GATEWAY_ALLOWED_HOSTS")),
	}
	c.AgentDB = getOr(os.Getenv("AGENT_DB"), dataDir+"/agent_memory.sqlite3")
	c.VectorDir = getOr(os.Getenv("AGENT_VECTOR_DIR"), dataDir+"/chromem")
	c.SkillsDir = getOr(os.Getenv("SKILLS_DIR"), dataDir+"/skills")
	c.SchedulerDB = getOr(os.Getenv("SCHEDULER_DB"), dataDir+"/scheduler.sqlite3")
	c.SkillSSHKnownHosts = getOr(os.Getenv("SKILL_SSH_KNOWN_HOSTS"), dataDir+"/ssh_known_hosts")
	c.SkillWorkspaceDir = getOr(os.Getenv("SKILL_WORKSPACE_DIR"), dataDir+"/workspace")
	c.SkillStateDB = getOr(os.Getenv("SKILL_STATE_DB"), dataDir+"/skills_state.sqlite3")
	c.SkillWorkspaceMaxMB = atofOr(os.Getenv("SKILL_WORKSPACE_MAX_MB"), 100)
	c.SkillGitHubToken = strings.TrimSpace(firstOf(os.Getenv("SKILL_GITHUB_TOKEN"), os.Getenv("GITHUB_TOKEN")))
	c.NotifyTelegramChats = splitCSV(os.Getenv("NOTIFY_TELEGRAM_CHATS"))
	c.NotifyTelegramDefaultChat = strings.TrimSpace(os.Getenv("NOTIFY_TELEGRAM_DEFAULT_CHAT"))
	c.NotifyEmailTo = splitCSV(os.Getenv("NOTIFY_EMAIL_TO"))
	c.NotifySMTPHost = strings.TrimSpace(os.Getenv("NOTIFY_SMTP_HOST"))
	c.NotifySMTPPort = atoiOr(os.Getenv("NOTIFY_SMTP_PORT"), 587)
	c.NotifySMTPUser = os.Getenv("NOTIFY_SMTP_USER")
	c.NotifySMTPPassword = os.Getenv("NOTIFY_SMTP_PASSWORD")
	c.NotifySMTPFrom = strings.TrimSpace(os.Getenv("NOTIFY_SMTP_FROM"))
	c.NotifyWebhooks = splitCSV(os.Getenv("NOTIFY_WEBHOOKS"))
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
	c.SkillOpenAPIAuth = openAPIPerName(c.SkillOpenAPI, "AUTH")
	c.SkillOpenAPIBaseURL = openAPIPerName(c.SkillOpenAPI, "BASE_URL")
	return c
}

// splitCSV splits a comma-separated env value into trimmed, non-empty entries.
// openAPIPerName reads SKILL_OPENAPI_<NAME>_<suffix> for every API named in
// SKILL_OPENAPI ("my-api" → SKILL_OPENAPI_MY_API_AUTH).
func openAPIPerName(entries []string, suffix string) map[string]string {
	out := map[string]string{}
	for _, e := range entries {
		name, _, ok := strings.Cut(e, "=")
		name = strings.ToLower(strings.TrimSpace(name))
		if !ok || name == "" {
			continue
		}
		env := strings.Map(func(r rune) rune {
			if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
				return r
			}
			return '_'
		}, strings.ToUpper(name))
		if v := strings.TrimSpace(os.Getenv("SKILL_OPENAPI_" + env + "_" + suffix)); v != "" {
			out[name] = v
		}
	}
	return out
}

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

// isTruthy accepts the same spellings isFalsy rejects. "on" was missing, so
// PLAYGROUND_DISABLED=on / MCP_SERVER_DISABLED=on left those surfaces enabled.
func isTruthy(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v == "true" || v == "1" || v == "yes" || v == "on"
}

// isFalsy is isTruthy's counterpart for settings that default to on: only an
// explicit negative turns them off, so unset or unrecognised values keep the
// default.
func isFalsy(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v == "false" || v == "0" || v == "no" || v == "off"
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
