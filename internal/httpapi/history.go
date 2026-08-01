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
	sessions, err := d.Memory.ListSessions(pageSize(r.URL.Query().Get("limit"), 50))
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
	limit := pageSize(r.URL.Query().Get("limit"), 200)
	offset := max(queryInt(r.URL.Query().Get("offset"), 0), 0)

	msgs, err := d.Memory.ListMessages(sid, limit, offset)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": sid, "messages": msgs})
}

// pageSize reads a limit that reaches SQLite as a LIMIT clause, where a
// non-positive value would misbehave: ?limit=0 becomes "LIMIT 0" and returns an
// empty list, ?limit=-1 becomes "no limit at all". Neither is what a caller
// asking for zero or a negative page means. Note that /api/memory deliberately
// differs — it paginates in memory, so limit=0 there means "everything".
func pageSize(s string, def int) int {
	if n := queryInt(s, def); n > 0 {
		return n
	}
	return def
}
