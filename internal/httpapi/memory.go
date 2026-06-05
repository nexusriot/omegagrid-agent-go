package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

type memoryAddRequest struct {
	Text string         `json:"text"`
	Meta map[string]any `json:"meta"`
}

func (d *Deps) handleMemoryAdd(w http.ResponseWriter, r *http.Request) {
	var req memoryAddRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	res, err := d.Memory.AddMemory(req.Text, req.Meta)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"memory_id": res.MemoryID,
		"skipped":   res.Skipped,
		"reason":    res.Reason,
	})
}

type memorySearchRequest struct {
	Query string `json:"query"`
	K     int    `json:"k"`
}

func (d *Deps) handleMemorySearch(w http.ResponseWriter, r *http.Request) {
	var req memorySearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}
	if req.K == 0 {
		req.K = 5
	}
	res, err := d.Memory.SearchMemory(req.Query, req.K)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "hits": res.Hits})
}

// handleMemoryList returns a newest-first page of stored memories.
// Query params: limit (default 100, 0 = all), offset (default 0).
func (d *Deps) handleMemoryList(w http.ResponseWriter, r *http.Request) {
	limit := atoiDefault(r.URL.Query().Get("limit"), 100)
	offset := atoiDefault(r.URL.Query().Get("offset"), 0)

	hits, total, err := d.Memory.ListMemories(limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"hits":   hits,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// handleMemoryGet returns a single stored memory by ID.
func (d *Deps) handleMemoryGet(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	hit, err := d.Memory.GetMemory(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if hit == nil {
		writeError(w, http.StatusNotFound, "memory not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "hit": hit})
}

// handleMemoryDelete removes a stored memory by ID.
func (d *Deps) handleMemoryDelete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existed, err := d.Memory.DeleteMemory(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !existed {
		writeError(w, http.StatusNotFound, "memory not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": id})
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}
