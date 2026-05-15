package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nexusriot/omegagrid-agent-go/internal/agent"
)

// handleListSkills returns all skills registered in the in-process skill registry.
func (d *Deps) handleListSkills(w http.ResponseWriter, _ *http.Request) {
	skills, err := d.Skills.List()
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skills": skills})
}

// handleSkillInvoke executes a named skill directly (skill playground).
func (d *Deps) handleSkillInvoke(w http.ResponseWriter, r *http.Request) {
	if !d.Cfg.PlaygroundEnabled {
		writeError(w, http.StatusForbidden, "skill playground is disabled (set PLAYGROUND_DISABLED=false to enable)")
		return
	}
	name := chi.URLParam(r, "name")

	var body struct {
		Args map[string]any `json:"args"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Args == nil {
		body.Args = map[string]any{}
	}

	t0 := time.Now()
	result, execErr := d.Skills.Execute(name, body.Args)
	elapsed := time.Since(t0).Seconds()

	var errMsg *string
	if execErr != nil {
		s := execErr.Error()
		errMsg = &s
		writeJSON(w, http.StatusOK, map[string]any{
			"name": name, "args": body.Args, "result": nil,
			"elapsed_s": elapsed, "error": errMsg, "attachments": nil,
		})
		return
	}

	atts, cleaned := agent.ExtractAttachments(name, result)
	writeJSON(w, http.StatusOK, map[string]any{
		"name": name, "args": body.Args, "result": cleaned,
		"elapsed_s": elapsed, "error": nil, "attachments": atts,
	})
}

// handleListTools returns the static set of built-in (Go-side) tools.  Skills
// are exposed via /api/skills; tools are the small set of vector_* primitives.
func (d *Deps) handleListTools(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"tools": []map[string]any{
			{
				"name":        "vector_add",
				"description": "Store a durable fact, decision, or preference in vector memory",
				"parameters": map[string]any{
					"text": map[string]any{"type": "string", "required": true, "description": "Text to store"},
					"meta": map[string]any{"type": "object", "required": false, "description": "Optional metadata"},
				},
			},
			{
				"name":        "vector_search",
				"description": "Semantic similarity search over stored memories",
				"parameters": map[string]any{
					"query": map[string]any{"type": "string", "required": true, "description": "Search query"},
					"k":     map[string]any{"type": "integer", "required": false, "description": "Max number of results"},
				},
			},
		},
	})
}
