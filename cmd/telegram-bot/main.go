// Command telegram-bot is the standalone Telegram client for the agent.
// It speaks HTTP to the Go gateway via /api/query[/stream].
package main

import (
	"log"
	"os"

	"github.com/nexusriot/omegagrid-agent-go/internal/telegram"
)

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
