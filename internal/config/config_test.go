package config

import (
	"reflect"
	"testing"
)

// clearEnv neutralizes every environment variable Load reads by setting it to
// the empty string, so a test starts from Load's built-in defaults regardless
// of the ambient environment. Every accessor (getOr/atoiOr/atofOr/isTruthy and
// direct os.Getenv) treats "" identically to unset, so this reliably simulates
// an unset variable. t.Setenv restores the prior value when the (sub)test ends.
func clearEnv(t *testing.T) {
	t.Helper()
	keys := []string{
		"DATA_DIR", "BACKEND_PORT", "LLM_PROVIDER",
		"OLLAMA_URL", "OLLAMA_MODEL", "OLLAMA_TIMEOUT",
		"OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_CHAT_MODEL",
		"OPENAI_TIMEOUT", "OPENAI_API_MODE", "OPENAI_REASONING_EFFORT", "OPENAI_TEMPERATURE",
		"AGENT_VECTOR_COLLECTION", "AGENT_DEDUP_DISTANCE",
		"OLLAMA_EMBED_MODEL", "OPENAI_EMBED_MODEL",
		"DIGITALOCEAN_API_KEY", "DIGITALOCEAN_BASE_URL",
		"DIGITALOCEAN_CHAT_MODEL", "DIGITALOCEAN_EMBED_MODEL",
		"DIGITALOCEAN_TIMEOUT",
		"SKILL_HTTP_TIMEOUT", "SKILL_SHELL_ENABLED", "SKILL_SSH_ENABLED",
		"SKILL_SSH_PRIVATE_KEY", "SKILL_SSH_DEFAULT_USER", "SKILL_SSH_IDENTITY_FILE",
		"AGENT_CONTEXT_TAIL", "AGENT_MEMORY_HITS", "AGENT_MAX_STEPS",
		"AGENT_PARALLEL_TOOLS", "AGENT_MAX_PARALLEL",
		"AUTO_MEMORY_EXTRACT", "AUTO_MEMORY_MAX_FACTS", "AUTO_MEMORY_MIN_ANSWER_LEN",
		"PLAYGROUND_DISABLED", "SCHEDULER_TICK_SEC", "TELEGRAM_BOT_TOKEN",
		"AUDIT_MAX_BLOB_BYTES", "MCP_SERVER_DISABLED", "MCP_SERVERS",
		"AGENT_DB", "AGENT_VECTOR_DIR", "SKILLS_DIR", "SCHEDULER_DB",
	}
	for _, k := range keys {
		t.Setenv(k, "")
	}
}

