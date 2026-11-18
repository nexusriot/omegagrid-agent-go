package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/nexusriot/omegagrid-agent-go/internal/agent"
	"github.com/nexusriot/omegagrid-agent-go/internal/memory"
)

// approvalBody is the decision a client posts for a paused tool call.
type approvalBody struct {
	Approved *bool  `json:"approved"`
	Reason   string `json:"reason,omitempty"`
	// By names the approver for the run's debug log ("web", "telegram:42");
	// free text, informational only.
	By string `json:"by,omitempty"`
	// Scope "session" also approves later calls to the same tool in the
	// same session (until the grant expires); "" or "once" does not.
	Scope string `json:"scope,omitempty"`
}

func (d *Deps) approvals() *agent.Approvals {
	if d.Agent == nil {
		return nil
	}
	return d.Agent.Approvals
}

// GET /api/approvals — the tool calls currently waiting for a decision.
func (d *Deps) handleListApprovals(w http.ResponseWriter, _ *http.Request) {
	a := d.approvals()
	if a == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "pending": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "pending": a.List()})
}

// GET /api/approvals/{id}
func (d *Deps) handleGetApproval(w http.ResponseWriter, r *http.Request) {
	a := d.approvals()
	if a == nil {
		writeError(w, http.StatusNotFound, "approvals are disabled")
		return
	}
	req, ok := a.Get(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, http.StatusNotFound, agent.ErrApprovalNotFound.Error())
		return
	}
	writeJSON(w, http.StatusOK, req)
}

// POST /api/approvals/{id} {"approved": true|false, "reason": "..."}
func (d *Deps) handleResolveApproval(w http.ResponseWriter, r *http.Request) {
	a := d.approvals()
	if a == nil {
		writeError(w, http.StatusNotFound, "approvals are disabled")
		return
	}
	var body approvalBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Approved == nil {
		writeError(w, http.StatusBadRequest, `"approved" (true or false) is required`)
		return
	}
	by := strings.TrimSpace(body.By)
	if by == "" {
		by = "api"
	}
	if len(by) > 64 {
		by = by[:64]
	}
	scope := strings.ToLower(strings.TrimSpace(body.Scope))
	switch scope {
	case "", "once":
		scope = ""
	case agent.ScopeSession:
	default:
		writeError(w, http.StatusBadRequest, `"scope" must be "once" or "session"`)
		return
	}
	reason := strings.TrimSpace(body.Reason)
	if len(reason) > 500 {
		reason = reason[:500]
	}
	err := a.Resolve(chi.URLParam(r, "id"), agent.ApprovalDecision{Approved: *body.Approved, By: by, Reason: reason, Scope: scope})
	if errors.Is(err, agent.ErrApprovalNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": chi.URLParam(r, "id"), "approved": *body.Approved, "scope": nonEmptyOr(scope, "once")})
}

func nonEmptyOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// GET /api/approvals/history?session_id=&tool=&by=&only_denied=&limit=&offset=
func (d *Deps) handleApprovalHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := memory.ApprovalFilter{
		SessionID:  queryInt(q.Get("session_id"), 0),
		Tool:       q.Get("tool"),
		By:         q.Get("by"),
		OnlyDenied: q.Get("only_denied") == "true" || q.Get("only_denied") == "1",
		Limit:      queryInt(q.Get("limit"), 50),
		Offset:     queryInt(q.Get("offset"), 0),
	}
	recs, total, err := d.Memory.ListApprovals(f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"approvals": recs, "total": total, "limit": f.Limit, "offset": f.Offset})
}

// GET /api/approvals/grants — active "approve for this session" grants and
// the configured allow-rules.
func (d *Deps) handleListGrants(w http.ResponseWriter, _ *http.Request) {
	a := d.approvals()
	if a == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "grants": []any{}, "rules": []string{}})
		return
	}
	rules := make([]string, 0, len(a.Rules))
	for _, r := range a.Rules {
		rules = append(rules, r.Text)
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "grants": a.Grants(), "rules": rules})
}

// DELETE /api/approvals/grants?session_id=N — revoke one session's grants
// (all sessions without session_id).
func (d *Deps) handleRevokeGrants(w http.ResponseWriter, r *http.Request) {
	a := d.approvals()
	if a == nil {
		writeError(w, http.StatusNotFound, "approvals are disabled")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": a.Revoke(queryInt(r.URL.Query().Get("session_id"), 0))})
}
