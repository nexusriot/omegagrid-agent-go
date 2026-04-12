package httpapi

import (
	"encoding/json"
	"net/http"
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
