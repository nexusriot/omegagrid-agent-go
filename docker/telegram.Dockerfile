# Build context is the omegagrid-agent-go project root.
FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/telegram-bot ./cmd/telegram-bot

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/telegram-bot /telegram-bot
ENTRYPOINT ["/telegram-bot"]
