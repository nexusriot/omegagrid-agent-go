# OmegaGrid Agent Go — Design Document

## 1. Overview

OmegaGrid Agent Go is a pure Go rewrite of the omegagrid-agent platform.
The gateway, agent loop, scheduler, Telegram bot, all 51 built-in skills, vector memory,
and conversation history are compiled into static Go binaries — no Python sidecar.
A React web UI is served by a dedicated **frontend** nginx service.
All source is self-contained in this repository, making
`docker compose up --build` the single command needed to run the full stack.

```
 Browser
    │ HTTP :80
    ▼
┌─────────────────────┐       HTTP        ┌────────────────────────────────────┐
│  frontend           │  ───────────────► │   Go gateway  :8000                │
│  nginx:1.27-alpine  │  proxies /api/*   │                                    │
│  serves /ui/*       │  and /health      │  agent loop · scheduler · skills   │
└─────────────────────┘                   │  vector memory · history           │
                                          └──────────────┬─────────────────────┘
┌─────────────────────┐       HTTP                       │ HTTP
│  telegram-bot       │  ───────────────►                ▼
│  (Go binary)        │  /api/query[/str]    Ollama / OpenAI / OpenAI Codex / DigitalOcean / opencode
└─────────────────────┘
```

### Design principles

| Principle | How it manifests |
|---|---|
| **Single binary per role** | `cmd/gateway` and `cmd/telegram-bot` compile to static, CGO-free executables. |
| **No runtime dependencies** | All skills, memory, and history run in-process — no sidecar, no separate Python process. |
| **Pure Go, no CGO** | `modernc.org/sqlite` (history, scheduler), `chromem-go` (vector memory), and all skills are pure Go packages. |
| **Self-contained repo** | No sibling project required.  `docker compose up --build` from any checkout. |
| **Compatible agent contract** | System prompt, JSON envelope and tool-calling protocol derive from the original `core/agent.py`; the Go loop has since added parallel `tool_calls` batches and more lenient JSON recovery. |
| **Frontend as a peer service** | The web UI is an independent nginx container; it never shares a process or filesystem with the gateway. |
| **Hot-extensible skills** | `skill_creator` writes `.md` files and hot-registers them into the in-process registry at runtime — no restart needed. |

---

## 2. Package layout

```
omegagrid-agent-go/
├── cmd/
│   ├── gateway/main.go          # HTTP gateway entry point
│   ├── cli/                     # `omega` CLI binary (local + remote modes)
│   │   ├── main.go              #   Subcommand dispatch
│   │   ├── client.go            #   Local-mode bootstrap + remote HTTP client
│   │   ├── ask.go               #   omega ask  — agent queries with streaming
│   │   ├── skills.go            #   omega skills list/describe/run
│   │   ├── memory.go            #   omega memory search/add/list/delete
│   │   ├── schedule.go          #   omega schedule list/create/delete
│   │   └── session.go           #   omega session list/export
│   ├── telegram-bot/main.go     # Telegram bot entry point
│   └── migrate-vector/          # One-shot ChromaDB → chromem-go migration
│       ├── main.go              #   Go importer (reads JSONL → writes chromem-go)
│       └── export.py            #   Python exporter (reads ChromaDB → writes JSONL)
├── internal/
│   ├── agent/
│   │   ├── agent.go             #   Tool-calling loop (Run / RunStream); tool_calls batches + audit
│   │   ├── native.go            #   Tool modes: native tool calling ↔ envelope mapping, auto fallback
│   │   └── extract.go           #   Opt-in auto-memory extraction
│   ├── bootstrap/
│   │   ├── bootstrap.go         #   Single New(cfg) wires every service (gateway + cli)
│   │   └── mcpclients.go        #   Remote MCP connection manager (parallel, retry, refresh)
│   ├── config/config.go         # Env-driven configuration
│   ├── httpapi/                 # chi router + REST handlers
│   │   ├── server.go            #   router, CORS, middleware
│   │   ├── chat.go              #   POST /api/query, /api/query/stream
│   │   ├── health.go            #   GET /health
│   │   ├── history.go           #   sessions list/search/rename/delete, messages, usage stats
│   │   ├── memory.go            #   memory add/search/list/get/update/delete, namespaces, export/import
│   │   ├── scheduler.go         #   scheduler task CRUD, run-now, run history
│   │   ├── skills.go            #   skills list, playground invoke, markdown source get/validate/save/delete
│   │   └── audit.go             #   GET /api/invocations, /invocations/{id}, /invocations/{id}/replay
│   ├── mcp/                     # Model Context Protocol (hand-rolled JSON-RPC 2.0, no SDK)
│   │   ├── mcp.go               #   Shared envelope, Tool/Param types, ToolProvider interface
│   │   ├── server.go            #   MCP server at POST /mcp — exposes skills as MCP tools
│   │   └── client.go            #   MCP client — consumes remote servers, proxies tools/call
│   ├── llm/
│   │   ├── llm.go               #   ChatClient interface + Message type
│   │   ├── tools.go             #   Native tool calling (ToolChatClient) for Ollama + OpenAI
│   │   ├── ollama.go            #   Ollama /api/chat client
│   │   ├── openai.go            #   OpenAI chat_completions + responses client
│   │   └── opencode.go          #   opencode Zen relay — OpenAIChat + session header
│   ├── memory/
│   │   ├── client.go            #   Public API — CreateSession / AddMemory / SearchMemory / ListMemories / GetMemory / DeleteMemory / AddInvocation / …
│   │   ├── history.go           #   SQLite sessions + messages + usage aggregation (modernc.org/sqlite, no CGO)
│   │   ├── audit.go             #   SQLite skill_invocations — AuditRecord / AuditFilter / record / list
│   │   ├── vector.go            #   chromem-go store: dedup, namespaces, expiry, edit, export/import, index rebuild
│   │   └── embeddings.go        #   Ollama (3-endpoint fallback) + OpenAI embeddings clients
│   ├── observability/timing.go  # Mark-based timer (surfaces in RunResult.Meta)
│   ├── version/version.go       # Release version (single source; -ldflags -X can override)
│   ├── scheduler/
│   │   ├── cron.go              #   5-field cron matcher
│   │   ├── runner.go            #   Ticker: due tasks + retries, prompt tasks, run-now
│   │   ├── skill.go             #   Native schedule_task skill
│   │   └── store.go             #   SQLite tasks + task_runs history, retries, ValidateSpec
│   ├── search/skill.go          # Native web_search skill (DuckDuckGo HTML scrape)
│   ├── skills/
│   │   ├── client.go            #   Public API — List() / Execute() / Register(); wires registry
│   │   ├── registry.go          #   Thread-safe sync.RWMutex skill map (context-aware executors, pipeline budget)
│   │   ├── source.go            #   Markdown skill source: read / validate / save / delete
│   │   ├── builtin/             #   Go implementations of 50 of the 51 built-in skills
│   │   │   ├── helpers.go       #     Local types (Skill/Param/Executor) + arg helpers
│   │   │   ├── web.go           #     weather, http_request, web_scrape, http_health, ip_info
│   │   │   ├── network.go       #     dns_lookup, ping_check, port_scan, whois_lookup
│   │   │   ├── recon.go         #     tls_probe, http_headers, banner_grab, ptr_lookup, email_auth
│   │   │   ├── encode.go        #     base64_skill, hash_skill, uuid_gen, password_gen, cidr_calc, jwt_inspect
│   │   │   ├── eval.go          #     datetime_skill, math_eval (safe AST parser), cron_schedule
│   │   │   ├── reminder.go      #     reminder (echo message for one-shot scheduled tasks)
│   │   │   ├── exec.go          #     shell_command, ssh_command
│   │   │   ├── exec_unix.go     #     Process-group kill on cancel, SO_BROADCAST (+ exec_other.go stub)
│   │   │   ├── qr.go            #     qr_generate
│   │   │   ├── rss.go           #     rss_fetch
│   │   │   ├── notify.go        #     notify (telegram / email / webhook, allowlisted)
│   │   │   ├── network2.go      #     http_timing, dns_compare, rdap_lookup, smtp_check, wake_on_lan, subnet_sweep
│   │   │   ├── traceroute*.go   #     traceroute (Linux error-queue implementation + stub)
│   │   │   ├── jsonquery.go     #     json_query
│   │   │   ├── textutil.go      #     time_convert, regex_test, text_diff, unit_convert
│   │   │   ├── csvquery.go      #     csv_query
│   │   │   ├── markdown.go      #     html_to_markdown
│   │   │   ├── knowledge.go     #     wikipedia, currency_convert, github
│   │   │   └── workspace.go     #     file_list, file_read, file_write, pdf_text
│   │   ├── markdown/            #   Markdown skill loader + pipeline executor
│   │   │   └── loader.go        #     YAML frontmatter parser, {{placeholder}} resolution
│   │   └── netguard/            #   SSRF guard — vets every outbound skill dial after DNS
│   └── telegram/
│       ├── agent_client.go      #   Calls gateway /api/query[/stream]
│       ├── auth.go              #   SQLite-backed Telegram user allowlist
│       └── bot.go               #   Update poller + command handlers
├── test/e2e/                    # End-to-end suite — drives a running gateway over HTTP
│   ├── e2e_test.go              #   Build tag `e2e`; imports no project packages
│   └── mockllm/main.go          #   Deterministic Ollama stand-in (chat queue + embeddings)
├── web/                         # React frontend (Vite + TypeScript + Tailwind)
│   ├── package.json
│   ├── vite.config.ts           #   base: '/ui/', dev proxy → :8000
│   ├── tailwind.config.ts       #   custom dark palette + animations
│   └── src/
│       ├── main.tsx             #   React entry, QueryClient, Toaster
│       ├── App.tsx              #   BrowserRouter + route table
│       ├── index.css            #   Tailwind directives + scrollbar + selection
│       ├── api/
│       │   ├── types.ts         #   TypeScript types matching Go API structs
│       │   ├── client.ts        #   Typed REST helpers (fetch + json<T>)
│       │   ├── auth.ts          #   GATEWAY_API_TOKEN prompt + localStorage + auth header
│       │   └── stream.ts        #   fetch-based SSE parser (supports POST)
│       ├── store/
│       │   └── chat.ts          #   Zustand: session ID + live stream steps
│       ├── components/
│       │   ├── Layout.tsx       #   Icon rail nav sidebar
│       │   ├── SessionList.tsx  #   Session list: search, inline rename, delete, New button
│       │   ├── ChatBubble.tsx   #   Markdown-rendered message bubbles + copy
│       │   ├── ToolCard.tsx     #   Collapsible tool call card (args + result)
│       │   ├── MemoryEditModal.tsx #  Edit a memory's text, metadata and expiry
│       │   ├── SkillEditor.tsx  #   Markdown skill editor with live validation
│       │   ├── TaskCreateModal.tsx #  New scheduler task (skill or prompt, time zone, retries) + cron preview
│       │   ├── TaskRunHistory.tsx #  Per-task run history
│       │   └── UsagePanel.tsx   #   Usage chart + top skills (Health page)
│       ├── lib/
│       │   ├── cron.ts          #   Next-run preview in a time zone (mirrors scheduler/cron.go)
│       │   ├── time.ts          #   Date / relative-time formatting
│       │   ├── download.ts      #   Save a Blob (memory export)
│       │   └── useDebounced.ts  #   Debounce hook (session search)
│       └── pages/
│           ├── Chat.tsx         #   Streaming chat, session sidebar, abort
│           ├── Memory.tsx       #   Search, add, namespace filter, edit, delete, export/import
│           ├── Skills.tsx       #   Skill catalog, playground, markdown skill editor
│           ├── Scheduler.tsx    #   Tasks: create, enable/disable, run now, history, retries
│           ├── Activity.tsx     #   Skill invocation audit log + replay
│           └── Health.tsx       #   Gateway status + version, usage panel, auto-refresh
├── docker/
│   ├── frontend.Dockerfile      # node:20-alpine build → nginx:1.27-alpine
│   ├── gateway.Dockerfile       # golang:1.25-bookworm → distroless (~16 MB)
│   ├── telegram.Dockerfile      # golang:1.25-bookworm → distroless (~12 MB)
│   ├── migrate.Dockerfile       # Three-stage: Python exporter, Go builder, distroless importer
│   ├── e2e.Dockerfile           # mockllm + the compiled e2e test binary
│   └── nginx.conf               # SPA fallback + /api proxy + SSE tuning
├── scripts/e2e.sh               # Runs the e2e suite (docker or --host mode)
├── .github/workflows/ci.yml     # CI: gofmt, vet, staticcheck, race suite, web build, e2e
├── run_tests.sh                 # Runs the unit suite (local -race, or --docker)
├── Dockerfile.test              # Image `make test` runs the unit suite in
├── docker-compose.yml           # 3-service stack (frontend, gateway, telegram-bot)
├── docker-compose.e2e.yml       # e2e stack (mockllm, gateway, gateway-locked, runner)
├── docker-compose.migrate.yml   # One-shot migration containers
├── Makefile                     # init / web / build / build-all / cli / migrate-vector / vet / test / e2e / e2e-host / dev-web / vector-*
├── .dockerignore
├── .env.example
├── CHANGELOG.md                 # Release notes (one entry per version)
└── go.mod
```

---

## 3. Component design

### 3.1 Configuration (`internal/config`)

All configuration is environment-driven, matching the original `.env` pattern.

| Group | Variables | Defaults |
|---|---|---|
| Gateway | `BACKEND_PORT`, `DATA_DIR` | 8000, `/app/data` |
| Frontend | `FRONTEND_PORT` | 80 (Docker Compose only) |
| LLM | `LLM_PROVIDER`, `CHAT_MODEL`, `OLLAMA_URL`, `OLLAMA_MODEL`, `OLLAMA_TIMEOUT`, `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `OPENAI_CHAT_MODEL`, `OPENAI_TIMEOUT`, `OPENAI_API_MODE`, `OPENAI_REASONING_EFFORT`, `OPENAI_TEMPERATURE` | ollama, —, `http://127.0.0.1:11434`, `llama3:latest`, 120s, —, `https://api.openai.com/v1`, `gpt-4o-mini` (`gpt-5.3-codex` for `openai-codex`), 120s, auto, — (omitted), 0.2 |
| LLM (DigitalOcean) | `DIGITALOCEAN_API_KEY`, `DIGITALOCEAN_BASE_URL`, `DIGITALOCEAN_CHAT_MODEL`, `DIGITALOCEAN_EMBED_MODEL`, `DIGITALOCEAN_TIMEOUT` | —, `https://inference.do-ai.run/v1`, `meta-llama/Llama-3.3-70B-Instruct`, `qwen3-embedding-0.6b`, 120s |
| LLM (opencode) | `OPENCODE_API_KEY` (fallbacks `OPENCODE_GO_API_KEY`, `OPENCODE_ZEN_API_KEY`), `OPENCODE_BASE_URL`, `OPENCODE_CHAT_MODEL`, `OPENCODE_SESSION_ID`, `OPENCODE_TIMEOUT` | —, tier-dependent, tier-dependent, random per process, 120s |
| Agent | `AGENT_DB`, `AGENT_CONTEXT_TAIL`, `AGENT_MEMORY_HITS`, `AGENT_MAX_STEPS`, `AGENT_PARALLEL_TOOLS`, `AGENT_MAX_PARALLEL`, `AGENT_TOOL_MODE` | `{DATA_DIR}/agent_memory.sqlite3`, 30, 5, 25, false, 4, auto |
| Auto-memory | `AUTO_MEMORY_EXTRACT`, `AUTO_MEMORY_MAX_FACTS`, `AUTO_MEMORY_MIN_ANSWER_LEN` | false, 5, 80 |
| Playground | `PLAYGROUND_DISABLED` | false (playground enabled) |
| CLI | `OMEGA_REMOTE`, `GATEWAY_API_TOKEN` | unset (local mode); set to gateway URL for remote mode; — (sent as bearer token in remote mode) |
| Vector memory | `AGENT_VECTOR_DIR`, `AGENT_VECTOR_COLLECTION`, `AGENT_DEDUP_DISTANCE`, `MEMORY_DEFAULT_TTL_DAYS` | `{DATA_DIR}/chromem`, `memories`, 0.08, 0 (never expire) |
| Embeddings | `EMBED_PROVIDER`, `OLLAMA_EMBED_MODEL`, `OPENAI_EMBED_MODEL` | `{LLM_PROVIDER}`, `nomic-embed-text`, `text-embedding-3-small` |
| Scheduler | `SCHEDULER_DB`, `SCHEDULER_TICK_SEC` | `{DATA_DIR}/scheduler.sqlite3`, 60 |
| Telegram | `TELEGRAM_BOT_TOKEN`, `BOT_AUTH_ENABLED`, `BOT_ADMIN_ID`, `BOT_AUTH_DB`, `GATEWAY_URL`, `BOT_MEMORY_SCOPE` | —, true, 0 (required while auth is on), `/app/data/telegram_auth.sqlite3`, `http://127.0.0.1:8000`, shared |
| Skills | `SKILLS_DIR`, `SKILL_HTTP_TIMEOUT`, `SKILL_SHELL_ENABLED`, `SKILL_SSH_ENABLED`, `SKILL_SSH_IDENTITY_FILE`, `SKILL_SSH_DEFAULT_USER`, `SKILL_SSH_PRIVATE_KEY`, `SKILL_ALLOW_PRIVATE_NETWORKS` | `{DATA_DIR}/skills`, 30, false, false, —, root, —, true |
| Shell sandbox | `SKILL_SHELL_SANDBOX`, `SKILL_SHELL_SANDBOX_NETWORK`, `SKILL_SHELL_SANDBOX_WRITABLE` | none, false, — |
| SSH host keys | `SKILL_SSH_KNOWN_HOSTS`, `SKILL_SSH_HOST_KEY_POLICY` | `{DATA_DIR}/ssh_known_hosts`, accept-new |
| Workspace / GitHub | `SKILL_WORKSPACE_DIR`, `SKILL_WORKSPACE_MAX_MB`, `SKILL_GITHUB_TOKEN` (fallback `GITHUB_TOKEN`) | `{DATA_DIR}/workspace`, 100, — |
| notify | `NOTIFY_TELEGRAM_CHATS`, `NOTIFY_TELEGRAM_DEFAULT_CHAT`, `NOTIFY_EMAIL_TO`, `NOTIFY_SMTP_HOST`, `NOTIFY_SMTP_PORT`, `NOTIFY_SMTP_USER`, `NOTIFY_SMTP_PASSWORD`, `NOTIFY_SMTP_FROM`, `NOTIFY_WEBHOOKS` | all empty (channels off), port 587 |
| Audit log | `AUDIT_MAX_BLOB_BYTES` | `65536` (0 = disabled) |
| MCP | `MCP_SERVER_DISABLED`, `MCP_SERVERS`, `MCP_CONNECT_WAIT_SEC`, `MCP_REFRESH_SEC` | false (server enabled); — (no remote servers); 10; 300 |
| API access | `GATEWAY_API_TOKEN`, `CORS_ALLOWED_ORIGINS`, `GATEWAY_ALLOWED_HOSTS` | — (API open), — (same-origin only), — (only IP / single-label / `*.localhost` Host values while no token is set) |

