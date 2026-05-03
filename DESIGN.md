# OmegaGrid Agent Go — Design Document

## 1. Overview

OmegaGrid Agent Go is a pure Go rewrite of the omegagrid-agent platform.
The gateway, agent loop, scheduler, Telegram bot, all 21 skills, vector memory,
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
│  (Go binary)        │  /api/query[/str]    Ollama / OpenAI / OpenAI Codex
└─────────────────────┘
```

### Design principles

| Principle | How it manifests |
|---|---|
| **Single binary per role** | `cmd/gateway` and `cmd/telegram-bot` compile to static, CGO-free executables. |
| **No runtime dependencies** | All skills, memory, and history run in-process — no sidecar, no separate Python process. |
| **Pure Go, no CGO** | `modernc.org/sqlite` (history, scheduler), `chromem-go` (vector memory), and all skills are pure Go packages. |
| **Self-contained repo** | No sibling project required.  `docker compose up --build` from any checkout. |
| **Identical agent contract** | System prompt, JSON envelope, tool-calling protocol match the original `core/agent.py` exactly. |
| **Frontend as a peer service** | The web UI is an independent nginx container; it never shares a process or filesystem with the gateway. |
| **Hot-extensible skills** | `skill_creator` writes `.md` files and hot-registers them into the in-process registry at runtime — no restart needed. |

---

## 2. Package layout

```
omegagrid-agent-go/
├── cmd/
│   ├── gateway/main.go          # HTTP gateway entry point
│   ├── telegram-bot/main.go     # Telegram bot entry point
│   └── migrate-vector/          # One-shot ChromaDB → chromem-go migration
│       ├── main.go              #   Go importer (reads JSONL → writes chromem-go)
│       └── export.py            #   Python exporter (reads ChromaDB → writes JSONL)
├── internal/
│   ├── agent/agent.go           # Tool-calling loop (Run / RunStream)
│   ├── config/config.go         # Env-driven configuration
│   ├── httpapi/                 # chi router + REST handlers
│   │   ├── server.go            #   router, CORS, middleware
│   │   ├── chat.go              #   POST /api/query, /api/query/stream
│   │   ├── health.go            #   GET /health
│   │   ├── history.go           #   sessions CRUD
│   │   ├── memory.go            #   POST /api/memory/{add,search}
│   │   ├── scheduler.go         #   scheduler task CRUD
│   │   └── skills.go            #   GET /api/skills, /api/tools
│   ├── llm/
│   │   ├── llm.go               #   ChatClient interface + Message type
│   │   ├── ollama.go            #   Ollama /api/chat client
│   │   └── openai.go            #   OpenAI chat_completions + responses client
│   ├── memory/
│   │   ├── client.go            #   Public API — CreateSession / AddMemory / SearchMemory / …
│   │   ├── history.go           #   SQLite sessions + messages (modernc.org/sqlite, no CGO)
│   │   ├── vector.go            #   chromem-go vector store + SHA256 + cosine dedup pipeline
│   │   └── embeddings.go        #   Ollama (3-endpoint fallback) + OpenAI embeddings clients
│   ├── observability/timing.go  # Mark-based timer (surfaces in RunResult.Meta)
│   ├── scheduler/
│   │   ├── cron.go              #   5-field cron matcher
│   │   ├── runner.go            #   Background goroutine executing due tasks
│   │   ├── skill.go             #   Native schedule_task skill
│   │   └── store.go             #   SQLite task CRUD
│   ├── search/skill.go          # Native web_search skill (DuckDuckGo HTML scrape)
│   ├── skills/
│   │   ├── client.go            #   Public API — List() / Execute(); wires registry
│   │   ├── registry.go          #   Thread-safe sync.RWMutex skill map
│   │   ├── builtin/             #   Go implementations of all 21 built-in skills
│   │   │   ├── helpers.go       #     Local types (Skill/Param/Executor) + arg helpers
│   │   │   ├── web.go           #     weather, http_request, web_scrape, http_health, ip_info
│   │   │   ├── network.go       #     dns_lookup, ping_check, port_scan, whois_lookup
│   │   │   ├── encode.go        #     base64, hash, uuid_gen, password_gen, cidr_calc
│   │   │   ├── eval.go          #     datetime, math_eval (safe AST parser), cron_schedule
│   │   │   ├── exec.go          #     shell_command, ssh_command
│   │   │   └── qr.go            #     qr_generate
│   │   └── markdown/            #   Markdown skill loader + pipeline executor
│   │       └── loader.go        #     YAML frontmatter parser, {{placeholder}} resolution
│   └── telegram/
│       ├── agent_client.go      #   Calls gateway /api/query[/stream]
│       ├── auth.go              #   SQLite-backed Telegram user allowlist
│       └── bot.go               #   Update poller + command handlers
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
│       │   └── stream.ts        #   fetch-based SSE parser (supports POST)
│       ├── store/
│       │   └── chat.ts          #   Zustand: session ID + live stream steps
│       ├── components/
│       │   ├── Layout.tsx       #   Icon rail nav sidebar
│       │   ├── SessionList.tsx  #   Scrollable session list + New button
│       │   ├── ChatBubble.tsx   #   Markdown-rendered message bubbles + copy
│       │   └── ToolCard.tsx     #   Collapsible tool call card (args + result)
│       └── pages/
│           ├── Chat.tsx         #   Streaming chat, session sidebar, abort
│           ├── Memory.tsx       #   Vector search + manual add
│           ├── Skills.tsx       #   Skill catalog with param schemas
│           ├── Scheduler.tsx    #   Cron task CRUD
│           └── Health.tsx       #   Gateway status, auto-refresh
├── docker/
│   ├── frontend.Dockerfile      # node:20-alpine build → nginx:1.27-alpine
│   ├── gateway.Dockerfile       # golang:1.25-bookworm → distroless (~19 MB)
│   ├── telegram.Dockerfile      # golang:1.25-bookworm → distroless (~15 MB)
│   ├── migrate.Dockerfile       # Two-stage: Python exporter + Go importer
│   └── nginx.conf               # SPA fallback + /api proxy + SSE tuning
├── docker-compose.yml           # 3-service stack (frontend, gateway, telegram-bot)
├── docker-compose.migrate.yml   # One-shot migration containers
├── Makefile                     # web / build / build-all / dev-web / vector-migrate / vet
├── .dockerignore
├── .env.example
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
| LLM | `LLM_PROVIDER`, `OLLAMA_URL`, `OLLAMA_MODEL`, `OLLAMA_TIMEOUT`, `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `OPENAI_CHAT_MODEL`, `OPENAI_TIMEOUT`, `OPENAI_API_MODE`, `OPENAI_REASONING_EFFORT` | ollama, `http://127.0.0.1:11434`, `llama3:latest`, 120s |
| Agent | `AGENT_DB`, `AGENT_CONTEXT_TAIL`, `AGENT_MEMORY_HITS`, `AGENT_MAX_STEPS` | `{DATA_DIR}/agent_memory.sqlite3`, 30, 5, 25 |
| Vector memory | `AGENT_VECTOR_DIR`, `AGENT_VECTOR_COLLECTION`, `AGENT_DEDUP_DISTANCE` | `{DATA_DIR}/chromem`, `memories`, 0.08 |
| Embeddings | `OLLAMA_EMBED_MODEL`, `OPENAI_EMBED_MODEL` | `nomic-embed-text`, `text-embedding-3-small` |
| Scheduler | `SCHEDULER_DB`, `SCHEDULER_TICK_SEC` | `{DATA_DIR}/scheduler.sqlite3`, 60 |
| Telegram | `TELEGRAM_BOT_TOKEN`, `BOT_AUTH_ENABLED`, `BOT_ADMIN_ID` | — |
| Skills | `SKILLS_DIR`, `SKILL_HTTP_TIMEOUT`, `SKILL_SHELL_ENABLED`, `SKILL_SSH_ENABLED`, `SKILL_SSH_IDENTITY_FILE`, `SKILL_SSH_DEFAULT_USER`, `SKILL_SSH_PRIVATE_KEY` | `{DATA_DIR}/skills`, 30, false, false |

