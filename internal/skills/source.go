package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nexusriot/omegagrid-agent-go/internal/skills/markdown"
)

// ErrNotMarkdown is returned when a source operation names a skill that is
// not a markdown skill in SKILLS_DIR (a built-in, a remote tool, nothing).
var ErrNotMarkdown = errors.New("not a markdown skill")

// SkillSource returns the file content of a markdown skill.
func (c *Client) SkillSource(name string) (string, error) {
	if !safeNameChars(name) {
		return "", errors.New(invalidNameMsg(name))
	}
	raw, err := os.ReadFile(filepath.Join(c.skillsDir, name+".md"))
	if os.IsNotExist(err) {
		return "", ErrNotMarkdown
	}
	return string(raw), err
}

// ValidateSkillSource parses content as a markdown skill and checks that it
// may be saved: a valid name that is not a built-in's. It returns the schema
// the skill would register with.
func (c *Client) ValidateSkillSource(content string) (*Skill, error) {
	sk, err := markdown.Parse(content, "editor")
	if err != nil {
		return nil, err
	}
	name := sk.Schema.Name
	if !safeNameChars(name) {
		return nil, errors.New(invalidNameMsg(name))
	}
	if c.reg.isReserved(name) {
		return nil, fmt.Errorf("%q is a built-in skill and cannot be replaced", name)
	}
	for _, st := range sk.Steps() {
		if strings.EqualFold(strings.TrimSpace(st), name) {
			return nil, fmt.Errorf("pipeline step calls %q itself; recursive pipelines are not allowed", name)
		}
	}
	out := mdToSkill(sk.Schema)
	out.Kind = "markdown"
	return &out, nil
}

// SaveSkillSource validates content and writes it to SKILLS_DIR/<name>.md,
// replacing an existing file atomically, then registers it. created reports
// whether the skill is new.
func (c *Client) SaveSkillSource(content string) (*Skill, bool, error) {
	sch, err := c.ValidateSkillSource(content)
	if err != nil {
		return nil, false, err
	}
	path, created, err := writeSkillFile(c.skillsDir, sch.Name, content)
	if err != nil {
		return nil, false, err
	}
	sk, err := markdown.Load(path)
	if err != nil {
		return nil, false, err
	}
	c.reg.registerMarkdown(sk)
	return sch, created, nil
}

// DeleteMarkdownSkill removes a markdown skill's file and registration.
func (c *Client) DeleteMarkdownSkill(name string) error {
	res, _ := scDelete(c.skillsDir, c.reg, map[string]any{"name": name})
	if m, ok := res.(map[string]any); ok {
		if msg, bad := m["error"].(string); bad {
			if strings.Contains(msg, "not found") {
				return ErrNotMarkdown
			}
			return errors.New(msg)
		}
	}
	return nil
}

// writeSkillFile writes content through a hidden temp sibling, checks it
// loads, and renames it over <name>.md — so a definition that does not parse
// never replaces a working one.
func writeSkillFile(dir, name, content string) (path string, created bool, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path = filepath.Join(dir, name+".md")
	created = !fileExists(path)
	tmp, err := os.CreateTemp(dir, "."+name+"-*.tmp")
	if err != nil {
		return "", false, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once renamed
	_, werr := tmp.WriteString(content)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return "", false, werr
	}
	sk, err := markdown.Load(tmpPath)
	if err != nil {
		return "", false, fmt.Errorf("generated skill does not parse: %w", err)
	}
	if sk.Schema.Name != name {
		return "", false, fmt.Errorf("generated skill declares name %q, want %q", sk.Schema.Name, name)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return "", false, err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return "", false, err
	}
	return path, created, nil
}
