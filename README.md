# omegagrid-agent-go

Pure Go rewrite of the [omegagrid-agent](https://github.com/nexusriot/omegagrid-agent) platform.
The gateway, agent loop, scheduler, Telegram bot, all 51 built-in skills, vector memory, and conversation
history are compiled Go binaries — no Python sidecar.  A React web UI provides a browser
interface for chat, memory, skills, and the scheduler.  All source is self-contained in this
repository.

### /b/-lobster way on Go.

<p align="center" width="100%">
    <img width="70%" src="b-claw-go.png">
</p>

```
 Browser
    │ HTTP
    ▼
┌─────────────────────┐   HTTP    ┌──────────────────────────────────────────┐
│  frontend (nginx)   │ ────────► │   Go gateway                     :8000   │
│  React UI  :80      │           │                                          │
│  proxies /api/*     │           │  agent loop · scheduler · skills         │
└─────────────────────┘           │  vector memory · history · /mcp          │
                                  └──────▲───────────────────┬───────────────┘
                 HTTP /api/query[/stream]│                   │ HTTP
                                         │                   ▼
 Telegram Bot API ◄── telegram-bot (Go) ─┘    Ollama / OpenAI / OpenAI Codex /
                                              DigitalOcean / opencode
```

## Repository layout

```
cmd/
  gateway/              Go gateway entry point (HTTP API on :8000)
  cli/                  `omega` CLI — local or remote agent / skills / memory / scheduler
  telegram-bot/         Standalone Telegram bot binary
  migrate-vector/       One-shot ChromaDB → chromem-go migration tool
    main.go             Go importer (reads JSONL → writes chromem-go)
    export.py           Python exporter (reads ChromaDB → writes JSONL)
internal/
  agent/                Tool-calling loop (Run / RunStream); native tool calling + envelope fallback
  bootstrap/            Single New() that wires every service (used by gateway + cli); MCP connection manager
  config/               Env-driven configuration loader
  httpapi/              chi router + all REST handlers
    audit.go            GET /api/invocations, GET /api/invocations/{id}, POST …/replay
  llm/                  Ollama + OpenAI chat clients (chat_completions & responses), native tool calling
                        opencode.go reuses OpenAIChat for the opencode Zen relay
  mcp/                  MCP server (/mcp) + client for remote MCP servers (JSON-RPC 2.0)
  memory/               In-process history (SQLite) + vector store (chromem-go)
    client.go           Public API — CreateSession / AddMemory / SearchMemory / …
    history.go          SQLite sessions + messages + usage stats (modernc.org/sqlite, no CGO)
    audit.go            SQLite skill_invocations table — record / get / list
    vector.go           chromem-go store: dedup, namespaces, expiry, edit, export/import
    embeddings.go       Ollama (3-endpoint fallback) + OpenAI embeddings clients
  observability/        Mark-based timer (surfaces in RunResult.Meta)
  version/              The release version (one place; see Versioning)
  scheduler/            SQLite store + run history, cron matcher, runner (retries, prompt tasks), schedule_task
  skills/               In-process skill registry
    client.go           Public API — List() / Execute() / ExecuteContext()
    source.go           Markdown skill source editing (validate / save / delete)
    registry.go         Thread-safe sync.RWMutex skill map
    builtin/            Go implementations of 50 of the 51 built-in skills
      web.go            weather, http_request, web_scrape, http_health, ip_info
      network.go        dns_lookup, ping_check, port_scan, whois_lookup
      recon.go          tls_probe, http_headers, banner_grab, ptr_lookup, email_auth
      encode.go         base64_skill, hash_skill, uuid_gen, password_gen, cidr_calc, jwt_inspect
      eval.go           datetime_skill, math_eval (safe parser), cron_schedule
      reminder.go       reminder (echo message for one-shot scheduled tasks)
      exec.go           shell_command, ssh_command
      exec_unix.go      process-group kill on cancel, SO_BROADCAST (+ exec_other.go stub)
      qr.go             qr_generate
      rss.go            rss_fetch
      notify.go         notify (telegram / email / webhook, allowlisted)
      network2.go       http_timing, dns_compare, rdap_lookup, smtp_check, wake_on_lan, subnet_sweep
      traceroute*.go    traceroute (Linux implementation + stub)
      jsonquery.go      json_query
      textutil.go       time_convert, regex_test, text_diff, unit_convert
      csvquery.go       csv_query
      markdown.go       html_to_markdown
      knowledge.go      wikipedia, currency_convert, github
      workspace.go      file_list, file_read, file_write, pdf_text
    markdown/           Markdown skill loader + pipeline executor
    netguard/           SSRF guard: vets every outbound skill dial (after DNS resolution)
  search/               Native web_search skill (DuckDuckGo HTML scrape, no API key)
  telegram/             Bot poller, command handlers, SQLite auth allowlist
test/e2e/               End-to-end suite — drives a running gateway over HTTP
  e2e_test.go           Build tag `e2e`; imports no project packages
  mockllm/              Deterministic Ollama stand-in (chat queue + embeddings)
web/                    React frontend — built by frontend container, served by nginx
  src/
    api/                TypeScript REST client, SSE stream helper, API types
    components/         Layout, SessionList, ChatBubble, ToolCard, MemoryEditModal, SkillEditor,
                        TaskCreateModal, TaskRunHistory, UsagePanel
    lib/                Cron next-run preview (mirrors the Go matcher), time formatting, download, debounce
    pages/              Chat, Memory, Skills, Scheduler, Activity, Health
    store/              Zustand chat + stream state
  package.json          Vite + React 18 + TypeScript + Tailwind project
docker/
  frontend.Dockerfile   node:20-alpine build → nginx:1.27-alpine serve
  gateway.Dockerfile    golang:1.25-bookworm build → distroless runtime (~16 MB)
  telegram.Dockerfile   golang:1.25-bookworm build → distroless runtime (~12 MB)
  e2e.Dockerfile        mockllm + the compiled e2e test binary
  migrate.Dockerfile    Three-stage: Python exporter, Go builder, distroless importer (migration only)
  nginx.conf            SPA routing + /api proxy + SSE streaming support
docker-compose.yml           3-service stack (frontend + gateway + telegram-bot)
docker-compose.e2e.yml       e2e stack (mockllm + gateway + gateway-locked + runner)
docker-compose.migrate.yml   One-shot migration containers
scripts/e2e.sh         Runs the end-to-end suite (docker, or --host for a fast loop)
.github/workflows/     CI: gofmt, vet, staticcheck, race suite, web build, e2e
run_tests.sh           Runs the unit suite (local -race, or --docker)
Dockerfile.test        Image `make test` runs the unit suite in
CHANGELOG.md           Release notes
Makefile               init / web / build / build-all / cli / migrate-vector / vet / test / e2e / e2e-host / dev-web / vector-* targets
```

## Running with Docker Compose

```bash
cp .env.example .env
# edit .env  (LLM_PROVIDER, API keys, TELEGRAM_BOT_TOKEN, …)
mkdir -p data                       # REQUIRED — see note below
docker compose up --build
```

> **`mkdir -p data` is required before the first `up`.** Docker will
> otherwise create the bind-mount source as `root:root` and the containers
> (which run as `${UID:-1000}:${GID:-1000}`) will fail with
> `mkdir /app/data/chromem: permission denied` and
> `auth: unable to open database file (14)`. See the
> [Quick Start guide](QUICK_START.md) for the full from-scratch walkthrough.

| URL | What |
|---|---|
| `http://localhost/ui/` | React web UI (chat, memory, skills, scheduler, activity, health) |
| `http://localhost/api/` | REST API proxied through nginx |
| `http://localhost:8000/` | Go gateway direct access (`/api/*`, `/health`, `/mcp`; published on `127.0.0.1` only) |

Set `FRONTEND_PORT` (default `80`) or `BACKEND_PORT` (default `8000`) in `.env`
to use different host ports.

### `./data` permissions (UID/GID)

Both the gateway and telegram-bot containers run as `${UID:-1000}:${GID:-1000}`
and bind-mount `./data:/app/data`. If the host `./data/` directory is owned by
a different user (commonly `root`, if an earlier run created it before the
`user:` directive was added, or if you ran compose with `sudo`), the container
processes can't write to it and you will see cryptic errors such as:

```
auth: unable to open database file (14)
```

(SQLite error 14 = `SQLITE_CANTOPEN` — almost always a permissions/path problem.)

Fix it on the host:

```bash
# Make sure your real UID/GID are passed into compose
echo "UID=$(id -u)"  >> .env
echo "GID=$(id -g)" >> .env

# Re-own any pre-existing data directory
sudo chown -R "$(id -u):$(id -g)" data/

docker compose up -d --force-recreate
```

Some shells (notably bash) treat `UID` as read-only and won't export it into
`docker compose`'s environment — writing it into `.env` as shown above is the
reliable way.

### VPN on the host (WireGuard/OpenVPN): skills fail with "TLS handshake timeout"

If the host routes traffic through a VPN tunnel (e.g. WireGuard `wg0`, MTU
1420), container egress can hit an MTU blackhole: containers advertise a
1500-byte MSS, some servers send full-size TLS packets that don't fit in the
tunnel and are silently dropped. Symptom: outbound HTTPS skills (`weather`,
`web_scrape`, …) fail with `net/http: TLS handshake timeout` while the same
URL works from the host — and only for *some* destinations (CDNs like
Cloudflare clamp MSS server-side and keep working, which makes it confusing).

Fix: set the compose network MTU to your tunnel MTU and recreate the network:

```bash
echo "DOCKER_NETWORK_MTU=1420" >> .env   # wg0 default; check `ip link show wg0`
docker compose down && docker compose up -d
```

(Alternatively, add an MSS-clamping iptables rule on the host:
`iptables -t mangle -A FORWARD -o wg0 -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu`.)

## Running locally (no Docker)

The gateway auto-creates `DATA_DIR` and its subdirectories (`chromem/`, `skills/`,
and the parent dirs of `AGENT_DB` / `SCHEDULER_DB`) on first start, so no
`mkdir -p data/skills` step is required. If you prefer to pre-create them (e.g.
to set custom permissions, or to bind-mount the dir into Docker), `make init`
runs `mkdir -p data/skills` for you.

```bash
# 1. Go gateway  (serves /api/*, /health and /mcp on :8000 — no UI)
LLM_PROVIDER=ollama OLLAMA_URL=http://127.0.0.1:11434 \
DATA_DIR=$PWD/data \
    go run ./cmd/gateway
# → http://localhost:8000/api/...

# 2. Web dev server with hot-reload  (proxies /api → :8000)
make dev-web
# → http://localhost:5173/ui/

# (optional) Telegram bot  (auth is on by default — BOT_ADMIN_ID is required)
TELEGRAM_BOT_TOKEN=... BOT_ADMIN_ID=<your-telegram-user-id> \
GATEWAY_URL=http://127.0.0.1:8000 \
DATA_DIR=$PWD/data BOT_AUTH_DB=$PWD/data/telegram_auth.sqlite3 \
    go run ./cmd/telegram-bot
```

### Makefile shortcuts

```bash
make init         # mkdir -p data/skills  (run once before the first compose up)
make web          # cd web && npm run build  (populates web/dist/)
make build        # web + go build -o bin/gateway
make build-all    # web + build gateway + telegram-bot + migrate-vector + omega CLI
make cli          # go build -o bin/omega ./cmd/cli
make migrate-vector  # go build -o bin/migrate-vector ./cmd/migrate-vector
make dev-web      # cd web && npm run dev  (Vite dev server, hot-reload)
make vet          # go vet ./...
make test         # unit suite inside a Docker container (Dockerfile.test)
make e2e          # end-to-end suite: real gateway image + mock LLM, network-isolated
make e2e-host     # the same tests against local binaries (fast, not isolated)
make vector-migrate   # ChromaDB → chromem-go migration (see below)
```

### Tests

Two suites. `make test` (unit/integration) needs only Docker; with a local Go
toolchain, drive the same suite directly — that is the faster loop and the only
way to get the race detector, which the Alpine container build cannot run:

```bash
./run_tests.sh              # local, with -race (default)
./run_tests.sh --no-race    # local, faster
./run_tests.sh --docker     # what `make test` runs
```

`make e2e` is the end-to-end suite: it builds the gateway from the production
Dockerfile and drives the running service over HTTP, next to a mock model, on a
Docker network with no route off the host. It covers what unit tests cannot —
boot from environment variables, SQLite and chromem on disk (including across a
restart), the scheduler goroutine firing a task, SSE framing over a socket,
image attachments, the MCP endpoint, audit redaction, and the DNS-rebinding
Host guard on the real binary.

```bash
make e2e                          # hermetic: real gateway image + mock LLM in Docker
./scripts/e2e.sh -r TestMemory    # just the tests matching a pattern
./scripts/e2e.sh -k --logs        # keep the stack up and dump service logs
./scripts/e2e.sh --host           # same tests against local binaries, seconds per cycle
make e2e-host                     # the same thing
```

`TestNetworkIsolation` dials the internet and requires it to fail, so the suite
tells you immediately if it ever stops being hermetic. Only the image build
needs a network.

Coverage, by package:

| Package | Coverage | Notes |
|---|---|---|
| `internal/config` | 100% | |
| `internal/observability` | 100% | |
| `internal/skills/netguard` | 95% | dial policy, redirects, NAT64/6to4 translation |
| `internal/search` | 94% | DuckDuckGo HTML parsing driven through a fake `http.RoundTripper` |
| `internal/mcp` | 91% | JSON and SSE transports, both directions; notifications, `list_changed` |
| `internal/llm` | 91% | Ollama, OpenAI and opencode clients against `httptest`, including native tool calls in all three API shapes and cancellation during backoff |
| `internal/skills/markdown` | 88% | |
| `internal/bootstrap` | 87% | `New()` wires real stores; the MCP connection manager against a fake server that goes down, slows and changes its tools |
| `internal/telegram` | 87% | handlers run against a fake Telegram API (`NewBotAPIWithAPIEndpoint`) |
| `internal/scheduler` | 85% | two stores on one SQLite file, retries, time zones, prompt tasks, schema migration |
| `internal/skills/builtin` | 84% | everything except `weather` and `ip_info` (hardcoded third-party URLs, no seam); the bwrap sandbox, traceroute (real ICMP error queue), WoL and subnet sweep run on loopback; SMTP and DNS against in-test servers; third-party APIs via base-URL seams |
| `internal/agent` | 82% | the loop is driven end-to-end with scripted models, envelope and native |
| `internal/skills` | 82% | `skill_creator` YAML output is loaded back and compared; the editor's source routes are covered from `internal/httpapi` |
| `internal/httpapi` | 79% | router-level, real stores, scripted model, a fake embeddings server |
| `internal/memory` | 69% | vector store with a deterministic fake embeddings client; session search/rename/delete and usage stats are exercised through `internal/httpapi` |
| `cmd/cli` | 17% | the HTTP/parsing helpers and interspersed-flag parsing; the subcommands themselves call `os.Exit` |
| `cmd/gateway`, `cmd/telegram-bot`, `cmd/migrate-vector` | 0% | `main()` wrappers over `internal/` — exercised by the e2e suite instead |

About 1,320 Go tests and subtests (75% of statements), plus 25 end-to-end tests.

`test/e2e` carries no coverage figure: it drives the gateway from outside the
process, so nothing it exercises is attributed to the packages under test.

No test reaches the network: outbound calls go to `httptest` servers, injected
transports, or local listeners. `weather` and `ip_info` are the exception — their
endpoints are hardcoded, so only their argument handling is covered.

## Versioning

The current release is **0.6.0** — see [CHANGELOG.md](CHANGELOG.md). The
version is defined once, in `internal/version/version.go`, and reported by
`/health` (`"version"`), the Health page, the MCP `initialize` handshake (both
as server and as client), the gateway and bot startup logs, and
`omega version`. Releases are tagged with the bare version (`0.6.0`). A
release build can stamp a different value without editing the source:

```bash
go build -ldflags "-X github.com/nexusriot/omegagrid-agent-go/internal/version.Version=0.6.1" -o bin/gateway ./cmd/gateway
```

To cut a release, bump `Version` (and `web/package.json`), add a CHANGELOG
entry, and tag the commit.

## CLI (`omega`)

A single static Go binary that exposes the full agent surface — agent queries,
skills, vector memory, scheduler, and session history — without requiring the
HTTP gateway to be running.

```bash
go build -o bin/omega ./cmd/cli
```

By default the CLI wires services in-process (same construction as the
gateway), reading the same `LLM_PROVIDER`, `DATA_DIR`, etc. env vars — outside
Docker set `DATA_DIR=$PWD/data`, since the default is the container path
`/app/data`.  Set `OMEGA_REMOTE=http://localhost:8000` to hit a running gateway
over HTTP instead; `GATEWAY_API_TOKEN` is sent as the bearer token when set.
Local mode does not run scheduled tasks — only the gateway does.

Flags may come before or after the positional argument (`omega ask "..." --stream`
and `omega ask --stream "..."` are the same); a literal `--` ends flag parsing,
so `omega memory add -- "-starts with a dash"` stores the text as given.
`omega skills run` prints the skill's result as JSON and exits non-zero when the
skill fails — in remote mode too. Remote agent calls (`ask`, `skills run`) wait
up to 15 minutes; other remote calls time out after 120 s.

```bash
# Local mode (in-process services)
export DATA_DIR=$PWD/data
omega version
omega ask --stream "what's the weather in Berlin?"
omega ask --namespace user:vlad "what did I tell you about my editor?"
omega skills list
omega skills run weather --arg city=Berlin
omega memory search -k 10 "kubernetes notes"
omega memory add --meta tag=preference "vlad prefers tabs"
omega memory list --limit 50
omega memory edit <memory-id> --text "..."
omega memory export -o memories.jsonl
omega memory import memories.jsonl
omega memory delete <memory-id>
omega schedule list
omega schedule create --name daily-news --cron "0 9 * * *" --skill web_search --arg query=hackernews
omega schedule create --name standup --cron "0 15 * * *" --tz Europe/Berlin --skill reminder --arg message="join the call" --one-shot
omega schedule create --name digest --cron "0 8 * * *" --prompt "summarise today's calendar" --retries 2
omega schedule run 7
omega schedule history 7
omega schedule disable 7
omega schedule delete 7
omega session list
omega session export 42 > session-42.json

# Remote mode (talks to a running gateway)
OMEGA_REMOTE=http://localhost:8000 omega ask "..."
OMEGA_REMOTE=http://localhost:8000 GATEWAY_API_TOKEN=... omega ask "..."   # token-protected gateway
```

`omega ask --stream` renders SSE events with ANSI color when stdout is a TTY
and falls back to plain text when piped (so `omega ask … | jq` works).

## Web UI pages

| Page | Path | Description |
|---|---|---|
| Chat | `/ui/` | Streaming agent chat with live tool-call cards, markdown rendering; session sidebar with search (titles and message text), inline rename and delete |
| Memory | `/ui/memory` | Semantic search, manual add (namespace, expiry), browse-all list with namespace filter, namespace / expiry badges, **edit** (text, metadata, expiry), delete, JSONL **export** (optionally with embeddings) and **import** |
| Skills | `/ui/skills` | Browse all skills with parameter schemas and a builtin / markdown / remote badge; **skill playground** (▶ button) with pretty / raw / timing tabs; **markdown skill editor** (new / edit / delete) with live validation |
| Scheduler | `/ui/scheduler` | Create tasks that run a skill or ask the agent, with time zone (the "Next:" preview is evaluated in it), retries, one-shot and Telegram chat; enable/disable, delete, **Run now**, per-task **run history**, pending-retry display |
| Activity | `/ui/activity` | Skill/tool invocation audit log: filter by skill or session, errors-only toggle, JSON drawer, replay button. Auto-refreshes every 10 s |
| Health | `/ui/health` | Live gateway + provider status and the gateway version (auto-refreshes every 30 s) and a **usage** panel: queries and tool calls per hour/day over 24 h / 7 d / 30 d with errors highlighted, plus a top-skills table |

## What's in Go

Everything is compiled into the gateway binary (pure Go, no CGO, distroless runtime):

**Core platform:**
- HTTP gateway, REST API, SSE streaming
- Agent tool-calling loop using the provider's **native tool calling** where it is supported (`AGENT_TOOL_MODE`, see [Tool calling](#tool-calling)), with the JSON-envelope protocol and its recovery parser as the fallback; **optional parallel tool calls** (gated by `AGENT_PARALLEL_TOOLS`); each tool result is capped at 32 KiB in the LLM context
- Cancellation reaches running tools: when the client disconnects, the gateway shuts down or the CLI gets Ctrl-C, a running shell/SSH command, scan, HTTP request or pipeline stops instead of finishing first
- Ollama + OpenAI (chat_completions & responses) LLM clients
- Cron scheduler (SQLite store, matcher, runner) — run by the gateway only; atomic per-minute task claims; per-task time zones, agent-prompt tasks, persisted retries, run history and run-now (see [Scheduler](#scheduler))
- Telegram bot with SQLite auth allowlist
- `omega` CLI binary with local + remote modes (`cmd/cli`)
- **Skill playground** endpoint (`POST /api/skills/{name}/invoke`) for invoking any skill directly without going through the agent loop
- **MCP server + client** (`POST /mcp`): expose skills as MCP tools and consume tools from remote MCP servers — hand-rolled JSON-RPC 2.0, no SDK dependency

**Memory & history:**
- `HistoryStore` — SQLite sessions + messages (`modernc.org/sqlite`, CGO-free)
- `VectorStore` — chromem-go cosine-similarity memory with SHA256 + semantic dedup (0.08 threshold); namespaces, expiry, in-place edits, export/import (see [Memory management API](#memory-management-api))
- Embeddings clients — Ollama (3-endpoint fallback) + OpenAI

**Built-in skills (51 total):**

| Skill | Description |
|---|---|
| `weather` | Current weather via Open-Meteo (no API key) |
| `datetime_skill` | Current UTC date and time |
| `http_request` | Arbitrary HTTP GET / POST |
| `web_scrape` | Fetch URL and extract readable text (`max_chars` counts characters, not bytes) |
| `http_health` | HTTP endpoint health check with timing |
| `ip_info` | Geolocation for an IP via ip-api.com (free tier — plain HTTP by design) |
| `dns_lookup` | A / AAAA / MX / TXT / CNAME / NS lookup (`dig` + stdlib fallback). Both the domain and `record_type` are validated (any other type is rejected), so neither can smuggle `dig` options or an `@server` |
| `ping_check` | TCP connect reachability check (`port` must be 1–65535) |
| `port_scan` | Concurrent TCP port scanner (up to 1024 distinct ports, each within 1–65535; duplicates are dropped) |
| `whois_lookup` | WHOIS via raw TCP (IANA → authoritative server) |
| `tls_probe` | TLS handshake probe: cert chain, SANs, expiry, protocol, cipher, ALPN, verify status |
| `http_headers` | Status, redirect chain, response headers, cookie flags, security-header posture |
| `banner_grab` | TCP banner grab with optional probe bytes / TLS wrap |
| `ptr_lookup` | Reverse DNS (PTR) for an IPv4/IPv6 address |
| `email_auth` | SPF + DMARC TXT lookup; optional DKIM selector probes (max 10, shared `timeout`) |
| `base64_skill` | Encode / decode Base64 (decoding accepts standard and URL-safe alphabets, padded or not) |
| `hash_skill` | MD5 / SHA1 / SHA256 / SHA512 |
| `uuid_gen` | UUID v1 / v3 / v4 / v5 / v7 (any other version is an error) |
| `password_gen` | Cryptographically secure password generator |
| `cidr_calc` | CIDR network details + IP membership check |
| `jwt_inspect` | Decode JWT header/payload without verifying; flags `alg=none` and exp/nbf.  Args/result are redacted in the audit log |
| `math_eval` | Safe expression evaluator (custom AST parser, no `eval`) with Python semantics: precedence (`-2**2` = `-4`, `**` right-associative), `%` takes the divisor's sign (`-7 % 3` = `2`), `round` is half-to-even with optional digits, `min`/`max` are variadic, `factorial` needs an integer, and call arguments need commas. Exponent literals such as `1e-5`; `inf`/`nan` results are returned as strings |
| `cron_schedule` | Parse cron expression, explain it, show next N run times (UTC). Same rules as the scheduler (see [Cron expressions](#cron-expressions)); out-of-range fields are rejected rather than yielding an empty run list |
| `reminder` | Echo a message back; used by one-shot scheduled tasks to deliver reminders at a set time |
| `shell_command` | Local shell (requires `SKILL_SHELL_ENABLED=true`). Timeout clamped to 1–300 s, stdout/stderr capped at 256 KiB each. Its command blocklist is **not** a security boundary |
| `ssh_command` | Remote SSH command (requires `SKILL_SSH_ENABLED=true`). Same timeout clamp and output cap as `shell_command`; an `identity_file` argument must be a regular file of at most 64 KiB |
| `qr_generate` | QR code as base64 PNG. `error_correction` is case-insensitive; `box_size` is the exact pixels per module and `border` the exact quiet-zone width in modules |
| `skill_creator` | Hot-register new skills from YAML + Markdown at runtime. Skill names are validated on every action, so they always resolve inside `SKILLS_DIR`; built-in names (and `skill_creator` itself) cannot be created or overwritten. The frontmatter is written with a YAML encoder and loaded back before it replaces the old file, so a definition that would not parse is rejected and leaves the existing skill untouched |
| `rss_fetch` | Newest items from up to 10 RSS/Atom feeds, merged and sorted; `since` (RFC3339 or `24h`/`7d`) keeps only newer ones — the input to scheduled digests |
| `notify` | Send a message by Telegram, email (SMTP) or a named webhook. Recipients come only from allowlists (`NOTIFY_*`); `channel=list` shows what is configured |
| `http_timing` | DNS / connect / TLS / time-to-first-byte / transfer breakdown of a request, on fresh connections, 1–5 samples |
| `dns_compare` | Resolve a name through several resolvers (default 1.1.1.1, 8.8.8.8, 9.9.9.9 and the domain's own nameservers) and report disagreements |
| `rdap_lookup` | RDAP (structured WHOIS) for a domain, IP or AS number via rdap.org: registrar, status, dates, nameservers, network range |
| `traceroute` | Hop-by-hop path to an IPv4 host with UDP probes and the kernel's ICMP error queue — no root; Linux only |
| `smtp_check` | Connect to a domain's MX hosts (or one host): banner, ESMTP extensions, STARTTLS, certificate validity and expiry |
| `wake_on_lan` | Send a Wake-on-LAN magic packet (local and broadcast addresses only; refused when `SKILL_ALLOW_PRIVATE_NETWORKS=false`) |
| `subnet_sweep` | Live hosts in a CIDR range (≤ 256 addresses) by TCP connect; a refused connection counts as up |
| `json_query` | jq-style selection over JSON: paths, `[]`, pipes, `select(...)`, `map(...)`, `sort_by(...)`, `length`/`keys`/`add`/`min`/`max`/… |
| `time_convert` | Convert a time between IANA zones; add durations (`2h30m`, `3d`, `1w`) or business days; difference between two times |
| `regex_test` | Run an RE2 pattern over sample text: matches, positions, named groups, optional replacement |
| `text_diff` | Unified line diff of two texts with added / removed counts (≤ 3000 lines each) |
| `csv_query` | Filter (`where`), sort, select columns, group and aggregate (`count`, `sum:`, `avg:`, `min:`, `max:`) a CSV given inline or from the workspace |
| `unit_convert` | Length, mass, temperature, time, data size (SI and IEC), data rate, speed, area, volume, pressure, energy |
| `html_to_markdown` | A page (or HTML string) as Markdown — headings, links, lists, code, tables — keeping `<main>`/`<article>` and dropping navigation, scripts and forms |
| `wikipedia` | Article summary or search, any language edition (REST API, no key) |
| `currency_convert` | Convert with the ECB's daily euro reference rates (~30 currencies; cached for an hour) |
| `github` | Public repo facts, releases, issues (pull requests filtered out), pulls, or repository search; `SKILL_GITHUB_TOKEN` raises the rate limit |
| `file_list` / `file_read` / `file_write` | Files in the agent's workspace (`SKILL_WORKSPACE_DIR`, default `DATA_DIR/workspace`) and nowhere else: paths are resolved through `os.Root`, so `..`, absolute paths and symlinks cannot leave it. Reads are chunked (≤ 256 KB), writes ≤ 1 MB each within a total quota (`SKILL_WORKSPACE_MAX_MB`) |
| `pdf_text` | Text of a PDF from the workspace or a URL (≤ 20 MB), optionally a page range; scanned PDFs yield little text (no OCR) |

**Native agent skills (registered directly into the agent's tool table):**

| Skill | Description |
|---|---|
| `schedule_task` | Create / list / delete / delete_all / enable / disable cron tasks against the Go scheduler. `cron_expr` is validated at creation time (UTC) — a malformed expression, or a date no calendar has (`0 0 30 2 *`), is rejected instead of creating a task that never fires. `args` may be an object or a JSON-object string. Pass `one_shot=true` for a one-time task that is disabled the moment it is claimed to run (e.g. a reminder at a specific time) |
| `web_search` | DuckDuckGo HTML search (no API key); returns title / URL / snippet (a result with no snippet gets an empty one, never its neighbour's) |

Plus the tools every agent run gets: `vector_add` and `vector_search` (vector
memory, see [Memory management API](#memory-management-api)) and
`session_search`, a keyword search over earlier conversations (never the
current one) that returns a snippet around each match — for "what did I tell
you about…" when the fact was never stored with `vector_add`.

### Cron expressions

The scheduler, `schedule_task`, `cron_schedule` and the web UI's "Next:"
preview all read cron the same way:

- Five fields — minute, hour, day-of-month, month, day-of-week — evaluated in
  the task's **time zone** (`timezone`, an IANA name such as `Europe/Berlin`;
  UTC when unset). The UI preview shows each fire time in your local time and
  in the task's zone. Across a DST change a wall-clock minute can happen twice
  (fall back: the task fires both times) or not at all (spring forward:
  skipped). `cron_schedule` always answers in UTC.
- Each field takes `*`, `*/S`, `N`, `A-B`, `A-B/S`, `N/S` (N through the field
  maximum, so `5/15` is :05, :20, :35, :50) and comma lists. Month and weekday
  accept three-letter names (`jan`, `mon-fri`); weekday `7` is Sunday.
- Day-of-month and day-of-week follow Vixie cron: when both are restricted the
  task fires if **either** matches (`0 9 1 * 1` = every Monday and every 1st);
  a field that starts with `*` (including `*/2`) counts as unrestricted, so
  `0 0 */2 * 1` means odd-numbered days that are Mondays.
- A day-of-month that no selected month has (`0 0 30 2 *`, `0 0 31 4,6,9,11 *`)
  is rejected when the task is created. Feb 29 is accepted.

### Scheduler

A task runs either a **skill** (`skill` + `args`) or an **agent prompt**
(`prompt`: the agent answers it with every tool available, e.g. "every morning
summarise the HN front page"); a prompt task's `skill` reads `agent`.

| Field | Meaning |
|---|---|
| `timezone` | IANA zone the cron is evaluated in; `""` = UTC |
| `one_shot` | Disabled the moment it is claimed to run (reminders) |
| `max_retries`, `retry_delay_sec` | A failed cron run is retried up to N times (0–10), the first after `retry_delay_sec` (default 60), doubling each time, capped at a day. The pending retry is stored in the database, so it survives a restart, and only one process runs it. Disabling the task cancels it |
| `notify_telegram_chat_id` | Sends the outcome to Telegram — once it is final, not on every failed attempt |

Every execution is recorded in a **run history** (`task_runs`, the newest 200
per task): trigger (`cron`, `retry`, `manual`), attempt, timing, ok/failed and
the result. **Run now** executes a task immediately without touching its
schedule (`last_run_at` is not moved, a one-shot stays armed, no retries are
armed).

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/scheduler/tasks` | Create (`name`, `cron_expr`, `skill`+`args` or `prompt`, `timezone`, `one_shot`, `max_retries`, `retry_delay_sec`, `notify_telegram_chat_id`); 400 on any invalid field |
| `POST` | `/api/scheduler/tasks/{id}/run` | Run now; returns the run record |
| `GET` | `/api/scheduler/tasks/{id}/runs?limit=50` | Run history, newest first |
| `POST` | `/api/scheduler/tasks/{id}/enable` \| `/disable` | Toggle |

```bash
omega schedule create --name digest --cron "0 8 * * 1-5" --tz Europe/Berlin \
  --prompt "Summarise the top 5 Hacker News stories" --retries 2
omega schedule run 7          # run now (in-process in local mode)
omega schedule history 7
omega schedule disable 7
omega schedule enable 7
```

### Tool calling

`AGENT_TOOL_MODE` picks how the model calls tools:

| Value | Behaviour |
|---|---|
| `auto` (default) | The provider's **native tool-calling API** when the client supports it (Ollama `/api/chat` tools, OpenAI chat-completions `tools`, the Responses API's function calls). The first time a backend rejects tools ("model does not support tools"), the process switches to the envelope protocol for good |
| `native` | Native only; a backend that rejects tools fails the run |
| `envelope` | The model writes the JSON envelope (`{"type":"tool_call",…}`) as text, parsed by the recovery parser — the original protocol |

In native mode a plain-text reply **is** the final answer (in envelope mode the
same reply was an unparseable turn and a "please rephrase" fallback), and a
model that writes the envelope as text anyway is still understood. Each result
goes back to the model tied to its call id. `meta.tool_mode` in every response
says which mode ran.

**Markdown / pipeline skills:**
Dynamic skills defined as `*.md` files in `SKILLS_DIR` (default `DATA_DIR/skills`).
`skill_creator` writes new `.md` files and hot-registers them without a restart.
The free-text instructions block of a markdown skill (everything after the YAML
frontmatter) is surfaced in the agent's system prompt (first 5 lines) and drives
prompt-only skill execution.

Rules the loader and `skill_creator` enforce:
- A markdown file whose name collides with a built-in skill is skipped at load
  (logged), so it can never shadow e.g. `http_request`.
- Pipelines nest at most 5 levels deep and may not call themselves. A pipeline
  file may have at most 20 steps, and one top-level execution may run at most
  200 skill steps across all nesting levels; past that each step reports a
  "call budget exhausted" error instead of fanning out further.
- A skill's `timeout` is capped at 300 s.
- The frontmatter's closing `---` must be on a line of its own.
- **Behaviour change:** values substituted into an `endpoint` URL are
  percent-encoded, so a placeholder can no longer inject `/` path segments or
  change the host. Put path structure in the template itself, not in a
  parameter value.

Markdown skills can also be written in the web UI's skill editor, which
validates as you type. The routes behind it (writes are disabled together with
the playground by `PLAYGROUND_DISABLED`):

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/skills` | Every skill, each with `kind`: `builtin`, `markdown` or `remote` (MCP) |
| `GET` | `/api/skills/{name}/source` | A markdown skill's file content (404 for other kinds) |
| `POST` | `/api/skills/validate` | `{content}` → `{ok:true, skill}` or `{ok:false, error}`; saves nothing |
| `POST` | `/api/skills/source` | `{content}` → validate, write `SKILLS_DIR/<name>.md` atomically, hot-register; `{skill, created}` |
| `DELETE` | `/api/skills/{name}` | Delete a markdown skill |

Outbound skills (built-in and markdown `endpoint` steps) go through the
`netguard` dial check — see [Security](#security).

## MCP (Model Context Protocol)

The gateway speaks MCP in both directions over a hand-rolled JSON-RPC 2.0
transport (no SDK dependency).

**As an MCP server** — every registered skill (built-in, dynamic, native, and
any consumed remote tools) is exposed as an MCP tool at `POST /mcp`. Point any
MCP client (Claude Desktop, Cursor, …) at the **gateway** port — the frontend
nginx proxies `/ui/`, `/health` and `/api/` but deliberately not `/mcp`, so use
`http://localhost:${BACKEND_PORT:-8000}/mcp` (compose publishes it on
`127.0.0.1` only), not the UI port. When `GATEWAY_API_TOKEN` is set, add
`-H "Authorization: Bearer $GATEWAY_API_TOKEN"` to the requests below:

```bash
# initialize
curl -s localhost:8000/mcp -d '{"jsonrpc":"2.0","id":1,"method":"initialize",
  "params":{"protocolVersion":"2025-06-18"}}'
# list tools
curl -s localhost:8000/mcp -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
# call a tool
curl -s localhost:8000/mcp -d '{"jsonrpc":"2.0","id":3,"method":"tools/call",
  "params":{"name":"weather","arguments":{"city":"Berlin"}}}'
```

Implemented methods: `initialize`, `tools/list`, `tools/call`, `ping`, and the
`notifications/initialized` notification. Any message without an `id` member
is a notification and gets `202 Accepted` with no body; `"id": null` is an
ordinary request. Malformed `tools/call` params return `-32602`. JSON-RPC batch
arrays are not supported. Disable the endpoint with `MCP_SERVER_DISABLED=true`.

**As an MCP client** — set `MCP_SERVERS` to consume tools from remote MCP
servers (streamable-HTTP transport). Each server's tools are registered into
the skill registry namespaced as `<name>_<tool>`, so the agent, the scheduler,
the skill playground, and the gateway's own MCP server can all use them. A
remote tool whose namespaced name is already taken — a built-in (a server
named `http` offering `request` would otherwise replace `http_request`), a
native tool (`web_search`, `vector_add`, …), a markdown skill or an earlier
server's tool — is skipped and logged:

```bash
MCP_SERVERS='weather=https://example.com/mcp,docs=https://docs.host/mcp|Authorization: Bearer TOKEN'
```

Servers are dialled **in parallel**, and startup waits at most
`MCP_CONNECT_WAIT_SEC` (default 10) for them. A server that is down at boot is
retried in the background with backoff (5 s doubling to 5 min) until it
answers, so its tools appear without a restart. Connected servers are re-listed
every `MCP_REFRESH_SEC` (default 300; 0 = never) and at once when a server sends
`notifications/tools/list_changed`: new tools are registered, changed ones
replaced, vanished ones withdrawn. A refresh that fails keeps the tools already
registered. Remote schemas whose `type` is a union such as `["string","null"]`
keep their parameters (the first non-null type is used). The client re-initializes once when a server
reports its session expired (HTTP 404), accepts SSE responses that interleave
notifications before the result, and flattens remote tool descriptions to one
line of at most 300 characters before they reach the agent's prompt.

## Memory management API

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/memory/add` | `{text, meta?, namespace?, ttl_seconds?}` — store a memory (deduplicated within its namespace) |
| `POST` | `/api/memory/search` | `{query, k?, namespaces?}` — semantic search; `namespaces` (e.g. `["user:alice", ""]`) restricts it, omitted searches all |
| `GET` | `/api/memory?limit=100&offset=0[&namespace=NS][&include_expired=1]` | List newest-first (`limit=0` → all); `namespace=` (empty) is the shared namespace |
| `GET` | `/api/memory/namespaces` | Namespaces with their live memory counts |
| `GET` | `/api/memory/{id}` | Fetch one memory by ID |
| `PUT` | `/api/memory/{id}` | `{text?, meta?, ttl_seconds?}` — edit in place (id and `created_at` kept; a new text is re-embedded; a `meta` key set to `null` is removed; `ttl_seconds` > 0 sets the expiry from now, ≤ 0 removes it). 400 when the new text duplicates another memory |
| `DELETE` | `/api/memory/{id}` | Delete a memory by ID |
| `GET` | `/api/memory/export[?embeddings=1]` | Every memory as JSON Lines (`{id, text, metadata, embedding?}`) |
| `POST` | `/api/memory/import` | A JSON Lines export (or a JSON array of records) → `{report: {imported, duplicates, existing, failed, errors}}`; up to 64 MiB |

**Namespaces.** Memories live in the shared namespace (`""`) unless a caller
names one. An agent run in namespace `N` (`/api/query` with `"namespace": "N"`,
`omega ask --namespace N`, or a Telegram chat under `BOT_MEMORY_SCOPE`) stores
its `vector_add` facts in `N` and recalls from `N` plus the shared namespace —
never another user's. Deduplication is per namespace, so the same fact may exist
in two of them.

**Expiry.** A memory can carry `expires_at` (unix seconds). It is set from
`ttl_seconds` / `--ttl` / the agent's `vector_add` `ttl_days`, or from
`MEMORY_DEFAULT_TTL_DAYS` when the caller gives none (0, the default, means
never). Expired memories disappear from search and listing at once; the gateway
deletes them hourly.

**Export / import.** An export without embeddings is portable across embeddings
models (the importer re-embeds). With `?embeddings=1` the vectors are reused
when they have the target collection's width, which makes an import into the
same setup fast. Records keep their ids unless the id is already taken; the
usual dedup applies.

**Restart behaviour.** The exact-duplicate (SHA256) index is rebuilt from the
stored documents when the gateway starts, and listing no longer needs the
embeddings backend — both enumerate the collection with a fixed probe vector of
the recorded embedding width (`omegagrid-<collection>.dim.json` next to the
chromem data).

From the CLI:

```bash
omega memory add "office wifi is guest-5" --namespace team --ttl 720h
omega memory search "wifi" --namespace team --namespace ""
omega memory list --limit 50 --namespace team
omega memory edit <memory-id> --text "office wifi is guest-6" --meta source=it --ttl 0
omega memory namespaces
omega memory export --embeddings -o memories.jsonl
omega memory import memories.jsonl          # "-" reads stdin
omega memory delete <memory-id>
```

## Migrating from a previous installation (Python sidecar → pure Go)

If you have an existing deployment with ChromaDB vector data, run the one-shot
migration before switching to the new stack.  No Python installation required
on the host — both steps run inside Docker.

```bash
# Run the full migration in one command:
make vector-migrate
```

This runs two containers sequentially:

1. **`migrate-export`** (Python 3.11 + chromadb==0.5.5) — reads
   `data/vector_db/` and writes every record to `data/vector_db.jsonl`.
   Embeddings are exported verbatim — no re-embedding, no model-version drift.

2. **`migrate-import`** (static Go binary) — reads the JSONL and writes a fresh
   chromem-go database at `data/chromem/`.

```
data/
  vector_db/        ← legacy ChromaDB (read by exporter; keep as backup)
  vector_db.jsonl   ← intermediate handoff file (safe to delete after migration)
  chromem/          ← new chromem-go database (used by gateway)
```

**Guard rails:**
- Refuses to overwrite a non-empty `data/chromem/`.  Remove it and re-run if needed.
- An empty source collection is a no-op (exits cleanly with "nothing to import").

**Manual steps (if `make` is not available):**

```bash
# Step 1 — export
docker compose -f docker-compose.migrate.yml run --rm --remove-orphans migrate-export

# Step 2 — import
docker compose -f docker-compose.migrate.yml run --rm --remove-orphans migrate-import

# Verify record count
wc -l data/vector_db.jsonl

# Clean up intermediate file
rm data/vector_db.jsonl
```

**Vector database location:**  the gateway reads the vector database from
`AGENT_VECTOR_DIR` (default `DATA_DIR/chromem`) — the new chromem-go directory,
not the legacy ChromaDB `data/vector_db/` path used by the Python sidecar.  If
you customised `AGENT_VECTOR_DIR` in `.env`, point it at `data/chromem`.

## LLM provider: opencode Zen

[opencode Zen](https://opencode.ai/docs/zen/) is an OpenAI-compatible relay in
front of a curated model set. One key from <https://opencode.ai/auth> covers
both tiers — `go` (the flat-rate subscription) and `zen` (pay-as-you-go
credits) — and the provider alias picks which one you hit:

| `LLM_PROVIDER` | Base URL | Default model |
|---|---|---|
| `opencode`, `opencode-go` | `https://opencode.ai/zen/go/v1` | `kimi-k2.7-code` |
| `opencode-zen`, `zen` | `https://opencode.ai/zen/v1` | `claude-sonnet-5` |

The relay describes two of its rejections precisely enough to act on, so the
client fixes the request and replays it instead of failing the turn. Each fix
sticks for the life of the process, so only the first call pays for it:

| Response | What the client does | Pin it with |
|---|---|---|
| `410 ModelDeprecated` — names its successor in `metadata.replacement` | Switches to the successor and retries | `CHAT_MODEL` / `OPENCODE_CHAT_MODEL` |
| `400 invalid temperature: only 1 is allowed for this model` | Stops sending `temperature`, leaving the model on its own default | `OPENAI_TEMPERATURE=none` |

Each shows up as one log line:

```
llm: model "kimi-k2.6" is deprecated, retrying with "kimi-k2.7-code" (set the chat-model option to silence this)
llm: model "kimi-k2.7-code" rejected temperature 0.2, retrying without it (set OPENAI_TEMPERATURE=none to silence this)
```

At most two such fixes are applied per call, and neither is ever repeated, so
a relay that keeps rejecting a request surfaces its error rather than looping.

Minimal `.env` for the Go tier:

```dotenv
LLM_PROVIDER=opencode
OPENCODE_API_KEY=sk-...

# The relay has no /embeddings endpoint, so vector memory needs its own
# backend. This one keeps everything remote:
EMBED_PROVIDER=openai
OPENAI_API_KEY=sk-...
OPENAI_EMBED_MODEL=text-embedding-3-small
```

Or embed locally instead, with no second cloud key:

```dotenv
LLM_PROVIDER=opencode
OPENCODE_API_KEY=sk-...
OLLAMA_URL=http://host.docker.internal:11434
OLLAMA_EMBED_MODEL=nomic-embed-text   # ollama pull nomic-embed-text
```

`EMBED_PROVIDER` is optional: left unset it falls back to Ollama, which is
what the second example relies on.

Run it locally without Docker:

```bash
LLM_PROVIDER=opencode OPENCODE_API_KEY=sk-... go run ./cmd/gateway
curl -s localhost:8000/health | jq
# → {"provider":"opencode","chat_base":"https://opencode.ai/zen/go/v1",
#    "chat_model":"kimi-k2.7-code","embed_provider":"ollama", ...}
```

`embed_provider` reports the backend embeddings actually run on, so it reads
`ollama` here even though `LLM_PROVIDER` is `opencode`:

```bash
# with no Ollama running and EMBED_PROVIDER unset
# → "embed_ok": false, "embed_error": "... 127.0.0.1:11434: connection refused"
```

Pick another model with `OPENCODE_CHAT_MODEL` (list them with
`curl -H "Authorization: Bearer $OPENCODE_API_KEY" https://opencode.ai/zen/go/v1/models`).
The agent loop is a strict JSON protocol, but narration is tolerated: the
parser strips `<think>` / `<reasoning>` preambles and ```json fences before
scanning for the envelope, so models that talk before they answer
(`minimax-m3`, for one) work even when the preamble itself quotes JSON.
`kimi-k2.7-code`, `kimi-k3` and `glm-5.3` return a bare object with no preamble at
all, which is still marginally cheaper.

### Troubleshooting

| Symptom | Cause |
|---|---|
| `400 MissingSessionID` | Something stripped the `x-opencode-session` header the client sends on every request |
| `401 Invalid API key` | Key is wrong, or `OPENCODE_API_KEY` never reached the process |
| `401 CreditsError` | Go-tier key against the `zen` tier — that tier bills credits separately |
| `embed_ok: false` in `/health` | No Ollama reachable and no `EMBED_PROVIDER` set — opencode cannot serve embeddings. The embed probe is cached for 30 s (5 s timeout), so a fix can take up to 30 s to show |

## Telegram bot

Commands (anything else is sent to the agent as a plain question):

| Command | What |
|---|---|
| `/start` | Show help (and, while auth is on, your Telegram ID); resets the chat's session only for an authorized sender |
| `/ask <question>` | Ask the agent explicitly (answer is suffixed with model + step count) |
| `/new` | Start a new session |
| `/skills` | List available skills |
| `/auth_add <telegram_id>` | Admin only — allow a user |
| `/auth_list` | Admin only — list allowed users |

Auth is **on by default**: the bot refuses to start unless `BOT_ADMIN_ID` is set
or `BOT_AUTH_ENABLED=false` is given explicitly. Authorization is by the
sender's Telegram user ID, so allowlisting a group chat's ID does not authorize
its members; unauthorized users get an "Access denied" reply. Up to 8 updates
are handled concurrently, and messages within one chat are processed in order.
If a streamed answer breaks after the agent has started, the bot reports the
interruption instead of re-running the query. `BOT_MEMORY_SCOPE` decides whose
memories a conversation sees: `shared` (default — everyone shares one memory),
`user` (namespace `telegram:user:<id>`, the same across a user's chats) or
`chat` (`telegram:chat:<id>`). Scoped conversations still recall the shared
namespace. Long answers are split into
messages of at most 4000 UTF-16 code units (Telegram's own unit, in which an
emoji counts twice). Replies are plain text — no Markdown parse mode is set.

## Security

The gateway runs tools that reach the network and (optionally) a shell, so treat
its API as privileged.

- **Bind address.** The gateway listens on every interface (it has to inside a
  container); compose publishes the gateway and the UI on `127.0.0.1` only.
  Keep it that way unless you also set a token. A gateway started without
  `GATEWAY_API_TOKEN` logs a warning saying the API is unauthenticated.
- **DNS rebinding.** Without a token, a web page can re-point its own domain at
  `127.0.0.1` and reach the gateway as a "same-origin" page. So while
  `GATEWAY_API_TOKEN` is unset, the gateway serves only requests whose `Host`
  is an IP address, a single-label name (`localhost`, the compose service name
  `gateway`) or a `*.localhost` name; anything else gets 403. Reaching the
  token-less gateway (or the UI through nginx) by a DNS name such as
  `agent.example.org` or `box.lan` needs that name in `GATEWAY_ALLOWED_HOSTS`
  — or a token, which makes the check unnecessary.
- **`GATEWAY_API_TOKEN`.** When set, `/api/*` and `/mcp` require
  `Authorization: Bearer <token>`; `/health` stays open. The Telegram bot and
  `omega` remote mode send it automatically from the same variable; the web UI
  prompts for it on the first 401 and keeps it in the browser's `localStorage`.
  Set it whenever the gateway is reachable beyond loopback.
- **CORS / Origin.** No wildcard CORS. `CORS_ALLOWED_ORIGINS` (comma-separated)
  lists cross-origin callers; empty means same-origin only, which is all the
  bundled UI (served through nginx) needs. State-changing requests that carry a
  foreign `Origin` header are rejected with 403; clients that send no `Origin`
  (curl, the CLI, the bot) are unaffected.
- **Request limits.** Bodies are capped at 4 MiB; the server uses a 60 s read
  timeout and 120 s idle timeout. SIGTERM cancels in-flight requests, and
  `/api/query` aborts the agent run when the client disconnects.
- **Outbound skill connections** (`netguard`). Every dial is checked after DNS
  resolution (redirects included): link-local / cloud metadata
  (`169.254.0.0/16`, `fe80::/10`, `fd00:ec2::254`), unspecified and multicast
  addresses are always refused. `SKILL_ALLOW_PRIVATE_NETWORKS=false` also
  blocks loopback, RFC1918, ULA and CGNAT. NAT64 (`64:ff9b::/96`) and 6to4
  (`2002::/16`) addresses are vetted as the IPv4 address they embed. The check is not enforced on the
  final target when an HTTP(S) proxy is configured through the environment —
  it then vets the proxy address. URL-taking skills accept only `http`/`https`;
  remote response bodies are read up to 2 MiB; skill timeouts are clamped
  (60 s; `port_scan` 10 s per port; shell/SSH 1–300 s).
- **Shell / SSH skills** are off by default. The `shell_command` blocklist is a
  guard against accidents, not a security boundary — enable these skills only
  for trusted users.
- **Shell sandbox.** `SKILL_SHELL_SANDBOX=bwrap` runs every `shell_command`
  inside [bubblewrap](https://github.com/containers/bubblewrap): the whole
  filesystem read-only, a private `/tmp` (the working directory), its own
  PID/IPC/UTS/user namespaces and **no network** unless
  `SKILL_SHELL_SANDBOX_NETWORK=true`. `SKILL_SHELL_SANDBOX_WRITABLE=/path` adds
  one read-write directory and makes it the working directory. If bwrap is
  missing the command is refused, not run unsandboxed. bwrap needs
  unprivileged user namespaces — available on most Linux hosts, usually not
  inside an unprivileged Docker container — so this is for running the gateway
  (or `omega`) natively. Results report `"sandbox": "bwrap"` or `"none"`.
- **SSH host keys.** `ssh_command` checks host keys against
  `SKILL_SSH_KNOWN_HOSTS` (default `DATA_DIR/ssh_known_hosts`).
  `SKILL_SSH_HOST_KEY_POLICY=accept-new` (default) trusts a host the first time
  and records its key; a changed key is refused. `strict` accepts only hosts
  already in the file (pre-populate it with `ssh-keyscan`); `insecure` skips
  the check (the old behaviour).
- **Secrets in results.** `password_gen` and `jwt_inspect` results reach the
  model for the current run only: the debug log, the stream's `tool_result`
  event, the session history (`/api/sessions/{sid}/messages`) and the audit log
  all store a redacted stand-in. `shell_command` / `ssh_command` arguments are
  redacted in the audit log but shown in the stream and debug log, so an
  operator can see what ran.
- **Telegram** — auth is on by default (see [Telegram bot](#telegram-bot)).

## Upgrade notes / behaviour changes

Behaviour changes in 0.6.0 (upgrading from 0.5.x — the full list is in [CHANGELOG.md](CHANGELOG.md)):

- `BOT_AUTH_ENABLED` now defaults to `true`. Set `BOT_ADMIN_ID`, or
  `BOT_AUTH_ENABLED=false` to keep an open bot.
- Telegram authorization checks the **sender's user ID**; an allowlisted group
  chat ID no longer authorizes everyone in that group. Add users individually.
- CORS is no longer `*`. Browser clients on another origin need
  `CORS_ALLOWED_ORIGINS`.
- Markdown skill `endpoint` placeholder values are now URL-escaped (see
  [Markdown / pipeline skills](#whats-in-go)).
- `omega` local mode no longer executes due scheduled tasks; only the gateway
  runs the scheduler. Task claims are atomic, so several processes can share
  the scheduler DB, and `last_run_at` records the scheduled minute.
- `AGENT_TOOL_MODE` defaults to `auto`: models are now called through the
  provider's native tool-calling API where available. Set
  `AGENT_TOOL_MODE=envelope` for the previous behaviour.
- `ssh_command` now verifies host keys (`SKILL_SSH_HOST_KEY_POLICY=accept-new`):
  the first connection to a host records its key, and a later key change is
  refused. `insecure` restores the old behaviour.
- Remote MCP servers are connected in the background; a slow one no longer
  delays startup by more than `MCP_CONNECT_WAIT_SEC`.
- The scheduler and history databases gain columns/tables (`timezone`,
  `prompt`, retries, `task_runs`, session `title`); they are added on start.
- With no `GATEWAY_API_TOKEN`, requests whose `Host` is a dotted DNS name are
  refused (DNS-rebinding guard). If you open the UI as `http://box.lan/`, add
  `GATEWAY_ALLOWED_HOSTS=box.lan` or set a token. IPs and `localhost` keep
  working.
- `AGENT_MEMORY_HITS=0` now really turns memory injection off (it used to
  fall back to 5).
- The `*_DISABLED` / `*_ENABLED` switches also accept `on`.
- Cron: `N/S` now steps (`5/15` = :05/:20/:35/:50, it used to fire at :05
  only), and a `*/N` day field counts as unrestricted (Vixie), which changes
  expressions that combine one with a weekday. See
  [Cron expressions](#cron-expressions).
- `math_eval` follows Python for `%` with negative operands, `round` (half to
  even) and `factorial` (integers only), and rejects arguments with no comma
  between them.
- `omega skills run` prints the skill's result itself (not the playground
  envelope) and exits non-zero on failure in remote mode as well.
- The migration containers now run as the host UID:GID. A `data/chromem`
  created by an earlier (root) migration may need
  `sudo chown -R $(id -u):$(id -g) data/chromem`.

## Environment variables

Boolean switches accept `true`/`1`/`yes`/`on` (any case); `SKILL_ALLOW_PRIVATE_NETWORKS`,
which defaults to on, is turned off by `false`/`0`/`no`/`off`.

| Variable | Default | Description |
|---|---|---|
| `BACKEND_PORT` | `8000` | Gateway listen port |
| `FRONTEND_PORT` | `80` | nginx listen port (Docker Compose only) |
| `DOCKER_NETWORK_MTU` | `1500` | Compose network MTU (Docker Compose only). Set to your VPN tunnel MTU (e.g. `1420` for WireGuard) when the host routes through a VPN |
| `DATA_DIR` | `/app/data` | Root directory for all persistent data |
| `LLM_PROVIDER` | `ollama` | `ollama` \| `openai` \| `openai-codex` \| `digitalocean` \| `opencode` (aliases: `do`, `codex`, `opencode-go`, `opencode-zen`, `zen`) |
| `CHAT_MODEL` | — | Chat model for whichever provider is active. Overrides the built-in default; the provider's own `*_CHAT_MODEL` variable, if set, wins over it |
| `OLLAMA_URL` | `http://127.0.0.1:11434` | Ollama server URL |
| `OLLAMA_MODEL` | `llama3:latest` | Ollama chat model |
| `OLLAMA_EMBED_MODEL` | `nomic-embed-text` | Ollama embeddings model (for vector memory) |
| `OLLAMA_TIMEOUT` | `120` | Ollama request timeout (seconds) |
| `OPENAI_API_KEY` | — | Required for `openai` / `openai-codex` provider |
| `OPENAI_BASE_URL` | `https://api.openai.com/v1` | OpenAI-compatible base URL |
| `OPENAI_CHAT_MODEL` | `gpt-4o-mini` | OpenAI chat model (`gpt-5.3-codex` for `openai-codex`) |
| `OPENAI_EMBED_MODEL` | `text-embedding-3-small` | OpenAI embeddings model (for vector memory) |
| `OPENAI_API_MODE` | auto | `chat_completions` \| `responses` (auto: `responses` when the model name contains `codex`) |
| `OPENAI_REASONING_EFFORT` | — | Reasoning effort for the `responses` API (omitted from requests when unset) |
| `OPENAI_TEMPERATURE` | `0.2` | Sampling temperature for the `chat_completions` path (also used by the `digitalocean` and `opencode` providers). Set to `none` (or `omit` / `off`) to leave temperature out of the request entirely — reasoning models such as the o-series and gpt-5 family reject any non-default value. The client also drops it on its own after a model rejects it |
| `OPENAI_TIMEOUT` | `120` | OpenAI request timeout (seconds) |
| `DIGITALOCEAN_API_KEY` | — | Required for `digitalocean` provider (model access key or DO personal access token) |
| `DIGITALOCEAN_BASE_URL` | `https://inference.do-ai.run/v1` | DigitalOcean Serverless Inference base URL |
| `DIGITALOCEAN_CHAT_MODEL` | `meta-llama/Llama-3.3-70B-Instruct` | DigitalOcean chat model |
| `DIGITALOCEAN_EMBED_MODEL` | `qwen3-embedding-0.6b` | DigitalOcean embeddings model (for vector memory) |
| `DIGITALOCEAN_TIMEOUT` | `120` | DigitalOcean request timeout (seconds) |
| `OPENCODE_API_KEY` | — | Required for `opencode` providers. Also read from `OPENCODE_GO_API_KEY` / `OPENCODE_ZEN_API_KEY`, in that order |
| `OPENCODE_BASE_URL` | `https://opencode.ai/zen/go/v1` | opencode relay base URL. Defaults to `https://opencode.ai/zen/v1` for `LLM_PROVIDER=opencode-zen` / `zen` |
| `OPENCODE_CHAT_MODEL` | `kimi-k2.7-code` | opencode chat model (`claude-sonnet-5` on the `zen` tier) |
| `OPENCODE_SESSION_ID` | random per process | Value for the mandatory `x-opencode-session` header. Pin it to share one prompt-cache lane across processes |
| `OPENCODE_TIMEOUT` | `120` | opencode request timeout (seconds) |
| `EMBED_PROVIDER` | `{LLM_PROVIDER}` | Embeddings backend, when it should differ from the chat provider. Required in practice for `opencode`, which serves no embeddings |
| `AGENT_DB` | `{DATA_DIR}/agent_memory.sqlite3` | Conversation history database |
| `AGENT_VECTOR_DIR` | `{DATA_DIR}/chromem` | chromem-go vector database directory |
| `AGENT_VECTOR_COLLECTION` | `memories` | Collection name inside the vector database |
| `AGENT_DEDUP_DISTANCE` | `0.08` | Cosine distance threshold for semantic deduplication |
| `MEMORY_DEFAULT_TTL_DAYS` | `0` | Lifetime given to memories stored without an explicit TTL; `0` = never expire. Expired memories are hidden at once and deleted hourly |
| `AGENT_CONTEXT_TAIL` | `30` | Most recent messages loaded from session history per run |
| `AGENT_MEMORY_HITS` | `5` | Vector memory results injected into context; `0` turns injection off |
| `AGENT_MAX_STEPS` | `25` | Maximum tool-call steps per agent run |
| `AGENT_PARALLEL_TOOLS` | `false` | Allow the LLM to emit `tool_calls` batches that execute concurrently |
| `AGENT_MAX_PARALLEL` | `4` | Max tool calls per `tool_calls` batch when `AGENT_PARALLEL_TOOLS=true` — both the concurrency limit and the batch-size cap. Extra calls in an oversized batch are dropped before execution and the model is told to re-issue them |
| `AGENT_TOOL_MODE` | `auto` | `auto` (native tool calling when the provider supports it, falling back to the JSON envelope once it refuses), `native` or `envelope`. See [Tool calling](#tool-calling) |
| `AUTO_MEMORY_EXTRACT` | `false` | After each final answer, run a background LLM pass that distils the turn into durable facts and stores them in vector memory (tagged `source=auto-extract`). Existing SHA256 + cosine dedup applies. At most 2 passes run at once; extra passes are dropped |
| `AUTO_MEMORY_MAX_FACTS` | `5` | Maximum facts stored per turn by auto-extraction |
| `AUTO_MEMORY_MIN_ANSWER_LEN` | `80` | Skip auto-extraction when the final answer is shorter than this many characters |
| `PLAYGROUND_DISABLED` | `false` | Disable `POST /api/skills/{name}/invoke` (skill playground) |
| `OMEGA_REMOTE` | — | Base URL of a running gateway; switches the `omega` CLI from local to remote mode |
| `SKILLS_DIR` | `{DATA_DIR}/skills` | Directory for dynamic markdown skills |
| `SKILL_HTTP_TIMEOUT` | `30` | Timeout for HTTP-based skills (seconds) |
| `SKILL_SHELL_ENABLED` | `false` | Enable `shell_command` skill |
| `SKILL_SHELL_SANDBOX` | `none` | `bwrap` runs `shell_command` in a bubblewrap jail (read-only filesystem, private `/tmp`, no network); refused if bwrap is missing |
| `SKILL_SHELL_SANDBOX_NETWORK` | `false` | Give the sandbox the host's network |
| `SKILL_SHELL_SANDBOX_WRITABLE` | — | Directory bind-mounted read-write into the sandbox and used as its working directory |
| `SKILL_SSH_ENABLED` | `false` | Enable `ssh_command` skill |
| `SKILL_SSH_IDENTITY_FILE` | — | Path to SSH private key file |
| `SKILL_SSH_KNOWN_HOSTS` | `{DATA_DIR}/ssh_known_hosts` | known_hosts file `ssh_command` checks host keys against |
| `SKILL_SSH_HOST_KEY_POLICY` | `accept-new` | `accept-new` (record a host's key on first contact, refuse a changed key), `strict` (only hosts already in the file) or `insecure` (no checking) |
| `SKILL_WORKSPACE_DIR` | `{DATA_DIR}/workspace` | The one directory `file_list` / `file_read` / `file_write` / `pdf_text` / `csv_query` may use |
| `SKILL_WORKSPACE_MAX_MB` | `100` | Total size the workspace may grow to through `file_write` |
| `SKILL_GITHUB_TOKEN` | — (falls back to `GITHUB_TOKEN`) | Optional token for the `github` skill (higher rate limit); never shown to the model |
| `NOTIFY_TELEGRAM_CHATS` | — | Chat ids `notify` may send to (uses `TELEGRAM_BOT_TOKEN`) |
| `NOTIFY_TELEGRAM_DEFAULT_CHAT` | — | Chat used when the model names none (implicitly allowed) |
| `NOTIFY_EMAIL_TO` | — | Allowed email recipients: addresses or `@domain` entries |
| `NOTIFY_SMTP_HOST` / `NOTIFY_SMTP_PORT` | — / `587` | SMTP server for `notify` email (STARTTLS when offered; 465 = implicit TLS) |
| `NOTIFY_SMTP_USER` / `NOTIFY_SMTP_PASSWORD` | — | SMTP login (PLAIN auth), if the server needs one |
| `NOTIFY_SMTP_FROM` | — | Sender address (required for email) |
| `NOTIFY_WEBHOOKS` | — | Named webhooks, comma-separated `name=https://…` entries; the model picks a name, never a URL |
| `SKILL_SSH_DEFAULT_USER` | `root` | Default SSH username |
| `SKILL_SSH_PRIVATE_KEY` | — | PEM or base64-encoded PEM private key (alternative to identity file) |
| `SKILL_ALLOW_PRIVATE_NETWORKS` | `true` | Let outbound skills reach loopback, RFC1918/ULA and CGNAT addresses. `false` blocks them. Link-local / cloud-metadata, unspecified and multicast addresses are always refused |
| `SCHEDULER_DB` | `{DATA_DIR}/scheduler.sqlite3` | Scheduler database path |
| `SCHEDULER_TICK_SEC` | `60` | Scheduler poll interval (seconds). Each tick also sweeps any whole minutes skipped since the previous one (up to 60), so ticker drift or a suspended host does not lose a scheduled run |
| `TELEGRAM_BOT_TOKEN` | — | Telegram bot token |
| `BOT_AUTH_ENABLED` | `true` | Telegram user allowlist. On unless explicitly `false`; with it on the bot refuses to start without `BOT_ADMIN_ID` |
| `BOT_ADMIN_ID` | `0` | Telegram user ID of the admin (required while auth is enabled) |
| `BOT_AUTH_DB` | `/app/data/telegram_auth.sqlite3` | Telegram allowlist database |
| `BOT_MEMORY_SCOPE` | `shared` | Memory namespace per conversation: `shared`, `user` (per Telegram user) or `chat` (per chat) |
| `GATEWAY_URL` | `http://127.0.0.1:8000` | Telegram bot → gateway base URL |
| `GATEWAY_API_TOKEN` | — | When set, `/api/*` and `/mcp` require `Authorization: Bearer <token>` (`/health` stays open). The Telegram bot and `omega` remote mode send it automatically |
| `CORS_ALLOWED_ORIGINS` | — | Comma-separated origins allowed to call the API cross-origin (`*` = any). Empty = same-origin only |
| `GATEWAY_ALLOWED_HOSTS` | — | Comma-separated DNS names the gateway answers to while `GATEWAY_API_TOKEN` is unset (`*` = any). IPs, single-label names and `*.localhost` are always accepted; ignored once a token is set. See [Security](#security) |
| `AUDIT_MAX_BLOB_BYTES` | `65536` | Max bytes stored per args/result blob in the audit log. Set to `0` to disable audit logging entirely |
| `MCP_SERVER_DISABLED` | `false` | Disable the MCP server endpoint (`POST /mcp`) that exposes skills as MCP tools |
| `MCP_SERVERS` | — | Remote MCP servers to consume, comma-separated `name=url` entries (append `\|Header: Value` for one auth header). Their tools are registered locally as `<name>_<tool>` |
| `MCP_CONNECT_WAIT_SEC` | `10` | How long startup waits for `MCP_SERVERS` to connect; the rest keep connecting in the background |
| `MCP_REFRESH_SEC` | `300` | Re-list each remote server's tools this often (`0` = only on `tools/list_changed`) |

## Frontend / gateway separation

The gateway binary is a pure JSON API: it serves only `/health`, `/api/*` and `/mcp`
and contains no static-file serving, no UI redirect, and no `//go:embed`
assets.  The React UI is built and served exclusively by the **frontend**
nginx service, which also reverse-proxies `/api/*` and `/health` back to
the gateway.  For UI development without Docker, run `make dev-web`
(Vite at `:5173`, proxying `/api` to the gateway on `:8000`).
