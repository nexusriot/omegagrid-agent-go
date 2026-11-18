package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/nexusriot/omegagrid-agent-go/internal/memory"
)

// GET /api/runs?session_id=&outcome=&tool=&days=&limit=&offset=
func (d *Deps) handleListRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := memory.RunFilter{
		SessionID: queryInt(q.Get("session_id"), 0),
		Outcome:   q.Get("outcome"),
		Tool:      q.Get("tool"),
		Limit:     queryInt(q.Get("limit"), 50),
		Offset:    queryInt(q.Get("offset"), 0),
	}
	if days := queryInt(q.Get("days"), 0); days > 0 {
		f.Since = float64(time.Now().Add(-time.Duration(days) * 24 * time.Hour).Unix())
	}
	runs, total, err := d.Memory.ListRuns(f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs, "total": total, "limit": f.Limit, "offset": f.Offset})
}

// GET /api/runs/{id}
func (d *Deps) handleGetRun(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	run, err := d.Memory.GetRun(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if run == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// GET /api/runs/stats?days=7 — cost, outcomes and retrieval hit rate.
func (d *Deps) handleRunStats(w http.ResponseWriter, r *http.Request) {
	days := queryInt(r.URL.Query().Get("days"), 7)
	if days <= 0 || days > 90 {
		days = 7
	}
	st, err := d.Memory.RunStats(float64(time.Now().Add(-time.Duration(days) * 24 * time.Hour).Unix()))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// GET /api/tools/rank?q=...&limit=30 — what tool retrieval would offer a run
// with this query.
func (d *Deps) handleRankTools(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeError(w, http.StatusBadRequest, "q is required")
		return
	}
	if d.Agent == nil {
		writeError(w, http.StatusServiceUnavailable, "agent unavailable")
		return
	}
	out, err := d.Agent.RankTools(r.Context(), q, queryInt(r.URL.Query().Get("limit"), 30))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}
