#!/usr/bin/env bash
# e2e.sh — run the end-to-end backend suite.
#
# Two modes:
#
#   docker (default)  Builds the real gateway image and runs the whole stack on
#                     an internal compose network with no route off the host.
#                     This is the hermetic one; it is what CI should run.
#
#   --host            Builds the gateway and mockllm binaries with the local Go
#                     toolchain and runs them as plain processes. No Docker, a
#                     few seconds per cycle — for iterating on a test. It is NOT
#                     network-isolated, so TestNetworkIsolation skips itself.
#
# Usage:
#   ./scripts/e2e.sh                      # full hermetic run + restart phase
#   ./scripts/e2e.sh -r TestMemory        # only tests matching a pattern
#   ./scripts/e2e.sh --no-build           # reuse the images already built
#   ./scripts/e2e.sh --skip-restart       # skip the persistence phase
#   ./scripts/e2e.sh -k                   # leave the stack running afterwards
#   ./scripts/e2e.sh --logs               # always dump service logs
#   ./scripts/e2e.sh --down               # tear down a stack left by -k
#   ./scripts/e2e.sh --host -r TestHealth # quick local loop, no Docker
#
# Service logs are dumped automatically whenever the suite fails.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT_DIR"

COMPOSE_FILE="docker-compose.e2e.yml"
PROJECT="omegagrid-e2e"

MODE="docker"
BUILD=true
KEEP=false
SHOW_LOGS=false
SKIP_RESTART=false
RUN_PATTERN=""

# Host-mode ports. Override with E2E_HOST_GATEWAY_PORT / E2E_HOST_MOCK_PORT.
HOST_GATEWAY_PORT="${E2E_HOST_GATEWAY_PORT:-18000}"
HOST_MOCK_PORT="${E2E_HOST_MOCK_PORT:-21434}"

# Host-mode state. These live at script scope on purpose: the EXIT trap fires
# after run_host_mode has returned, and locals would already be out of scope
# (fatal under `set -u`).
HOST_TMP=""
GATEWAY_PID=""
MOCK_PID=""

usage() { sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --host)          MODE="host" ;;
    --no-build)      BUILD=false ;;
    -k|--keep)       KEEP=true ;;
    -l|--logs)       SHOW_LOGS=true ;;
    --skip-restart)  SKIP_RESTART=true ;;
    -r|--run)        RUN_PATTERN="${2:-}"; shift ;;
    -r=*|--run=*)    RUN_PATTERN="${1#*=}" ;;
    --down)          docker compose -p "$PROJECT" -f "$COMPOSE_FILE" down -v --remove-orphans; exit 0 ;;
    -h|--help)       usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; echo "Run '$0 --help' for usage." >&2; exit 2 ;;
  esac
  shift
done

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
warn() { printf '\033[1;33m%s\033[0m\n' "$*" >&2; }

# ---------------------------------------------------------------------------
# host mode
# ---------------------------------------------------------------------------

cleanup_host() {
  local code=$?
  [[ -n "$GATEWAY_PID" ]] && kill "$GATEWAY_PID" 2>/dev/null || true
  [[ -n "$MOCK_PID" ]] && kill "$MOCK_PID" 2>/dev/null || true
  wait 2>/dev/null || true
  if [[ -n "$HOST_TMP" ]] && { [[ $code -ne 0 ]] || [[ "$SHOW_LOGS" == true ]]; }; then
    say "gateway log"; tail -n 40 "$HOST_TMP/gateway.log" 2>/dev/null || true
    say "mockllm log"; tail -n 20 "$HOST_TMP/mockllm.log" 2>/dev/null || true
  fi
  if [[ "$KEEP" == true ]]; then
    warn "kept working directory: $HOST_TMP"
  elif [[ -n "$HOST_TMP" ]]; then
    rm -rf "$HOST_TMP"
  fi
  exit $code
}

