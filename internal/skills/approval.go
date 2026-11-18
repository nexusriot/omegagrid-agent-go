package skills

import (
	"strings"
)

// DefaultApprovalSkills are the skills that need a human yes before the agent
// may run them (APPROVAL_SKILLS overrides): they execute commands or write
// files on behalf of a model that may have misread the request.
var DefaultApprovalSkills = []string{"shell_command", "ssh_command", "file_write"}

// SetApprovalPatterns sets which skills need approval. An entry is a skill
// name or a prefix ending in "*" ("github_*" covers every tool of an MCP
// server named github). "off" or "none" alone disables the list.
func (c *Client) SetApprovalPatterns(patterns []string) {
	var out []string
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if l := strings.ToLower(p); l == "off" || l == "none" {
			out = nil
			break
		}
		out = append(out, p)
	}
	c.approvalMu.Lock()
	c.approval = out
	c.approvalMu.Unlock()
}

// ApprovalPatterns returns the configured patterns.
func (c *Client) ApprovalPatterns() []string {
	c.approvalMu.RLock()
	defer c.approvalMu.RUnlock()
	return append([]string(nil), c.approval...)
}

func matchApproval(patterns []string, name string) bool {
	for _, p := range patterns {
		if strings.HasSuffix(p, "*") {
			if strings.HasPrefix(name, strings.TrimSuffix(p, "*")) {
				return true
			}
		} else if p == name {
			return true
		}
	}
	return false
}

// NeedsApproval reports whether running name requires a human yes: it matches
// a configured pattern, its markdown file sets requires_approval, or it is a
// pipeline with a step (at any depth) that needs approval — otherwise a
// two-line pipeline wrapping shell_command would walk around the gate.
func (c *Client) NeedsApproval(name string) bool {
	return c.needsApproval(name, c.ApprovalPatterns(), 0)
}

// NeedsApprovalFor is NeedsApproval for one concrete call: it also asks the
// skill's own argument check (openapi gates every non-GET call).
func (c *Client) NeedsApprovalFor(name string, args map[string]any) bool {
	if c.NeedsApproval(name) {
		return true
	}
	if fn := c.reg.argApproval[name]; fn != nil {
		return fn(args)
	}
	return false
}

func (c *Client) needsApproval(name string, patterns []string, depth int) bool {
	if matchApproval(patterns, name) {
		return true
	}
	// A pipeline step's arguments are templates, unknown until it runs, so a
	// pipeline calling an argument-gated skill is gated as a whole (depth > 0
	// only: the skill itself is decided per call by NeedsApprovalFor).
	if depth > 0 && c.reg.argApproval[name] != nil {
		return true
	}
	if depth > 6 {
		return false
	}
	c.reg.mu.RLock()
	e, ok := c.reg.entries[name]
	c.reg.mu.RUnlock()
	if !ok || e.md == nil {
		return false
	}
	if e.md.RequiresApproval() {
		return true
	}
	for _, step := range e.md.Steps() {
		if step != name && c.needsApproval(step, patterns, depth+1) {
			return true
		}
	}
	return false
}