`LLM_PROVIDER` determines both the chat client and the embeddings backend.
When set to `openai-codex` or when the model name contains `codex`, the
OpenAI client automatically switches to the `/responses` API endpoint.

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
| `responses` | `/responses` | Parses `output_text` or nested `output` array | Codex models (`codex-*`) or explicit `OPENAI_API_MODE=responses` |

Both modes map `role: "tool"` messages to `role: "user"` with a
`"[Tool result]:"` prefix, since the APIs do not accept a native tool role
without a preceding tool-use turn.

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

#### Run flow

```
startSession()
  ├─ create or load session (via memory.Client)
  ├─ semantic search for relevant memories (vector store)
  ├─ load message tail (last N turns from history store)
  └─ assemble tool table (in-process skills + native skills)

for step = 1..MaxSteps:
  ├─ call ChatClient.CompleteJSON(messages)
  ├─ parse JSON (with auto-recovery for malformed envelopes)
  ├─ normalizeToolCall() — fix LLM mistakes, e.g. tool name in type field
  │
  ├─ if type == "final":
  │     persist answer, return RunResult
  │
  └─ if type == "tool_call":
        dispatch to skill registry or native skill
        append tool result to messages
        continue

if MaxSteps exceeded:
  return "could not finish" fallback
```

#### Built-in tools

