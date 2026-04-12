# omegagrid-agent-go

Go rewrite of the omegagrid-agent gateway, agent loop, scheduler, and
Telegram bot.  Skills and the vector / history stores remain in Python and
are exposed to the Go side via a small HTTP **sidecar**.  The Python source
is vendored under `sidecar/python/`, so this repo is fully self-contained.

### /b/-lobster way  on Go.

<p align="center" width="100%">
    <img width="70%" src="b-claw-go.png"> 
</p>

```
┌──────────────┐    HTTP     ┌────────────────────┐   HTTP    ┌────────────────────┐
│ telegram-bot │ ──────────► │   Go gateway       │ ────────► │ Python sidecar     │
│   (Go)       │             │  /api/* + agent    │           │ skills, memory,    │
└──────────────┘             │  loop + scheduler  │           │ history (FastAPI)  │
                             └────────────────────┘           └────────────────────┘
                                       │
                                       └──► Ollama / OpenAI / OpenAI Codex
```

## Layout

```
cmd/gateway/             Go gateway entry point (HTTP API on :8000)
cmd/telegram-bot/        Standalone Telegram bot binary
internal/agent/          Tool-calling loop
internal/llm/            Ollama + OpenAI chat clients (chat_completions + responses)
internal/memory/         HTTP client to the sidecar's memory + history endpoints
internal/skills/         HTTP client to the sidecar's skill registry
internal/scheduler/      sqlite + cron parser + runner + native schedule_task skill
internal/telegram/       Bot, command handlers, sqlite-backed auth allowlist
internal/httpapi/        chi router + handlers for the gateway service
internal/config/         env-driven config loader
sidecar/main.py          FastAPI shim that exposes the vendored skills/memory
sidecar/python/          Vendored Python packages (skills, memory, llm)
docker/*.Dockerfile      Build images for gateway / telegram / sidecar
docker-compose.yml       3-service stack (sidecar + gateway + telegram-bot)
```

The sidecar imports skills, memory and llm clients from `sidecar/python/`,
which is checked into this repo.  No external project is required at build
or runtime.

## Running locally (no docker)

Two terminals:

```bash
# 1. Sidecar — uses the vendored Python tree under sidecar/python/
cd omegagrid-agent-go
pip install -r sidecar/requirements.txt
PYTHONPATH=$PWD/sidecar/python SKILLS_DIR=$PWD/sidecar/python/skills \
DATA_DIR=$PWD/data \
    uvicorn sidecar.main:app --host 127.0.0.1 --port 8001

# 2. Go gateway
SIDECAR_URL=http://127.0.0.1:8001 \
LLM_PROVIDER=ollama OLLAMA_URL=http://127.0.0.1:11434 \
DATA_DIR=$PWD/data \
go run ./cmd/gateway

# (optional) Telegram bot
TELEGRAM_BOT_TOKEN=... GATEWAY_URL=http://127.0.0.1:8000 \
go run ./cmd/telegram-bot
```

## Running with docker compose

```bash
cp .env.example .env
# edit .env (LLM keys, telegram token, ...)
docker compose up --build
```

- Gateway:  http://localhost:8000
- Sidecar:  http://localhost:8001 (internal API; you usually don't hit this directly)
- Health:   http://localhost:8000/health

## What's in Go vs Python

Go (this repo):

- HTTP gateway, agent loop, LLM clients, scheduler, Telegram bot,
  `schedule_task` skill (because the scheduler store lives in Go now).

Python (vendored under `sidecar/python/`):

- `skills/*` (incl. markdown skills, `skill_creator`) — minus `schedule_task`
- `memory/history_store.py`, `memory/vector_store.py`
- `llm/embeddings_client.py`, `llm/openai_client.py`

The Python sidecar exposes those packages via a small FastAPI service that
the Go gateway calls over HTTP.
