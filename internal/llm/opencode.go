package llm

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// SessionHeader is the routing header the opencode Zen relay demands on every
// request. Without it the relay answers 400 MissingSessionID ("Request is
// missing x-opencode-session and cannot be routed efficiently"), so a stock
// OpenAI client cannot talk to it.
const SessionHeader = "x-opencode-session"

// NewOpenCodeChat builds a chat client for the opencode Zen relay
// (https://opencode.ai/zen/go/v1 for the Go subscription tier,
// https://opencode.ai/zen/v1 for the credit-funded tier).  The relay is
// OpenAI-compatible — bearer auth, POST /chat/completions, response_format
// json_object — so it reuses OpenAIChat with the session header bolted on.
//
// sessionID is what the relay uses for prompt-cache affinity; an empty value
// mints a random one for the lifetime of this client, which keeps a long-lived
// gateway process on a single cache lane.
//
// Only chat_completions is supported: the relay exposes no /responses endpoint.
func NewOpenCodeChat(apiKey, baseURL, model, sessionID string, temperature *float64, timeoutSec float64) *OpenAIChat {
	if sessionID == "" {
		sessionID = NewSessionID()
	}
	return NewOpenAIChat(
		apiKey, baseURL, model, "chat_completions", "", temperature, timeoutSec,
		WithHeader(SessionHeader, sessionID),
	)
}

// NewSessionID mints an opencode session identifier. The relay only requires
// a stable opaque string, so a random 128-bit value is plenty.
func NewSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand is effectively infallible on Linux, but a session id is
		// a routing hint, not a secret — a clock-based fallback beats failing
		// to build the client at all.
		return fmt.Sprintf("ses_%d", time.Now().UnixNano())
	}
	return "ses_" + hex.EncodeToString(b[:])
}
