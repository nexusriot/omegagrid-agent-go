package skills

import (
	"sort"
	"sync"

	"github.com/nexusriot/omegagrid-agent-go/internal/skills/markdown"
)

// entry holds a skill schema + its executor.
type entry struct {
	schema  Skill
	execute func(map[string]any) (any, error)
	// md is set for markdown skills so pipeline steps can call them with the
	// nesting depth carried along (see executorAt).
	md *markdown.Skill
}

// Registry is a thread-safe map of named skills.
type Registry struct {
	mu      sync.RWMutex
	entries map[string]entry
	// reserved names belong to built-in skills (and skill_creator). Dynamic
	// markdown skills may not take them: a skills/http_request.md would
	// otherwise silently replace the real http_request for every caller, and
	// skill_creator could overwrite — or delete — itself.
	reserved map[string]bool
}

func newRegistry() *Registry {
	return &Registry{entries: make(map[string]entry), reserved: make(map[string]bool)}
}

func (r *Registry) register(schema Skill, exec func(map[string]any) (any, error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[schema.Name] = entry{schema: schema, execute: exec}
}

// registerReserved registers a built-in skill and reserves its name.
func (r *Registry) registerReserved(schema Skill, exec func(map[string]any) (any, error)) {
	r.register(schema, exec)
	r.mu.Lock()
	r.reserved[schema.Name] = true
	r.mu.Unlock()
}

// isReserved reports whether name belongs to a built-in skill.
func (r *Registry) isReserved(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.reserved[name]
}

// registerMarkdown registers a markdown skill. A top-level call runs at depth
// 0; skills its pipeline steps invoke run one level deeper.
func (r *Registry) registerMarkdown(sk *markdown.Skill) {
	exec := func(args map[string]any) (any, error) {
		return sk.ExecuteAt(args, r.executorAt(1), 0)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[sk.Schema.Name] = entry{schema: mdToSkill(sk.Schema), execute: exec, md: sk}
}

// executorAt returns the executor handed to a pipeline whose steps run at the
// given nesting depth. Markdown targets are called with that depth so a
// pipeline that (directly or through others) calls itself stops at
// markdown.MaxPipelineDepth instead of recursing until the goroutine stack
// overflows — a fatal error that recover cannot catch, taking down the whole
// gateway.
func (r *Registry) executorAt(depth int) markdown.SkillExecutor {
	return func(name string, args map[string]any) (any, error) {
		r.mu.RLock()
		e, ok := r.entries[name]
		r.mu.RUnlock()
		if !ok {
			return nil, &errNotFound{name}
		}
		if e.md != nil {
			return e.md.ExecuteAt(args, r.executorAt(depth+1), depth)
		}
		return e.execute(args)
	}
}

func (r *Registry) unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, name)
}

// list returns every registered schema, sorted by name. The order matters:
// callers render it in the UI's skill list and splice it into the agent's
// system prompt, and Go's map iteration reshuffled both on every single call.
func (r *Registry) list() []Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Skill, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e.schema)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *Registry) execute(name string, args map[string]any) (any, error) {
	r.mu.RLock()
	e, ok := r.entries[name]
	r.mu.RUnlock()
	if !ok {
		return nil, &errNotFound{name}
	}
	return e.execute(args)
}

type errNotFound struct{ name string }

func (e *errNotFound) Error() string { return "skill not found: " + e.name }
