# Export host UID/GID so docker compose runs containers as the current user,
# preventing root-owned files on the bind-mounted ./data volume.
export UID := $(shell id -u)
export GID := $(shell id -g)

.PHONY: web build build-all cli dev-web migrate-vector vector-export vector-import vector-migrate vet test e2e e2e-host init

## Create ./data with correct ownership (run once before first docker compose up)
init:
	mkdir -p data/skills

## Build the React UI (outputs to web/dist)
web:
	cd web && npm run build

## Build the Go gateway (requires web/dist to exist)
build: web
	go build -o bin/gateway ./cmd/gateway

## Build all binaries (gateway + telegram-bot + migrate-vector + omega CLI)
build-all: web
	go build -o bin/gateway        ./cmd/gateway
	go build -o bin/telegram-bot   ./cmd/telegram-bot
	go build -o bin/migrate-vector ./cmd/migrate-vector
	go build -o bin/omega          ./cmd/cli

## Build the omega CLI binary only
cli:
	go build -o bin/omega ./cmd/cli

## Run go vet on all packages
vet:
	go vet ./...

## Run the full Go test suite isolated in a Docker container (builds Dockerfile.test).
## Needs only Docker on the host — no local Go toolchain required.
test:
	./run_tests.sh --docker

## Run the hermetic end-to-end backend suite: the real gateway image plus a
## mock LLM on an internal Docker network with no route off the host.
e2e:
	./scripts/e2e.sh

## Same suite against locally built binaries — seconds per cycle, no Docker,
## but NOT network-isolated. For iterating on a test; CI should run `make e2e`.
e2e-host:
	./scripts/e2e.sh --host

## Run the Vite dev server (proxies /api to localhost:8000)
dev-web:
	cd web && npm run dev

## Build the one-shot ChromaDB → chromem-go migrator
migrate-vector:
	go build -o bin/migrate-vector ./cmd/migrate-vector

## Step 1: dump ChromaDB → JSONL (docker — no local Python needed)
##   docker compose -f docker-compose.migrate.yml run --rm migrate-export
vector-export:
	@echo "Run via docker: docker compose -f docker-compose.migrate.yml run --rm migrate-export"

## Step 2: load JSONL → chromem-go (host)
vector-import: migrate-vector
	./bin/migrate-vector --in data/vector_db.jsonl --db data/chromem --collection memories

## Full migration using Docker containers (no local Python needed)
## Builds both stages, exports ChromaDB → JSONL, then imports JSONL → chromem-go.
## Safe to re-run on an empty collection (no-op).
vector-migrate:
	@if [ -d data/chromem ] && [ "$$(ls -A data/chromem 2>/dev/null)" ]; then \
		echo "ERROR: data/chromem already exists and is non-empty."; \
		echo "       Remove it first if you want to re-run the migration:  rm -rf data/chromem"; \
		exit 1; \
	fi
	@echo "==> Building migration images…"
	docker compose -f docker-compose.migrate.yml build
	@echo "==> Step 1/2: Exporting ChromaDB → data/vector_db.jsonl"
	docker compose -f docker-compose.migrate.yml run --rm --remove-orphans migrate-export
	@echo "==> Step 2/2: Importing JSONL → data/chromem"
	docker compose -f docker-compose.migrate.yml run --rm --remove-orphans migrate-import
	@echo "==> Migration complete. Intermediate file: data/vector_db.jsonl (safe to delete)"