func TestOpenAITemperature(t *testing.T) {
	cases := []struct {
		name    string
		val     string
		wantNil bool
		want    float64
	}{
		{"empty defaults to 0.2", "", false, 0.2},
		{"explicit value", "0.7", false, 0.7},
		{"zero is honored", "0", false, 0},
		{"none omits", "none", true, 0},
		{"omit omits", "omit", true, 0},
		{"off omits (case-insensitive)", "OFF", true, 0},
		{"unparseable falls back to 0.2", "hot", false, 0.2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("OPENAI_TEMPERATURE", tc.val)
			c := Load()
			if tc.wantNil {
				if c.OpenAITemperature != nil {
					t.Fatalf("OpenAITemperature = %v, want nil (omitted)", *c.OpenAITemperature)
				}
				return
			}
			if c.OpenAITemperature == nil {
				t.Fatalf("OpenAITemperature = nil, want %v", tc.want)
			}
			if *c.OpenAITemperature != tc.want {
				t.Errorf("OpenAITemperature = %v, want %v", *c.OpenAITemperature, tc.want)
			}
		})
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)
	c := Load()

	if c.BackendPort != 8000 {
		t.Errorf("BackendPort = %d, want 8000", c.BackendPort)
	}
	if c.DataDir != "/app/data" {
		t.Errorf("DataDir = %q, want %q", c.DataDir, "/app/data")
	}
	if c.Provider != "ollama" {
		t.Errorf("Provider = %q, want %q", c.Provider, "ollama")
	}
	if c.OllamaURL != "http://127.0.0.1:11434" {
		t.Errorf("OllamaURL = %q, want %q", c.OllamaURL, "http://127.0.0.1:11434")
	}
	if c.OllamaModel != "llama3:latest" {
		t.Errorf("OllamaModel = %q, want %q", c.OllamaModel, "llama3:latest")
	}
	if c.OllamaTimeoutSec != 120 {
		t.Errorf("OllamaTimeoutSec = %v, want 120", c.OllamaTimeoutSec)
	}
	if c.OpenAIBaseURL != "https://api.openai.com/v1" {
		t.Errorf("OpenAIBaseURL = %q, want %q", c.OpenAIBaseURL, "https://api.openai.com/v1")
	}
	if c.OpenAITimeoutSec != 120 {
		t.Errorf("OpenAITimeoutSec = %v, want 120", c.OpenAITimeoutSec)
	}
	if c.VectorCollection != "memories" {
		t.Errorf("VectorCollection = %q, want %q", c.VectorCollection, "memories")
	}
	if c.DedupDistance != 0.08 {
		t.Errorf("DedupDistance = %v, want 0.08", c.DedupDistance)
	}
	if c.OllamaEmbedModel != "nomic-embed-text" {
		t.Errorf("OllamaEmbedModel = %q, want %q", c.OllamaEmbedModel, "nomic-embed-text")
	}
	if c.OpenAIEmbedModel != "text-embedding-3-small" {
		t.Errorf("OpenAIEmbedModel = %q, want %q", c.OpenAIEmbedModel, "text-embedding-3-small")
	}
	if c.DigitalOceanBaseURL != "https://inference.do-ai.run/v1" {
		t.Errorf("DigitalOceanBaseURL = %q, want %q", c.DigitalOceanBaseURL, "https://inference.do-ai.run/v1")
	}
	if c.DigitalOceanChatModel != "meta-llama/Llama-3.3-70B-Instruct" {
		t.Errorf("DigitalOceanChatModel = %q, want %q", c.DigitalOceanChatModel, "meta-llama/Llama-3.3-70B-Instruct")
	}
	if c.DigitalOceanEmbedModel != "qwen3-embedding-0.6b" {
		t.Errorf("DigitalOceanEmbedModel = %q, want %q", c.DigitalOceanEmbedModel, "qwen3-embedding-0.6b")
	}
	if c.DigitalOceanTimeoutSec != 120 {
		t.Errorf("DigitalOceanTimeoutSec = %v, want 120", c.DigitalOceanTimeoutSec)
	}
	if c.SkillHTTPTimeout != 30 {
		t.Errorf("SkillHTTPTimeout = %v, want 30", c.SkillHTTPTimeout)
	}
	if c.SkillSSHDefaultUser != "root" {
		t.Errorf("SkillSSHDefaultUser = %q, want %q", c.SkillSSHDefaultUser, "root")
	}
	if c.ContextTail != 30 {
		t.Errorf("ContextTail = %d, want 30", c.ContextTail)
	}
	if c.MemoryHits != 5 {
		t.Errorf("MemoryHits = %d, want 5", c.MemoryHits)
	}
	if c.AgentMaxSteps != 25 {
		t.Errorf("AgentMaxSteps = %d, want 25", c.AgentMaxSteps)
	}
	if c.AgentMaxParallel != 4 {
		t.Errorf("AgentMaxParallel = %d, want 4", c.AgentMaxParallel)
	}
	if c.AutoMemoryMaxFacts != 5 {
		t.Errorf("AutoMemoryMaxFacts = %d, want 5", c.AutoMemoryMaxFacts)
	}
	if c.AutoMemoryMinAnswerLen != 80 {
		t.Errorf("AutoMemoryMinAnswerLen = %d, want 80", c.AutoMemoryMinAnswerLen)
	}
	if c.SchedulerTickSec != 60 {
		t.Errorf("SchedulerTickSec = %d, want 60", c.SchedulerTickSec)
	}
	if c.AuditMaxBlobBytes != 65536 {
		t.Errorf("AuditMaxBlobBytes = %d, want 65536", c.AuditMaxBlobBytes)
	}

	// Boolean defaults: opt-in features off, inverted toggles on.
	if c.SkillShellEnabled {
		t.Error("SkillShellEnabled = true, want false")
	}
	if c.SkillSSHEnabled {
		t.Error("SkillSSHEnabled = true, want false")
	}
	if c.AgentParallelTools {
		t.Error("AgentParallelTools = true, want false")
	}
	if c.AutoMemoryExtract {
		t.Error("AutoMemoryExtract = true, want false")
	}
	if !c.PlaygroundEnabled {
		t.Error("PlaygroundEnabled = false, want true")
	}
	if !c.MCPServerEnabled {
		t.Error("MCPServerEnabled = false, want true")
	}

	// Strings that are empty unless explicitly provided.
	if c.OpenAIAPIKey != "" {
		t.Errorf("OpenAIAPIKey = %q, want empty", c.OpenAIAPIKey)
	}
	if c.DigitalOceanAPIKey != "" {
		t.Errorf("DigitalOceanAPIKey = %q, want empty", c.DigitalOceanAPIKey)
	}
	if c.TelegramBotToken != "" {
		t.Errorf("TelegramBotToken = %q, want empty", c.TelegramBotToken)
	}
	if c.MCPServers != nil {
		t.Errorf("MCPServers = %#v, want nil", c.MCPServers)
	}

	// Paths derived off the default data dir.
	if c.AgentDB != "/app/data/agent_memory.sqlite3" {
		t.Errorf("AgentDB = %q, want %q", c.AgentDB, "/app/data/agent_memory.sqlite3")
	}
	if c.VectorDir != "/app/data/chromem" {
		t.Errorf("VectorDir = %q, want %q", c.VectorDir, "/app/data/chromem")
	}
	if c.SkillsDir != "/app/data/skills" {
		t.Errorf("SkillsDir = %q, want %q", c.SkillsDir, "/app/data/skills")
	}
	if c.SchedulerDB != "/app/data/scheduler.sqlite3" {
		t.Errorf("SchedulerDB = %q, want %q", c.SchedulerDB, "/app/data/scheduler.sqlite3")
	}

	// Provider is ollama, so no OpenAI chat-model default is applied, but the
	// api-mode still resolves (empty model does not contain "codex").
	if c.OpenAIChatModel != "" {
		t.Errorf("OpenAIChatModel = %q, want empty for ollama provider", c.OpenAIChatModel)
	}
	if c.OpenAIAPIMode != "chat_completions" {
		t.Errorf("OpenAIAPIMode = %q, want %q", c.OpenAIAPIMode, "chat_completions")
	}
}