run_host_mode() {
  command -v go >/dev/null 2>&1 || { echo "host mode needs a local Go toolchain" >&2; exit 1; }

  HOST_TMP="$(mktemp -d -t omegagrid-e2e-XXXXXX)"
  local data_dir="$HOST_TMP/data"
  mkdir -p "$data_dir/skills" "$HOST_TMP/bin"

  trap cleanup_host EXIT INT TERM

  say "building binaries (host mode)"
  go build -o "$HOST_TMP/bin/gateway" ./cmd/gateway
  go build -o "$HOST_TMP/bin/mockllm" ./test/e2e/mockllm

  say "starting mockllm on :$HOST_MOCK_PORT"
  "$HOST_TMP/bin/mockllm" -addr "127.0.0.1:$HOST_MOCK_PORT" >"$HOST_TMP/mockllm.log" 2>&1 &
  MOCK_PID=$!

  say "starting gateway on :$HOST_GATEWAY_PORT"
  env \
    BACKEND_PORT="$HOST_GATEWAY_PORT" \
    DATA_DIR="$data_dir" \
    SKILLS_DIR="$data_dir/skills" \
    AGENT_DB="$data_dir/agent_memory.sqlite3" \
    AGENT_VECTOR_DIR="$data_dir/chromem" \
    SCHEDULER_DB="$data_dir/scheduler.sqlite3" \
    LLM_PROVIDER=ollama \
    OLLAMA_URL="http://127.0.0.1:$HOST_MOCK_PORT" \
    OLLAMA_MODEL=e2e-model \
    OLLAMA_EMBED_MODEL=e2e-embed \
    SCHEDULER_TICK_SEC=1 \
    AGENT_MAX_STEPS=8 \
    AUTO_MEMORY_EXTRACT=false \
    PLAYGROUND_DISABLED=false \
    MCP_SERVER_DISABLED=false \
    "$HOST_TMP/bin/gateway" >"$HOST_TMP/gateway.log" 2>&1 &
  GATEWAY_PID=$!

  say "running suite (host mode — not network-isolated)"
  local args=(-tags e2e ./test/e2e -count=1 -v -timeout 10m)
  [[ -n "$RUN_PATTERN" ]] && args+=(-run "$RUN_PATTERN")
  env \
    E2E_BASE_URL="http://127.0.0.1:$HOST_GATEWAY_PORT" \
    E2E_MOCK_URL="http://127.0.0.1:$HOST_MOCK_PORT" \
    E2E_EXPECT_ISOLATED=0 \
    E2E_PHASE=main \
    go test "${args[@]}"
}

# ---------------------------------------------------------------------------
# docker mode
# ---------------------------------------------------------------------------

dc() { docker compose -p "$PROJECT" -f "$COMPOSE_FILE" "$@"; }

dump_logs() {
  say "service logs"
  dc logs --no-color --tail 120 mockllm gateway gateway-locked 2>&1 || true
}

# run_suite <phase> [test-name-pattern]
run_suite() {
  local phase="$1"
  local pattern="${2-}"
  local flags=(-test.v=true -test.timeout=10m)
  [[ -n "$pattern" ]] && flags+=("-test.run=$pattern")
  E2E_PHASE="$phase" dc run --rm runner "${flags[@]}"
}

run_docker_mode() {
  command -v docker >/dev/null 2>&1 || { echo "docker is required" >&2; exit 1; }

  local failed=0
  cleanup_docker() {
    local code=$?
    if [[ $code -ne 0 || "$SHOW_LOGS" == true ]]; then
      dump_logs
    fi
    if [[ "$KEEP" == true ]]; then
      warn "stack left running. Inspect with:"
      warn "  docker compose -p $PROJECT -f $COMPOSE_FILE ps"
      warn "  docker compose -p $PROJECT -f $COMPOSE_FILE logs -f gateway"
      warn "Tear it down with: $0 --down"
    else
      say "tearing down"
      dc down -v --remove-orphans >/dev/null 2>&1 || true
    fi
    exit $code
  }
  trap cleanup_docker EXIT INT TERM

  if [[ "$BUILD" == true ]]; then
    say "building images (gateway from the production Dockerfile)"
    dc build
  fi

  say "starting the stack on an internal network"
  dc up -d mockllm gateway gateway-locked

  say "running suite"
  if ! run_suite main "$RUN_PATTERN"; then
    failed=1
  fi

  if [[ $failed -eq 0 && "$SKIP_RESTART" == false && -z "$RUN_PATTERN" ]]; then
    say "restarting the gateway to check on-disk state survives"
    dc restart gateway
    if ! run_suite post-restart TestPersistenceSurvivesRestart; then
      failed=1
    fi
  fi

  if [[ $failed -ne 0 ]]; then
    warn "e2e suite FAILED"
    exit 1
  fi
  say "e2e suite passed"
}

if [[ "$MODE" == "host" ]]; then
  run_host_mode
else
  run_docker_mode
fi
