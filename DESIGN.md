# OmegaGrid Agent Go — Design Document

## 1. Overview

OmegaGrid Agent Go is a Go rewrite of the omegagrid-agent platform.  The
gateway, agent loop, scheduler, and Telegram bot are compiled Go binaries.
Skills, vector memory, and conversation history remain in Python, served by a
lightweight FastAPI **sidecar**.  A React web UI is served by a dedicated
**frontend** nginx service.  All source is vendored in this repository, making
`docker compose up --build` the single command needed to run the full stack.

```
 Browser
    │ HTTP :80
    ▼
┌─────────────────────┐       HTTP        ┌────────────────────────────────┐
│  frontend           │  ───────────────► │    Go gateway  :8000           │
│  nginx:1.27-alpine  │  proxies /api/*   │                                │
│  serves /ui/*       │  and /health      │  ┌──────────────┐              │
└─────────────────────┘                   │  │ agent loop   │              │
                                          │  │ (tool-call)  │              │
┌─────────────────────┐       HTTP        │  └──────┬───────┘              │
│  telegram-bot       │  ───────────────► │         │                      │
│  (Go binary)        │  /api/query[/str] │  ┌──────▼───────┐              │
└─────────────────────┘                   │  │ scheduler    │              │
                                          │  │ (cron/sqlite)│              │
                                          │  └──────────────┘              │
                                          └──────────────┬─────────────────┘
                                               HTTP │         │ HTTP
                                      ┌─────────────┘         └────────────────┐
                                      ▼                                         ▼
                             ┌─────────────────┐                    ┌──────────────────┐
                             │ Python sidecar  │                    │ Ollama / OpenAI  │
                             │ :8001           │                    │ (LLM backend)    │
                             │  skills         │                    └──────────────────┘
                             │  vector memory  │
                             │  history store  │
                             └─────────────────┘
```

### Design principles

| Principle | How it manifests |
|---|---|
| **Single binary per role** | `cmd/gateway` and `cmd/telegram-bot` compile to static executables. |
| **Python stays where it adds value** | Skills, ChromaDB embeddings, markdown skill loader, hot-registration. |
| **Loose coupling** | Go ↔ Python via plain JSON-over-HTTP; either side can be restarted independently. |
| **Self-contained repo** | No sibling project required.  Python code vendored.  `docker compose up --build` from any checkout. |
| **Identical agent contract** | System prompt, JSON envelope, tool-calling protocol match the original `core/agent.py` exactly. |
| **Frontend as a peer service** | The web UI is an independent nginx container; it never shares a process or filesystem with the gateway. |

---

## 2. Package layout

