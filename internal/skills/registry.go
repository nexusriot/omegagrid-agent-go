package skills

import "sync"

// entry holds a skill schema + its executor.
type entry struct {
	schema  Skill
	execute func(map[string]any) (any, error)
}

// Registry is a thread-safe map of named skills.
type Registry struct {
	mu      sync.RWMutex
	entries map[string]entry
}

func newRegistry() *Registry {
	return &Registry{entries: make(map[string]entry)}
}

func (r *Registry) register(schema Skill, exec func(map[string]any) (any, error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[schema.Name] = entry{schema: schema, execute: exec}
}

func (r *Registry) unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, name)
}

func (r *Registry) list() []Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Skill, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e.schema)
	}
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
