# Changelog

Versions follow [semantic versioning](https://semver.org/) and match the git
tags (`0.7.0`, no `v` prefix). The version lives in one place,
`internal/version/version.go`; see [Versioning](README.md#versioning).

## 0.7.0 — 2026-11-18

Approval gate, tool retrieval, run traces, and twelve new skills.

### Added

- **Tool retrieval** (`AGENT_TOOL_TOP_K`, default 12; `AGENT_TOOLS_PINNED`):
  a run's prompt lists only the skills most relevant to the query (lexical +
  embedding ranking fused by reciprocal rank, embeddings cached and warmed at
  start) plus a new `find_tools` meta-tool that searches the whole catalogue.
  `meta.tool_retrieval` / `meta.tools_offered` report it.
- **Approval gate** (`APPROVAL_SKILLS`, `APPROVAL_TIMEOUT_SEC`,
  `APPROVAL_UNATTENDED`): gated skills pause the run until a human approves —
  `approval_required` / `approval_resolved` SSE events, an Approve/Deny card in
  the web chat, Telegram inline buttons, a `[y/N]` prompt in `omega ask`, and
  `GET/POST /api/approvals[/{id}]`. Markdown skills can set
  `requires_approval: true`; pipelines inherit approval from their steps.
  `/api/skills` reports `requires_approval`.
- **10 new skills** (nine built-in plus the native `bookmark`; two more below):
  - monitoring: `uptime_check` (recorded history, uptime %, state changes),
    `ssl_labs_grade`, `dns_propagation`;
  - productivity: `calendar_ics` (RRULE expansion), `imap_search`
    (read-only), `todo`, `bookmark` (native, vector memory);
  - data: `sql_query` (read-only PostgreSQL / SQLite), `chart_render` (PNG
    bar/line/pie), `yaml_query`.
- **Bundled example pipelines** installed into `SKILLS_DIR` on first start
  (`SKILLS_INSTALL_EXAMPLES`): `domain_report`, `site_check`, `infra_digest`,
  `morning_briefing`, `dns_cutover_check`.
- **Approval shortcuts**: "approve for this session" (web button, Telegram
  button, `a` in `omega ask`, `scope: "session"` in the API;
  `APPROVAL_GRANT_TTL_HOURS`, `GET/DELETE /api/approvals/grants`) and
  argument allow-rules (`APPROVAL_ALLOW`, e.g.
  `shell_command: command=df( -h)?|uptime`; whole-value match, shell
  metacharacters never match; rules also cover scheduled tasks).
- **Approval log**: every decision — human, rule, session grant, policy,
  timeout — with its wait time (`GET /api/approvals/history`, Activity →
  Approvals tab).
- **Run traces**: one record per agent run — tools offered vs. used, LLM
  calls, prompt/completion tokens (OpenAI-compatible and Ollama backends),
  tool and LLM time, approval wait, outcome (`GET /api/runs`,
  `/api/runs/{id}`, `/api/runs/stats`; Activity → Runs tab with a 7-day
  summary and the retrieval miss rate).
- `GET /api/tools/rank?q=` shows the tool-retrieval ranking for a query, and
  `internal/agent/testdata/retrieval_eval.json` + `TestRetrievalEval` guard
  ranking quality (57 queries; it caught "17% of 2340" missing `math_eval`).
- **New skills**: `prometheus_query` (instant / range with stats and chart /
  alerts / down targets / metric search) and `openapi` (search, describe and
  call any REST API from its OpenAPI 3 / Swagger 2 spec; non-GET calls need
  approval). `sql_query` can now query workspace CSV / TSV / JSONL files as
  tables (`files`), including joins across files. 62 built-in skills.
- New configuration: `SKILL_STATE_DB`, `SKILL_SSLLABS_EMAIL`,
  `SKILL_CALENDARS`, `SKILL_IMAP_*`, `SKILL_SQL_DATABASES`,
  `SKILL_PROMETHEUS`, `SKILL_PROMETHEUS_TOKEN`, `SKILL_OPENAPI`,
  `SKILL_OPENAPI_<NAME>_AUTH`, `SKILL_OPENAPI_<NAME>_BASE_URL`,
  `APPROVAL_ALLOW`, `APPROVAL_GRANT_TTL_HOURS`.
- Dependencies: `github.com/jackc/pgx/v5` (Postgres driver for `sql_query`),
  `golang.org/x/image` (chart fonts).

### Changed

- The agent asks before running `shell_command`, `ssh_command` and
  `file_write`; scheduled agent-prompt tasks that call them are denied unless
  `APPROVAL_UNATTENDED=allow` (`APPROVAL_SKILLS=off` restores the old
  behaviour).
- Runs no longer list every skill in the prompt once the catalogue exceeds
  `AGENT_TOOL_TOP_K` (`AGENT_TOOL_TOP_K=0` restores it).

### Fixed

- `imap_search` and `rss_fetch` decode every WHATWG charset through
  `golang.org/x/text` (now a direct dependency): windows-1252 quotes, € and
  dashes in mail were mangled, and Cyrillic (windows-1251, koi8-r), CJK and
  other encodings came through undecoded (mail) or were refused (feeds).

## 0.6.0 — 2026-10-31

Everything since 0.5.0: the commits from 2026-07-22 to 2026-10-23 plus two
review rounds and a feature round.

### Added

- **Native tool calling** (`AGENT_TOOL_MODE=auto|native|envelope`, default
  `auto`) for Ollama, OpenAI chat-completions and the Responses API, falling
  back to the JSON envelope when a model refuses tools. A plain-text reply is
  the answer instead of a "please rephrase" fallback.
- **Cancellation reaches running tools**: a client disconnect, gateway
  shutdown, scheduler stop or Ctrl-C in `omega` stops a running shell/SSH
  command, scan, HTTP request or pipeline.
- **Memory**: namespaces (per API call, `omega --namespace`, or Telegram
  `BOT_MEMORY_SCOPE`), expiry (`MEMORY_DEFAULT_TTL_DAYS`, hourly purge),
  in-place edit, JSONL export/import, namespace counts. The exact-duplicate
  index is rebuilt at startup and listing no longer needs the embeddings
  backend.
- **Scheduler**: per-task time zones, agent-prompt tasks, persisted retries
  with backoff, per-task run history, run-now, `omega schedule
  enable|disable|run|history`.
- **MCP client**: servers connect in parallel (startup waits at most
  `MCP_CONNECT_WAIT_SEC`), unreachable ones retry in the background, tool lists
  refresh on a timer and on `tools/list_changed`.
- **Shell sandbox** (`SKILL_SHELL_SANDBOX=bwrap`) and **SSH host-key checking**
  (`SKILL_SSH_HOST_KEY_POLICY`, default `accept-new`).
- **Web UI**: session search/rename/delete; memory edit, namespace filter,
  export/import; scheduler time zones, agent tasks, retries, run-now and
  history; a markdown skill editor with live validation; a usage chart on the
  Health page; the version on the Health page.
- **API**: `PATCH`/`DELETE /api/sessions/{sid}`, `GET /api/stats/usage`,
  `PUT /api/memory/{id}`, `/api/memory/{namespaces,export,import}`,
  `/api/scheduler/tasks/{id}/{run,runs}`, `/api/skills/{validate,source}`,
  `GET /api/skills/{name}/source`, `DELETE /api/skills/{name}`; `/health`
  reports `version`.
- **opencode Zen provider** (`LLM_PROVIDER=opencode`, `EMBED_PROVIDER`).
- **Network/recon skills**: `tls_probe`, `http_headers`, `banner_grab`,
  `ptr_lookup`, `email_auth`, `jwt_inspect`.
- `GATEWAY_API_TOKEN`, `CORS_ALLOWED_ORIGINS`, `GATEWAY_ALLOWED_HOSTS`.
- **23 new built-in skills (51 in all)** and an agent tool:
  - for scheduled agent tasks: `rss_fetch`, `notify` (Telegram / email /
    webhook, allowlisted recipients), and `session_search` (keyword search
    over earlier conversations);
  - network: `http_timing`, `dns_compare`, `rdap_lookup`, `traceroute`
    (unprivileged, Linux), `smtp_check`, `wake_on_lan`, `subnet_sweep`;
  - data and text: `json_query`, `time_convert`, `regex_test`, `text_diff`,
    `csv_query`, `unit_convert`, `html_to_markdown`;
  - knowledge, no API key: `wikipedia`, `currency_convert` (ECB rates),
    `github`;
  - a confined workspace directory: `file_list`, `file_read`, `file_write`,
    `pdf_text` (new dependency `github.com/ledongthuc/pdf`).
- `omega version` / `--version`; a hermetic end-to-end suite (`make e2e`); CI
  (`.github/workflows/ci.yml`).

### Changed

- `ssh_command` verifies host keys; a changed key is refused
  (`SKILL_SSH_HOST_KEY_POLICY=insecure` restores the old behaviour).
- Models are called through native tool calling by default
  (`AGENT_TOOL_MODE=envelope` restores the old behaviour).
- Without `GATEWAY_API_TOKEN`, requests whose `Host` is a dotted DNS name are
  refused (DNS-rebinding guard) unless listed in `GATEWAY_ALLOWED_HOSTS`.
- `BOT_AUTH_ENABLED` defaults to `true`; Telegram authorization is by sender.
- CORS is no longer `*`.
- Markdown skill `endpoint` placeholder values are URL-escaped.
- `omega` local mode no longer runs scheduled tasks; flags may follow the
  positional argument; `omega skills run` prints the bare result and exits
  non-zero on failure in both modes.
- Cron: `N/S` steps, a `*`-prefixed day field is unrestricted (Vixie),
  impossible dates are rejected.
- `math_eval` follows Python for `%`, `round` and `factorial`.
- The MCP server/client report the release version instead of a fixed `1.0.0`.
- Database schemas gain columns and tables (scheduler: `timezone`, `prompt`,
  retries, `task_runs`; history: session `title`); they are added on start.

### Fixed

Many bugs across several review rounds, among them: a DNS-rebinding hole that let any
web page drive a token-less gateway; secrets from `password_gen` and
`jwt_inspect` stored in chat history; cross-process scheduler double-runs and
one-shot re-fires; `skill_creator` frontmatter injection and unbounded pipeline
fan-out; remote MCP tools able to replace built-ins; vector search failing
during deletes; a panicking skill taking the gateway down; Telegram rejecting
emoji-heavy answers.

## 0.5.0 — 2026-06-17

Auto memory extraction (`AUTO_MEMORY_EXTRACT`).

## Earlier

0.4.2 (2026-06-12), 0.3.1 (2026-06-05), 0.2.0 (2026-05-24), 0.1.6
(2026-05-17), 0.1.0 (2026-05-04) — see `git log` for these tags.
