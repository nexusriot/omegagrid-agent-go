package httpapi

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

func (d *Deps) handleNewSession(w http.ResponseWriter, _ *http.Request) {
	sid, err := d.Memory.CreateSession()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": sid})
}

func (d *Deps) handleListSessions(w http.ResponseWriter, r *http.Request) {
	limit := atoiOr(r.URL.Query().Get("limit"), 50)
	sessions, err := d.Memory.ListSessions(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

func (d *Deps) handleSessionMessages(w http.ResponseWriter, r *http.Request) {
	sid, err := strconv.Atoi(chi.URLParam(r, "sid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid session id")
		return
	}
	limit := atoiOr(r.URL.Query().Get("limit"), 200)
	offset := atoiOr(r.URL.Query().Get("offset"), 0)
	msgs, err := d.Memory.ListMessages(sid, limit, offset)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": sid, "messages": msgs})
}

func atoiOr(s string, def int) int {
	if s == "" {
		return def
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}