func TestLoadOverrides(t *testing.T) {
	clearEnv(t)
	env := map[string]string{
		"BACKEND_PORT":               "9090",
		"DATA_DIR":                   "/data2",
		"LLM_PROVIDER":               "DigitalOcean",
		"OLLAMA_URL":                 "http://ol:1/",
		"OLLAMA_MODEL":               "mymodel",
		"OLLAMA_TIMEOUT":             "45.5",
		"OPENAI_API_KEY":             "sk-test",
		"OPENAI_BASE_URL":            "https://proxy/v1/",
		"OPENAI_CHAT_MODEL":          "gpt-4o",
		"OPENAI_TIMEOUT":             "99",
		"OPENAI_API_MODE":            "Chat_Completions",
		"OPENAI_REASONING_EFFORT":    "High",
		"AGENT_VECTOR_COLLECTION":    "mems2",
		"AGENT_DEDUP_DISTANCE":       "0.5",
		"OLLAMA_EMBED_MODEL":         "embed-a",
		"OPENAI_EMBED_MODEL":         "embed-b",
		"DIGITALOCEAN_API_KEY":       "do-key",
		"DIGITALOCEAN_BASE_URL":      "https://do/v1/",
		"DIGITALOCEAN_CHAT_MODEL":    "do-chat",
		"DIGITALOCEAN_EMBED_MODEL":   "do-embed",
		"DIGITALOCEAN_TIMEOUT":       "15",
		"SKILL_HTTP_TIMEOUT":         "10",
		"SKILL_SHELL_ENABLED":        "true",
		"SKILL_SSH_ENABLED":          "yes",
		"SKILL_SSH_PRIVATE_KEY":      "pem-data",
		"SKILL_SSH_DEFAULT_USER":     "admin",
		"SKILL_SSH_IDENTITY_FILE":    "/id",
		"AGENT_CONTEXT_TAIL":         "10",
		"AGENT_MEMORY_HITS":          "7",
		"AGENT_MAX_STEPS":            "50",
		"AGENT_PARALLEL_TOOLS":       "1",
		"AGENT_MAX_PARALLEL":         "8",
		"AUTO_MEMORY_EXTRACT":        "true",
		"AUTO_MEMORY_MAX_FACTS":      "3",
		"AUTO_MEMORY_MIN_ANSWER_LEN": "200",
		"SCHEDULER_TICK_SEC":         "30",
		"TELEGRAM_BOT_TOKEN":         "tok",
		"AUDIT_MAX_BLOB_BYTES":       "1024",
		"MCP_SERVERS":                "a=http://a, b=http://b",
		"AGENT_DB":                   "/x/db.sqlite",
		"AGENT_VECTOR_DIR":           "/x/vec",
		"SKILLS_DIR":                 "/x/skills",
		"SCHEDULER_DB":               "/x/sched.db",
	}
	for k, v := range env {
		t.Setenv(k, v)
	}

	c := Load()

	if c.BackendPort != 9090 {
		t.Errorf("BackendPort = %d, want 9090", c.BackendPort)
	}
	if c.DataDir != "/data2" {
		t.Errorf("DataDir = %q, want %q", c.DataDir, "/data2")
	}
	if c.Provider != "digitalocean" {
		t.Errorf("Provider = %q, want %q (lowercased)", c.Provider, "digitalocean")
	}
	if c.OllamaURL != "http://ol:1" {
		t.Errorf("OllamaURL = %q, want %q (trailing slash trimmed)", c.OllamaURL, "http://ol:1")
	}
	if c.OllamaModel != "mymodel" {
		t.Errorf("OllamaModel = %q, want %q", c.OllamaModel, "mymodel")
	}
	if c.OllamaTimeoutSec != 45.5 {
		t.Errorf("OllamaTimeoutSec = %v, want 45.5", c.OllamaTimeoutSec)
	}
	if c.OpenAIAPIKey != "sk-test" {
		t.Errorf("OpenAIAPIKey = %q, want %q", c.OpenAIAPIKey, "sk-test")
	}
	if c.OpenAIBaseURL != "https://proxy/v1" {
		t.Errorf("OpenAIBaseURL = %q, want %q (trailing slash trimmed)", c.OpenAIBaseURL, "https://proxy/v1")
	}
	if c.OpenAIChatModel != "gpt-4o" {
		t.Errorf("OpenAIChatModel = %q, want %q", c.OpenAIChatModel, "gpt-4o")
	}
	if c.OpenAITimeoutSec != 99 {
		t.Errorf("OpenAITimeoutSec = %v, want 99", c.OpenAITimeoutSec)
	}
	if c.OpenAIAPIMode != "chat_completions" {
		t.Errorf("OpenAIAPIMode = %q, want %q (lowercased+trimmed)", c.OpenAIAPIMode, "chat_completions")
	}
	if c.OpenAIReasoning != "high" {
		t.Errorf("OpenAIReasoning = %q, want %q (lowercased)", c.OpenAIReasoning, "high")
	}
	if c.VectorCollection != "mems2" {
		t.Errorf("VectorCollection = %q, want %q", c.VectorCollection, "mems2")
	}
	if c.DedupDistance != 0.5 {
		t.Errorf("DedupDistance = %v, want 0.5", c.DedupDistance)
	}
	if c.OllamaEmbedModel != "embed-a" {
		t.Errorf("OllamaEmbedModel = %q, want %q", c.OllamaEmbedModel, "embed-a")
	}
	if c.OpenAIEmbedModel != "embed-b" {
		t.Errorf("OpenAIEmbedModel = %q, want %q", c.OpenAIEmbedModel, "embed-b")
	}
	if c.DigitalOceanAPIKey != "do-key" {
		t.Errorf("DigitalOceanAPIKey = %q, want %q", c.DigitalOceanAPIKey, "do-key")
	}
	if c.DigitalOceanBaseURL != "https://do/v1" {
		t.Errorf("DigitalOceanBaseURL = %q, want %q (trailing slash trimmed)", c.DigitalOceanBaseURL, "https://do/v1")
	}
	if c.DigitalOceanChatModel != "do-chat" {
		t.Errorf("DigitalOceanChatModel = %q, want %q", c.DigitalOceanChatModel, "do-chat")
	}
	if c.DigitalOceanEmbedModel != "do-embed" {
		t.Errorf("DigitalOceanEmbedModel = %q, want %q", c.DigitalOceanEmbedModel, "do-embed")
	}
	if c.DigitalOceanTimeoutSec != 15 {
		t.Errorf("DigitalOceanTimeoutSec = %v, want 15", c.DigitalOceanTimeoutSec)
	}
	if c.SkillHTTPTimeout != 10 {
		t.Errorf("SkillHTTPTimeout = %v, want 10", c.SkillHTTPTimeout)
	}
	if !c.SkillShellEnabled {
		t.Error("SkillShellEnabled = false, want true")
	}
	if !c.SkillSSHEnabled {
		t.Error("SkillSSHEnabled = false, want true (yes is truthy)")
	}
	if c.SkillSSHPrivKey != "pem-data" {
		t.Errorf("SkillSSHPrivKey = %q, want %q", c.SkillSSHPrivKey, "pem-data")
	}
	if c.SkillSSHDefaultUser != "admin" {
		t.Errorf("SkillSSHDefaultUser = %q, want %q", c.SkillSSHDefaultUser, "admin")
	}
	if c.SkillSSHIdentFile != "/id" {
		t.Errorf("SkillSSHIdentFile = %q, want %q", c.SkillSSHIdentFile, "/id")
	}
	if c.ContextTail != 10 {
		t.Errorf("ContextTail = %d, want 10", c.ContextTail)
	}
	if c.MemoryHits != 7 {
		t.Errorf("MemoryHits = %d, want 7", c.MemoryHits)
	}
	if c.AgentMaxSteps != 50 {
		t.Errorf("AgentMaxSteps = %d, want 50", c.AgentMaxSteps)
	}
	if !c.AgentParallelTools {
		t.Error("AgentParallelTools = false, want true (1 is truthy)")
	}
	if c.AgentMaxParallel != 8 {
		t.Errorf("AgentMaxParallel = %d, want 8", c.AgentMaxParallel)
	}
	if !c.AutoMemoryExtract {
		t.Error("AutoMemoryExtract = false, want true")
	}
	if c.AutoMemoryMaxFacts != 3 {
		t.Errorf("AutoMemoryMaxFacts = %d, want 3", c.AutoMemoryMaxFacts)
	}
	if c.AutoMemoryMinAnswerLen != 200 {
		t.Errorf("AutoMemoryMinAnswerLen = %d, want 200", c.AutoMemoryMinAnswerLen)
	}
	if c.SchedulerTickSec != 30 {
		t.Errorf("SchedulerTickSec = %d, want 30", c.SchedulerTickSec)
	}
	if c.TelegramBotToken != "tok" {
		t.Errorf("TelegramBotToken = %q, want %q", c.TelegramBotToken, "tok")
	}
	if c.AuditMaxBlobBytes != 1024 {
		t.Errorf("AuditMaxBlobBytes = %d, want 1024", c.AuditMaxBlobBytes)
	}
	wantServers := []string{"a=http://a", "b=http://b"}
	if !reflect.DeepEqual(c.MCPServers, wantServers) {
		t.Errorf("MCPServers = %#v, want %#v", c.MCPServers, wantServers)
	}
	if c.AgentDB != "/x/db.sqlite" {
		t.Errorf("AgentDB = %q, want %q (explicit override)", c.AgentDB, "/x/db.sqlite")
	}
	if c.VectorDir != "/x/vec" {
		t.Errorf("VectorDir = %q, want %q (explicit override)", c.VectorDir, "/x/vec")
	}
	if c.SkillsDir != "/x/skills" {
		t.Errorf("SkillsDir = %q, want %q (explicit override)", c.SkillsDir, "/x/skills")
	}
	if c.SchedulerDB != "/x/sched.db" {
		t.Errorf("SchedulerDB = %q, want %q (explicit override)", c.SchedulerDB, "/x/sched.db")
	}

	// PLAYGROUND_DISABLED / MCP_SERVER_DISABLED left cleared, so both stay on.
	if !c.PlaygroundEnabled {
		t.Error("PlaygroundEnabled = false, want true")
	}
	if !c.MCPServerEnabled {
		t.Error("MCPServerEnabled = false, want true")
	}
}

