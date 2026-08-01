package skills

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/nexusriot/omegagrid-agent-go/internal/config"
)

// list() feeds both the UI skill list and the agent's system prompt; Go map
// iteration reshuffled it on every call.
func TestListIsSortedAndStable(t *testing.T) {
	c, err := New(config.Config{SkillsDir: t.TempDir()})
	if err != nil {
		t.Fatalf("skills.New: %v", err)
	}

	first, err := c.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(first) < 5 {
		t.Fatalf("only %d skills registered, expected the built-in set", len(first))
	}

	names := make([]string, len(first))
	for i, s := range first {
		names[i] = s.Name
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("List() is not sorted by name: %v", names)
	}

	for i := 0; i < 25; i++ {
		again, _ := c.List()
		for j := range again {
			if again[j].Name != first[j].Name {
				t.Fatalf("List() order changed between calls at %d: %q vs %q", j, again[j].Name, first[j].Name)
			}
		}
	}
}

// skill_creator turns the name argument into a path, so every action that does
// must validate it. show and delete used to join it raw.
func TestSkillCreatorRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	skillsDir := filepath.Join(root, "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// A file the caller must not be able to read or remove.
	outside := filepath.Join(root, "secret.md")
	if err := os.WriteFile(outside, []byte("---\nname: secret\n---\n"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	c, err := New(config.Config{SkillsDir: skillsDir})
	if err != nil {
		t.Fatalf("skills.New: %v", err)
	}

	for _, name := range []string{"../secret", "../../etc/passwd", "..", "a/../../secret"} {
		for _, action := range []string{"show", "delete"} {
			res, err := c.Execute("skill_creator", map[string]any{"action": action, "name": name})
			if err != nil {
				t.Fatalf("%s %q: %v", action, name, err)
			}
			m, _ := res.(map[string]any)
			msg, _ := m["error"].(string)
			if !strings.Contains(msg, "invalid skill name") {
				t.Fatalf("%s %q was not rejected: %v", action, name, res)
			}
		}
	}

	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("file outside SKILLS_DIR was removed: %v", err)
	}
}

// Deleting a skill must unregister the name the registry is actually keyed by —
// the frontmatter name, which need not match the filename.
func TestSkillCreatorDeleteUnregistersFrontmatterName(t *testing.T) {
	dir := t.TempDir()
	// File is my_file.md but declares name: my_skill.
	if err := os.WriteFile(filepath.Join(dir, "my_file.md"),
		[]byte("---\nname: my_skill\ndescription: d\n---\n\nbody\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	c, err := New(config.Config{SkillsDir: dir})
	if err != nil {
		t.Fatalf("skills.New: %v", err)
	}
	if _, err := c.Execute("my_skill", map[string]any{}); err != nil {
		t.Fatalf("skill not registered before delete: %v", err)
	}

	if _, err := c.Execute("skill_creator", map[string]any{"action": "delete", "name": "my_file"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := c.Execute("my_skill", map[string]any{}); err == nil {
		t.Fatal("skill is still callable after its file was deleted")
	}
}

func TestSkillCreatorCreateShowDeleteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c, err := New(config.Config{SkillsDir: dir})
	if err != nil {
		t.Fatalf("skills.New: %v", err)
	}

	created, err := c.Execute("skill_creator", map[string]any{
		"action":       "create",
		"name":         "greet_user",
		"description":  "Greet someone",
		"instructions": "Say hello to {{name}}.",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if m, _ := created.(map[string]any); m["status"] != "created" {
		t.Fatalf("create result = %v", created)
	}

	shown, err := c.Execute("skill_creator", map[string]any{"action": "show", "name": "greet_user"})
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	content, _ := shown.(map[string]any)["content"].(string)
	if !strings.Contains(content, "name: greet_user") {
		t.Fatalf("show returned unexpected content: %q", content)
	}

	if _, err := c.Execute("skill_creator", map[string]any{"action": "delete", "name": "greet_user"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "greet_user.md")); !os.IsNotExist(err) {
		t.Fatalf("skill file still present after delete: %v", err)
	}
}
