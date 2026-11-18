package skills

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/nexusriot/omegagrid-agent-go/internal/skills/builtin"
	"github.com/nexusriot/omegagrid-agent-go/internal/skills/markdown"
)

// entry holds a skill schema + its executor.
type entry struct {
	schema  Skill
	execute builtin.ContextExecutor
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
	// argApproval decides per call for skills whose risk depends on their
	// arguments (openapi: GET is a read, DELETE is not). Set at startup only.
	argApproval map[string]func(args map[string]any) bool
}

func newRegistry() *Registry {
	return &Registry{entries: make(map[string]entry), reserved: make(map[string]bool),
		argApproval: map[string]func(map[string]any) bool{}}
}

func (r *Registry) register(schema Skill, exec builtin.ContextExecutor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[schema.Name] = entry{schema: schema, execute: exec}
}

// registerNew registers schema only when no skill of that name exists yet,
// reporting whether it did. The check and the insert share one lock.
func (r *Registry) registerNew(schema Skill, exec builtin.ContextExecutor) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, taken := r.entries[schema.Name]; taken || r.reserved[schema.Name] {
		return false
	}
	r.entries[schema.Name] = entry{schema: schema, execute: exec}
	return true
}

// registerReserved registers a built-in skill and reserves its name.
func (r *Registry) registerReserved(schema Skill, exec builtin.ContextExecutor) {
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
	exec := func(ctx context.Context, args map[string]any) (any, error) {
		budget := &atomic.Int64{}
		budget.Store(maxPipelineCalls)
		return sk.ExecuteAt(ctx, args, r.executorAt(1, budget), 0)
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
//
// Depth bounds nesting, not width: two pipelines of N steps that call each
// other fan out to N^5 leaf calls (N=20 measured 3.2M calls and 2.8 GB of
// heap; N=30 runs the process out of memory). budget is shared by every
// nested call of one top-level execution and stops the fan-out at
// maxPipelineCalls.
func (r *Registry) executorAt(depth int, budget *atomic.Int64) markdown.SkillExecutor {
	return func(ctx context.Context, name string, args map[string]any) (any, error) {
		if budget.Add(-1) < 0 {
			return nil, fmt.Errorf("pipeline call budget of %d skill calls exhausted", maxPipelineCalls)
		}
		r.mu.RLock()
		e, ok := r.entries[name]
		r.mu.RUnlock()
		if !ok {
			return nil, &errNotFound{name}
		}
		if e.md != nil {
			return e.md.ExecuteAt(ctx, args, r.executorAt(depth+1, budget), depth)
		}
		return e.execute(ctx, args)
	}
}

// maxPipelineCalls caps the skill steps one top-level pipeline execution may
// run across all of its nesting levels.
const maxPipelineCalls = 200

// unregisterRuntime removes a Register'd (remote) skill, never a built-in or
// markdown one.
func (r *Registry) unregisterRuntime(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[name]
	if !ok || r.reserved[name] || e.md != nil {
		return false
	}
	delete(r.entries, name)
	return true
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
	for name, e := range r.entries {
		sch := e.schema
		switch {
		case r.reserved[name]:
			sch.Kind = "builtin"
		case e.md != nil:
			sch.Kind = "markdown"
		default:
			sch.Kind = "remote"
		}
		out = append(out, sch)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *Registry) execute(ctx context.Context, name string, args map[string]any) (any, error) {
	r.mu.RLock()
	e, ok := r.entries[name]
	r.mu.RUnlock()
	if !ok {
		return nil, &errNotFound{name}
	}
	return e.execute(ctx, args)
}

type errNotFound struct{ name string }

func (e *errNotFound) Error() string { return "skill not found: " + e.name }
