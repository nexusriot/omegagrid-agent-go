.PHONY: web build build-all dev-web

## Build the React UI (outputs to web/dist)
web:
	cd web && npm run build

## Build the Go gateway (requires web/dist to exist)
build: web
	go build -o bin/gateway ./cmd/gateway

## Build both binaries
build-all: web
	go build -o bin/gateway     ./cmd/gateway
	go build -o bin/telegram-bot ./cmd/telegram-bot

## Run the Vite dev server (proxies /api to localhost:8000)
dev-web:
	cd web && npm run dev
