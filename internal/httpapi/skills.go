package httpapi

import "net/http"

// handleListSkills returns all skills registered in the in-process skill registry.
func (d *Deps) handleListSkills(w http.ResponseWriter, _ *http.Request) {
	skills, err := d.Skills.List()
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skills": skills})
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
