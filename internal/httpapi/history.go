package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

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
	sessions, err := d.Memory.SearchSessions(pageSize(r.URL.Query().Get("limit"), 50), r.URL.Query().Get("q"))
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

// handleRenameSession sets a session title (PATCH /api/sessions/{sid}).
func (d *Deps) handleRenameSession(w http.ResponseWriter, r *http.Request) {
	sid, err := strconv.Atoi(chi.URLParam(r, "sid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid session id")
		return
	}
	var body struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len([]rune(body.Title)) > 200 {
		writeError(w, http.StatusBadRequest, "title is longer than 200 characters")
		return
	}
	ok, err := d.Memory.RenameSession(sid, body.Title)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sid, "title": strings.TrimSpace(body.Title)})
}

// handleDeleteSession removes a session and its messages.
func (d *Deps) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	sid, err := strconv.Atoi(chi.URLParam(r, "sid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid session id")
		return
	}
	ok, err := d.Memory.DeleteSession(sid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": sid})
}

// handleUsage reports activity for the Health page (GET /api/stats/usage).
func (d *Deps) handleUsage(w http.ResponseWriter, r *http.Request) {
	u, err := d.Memory.Usage(queryInt(r.URL.Query().Get("days"), 7))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, u)
}
