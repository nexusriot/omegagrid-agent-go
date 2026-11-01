# Changelog

Versions follow [semantic versioning](https://semver.org/) and match the git
tags (`0.6.0`, no `v` prefix). The version lives in one place,
`internal/version/version.go`; see [Versioning](README.md#versioning).

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