| Tool | Purpose | Implementation |
|---|---|---|
| `vector_add` | Store a durable fact or preference | `memory.Client.AddMemory()` → in-process vector store |
| `vector_search` | Semantic search over stored memories | `memory.Client.SearchMemory()` → in-process vector store |
| `schedule_task` | Create/list/delete/enable/disable cron tasks | Native Go via `scheduler.ScheduleTaskSkill` |
| `web_search` | DuckDuckGo HTML search (no API key); returns title/url/snippet | Native Go via `search.WebSearchSkill` |

All registered skills (weather, dns_lookup, shell_command, etc.) are listed
directly from the in-process `skills.Registry` on each run, so hot-registered
skills (via `skill_creator`) appear immediately without a restart.

#### System prompt

The system prompt instructs the LLM to:

1. Always output strict JSON (`tool_call` or `final`).
2. Use `vector_add` / `vector_search` for persistent memory.
3. Call tools instead of hallucinating answers.
4. Use `skill_creator` to self-extend when no existing skill fits.

#### Streaming

`RunStream()` writes `Event` structs to a channel:

| Event type | Payload |
|---|---|
| `thinking` | Step number |
| `tool_call` | Tool name, args, why |
| `tool_result` | Tool name, result text, elapsed seconds |
| `final` | Session ID, answer, metadata |
| `error` | Error message |

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
2. **Embedded JSON** — extract first `{...}` via regex.
3. **`raw_model_json` wrapper** — some models wrap output in a meta-envelope;
   the parser unwraps it.
4. **normalizeToolCall** — if the LLM puts the tool name in the `type` field
   instead of `tool`, the parser auto-corrects.
5. On total failure — return a fallback answer containing the raw LLM output.

### 3.4 Memory & history (`internal/memory`)

All memory operations run in-process — no HTTP round-trip to a sidecar.
`memory.New(cfg)` returns a `*Client` that owns both stores.

#### History store (`history.go`)

SQLite with WAL journal mode and a single connection (`SetMaxOpenConns(1)`):