func TestProviderModelAPIModeResolution(t *testing.T) {
	tests := []struct {
		name      string
		provider  string
		chatModel string
		apiMode   string
		wantModel string
		wantMode  string
	}{
		{
			name:      "openai empty model defaults to gpt-4o-mini and chat mode",
			provider:  "openai",
			wantModel: "gpt-4o-mini",
			wantMode:  "chat_completions",
		},
		{
			name:      "openai-codex empty model defaults to gpt-5.3-codex and responses mode",
			provider:  "openai-codex",
			wantModel: "gpt-5.3-codex",
			wantMode:  "responses",
		},
		{
			name:      "codex alias defaults to gpt-5.3-codex and responses mode",
			provider:  "codex",
			wantModel: "gpt-5.3-codex",
			wantMode:  "responses",
		},
		{
			name:      "openai with explicit non-codex model keeps chat mode",
			provider:  "openai",
			chatModel: "gpt-4o",
			wantModel: "gpt-4o",
			wantMode:  "chat_completions",
		},
		{
			name:      "openai with explicit codex-containing model resolves to responses",
			provider:  "openai",
			chatModel: "my-codex-preview",
			wantModel: "my-codex-preview",
			wantMode:  "responses",
		},
		{
			name:      "explicit api mode overrides codex-model default",
			provider:  "openai-codex",
			apiMode:   "chat_completions",
			wantModel: "gpt-5.3-codex",
			wantMode:  "chat_completions",
		},
		{
			name:      "explicit responses mode respected for non-codex model",
			provider:  "openai",
			chatModel: "gpt-4o",
			apiMode:   "responses",
			wantModel: "gpt-4o",
			wantMode:  "responses",
		},
		{
			name:      "api mode is lowercased and trimmed",
			provider:  "openai",
			chatModel: "gpt-4o",
			apiMode:   "  RESPONSES  ",
			wantModel: "gpt-4o",
			wantMode:  "responses",
		},
		{
			name:      "ollama leaves openai model empty and defaults to chat mode",
			provider:  "ollama",
			wantModel: "",
			wantMode:  "chat_completions",
		},
		{
			name:      "provider is case-insensitive",
			provider:  "OpenAI-Codex",
			wantModel: "gpt-5.3-codex",
			wantMode:  "responses",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("LLM_PROVIDER", tt.provider)
			t.Setenv("OPENAI_CHAT_MODEL", tt.chatModel)
			t.Setenv("OPENAI_API_MODE", tt.apiMode)
			c := Load()
			if c.OpenAIChatModel != tt.wantModel {
				t.Errorf("OpenAIChatModel = %q, want %q", c.OpenAIChatModel, tt.wantModel)
			}
			if c.OpenAIAPIMode != tt.wantMode {
				t.Errorf("OpenAIAPIMode = %q, want %q", c.OpenAIAPIMode, tt.wantMode)
			}
		})
	}
}

