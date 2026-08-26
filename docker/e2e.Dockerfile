# Images for the hermetic end-to-end stack (docker-compose.e2e.yml).
#
#   mockllm — deterministic stand-in for Ollama (chat + embeddings)
#   runner  — the compiled e2e test binary
#
# Both stages compile during `docker build`, which is the only phase that needs
# a network. The stack itself then runs on an internal compose network with no
# route off the host, so nothing the tests do can reach the internet.
#
# The gateway under test is NOT built here: docker-compose.e2e.yml builds it
# from docker/gateway.Dockerfile, the same image definition production uses.

FROM golang:1.25-alpine AS build
ENV CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -o /out/mockllm ./test/e2e/mockllm
# Compiling the suite into a binary keeps the run phase free of both the
# toolchain and the module proxy — `go test` would otherwise want to resolve
# imports at run time, on a network that deliberately goes nowhere.
RUN go test -tags e2e -c -trimpath -o /out/e2e.test ./test/e2e

FROM alpine:3.20 AS mockllm
COPY --from=build /out/mockllm /usr/local/bin/mockllm
EXPOSE 11434
ENTRYPOINT ["/usr/local/bin/mockllm"]

FROM alpine:3.20 AS runner
COPY --from=build /out/e2e.test /usr/local/bin/e2e.test
# -test.v is on by default: a CI log with no output tells you nothing when the
# suite fails. scripts/e2e.sh appends -test.run=… and friends.
ENTRYPOINT ["/usr/local/bin/e2e.test", "-test.v=true", "-test.timeout=10m"]
