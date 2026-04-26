package httpapi

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/nexusriot/omegagrid-agent-go/internal/agent"
)

type queryRequest struct {
	Query          string `json:"query"`
	SessionID      int    `json:"session_id,omitempty"`
	Remember       *bool  `json:"remember,omitempty"`
	MaxSteps       int    `json:"max_steps,omitempty"`
	TelegramChatID *int64 `json:"telegram_chat_id,omitempty"`
}

const maxStepsHardLimit = 100

func (req queryRequest) toAgentReq(defaultMaxSteps int) agent.RunRequest {
	maxSteps := req.MaxSteps
	if maxSteps <= 0 {
		maxSteps = defaultMaxSteps
	}
	if maxSteps > maxStepsHardLimit {
		maxSteps = maxStepsHardLimit
	}
	remember := true
	if req.Remember != nil {
		remember = *req.Remember
	}
	return agent.RunRequest{
		Query:          req.Query,
		SessionID:      req.SessionID,
		Remember:       remember,
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

	events := make(chan agent.Event, 16)
	go d.Agent.RunStream(req.toAgentReq(d.Cfg.AgentMaxSteps), events)

	ctx := r.Context()
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