```sql
sessions (id INTEGER PK, created_at REAL)
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
| `ListSessions(limit)` | Most-recent sessions + message count |
| `ListMessages(sid, limit, offset)` | Paginated message list |
| `LoadTail(sid, limit)` | Last N messages for LLM context (skips `raw_model_json`) |
| `AddMessage(sid, role, content)` | Persist one message |

#### Vector store (`vector.go`)

`chromem-go` (`github.com/philippgille/chromem-go`) cosine-similarity store
opened with `NewPersistentDB(vectorDir, false)`.

Deduplication pipeline:

```
addText(text, meta)
  ├─ SHA256(text) → hashes.Load (sync.Map) → skip if exact duplicate
  ├─ embed(text) via embeddings client
  ├─ collection.QueryEmbedding(embedding, 1)
  │     └─ if (1.0 - similarity) ≤ dedupDistance (0.08) → skip semantic duplicate
  └─ collection.AddDocument(chromem.Document{...})
        └─ hashes.Store(sha, memoryID)
```

Metadata is stored as `map[string]string`; `coerceMeta()` converts any
numeric, bool, or nil values from the caller-supplied `map[string]any`.

`SearchMemory(query, k)` calls `collection.QueryEmbedding(embed(query), k)` and
returns `(id, text, metadata, distance)` tuples sorted by ascending distance.

#### Embeddings clients (`embeddings.go`)

Selected by `LLM_PROVIDER` at construction time:

| Client | Endpoints tried (in order) | Model env var |
|---|---|---|
| `ollamaEmbeddings` | `/api/embed`, `/v1/embeddings`, `/api/embeddings` | `OLLAMA_EMBED_MODEL` |
| `openAIEmbeddings` | `{OPENAI_BASE_URL}/embeddings` | `OPENAI_EMBED_MODEL` |

`ollamaEmbeddings.tryEndpoint()` parses all three Ollama response shapes
(`{"embeddings":[...]}`, `{"data":[{"embedding":[...]}]}`, `{"embedding":[...]}`)
in a single pass.  `openAIEmbeddings` uses Bearer authentication and parses
`data[0].embedding`.

### 3.5 Skills (`internal/skills`)

All 21 skills run in-process inside the gateway binary via a thread-safe
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

#### Built-in skills (`builtin/`)

21 skills compiled directly into the gateway binary.  Local `Skill`/`Param`
types are defined in `builtin/helpers.go` to avoid import cycles; `client.go`
converts them to the top-level `skills.Skill` type via `toSkill()`.

| File | Skills |
|---|---|
| `web.go` | `weather`, `http_request`, `web_scrape`, `http_health`, `ip_info` |
| `network.go` | `dns_lookup`, `ping_check`, `port_scan`, `whois_lookup` |
| `encode.go` | `base64_skill`, `hash_skill`, `uuid_gen`, `password_gen`, `cidr_calc` |
| `eval.go` | `datetime_skill`, `math_eval`, `cron_schedule` |
| `exec.go` | `shell_command`, `ssh_command` |
| `qr.go` | `qr_generate` |

Key implementation notes:

- **`math_eval`** — recursive-descent AST parser (`parseAddSub → parseMulDiv →
  parsePow → parseUnary → parsePrimary`).  Supports `//` floor division, `**`
  power, and all Python-parity math functions (`sqrt`, `sin`, `cos`, `tan`,
  `log`, `log2`, `log10`, `factorial`, `abs`, `ceil`, `floor`, `round`, `pow`,
  `pi`, `e`, `inf`, `nan`).  No `eval()` — safe for untrusted input.

- **`dns_lookup`** — tries `exec.LookPath("dig")` first for full record-type
  coverage (MX, TXT, CNAME, NS); falls back to Go stdlib (`net.LookupHost`,
  `net.LookupMX`, etc.).

- **`port_scan`** — bounded concurrency via semaphore channel (100 slots),
  `net.DialTimeout` with configurable timeout; returns sorted open port list.

