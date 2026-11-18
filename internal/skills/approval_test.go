package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nexusriot/omegagrid-agent-go/internal/config"
)

func TestNeedsApproval(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("wrap_shell.md", "---\nname: wrap_shell\ndescription: wraps\nsteps:\n  - name: s\n    skill: shell_command\n    args: {command: ls}\n---\n")
	write("wrap_wrap.md", "---\nname: wrap_wrap\ndescription: wraps a wrapper\nsteps:\n  - name: s\n    skill: wrap_shell\n---\n")
	write("flagged.md", "---\nname: flagged\ndescription: flagged\nrequires_approval: true\n---\nDo it.\n")
	write("harmless.md", "---\nname: harmless\ndescription: fine\n---\nHi.\n")

	c, err := New(config.Config{SkillsDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{
		"shell_command": true, "ssh_command": true, "file_write": true,
		"wrap_shell": true, "wrap_wrap": true, "flagged": true,
		"harmless": false, "weather": false,
	} {
		if got := c.NeedsApproval(name); got != want {
			t.Errorf("NeedsApproval(%s) = %v, want %v", name, got, want)
		}
	}
	list, _ := c.List()
	for _, s := range list {
		if s.Name == "flagged" && !s.RequiresApproval {
			t.Error("List does not mark flagged")
		}
	}

	c.SetApprovalPatterns([]string{"off"})
	if c.NeedsApproval("shell_command") || c.NeedsApproval("wrap_shell") {
		t.Error("off did not disable patterns")
	}
	if !c.NeedsApproval("flagged") {
		t.Error("frontmatter flag must survive APPROVAL_SKILLS=off")
	}
	c.SetApprovalPatterns([]string{"github_*"})
	if !c.NeedsApproval("github_create_issue") || c.NeedsApproval("gitlab_x") {
		t.Error("prefix pattern")
	}
}

// Every bundled example installs, loads, and calls only skills that exist.
func TestExamplesInstallAndReferenceRealSkills(t *testing.T) {
	dir := t.TempDir()
	c, err := New(config.Config{SkillsDir: dir, SkillExamplesSeeded: true})
	if err != nil {
		t.Fatal(err)
	}
	native := map[string]bool{"web_search": true, "schedule_task": true, "bookmark": true}
	list, _ := c.List()
	have := map[string]bool{}
	for _, s := range list {
		have[s.Name] = true
	}
	n := 0
	for _, s := range list {
		if s.Kind != "markdown" {
			continue
		}
		n++
		c.reg.mu.RLock()
		md := c.reg.entries[s.Name].md
		c.reg.mu.RUnlock()
		for _, step := range md.Steps() {
			if !have[step] && !native[step] {
				t.Errorf("%s calls unknown skill %s", s.Name, step)
			}
		}
	}
	if n < 5 {
		t.Fatalf("only %d example skills loaded", n)
	}
}

func TestNeedsApprovalForArgGatedSkill(t *testing.T) {
	dir := t.TempDir()
	c, err := New(config.Config{SkillsDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	c.reg.argApproval["fake_api"] = func(args map[string]any) bool { return args["method"] != "GET" }
	if c.NeedsApprovalFor("fake_api", map[string]any{"method": "GET"}) {
		t.Fatal("GET gated")
	}
	if !c.NeedsApprovalFor("fake_api", map[string]any{"method": "DELETE"}) {
		t.Fatal("DELETE not gated")
	}
	if c.NeedsApproval("fake_api") {
		t.Fatal("name-only check should not gate an argument-gated skill")
	}
	// A pipeline step's args are templates, so a pipeline calling an
	// argument-gated skill is gated as a whole.
	body := "---\nname: api_wrap\ndescription: x\nsteps:\n  - name: s\n    skill: openapi\n    args: {action: call, operation: listPets}\n---\n"
	if err := os.WriteFile(filepath.Join(dir, "api_wrap.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.loadMarkdownSkills(); err != nil {
		t.Fatal(err)
	}
	if !c.NeedsApproval("api_wrap") {
		t.Fatal("pipeline around openapi not gated")
	}
	// The real openapi skill errs closed when nothing is configured.
	if !c.NeedsApprovalFor("openapi", map[string]any{"action": "call", "operation": "x"}) {
		t.Fatal("unresolvable openapi call not gated")
	}
	if c.NeedsApprovalFor("openapi", map[string]any{"action": "operations"}) {
		t.Fatal("openapi search gated")
	}
}
