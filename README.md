# omegagrid-agent-go

Go rewrite of the [omegagrid-agent](https://github.com/nexusriot/omegagrid-agent) gateway, agent loop, scheduler, and
Telegram bot.  Skills and the vector / history stores remain in Python and
are exposed to the Go side via a small HTTP **sidecar**.  A React web UI
provides a browser interface for chat, memory, skills, and the scheduler.
All source is self-contained in this repository.

### /b/-lobster way on Go.

<p align="center" width="100%">
    <img width="70%" src="b-claw-go.png">
</p>

```
 Browser
    │ HTTP
    ▼
┌─────────────────────┐   HTTP    ┌──────────────────────┐   HTTP    ┌────────────────────┐
│  frontend (nginx)   │ ────────► │   Go gateway         │ ────────► │ Python sidecar     │
│  React UI  :80      │           │  /api/* + agent loop │           │ skills, memory,    │
│  proxies /api/*     │           │  scheduler    :8000  │           │ history    :8001   │
└─────────────────────┘           └──────────────────────┘           └────────────────────┘
         ▲                                    ▲
         │ Telegram Bot API                   │ long-polling
         │                        ┌───────────┘
         └──────────────────────  │  telegram-bot (Go)
                                  └────────────────────
                                            │
                                            └──► Ollama / OpenAI / OpenAI Codex
```

## Repository layout

```
cmd/
  gateway/             Go gateway entry point (HTTP API on :8000)
  telegram-bot/        Standalone Telegram bot binary
internal/
  agent/               Tool-calling loop (Run / RunStream)
  config/              Env-driven configuration loader
  httpapi/             chi router + all REST handlers + optional embedded UI
  llm/                 Ollama + OpenAI chat clients (chat_completions & responses)
  memory/              HTTP client → sidecar memory & history endpoints
  observability/       Mark-based timer (surfaces in RunResult.Meta)
  scheduler/           SQLite store, cron matcher, runner, schedule_task skill
  skills/              HTTP client → sidecar skill registry
  search/              Native web_search skill (DuckDuckGo HTML scrape, no API key)
  telegram/            Bot poller, command handlers, SQLite auth allowlist
web/                   React frontend — built by frontend container, served by nginx
  src/
    api/               TypeScript REST client, SSE stream helper, API types
    components/        Layout, SessionList, ChatBubble, ToolCard
    pages/             Chat, Memory, Skills, Scheduler, Health
    store/             Zustand chat + stream state
  package.json         Vite + React 18 + TypeScript + Tailwind project
sidecar/
  main.py              FastAPI shim (skills, memory, history)
  python/              Vendored Python packages (skills, memory, llm)
docker/
  frontend.Dockerfile  node:20-alpine build → nginx:1.27-alpine serve
  gateway.Dockerfile   golang:1.25-bookworm build → distroless runtime (~16 MB)
  telegram.Dockerfile  golang:1.25-bookworm build → distroless runtime (~15 MB)
  sidecar.Dockerfile   python:3.11-slim (~1.5 GB)
  nginx.conf           SPA routing + /api proxy + SSE streaming support
docker-compose.yml     4-service stack (frontend + gateway + sidecar + telegram-bot)
Makefile               web / build / build-all / dev-web targets
```

## Running with Docker Compose

```bash
cp .env.example .env
# edit .env  (LLM_PROVIDER, API keys, TELEGRAM_BOT_TOKEN, …)
docker compose up --build
```

| URL | What |
|---|---|
| `http://localhost/ui/` | React web UI (chat, memory, skills, scheduler, health) |
| `http://localhost/api/` | REST API proxied through nginx |
| `http://localhost:8000/` | Go gateway direct access |
| `http://localhost:8001/` | Python sidecar (internal; rarely needed directly) |

Set `FRONTEND_PORT` (default `80`) or `BACKEND_PORT` (default `8000`) in `.env`
to use different host ports.

## Running locally (no Docker)

```bash
# 1. Python sidecar
pip install -r sidecar/requirements.txt
PYTHONPATH=$PWD/sidecar/python SKILLS_DIR=$PWD/sidecar/python/skills \
DATA_DIR=$PWD/data \
    uvicorn sidecar.main:app --host 127.0.0.1 --port 8001

# 2. Go gateway  (serves /api/* and /health on :8000 — no UI)
SIDECAR_URL=http://127.0.0.1:8001 \
LLM_PROVIDER=ollama OLLAMA_URL=http://127.0.0.1:11434 \
DATA_DIR=$PWD/data \
    go run ./cmd/gateway
# → http://localhost:8000/api/...

# 3. Web dev server with hot-reload  (proxies /api → :8000)
make dev-web
# → http://localhost:5173/ui/

# (optional) Telegram bot
TELEGRAM_BOT_TOKEN=... GATEWAY_URL=http://127.0.0.1:8000 \
    go run ./cmd/telegram-bot
```

### Makefile shortcuts

```bash
make web        # cd web && npm run build  (populates web/dist/)
make build      # web + go build -o bin/gateway
make build-all  # web + build gateway + telegram-bot
make dev-web    # cd web && npm run dev  (Vite dev server, hot-reload)
```

## Web UI pages

| Page | Path | Description |
|---|---|---|
| Chat | `/ui/` | Streaming agent chat with live tool-call cards, session sidebar, markdown rendering |
| Memory | `/ui/memory` | Semantic search + manual add to the vector store |
| Skills | `/ui/skills` | Browse all registered skills with parameter schemas |
| Scheduler | `/ui/scheduler` | CRUD for cron tasks: create, enable/disable, delete, view last result |
| Health | `/ui/health` | Live gateway + provider status, auto-refreshes every 30 s |

## What's in Go vs Python

**Go** (compiled binaries):

- HTTP gateway, REST API, SSE streaming
- Agent tool-calling loop with JSON recovery
- Ollama + OpenAI (chat_completions & responses) LLM clients
- Cron scheduler (SQLite store, matcher, runner)
- Native `schedule_task` skill (talks to the Go scheduler store directly)
- Native `web_search` skill (DuckDuckGo HTML scrape, no API key required)
- Telegram bot with SQLite auth allowlist

**Python** (vendored under `sidecar/python/`):

- 21 built-in skills: weather, datetime, HTTP request, shell, SSH, web scrape, DNS, port scan, ping check, HTTP health, WHOIS, CIDR, base64, hash, math, IP info, UUID, password, QR code, cron explainer
- `skill_creator` — hot-register new skills at runtime without a restart
- Markdown pipeline skills (YAML frontmatter + multi-step chaining)
- `HistoryStore` — SQLite sessions + messages
- `VectorStore` — ChromaDB cosine-similarity memory with SHA256 + semantic deduplication
- Embeddings clients (Ollama and OpenAI)

## Environment variables

| Variable | Default | Description |
|---|---|---|
| `BACKEND_PORT` | `8000` | Gateway listen port |
| `FRONTEND_PORT` | `80` | nginx listen port (Docker Compose only) |
| `DATA_DIR` | `/app/data` | Directory for SQLite databases and ChromaDB |
| `SIDECAR_URL` | `http://127.0.0.1:8001` | Python sidecar base URL |
| `LLM_PROVIDER` | `ollama` | `ollama` \| `openai` \| `openai-codex` |
| `OLLAMA_URL` | `http://127.0.0.1:11434` | Ollama server URL |
| `OLLAMA_MODEL` | `llama3:latest` | Ollama chat model |
| `OLLAMA_EMBED_MODEL` | `nomic-embed-text` | Ollama embeddings model |
| `OLLAMA_TIMEOUT` | `120` | Ollama request timeout (seconds) |
| `OPENAI_API_KEY` | — | Required for `openai` / `openai-codex` provider |
| `OPENAI_BASE_URL` | `https://api.openai.com/v1` | OpenAI-compatible base URL |
| `OPENAI_CHAT_MODEL` | `gpt-4o-mini` | OpenAI chat model |
| `OPENAI_API_MODE` | auto | `chat_completions` \| `responses` (auto-selected for codex models) |
| `OPENAI_REASONING_EFFORT` | `medium` | Reasoning effort for the `responses` API |
| `OPENAI_TIMEOUT` | `120` | OpenAI request timeout (seconds) |
| `AGENT_CONTEXT_TAIL` | `30` | Messages loaded from history per run |
| `AGENT_MEMORY_HITS` | `5` | Vector memory results injected into context |
| `AGENT_MAX_STEPS` | `25` | Maximum tool-call steps per agent run |
| `SCHEDULER_DB` | `{DATA_DIR}/scheduler.sqlite3` | Scheduler database path |
| `SCHEDULER_TICK_SEC` | `60` | Scheduler poll interval (seconds) |
| `TELEGRAM_BOT_TOKEN` | — | Telegram bot token |
| `BOT_AUTH_ENABLED` | `false` | Enable Telegram user allowlist |
| `BOT_ADMIN_ID` | `0` | Telegram user ID of the admin |
| `SKILL_HTTP_TIMEOUT` | `30` | Timeout for HTTP-based skills (seconds) |
| `SKILL_SHELL_ENABLED` | `false` | Enable `shell_command` skill |
| `SKILL_SSH_ENABLED` | `false` | Enable `ssh_command` skill |
| `GATEWAY_URL` | `http://127.0.0.1:8000` | Telegram bot → gateway base URL |

## Frontend / gateway separation

The gateway binary is a pure JSON API: it serves only `/health` and `/api/*`
and contains no static-file serving, no UI redirect, and no `//go:embed`
assets.  The React UI is built and served exclusively by the **frontend**
nginx service, which also reverse-proxies `/api/*` and `/health` back to
the gateway.  For UI development without Docker, run `make dev-web`
(Vite at `:5173`, proxying `/api` to the gateway on `:8000`).