Boolean switches read through `isTruthy` / `isFalsy` (and the bot's own
`boolEnv`) share one vocabulary: `true`/`1`/`yes`/`on` and
`false`/`0`/`no`/`off`, case-insensitive.  `isTruthy` used to miss `on`, so
`PLAYGROUND_DISABLED=on` or `MCP_SERVER_DISABLED=on` left the surface enabled
— a disable switch that fails open.

`LLM_PROVIDER` determines both the chat client and the embeddings backend.
When `OPENAI_API_MODE` is unset and the chat model name contains `codex`, the
OpenAI client switches to the `/responses` API endpoint.  The switch depends
only on the model name; `openai-codex` gets there because its default chat
model is `gpt-5.3-codex`, and pointing it at a non-codex model keeps
`chat_completions`.  `digitalocean` (alias `do`) is
served through the same OpenAI-compatible client in `chat_completions` mode,
pointed at `DIGITALOCEAN_BASE_URL`.  `opencode` (aliases `opencode-go`,
`opencode-zen`, `zen`) likewise, plus one extra request header — see §3.2.

`LLM_PROVIDER` also picks the embeddings backend, unless `EMBED_PROVIDER`
overrides it.  The split exists because a chat provider need not serve
embeddings at all: the opencode relay does not, so `EMBED_PROVIDER=openai`
(or `digitalocean`, or the default `ollama`) is how vector memory gets filled
while chat runs on opencode.

`OPENAI_TEMPERATURE` is parsed into a `*float64` so that "unset" and "omit"
stay distinguishable: an empty value means the 0.2 default, an unparseable one
falls back to it, and `none` / `omit` / `off` yield `nil`, which drops the
`temperature` field from the request body altogether. Reasoning models
(o-series, gpt-5 family) reject any non-default temperature, so sending the
field at all makes them fail. The Ollama client is unaffected — it pins
`options.temperature` to 0.2.

### 3.2 LLM clients (`internal/llm`)

```go
type ChatClient interface {
    CompleteJSON(messages []Message) (raw string, elapsedSec float64, err error)
    Model() string
    BaseURL() string
}
```

#### OllamaChat

POSTs to `/api/chat` with `stream: false`, `format: "json"`,
`temperature: 0.2`.  Returns `message.content` from the Ollama JSON envelope.

#### OpenAIChat

Two code paths selected by the `mode` field:

| Mode | Endpoint | Response format | When |
|---|---|---|---|
| `chat_completions` | `/chat/completions` | `response_format: {type: "json_object"}` | Default for all non-codex OpenAI models |
| `responses` | `/responses` | Parses `output_text` or nested `output` array | Model name containing `codex`, or explicit `OPENAI_API_MODE=responses` |

Both modes map `role: "tool"` messages to `role: "user"` with a
`"[Tool result]:"` prefix, since the APIs do not accept a native tool role
without a preceding tool-use turn.  (Ollama accepts the tool role verbatim, so
`OllamaChat` sends the message list unchanged.)

`postWithRetry` retries HTTP 429, any 5xx, and transport-level errors up to
three attempts, backing off 2s then 4s.  Client **timeouts** are deliberately
not retried: each attempt already waited the full configured timeout, and
stacking two more multi-minute waits makes the agent look frozen.  The backoff
honours the request context: `sleepFor` waits on a timer *or* `ctx.Done()`, so
a cancelled request (client gone, gateway shutting down) returns at once
instead of sitting out 2 s / 4 s.  Tests replace the wait with the `sleep`
field to assert the schedule without waiting; production leaves it nil —
`NewOpenAIChat` used to install `time.Sleep` there, which made the
context-aware branch unreachable.  Provider error bodies are cut to 400 bytes
on a rune boundary.

#### DigitalOcean

DigitalOcean Serverless Inference is OpenAI-compatible, so there is no separate
client type: `bootstrap.BuildChat` constructs an `OpenAIChat` in
`chat_completions` mode using `DIGITALOCEAN_API_KEY` / `DIGITALOCEAN_BASE_URL` /
`DIGITALOCEAN_CHAT_MODEL`. Embeddings likewise reuse `openAIEmbeddings` against
the DigitalOcean base URL (`DIGITALOCEAN_EMBED_MODEL`).

#### opencode