```
omegagrid-agent-go/
├── cmd/
│   ├── gateway/main.go          # HTTP gateway entry point
│   └── telegram-bot/main.go     # Telegram bot entry point
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
│   ├── memory/client.go         #   HTTP client → sidecar memory/history endpoints
│   ├── observability/timing.go  #   Mark-based timer
│   ├── scheduler/
│   │   ├── cron.go              #   5-field cron matcher
│   │   ├── runner.go            #   Background goroutine executing due tasks
│   │   ├── skill.go             #   Native schedule_task skill
│   │   └── store.go             #   SQLite task CRUD
│   ├── search/skill.go          #   Native web_search skill (DuckDuckGo HTML scrape)
│   ├── skills/client.go         #   HTTP client → sidecar skill endpoints
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
├── sidecar/
│   ├── main.py                  # FastAPI shim (skills, memory, history)
│   ├── requirements.txt
│   └── python/                  # Vendored Python packages
│       ├── skills/              #   All skill implementations + registry
│       ├── memory/              #   HistoryStore (sqlite), VectorStore (ChromaDB)
│       └── llm/                 #   Embeddings clients (Ollama, OpenAI)
├── docker/
│   ├── frontend.Dockerfile      # node:20-alpine build → nginx:1.27-alpine
│   ├── gateway.Dockerfile       # golang:1.25-bookworm → distroless
│   ├── telegram.Dockerfile      # golang:1.25-bookworm → distroless
│   ├── sidecar.Dockerfile       # python:3.11-slim
│   └── nginx.conf               # SPA fallback + /api proxy + SSE tuning
├── docker-compose.yml           # 4-service stack
├── Makefile                     # web / build / build-all / dev-web
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
| Gateway | `BACKEND_PORT`, `DATA_DIR`, `SIDECAR_URL` | 8000, `/app/data`, `http://127.0.0.1:8001` |
| Frontend | `FRONTEND_PORT` | 80 (Docker Compose only) |
| LLM | `LLM_PROVIDER`, `OLLAMA_URL`, `OLLAMA_MODEL`, `OLLAMA_TIMEOUT`, `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `OPENAI_CHAT_MODEL`, `OPENAI_TIMEOUT`, `OPENAI_API_MODE`, `OPENAI_REASONING_EFFORT` | ollama, `http://127.0.0.1:11434`, `llama3:latest`, 120s |
| Agent | `AGENT_CONTEXT_TAIL`, `AGENT_MEMORY_HITS`, `AGENT_MAX_STEPS` | 30, 5, 25 |
| Scheduler | `SCHEDULER_DB`, `SCHEDULER_TICK_SEC` | `{DATA_DIR}/scheduler.sqlite3`, 60 |
| Telegram | `TELEGRAM_BOT_TOKEN`, `BOT_AUTH_ENABLED`, `BOT_ADMIN_ID`, `BOT_AUTH_DB` | — |
| Skills | `SKILL_HTTP_TIMEOUT`, `SKILL_SHELL_ENABLED`, `SKILL_SSH_ENABLED` | 30, false, false |
| Memory | `AGENT_VECTOR_COLLECTION`, `AGENT_DEDUP_DISTANCE`, `OLLAMA_EMBED_MODEL`, `OPENAI_EMBED_MODEL` | `memories`, 0.08, `nomic-embed-text`, `text-embedding-3-small` |

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
  ├─ create or load session (via sidecar)
  ├─ semantic search for relevant memories
  ├─ load message tail (last N turns)
  └─ assemble tool table (sidecar skills + native skills)

for step = 1..MaxSteps:
  ├─ call ChatClient.CompleteJSON(messages)
  ├─ parse JSON (with auto-recovery for malformed envelopes)
  ├─ normalizeToolCall() — fix LLM mistakes, e.g. tool name in type field
  │
  ├─ if type == "final":
  │     persist answer, return RunResult
  │
  └─ if type == "tool_call":
        dispatch to native skill or sidecar
        append tool result to messages
        continue

if MaxSteps exceeded:
  return "could not finish" fallback
