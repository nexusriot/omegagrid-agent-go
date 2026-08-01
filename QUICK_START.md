# Quick Start — Deploy from Scratch

This guide walks you through a clean, end-to-end deployment of
**omegagrid-agent-go** on a Linux host using Docker Compose.

**Prerequisites**

- Linux host (or WSL2) with:
  - Docker Engine + `docker compose` plugin
  - `git`
  - `make` (optional — only needed for the host-side `omega` CLI)
- One of the supported LLM backends reachable from the host:
  - A local/remote **Ollama** server, OR
  - An **OpenAI** API key, OR
  - A **DigitalOcean Serverless Inference** model access key

---

## 1. Clone the repository

```bash
git clone https://github.com/nexusriot/omegagrid-agent-go.git
cd omegagrid-agent-go
```

## 2. Configure `.env`

```bash
cp .env.example .env
```

Edit `.env` and uncomment **exactly one** LLM provider block.

```dotenv
# --- LLM provider (pick ONE block) ---

# Option A: Ollama (default, local)
LLM_PROVIDER=ollama
OLLAMA_URL=http://host.docker.internal:11434   # or your remote Ollama
OLLAMA_MODEL=llama3:latest
OLLAMA_EMBED_MODEL=nomic-embed-text

# Option B: OpenAI
# LLM_PROVIDER=openai
# OPENAI_API_KEY=sk-...
# OPENAI_CHAT_MODEL=gpt-4o-mini
# OPENAI_EMBED_MODEL=text-embedding-3-small

# Option C: DigitalOcean Serverless Inference
# LLM_PROVIDER=digitalocean
# DIGITALOCEAN_API_KEY=do_inference_...
# DIGITALOCEAN_CHAT_MODEL=meta-llama/Llama-3.3-70B-Instruct
# DIGITALOCEAN_EMBED_MODEL=qwen3-embedding-0.6b

# --- UID/GID (avoids the data/ permission problem) ---
UID=1000
GID=1000

# --- Optional: Telegram bot ---
TELEGRAM_BOT_TOKEN=
BOT_AUTH_ENABLED=false
BOT_ADMIN_ID=0

# --- Optional: ports ---
FRONTEND_PORT=80
BACKEND_PORT=8000
```

Replace the placeholder UID/GID with your real values so the bind-mounted
`./data/` directory is owned by the same user inside the container as on the
host:

```bash
sed -i "s/^UID=.*/UID=$(id -u)/"  .env
sed -i "s/^GID=.*/GID=$(id -g)/"  .env
```

> **Note (bash):** `UID` is read-only in interactive bash, so a plain
> `UID=1000 docker compose up` won't propagate it. Writing it into `.env`
> as shown above is the reliable way.

## 3. Pre-create `./data/` on the host (REQUIRED)

> **This step is mandatory for Docker deployments.** If you skip it, the
> first `docker compose up` will fail with errors like
> `mkdir /app/data/chromem: permission denied` and
> `auth: unable to open database file (14)`.

When Docker encounters a bind-mount source that does not exist on the host,
the Docker daemon (running as root) creates it as `root:root` before
mounting it. The container then runs as `${UID:-1000}:${GID:-1000}` and
cannot write to that root-owned directory. The fix is to create `./data`
yourself first, so it is already owned by your user when the mount happens:

```bash
mkdir -p data
```

If `./data/` already exists from an earlier run that used `sudo` (or that
ran before the `user:` directive was added), re-own it:

```bash
sudo chown -R "$(id -u):$(id -g)" data/
```

The gateway auto-creates every required *subdirectory* (`chromem/`,
`skills/`, plus the parent dirs of the SQLite files) at startup, so the
single top-level `mkdir -p data` is all you need.

## 4. Build and start

```bash
docker compose up -d --build
```

Watch the gateway warm up:

```bash
docker compose logs -f gateway
```

## 5. Verify

```bash
# Health endpoint — should return {"ok":true, ...}
curl -s http://localhost:8000/health | jq .

# Web UI (chat, memory, skills, scheduler, health)
xdg-open http://localhost/ui/    # or open it in a browser
```

If you picked **Option A (Ollama)**, the embed probe inside `/health` will
fail until both models are pulled on the Ollama host:

```bash
ollama pull llama3
ollama pull nomic-embed-text
```

## 6. (Optional) Enable the Telegram bot

The `telegram-bot` container is part of the compose stack and starts
automatically when `TELEGRAM_BOT_TOKEN` is set in `.env`. To restrict it to
specific Telegram users:

```dotenv
BOT_AUTH_ENABLED=true
BOT_ADMIN_ID=123456789    # your Telegram numeric user ID
```

Then recreate the bot container:

```bash
docker compose up -d --force-recreate telegram-bot
```

Non-admin users will be ignored until the admin allowlists them.

## 7. (Optional) Use the `omega` CLI against the running gateway

The CLI can talk to a local or remote gateway. Build it once on the host
(needs Go ≥ 1.25 — see `go.mod`):

```bash
make cli                # produces ./bin/omega
export OMEGA_REMOTE=http://localhost:8000
./bin/omega skills list
./bin/omega ask "hello"
```

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `auth: unable to open database file (14)` | `./data/` owned by wrong user | `sudo chown -R $(id -u):$(id -g) data/` then `docker compose up -d --force-recreate` |
| `mkdir /app/data/chromem: permission denied` | Docker auto-created `./data/` as `root` because you skipped step 3 | `docker compose down && sudo chown -R $(id -u):$(id -g) data/ && docker compose up -d` |
| `nginx: host not found in upstream "gateway"` | Cascade — the `gateway` container crashed (see its logs) | Fix the gateway error (almost always the `./data/` permission issue above), then `docker compose up -d` again |
| `/health` shows `embed: ...` error | Embed model not pulled / wrong API key | `ollama pull nomic-embed-text` or check `*_API_KEY` and `*_EMBED_MODEL` in `.env` |
| Bot starts but ignores every message | `BOT_AUTH_ENABLED=true` and you're not the admin | Set the correct `BOT_ADMIN_ID`, or have the admin message the bot and run `/auth_add <telegram_id>` (`/auth_list` shows the allowlist) |
| Every answer is *"I had trouble processing that request. Please try rephrasing."* | Local model too small to emit the clean JSON envelope the agent requires (the reply is reported as `meta.fallback: true`) | Switch to a stronger model (`llama3:70b`, `gpt-4o-mini`, `Llama-3.3-70B-Instruct`) |
| Port 80 already in use | Another service occupies the port | Set `FRONTEND_PORT=8080` in `.env`, then `docker compose up -d --force-recreate frontend` |
| `DIGITALOCEAN_API_KEY required` on startup | `LLM_PROVIDER=digitalocean` without a key | Add `DIGITALOCEAN_API_KEY=...` to `.env` |

## Day-2 operations

```bash
# Update to latest code + rebuild images
git pull
docker compose pull && docker compose up -d --build

# Tail logs
docker compose logs -f gateway
docker compose logs -f telegram-bot

# Stop the stack (data is preserved in ./data/)
docker compose down

# Back up state
tar czf "backup-$(date +%F).tgz" data/

# Restore from a backup
tar xzf backup-YYYY-MM-DD.tgz
docker compose up -d
```

---

For the full configuration reference (every env var, every skill, every
endpoint) see [`README.md`](README.md).
