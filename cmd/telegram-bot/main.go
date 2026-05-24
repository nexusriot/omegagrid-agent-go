// Command telegram-bot is the standalone Telegram client for the agent.
// It speaks HTTP to the Go gateway via /api/query[/stream].
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/nexusriot/omegagrid-agent-go/internal/telegram"
)

// ensureDataDirs creates every on-disk directory the bot writes to before
// any sqlite store opens. Mirrors bootstrap.ensureDataDirs in the gateway —
// the bot is a separate binary that does not go through bootstrap.New, so
// it needs its own copy.
//
// Without this, a fresh deploy where DATA_DIR (or the parent of
// BOT_AUTH_DB) is missing fails with the cryptic
// "auth: unable to open database file (14)" — SQLITE_CANTOPEN — because
// the MkdirAll inside internal/telegram/auth.go silently swallows its
// error. Doing it here, eagerly, with a fatal-on-error, surfaces the real
// reason (e.g. "permission denied") right at the top of the bot's logs.
func ensureDataDirs() error {
	dirs := []string{
		envOr("DATA_DIR", "/app/data"),
		filepath.Dir(envOr("BOT_AUTH_DB", "/app/data/telegram_auth.sqlite3")),
	}
	seen := map[string]bool{}
	for _, d := range dirs {
		if d == "" || d == "." || seen[d] {
			continue
		}
		seen[d] = true
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		log.Fatalf("TELEGRAM_BOT_TOKEN is not set. Cannot start bot.")
	}
	gatewayURL := os.Getenv("GATEWAY_URL")
	if gatewayURL == "" {
		gatewayURL = "http://127.0.0.1:8000"
	}

	if err := ensureDataDirs(); err != nil {
		log.Fatalf("init: %v", err)
	}

	auth, err := telegram.AuthFromEnv()
	if err != nil {
		log.Fatalf("auth: %v", err)
	}

	bot, err := telegram.New(token, gatewayURL, auth)
	if err != nil {
		log.Fatalf("bot init: %v", err)
	}
	bot.Run()
}