```

#### Built-in tools

| Tool | Purpose | Implementation |
|---|---|---|
| `vector_add` | Store a durable fact or preference | `Memory.AddMemory()` → sidecar `/memory/add` |
| `vector_search` | Semantic search over stored memories | `Memory.SearchMemory()` → sidecar `/memory/search` |
| `schedule_task` | Create/list/delete/enable/disable cron tasks | Native Go via `scheduler.ScheduleTaskSkill` |
| `web_search` | DuckDuckGo HTML search (no API key); returns title/url/snippet | Native Go via `search.WebSearchSkill` |

All sidecar skills (weather, dns_lookup, shell_command, etc.) are merged into
the tool table dynamically via `Skills.List()` on each run, so hot-registered
skills (via `skill_creator`) appear without a restart.

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

### 3.4 Memory & history client (`internal/memory`)

Thin HTTP client calling the sidecar:

| Method | Sidecar endpoint | Purpose |
|---|---|---|
| `NewSession()` | `POST /sessions/new` | Create conversation session |
| `ListSessions(limit)` | `GET /sessions` | List recent sessions |
| `LoadTail(sid, limit)` | `GET /sessions/{sid}/tail` | Load last N messages for LLM context |
| `ListMessages(sid, limit, offset)` | `GET /sessions/{sid}/messages` | Paginated history |
| `AddMessage(sid, role, content)` | `POST /sessions/{sid}/messages` | Persist one message |
| `AddMemory(text, meta)` | `POST /memory/add` | Store vector memory (with dedup) |
| `SearchMemory(query, k)` | `POST /memory/search` | Semantic search |

The sidecar decodes `content_json` from SQLite before returning, so the
`/messages` response carries a plain `content` string — no further parsing
needed in callers.

### 3.5 Skills client (`internal/skills`)

| Method | Sidecar endpoint | Purpose |
|---|---|---|
| `List()` | `GET /skills` | Fetch all registered skill schemas |
| `Execute(name, args)` | `POST /skills/execute` | Run a skill and return its result |

The HTTP client uses a **5-minute timeout** to accommodate slow skills like
`web_scrape` or `ssh_command`.

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

Registered as a **native** Go skill in `cmd/gateway/main.go`, bypassing the
sidecar entirely.

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
| `GET` | `/health` | `handleHealth` | Provider, model, sidecar URL, scheduler DB |
| `POST` | `/api/query` | `handleQuery` | Synchronous agent query |
| `POST` | `/api/query/stream` | `handleQueryStream` | SSE agent query |
| `POST` | `/api/sessions/new` | `handleNewSession` | Proxy to sidecar |
| `GET` | `/api/sessions` | `handleListSessions` | Proxy to sidecar |
| `GET` | `/api/sessions/{sid}/messages` | `handleSessionMessages` | Proxy to sidecar |
| `POST` | `/api/memory/add` | `handleMemoryAdd` | Proxy to sidecar |
| `POST` | `/api/memory/search` | `handleMemorySearch` | Proxy to sidecar |
| `GET` | `/api/skills` | `handleListSkills` | Proxy to sidecar |
| `GET` | `/api/tools` | `handleListTools` | Built-in tool schemas |
| `POST` | `/api/scheduler/tasks` | `handleSchedulerCreate` | Direct to scheduler store |
| `GET` | `/api/scheduler/tasks` | `handleSchedulerList` | Direct to scheduler store |
| `GET` | `/api/scheduler/tasks/{id}` | `handleSchedulerGet` | Direct to scheduler store |
| `POST` | `/api/scheduler/tasks/{id}/enable` | `handleSchedulerEnable` | Direct to scheduler store |
| `POST` | `/api/scheduler/tasks/{id}/disable` | `handleSchedulerDisable` | Direct to scheduler store |
| `DELETE` | `/api/scheduler/tasks/{id}` | `handleSchedulerDelete` | Direct to scheduler store |

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

## 4. Python sidecar

### 4.1 FastAPI application (`sidecar/main.py`)

Lifespan-managed application that initializes three subsystems on startup:

| Subsystem | Class | Storage |
|---|---|---|
| Skill registry | `SkillRegistry` | In-memory (populated at startup + hot-registration) |
| History store | `HistoryStore` | `{DATA_DIR}/agent_memory.sqlite3` |
| Vector store | `VectorStore` | `{DATA_DIR}/vector_db/` (ChromaDB persistent) |

Embeddings backend is selected by `LLM_PROVIDER`:

| Provider | Client | Model env var |
|---|---|---|
| `ollama` | `OllamaEmbeddingsClient` | `OLLAMA_EMBED_MODEL` |
| `openai` / `openai-codex` | `OpenAIEmbeddingsClient` | `OPENAI_EMBED_MODEL` |

### 4.2 Skill registry

21 hard-coded skills + the `skill_creator` and any markdown skills are
registered at startup.  `schedule_task` and `web_search` are excluded —
they are native Go skills registered by the gateway.

| Skill | Summary |
|---|---|
| `weather` | Open-Meteo geocode + current conditions |
| `datetime` | Current date/time in any timezone |
| `http_request` | GET/POST to arbitrary URLs |
| `shell_command` | Local shell execution (disabled by default) |
| `web_scrape` | Fetch URL, strip HTML, return text |
| `dns_lookup` | A/AAAA/MX/TXT resolution |
| `cron_schedule` | Parse & explain cron expressions |
| `ping_check` | TCP connect check |
| `port_scan` | Scan port range on a host |
| `http_health` | HTTP health check with timing |
| `whois_lookup` | WHOIS query |
| `base64` | Encode/decode base64 |
| `hash` | MD5/SHA1/SHA256 hashing |
| `math_eval` | Safe arithmetic expression evaluator |
| `ip_info` | GeoIP via ip-api.com |
| `uuid_gen` | Generate UUIDs (v4) |
| `password_gen` | Cryptographic random passwords |
| `qr_generate` | QR code as base64 PNG |
| `cidr_calc` | CIDR range calculator |
| `ssh_command` | Remote SSH execution (disabled by default) |
| `skill_creator` | Create/list/show/delete markdown skills |
| *(markdown skills)* | Loaded from `.md` files in `SKILLS_DIR` |

#### Hot-registration

`skill_creator` writes `.md` files to `SKILLS_DIR` and registers them in the
live `SkillRegistry`.  The Go agent loop calls `Skills.List()` on every run,
so newly created skills appear without restarting anything.

#### Markdown skills

`.md` files with YAML frontmatter in `SKILLS_DIR` are loaded as skills.
Pipeline skills can chain multiple steps, referencing previous results via
`{{step.N.path.to.field}}`.

### 4.3 Vector store

ChromaDB with cosine similarity.  Deduplication pipeline:

```
add_text(text, meta)
  ├─ SHA256 hash text → check exact_hash_duplicate → skip if seen
  ├─ embed text via embeddings client
  ├─ query nearest neighbor
  │   └─ if distance ≤ dedup_distance (0.08) → skip as semantic_duplicate
  └─ upsert into ChromaDB collection
      └─ return {memory_id, skipped, reason, nearest_distance, timings}