func TestPathDerivation(t *testing.T) {
	tests := []struct {
		name          string
		dataDir       string
		agentDB       string
		vectorDir     string
		skillsDir     string
		schedulerDB   string
		wantDataDir   string
		wantAgentDB   string
		wantVectorDir string
		wantSkillsDir string
		wantSchedDB   string
	}{
		{
			name:          "default data dir drives all paths",
			wantDataDir:   "/app/data",
			wantAgentDB:   "/app/data/agent_memory.sqlite3",
			wantVectorDir: "/app/data/chromem",
			wantSkillsDir: "/app/data/skills",
			wantSchedDB:   "/app/data/scheduler.sqlite3",
		},
		{
			name:          "custom data dir flows through to derived paths",
			dataDir:       "/srv/omega",
			wantDataDir:   "/srv/omega",
			wantAgentDB:   "/srv/omega/agent_memory.sqlite3",
			wantVectorDir: "/srv/omega/chromem",
			wantSkillsDir: "/srv/omega/skills",
			wantSchedDB:   "/srv/omega/scheduler.sqlite3",
		},
		{
			name:          "explicit paths override data-dir derivation",
			dataDir:       "/srv/omega",
			agentDB:       "/db/agent.sqlite",
			vectorDir:     "/vec/store",
			skillsDir:     "/opt/skills",
			schedulerDB:   "/db/sched.sqlite",
			wantDataDir:   "/srv/omega",
			wantAgentDB:   "/db/agent.sqlite",
			wantVectorDir: "/vec/store",
			wantSkillsDir: "/opt/skills",
			wantSchedDB:   "/db/sched.sqlite",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("DATA_DIR", tt.dataDir)
			t.Setenv("AGENT_DB", tt.agentDB)
			t.Setenv("AGENT_VECTOR_DIR", tt.vectorDir)
			t.Setenv("SKILLS_DIR", tt.skillsDir)
			t.Setenv("SCHEDULER_DB", tt.schedulerDB)
			c := Load()
			if c.DataDir != tt.wantDataDir {
				t.Errorf("DataDir = %q, want %q", c.DataDir, tt.wantDataDir)
			}
			if c.AgentDB != tt.wantAgentDB {
				t.Errorf("AgentDB = %q, want %q", c.AgentDB, tt.wantAgentDB)
			}
			if c.VectorDir != tt.wantVectorDir {
				t.Errorf("VectorDir = %q, want %q", c.VectorDir, tt.wantVectorDir)
			}
			if c.SkillsDir != tt.wantSkillsDir {
				t.Errorf("SkillsDir = %q, want %q", c.SkillsDir, tt.wantSkillsDir)
			}
			if c.SchedulerDB != tt.wantSchedDB {
				t.Errorf("SchedulerDB = %q, want %q", c.SchedulerDB, tt.wantSchedDB)
			}
		})
	}
}

