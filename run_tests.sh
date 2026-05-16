#!/usr/bin/env bash
# run_tests.sh — run the Go backend test suite locally or in Docker.
#
# Usage:
#   ./run_tests.sh              # local, with race detector (default)
#   ./run_tests.sh --docker     # build Docker test image and run inside container
#   ./run_tests.sh --no-race    # local, without race detector (faster)
#   ./run_tests.sh --help       # print this help

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

IMAGE_NAME="omegagrid-agent-test"
DOCKER=false
RACE_FLAG="-race"

for arg in "$@"; do
  case "$arg" in
    --docker)   DOCKER=true ;;
    --no-race)  RACE_FLAG="" ;;
    --help|-h)
      sed -n '2,10p' "$0"   # print the header comment
      exit 0
      ;;
    *)
      echo "Unknown argument: $arg" >&2
      echo "Run '$0 --help' for usage." >&2
      exit 1
      ;;
  esac
done

# ── Docker mode ───────────────────────────────────────────────────────────────
if $DOCKER; then
  echo "==> Building Docker test image: $IMAGE_NAME"
  docker build \
    --file Dockerfile.test \
    --tag "$IMAGE_NAME" \
    --progress=plain \
    .

  echo ""
  echo "==> Running tests inside container (race detector disabled in Alpine build)"
  docker run --rm "$IMAGE_NAME"
  exit $?
fi

# ── Local mode ────────────────────────────────────────────────────────────────
echo "==> Running tests locally (race=${RACE_FLAG:-off}, timeout=120s)"
# shellcheck disable=SC2086
go test \
  $RACE_FLAG \
  -v \
  -timeout 120s \
  -count=1 \
  ./...