```

### 4.4 History store

SQLite with two tables:

```sql
sessions (id INTEGER PK, created_at REAL)
messages (id INTEGER PK, session_id FK, ts REAL, role TEXT, content_json TEXT)
  INDEX ON messages(session_id, ts)
```

`list_messages` decodes `content_json` before returning, so API consumers
receive a plain `content` string.  `load_tail` skips `raw_model_json` entries.

### 4.5 Embeddings clients

| Client | Endpoints tried (in order) |
|---|---|
| `OllamaEmbeddingsClient` | `/api/embed`, `/v1/embeddings`, `/api/embeddings` |
| `OpenAIEmbeddingsClient` | `{base_url}/embeddings` |

Both return `(vector: List[float], elapsed_seconds: float)`.

---

## 5. Data persistence

```
{DATA_DIR}/
├── agent_memory.sqlite3       # HistoryStore: sessions + messages
├── scheduler.sqlite3          # Scheduler task definitions + run state
├── telegram_auth.sqlite3      # Telegram bot allowlist (if auth enabled)
└── vector_db/                 # ChromaDB persistent collection
    └── chroma-collections/
```

All SQLite databases are auto-created on first access.  In Docker all three
services (gateway, sidecar, telegram-bot) share a bind-mounted `./data`
volume.  The frontend service is stateless and needs no volume.

---

## 6. Docker & deployment

### 6.1 Images

| Image | Base | Approx. size | Build stages |
|---|---|---|---|
| `frontend` | `nginx:1.27-alpine` | ~50 MB | `node:20-alpine` → nginx |
| `gateway` | `gcr.io/distroless/static-debian12` | ~16 MB | `golang:1.25-bookworm` → distroless |
| `telegram-bot` | `gcr.io/distroless/static-debian12` | ~15 MB | `golang:1.25-bookworm` → distroless |
| `sidecar` | `python:3.11-slim` | ~1.5 GB | pip install + vendored Python |

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
  sidecar:      # :8001, no deps
  gateway:      # :8000, depends_on sidecar
  frontend:     # :80,   depends_on gateway
  telegram-bot: # no port, depends_on gateway
```

gateway, sidecar, and telegram-bot share a bind-mounted `./data` volume.
frontend is stateless (no volume).  Build context for all four services is `.`
(the repo root) — no external paths required.

### 6.3 Networking

| Service | Listens on | Connects to |
|---|---|---|
| sidecar | `:8001` | Ollama / OpenAI (for embeddings) |
| gateway | `:8000` | sidecar `:8001`, Ollama / OpenAI (for chat) |
| frontend | `:80` | gateway `:8000` (nginx proxy for `/api/*`, `/health`) |
| telegram-bot | — | gateway `:8000`, Telegram Bot API |

Within compose, services reference each other by name (`http://sidecar:8001`,
`http://gateway:8000`).  For Ollama on the host machine,
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
               "sidecar_url": "http://sidecar:8001", "scheduler_db": "..."}