- **`whois_lookup`** — raw TCP port 43 to `whois.iana.org`, parses `refer:`
  line, re-queries the authoritative WHOIS server.

- **`ssh_command`** — `loadSigner()` checks: identity_file arg → `SKILL_SSH_PRIVATE_KEY`
  env (with base64 decode attempt) → `SKILL_SSH_IDENTITY_FILE` path.

- **`shell_command`** — blocked-pattern list, `context.WithTimeout`, captures
  both stdout and stderr.  Disabled by default (`SKILL_SHELL_ENABLED=false`).

- **`qr_generate`** — uses `github.com/skip2/go-qrcode`; `qr.Bitmap()` returns
  `[][]bool`; module count derived from `len(bitmap)`.

- **`skill_creator`** — writes `.md` files to `SKILLS_DIR` and calls
  `reg.register()` directly on the live registry for instant hot-registration.

#### Markdown / pipeline skills (`markdown/`)

`.md` files with YAML frontmatter in `SKILLS_DIR` are loaded as skills.
Local `SkillSchema`/`Param` types in `loader.go` avoid import cycles;
`client.go` converts them via `mdToSkill()`.

Pipeline execution:

```
for each step:
  resolve {{placeholder}} tokens in request template:
    - {{param_name}}         → caller-supplied argument
    - {{step.N.field.path}}  → dot-path into step N's JSON result
  execute: HTTP GET/POST  OR  call another skill in the registry
  accumulate step results
```

`LoadDir(dir)` scans `SKILLS_DIR` for `*.md` files on startup.
`skill_creator` calls `Load(path)` after writing a new file for immediate
hot-registration without a restart.

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
    enabled                 INTEGER NOT NULL DEFAULT 1,
    created_at              REAL NOT NULL,
    last_run_at             REAL,
    last_result             TEXT,
    run_count               INTEGER NOT NULL DEFAULT 0
);
```

Operations: `Create`, `Get`, `ListAll`, `ListEnabled`, `UpdateLastRun`,
`SetEnabled`, `Delete`.  The directory for the database file is created
automatically via `os.MkdirAll`.

#### Cron matcher (`cron.go`)

```go
func Matches(cronExpr string, dt time.Time) bool
```

5-field format: `minute hour dayOfMonth month dayOfWeek`.

| Syntax | Meaning |
|---|---|
| `*` | Any value |
| `*/N` | Every N-th value |
| `N` | Exact match |
| `N-M` | Range (inclusive) |
| `A,B,C` | Comma-separated list |

Day of week: 0 = Sunday.  Invalid expressions return `false` (safe default).

#### Runner (`runner.go`)

A background goroutine started by the gateway:

```
every SchedulerTickSec (default 60s):
  for each enabled task:
    if Matches(task.CronExpr, now) AND not already run this minute:
      execute task.Skill with task.Args
      UpdateLastRun(task.ID, result)
      if task.NotifyTelegramChatID != nil:
        send result via Telegram Bot API
