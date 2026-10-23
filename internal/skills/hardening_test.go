package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nexusriot/omegagrid-agent-go/internal/config"
	"github.com/nexusriot/omegagrid-agent-go/internal/skills/markdown"
	"github.com/nexusriot/omegagrid-agent-go/internal/skills/netguard"
)

func newTestClient(t *testing.T, dir string) *Client {
	t.Helper()
	c, err := New(config.Config{SkillsDir: dir})
	if err != nil {
		t.Fatalf("skills.New: %v", err)
	}
	return c
}

func execMap(t *testing.T, c *Client, name string, args map[string]any) map[string]any {
	t.Helper()
	res, err := c.Execute(name, args)
	if err != nil {
		t.Fatalf("Execute(%s): %v", name, err)
	}
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("Execute(%s) = %T, want map", name, res)
	}
	return m
}

// skill_creator must not be able to replace a built-in, or itself.
func TestSkillCreatorRefusesBuiltinNames(t *testing.T) {
	dir := t.TempDir()
	c := newTestClient(t, dir)
	for _, name := range []string{"http_request", "shell_command", "skill_creator"} {
		m := execMap(t, c, "skill_creator", map[string]any{
			"action": "create", "name": name, "description": "evil twin",
		})
		if errStr, _ := m["error"].(string); !strings.Contains(errStr, "built-in") {
			t.Errorf("create %q: got %#v, want built-in refusal", name, m)
		}
		if fileExists(filepath.Join(dir, name+".md")) {
			t.Errorf("create %q wrote a file", name)
		}
	}
	// skill_creator still works.
	if m := execMap(t, c, "skill_creator", map[string]any{"action": "list"}); m["error"] != nil {
		t.Fatalf("skill_creator broken after refused create: %#v", m)
	}
}

// A markdown file declaring a built-in's name is skipped at load, and deleting
// it leaves the built-in registered.
func TestMarkdownCannotShadowOrDeleteBuiltin(t *testing.T) {
	dir := t.TempDir()
	shadow := "---\nname: math_eval\ndescription: shadow\n---\nprompt only\n"
	if err := os.WriteFile(filepath.Join(dir, "math_eval.md"), []byte(shadow), 0o644); err != nil {
		t.Fatal(err)
	}
	c := newTestClient(t, dir)

	m := execMap(t, c, "math_eval", map[string]any{"expression": "1+1"})
	if m["result"] != 2.0 {
		t.Fatalf("math_eval shadowed by markdown file: %#v", m)
	}

	m = execMap(t, c, "skill_creator", map[string]any{"action": "delete", "name": "math_eval"})
	if m["status"] != "deleted" {
		t.Fatalf("delete stray file: %#v", m)
	}
	m = execMap(t, c, "math_eval", map[string]any{"expression": "2+2"})
	if m["result"] != 4.0 {
		t.Fatalf("math_eval unregistered by delete: %#v", m)
	}
	m = execMap(t, c, "skill_creator", map[string]any{"action": "delete", "name": "skill_creator"})
	if _, err := c.Execute("skill_creator", map[string]any{"action": "list"}); err != nil {
		t.Fatalf("skill_creator unregistered by delete: %v (%#v)", err, m)
	}
}

func TestSkillCreatorRejectsSelfReferencingPipeline(t *testing.T) {
	dir := t.TempDir()
	c := newTestClient(t, dir)
	m := execMap(t, c, "skill_creator", map[string]any{
		"action": "create", "name": "loop", "description": "recurses",
		"steps": []any{map[string]any{"name": "again", "skill": "loop"}},
	})
	if errStr, _ := m["error"].(string); !strings.Contains(errStr, "itself") {
		t.Fatalf("self-referencing pipeline accepted: %#v", m)
	}
	if fileExists(filepath.Join(dir, "loop.md")) {
		t.Fatal("self-referencing pipeline was written to disk")
	}
}

// Mutually recursive pipelines (written by hand, so create-time checks do not
// see them) used to overflow the goroutine stack — a fatal, unrecoverable
// crash. They must stop at the depth limit with an error.
func TestPipelineRecursionIsBounded(t *testing.T) {
	dir := t.TempDir()
	write := func(name, callee string) {
		body := "---\nname: " + name + "\ndescription: x\nsteps:\n  - name: s\n    skill: " + callee + "\n---\n"
		if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("ping_pong_a", "ping_pong_b")
	write("ping_pong_b", "ping_pong_a")
	c := newTestClient(t, dir)

	m := execMap(t, c, "ping_pong_a", map[string]any{})
	// Walk down the nested results to the innermost one.
	depth := 0
	cur := m
	for {
		results, _ := cur["results"].([]any)
		if len(results) == 0 {
			break
		}
		step, _ := results[0].(map[string]any)
		next, _ := step["body"].(map[string]any)
		if next == nil {
			break
		}
		cur = next
		depth++
	}
	if errStr, _ := cur["error"].(string); !strings.Contains(errStr, "nesting depth") {
		t.Fatalf("innermost result = %#v, want depth-limit error", cur)
	}
	if depth != markdown.MaxPipelineDepth {
		t.Errorf("recursion stopped after %d levels, want %d", depth, markdown.MaxPipelineDepth)
	}
}

func TestNewAppliesPrivateNetworkPolicy(t *testing.T) {
	prev := netguard.AllowPrivate()
	t.Cleanup(func() { netguard.SetAllowPrivate(prev) })

	if _, err := New(config.Config{SkillsDir: t.TempDir(), SkillBlockPrivateNetworks: true}); err != nil {
		t.Fatal(err)
	}
	if netguard.AllowPrivate() {
		t.Error("SkillBlockPrivateNetworks=true left private networks allowed")
	}
	if _, err := New(config.Config{SkillsDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if !netguard.AllowPrivate() {
		t.Error("zero Config blocked private networks; the default must allow them")
	}
}
