package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nexusriot/omegagrid-agent-go/internal/memory"
)

func (d *Deps) handleListInvocations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := memory.AuditFilter{
		SessionID:  queryInt(q.Get("session_id"), 0),
		Skill:      q.Get("skill"),
		Since:      queryFloat(q.Get("since"), 0),
		Until:      queryFloat(q.Get("until"), 0),
		OnlyErrors: q.Get("only_errors") == "true" || q.Get("only_errors") == "1",
		Limit:      queryInt(q.Get("limit"), 0),
		Offset:     queryInt(q.Get("offset"), 0),
	}
	recs, total, err := d.Memory.ListInvocations(f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if recs == nil {
		recs = []memory.AuditRecord{}
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"invocations": recs,
		"total":       total,
		"limit":       limit,
		"offset":      f.Offset,
	})
}

func (d *Deps) handleGetInvocation(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	rec, err := d.Memory.GetInvocation(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rec == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (d *Deps) handleReplayInvocation(w http.ResponseWriter, r *http.Request) {
	// Replay re-executes a skill outside the agent loop, exactly like the
	// playground, so it is gated by the same flag.
	if !d.Cfg.PlaygroundEnabled {
		writeError(w, http.StatusForbidden, "skill playground is disabled (set PLAYGROUND_DISABLED=false to enable)")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	rec, err := d.Memory.GetInvocation(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rec == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	// Only registry skills are replayable. Built-in agent tools (vector_add,
	// vector_search) and audit-only rows (replay/unknown) are not in the skill
	// registry and must not be re-executed here.
	if rec.Kind != "skill" {
		writeError(w, http.StatusBadRequest, "replay is only supported for skill invocations (kind=skill)")
		return
	}

	args, _ := rec.Args.(map[string]any)
	if args == nil {
		args = map[string]any{}
	}

	t0 := time.Now()
	result, execErr := d.Skills.Execute(rec.Skill, args)
	elapsed := time.Since(t0)

	errMsg := ""
	if execErr != nil {
		errMsg = execErr.Error()
		result = map[string]any{"error": errMsg}
	}

	newRec := memory.AuditRecord{
		SessionID:    rec.SessionID,
		Step:         0,
		Skill:        rec.Skill,
		Kind:         "replay",
		Args:         args,
		Result:       result,
		ErrorMsg:     errMsg,
		DurationMS:   elapsed.Milliseconds(),
		ReplayedFrom: &id,
	}
	_ = d.Memory.AddInvocation(newRec)

	writeJSON(w, http.StatusOK, map[string]any{
		"replayed_from": id,
		"skill":         rec.Skill,
		"result":        result,
		"duration_ms":   elapsed.Milliseconds(),
		"error":         errMsg,
	})
}
