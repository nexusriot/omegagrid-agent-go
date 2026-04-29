# Multi-stage migration image.
#
# Stage "exporter": Python + chromadb — reads the legacy ChromaDB persist dir
#                   and writes each record as one JSON line to a JSONL file.
#
# Stage "importer": static Go binary — reads the JSONL and writes a fresh
#                   chromem-go persistent database.
#
# Used by docker-compose.migrate.yml; do not invoke directly.

# ── Stage 1: Python exporter ────────────────────────────────────────────────
FROM python:3.11-slim AS exporter
WORKDIR /app
# Only chromadb is needed; pull exact version used by the sidecar.
RUN pip install --no-cache-dir "chromadb==0.5.5"
COPY cmd/migrate-vector/export.py /app/export.py
ENTRYPOINT ["python3", "/app/export.py"]

# ── Stage 2: Go builder ──────────────────────────────────────────────────────
FROM golang:1.25-bookworm AS go-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
    -o /out/migrate-vector ./cmd/migrate-vector

# ── Stage 3: Go importer ─────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12 AS importer
COPY --from=go-builder /out/migrate-vector /migrate-vector
ENTRYPOINT ["/migrate-vector"]