func TestInvertedToggles(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		enabled bool
	}{
		{name: "unset means enabled", value: "", enabled: true},
		{name: "true disables", value: "true", enabled: false},
		{name: "one disables", value: "1", enabled: false},
		{name: "yes disables", value: "yes", enabled: false},
		{name: "mixed-case true disables", value: "TRUE", enabled: false},
		{name: "false leaves enabled", value: "false", enabled: true},
		{name: "zero leaves enabled", value: "0", enabled: true},
		{name: "non-truthy string leaves enabled", value: "maybe", enabled: true},
	}
	for _, tt := range tests {
		t.Run("playground/"+tt.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("PLAYGROUND_DISABLED", tt.value)
			c := Load()
			if c.PlaygroundEnabled != tt.enabled {
				t.Errorf("PlaygroundEnabled = %v, want %v (PLAYGROUND_DISABLED=%q)", c.PlaygroundEnabled, tt.enabled, tt.value)
			}
		})
		t.Run("mcp/"+tt.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("MCP_SERVER_DISABLED", tt.value)
			c := Load()
			if c.MCPServerEnabled != tt.enabled {
				t.Errorf("MCPServerEnabled = %v, want %v (MCP_SERVER_DISABLED=%q)", c.MCPServerEnabled, tt.enabled, tt.value)
			}
		})
	}
}

