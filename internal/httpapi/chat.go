package httpapi

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/nexusriot/omegagrid-agent-go/internal/agent"
)

type queryRequest struct {
	Query     string `json:"query"`
	SessionID int    `json:"session_id,omitempty"`
	// Remember is accepted for backward compatibility with the original API but
	// is a no-op: memory writes are driven by the agent's vector_add tool.
	Remember       *bool  `json:"remember,omitempty"`
	MaxSteps       int    `json:"max_steps,omitempty"`
	TelegramChatID *int64 `json:"telegram_chat_id,omitempty"`
}

const (
	maxStepsHardLimit = 100

	// maxStepsFloor guards against a non-positive configured default. The agent
	// loop is `for step := 1; step <= MaxSteps`, so a zero would skip the loop
	// body entirely and every single query would come back with "I could not
	// finish within max_steps" — a config typo (AGENT_MAX_STEPS=0) that looks
	// like a broken model.
	maxStepsFloor = 1
)

func (req queryRequest) toAgentReq(defaultMaxSteps int) agent.RunRequest {
	maxSteps := req.MaxSteps
	if maxSteps <= 0 {
		maxSteps = defaultMaxSteps
	}
	if maxSteps <= 0 {
		maxSteps = maxStepsFloor
	}
	if maxSteps > maxStepsHardLimit {
		maxSteps = maxStepsHardLimit
	}
	return agent.RunRequest{
		Query:          req.Query,
		SessionID:      req.SessionID,
		MaxSteps:       maxSteps,
		TelegramChatID: req.TelegramChatID,
	}
}

func (d *Deps) handleQuery(w http.ResponseWriter, r *http.Request) {
	var req queryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}
	res, err := d.Agent.Run(req.toAgentReq(d.Cfg.AgentMaxSteps))
	if err != nil {
		log.Printf("agent run error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "Agent failed to process the request.",
			"hint":  "Check that your LLM and embedding models are available and running.",
		})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleQueryStream emits server-sent events for the agent loop.  The wire
// format matches the FastAPI version (`event: <name>\ndata: <json>\n\n`) so
// the existing telegram bot stream parser keeps working.
func (d *Deps) handleQueryStream(w http.ResponseWriter, r *http.Request) {
	var req queryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	ctx := r.Context()
	events := make(chan agent.Event, 16)
	go func() {
		// The agent loop runs on its own goroutine here, so chi's Recoverer
		// middleware — which only wraps the handler goroutine — cannot catch a
		// panic raised inside it, and the whole gateway would go down with it.
		// RunStream closes the event channel through its own defer even while
		// panicking, so the reader below still terminates cleanly.
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("agent stream panic: %v\n%s", rec, debug.Stack())
			}
		}()
		d.Agent.RunStream(ctx, req.toAgentReq(d.Cfg.AgentMaxSteps), events)
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			payload, merr := json.Marshal(ev)
			if merr != nil {
				fmt.Fprintf(w, "event: error\ndata: {\"error\":\"internal marshal error\"}\n\n")
				flusher.Flush()
				return
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Event, payload)
			flusher.Flush()
			if ev.Event == "final" || ev.Event == "error" {
				// Drain remaining buffered events so the RunStream goroutine
				// can finish writing before we return. Capped to prevent hang
				// if the goroutine stalls before closing the channel.
				drain := time.After(5 * time.Second)
				for {
					select {
					case _, ok := <-events:
						if !ok {
							return
						}
					case <-drain:
						return
					}
				}
			}
		}
	}
}
