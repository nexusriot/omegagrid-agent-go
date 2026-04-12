package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/nexusriot/omegagrid-agent-go/internal/agent"
)

type queryRequest struct {
	Query          string `json:"query"`
	SessionID      int    `json:"session_id,omitempty"`
	Remember       *bool  `json:"remember,omitempty"`
	MaxSteps       int    `json:"max_steps,omitempty"`
	TelegramChatID *int64 `json:"telegram_chat_id,omitempty"`
}

func (req queryRequest) toAgentReq() agent.RunRequest {
	maxSteps := req.MaxSteps
	if maxSteps == 0 {
		maxSteps = 10
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
	res, err := d.Agent.Run(req.toAgentReq())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": err.Error(),
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
	go d.Agent.RunStream(req.toAgentReq(), events)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			payload, _ := json.Marshal(ev)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Event, payload)
			flusher.Flush()
			if ev.Event == "final" || ev.Event == "error" {
				// Drain any remaining buffered events before returning so we
				// don't deadlock the goroutine writing to the channel.
				for range events {
				}
				return
			}
		}
	}
}