```

Deduplication: compares `task.LastRunAt` with the current minute boundary to
avoid double-firing if the tick period is shorter than a minute.

#### Native skill (`skill.go`)

`ScheduleTaskSkill` exposes five actions:

| Action | Parameters | Returns |
|---|---|---|
| `create` | `cron_expr`, `skill`, `name`, `args`, `notify_telegram_chat_id` | `{created: true, task: {...}}` |
| `list` | — | `{count: N, tasks: [...]}` |
| `delete` | `task_id` | `{deleted: true}` |
| `enable` | `task_id` | `{enabled: true}` |
| `disable` | `task_id` | `{disabled: true}` |

Registered as a **native** Go skill in `cmd/gateway/main.go`, operating
directly on the `Store` struct — no HTTP round-trip required.

### 3.7 HTTP gateway (`internal/httpapi`)

Built on [go-chi/chi v5](https://github.com/go-chi/chi).

#### Middleware stack

1. `middleware.Recoverer` — panic recovery
2. `middleware.RealIP` — trust `X-Forwarded-For`
3. `middleware.RequestID` — inject unique request ID
4. Custom CORS — `Access-Control-Allow-Origin: *`, all methods, common headers

#### Route table

| Method | Path | Handler | Notes |
|---|---|---|---|
| `GET` | `/health` | `handleHealth` | Provider, model, skills dir, scheduler DB |
| `POST` | `/api/query` | `handleQuery` | Synchronous agent query |
| `POST` | `/api/query/stream` | `handleQueryStream` | SSE agent query |
| `POST` | `/api/sessions/new` | `handleNewSession` | In-process history store |
| `GET` | `/api/sessions` | `handleListSessions` | In-process history store |
| `GET` | `/api/sessions/{sid}/messages` | `handleSessionMessages` | In-process history store |
| `POST` | `/api/memory/add` | `handleMemoryAdd` | In-process vector store |
| `POST` | `/api/memory/search` | `handleMemorySearch` | In-process vector store |
| `GET` | `/api/skills` | `handleListSkills` | In-process skill registry |
| `GET` | `/api/tools` | `handleListTools` | Built-in tool schemas |
| `POST` | `/api/scheduler/tasks` | `handleSchedulerCreate` | In-process scheduler store |
| `GET` | `/api/scheduler/tasks` | `handleSchedulerList` | In-process scheduler store |
| `GET` | `/api/scheduler/tasks/{id}` | `handleSchedulerGet` | In-process scheduler store |
| `POST` | `/api/scheduler/tasks/{id}/enable` | `handleSchedulerEnable` | In-process scheduler store |
| `POST` | `/api/scheduler/tasks/{id}/disable` | `handleSchedulerDisable` | In-process scheduler store |
| `DELETE` | `/api/scheduler/tasks/{id}` | `handleSchedulerDelete` | In-process scheduler store |

The gateway router serves only `/health` and `/api/*`.  The gateway binary
contains no static-file serving and no UI assets; all browser UI traffic is
served by the separate nginx **frontend** service, which proxies `/api/*`
and `/health` back to the gateway.

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
data: {"session_id":42,"answer":"It's 15°C and cloudy in London.","meta":{"steps":1,"model":"llama3:latest"}}
```

### 3.8 Telegram bot (`internal/telegram`)

#### Auth store (`auth.go`)

Optional SQLite-backed allowlist:

```sql
CREATE TABLE IF NOT EXISTS users (
    telegram_id INTEGER PRIMARY KEY,
    added_at    REAL NOT NULL,
    last_activity REAL
);
```

Controlled by `BOT_AUTH_ENABLED` and `BOT_ADMIN_ID` environment variables.

#### Agent client (`agent_client.go`)

| Method | Endpoint | Behaviour |
|---|---|---|
| `Query(query, sessionID)` | `POST /api/query` | Synchronous; returns answer + session ID |
| `QueryStream(query, sessionID, out chan Event)` | `POST /api/query/stream` | Parses SSE via `bufio.Scanner` (64 KB buffer), emits `Event` structs |

#### Bot (`bot.go`)

Long-polling update handler.

| Command | Handler | Description |
|---|---|---|
| `/start` | `handleStart` | Reset session, show help text |
| `/ask <query>` | `handleAsk` | Explicit query (shows model/steps in footer) |
| `/new` | `handleNew` | Start fresh session |
| `/skills` | `handleSkills` | List available skills |
| `/auth_add <id>` | `handleAuthAdd` | Admin: allowlist a Telegram user |
| `/auth_list` | `handleAuthList` | Admin: show allowlist |
| *(plain text)* | `handleText` | Treated as agent query |

**Progressive status updates**: when streaming, the bot edits its status
message every 1500 ms with the current step.  Falls back to synchronous
`/api/query` if the stream fails.

### 3.9 Observability (`internal/observability`)

```go
type Timer struct { ... }

func NewTimer() *Timer
func (t *Timer) Mark(name string)          // record time since last Mark
func (t *Timer) AsMap() map[string]float64  // {name: seconds, total_s: total}
```

Timings surface in `RunResult.Meta` and in the streaming `final` event.

### 3.10 Web UI (`web/`)

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
| `/ui/` | Chat | Streaming chat; SSE events rendered as tool-call cards; session sidebar; auto-scroll; abort button |
| `/ui/memory` | Memory | Semantic search (configurable k); manual add with JSON metadata |
| `/ui/skills` | Skills | Collapsible skill cards with full parameter schemas |
| `/ui/scheduler` | Scheduler | Task list with enable/disable toggle; create-modal with skill picker |
| `/ui/health` | Health | Provider/model status card; 30 s auto-refresh |

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
- `error` → set `errorMsg`, show toast

#### Gateway / UI separation

The gateway binary contains no UI assets and no static-file serving.  The
React app is built and served exclusively by the nginx frontend container.
For local UI development without Docker, run `make dev-web` (Vite dev
server at `:5173/ui/`, proxying `/api` to the gateway on `:8000`).

---

## 4. Vector memory migration (ChromaDB → chromem-go)

If you are upgrading from the previous Python sidecar deployment, a one-shot
migration tool moves existing ChromaDB data into the new chromem-go format.
No local Python installation is required — both steps run inside Docker.

### 4.1 Migration architecture

```
 ┌──────────────────────────────────────────────┐
 │  migrate-export (Python 3.11 + chromadb 0.5.5) │
 │  Reads data/vector_db/ → data/vector_db.jsonl  │
 └──────────────────────────────────────────────┘
                         │ JSONL (one record per line)
                         ▼
 ┌──────────────────────────────────────────────┐
 │  migrate-import (static Go binary)            │
 │  Reads data/vector_db.jsonl → data/chromem/   │
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
collection total; exits cleanly with "nothing to export" for an empty collection.

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
├── agent_memory.sqlite3       # HistoryStore: sessions + messages
├── scheduler.sqlite3          # Scheduler task definitions + run state
├── telegram_auth.sqlite3      # Telegram bot allowlist (if auth enabled)
├── skills/                    # Dynamic markdown skill files (SKILLS_DIR)
│   └── *.md
└── chromem/                   # chromem-go vector database
    └── memories/              #   Collection directory
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
| `gateway` | `gcr.io/distroless/static-debian12` | ~19 MB | `golang:1.25-bookworm` → distroless |
| `telegram-bot` | `gcr.io/distroless/static-debian12` | ~15 MB | `golang:1.25-bookworm` → distroless |
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

Gateway and telegram-bot share a bind-mounted `./data` volume.
Frontend is stateless (no volume).  Build context for all three services is `.`
(the repo root) — no external paths required.

### 6.3 Networking

| Service | Listens on | Connects to |
|---|---|---|
| gateway | `:8000` | Ollama / OpenAI (for chat + embeddings) |
| frontend | `:80` | gateway `:8000` (nginx proxy for `/api/*`, `/health`) |
| telegram-bot | — | gateway `:8000`, Telegram Bot API |

Within compose, services reference each other by name (`http://gateway:8000`).
For Ollama on the host machine,
`http://host.docker.internal:11434` is the recommended URL.

### 6.4 nginx configuration (`docker/nginx.conf`)

```nginx
location /ui/ {
    try_files $uri /ui/index.html;   # SPA fallback for client-side routing
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
  "remember": true,
  "max_steps": 10,
  "telegram_chat_id": null
}
→ {
  "session_id": 42,
  "answer": "It's 15°C and cloudy in London.",
  "meta": {"steps": 1, "model": "llama3:latest", "timings": {...}},
  "memories": [...],
  "debug_log": "..."
}
```

```
POST /api/query/stream
(same request body)
→ SSE stream of event: thinking | tool_call | tool_result | final | error
```

#### Sessions & history

```
POST /api/sessions/new           → {"session_id": 43}
GET  /api/sessions?limit=50      → {"sessions": [{id, created_at, message_count}]}
GET  /api/sessions/42/messages   → {"session_id": 42, "messages": [{id, session_id, ts, role, content}]}
```

#### Vector memory

```
POST /api/memory/add    {"text": "...", "meta": {}}
                      → {"ok": true, "memory_id": "uuid", "skipped": false}

POST /api/memory/search {"query": "...", "k": 5}
                      → {"ok": true, "hits": [{id, text, metadata, distance}]}
```

#### Skills

```
GET /api/skills → {"skills": [{name, description, parameters, body}]}
GET /api/tools  → {"tools": [{name, description, parameters}]}
```

#### Scheduler

```
POST   /api/scheduler/tasks          create task
GET    /api/scheduler/tasks          list all
GET    /api/scheduler/tasks/{id}     get one
POST   /api/scheduler/tasks/{id}/enable
POST   /api/scheduler/tasks/{id}/disable
DELETE /api/scheduler/tasks/{id}     remove
```

#### Health

```
GET /health → {"ok": true, "provider": "ollama", "chat_model": "llama3:latest",
               "skills_dir": "/app/data/skills", "scheduler_db": "..."}
```

---

## 8. Key design decisions

### Why Go for everything?

- **Compiled binaries** — single ~19 MB static binary, no runtime deps.
- **Goroutine-based concurrency** — scheduler runner, SSE streaming, and
  request handling are natural goroutine workloads.
- **SQLite via pure Go** — `modernc.org/sqlite` needs no CGO, simplifying
  cross-compilation and distroless deployment.
- **Fast startup** — sub-second cold start vs Python's multi-second import chain.
- **Single deploy unit** — one binary contains the gateway, all 21 skills,
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
  calls `reg.register()` on the live in-process registry; skills are available
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
| LLM returns invalid JSON | Agent parser tries three recovery strategies; falls back to returning raw LLM output as the answer. |
| LLM calls unknown tool | Agent returns an error message listing valid tools; LLM can retry. |
| Skill execution fails | Executor returns `(nil, err)`; agent loop wraps it as `{error: "..."}` and appends as tool result; LLM can retry or answer. |
| Embeddings unreachable | `addText` and `searchMemory` return errors surfaced in the agent loop; agent continues without memory injection. |
| Scheduler task fails | Runner captures the error, stores it in `last_result`, optionally notifies via Telegram. |
| Telegram stream breaks | Bot falls back to synchronous `/api/query` endpoint. |
| MaxSteps exceeded | Agent returns "could not finish" with partial debug log. |
| SQLite directory missing | `os.MkdirAll` creates it before opening the database. |
| Browser aborts SSE | `fetch` AbortController cancels the request; gateway detects `r.Context().Done()` and stops writing. |
| chromem-go dir missing | `os.MkdirAll` + `NewPersistentDB` creates the directory and initialises the collection on first run. |

---

## 10. Extending the system

### Adding a new built-in Go skill

1. Create (or add to) a file in `internal/skills/builtin/`, defining a `Skill`
   struct and an `Executor` function using the local types from `helpers.go`.
2. Add the skill to the `All()` slice returned by that file (or a new `All()`
   function if in a new file).
3. Wire the new `All()` slice in `internal/skills/client.go` where the registry
   is populated at construction time.

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
2. Add a case in `cmd/gateway/main.go` to construct it when
   `LLM_PROVIDER=my_backend`.
3. If the backend needs a different embeddings client, add it in
   `internal/memory/embeddings.go` and wire it in `memory.New()`.

### Adding a new native Go skill

1. Create a file under `internal/` implementing `agent.Skill`.
2. Register it in `cmd/gateway/main.go` in the `nativeSkills` map.

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