func TestIsTruthy(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"true", true},
		{"TRUE", true},
		{"True", true},
		{"1", true},
		{"yes", true},
		{"YES", true},
		{"  yes  ", true},
		{" TrUe ", true},
		{"false", false},
		{"0", false},
		{"no", false},
		{"", false},
		{"y", false},
		{"on", false},
		{"2", false},
		{"truthy", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := isTruthy(tt.in); got != tt.want {
				t.Errorf("isTruthy(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestSplitCSV(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "empty is nil", in: "", want: nil},
		{name: "whitespace only is nil", in: "   ", want: nil},
		{name: "single value", in: "a", want: []string{"a"}},
		{name: "multiple values", in: "a,b,c", want: []string{"a", "b", "c"}},
		{name: "values are trimmed", in: " a , b ,c ", want: []string{"a", "b", "c"}},
		{name: "empty entries dropped", in: "a,,b", want: []string{"a", "b"}},
		{name: "leading and trailing separators dropped", in: ",a,", want: []string{"a"}},
		{name: "only separator yields empty non-nil slice", in: ",", want: []string{}},
		{name: "separators and spaces yield empty non-nil slice", in: " , , ", want: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitCSV(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("splitCSV(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

func TestAtoiOr(t *testing.T) {
	tests := []struct {
		name string
		in   string
		def  int
		want int
	}{
		{name: "empty uses default", in: "", def: 8000, want: 8000},
		{name: "valid positive", in: "42", def: 8000, want: 42},
		{name: "zero", in: "0", def: 8000, want: 0},
		{name: "negative", in: "-5", def: 8000, want: -5},
		{name: "non-numeric uses default", in: "abc", def: 8000, want: 8000},
		{name: "float string uses default", in: "3.14", def: 8000, want: 8000},
		{name: "surrounding spaces use default", in: " 5 ", def: 8000, want: 8000},
		{name: "trailing junk uses default", in: "12abc", def: 8000, want: 8000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := atoiOr(tt.in, tt.def); got != tt.want {
				t.Errorf("atoiOr(%q, %d) = %d, want %d", tt.in, tt.def, got, tt.want)
			}
		})
	}
}

func TestAtofOr(t *testing.T) {
	tests := []struct {
		name string
		in   string
		def  float64
		want float64
	}{
		{name: "empty uses default", in: "", def: 0.08, want: 0.08},
		{name: "valid decimal", in: "0.5", def: 0.08, want: 0.5},
		{name: "integer string", in: "1", def: 0.08, want: 1},
		{name: "negative", in: "-2.5", def: 0.08, want: -2.5},
		{name: "scientific notation", in: "1e3", def: 0.08, want: 1000},
		{name: "non-numeric uses default", in: "abc", def: 0.08, want: 0.08},
		{name: "trailing junk uses default", in: "1.5x", def: 0.08, want: 0.08},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := atofOr(tt.in, tt.def); got != tt.want {
				t.Errorf("atofOr(%q, %v) = %v, want %v", tt.in, tt.def, got, tt.want)
			}
		})
	}
}