The opencode Zen relay (<https://opencode.ai/docs/zen/>) is OpenAI-compatible
with one deviation: every request must carry an **`x-opencode-session`** header
or the relay answers `400 MissingSessionID` — *"Request is missing
x-opencode-session and cannot be routed efficiently"*.  A stock OpenAI client
therefore cannot talk to it.  `OpenAIChat` grew a generic `WithHeader` option
for this, and `llm.NewOpenCodeChat` applies it:

```go
llm.NewOpenCodeChat(apiKey, baseURL, model, sessionID, temperature, timeoutSec)
```

An empty `sessionID` mints a random `ses_<32 hex>` value that lives as long as
the client, which keeps a long-running gateway on one prompt-cache lane.  Pin
it with `OPENCODE_SESSION_ID` when several processes should share that lane.

One key serves two tiers on two paths, so the provider alias picks the default
base URL and model:

| `LLM_PROVIDER` | Default `OPENCODE_BASE_URL` | Default `OPENCODE_CHAT_MODEL` | Billing |
|---|---|---|---|
| `opencode`, `opencode-go` | `https://opencode.ai/zen/go/v1` | `kimi-k2.7-code` | opencode Go subscription |
| `opencode-zen`, `zen` | `https://opencode.ai/zen/v1` | `claude-sonnet-5` | pay-as-you-go credits |

Only `chat_completions` is supported — the relay exposes no `/responses`
endpoint — and it serves no `/embeddings` either, hence `EMBED_PROVIDER`.

Relay behaviours worth knowing:

- A Go-tier key hitting the zen tier without credits gets `401 CreditsError`,
  not a permissions error — the tier is a billing boundary, not an auth one.
- A retired model answers `410 ModelDeprecated`, naming its successor in
  `metadata.replacement` (and in the message prose).  `OpenAIChat` adopts that
  successor and replays the request, so the deprecation surfaces as a log
  line rather than a failed turn; the adoption lasts for the life of the
  client, and `CHAT_MODEL` / `OPENCODE_CHAT_MODEL` pins a model outright.
- Some models accept only their own default temperature and answer `400
  invalid temperature: only 1 is allowed for this model`.  `OpenAIChat` stops
  sending the field and replays; omitting it lands on the model's default,
  which is by definition the value such a model allows, so nothing has to
  guess at what that value is.  `OPENAI_TEMPERATURE=none` sets it up front.

Both live in `healAfterError`, which applies at most `maxSelfHeal` fixes per
call and refuses to repeat a fix, so a relay rejecting a request for a reason
we keep misreading surfaces its error instead of looping.
- Models differ in how they wrap the envelope.  `kimi-k2.7-code`, `kimi-k3` and
  `glm-5.3` return a clean JSON body (kimi puts its reasoning in a separate
  `reasoning` field, which the client ignores), while `minimax-m3` prepends a
  literal `<think>` block to the content despite `response_format`.
  `parseJSONSafely` handles both — see §3.3 — so model choice is a cost
  question here, not a correctness one.

### 3.3 Agent loop (`internal/agent`)

The agent loop implements a strict JSON tool-calling protocol.  On each
iteration the LLM must return one of two envelope types:

```json
// Tool invocation
{
  "type": "tool_call",
  "tool": "<name>",
  "args": { ... },
  "why": "<one-line reason>"
}

// Final answer
{
  "type": "final",
  "answer": "<text>",
  "notes": "<optional>"
}
```

When `AGENT_PARALLEL_TOOLS=true`, a third envelope is allowed and a corresponding
addendum is appended to the system prompt:

```json
// Parallel batch — execute multiple INDEPENDENT tools at once
{
  "type": "tool_calls",
  "calls": [
    {"tool": "weather", "args": {"city": "Berlin"}, "why": "..."},
    {"tool": "weather", "args": {"city": "Paris"},  "why": "..."}
  ]
}
```

#### Tool modes (`native.go`)

`AGENT_TOOL_MODE` (`Service.ToolMode`) selects how the model calls tools:

- **native** — when the chat client implements `llm.ToolChatClient`
  (`OllamaChat`, `OpenAIChat` in both API modes), `startSession` renders the
  tool table as JSON-Schema function definitions (`toolSpecs`, name-sorted,
  `vector_add` / `vector_search` included with their parameters) and appends
  `nativeAddendum` to the system prompt. `nextMove` calls
  `CompleteTools(ctx, messages, specs)` and maps the reply onto the envelope
  vocabulary the rest of the loop speaks (`envelopeFromCompletion`): one tool
  call → `tool_call`, several → `tool_calls` (each with its provider call id),
  text that parses as an envelope → that envelope, any other non-empty text →
  `final`. `toolTurn` then records the step the way the API demands: an
  assistant message carrying `ToolCalls` and one `role:"tool"` message per
  result tied by `ToolCallID` (ids are generated for Ollama, which issues none).
- **envelope** — the original protocol: the reply is text, parsed by
  `parseJSONSafely` below; `toolTurn` replays the envelope JSON plus one tool
  message.
- **auto** (default) — native when supported. The first
  `llm.ErrToolsUnsupported` (a 400 whose body says tools are unsupported —
  `toolsUnsupportedRE`) latches `Service.nativeOff`, strips the addendum and
  retries the same step through the envelope; every later run starts in
  envelope mode. `envelopeMessages` flattens any native turns already in the
  history into envelope text first.

`meta.tool_mode` reports the mode a run used. JSON mode (`format=json`,
`response_format: json_object`) is not sent with tools: it forces a text reply
and suppresses tool calls on several models.

#### Envelope recovery (`parseJSONSafely`)

Not every model returns the bare object the prompt asks for, so the parser is
deliberately tolerant:

1. Reasoning preambles — `<think>`, `<thinking>`, `<reasoning>`, `<scratchpad>`,
   in any case, with or without attributes — are stripped first, along with a
   stray closing tag whose opener the provider swallowed.
2. The remaining text is scanned for *balanced* top-level `{...}` spans, with
   braces inside string literals ignored.  Markdown ```json fences need no
   special handling: they contribute no braces, so the scan finds the object
   inside them.  A `{` in prose that never closes ("pick one of {a, b. Final:
   {...}") would keep the depth above zero to the end and hide the real
   envelope, so the scan restarts just past such a brace (at most 16 restarts,
   which keeps a reply made of thousands of stray braces from going
   quadratic).
3. Each span is unmarshalled in order; the first that parses **and** carries a
   `type` field wins, with the first otherwise-valid object as a fallback.
4. A single-key `{"raw_model_json": "..."}` wrapper (legacy history rows) is
   unwrapped at whichever step produced the object.

The earlier implementation used a greedy `\{.*\}` match, which spans from the
first `{` anywhere in the text to the last `}`.  That worked only while a
narrated preamble contained no braces — a `<think>` block quoting JSON welded
the preamble onto the real envelope and failed the whole turn.  The balancing
scan removes that failure mode; `internal/agent/agent_test.go` covers the
reproducers.

`AGENT_MAX_PARALLEL` (default 4) is both the concurrency limit and the batch
size limit: `Service.capBatch` truncates an oversized `calls` array to that many
entries before anything executes, and the follow-up message tells the model how
many calls were dropped so it can re-issue them in a later batch.  Without that
cap a single LLM reply could fan out arbitrarily many concurrent executions —
port scans, shell commands — regardless of the limit the system prompt states.

Calls in a batch run concurrently up to `AGENT_MAX_PARALLEL` via a
semaphore-bounded goroutine pool (`Service.executeBatch`).  Results are
appended to the LLM context in the order the model emitted them — not in
completion order — so the LLM's view stays deterministic.  A failure in one
call does not cancel its siblings; each result is surfaced individually.
Streaming mode emits all `tool_call` events upfront, then `tool_result` events
as each completes (potentially out of order).

#### Run flow

```
startSession()
  ├─ create or load session (via memory.Client)
  ├─ semantic search for relevant memories (vector store; skipped when
  │     AGENT_MEMORY_HITS <= 0 — the store's own default of 5 used to apply)
  ├─ load message tail (last N turns from history store)
  ├─ inject context block: relevant memories + current UTC datetime
  │     (+ Telegram chat_id when called from the bot)
  └─ assemble tool table (in-process skills + native skills)

for step = 1..MaxSteps:
  ├─ call ChatClient.CompleteJSON(messages)
  ├─ parse JSON (with auto-recovery for malformed envelopes)
  ├─ normalizeToolCall() — fix LLM mistakes, e.g. tool name in type field
  │
  ├─ if type == "final":
  │     persist answer
  │     maybeExtractMemories() — background fact distillation (opt-in)
  │     return RunResult
  │
  ├─ if type == "tool_calls":
  │     capBatch(calls) — keep at most MaxParallel, report the rest as dropped
  │     executeBatch(state, calls) — runOne() per call (parallel up to MaxParallel)
  │     append combined results to messages, continue
  │
  └─ if type == "tool_call":
        runOne(state, batchCall{name, args, why, step})
          ├─ safeExecute() the skill or built-in tool — a panic becomes an
          │    ordinary tool error instead of killing the process
          ├─ extract binary attachments (QR images, etc.)
          ├─ write AuditRecord to skill_invocations (best-effort, redacted
          │    for sensitive skills)
          └─ return batchResult{result, elapsed, attachments}
        persist historyResult(result) — redacted for secret-bearing skills
        update timings + debug log
        append tool result to messages (capped at 32 KiB — maxToolContentBytes)
        continue

if MaxSteps exceeded:
  return "could not finish" fallback
```

#### Built-in tools

| Tool | Purpose | Implementation |
|---|---|---|
| `vector_add` | Store a durable fact or preference | `memory.Client.AddMemory()` → in-process vector store |
| `vector_search` | Semantic search over stored memories | `memory.Client.SearchMemory()` → in-process vector store |
| `session_search` | Keyword search over earlier conversations (all words must match; the current session is excluded) | `memory.Client.SearchMessages()` → SQLite `LIKE` over user/assistant messages, snippet around the first word |
| `schedule_task` | Create/list/delete/delete_all/enable/disable cron tasks | Native Go via `scheduler.ScheduleTaskSkill` |
| `web_search` | DuckDuckGo HTML search (no API key); returns title/url/snippet | Native Go via `search.WebSearchSkill` |

All registered skills (weather, dns_lookup, shell_command, etc.) are listed
directly from the in-process `skills.Registry` on each run, so hot-registered
skills (via `skill_creator`) appear immediately without a restart.  The list is
name-sorted, which keeps the system prompt byte-identical between runs — see
§3.5.

`vector_add` and `vector_search` are agent-loop tools, not registry skills, so
they are not exposed via `/api/skills`, the playground, or `/api/…/replay`
(which only accepts `kind=skill` rows).  `/api/tools` documents them instead.

#### System prompt

The system prompt instructs the LLM to:

1. Always output strict JSON (`tool_call` or `final`).
2. Use `vector_add` / `vector_search` for persistent memory.
3. Call tools instead of hallucinating answers.
4. Use `skill_creator` to self-extend when no existing skill fits.

A second context message carries the semantic-search memory hits, the current
UTC datetime, and (from the Telegram bot) the caller's chat_id.  The current
datetime lets the LLM convert relative/local times ("at 3pm", "in 20 minutes")
into UTC cron expressions for `schedule_task` without a `datetime_skill`
round-trip — the scheduler matches cron in UTC.

#### Auto-memory extraction (`extract.go`)

When `AUTO_MEMORY_EXTRACT=true`, both `Run` and `RunStream` call
`maybeExtractMemories()` immediately after persisting a `final` answer.  It
spawns a **detached goroutine** so the user-facing response returns with no
added latency, then runs one extra `CompleteJSON` call asking the model to
distil the (query, answer) turn into at most `AUTO_MEMORY_MAX_FACTS` durable,
self-contained facts.  Each fact is stored via `Memory.AddMemory` tagged
`source=auto-extract`; the vector store's existing SHA256 + cosine dedup makes
the pass idempotent.

Guards: the feature is off by default; turns whose answer is shorter than
`AUTO_MEMORY_MIN_ANSWER_LEN` are skipped; only clean `final` answers trigger it
(fallback answers, parse failures, and max-steps bailouts do not); and all
errors are logged, never surfaced — extraction must not affect the answer the
user already received.  The extraction prompt requests a `{"facts": [...]}`
object (not a bare array) because the OpenAI client pins
`response_format=json_object`, which rejects top-level arrays; parsing also
accepts a bare array or one embedded in surrounding text.  At most 2
extraction passes run concurrently (`extractSlots`); a pass that finds both
slots taken is dropped and logged rather than queued — auto-memory is
best-effort.

#### Streaming

`RunStream()` writes `Event` structs to a channel:

| Event type | Payload |
|---|---|
| `thinking` | Step number |
| `tool_call` | Tool name, args, why |
| `tool_result` | Tool name, result text (first 300 bytes; `[redacted]` for `password_gen` / `jwt_inspect`), elapsed seconds |
| `final` | Session ID, answer, metadata (`meta.fallback = true` when the answer is a recovery, not a real model answer) |
| `error` | Error message — the run could not proceed (session creation or LLM transport failure). A model reply the parser cannot use is *not* an error; it arrives as a `final` with `meta.fallback = true` |

The HTTP handler in `httpapi/chat.go` serializes these as SSE:
```
event: tool_call
data: {"step":1,"tool":"weather","args":{"city":"London"},"why":"checking current weather"}

event: final
data: {"session_id":42,"answer":"It's 15°C in London.","meta":{...}}
```

#### JSON parsing & recovery

The agent must tolerate malformed LLM output:

1. **Clean JSON** — parse directly.
2. **Embedded JSON** — after stripping `<think>`-style preambles and code fences, scan for balanced top-level `{...}` spans (string- and escape-aware) and take the first that parses as an envelope.
3. **`raw_model_json` wrapper** — some models wrap output in a meta-envelope;
   the parser unwraps it.
4. **normalizeToolCall** — if the LLM puts the tool name in the `type` field
   instead of `tool`, the parser auto-corrects.
5. On unrecoverable parse failure — **both** `Run` and `RunStream` return the
   same graceful fallback answer (*"I had trouble processing that request. Please
   try rephrasing."*) with `meta.fallback = true`; streaming delivers it as a
   normal `final` event carrying the session id, so the client can continue the
   conversation. `RunStream` used to emit an `error` event instead, which showed
   the raw parser message (*"model did not return JSON. Got: …"*) in the web UI
   and Telegram bot for a reply the non-streaming endpoint answered politely.
   An `error` event now means the run genuinely could not proceed (session
   creation or LLM transport failure), not that the model phrased itself badly.
   When the envelope parses but is not a recognised `type`, the loop falls back
   to the model's best-effort text via `bestAnswer`.

### 3.4 Memory & history (`internal/memory`)

All memory operations run in-process — no HTTP round-trip to a sidecar.
`memory.New(cfg)` returns a `*Client` that owns both stores.

#### History store (`history.go`)

SQLite with WAL journal mode and a single connection (`SetMaxOpenConns(1)`):

```sql
sessions (id INTEGER PK, created_at REAL, title TEXT DEFAULT '')
messages (id INTEGER PK, session_id FK, ts REAL, role TEXT, content_json TEXT)
  INDEX ON messages(session_id, ts)
```

`unwrapContent()` decodes `content_json` before returning:
- Skips rows where `role = "raw_model_json"`.
- Unwraps single-key `{"content":"..."}` JSON payloads to a plain string.

Public methods:

| Method | Purpose |
|---|---|
| `CreateSession()` | Insert a new session row, return ID |
| `ListSessions(limit)` / `SearchSessions(limit, q)` | Most-recent sessions with message count, title, first-user-message preview and last activity; `q` matches titles or message text (`LIKE … ESCAPE`, wildcards escaped) |
| `RenameSession(id, title)` / `DeleteSession(id)` | Title a session; delete it with its messages in one transaction (audit rows are kept: the record of what ran outlives the chat) |
| `Usage(days)` | Queries (user messages) and audited tool calls per hour (≤ 2 days) or day, with error counts and mean duration, plus per-skill totals — the Health page charts |
| `ListMessages(sid, limit, offset)` | Paginated message list |
| `LoadTail(sid, limit)` | Last N messages for LLM context (skips `raw_model_json`) |
| `AddMessage(sid, role, content)` | Persist one message |

#### Audit log (`audit.go`)

Skill and tool invocations are recorded in `skill_invocations`, which lives in
the same `agent_memory.sqlite3` database:

```sql
skill_invocations (
    id            INTEGER PK,
    session_id    INTEGER FK → sessions(id),
    step          INTEGER,       -- agent loop step number
    ts            REAL,          -- unix seconds
    skill         TEXT,          -- skill/tool name
    kind          TEXT,          -- 'skill' | 'tool' | 'unknown' | 'replay'
    args_json     TEXT,          -- caller args (truncated to AUDIT_MAX_BLOB_BYTES)
    result_json   TEXT,          -- skill result (nullable; truncated)
    error_msg     TEXT,          -- nullable; non-null on failure
    duration_ms   INTEGER,
    why           TEXT,          -- LLM's stated reason for the call
    replayed_from INTEGER        -- FK to id of the original invocation
)
INDEX ON skill_invocations(session_id, ts)
INDEX ON skill_invocations(skill, ts)
```

**Sensitive skills** (`password_gen`, `shell_command`, `ssh_command`,
`jwt_inspect`) always
store `{"redacted": true}` for both args and result — raw values are never
written to the database.

The audit log is not the only place a result is persisted or echoed.  For the
two skills whose *result* is itself a secret (`password_gen`, `jwt_inspect` —
`secretResult()` in agent.go) the model gets the real value in-band for the
current run, while the session history row (served by
`/api/sessions/{sid}/messages` and replayed into every later turn), the
stream's `tool_result` event and the debug log all get a redacted stand-in.
That includes a *failed* call, whose error result echoes its args — for
`jwt_inspect`, the token.  Shell and SSH command lines are redacted in the
audit log only: the stream and debug log show them so an operator can see what
ran (`eventArgs`).

**Blob truncation** — args and result JSON blobs are truncated to
`AUDIT_MAX_BLOB_BYTES` (default 64 KB) before storage.  Set to `0` to disable
audit logging entirely (`AddInvocation` becomes a no-op).

Public methods on `memory.Client`:

| Method | Purpose |
|---|---|
| `AddInvocation(r AuditRecord)` | Write one invocation record (no-op when disabled) |
| `GetInvocation(id)` | Fetch one record by ID, nil if not found |
| `ListInvocations(f AuditFilter)` | Filtered, paginated list + total count |

#### Vector store (`vector.go`)

`chromem-go` (`github.com/philippgille/chromem-go`) cosine-similarity store
opened with `NewPersistentDB(vectorDir, false)`.

Deduplication pipeline:

```
addText(text, meta)                       # whole pipeline under v.mu
  ├─ SHA256(text) → hashes[sha] (plain map) → skip if exact duplicate
  ├─ embed(text) via embeddings client
  ├─ queryClamped(embedding, 1)
  │     └─ if (1.0 - similarity) ≤ dedupDistance (0.08) → skip semantic duplicate
  └─ collection.AddDocument(chromem.Document{...})
        └─ hashes[sha] = memoryID
```

`v.mu` serialises adds, so two identical `vector_add` calls in one parallel
batch cannot both pass the hash check.  A second lock, `v.shrink`
(`sync.RWMutex`), covers the other race: chromem refuses a query for more
results than the collection holds, and every query sizes `k` from `Count()`
first.  A delete landing between the two failed the search or list ("nResults
must be <= the number of documents") — measured at ~10% of searches while a
bulk delete ran.  Queries (`queryClamped`) take the read side around
Count+Query, `deleteByID` takes the write side around `Delete`.  `deleteByID`
also evicts a hash entry only while it still names the document being
deleted.

Metadata is stored as `map[string]string`; `coerceMeta()` converts any
numeric, bool, or nil values from the caller-supplied `map[string]any`.

`SearchMemory(query, k)` embeds the query and runs `queryClamped(emb, k)`
(k ≤ 0 means 5; the agent skips the call entirely when `AGENT_MEMORY_HITS` ≤ 0),
returning `(id, text, metadata, distance)` tuples sorted by ascending distance.

**Namespaces.** `metadata.namespace` scopes a memory (absent = the shared
namespace `""`). `SearchMemoryIn(query, k, namespaces)` runs one filtered
query per namespace (chromem's `where` is an exact-match AND) and merges by
similarity; `AddMemoryOpts` stores into one. The exact-dedup key is
`sha256(text)` in the shared namespace — unchanged, so old documents still
match — and `sha256(namespace + "\x00" + text)` elsewhere; the semantic
check is filtered to the same namespace. An agent run's `RunRequest.Namespace`
decides what `vector_add` writes and what injection / `vector_search` read
(`recallNamespaces`: the run's namespace plus the shared one).

**Expiry.** `metadata.expires_at` (unix seconds) comes from a TTL
(`AddOptions.TTL`; 0 = `MEMORY_DEFAULT_TTL_DAYS`, < 0 = never) or the caller.
Search, list and namespace counts skip expired documents (search asks for 2k
candidates to leave room for them); `PurgeExpired` deletes them, and the
gateway runs `RunJanitor` hourly.

**Enumeration without embeddings.** chromem v0.7.0 has no list API, and
listing used to embed a probe text, so it failed whenever the embeddings
backend was down. The store now records the embedding width in
`omegagrid-<collection>.dim.json` next to the chromem data (chromem skips
non-directory entries) and enumerates with a fixed unit vector of that width.
A collection written before the file existed is probed with common widths
(768, 1024, 1536, …) and, failing that, the first add learns the width from
its own embedding.

**Index rebuild.** `newVectorStore` repopulates the SHA256 index from the
stored documents (`rebuildIndexLocked`), so an exact re-add after a restart is
caught without an embed call. When the width is still unknown at open, the
first add rebuilds the index before its hash check.

**Edit / export / import.** `UpdateMemory` rewrites a document in place
(chromem's `AddDocument` overwrites by id) keeping `created_at`, adding
`updated_at`, re-embedding a changed text, merging metadata (a `nil` value
deletes a key) and moving the hash entry; it refuses a text that would
duplicate a different memory. `ExportMemories` returns every document
(expired included) oldest first, optionally with embeddings;
`ImportMemories` adds each record through the normal dedup pipeline, keeping
its id unless taken and reusing its embedding only when the width matches.

#### Embeddings clients (`embeddings.go`)

Selected by `EMBED_PROVIDER` (falling back to `LLM_PROVIDER`) at construction
time in `buildEmbeddings()`:

| Provider | Client | Endpoints tried (in order) | Model env var |
|---|---|---|---|
| `ollama` (and default) | `ollamaEmbeddings` | `/api/embed`, `/v1/embeddings`, `/api/embeddings` | `OLLAMA_EMBED_MODEL` |
| `openai`, `openai-codex` | `openAIEmbeddings` | `{OPENAI_BASE_URL}/embeddings` | `OPENAI_EMBED_MODEL` |
| `digitalocean` | `openAIEmbeddings` | `{DIGITALOCEAN_BASE_URL}/embeddings` | `DIGITALOCEAN_EMBED_MODEL` |
| `opencode*` | `ollamaEmbeddings` (fallback — the relay has no embeddings) | as Ollama | `OLLAMA_EMBED_MODEL` |

`ollamaEmbeddings.tryEndpoint()` parses all three Ollama response shapes
(`{"embeddings":[...]}`, `{"data":[{"embedding":[...]}]}`, `{"embedding":[...]}`)
in a single pass.  `openAIEmbeddings` uses Bearer authentication and parses
`data[0].embedding`.

### 3.5 Skills (`internal/skills`)

All 51 built-in skills run in-process inside the gateway binary via a thread-safe
`Registry`.  The `skills.Client` wraps the registry with the public `List()`
and `Execute()` API consumed by the agent loop and HTTP handlers.

#### Registry (`registry.go`)

```go
type Registry struct {
    mu      sync.RWMutex
    entries map[string]entry   // name → {Skill, Executor}
}
```

`register(name, skill, executor)` — add or replace a skill entry (used for
hot-registration by `skill_creator`).  `list()` and `execute(name, args)`
hold `mu.RLock` to support concurrent reads during agent loops.

`list()` returns the schemas **sorted by name**. The order is not cosmetic: it
is rendered in the web UI's skill list and spliced into the agent's system
prompt, and raw Go map iteration reshuffled both on every call — churning the UI
between refreshes and rewriting the system prompt on every run, which defeats
provider-side prompt caching and makes identical queries answer differently.
`agent.buildSystemPrompt` sorts the merged tool table (and each skill's
parameter list) for the same reason.

#### Built-in skills (`builtin/`)

51 skills compiled directly into the gateway binary: 50 in `builtin/`, plus
`skill_creator` (defined in `client.go`).  Local `Skill`/`Param` types are
defined in `builtin/helpers.go` to avoid import cycles; `client.go` converts
them to the top-level `skills.Skill` type via `toSkill()`.

| File | Skills |
|---|---|
| `web.go` | `weather`, `http_request`, `web_scrape`, `http_health`, `ip_info` |
| `network.go` | `dns_lookup`, `ping_check`, `port_scan`, `whois_lookup` |
| `recon.go` | `tls_probe`, `http_headers`, `banner_grab`, `ptr_lookup`, `email_auth` |
| `encode.go` | `base64_skill`, `hash_skill`, `uuid_gen`, `password_gen`, `cidr_calc`, `jwt_inspect` |
| `eval.go` | `datetime_skill`, `math_eval`, `cron_schedule` |
| `reminder.go` | `reminder` |
| `exec.go` | `shell_command`, `ssh_command` |
| `qr.go` | `qr_generate` |
| `rss.go` | `rss_fetch` |
| `notify.go` | `notify` |
| `network2.go` | `http_timing`, `dns_compare`, `rdap_lookup`, `smtp_check`, `wake_on_lan`, `subnet_sweep` |
| `traceroute.go` (+ `_linux` / `_other`) | `traceroute` |
| `jsonquery.go` | `json_query` |
| `textutil.go` | `time_convert`, `regex_test`, `text_diff`, `unit_convert` |
| `csvquery.go` | `csv_query` |
| `markdown.go` | `html_to_markdown` |
| `knowledge.go` | `wikipedia`, `currency_convert`, `github` |
| `workspace.go` | `file_list`, `file_read`, `file_write`, `pdf_text` |

Shared hardening (skill arguments come from the LLM, so treat them as
attacker-influenced):

- **`netguard`** (`internal/skills/netguard`) checks every outbound dial in
  `net.Dialer.Control`, i.e. after DNS resolution and on every redirect, which
  also defeats DNS rebinding.  Link-local / cloud-metadata (`169.254.0.0/16`,
  `fe80::/10`, `fd00:ec2::254`), unspecified (`0.0.0.0/8`, `::`) and multicast
  addresses are always refused; `SKILL_ALLOW_PRIVATE_NETWORKS=false` (default
  `true`, since the agent is used for LAN administration) also refuses
  loopback, RFC1918, ULA and CGNAT.  Caveat: with an HTTP(S) proxy configured
  through the environment the transport dials the proxy, so the guard vets the
  proxy address, not the final target.
- **URL schemes** — `http_request`, `web_scrape`, `http_health` and
  `http_headers` accept only absolute `http`/`https` URLs (`checkHTTPURL`).
- **Body cap** — remote response bodies are read up to 2 MiB (`maxBodyBytes`).
- **Timeouts** — model-supplied timeouts are clamped to 60 s
  (`maxTimeoutSec`); `port_scan`'s per-port timeout to 10 s; `shell_command` /
  `ssh_command` to [1, 300] s.  A non-positive value falls back to the default
  rather than meaning "no timeout".
- **Command output** — `shell_command` / `ssh_command` keep at most 256 KiB of
  each of stdout and stderr.  The shell blocklist catches common destructive
  commands by spelling only; it is **not** a security boundary.

Key implementation notes:

- **`tls_probe`** — `crypto/tls` dial with `InsecureSkipVerify` so the peer
  chain is always returned; `verify_ok` / `verify_error` report what a normal
  system-roots + SNI check would say, against the name in `verify_name`.  When
  the target is an IP there is no SNI, and an empty `DNSName` makes `Verify`
  skip name checking entirely — reporting `verify_ok: true` for a certificate
  that does not cover the address; the IP is used as the verify name instead, so
  IP SANs are actually checked.  Leaf SANs, expiry, protocol, cipher and ALPN
  are surfaced as top-level fields for the agent.

- **`http_headers`** — manual redirect loop (up to `max_redirects`) so every
  hop's status and `Location` are recorded; flattens response headers, cookie
  flags (`Secure`/`HttpOnly`/`SameSite`), and a security-header present/missing
  set (HSTS, CSP, XFO, XCTO, Referrer-Policy, Permissions-Policy).

- **`banner_grab`** — TCP connect + optional TLS wrap + optional probe bytes
  (`\r\n` escapes expanded); returns printable banner + hex, capped at 8 KiB.

- **`ptr_lookup`** — `net.LookupAddr` after strict IP parse.

- **`email_auth`** — SPF (`v=spf1`) and DMARC (`_dmarc.`) TXT filters; optional
  comma-separated DKIM selectors (`selector._domainkey.`), which may contain
  dots (RFC 6376 allows a label sequence).  The whole call is bounded: at most
  `maxDKIMSelectors` (10) selectors are probed and every lookup shares one
  `context` deadline (`timeout`, default 10s).  `net.LookupTXT` takes no context
  at all, and the selector list comes from the model — 60 selectors cost ~9s of
  serial lookups even with no network reachable, and minutes against a slow
  resolver, blocking the agent step for the duration.  Selectors that are
  invalid or past the cap come back in `dkim_skipped` with a reason instead of
  disappearing.  DNS goes through the package-level `lookupTXT` seam so tests
  cover record filtering without a network.

- **`jwt_inspect`** — base64url-decodes header/payload only (never verifies);
  flags `alg=none`, `exp`/`nbf` timing, and common claims.  It is in
  `sensitiveSkills`, so its args and result are redacted in the audit log: the
  argument is a live bearer token and `/api/invocations` is readable by anyone
  who can reach the API (unless `GATEWAY_API_TOKEN` is set).  The model still
  receives the full result in-band.

- **`math_eval`** — recursive-descent AST parser (`parseAddSub → parseMulDiv →
  parsePow → parseUnary → parsePrimary`).  Supports `//` floor division, `**`
  power, and all Python-parity math functions (`sqrt`, `sin`, `cos`, `tan`,
  `log`, `log2`, `log10`, `factorial`, `abs`, `ceil`, `floor`, `round`, `pow`,
  `pi`, `e`, `inf`, `nan`).  No `eval()` — safe for untrusted input.
  Precedence follows Python: unary minus binds looser than `**` on its left
  (`-2**2` = `-4`), an exponent may itself be signed (`2**-1` = `0.5`), and
  `**` is right-associative (`2**3**2` = `512`).  So do the operators and
  functions: `%` takes the divisor's sign (`-7 % 3` = `2`, where `math.Mod`
  gives `-1`), `round` rounds half to even and takes optional digits,
  `min`/`max` are variadic (two or more arguments), `factorial` rejects
  non-integers instead of truncating them, call arguments must be separated by
  commas (`pow(2 3)` is an error, a trailing comma is fine), and tabs/newlines
  count as whitespace.  Exponent literals (`1e-5`,
  `1e+3`) lex as numbers.  Infinite or NaN results are returned as strings,
  since `encoding/json` cannot encode them.

- **`dns_lookup`** — tries `exec.LookPath("dig")` first for full record-type
  coverage (MX, TXT, CNAME, NS); falls back to Go stdlib (`net.LookupHost`,
  `net.LookupMX`, etc.).  The `domain` argument is validated against
  `isHostname()` before either path runs: `dig` interprets leading `-` and `@`
  tokens as its own options (`-f <file>` reads a file, `@host` picks a server),
  so an unvalidated domain straight from the model could redirect the query.
  `record_type` is an argv entry too, so it is checked against the six
  supported types before `dig` runs; `@10.0.0.53` used to survive `ToUpper` and
  send the query to that server directly, past netguard.

- **`port_scan`** — bounded concurrency via semaphore channel (100 slots),
  `net.DialTimeout` with configurable timeout; returns sorted open port list.
  `parsePorts` enforces the 1–65535 range while expanding, drops duplicates,
  and stops expanding at `maxScanPorts` (1024) — so neither `1-1000000` nor
  thousands of repeated `1-65535` parts materialise a huge slice first, and
  `80,80,80` no longer lists an open port three times.

- **`ping_check`** — TCP connect to `host:port`; `port` outside 1–65535 is an
  error rather than a silent `reachable:false`.

- **`uuid_gen`** — v1, v3, v4, v5, v7.  Any other version is an error; it used
  to return v4 UUIDs labelled with the version requested.

- **`whois_lookup`** — raw TCP port 43 to `whois.iana.org`, parses `refer:`
  line, re-queries the authoritative WHOIS server.

- **`ssh_command`** — `loadSigner()` checks: identity_file arg → `SKILL_SSH_PRIVATE_KEY`
  env (with base64 decode attempt) → `SKILL_SSH_IDENTITY_FILE` path.  The
  model-supplied `identity_file` goes through `readKeyFile`: regular files only,
  at most 64 KiB.  `os.ReadFile("/dev/zero")` grew its buffer until the process
  ran out of memory, and a FIFO or `/dev/stdin` blocked forever, before any
  timeout applied.  Host keys are checked by `hostKeyCallback`
  (`golang.org/x/crypto/ssh/knownhosts`) against `SKILL_SSH_KNOWN_HOSTS`:
  `accept-new` (default) appends an unknown host's key under a mutex and
  refuses a changed one, `strict` refuses unknown hosts, `insecure` is the old
  `InsecureIgnoreHostKey`.

- **`shell_command`** — blocked-pattern list, `context.WithTimeout` derived
  from the run's context, captures both stdout and stderr.  Disabled by default
  (`SKILL_SHELL_ENABLED=false`).  `ShellCommandWith(ShellConfig)` builds the
  process through `shellCmd`: with `SKILL_SHELL_SANDBOX=bwrap` it is
  `bwrap --ro-bind / / --dev /dev --proc /proc --tmpfs /tmp --unshare-all
  --die-with-parent --new-session [--share-net] [--bind DIR DIR] --chdir … sh
  -c CMD` — read-only host filesystem, private `/tmp`, fresh namespaces, no
  network. A missing bwrap fails closed (`bwrapPath` is the test seam). The
  process-group kill on cancel covers bwrap and everything inside it.

- **Context-aware skills** — `builtin.ContextExecutor` (`func(ctx, args)`) is
  what the registry stores. Every skill that blocks on the network or a
  process has a `XxxContext` constructor that threads ctx into
  `http.NewRequestWithContext`, `Dialer.DialContext`,
  `Resolver.Lookup*`, `exec.CommandContext` and retry backoffs; the plain
  `Xxx()` constructors stay as `context.Background()` wrappers for callers and
  tests. Pure-computation skills are wrapped with `builtin.Lift`, which checks
  ctx once before running.

- **`rss_fetch`** — RSS 2.0, RSS 1.0 (RDF) and Atom through one `encoding/xml`
  struct; items from all feeds are merged and sorted newest first. `since`
  takes RFC3339, a date or a duration ago (`parseLongDuration` adds `d` and `w`
  units to `time.ParseDuration`). An undated item is never filtered out — it
  cannot be shown to be old. Besides UTF-8 the decoder accepts ISO-8859-1 and
  Windows-1252 (`feedCharsetReader`) rather than pulling in `x/text`.

- **`notify`** — the model chooses among recipients the operator configured,
  never an arbitrary one: Telegram chats in `NOTIFY_TELEGRAM_CHATS` (plus the
  default chat), email addresses or `@domain`s in `NOTIFY_EMAIL_TO`, webhooks by
  name from `NOTIFY_WEBHOOKS`. Email goes through `net/smtp` over a netguard
  dial (STARTTLS when offered, implicit TLS on 465, PLAIN auth when a user is
  set); CR/LF in the subject and recipient are neutralised, so a title cannot
  add headers. Webhooks get `{"title","text","content"}` — `text` for Slack and
  generic receivers, `content` for Discord. Transport errors are unwrapped from
  `*url.Error` so the bot token or a secret webhook URL never reaches the model.

- **`http_timing`** — `httptrace.ClientTrace` hooks on a netguard transport with
  keep-alives off, so every sample pays DNS, connect and TLS. Redirects are not
  followed (`http.ErrUseLastResponse`); the Location is reported instead.

- **`dns_compare`** — one `net.Resolver{PreferGo: true}` per server whose
  `Dial` targets that server through the netguard dialer; the authoritative
  servers are found with `lookupNS` on the registrable-looking suffix. Answers
  are sorted and grouped, so `consistent` is a plain comparison.

- **`rdap_lookup`** — queries `rdap.org`, which redirects to the authoritative
  registry, and summarises the response (events by action, nameservers,
  entities with their jCard `fn`).

- **`traceroute`** — no raw socket: one UDP socket per TTL with `IP_TTL` and
  `IP_RECVERR`; the kernel queues the ICMP time-exceeded / port-unreachable on
  the socket's error queue, read with `recvmsg(MSG_ERRQUEUE)` and parsed as
  `sock_extended_err` + the offender's `sockaddr_in`. IPv4 and Linux only; the
  `_other` build returns an explanatory error.

- **`smtp_check`** — a hand-driven `textproto` session (the banner is needed,
  and `net/smtp` hides it): banner, `EHLO`, `STARTTLS`, then a TLS handshake
  with verification done separately so the result can say *why* a certificate
  fails. MX hosts are tried in preference order; no MX means the domain itself
  (RFC 5321 §5.1).

- **`wake_on_lan`** — a 102-byte magic packet from a socket with `SO_BROADCAST`
  (set in `exec_unix.go`'s `enableBroadcast`); only private, loopback or
  limited-broadcast targets, and refused entirely when private networks are
  blocked.

- **`subnet_sweep`** — TCP connect to a few ports per address, 64 dials at a
  time; `ECONNREFUSED` proves a host is up as well as an accept does. Capped at
  256 addresses, network/broadcast skipped for IPv4, each address vetted by
  netguard (refused ones are counted, not probed).

- **`json_query`** — a small recursive-descent jq subset evaluated over value
  streams: paths (`.a`, `."k"`, `[n]`, `[a:b]`, `[]`), pipes, `select` with
  comparisons and `contains`, `map`, `sort_by`, and the common built-ins.
  Ordering follows jq (null < false < true < numbers < strings < arrays <
  objects).

- **`time_convert`** — reads `now`, RFC3339, `YYYY-MM-DD[ HH:MM[:SS]]`, `15:04`,
  `3pm` and unix seconds in the source zone, relying on the embedded tzdata;
  business days skip Saturday and Sunday (no holiday calendars).

- **`regex_test`** — Go's RE2: no backreferences or lookaround, which the error
  message makes visible; inputs are capped (2000-byte pattern, 100 KB text).

- **`text_diff`** — LCS line diff with deletions ordered before insertions in a
  change, rendered as unified-diff hunks; 3000 lines per side keeps the
  quadratic table small.

- **`csv_query`** — `encoding/csv` with delimiter detection on the header line;
  values compare numerically when both parse as numbers (thousands commas
  stripped), else case-insensitively; groups are sorted by their first
  aggregate. Workspace files go through `file_read`, so they are confined the
  same way.

- **`html_to_markdown`** — walks the `x/net/html` tree from `<main>`,
  `<article>` or `<body>`, skipping `nav`, `header`, `footer`, `aside`, forms,
  scripts and hidden elements; relative links and images are resolved against
  the final URL.

- **`wikipedia`, `currency_convert`, `github`** — free APIs with no key
  (`github` takes an optional token). Their roots are package variables
  (`wikipediaBase`, `ecbURL`, `githubAPI`, `rdapBase`, `telegramAPIBase`) so
  tests point them at `httptest`; ECB rates are cached for an hour; GitHub's
  issues endpoint also returns pull requests, which are filtered out.

- **Workspace (`file_*`, `pdf_text`)** — every path is resolved through
  `os.Root` (Go 1.24+), which refuses `..`, absolute paths and symlinks leading
  out at the system-call level; `relPath` additionally maps a leading `/` to
  the workspace root. Writes are capped per call (1 MB) and in total
  (`SKILL_WORKSPACE_MAX_MB`, checked before writing, counting only the growth
  of an overwritten file). Reads return ≤ 256 KB chunks ending on a rune
  boundary, with `next_offset`. `pdf_text` uses `github.com/ledongthuc/pdf`
  (pure Go, the project's one PDF dependency) behind a `recover`, since a
  malformed file can make a parser panic.

- **`qr_generate`** — uses `github.com/skip2/go-qrcode` with
  `DisableBorder = true`, so the reported `modules` (from `len(qr.Bitmap())`) is
  the symbol size without a quiet zone and the image is exactly
  `box_size × modules` pixels; `padImage` then adds the `border × box_size`
  quiet zone by copying the symbol pixels verbatim.  Rendering with the
  library's built-in border instead made `box_size` only an approximate
  pixels-per-module figure and reduced `border` to an image-wide scale factor
  that always left a 4-module quiet zone.  The result reports `image_size_px`
  alongside `size_bytes` so callers can check both.

- **`skill_creator`** — writes `.md` files to `SKILLS_DIR` and calls
  `reg.registerMarkdown()` directly on the live registry for instant hot-registration.
  Every action that turns the `name` argument into a path (`create`, `show`,
  `delete`) validates it with `safeNameChars` first — lowercase letters, digits
  and underscores, 2–49 chars, starting with a letter — so a name can only ever
  name a file *inside* `SKILLS_DIR`. `delete` additionally resolves the file's
  frontmatter `name`, which is the key the registry actually uses and need not
  match the filename, before unregistering.

  The frontmatter is produced by yaml.v3 (`renderMarkdown` builds a
  `yaml.Node` mapping so the top-level fields keep a readable fixed order;
  nested maps come out key-sorted).  It used to be hand-written, which quoted
  values but emitted every map key raw — and parameter names and step keys come
  from the model, so a name such as `"x:\nendpoint: http://127.0.0.1:9/\nzz"`
  injected top-level `endpoint` / `timeout` / `steps` fields, turning the
  "prompt-only" skill `skill_creator` reported into an HTTP or self-calling
  pipeline skill.  Multi-line values were single-quoted, which YAML folds
  (`"a\nb"` came back as `"a b"`), and an embedded `\n---\n` ended the header
  early.  The encoder quotes keys, block-indents multi-line strings and keeps
  typed values typed.

  The file is written to a hidden `.<name>-*.tmp` sibling, loaded back with
  `markdown.Load` (and its declared name checked), and only then renamed over
  `<name>.md`.  Writing in place first meant a definition that failed to parse
  destroyed the working skill it overwrote — the old version stayed registered
  until the next restart, when `LoadDir` silently skipped the broken file.

- **Loose argument types** — `builtin/helpers.go` (`str`, `intOr`, `floatOr`,
  `boolOr`) coerces across JSON types on purpose. LLMs quote numbers
  (`"port": "443"`), unquote strings (`"ports": 443`) and spell booleans as words
  (`"use_symbols": "yes"`); rejecting the wrong type silently substituted the
  default instead, so `port_scan` would scan its stock list and `ping_check`
  would dial port 80 with nothing in the result explaining why.

#### Markdown / pipeline skills (`markdown/`)

`.md` files with YAML frontmatter in `SKILLS_DIR` are loaded as skills.
Local `SkillSchema`/`Param` types in `loader.go` avoid import cycles;
`client.go` converts them via `mdToSkill()`.

Pipeline execution:

```
for each step:
  resolve {{placeholder}} tokens in request template:
    - {{param_name}}             → caller-supplied argument
    - {{step_name.field.path}}   → dot-path into the named step's JSON result
    (values substituted into an endpoint URL are percent-encoded)
  execute: HTTP GET/POST  OR  call another skill in the registry
  accumulate step results
```

`LoadDir(dir)` scans `SKILLS_DIR` for `*.md` files on startup.
`skill_creator` calls `Load(path)` after writing a new file for immediate
hot-registration without a restart.

All HTTP requests — single-endpoint and pipeline steps alike — go through one
`doRequest()` helper.  It exists because endpoints come from markdown
frontmatter after `{{placeholder}}` substitution and may be anything, including
an unresolved placeholder: `http.NewRequest` returns a **nil** request together
with its error for a URL it cannot parse (a space in the host, a malformed
scheme), and the previous `req, _ :=` call sites then panicked on `req.Header`.
Inside a parallel tool batch such a panic runs on its own goroutine and takes
the whole gateway down.  `doRequest` honours the error and the step reports it
as an ordinary skill error instead.  GET steps send the caller's kwargs as query
parameters (explicit step `params` win on conflict); POST steps send the
resolved `body` as JSON with `params` in the query string.

Constraints enforced by the loader, the registry and `skill_creator`:

- **Built-in names are reserved.** `skill_creator` refuses to create or
  overwrite a built-in skill or `skill_creator` itself, and `LoadDir` skips (and
  logs) any markdown file whose name collides with one, so a stray
  `http_request.md` cannot shadow the real skill.
- **Nesting.** A pipeline may not list itself as a step, and nested skill steps
  stop at `MaxPipelineDepth` (5) instead of recursing until the goroutine stack
  overflows.
- **Width.** Depth alone does not bound the work: two 20-step pipelines that
  call each other fan out to 20^5 = 3.2M leaf calls (measured 2.8 GB of heap;
  30 steps runs the process out of memory).  A pipeline file may have at most
  `MaxPipelineSteps` (20) steps — `Load` rejects more — and every top-level
  execution carries one shared budget of `maxPipelineCalls` (200) skill steps
  through `Registry.executorAt`; once it is spent each further step reports
  "pipeline call budget … exhausted" as its result.
- **Timeout.** A skill's `timeout` is clamped to `maxTimeoutSec` (300 s).
- **Endpoint escaping (behaviour change).** Values substituted into `endpoint`
  are percent-encoded (`escapeURLValue`: `url.QueryEscape`, `+` → `%20`), so a
  placeholder value can no longer add `/` path segments, `@`/`:` userinfo, or
  otherwise change the host.  Skills that relied on passing a path fragment
  through a parameter must put that structure in the template.
- **Frontmatter.** The closing `---` must be a line of its own; a `---`
  substring inside a value no longer ends the frontmatter.
- Outbound dials go through `netguard` like the built-in HTTP skills.

### 3.6 Scheduler (`internal/scheduler`)

#### Task store (`store.go`)

SQLite table `scheduled_tasks`:

```sql
CREATE TABLE IF NOT EXISTS scheduled_tasks (
    id                      INTEGER PRIMARY KEY AUTOINCREMENT,
    name                    TEXT NOT NULL,
    cron_expr               TEXT NOT NULL,
    skill                   TEXT NOT NULL,
    args_json               TEXT NOT NULL DEFAULT '{}',
    notify_telegram_chat_id INTEGER,
    one_shot                INTEGER NOT NULL DEFAULT 0,
    enabled                 INTEGER NOT NULL DEFAULT 1,
    created_at              REAL NOT NULL,
    last_run_at             REAL,
    last_result             TEXT,
    run_count               INTEGER NOT NULL DEFAULT 0,
    timezone                TEXT NOT NULL DEFAULT '',   -- IANA zone; '' = UTC
    prompt                  TEXT NOT NULL DEFAULT '',   -- agent task when set (skill = 'agent')
    max_retries             INTEGER NOT NULL DEFAULT 0,
    retry_delay_sec         INTEGER NOT NULL DEFAULT 60,
    retry_attempt           INTEGER NOT NULL DEFAULT 0, -- pending retry's attempt number
    next_retry_at           REAL                        -- when it is due; NULL = none
);
CREATE TABLE IF NOT EXISTS task_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT, task_id INTEGER NOT NULL,
    trigger TEXT NOT NULL,            -- cron | retry | manual
    attempt INTEGER NOT NULL DEFAULT 0,
    started_at REAL NOT NULL, finished_at REAL NOT NULL,
    ok INTEGER NOT NULL, result TEXT
);
```

`one_shot` marks a task that should run exactly once and then auto-disable
(used for one-time reminders); the disable happens in the same statement that
claims the run (see below).  Databases created before a column existed are
migrated on startup with a tolerated `ALTER TABLE ... ADD COLUMN` per column
(SQLite has no `ADD COLUMN IF NOT EXISTS`, so the duplicate-column error is
ignored after the first run).  `task_runs` keeps the newest
`maxRunsPerTask` (200) rows per task (`RecordRun` prunes) and is cleared with
its task.

Operations: `Create` / `CreateTask(TaskSpec)`, `Get`, `ListAll`,
`ListEnabled`, `ClaimRun`, `RecordResult`, `RecordRun`, `ListRuns`,
`ScheduleRetry`, `DueRetries`, `ClaimRetry`, `ClearRetry`, `UpdateLastRun`,
`SetEnabled` (disabling also cancels a pending retry), `Delete`, `DeleteAll`.
`ValidateSpec` is the one validator every entry point uses (API, CLI local
mode, `schedule_task`): cron, time zone, exactly one of skill / prompt,
`max_retries` 0–10, `retry_delay_sec` 0–86400.  `DeleteAll` truncates the table in a
single statement and returns the number of rows removed.  The directory for
the database file is created automatically via `os.MkdirAll`.

#### Cron matcher (`cron.go`)

```go
func Matches(cronExpr string, dt time.Time) bool
```

5-field format: `minute hour dayOfMonth month dayOfWeek`, evaluated on the
wall clock of the task's `timezone` (UTC when empty; `Task.Location()`,
`latestMatchIn`). Claims stay keyed on UTC instants, so a wall-clock minute
that repeats at the end of DST fires twice and one skipped at its start never
fires. The zone database is embedded (`time/tzdata`) so zones resolve in the
distroless image.

| Syntax | Meaning |
|---|---|
| `*` | Any value |
| `*/S` | Every S-th value from the field minimum |
| `N` | Exact match |
| `N/S` | N, N+S, … up to the field maximum (Vixie / croniter: `5/15` = 5,20,35,50) |
| `N-M`, `N-M/S` | Range (inclusive), optionally stepped |
| `A,B,C` | Comma-separated list |
| `jan`…`dec`, `sun`…`sat` | Month / weekday names (any case), usable in ranges |

Day of week: 0 = Sunday, 7 is also Sunday.  Day-of-month and day-of-week
follow Vixie semantics: when **both** are restricted the expression fires when
either matches; a field that *starts with* `*` (so `*/2` too) is unrestricted,
and then the two are ANDed — `0 0 */2 * 1` means odd days that are Mondays,
not "odd days plus Mondays".  Invalid expressions return `false` (safe default).

`ValidateCron` checks syntax and ranges, and additionally rejects a
day-of-month that none of the selected months has when day-of-week is
unrestricted (`0 0 30 2 *`, `0 0 31 4,6,9,11 *`): such a task validated and
then never fired.  Feb 29 is accepted.  The `cron_schedule` builtin
(`builtin/eval.go`) and the web UI's "Next:" preview (`Scheduler.tsx`)
implement the same rules and must be kept in step with `cron.go`.

#### Runner (`runner.go`)

A background goroutine started by the gateway only — `bootstrap.New` builds the
`Runner` but leaves it stopped, and only `cmd/gateway` calls `Runner.Start()`,
so `omega` in local mode never executes due tasks:

```
every SchedulerTickSec (default 60s):
  minutes = every whole minute not yet evaluated, oldest first (see below)
  for each enabled task:
    slot = the LATEST of those minutes task.CronExpr matches on the task's
           wall clock (task.Location()); skip if none
    if ClaimRun(task.ID, slot):        # atomic; also disables a one-shot
      execute(task, "cron", 0)
  for each task with next_retry_at <= now:
    if ClaimRetry(task.ID, next_retry_at):   # atomic
      execute(task, "retry", task.RetryAttempt)

execute(task, trigger, attempt):
  run the prompt through the agent (prompt task) or the skill with its args,
    under the runner's context (cancelled by Stop); panic → task error
  RecordRun(...) ; RecordResult(task.ID, result)
  if trigger != manual:
    failed and attempt < MaxRetries → ScheduleRetry(attempt+1, now + delay·2^attempt)
    otherwise                         → ClearRetry
    notify Telegram unless a retry is pending
```

`Runner.SetAgent(AgentFunc)` enables prompt tasks; `bootstrap.New` wires it
to `agent.Service.RunContext` with `AGENT_MAX_STEPS`. `Runner.RunNow(ctx, id)`
is the run-now path: it records a `manual` run and `last_result` but leaves
`last_run_at`, one-shot state and retries alone, so testing a task does not
shift or consume its schedule.

**Missed-minute sweep.** Cron matching works on whole minutes, but a 60s ticker
drifts: fire at `12:00:59.8` and the next fire lands at `12:02:00.1`, so `12:01`
was never examined and anything scheduled for it was lost — silently, with no
error anywhere. `Runner.pendingMinutes` therefore records the last minute it
evaluated and returns every minute since, oldest first; `tick` then asks whether
a task matches *any* of them. The same mechanism covers a suspended host and a
tick delayed past a minute by a long-running task.

The sweep is bounded by `maxCatchupMinutes` (60): a machine asleep for a day
resumes with the last hour checked and logs how many minutes it skipped, rather
than replaying a day of cron. A task matching several swept minutes still runs
**once** — the sweep exists to avoid losing a run, not to replay a backlog.

Deduplication: before executing, the runner calls `Store.ClaimRun(id, slot)`,
an atomic `UPDATE … WHERE enabled = 1 AND (last_run_at IS NULL OR last_run_at
< slot)`.  Only the caller whose update hits a row runs the task, so several
processes can share one scheduler DB without double-firing, and a tick period
shorter than a minute cannot fire twice.  `last_run_at` is set to the
**scheduled minute** (not the completion time); `RecordResult` later stores
`last_result` and bumps `run_count`.

The claimed minute is the one that *matched*, not the current one.  Claiming
"now" let a process whose ticker had drifted past a slot win a claim for a slot
another process had already run (its `last_run_at` was older than "now"), so
the task ran twice.

The same `UPDATE` sets `enabled = 0` for a one-shot task.  Disabling it only
after the run left it armed while the skill executed: a crash, or a shutdown
that abandons a slow task after `StopTimeout`, made "remind me at 3pm" fire
again the next day.

A clock that steps backwards (NTP correction) leaves every `last_run_at` in
the future until wall time catches up, so tasks due in that window are
skipped; the sweep does not try to detect this.

Tasks run **sequentially inside the tick**, so one slow skill delays the rest of
that tick's tasks; the missed-minute sweep is what stops that delay from turning
into a lost run.

One-shot tasks are **disabled** rather than deleted when they fire, so their
`last_result` / `run_count` audit trail survives.  When a one-shot task runs
the `reminder` skill, the Telegram notification delivers the bare reminder
text (`⏰ Reminder: <message>`) instead of the task's JSON result; all other
skills keep the generic `⏰ Scheduled: <name>` + JSON format, and a *failed*
reminder run also falls back to the JSON error so failures stay visible.

#### Native skill (`skill.go`)

`ScheduleTaskSkill` exposes seven actions:

| Action | Parameters | Returns |
|---|---|---|
| `create` | `cron_expr`, `timezone`, `skill` + `args` **or** `prompt`, `name`, `notify_telegram_chat_id`, `one_shot`, `max_retries`, `retry_delay_sec` | `{created: true, task: {...}}` |
| `list` | — | `{count: N, tasks: [...]}` |
| `history` (alias `runs`) | `task_id`, `limit` | `{task_id, count, runs: [...]}` |
| `delete` | `task_id` | `{deleted: true, task_id: N}` |
| `delete_all` | — | `{deleted_all: true, deleted_count: N}` |
| `enable` | `task_id` | `{ok: true, task_id: N, enabled: true}` |
| `disable` | `task_id` | `{ok: true, task_id: N, enabled: false}` |

`delete_all` (aliases `deleteall`, `delete-all`) requires no `task_id` and
removes every scheduled task — this is what the agent calls for requests like
"remove all scheduled tasks".

Registered as a **native** Go skill by `bootstrap.New()` (so the gateway, the
CLI's local mode and the MCP tool provider all share one registration),
operating directly on the `Store` struct — no HTTP round-trip required.

`delete`, `enable` and `disable` return `{error: "Task N not found"}` for an
unknown id rather than reporting success.  `create` accepts `args` as an
object or as a string holding a JSON object (models often send the latter);
anything else is an error rather than a silent `{}`, which used to make a
reminder fire with no message.

### 3.7 HTTP gateway (`internal/httpapi`)

Built on [go-chi/chi v5](https://github.com/go-chi/chi).

#### Middleware stack

1. `middleware.Recoverer` — panic recovery
2. `middleware.RequestID` — inject unique request ID
3. `middleware.RealIP` — trust `X-Forwarded-For`
4. `hostGuard` — DNS-rebinding defence, active only while `GATEWAY_API_TOKEN`
   is empty.  A page on `evil.example` can re-point its own name at
   `127.0.0.1`; its requests then carry `Host: evil.example` and
   `Origin: http://evil.example`, which `originGuard` rightly treats as
   same-origin — full read/write API access, and through `/api/query` the
   agent's tools.  Such a request always names the attacker's dotted DNS
   name, so only Host values a public resolver cannot hand out are served: IP
   literals, single-label names (`localhost`, the compose service `gateway`),
   `*.localhost`, plus `GATEWAY_ALLOWED_HOSTS`.  Anything else gets 403 on
   every route, reads included.  With a token the guard steps aside: the
   rebinding page cannot read the token.
5. `corsMiddleware` — no wildcard: CORS headers are sent only for origins in
   `CORS_ALLOWED_ORIGINS` (`*` allows any); empty means same-origin only, which
   is all the nginx-served UI needs.  `OPTIONS` answers 204.
6. `originGuard` — rejects state-changing requests (anything but
   GET/HEAD/OPTIONS) whose `Origin` header is neither the request's own host nor
   an allowed origin, with 403.  A cross-site form post is a "simple" request
   that skips preflight, so withholding CORS headers alone would not stop it.
   Clients that send no `Origin` (curl, CLI, bot) pass.
7. `limitBody` — `http.MaxBytesReader` caps every request body at 4 MiB.
8. `requireToken` — on `/api/*` and `/mcp` only, when `GATEWAY_API_TOKEN` is
   set: constant-time compare of `Authorization: Bearer <token>`, else 401 with
   `WWW-Authenticate`.  `/health` is never gated.

Server settings (`cmd/gateway/main.go`): `ReadHeaderTimeout` 30 s,
`ReadTimeout` 60 s, `IdleTimeout` 120 s, no `WriteTimeout` (SSE and long agent
runs).  The listener binds every interface (`:BACKEND_PORT`, required inside a
container); with no token set the gateway logs a startup warning that the API
is unauthenticated.  Request contexts derive from a base context cancelled on SIGTERM/SIGINT,
so in-flight runs wind down before `Shutdown`'s 10 s deadline; `/api/query`
passes `r.Context()` to `Agent.RunContext`, so a client disconnect aborts the run.

#### Route table

| Method | Path | Handler | Notes |
|---|---|---|---|
| `GET` | `/health` | `handleHealth` | Release version, provider, model, skills dir, scheduler DB, embeddings probe |
| `POST` | `/api/query` | `handleQuery` | Synchronous agent query |
| `POST` | `/api/query/stream` | `handleQueryStream` | SSE agent query |
| `POST` | `/api/sessions/new` | `handleNewSession` | In-process history store |
| `GET` | `/api/sessions` | `handleListSessions` | In-process history store; `limit` (default 50, non-positive → default); `q` searches titles and message text (LIKE wildcards escaped); rows carry `title`, `preview`, `last_at` |
| `GET` | `/api/sessions/{sid}/messages` | `handleSessionMessages` | In-process history store; `limit` (default 200, non-positive → default), `offset` (clamped to ≥0) |
| `PATCH` | `/api/sessions/{sid}` | `handleRenameSession` | `{title}` (≤ 200 chars); 404 when absent |
| `DELETE` | `/api/sessions/{sid}` | `handleDeleteSession` | Deletes the session and its messages; the audit log keeps its rows |
| `GET` | `/api/stats/usage` | `handleUsage` | `days` (default 7, max 90): queries and audited tool calls per hour (≤ 2 days) or day, plus per-skill totals |
| `POST` | `/api/memory/add` | `handleMemoryAdd` | In-process vector store |
| `POST` | `/api/memory/search` | `handleMemorySearch` | In-process vector store |
| `GET` | `/api/memory` | `handleMemoryList` | List stored memories newest-first; `limit` (default 100, 0=all), `offset` |
| `GET` | `/api/memory/{id}` | `handleMemoryGet` | Single memory by ID (404 if absent) |
| `DELETE` | `/api/memory/{id}` | `handleMemoryDelete` | Delete a memory by ID (404 if absent) |
| `PUT` | `/api/memory/{id}` | `handleMemoryUpdate` | Edit text/meta/expiry in place; 400 on a duplicate text, 404 when absent |
| `GET` | `/api/memory/namespaces` | `handleMemoryNamespaces` | Namespaces with live memory counts |
| `GET` | `/api/memory/export` | `handleMemoryExport` | JSON Lines; `embeddings=1` includes vectors |
| `POST` | `/api/memory/import` | `handleMemoryImport` | JSON Lines or JSON array; exempt from the 4 MiB body cap, limited to 64 MiB itself |
| `POST` | `/mcp` | `mcp.Server.Handler` | MCP server (JSON-RPC 2.0) exposing skills as tools. Gated by `MCP_SERVER_DISABLED`. |
| `GET` | `/api/skills` | `handleListSkills` | In-process skill registry, sorted by name; each skill has `kind` (`builtin`, `markdown`, `remote`) |
| `POST` | `/api/skills/validate` | `handleSkillValidate` | Parse a markdown skill without saving it |
| `POST` | `/api/skills/source` | `handleSkillSave` | Validate, write atomically, hot-register. Gated by `PLAYGROUND_DISABLED` |
| `GET` | `/api/skills/{name}/source` | `handleSkillSource` | A markdown skill's file (404 otherwise) |
| `DELETE` | `/api/skills/{name}` | `handleSkillDelete` | Markdown skills only. Gated by `PLAYGROUND_DISABLED` |
| `POST` | `/api/skills/{name}/invoke` | `handleSkillInvoke` | **Skill playground** — direct skill execution, no agent loop. Gated by `PLAYGROUND_DISABLED`. |
| `GET` | `/api/tools` | `handleListTools` | Built-in tool schemas |
| `GET` | `/api/invocations` | `handleListInvocations` | Audit log — filter by skill, session, errors; paginated |
| `GET` | `/api/invocations/{id}` | `handleGetInvocation` | Single audit record |
| `POST` | `/api/invocations/{id}/replay` | `handleReplayInvocation` | Re-run skill with same args; writes a new `replay` row. Gated by `PLAYGROUND_DISABLED` and restricted to `kind=skill` rows |
| `POST` | `/api/scheduler/tasks` | `handleSchedulerCreate` | In-process scheduler store; the whole spec is validated up front by `scheduler.ValidateSpec` (cron, time zone, skill-or-prompt, retry bounds — 400 on any error) |
| `GET` | `/api/scheduler/tasks` | `handleSchedulerList` | In-process scheduler store |
| `GET` | `/api/scheduler/tasks/{id}` | `handleSchedulerGet` | In-process scheduler store |
| `POST` | `/api/scheduler/tasks/{id}/enable` | `handleSchedulerEnable` | In-process scheduler store; 404 when the task is gone |
| `POST` | `/api/scheduler/tasks/{id}/disable` | `handleSchedulerDisable` | In-process scheduler store; 404 when the task is gone |
| `DELETE` | `/api/scheduler/tasks/{id}` | `handleSchedulerDelete` | In-process scheduler store; 404 when the task is gone |
| `POST` | `/api/scheduler/tasks/{id}/run` | `handleSchedulerRun` | Run now through `Deps.Runner` (synchronous, bound to the request); 503 without a runner, 404 when absent |
| `GET` | `/api/scheduler/tasks/{id}/runs` | `handleSchedulerRuns` | Run history, newest first; `limit` (default 50) |

The gateway router serves `/health`, `/api/*`, and (unless disabled) `/mcp`.
The gateway binary contains no static-file serving and no UI assets; all
browser UI traffic is served by the separate nginx **frontend** service, which
proxies `/api/*` and `/health` back to the gateway.

#### SSE streaming

`handleQueryStream` uses `http.Flusher` to push events as they arrive from
the agent's `RunStream()` channel:

```
event: thinking
data: {"step":1}

event: tool_call
data: {"step":1,"tool":"weather","args":{"city":"London"},"why":"user asked about weather"}

event: tool_result
data: {"step":1,"tool":"weather","result":"{...}","elapsed_s":0.34}

event: final
data: {"session_id":42,"answer":"It's 15°C and cloudy in London.","meta":{"step_count":1,"model":"llama3:latest"}}
```

### 3.8 Telegram bot (`internal/telegram`)

#### Auth store (`auth.go`)

SQLite-backed allowlist (on by default):

```sql
CREATE TABLE IF NOT EXISTS users (
    telegram_id   INTEGER PRIMARY KEY,
    created_at    TEXT NOT NULL,
    last_activity TEXT NOT NULL
);
```

Controlled by `BOT_AUTH_ENABLED`, `BOT_ADMIN_ID` and `BOT_AUTH_DB`.  Auth
**fails closed**: unset `BOT_AUTH_ENABLED` means enabled, and `AuthFromEnv`
returns an error (the bot refuses to start) when `BOT_ADMIN_ID` is missing,
unless `BOT_AUTH_ENABLED=false` is set explicitly.  Authorization keys on the
sender's user ID (`msg.From.ID`), not the chat ID, so allowlisting a group
chat does not authorize its members; unauthorized senders get an "Access
denied" reply.

#### Agent client (`agent_client.go`)

| Method | Endpoint | Behaviour |
|---|---|---|
| `Query(text, chatID, sessionID)` | `POST /api/query` | Synchronous; returns answer + session ID |
| `QueryStream(text, chatID, sessionID, out chan<- Event)` | `POST /api/query/stream` | Parses SSE via `bufio.Scanner` (64 KB initial buffer, events up to 32 MiB for image attachments), emits `Event` structs |

Both send `Authorization: Bearer $GATEWAY_API_TOKEN` when that variable is set;
the bot's log writer redacts the Telegram and gateway tokens.

#### Bot (`bot.go`)

Long-polling update handler.

| Command | Handler | Description |
|---|---|---|
| `/start` | `handleStart` | Show help text (and the sender's ID while auth is on); resets the chat's session only for an authorized sender — in a group the session is shared, and any member could otherwise wipe it |
| `/ask <query>` | `handleAsk` | Explicit query (shows model/steps in footer) |
| `/new` | `handleNew` | Start fresh session |
| `/skills` | `handleSkills` | List available skills |
| `/auth_add <id>` | `handleAuthAdd` | Admin: allowlist a Telegram user |
| `/auth_list` | `handleAuthList` | Admin: show allowlist |
| *(plain text)* | `handleText` | Treated as agent query |

**Progressive status updates**: when streaming, the bot edits its status
message every 1500 ms with the current step.  Falls back to synchronous
`/api/query` only if the stream fails before delivering any event; a stream
that breaks after the agent has started is reported as interrupted rather than
re-run (which would duplicate tool calls and side effects).

**Memory scope**: `BOT_MEMORY_SCOPE` (`Bot.SetMemoryScope`) maps each query to
a memory namespace sent as `namespace` in the gateway request —
`shared` (none), `user` (`telegram:user:<sender id>`) or `chat`
(`telegram:chat:<chat id>`).

**Concurrency**: at most 8 updates are handled at once (`maxConcurrentHandlers`),
and a per-chat lock serializes messages within one chat.

**Message length**: answers are split by `splitForTelegram` into chunks of at
most `telegramMaxLen` (4000) **UTF-16 code units** — Telegram's unit, in which
a non-BMP character such as most emoji counts twice — preferring line breaks
and never splitting a rune.  Counting runes let 3000 emoji (6000 units) through
as one chunk; Telegram answered 400 and the user was left with the stale
status message.  No `ParseMode` is set, so replies are plain text (the `/ask`
footer is `model: … | steps: N`, not Markdown).

### 3.9 Observability (`internal/observability`)

```go
type Timer struct { ... }

func NewTimer() *Timer
func (t *Timer) Mark(name string)          // record time since last Mark
func (t *Timer) AsMap() map[string]float64  // {name: seconds, total_s: total}
```

Timings surface in `RunResult.Meta` and in the streaming `final` event.

### 3.10 Skill invocation audit log (`internal/memory/audit.go`, `internal/httpapi/audit.go`)

Every skill and tool execution during an agent run is recorded in
`skill_invocations` (co-located in `agent_memory.sqlite3`).  The audit system
has three layers:

#### Storage layer (`memory/audit.go`)

`AuditRecord` carries the full context of one invocation:

```go
type AuditRecord struct {
    ID                  int64
    SessionID, Step     int
    TS                  float64        // unix seconds
    Skill, Kind, Why    string
    Args, Result        any            // JSON-deserialised blobs
    ErrorMsg            string
    DurationMS          int64
    ReplayedFrom        *int64         // non-nil for replay rows
}
```

`AuditFilter` drives filtered queries: `SessionID`, `Skill`, `Since`, `Until`,
`OnlyErrors`, `Limit`, `Offset`.

#### Agent integration (`agent/agent.go`)

The audit write lives in `runOne`, the single execution path shared by the
single `tool_call`, the `tool_calls` batch, and parallel execution — so every
invocation is recorded exactly once regardless of how it was dispatched:

```
runOne(state, batchCall{name, args, why, step})
  1. classify kind: skill / tool / unknown
  2. safeExecute(entry.Execute, args) — a panic becomes a tool error; unknown
     tool names get an error result listing what does exist
  3. measure elapsed time (the caller updates state.timings + state.debug)
  4. extract binary attachments (qr_generate images, etc.)
  5. redact args + result for sensitive skills (password_gen, shell_command,
     ssh_command, jwt_inspect)
  6. _ = s.Memory.AddInvocation(rec)   // best-effort — never aborts the agent loop
  7. return batchResult{result, elapsed, attachments}
```

#### HTTP layer (`httpapi/audit.go`)

| Endpoint | Behaviour |
|---|---|
| `GET /api/invocations` | Paginated list with `skill`, `session_id`, `only_errors`, `since`, `until`, `limit`, `offset` query params |
| `GET /api/invocations/{id}` | Full record including untruncated blobs (up to `AUDIT_MAX_BLOB_BYTES`) |
| `POST /api/invocations/{id}/replay` | Re-executes the skill via `skills.Client.Execute()` with the original args; writes a new `kind="replay"` row with `replayed_from` set; returns `{replayed_from, skill, result, duration_ms, error}` |

Replay is gated by the same env flags as the underlying skill — `shell_command`
and `ssh_command` remain disabled unless `SKILL_SHELL_ENABLED` / `SKILL_SSH_ENABLED`
are set.  Replay is not available for built-in agent tools (`vector_add`,
`vector_search`) since they are not in the `skills.Client` registry.

### 3.11 Web UI (`web/`)

A single-page application built with **Vite + React 18 + TypeScript + Tailwind CSS**.

#### Technology stack

| Concern | Library |
|---|---|
| Routing | `react-router-dom` v6 (basename `/ui`) |
| Server state | `@tanstack/react-query` v5 |
| Client state | `zustand` v5 |
| Streaming | Native `fetch` + `ReadableStream` SSE parser |
| Markdown | `react-markdown` + `remark-gfm` |
| Icons | `lucide-react` |
| Toasts | `sonner` |
| Styling | Tailwind CSS v3, custom dark palette |

#### Pages

| Route | Page | Key behaviour |
|---|---|---|
| `/ui/` | Chat | Streaming chat; SSE events rendered as tool-call cards; session sidebar with debounced search (`?q=`), inline rename (PATCH) and delete; auto-scroll; abort button |
| `/ui/memory` | Memory | Semantic search (configurable k); manual add with namespace and expiry; browse-all list with a namespace filter (`/api/memory/namespaces`); edit modal (`PUT`); delete; JSONL export (authenticated fetch → Blob download) and import |
| `/ui/skills` | Skills | Collapsible skill cards with full parameter schemas and a `kind` badge; **inline playground panel** with auto-generated form, three-tab result view (Pretty / Raw / Timing), and a "Copy as `tool_call` JSON" button; markdown **skill editor** (`SkillEditor.tsx`) with a 400 ms debounced `/api/skills/validate` pane, Save disabled until valid |
| `/ui/scheduler` | Scheduler | Task list with enable/disable toggle, delete, **Run now** and a run-history expander (`TaskRunHistory.tsx`); shows time zone, agent badge, retry settings and a pending retry; create modal (`TaskCreateModal.tsx`) for skill or agent tasks with time zone, retries, one-shot and Telegram chat, and a "Next:" preview (`lib/cron.ts`) evaluated in the chosen zone with the server's rules (§3.6), shown as local time + zone time; 30 s auto-refresh |
| `/ui/activity` | Activity | Skill invocation audit log; filter by skill/session/errors; JSON drawer; Replay button; 10 s auto-refresh |
| `/ui/health` | Health | Provider/model status card (30 s auto-refresh) and a usage panel (`UsagePanel.tsx`, hand-drawn SVG — no chart library) over `/api/stats/usage` |

#### SSE stream protocol

The web client uses `fetch` (not `EventSource`) so the query can be a POST
request with a JSON body.  The stream parser:

```
fetch POST /api/query/stream
  → ReadableStream of bytes
    → TextDecoder, buffered line accumulator
      → split on \n\n boundaries
        → extract "event:" and "data:" lines
          → JSON.parse(data) → dispatch to Zustand store
```

Stream events update Zustand state which React components observe:
- `thinking` → add a `StreamStep` slot
- `tool_call` → fill the slot's `toolCall` field
- `tool_result` → fill the slot's `toolResult` field
- `final` → set `finalData`, trigger history refetch, refresh session list
- `error` → set `errorMsg`, show toast, and put the question back in the input
  box for a resend.  The gateway's payload is `{"error": "..."}`; errors raised
  inside `stream.ts` itself (401, network failure) use `{"message": "..."}`, and
  the handler reads either.  Reading only `.message` used to make every gateway
  error `undefined`: the error box stayed hidden and the question vanished.

#### API token

When the gateway has `GATEWAY_API_TOKEN` set, the UI's first request gets a
401; `api/auth.ts` then prompts for the token (`window.prompt`), stores it in
`localStorage` for that browser, and sends `Authorization: Bearer <token>` on
every REST and SSE request from then on.

#### Gateway / UI separation

The gateway binary contains no UI assets and no static-file serving.  The
React app is built and served exclusively by the nginx frontend container.
For local UI development without Docker, run `make dev-web` (Vite dev
server at `:5173/ui/`, proxying `/api` to the gateway on `:8000`). The proxy
keeps the browser's Host (`changeOrigin: false`): with it rewritten to
`:8000` while Origin says `:5173`, the origin guard rejected every write.

### 3.12 Bootstrap (`internal/bootstrap`)

A single `bootstrap.New(cfg)` function constructs every runtime service
(LLM chat, memory, skills, scheduler store, scheduler runner, native
skills, agent service) and returns a `*Services` struct plus a cleanup
closure.  Both `cmd/gateway` and `cmd/cli` (in local mode) call it, so
service wiring lives in exactly one place.

```go
type Services struct {
    Chat         llm.ChatClient
    Memory       *memory.Client
    Skills       *skills.Client
    Sched        *scheduler.Store
    Runner       *scheduler.Runner // built but not started; cmd/gateway calls Runner.Start()
    Agent        *agent.Service
    NativeSkills map[string]agent.Skill
    ToolProvider mcp.ToolProvider  // registry + native skills, backs the /mcp server
}

func New(cfg config.Config) (*Services, func(), error)
```

This keeps `cmd/gateway/main.go` a thin wrapper and lets the CLI reuse the gateway's exact construction without duplication.

`New` also starts the MCP connection manager (`startMCPServers`, waiting at
most `MCP_CONNECT_WAIT_SEC`; cleanup cancels it) and wires the runner's agent
(`Runner.SetAgent`) for prompt tasks. The gateway additionally starts the
memory janitor (`Memory.RunJanitor`, hourly) and hands `svc.Runner` to the
router for run-now.

### 3.13 CLI (`cmd/cli` — `omega` binary)

A single static Go binary that exposes the agent's full surface without
requiring a running HTTP gateway.  Built with stdlib only (no cobra) using
manual subcommand dispatch + `flag.FlagSet` per subcommand.

Every subcommand parses through `parseArgs`, which accepts flags on either
side of the positionals (a literal `--` still ends flag parsing).  Go's `flag`
package stops at the first non-flag, so the documented `omega ask "q"
--session 7` and `omega skills run NAME --arg k=v` used to drop every flag
after the positional — the skill ran with empty args, the session was lost —
and still exit 0.

```
omega <ask|skills|memory|schedule|session> [flags] [args]
```

#### Operating modes

The CLI checks the `OMEGA_REMOTE` env var to choose between two modes:

| Mode | When | How |
|---|---|---|
| **local** (default) | `OMEGA_REMOTE` unset | `bootstrap.New(cfg)` constructs all services in-process — same wiring as the gateway. The `Skills`, `Memory`, `Sched`, and `Agent` services are then called directly. |
| **remote** | `OMEGA_REMOTE=http://host:port` set | Each subcommand makes JSON HTTP calls to the gateway's REST endpoints, sending `Authorization: Bearer $GATEWAY_API_TOKEN` when set. SSE streaming is parsed by `bufio.Scanner`. Agent calls (`/api/query*`, `/invoke`) get a 15-minute client timeout (`agentRunTimeout`, the bot's budget) — `http.Client.Timeout` covers the body too, so the old 120 s / 300 s cut off long runs and the disconnect cancelled them on the gateway; everything else keeps 120 s. Path segments (skill name, memory ID) are escaped. |

Subcommands depend only on the typed wrappers in `cmd/cli/client.go`
(`listSkills()`, `invokeSkill()`, `searchMemory()`, etc.); the wrappers
internally branch on `isRemote()` to pick the right path.

#### Subcommands

| Command | Purpose |
|---|---|
| `omega version` | Print the release version (`internal/version`). |
| `omega ask [--stream] [--session N] [--max-steps N] [--namespace NS] "..."` | Run an agent query.  With `--stream` and a TTY stdout, renders thinking / tool_call / tool_result events with ANSI color; falls back to plain text when stdout is piped.  The step budget is `--max-steps`, else `AGENT_MAX_STEPS`, else 25 (`resolveMaxSteps`); local mode used to hardcode 25 and ignore the env var every other entry point honours. |
| `omega skills list` | List all registered skills with signatures. |
| `omega skills describe NAME` | Show one skill's full schema. |
| `omega skills run NAME --arg k=v` | Invoke a skill directly (same execution path as the playground endpoint) and print its result as JSON. A failed or unknown skill exits 1 in both modes — remote mode used to print the playground's `null` result and exit 0. |
| `omega memory search -k N "Q"` | Semantic search over vector memory. |
| `omega memory add --meta k=v "TEXT"` | Manually add a memory. |
| `omega memory list [-limit N] [-offset N]` | List stored memories newest-first. |
| `omega memory delete ID` | Delete a memory by ID (alias `rm`). |
| `omega memory edit ID [--text] [--meta k=v] [--ttl D]` | Edit in place (`--meta k=` removes a key; `--ttl 0` removes the expiry). |
| `omega memory namespaces` / `export [-o F] [--embeddings]` / `import F` | Namespace counts; JSON Lines export to stdout or a file; import from a file or `-` (stdin). `add`, `search` and `list` take `--namespace` (`search` repeatable; `""` = shared), `add` takes `--ttl`. |
| `omega schedule list / create / delete` | CRUD for scheduled cron tasks. `create` takes `--skill` + `--arg` or `--prompt`, plus `--tz`, `--retries`, `--retry-delay`, `--one-shot`. Local `create` validates with `scheduler.ValidateSpec` like the API does, and local `delete` of a missing ID is an error. |
| `omega schedule enable / disable / run / history ID` | Toggle a task, run it now (in-process in local mode; exits 1 when the run failed), or show its run history. |
| `omega session list` | List sessions with message counts. |
| `omega session export ID` | Dump a session's full message history as JSON (for piping to `jq`). |

`--json` is supported across most subcommands for machine-readable output.

---

## 4. Vector memory migration (ChromaDB → chromem-go)

If you are upgrading from the previous Python sidecar deployment, a one-shot
migration tool moves existing ChromaDB data into the new chromem-go format.
No local Python installation is required — both steps run inside Docker.

### 4.1 Migration architecture

```
 ┌──────────────────────────────────────────────┐
 │  migrate-export (Python 3 + chromadb 0.5.5)  │
 │  Reads data/vector_db/ → data/vector_db.jsonl│
 └──────────────────────────────────────────────┘
                         │ JSONL (one record per line)
                         ▼
 ┌──────────────────────────────────────────────┐
 │  migrate-import (static Go binary)           │
 │  Reads data/vector_db.jsonl → data/chromem/  │
 └──────────────────────────────────────────────┘
```

Embeddings are exported verbatim from ChromaDB — no re-embedding step, no
model-version drift.

### 4.2 Running the migration

```bash
make vector-migrate
```

Or manually:

```bash
# Step 1 — export ChromaDB → JSONL
docker compose -f docker-compose.migrate.yml run --rm --remove-orphans migrate-export

# Step 2 — import JSONL → chromem-go
docker compose -f docker-compose.migrate.yml run --rm --remove-orphans migrate-import

# Verify record count
wc -l data/vector_db.jsonl

# Clean up intermediate file (safe once migration is verified)
rm data/vector_db.jsonl
```

### 4.3 Export tool (`cmd/migrate-vector/export.py`)

Python 3.11 + `chromadb==0.5.5`.  Reads the legacy `data/vector_db/` persist
directory using paginated `col.get(limit=batch, offset=offset, include=[...])`.
Each record is written as one JSON line:

```json
{"id": "uuid", "document": "text", "embedding": [0.1, ...], "metadata": {...}}
```

Exits with code 1 if the number of written records does not match the
collection total (e.g. records without embeddings were skipped) and with code 2
if the collection does not exist.  An empty collection prints
`exporting 0 records` and writes an empty JSONL file (exit 0); the importer then
reports "source collection is empty — nothing to import".

### 4.4 Import tool (`cmd/migrate-vector/main.go`)

Static Go binary (CGO-free).  Parses the JSONL, validates embedding dimension
consistency, then calls `collection.AddDocuments()`.  Guard rails:

- **Refuses to overwrite** a non-empty `data/chromem/`.  Remove it and re-run.
- **Empty source** is a no-op — exits cleanly with "source collection is empty".
- **`--dry-run`** flag parses and validates without writing.

### 4.5 Data layout

```
data/
  vector_db/        ← legacy ChromaDB (read by exporter; keep as backup)
  vector_db.jsonl   ← intermediate handoff (safe to delete after migration)
  chromem/          ← new chromem-go database (used by gateway)
```

### 4.6 Dockerfile (`docker/migrate.Dockerfile`)

Three stages — only the relevant one runs per `docker compose run`:

| Stage name | Base | Purpose |
|---|---|---|
| `exporter` | `python:3.11-slim` | Python exporter |
| `go-builder` | `golang:1.25-bookworm` | Compiles the Go importer |
| `importer` | `gcr.io/distroless/static-debian12` | Runs the static binary |

---

## 5. Data persistence

```
{DATA_DIR}/
├── agent_memory.sqlite3       # HistoryStore: sessions + messages + skill_invocations (audit)
├── scheduler.sqlite3          # Scheduler task definitions + run state
├── telegram_auth.sqlite3      # Telegram bot allowlist (if auth enabled)
├── skills/                    # Dynamic markdown skill files (SKILLS_DIR)
│   └── *.md
└── chromem/                   # chromem-go vector database
    └── <hash>/                #   Collection directory (chromem-go names it by a hash of the collection name)
```

All SQLite databases are auto-created on first access (`os.MkdirAll` + auto
DDL).  In Docker, the gateway and telegram-bot share a bind-mounted `./data`
volume.  The frontend service is stateless and needs no volume.

---

## 6. Docker & deployment

### 6.1 Images

| Image | Base | Approx. size | Build stages |
|---|---|---|---|
| `frontend` | `nginx:1.27-alpine` | ~50 MB | `node:20-alpine` → nginx |
| `gateway` | `gcr.io/distroless/static-debian12` | ~16 MB | `golang:1.25-bookworm` → distroless |
| `telegram-bot` | `gcr.io/distroless/static-debian12` | ~12 MB | `golang:1.25-bookworm` → distroless |
| `migrate` (exporter stage) | `python:3.11-slim` | ~500 MB | migration only |
| `migrate` (importer stage) | `gcr.io/distroless/static-debian12` | ~10 MB | migration only |

Go binaries are built with `CGO_ENABLED=0 -trimpath -ldflags="-s -w"` for
minimal static binaries.  The `modernc.org/sqlite` driver is pure Go — no CGO
needed.

The frontend Dockerfile build sequence:

```
node:20-alpine
  COPY web/package.json web/package-lock.json → npm ci  (cached layer)
  COPY web/                                   → npm run build
  → /web/dist/{index.html,assets/}

nginx:1.27-alpine
  COPY /web/dist      → /usr/share/nginx/html/ui
  COPY nginx.conf     → /etc/nginx/conf.d/default.conf
```

### 6.2 Compose topology

```yaml
services:
  gateway:      # :8000, no deps
  frontend:     # :80,   depends_on gateway
  telegram-bot: # no port, depends_on gateway
```

Gateway and telegram-bot share a bind-mounted `./data` volume and run as the
host `${UID}:${GID}`; both ports are published on `127.0.0.1` only.  The
migration containers (`docker-compose.migrate.yml`) also run as the host
UID:GID — a `data/chromem` written by an earlier root-run migration may need
`sudo chown -R $(id -u):$(id -g) data/chromem`.
Frontend is stateless (no volume).  Build context for all three services is `.`
(the repo root) — no external paths required.

### 6.3 Networking

| Service | Listens on | Connects to |
|---|---|---|
| gateway | `:8000` | The chat provider (Ollama / OpenAI / DigitalOcean / opencode) and the embeddings provider, which `EMBED_PROVIDER` may point somewhere else |
| frontend | `:80` | gateway `:8000` (nginx proxy for `/api/*`, `/health`) |
| telegram-bot | — | gateway `:8000`, Telegram Bot API |

Within compose, services reference each other by name (`http://gateway:8000`).
For Ollama on the host machine,
`http://host.docker.internal:11434` is the recommended URL.

### 6.4 nginx configuration (`docker/nginx.conf`)

```nginx
location = / { return 302 /ui/; }    # bare root → the SPA

location /ui/ {
    try_files $uri /ui/index.html;   # SPA fallback for client-side routing
}

location /health {                   # the Health page fetches this by origin
    proxy_pass         http://gateway:8000;
}

location /api/ {
    proxy_pass         http://gateway:8000;
    proxy_buffering    off;           # required for SSE streaming
    proxy_cache        off;
    proxy_read_timeout 300s;          # allow long agent loops
}
```

`proxy_buffering off` is critical for SSE: without it nginx buffers the
response body and the browser receives no events until the connection closes.

**`/mcp` is deliberately not proxied.** The frontend serves the browser UI;
MCP clients connect to the gateway directly (`BACKEND_PORT`, published on
`127.0.0.1:8000` by the compose file), which keeps the tool-calling surface off
the port the UI is exposed on.

### 6.5 Local dev vs Docker difference

| Scenario | UI served by | API reached via |
|---|---|---|
| `make build` + `go run ./cmd/gateway` | (gateway is API-only — no UI) | Direct to `:8000/api/*` |
| `make dev-web` | Vite dev server at `:5173/ui/` | Vite proxy → `:8000` |
| `docker compose up --build` | nginx at `:80/ui/` | nginx proxy → `gateway:8000` |

For browser UI work without Docker, use `make dev-web`; for full-stack runs
use Docker Compose.

---

## 7. API reference

### 7.1 Gateway API (`/api/*`)

#### Agent

```
POST /api/query
{
  "query": "What's the weather in London?",
  "session_id": 0,          // 0 = create new session
  "remember": true,         // accepted for API compatibility; currently a no-op
  "max_steps": 10,
  "telegram_chat_id": null
}
→ {
  "session_id": 42,
  "answer": "It's 15°C and cloudy in London.",
  "meta": {"step_count": 1, "model": "llama3:latest", "timings": {...}},
  "memories": [...],
  "debug_log": "...",
  "attachments": [...]      // omitted unless a skill produced one (qr_generate)
}
```

`meta` also carries `fallback: true` when the answer is a recovery rather than
a real model answer, and `max_steps_hit: true` when the loop ran out of steps.

```
POST /api/query/stream
(same request body)
→ SSE stream of event: thinking | tool_call | tool_result | final | error
```

An `error` event means the run could not proceed at all (session creation or LLM
transport failure). A model reply the parser cannot make sense of is **not** an
error: it arrives as a normal `final` event with `meta.fallback = true`, matching
what `POST /api/query` returns for the same reply.

`max_steps` is clamped to `maxStepsHardLimit` (100); `0` or a negative value
falls back to `AGENT_MAX_STEPS`.

#### Sessions & history

```
POST /api/sessions/new           → {"session_id": 43}
GET  /api/sessions?limit=50      → {"sessions": [{id, created_at, message_count}]}
GET  /api/sessions/42/messages?limit=200&offset=0
                                 → {"session_id": 42, "messages": [{id, session_id, ts, role, content}]}
```

Both `limit` parameters are page sizes: a zero, negative or unparseable value
falls back to the endpoint default (50 sessions, 200 messages) rather than
reaching SQLite as `LIMIT 0` (empty result) or `LIMIT -1` (no limit at all).
Note the deliberate difference from `GET /api/memory`, where `limit=0` means
"every memory" — that endpoint paginates in memory, not in SQL.

#### Vector memory

```
POST /api/memory/add    {"text": "...", "meta": {}}
                      → {"ok": true, "memory_id": "uuid", "skipped": false}

POST /api/memory/search {"query": "...", "k": 5}
                      → {"ok": true, "hits": [{id, text, metadata, distance}]}

GET  /api/memory?limit=100&offset=0
                      → {"ok": true, "hits": [{id, text, metadata}], "total": 12, "limit": 100, "offset": 0}
GET  /api/memory/{id} → {"ok": true, "hit": {id, text, metadata}}   (404 if absent)
DELETE /api/memory/{id} → {"ok": true, "deleted": "id"}              (404 if absent)
```

`GET /api/memory` enumerates the collection newest-first by `created_at`
metadata. chromem-go exposes no list-all primitive, so the store embeds a
constant probe and issues `QueryEmbedding(nResults = Count)` to retrieve every
document, then sorts and paginates in memory. `DELETE` also evicts the in-memory
SHA256 dedup entry so identical text can be re-added afterwards.

#### Skills

```
GET  /api/skills                     → {"skills": [{name, description, parameters, body}]}
GET  /api/tools                      → {"tools": [{name, description, parameters}]}

POST /api/skills/{name}/invoke       — skill playground; bypasses the agent loop
{"args": {"city": "Berlin"}}
→ {
    "name":      "weather",
    "args":      {"city": "Berlin"},
    "result":    {...}        // raw tool result (or null on error)
    "elapsed_s": 0.412,
    "error":     null,        // string when execution fails
    "attachments": [...]      // images extracted via ExtractAttachments (or null)
  }
```

The invoke endpoint is gated by `PLAYGROUND_DISABLED` (default off — playground
enabled).  It runs the same `skills.Execute()` path as the agent loop and
applies the same `agent.ExtractAttachments()` post-processing so binary
artifacts like QR-code images come back as base64 attachments instead of
inflating the response payload.

#### Scheduler

```
POST   /api/scheduler/tasks          create task
GET    /api/scheduler/tasks          list all
GET    /api/scheduler/tasks/{id}     get one
POST   /api/scheduler/tasks/{id}/enable
POST   /api/scheduler/tasks/{id}/disable
DELETE /api/scheduler/tasks/{id}     remove
```

#### Invocations (audit log)

```
GET /api/invocations
    ?skill=weather&session_id=3&only_errors=true&limit=50&offset=0
  → {
      "invocations": [
        {
          "id": 42, "session_id": 3, "step": 2, "ts": 1747390000.1,
          "skill": "weather", "kind": "skill",
          "args": {"city": "Berlin"},
          "result": {"temperature": 18, ...},
          "duration_ms": 312,
          "why": "User asked for current weather in Berlin"
        }
      ],
      "total": 87, "limit": 50, "offset": 0
    }

GET  /api/invocations/{id}          → full AuditRecord
POST /api/invocations/{id}/replay   → {"replayed_from":42,"skill":"weather","result":{...},"duration_ms":298,"error":""}
```

#### Health

```
GET /health → {"ok": true, "version": "0.6.0", "provider": "ollama",
               "chat_base": "http://127.0.0.1:11434", "chat_model": "llama3:latest",
               "skills_dir": "/app/data/skills", "scheduler_db": "...",
               "embed_model": "nomic-embed-text", "embed_provider": "ollama",
               "embed_ok": true, "embed_error": ""}
```

`ok` mirrors `embed_ok`: it is `false` when the embeddings backend (vector
memory) is unreachable, so `/health` doubles as an embeddings readiness probe.
The embed probe is cached for 30 s per memory client and bounded by a 5 s
timeout, so a slow backend cannot make `/health` hang and polling it does not
hammer the embeddings endpoint.  `/health` is never behind `GATEWAY_API_TOKEN`.

#### MCP (Model Context Protocol)

The gateway is both an MCP **server** and an MCP **client**, implemented as a
hand-rolled JSON-RPC 2.0 layer (`internal/mcp`) with no SDK dependency.

Server — single endpoint, "streamable HTTP" style (one JSON request → one JSON
response; notifications get `202 Accepted` with no body).  A notification is a
message with **no `id` member**, whatever its method — `initialize`, `ping` and
`tools/list` notifications used to be answered — while `"id": null` is an
ordinary request (it used to be treated as a notification, so a `tools/call`
with `id: null` was silently dropped).  Malformed `tools/call` params answer
`-32602 Invalid params`; batch arrays are not supported:

```
POST /mcp
  initialize                  → {protocolVersion, capabilities:{tools:{listChanged:false}}, serverInfo}
  tools/list                  → {tools:[{name, description, inputSchema}]}
  tools/call {name,arguments} → {content:[{type:"text",text}], isError}
  ping                        → {}
  notifications/initialized   → (202, no body)
```

Every registered skill (built-in, dynamic markdown, native `schedule_task` /
`web_search`, and any tools consumed from remote MCP servers) is advertised as
an MCP tool; its parameter map is rendered as a JSON-Schema `inputSchema`. Skill
execution errors are returned in-band as `isError:true` results (not JSON-RPC
errors) so a calling model can read and react to them. `tools/list` is sorted by
name — the registry list already was, but the native tools came straight off a
map, so connected clients saw the tail of the list reshuffle on every call. The
endpoint is gated by `MCP_SERVER_DISABLED`.

Client — `MCP_SERVERS` is a comma-separated list of `name=url` entries (append
`|Header: Value` for a single auth header). At startup the gateway dials each
server (`initialize` + `tools/list`), then registers every remote tool into the
local skill registry namespaced as `<name>_<tool>`.  `skills.Client.Register`
refuses a name that is already taken (built-in, markdown, or an earlier
server's tool) and `registerRemoteTool` refuses the native tool names
(`schedule_task`, `web_search`, `vector_add`, `vector_search`) the registry
cannot see; the tool is skipped and logged.  The remote server picks the tool
half of the name, so before this a server named `http` offering `request`
replaced the built-in `http_request` — and with it the private-network and
enable gates the built-in enforces.  A remote `inputSchema` property whose
`type` is a union (`["string","null"]`, as Pydantic and Zod emit for optional
fields) maps to its first non-null type; a single such property used to fail
the whole schema decode, registering the tool with no parameters at all.
The connection manager (`bootstrap/mcpclients.go`, one goroutine per server)
dials all servers in parallel and `bootstrap.New` waits at most
`MCP_CONNECT_WAIT_SEC` for the first attempts; a server that failed retries
with backoff (`mcpBackoffMin` 5 s doubling to 5 min) until it answers. Each
connected server is re-listed every `MCP_REFRESH_SEC` and whenever the client
sees `notifications/tools/list_changed` in an SSE response
(`Client.ListChanged()`): tools are registered, replaced when their
definition changed, and withdrawn (`skills.Client.Unregister`, which refuses
built-ins and markdown skills) when gone. A failed refresh keeps the current
tools. Cleanup cancels the manager.  Responses are accepted as
either `application/json` or `text/event-stream`; in the SSE case the client
skips server notifications and requests interleaved before the matching
response. When a server answers 404 to a request that carried a session id
(the remote restarted and forgot it), the client re-initializes once and
retries. Remote tool descriptions are flattened to one line and capped at 300
characters before they enter the agent's system prompt. A server that
is unreachable at startup is logged and skipped — it never blocks boot. Once
registered, remote tools are reachable from the agent loop, the scheduler, the
skill playground, and the gateway's own MCP server.

Because remote tools become ordinary skills, one `mcp.Client` can be driven from
several goroutines at once — a parallel tool batch calling two tools from the
same server. Its JSON-RPC request counter and the server-assigned session id are
therefore mutex-guarded (`takeID`, `getSessionID`, `setSessionID`); previously
both were read and written unsynchronised, which the race detector flags and
which could hand two in-flight requests the same JSON-RPC id.

---

## 8. Key design decisions

### Why Go for everything?

- **Compiled binaries** — single ~14 MB static binary (`-trimpath -ldflags="-s -w"`), no runtime deps.
- **Goroutine-based concurrency** — scheduler runner, SSE streaming, and
  request handling are natural goroutine workloads.
- **SQLite via pure Go** — `modernc.org/sqlite` needs no CGO, simplifying
  cross-compilation and distroless deployment.
- **Fast startup** — sub-second cold start vs Python's multi-second import chain.
- **Single deploy unit** — one binary contains the gateway, all 51 built-in skills,
  vector memory, history, and embeddings.  No sidecar to keep in sync.

### Why chromem-go instead of ChromaDB?

- **No sidecar process** — chromem-go is an in-process library; no separate
  HTTP server to manage, version, or restart.
- **Pure Go** — no Python, no CGO, compiles into the gateway binary.
- **Cosine similarity** — same distance metric as ChromaDB; migration preserves
  embeddings verbatim so semantic neighborhoods are identical post-migration.
- **Persistent** — `NewPersistentDB` writes a directory of files; survives
  container restarts.

### Why in-process skills (not HTTP sidecar)?

- **Latency** — in-process calls are µs, not ms.  On a multi-step agent loop
  with 10+ tool calls the difference is noticeable.
- **No networking surprises** — skills can't fail due to connection refused,
  timeouts, or sidecar OOM.
- **Single binary** — `docker build` produces one artifact; no multi-service
  versioning skew.
- **Hot-registration still works** — `skill_creator` writes `.md` files and
  calls `reg.registerMarkdown()` on the live in-process registry; skills are available
  immediately without a restart.

### Why `schedule_task` is native Go

The scheduler store (SQLite) lives on the Go side.  Making the skill call
back into Go via an HTTP round-trip would create a circular dependency
(gateway → HTTP → gateway).  `ScheduleTaskSkill` operates directly on
the `Store` struct.

### Why the frontend is a separate nginx service (not embedded in the gateway)

- **Separation of concerns** — the gateway binary focuses on API logic; nginx
  focuses on static file serving and reverse proxying.
- **Independent rebuild** — changing the UI does not require recompiling Go.
  `docker compose build frontend` is enough.
- **Better static serving** — nginx handles gzip, caching headers, and
  concurrent file serving more efficiently than Go's `http.FileServer`.
- **SSE buffering control** — nginx's `proxy_buffering off` directive gives
  fine-grained control over streaming behaviour that is otherwise hard to
  achieve through a Go reverse proxy.
- **Single origin for the browser** — all traffic (static files + API) goes
  through the same nginx host:port, eliminating CORS entirely.

The gateway binary contains no UI assets at all — the nginx frontend is
the only path that serves the UI in any deployment.

### Why distroless runtime images

- **Minimal attack surface** — no shell, no package manager, no libc.
- **Small images** — ~2 MB base layer + the Go binary.
- **Immutable** — nothing to patch at runtime; rebuild from source to update.

---

## 9. Error handling & resilience

| Scenario | Behaviour |
|---|---|
| LLM returns invalid JSON | Agent parser tries several recovery strategies (balanced-brace embedded-JSON scan, `raw_model_json` unwrap, `normalizeToolCall`); on unrecoverable failure both `Run` and `RunStream` return the same graceful "please rephrase" fallback answer with `meta.fallback = true`. |
| LLM calls unknown tool | Agent returns an error message listing valid tools; LLM can retry. |
| Skill execution fails | Executor returns `(nil, err)`; agent loop wraps it as `{error: "..."}` and appends as tool result; LLM can retry or answer. |
| Embeddings unreachable | `addText` and `searchMemory` return errors surfaced in the agent loop; agent continues without memory injection. |
| Model emits an unusable URL | Skills honour the `http.NewRequest` error (it returns a **nil** request alongside it) and report it as a skill error. Dereferencing that nil used to panic — fatal for the process when it happened on a parallel batch's goroutine. |
| A skill panics | `safeExecute` recovers it inside `runOne` and hands the model `{"error": "tool \"x\" crashed: ..."}`, exactly like a returned error; the sibling calls in the same batch still finish. This is not optional politeness: `executeBatch` runs each call on its own goroutine and the gateway starts `RunStream` on one too, and chi's `Recoverer` only wraps the handler goroutine — a panic anywhere else killed the whole gateway. The streaming handler and the auto-memory goroutine carry their own recover as a second net. |
| Oversized `tool_calls` batch | `capBatch` keeps the first `AGENT_MAX_PARALLEL` calls, executes only those, and tells the model how many it dropped. |
| A scheduled skill panics | `Runner.safeExec` records it as that task's error in `last_result` and the tick continues. Only the tick's own recover stood behind this before, and it fired after unwinding `runTask` — losing the result *and* every task still queued for that minute. |
| Scheduler task fails | Runner captures the error, stores it in `last_result`, optionally notifies via Telegram. |
| Scheduler tick drifts or the host sleeps | The next tick sweeps every whole minute since the last one it evaluated (bounded to 60), so a cron minute skipped by ticker drift still fires — once, not once per missed minute. |
| Non-UTF-8 tool output | Truncation helpers trim only a trailing partial rune (and, on the Telegram path, drop stray invalid bytes). Trimming until the whole prefix validated returned an empty string, silently discarding binary-ish results from debug logs, audit previews and `last_result`. |
| `final` envelope with no answer | `finalAnswer` falls back to the same lenient extraction the malformed-envelope path uses (`answer` → `text` → `result` → the "please rephrase" message), so a missing or empty `answer` never reaches the user as a blank reply. |
| Telegram stream breaks | Before any event arrived: the bot falls back to synchronous `/api/query`. After the agent has started: it reports the interruption instead of re-running the query. |
| Client disconnects mid-run | The run's context is cancelled: the loop stops, an LLM call (or its retry backoff) returns at once, and so does a running tool — every skill that waits on the network or a process takes the run's context (`agent.Skill.Execute(ctx, args)`, `skills.Client.ExecuteContext`), down to nested pipeline steps and the shell process group. |
| Vector delete races a search or list | Both sides of chromem's `k ≤ Count()` check run under `vectorStore.shrink`, so the query never asks for more documents than exist. |
| A dotted `Host` reaches a token-less gateway | `hostGuard` answers 403 (DNS-rebinding defence); add the name to `GATEWAY_ALLOWED_HOSTS` or set a token. |
| Remote MCP server unreachable at boot | Startup waits at most `MCP_CONNECT_WAIT_SEC` for all servers together (they connect in parallel); the unreachable one keeps retrying in the background with backoff and registers its tools when it comes up. |
| A scheduled run fails | With `max_retries`, a retry is persisted (`retry_attempt`, `next_retry_at`) and claimed atomically by a later tick, `retry_delay_sec` × 2^attempt later; the Telegram notification waits for the final outcome. |
| The backend rejects native tools | In `AGENT_TOOL_MODE=auto` the process latches to the JSON envelope (one log line) and the same step is retried that way; `native` mode surfaces the error. |
| An SSH host key changes | `ssh_command` refuses to connect (possible man-in-the-middle) unless `SKILL_SSH_HOST_KEY_POLICY=insecure`. |
| System clock steps backwards | Tasks whose `last_run_at` is now in the future are not claimed until wall time passes it; nothing detects the step. |
| MaxSteps exceeded | Agent returns "could not finish" with partial debug log. |
| SQLite directory missing | `os.MkdirAll` creates it before opening the database. |
| Browser aborts SSE | `fetch` AbortController cancels the request; gateway detects `r.Context().Done()` and stops writing. |
| chromem-go dir missing | `os.MkdirAll` + `NewPersistentDB` creates the directory and initialises the collection on first run. |

---

## 10. Testing

Two suites, answering different questions:

| Suite | Command | What it proves |
|---|---|---|
| Unit / integration | `./run_tests.sh` (`-race`), `make test` (in `Dockerfile.test`) | Every package in isolation, with real SQLite and chromem stores in `t.TempDir()` |
| End-to-end | `make e2e` (`scripts/e2e.sh`) | A **running gateway**, over HTTP, from outside the process |

CI (`.github/workflows/ci.yml`) runs both on every push and pull request,
plus gofmt, `go vet` (also with `-tags e2e`), staticcheck and the web build
(`npm run build`, i.e. `tsc -b` — plain `tsc --noEmit` checks nothing here).
The bwrap sandbox tests skip themselves where the runner forbids unprivileged
user namespaces.

Coverage per package is listed in the README.

**No test touches the network.** The seams that make that possible:

| Dependency | How tests replace it |
|---|---|
| LLM providers | `httptest` servers; `OpenAIChat.sleep` is injected so the retry backoff is asserted, not waited out (one test leaves it nil and cancels during the real backoff), and `OPENCODE_SESSION_ID` pins the relay's session header so tests can assert it rides every attempt |
| Embeddings | `mockEmbeddings` derives a deterministic vector from the text, so dedup distances are reproducible; `errEmbeddings` covers the backend-down paths |
| The model, in agent/gateway tests | a scripted `ChatClient` that replays canned envelopes and records the message list it was handed; `nativeLLM` (agent) additionally implements `ToolChatClient` and can refuse tools to drive the auto-mode fallback |
| Embeddings over HTTP, in gateway tests | `embedServer` — an `/api/embed` stand-in returning a hashed bag-of-words vector, for the namespace / edit / export routes |
| Remote MCP servers, resilience | `fakeMCP` (bootstrap) whose latency, availability and tool list change mid-test; `mcpBackoffMin` is shortened so retries happen in milliseconds |
| bubblewrap | the real binary when the host allows user namespaces (else skipped); `bwrapPath` simulates a missing one |
| SSH host keys | `hostKeyCallback` is exercised directly with generated ed25519 keys — no SSH server needed |
| Third-party APIs of the newer skills | `rdapBase`, `wikipediaBase`, `ecbURL`, `githubAPI`, `telegramAPIBase` point at `httptest` servers |
| DNS for `dns_compare` / `smtp_check` | in-test UDP DNS servers built with `x/net/dns/dnsmessage`; `lookupNS` / `lookupMX` seams |
| SMTP | in-test ESMTP servers (`textproto`): one records the `notify` envelope and data, one offers STARTTLS with a generated certificate |
| `traceroute`, `wake_on_lan`, `subnet_sweep` | real sockets on loopback (traceroute reads the real ICMP error queue) |
| PDFs | `minimalPDF` writes a valid multi-page PDF in the test |
| DuckDuckGo | an injected `http.RoundTripper` on `WebSearchSkill.client` (the URL is hardcoded, so the transport is the seam) |
| Telegram | `tgbotapi.NewBotAPIWithAPIEndpoint` pointed at an `httptest` server that answers `getMe` and records outgoing messages |
| Remote MCP servers | an `httptest` JSON-RPC server; one test also serves the single-event SSE variant |
| e2e native tool calls | `mockllm` serves a scripted `{"tool_calls":[…]}` reply as a native Ollama tool call when the request offered tools |
| DNS, for `email_auth` | the package-level `lookupTXT` seam is swapped for a table of canned TXT records, which also covers the SPF/DMARC/DKIM filtering |
| SQLite / chromem-go | real stores in `t.TempDir()` — they are pure Go and fast enough to use for real |

### End-to-end suite (`test/e2e`, `docker-compose.e2e.yml`)

`make e2e` builds the gateway from **`docker/gateway.Dockerfile` — the same
image definition production uses** — and runs it next to a mock model and the
compiled test binary on a compose network declared `internal: true`. The suite
imports none of the project's own packages: it only sees what a client sees, so
it covers the seams unit tests cannot reach.

| Piece | Role |
|---|---|
| `test/e2e/mockllm` | Stands in for Ollama. `POST /api/chat` serves replies from a queue the tests load through `POST /__control/script`; `GET /__control/requests` returns every chat request the gateway made, so a test can assert what the agent actually put in the model's context. Embeddings are a hashed bag of words — deterministic (so dedup is reproducible) and similarity-bearing (so `vector_search` returns something worth asserting on). |
| `gateway` | The real image, configured entirely through environment variables, writing to a throwaway volume. |
| `gateway-locked` | The same image with `PLAYGROUND_DISABLED` and `MCP_SERVER_DISABLED` set, which is how the suite proves those flags reach the router rather than just the config struct. |
| `runner` | `go test -tags e2e -c` compiled into a binary at image-build time, so the run phase needs neither the toolchain nor the module proxy. |

What only this suite can check: process boot from environment variables;
SQLite + chromem on a real filesystem, including surviving a restart
(`scripts/e2e.sh` restarts the gateway and re-runs
`TestPersistenceSurvivesRestart`); the scheduler goroutine actually firing a
task; SSE framing over a socket; an image attachment surviving the whole path
from `qr_generate` to the final event with the base64 kept *out* of the model's
context; the MCP endpoint as a separate protocol surface; audit redaction
for a credential-handling skill; and the DNS-rebinding `hostGuard` on the real
binary (a dotted `Host` is refused on reads and writes, the service name
`gateway` is served).

`TestNetworkIsolation` dials the public internet and requires it to fail — if
the stack is ever given a route out, the suite says so instead of quietly
becoming dependent on the outside world. The build phase is the only step that
needs a network.

`scripts/e2e.sh --host` runs the identical tests against locally built binaries
in a few seconds, for iterating on a test. It is not network-isolated, so
`TestNetworkIsolation` and the locked-gateway checks skip themselves; CI should
run the Docker mode.

**Verifying the web UI — use `npm run build` (or `make web`), not `tsc --noEmit`.**
`web/tsconfig.json` is a solution-style file: `"files": []` plus references to
`tsconfig.app.json` / `tsconfig.node.json`. A bare `tsc --noEmit` therefore
compiles *nothing* and exits 0 no matter what is broken. Only build mode
(`tsc -b`, which `npm run build` runs) follows the references into
`tsconfig.app.json`, where `noUnusedLocals` / `noUnusedParameters` live — so an
unused import fails the Docker frontend image but passes `tsc --noEmit`.

Deliberate exceptions: `weather` and `ip_info` build hardcoded third-party URLs
with no injection point, so only their argument handling is covered, and
`ptr_lookup` calls `net.LookupAddr` directly (it takes an IP, so there is no URL
to redirect — the test uses `127.0.0.1` and accepts either an answer or a
resolver error). The rest of the network skills run against local listeners:
`dns_lookup` and `ping_check` against `localhost`, `tls_probe` against a
self-signed TLS listener, `banner_grab` against a raw TCP listener, and
`http_headers` / `http_health` / `web_scrape` against `httptest`.

Things worth testing that are easy to get wrong here, and are covered:
ordering guarantees (the skill list and system prompt must be byte-stable
across runs), the UTF-8 truncation helpers, the scheduler's missed-minute
sweep and cross-process claims, `skill_creator`'s YAML (hostile values are
written, loaded back and compared), and concurrent access to the MCP client and the bot's session map
(both are `-race` tests that fail without their mutexes).

---

## 11. Extending the system

### Adding a new built-in Go skill

1. Create (or add to) a file in `internal/skills/builtin/`, exporting two
   functions: `XSchema() Skill` returning the schema (local types from
   `helpers.go`), and the implementation. A skill that waits on the network or
   a process exports `XContext(...) ContextExecutor` (threading `ctx` into its
   requests, dials and commands) plus `X(...) Executor` as
   `withoutContext(XContext(...))`; a pure-computation skill exports just
   `X(...) Executor`. Read arguments through `str` / `strList` / `intOr` /
   `floatOr` / `boolOr` so the skill tolerates the loose JSON types an LLM
   emits; dial and fetch through `netguard` / `httpClient`; put a third-party
   base URL in a package variable so tests can redirect it.
2. Register it in `registerBuiltins()` in `internal/skills/client.go`:
   `register(toSkill(builtin.XSchema()), builtin.XContext(t))`, or
   `builtin.Lift(builtin.X(...))` for a pure one — pass whatever slice of `cfg`
   the executor needs (timeouts, enable flags, allowlists).
3. Return `map[string]any{"error": "..."}, nil` for user-facing failures rather
   than a Go error, so the agent sees the reason and can react to it.
4. Add the schema to `allSchemas()` in `internal/skills/builtin/skills_test.go`
   and bump the count in `TestEverySchemaIsWellFormed` (and the 51 in
   `TestBuiltinInventory` in `internal/skills`); add the required
   parameter to the `want` map in `TestRequiredParametersAreDeclared`. The
   suite fails without this on purpose — it is what keeps the inventory honest.
5. If the skill handles credentials, add it to `sensitiveSkills` in
   `internal/agent/agent.go` so its args and result are redacted in the audit
   log (`/api/invocations` is readable by any API caller — and by anyone who can
   reach the gateway when `GATEWAY_API_TOKEN` is unset).  If its *result* is
   itself a secret, also add it to `secretResult()` so the session history,
   the stream's `tool_result` event and the debug log are redacted too.
6. Update the counts and tables that name skills: the README skill table and
   its "(N total)" heading, and in this document §2's `builtin/` line, §3.5's
   file table, and the three "all N skills" mentions.

### Adding a new markdown skill at runtime

Use the agent's `skill_creator` tool to describe the skill:
```
"use skill_creator to create a skill called my_skill that ..."
```
The skill is written to `SKILLS_DIR/my_skill.md` and hot-registered
immediately — no restart, no recompile.

Or write the `.md` file manually and restart the gateway (it loads all files in
`SKILLS_DIR` on startup via `markdown.LoadDir`).

### Adding a new LLM backend

1. Create `internal/llm/my_backend.go` implementing `ChatClient`.
2. Add a case in `bootstrap.BuildChat()` to construct it when
   `LLM_PROVIDER=my_backend`.  Both `cmd/gateway` and `cmd/cli` (local mode)
   pick it up automatically.
3. If the backend speaks a dialect of an existing one, reuse that client
   instead of writing a new type — `digitalocean` and `opencode` are both
   `OpenAIChat`.  A relay that needs extra request headers takes them as
   `llm.WithHeader(...)` options rather than a new field.
4. If the backend needs a different embeddings client, add it in
   `internal/memory/embeddings.go` and a case in `memory.buildEmbeddings()`.
   Add a case there too if the backend serves **no** embeddings, so operators
   see which backend the fallback picks; `/health` reports it as
   `embed_provider`, and `EMBED_PROVIDER` overrides it.

### Adding a new native Go skill

1. Create a file under `internal/` implementing `agent.Skill`.
2. Register it in `bootstrap.New()` in the `native` map (used by both gateway
   and CLI local mode).

### Adding a new CLI subcommand

1. Add a switch-case in `cmd/cli/main.go` dispatching to a new `runX(args)` function.
2. Create `cmd/cli/x.go` with the new function, using `flag.FlagSet` for argument parsing.
3. Add typed wrappers to `cmd/cli/client.go` if the command needs a new
   service or HTTP call — branch on `isRemote()` to pick local vs. remote.

### Adding a new Telegram command

1. Add a handler method on `telegram.Bot` in `internal/telegram/bot.go`.
2. Wire it in the `handle()` switch statement.

### Adding a new web UI page

1. Create `web/src/pages/MyPage.tsx`.
2. Add a `<Route path="/my-page" element={<MyPage />} />` to `src/App.tsx`.
3. Add a nav entry (icon + label + path) to the `NAV` array in
   `src/components/Layout.tsx`.
4. Add REST helpers to `src/api/client.ts` if the page needs new endpoints.
5. Run `npm run build` (or `make web`) to update `web/dist/`.