```

### 7.2 Sidecar API (internal, called by gateway only)

```
GET  /skills                      list skill schemas
POST /skills/execute              {name, args} → {result}
POST /memory/add                  {text, meta} → {memory_id, skipped, ...}
POST /memory/search               {query, k}   → {hits, timings}
POST /sessions/new                              → {session_id}
GET  /sessions                                  → {sessions}
GET  /sessions/{id}/messages                    → {session_id, messages: [{id,session_id,ts,role,content}]}
GET  /sessions/{id}/tail?limit=30               → {messages: [{role, content}]}
POST /sessions/{id}/messages      {role, content} → {ok}
GET  /health                                    → {ok, skill_count}
```

Note: `messages` items carry a `content` string (already decoded from the
SQLite `content_json` column by the history store).

---

## 8. Key design decisions

### Why Go for the gateway?

- **Compiled binaries** — single ~16 MB static binary, no runtime deps.
- **Goroutine-based concurrency** — scheduler runner, SSE streaming, and
  request handling are natural goroutine workloads.
- **SQLite via pure Go** — `modernc.org/sqlite` needs no CGO, simplifying
  cross-compilation and distroless deployment.
- **Fast startup** — sub-second cold start vs Python's multi-second import chain.

### Why Python stays for skills?

- **Ecosystem** — skills use requests, yaml, qrcode, chromadb, and other
  packages with no Go equivalents of equal quality.
- **Dynamic skills** — `skill_creator` writes `.md` files and hot-registers
  them at runtime.  This is natural in Python, awkward in Go.
- **User extensibility** — operators write new skills in Python (`.py` or
  `.md`), the sidecar picks them up without recompilation.

### Why HTTP sidecar (not gRPC, not embedded Python)?

- **Debugging** — `curl http://localhost:8001/skills` works out of the box.
- **Independent scaling** — sidecar can be restarted without touching the gateway.
- **No CGO** — embedding Python via cgo would negate the static binary advantage.
- **Protocol simplicity** — plain JSON over HTTP; no code generation, no protobuf.

### Why `schedule_task` is native Go

The scheduler store (SQLite) lives on the Go side.  Making the skill call
back into Go via the sidecar would create a circular dependency
(gateway → sidecar → gateway).  `ScheduleTaskSkill` operates directly on
the `Store` struct, and the Python sidecar excludes `schedule_task.py`.

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

### Why vendored Python

- **Self-contained** — `docker compose up --build` works from any clone.
- **Version pinned** — exact Python code committed; no drift between projects.
- **CI-friendly** — tests run against vendored code without multi-repo checkout.

---

## 9. Error handling & resilience

| Scenario | Behaviour |
|---|---|
| Sidecar down | Gateway returns HTTP 502 on skill/memory calls; agent loop surfaces the error to the user. |
| LLM returns invalid JSON | Agent parser tries three recovery strategies; falls back to returning raw LLM output as the answer. |
| LLM calls unknown tool | Agent returns an error message listing valid tools; LLM can retry. |
| Skill execution fails | Sidecar returns `{result: {error: "..."}}` (HTTP 200); agent loop sees the error and can retry or answer. |
| Scheduler task fails | Runner captures the error, stores it in `last_result`, optionally notifies via Telegram. |
| Telegram stream breaks | Bot falls back to synchronous `/api/query` endpoint. |
| MaxSteps exceeded | Agent returns "could not finish" with partial debug log. |
| SQLite directory missing | `os.MkdirAll` creates it before opening the database. |
| Browser aborts SSE | `fetch` AbortController cancels the request; gateway detects `r.Context().Done()` and stops writing. |

---

## 10. Extending the system

### Adding a new Python skill

1. Create `sidecar/python/skills/my_skill.py` extending `BaseSkill`.
2. Import and register it in `sidecar/main.py` (`_build_skill_registry()`).
3. Restart the sidecar (or use `skill_creator` to hot-register a markdown
   skill at runtime without restart).

### Adding a new LLM backend

1. Create `internal/llm/my_backend.go` implementing `ChatClient`.
2. Add a case in `cmd/gateway/main.go` to construct it when
   `LLM_PROVIDER=my_backend`.

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
