// Package skills provides an in-process skill registry backed by Go
// implementations of every Python sidecar skill.  The public API (List /
// Execute) is unchanged from the former HTTP-based client.
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nexusriot/omegagrid-agent-go/internal/config"
	"github.com/nexusriot/omegagrid-agent-go/internal/skills/builtin"
	"github.com/nexusriot/omegagrid-agent-go/internal/skills/markdown"
)

// Param describes a single skill parameter.
type Param struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// Skill is the public schema returned by List().
type Skill struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Parameters  map[string]Param `json:"parameters"`
	Body        string           `json:"body,omitempty"`
}

// Client is the in-process skill manager.  Callers use the same List() and
// Execute() methods they used with the former HTTP sidecar client.
type Client struct {
	reg       *Registry
	skillsDir string
}

// New builds the skill registry from built-in skills and any *.md files in cfg.SkillsDir.
func New(cfg config.Config) (*Client, error) {
	reg := newRegistry()
	c := &Client{reg: reg, skillsDir: cfg.SkillsDir}

	// executor closure used by markdown pipeline steps + skill_creator
	executor := func(name string, args map[string]any) (any, error) {
		return reg.execute(name, args)
	}

	registerBuiltins(reg, cfg)

	reg.register(skillCreatorSchema(), skillCreatorFunc(cfg.SkillsDir, reg, executor))

	if err := c.loadMarkdownSkills(executor); err != nil {
		return nil, fmt.Errorf("load markdown skills: %w", err)
	}

	return c, nil
}

func (c *Client) loadMarkdownSkills(executor markdown.SkillExecutor) error {
	loaded, err := markdown.LoadDir(c.skillsDir)
	if err != nil {
		return err
	}
	for _, s := range loaded {
		sk := s // capture
		c.reg.register(mdToSkill(sk.Schema), func(args map[string]any) (any, error) {
			return sk.Execute(args, executor)
		})
	}
	return nil
}

// mdToSkill converts markdown.SkillSchema → skills.Skill.
func mdToSkill(ms markdown.SkillSchema) Skill {
	params := make(map[string]Param, len(ms.Parameters))
	for k, v := range ms.Parameters {
		params[k] = Param{Type: v.Type, Description: v.Description, Required: v.Required}
	}
	return Skill{Name: ms.Name, Description: ms.Description, Parameters: params, Body: ms.Body}
}

// List returns all registered skill schemas.  Called on every agent run so
// hot-registered skills (skill_creator) are immediately visible.
func (c *Client) List() ([]Skill, error) { return c.reg.list(), nil }

// Execute runs a skill by name.
func (c *Client) Execute(name string, args map[string]any) (any, error) {
	return c.reg.execute(name, args)
}

// toSkill converts a builtin.Skill to the public skills.Skill type.
func toSkill(b builtin.Skill) Skill {
	params := make(map[string]Param, len(b.Parameters))
	for k, v := range b.Parameters {
		params[k] = Param{Type: v.Type, Description: v.Description, Required: v.Required}
	}
	return Skill{Name: b.Name, Description: b.Description, Parameters: params, Body: b.Body}
}

func registerBuiltins(reg *Registry, cfg config.Config) {
	t := cfg.SkillHTTPTimeout

	reg.register(toSkill(builtin.WeatherSchema()), builtin.Weather(t))
	reg.register(toSkill(builtin.HttpRequestSchema()), builtin.HttpRequest(t))
	reg.register(toSkill(builtin.WebScrapeSchema()), builtin.WebScrape(t))
	reg.register(toSkill(builtin.HttpHealthSchema()), builtin.HttpHealth())
	reg.register(toSkill(builtin.IpInfoSchema()), builtin.IpInfo(t))

	reg.register(toSkill(builtin.DnsLookupSchema()), builtin.DnsLookup())
	reg.register(toSkill(builtin.PingCheckSchema()), builtin.PingCheck())
	reg.register(toSkill(builtin.PortScanSchema()), builtin.PortScan())
	reg.register(toSkill(builtin.WhoisLookupSchema()), builtin.WhoisLookup())

	reg.register(toSkill(builtin.Base64Schema()), builtin.Base64())
	reg.register(toSkill(builtin.HashSchema()), builtin.Hash())
	reg.register(toSkill(builtin.UuidGenSchema()), builtin.UuidGen())
	reg.register(toSkill(builtin.PasswordGenSchema()), builtin.PasswordGen())
	reg.register(toSkill(builtin.CidrCalcSchema()), builtin.CidrCalc())

	reg.register(toSkill(builtin.DateTimeSchema()), builtin.DateTime())
	reg.register(toSkill(builtin.MathEvalSchema()), builtin.MathEval())
	reg.register(toSkill(builtin.CronScheduleSchema()), builtin.CronSchedule())

	reg.register(toSkill(builtin.ShellCommandSchema()), builtin.ShellCommand(cfg.SkillShellEnabled))
	reg.register(toSkill(builtin.SshCommandSchema()), builtin.SshCommand(builtin.SSHConfig{
		Enabled:     cfg.SkillSSHEnabled,
		PrivKey:     cfg.SkillSSHPrivKey,
		DefaultUser: cfg.SkillSSHDefaultUser,
		IdentFile:   cfg.SkillSSHIdentFile,
	}))

	reg.register(toSkill(builtin.QrGenerateSchema()), builtin.QrGenerate())
}

func skillCreatorSchema() Skill {
	return Skill{
		Name: "skill_creator",
		Description: "Create, list, show, or delete dynamic skills. Use action='create' " +
			"when a user asks for a capability that no existing skill covers.",
		Parameters: map[string]Param{
			"action":            {Type: "string", Description: "create | list | show | delete", Required: true},
			"name":              {Type: "string", Description: "Skill name (lowercase, underscores, 2-49 chars)", Required: false},
			"description":       {Type: "string", Description: "Short skill description", Required: false},
			"parameters_schema": {Type: "object", Description: `Parameter definitions, e.g. {"city":{"type":"string","required":true}}`, Required: false},
			"endpoint":          {Type: "string", Description: "HTTP endpoint (omit for prompt-only)", Required: false},
			"method":            {Type: "string", Description: "GET or POST (default GET)", Required: false},
			"instructions":      {Type: "string", Description: "Free-text body appended after frontmatter", Required: false},
			"steps": {
				Type: "array",
				Description: `Pipeline steps. Each step: HTTP (name+endpoint) or skill (name+skill+args). ` +
					`Use {{param}} and {{step_name.path}} for placeholders.`,
				Required: false,
			},
		},
	}
}

var safeNameChars = func(name string) bool {
	if len(name) < 2 || len(name) > 49 {
		return false
	}
	for i, r := range name {
		if i == 0 {
			if !isAlpha(r) {
				return false
			}
		} else {
			if !isAlpha(r) && !isDigit(r) && r != '_' {
				return false
			}
		}
	}
	return true
}

func skillCreatorFunc(skillsDir string, reg *Registry, executor markdown.SkillExecutor) func(map[string]any) (any, error) {
	return func(args map[string]any) (any, error) {
		action := strings.ToLower(strings.TrimSpace(strArg(args, "action")))
		switch action {
		case "create":
			return scCreate(skillsDir, reg, executor, args)
		case "list":
			return scList(skillsDir)
		case "show":
			return scShow(skillsDir, args)
		case "delete":
			return scDelete(skillsDir, reg, args)
		default:
			return map[string]any{"error": fmt.Sprintf("unknown action %q (use create/list/show/delete)", action)}, nil
		}
	}
}

func scCreate(dir string, reg *Registry, executor markdown.SkillExecutor, args map[string]any) (any, error) {
	name := strings.ToLower(strings.TrimSpace(strArg(args, "name")))
	if name == "" {
		return map[string]any{"error": "name is required for create"}, nil
	}
	if !safeNameChars(name) {
		return map[string]any{"error": fmt.Sprintf("invalid skill name %q (lowercase + underscores, 2-49 chars, start with letter)", name)}, nil
	}
	desc := strings.TrimSpace(strArg(args, "description"))
	if desc == "" {
		return map[string]any{"error": "description is required for create"}, nil
	}

	paramsRaw := args["parameters_schema"]
	if paramsRaw == nil {
		paramsRaw = args["parameters"]
	}
	paramsMap := normalizeParamsSchema(paramsRaw)

	endpoint := strings.TrimSpace(strArg(args, "endpoint"))
	method := strings.ToUpper(strings.TrimSpace(strArg(args, "method")))
	if method == "" {
		method = "GET"
	}
	instructions := strings.TrimSpace(strArg(args, "instructions"))
	steps := normalizeSteps(args["steps"])

	// build YAML frontmatter
	fm := map[string]any{"name": name, "description": desc}
	if len(paramsMap) > 0 {
		fm["parameters"] = paramsMap
	}
	if len(steps) > 0 {
		fm["steps"] = steps
	} else if endpoint != "" {
		fm["endpoint"] = endpoint
		fm["method"] = method
	}

	content := renderMarkdown(fm, instructions)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return map[string]any{"error": fmt.Sprintf("mkdir %s: %v", dir, err)}, nil
	}
	fpath := filepath.Join(dir, name+".md")
	overwrite := fileExists(fpath)
	if err := os.WriteFile(fpath, []byte(content), 0o644); err != nil {
		return map[string]any{"error": err.Error()}, nil
	}

	// hot-register
	s, err := markdown.Load(fpath)
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("reload skill: %v", err)}, nil
	}
	sk := s
	reg.register(mdToSkill(sk.Schema), func(a map[string]any) (any, error) {
		return sk.Execute(a, executor)
	})

	skillType := "prompt-only"
	if len(steps) > 0 {
		skillType = "pipeline"
	} else if endpoint != "" {
		skillType = "endpoint"
	}
	status := "created"
	if overwrite {
		status = "updated"
	}
	var paramKeys []string
	for k := range paramsMap {
		paramKeys = append(paramKeys, k)
	}
	return map[string]any{
		"status":       status,
		"skill_name":   name,
		"file":         fpath,
		"description":  desc,
		"type":         skillType,
		"steps_count":  len(steps),
		"has_endpoint": endpoint != "",
		"parameters":   paramKeys,
		"hint":         fmt.Sprintf("Skill '%s' is now registered and can be called immediately.", name),
	}, nil
}

func scList(dir string) (any, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return map[string]any{"dynamic_skills": []any{}, "count": 0}, nil
	}
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	var out []map[string]any
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		s, err := markdown.Load(filepath.Join(dir, e.Name()))
		if err != nil {
			out = append(out, map[string]any{"file": e.Name(), "error": "parse error"})
			continue
		}
		out = append(out, map[string]any{
			"name":        s.Schema.Name,
			"description": s.Schema.Description,
			"file":        e.Name(),
		})
	}
	return map[string]any{"dynamic_skills": out, "count": len(out)}, nil
}

func scShow(dir string, args map[string]any) (any, error) {
	name := strings.ToLower(strings.TrimSpace(strArg(args, "name")))
	if name == "" {
		return map[string]any{"error": "name is required for show"}, nil
	}
	fpath := filepath.Join(dir, name+".md")
	raw, err := os.ReadFile(fpath)
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("skill file not found: %s.md", name)}, nil
	}
	return map[string]any{"name": name, "file": fpath, "content": string(raw)}, nil
}

func scDelete(dir string, reg *Registry, args map[string]any) (any, error) {
	name := strings.ToLower(strings.TrimSpace(strArg(args, "name")))
	if name == "" {
		return map[string]any{"error": "name is required for delete"}, nil
	}
	fpath := filepath.Join(dir, name+".md")
	if err := os.Remove(fpath); err != nil {
		if os.IsNotExist(err) {
			return map[string]any{"error": fmt.Sprintf("skill file not found: %s.md", name)}, nil
		}
		return map[string]any{"error": err.Error()}, nil
	}
	reg.unregister(name)
	return map[string]any{"status": "deleted", "skill_name": name}, nil
}

func strArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func isAlpha(r rune) bool { return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') }
func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func normalizeParamsSchema(raw any) map[string]any {
	switch v := raw.(type) {
	case nil:
		return nil
	case map[string]any:
		out := make(map[string]any, len(v))
		for pk, pv := range v {
			switch x := pv.(type) {
			case string:
				out[pk] = map[string]any{"type": x, "description": pk, "required": false}
			case map[string]any:
				out[pk] = x
			default:
				out[pk] = map[string]any{"type": "string", "description": fmt.Sprintf("%v", x), "required": false}
			}
		}
		return out
	default:
		return nil
	}
}

func normalizeSteps(raw any) []map[string]any {
	switch v := raw.(type) {
	case []any:
		out := make([]map[string]any, 0, len(v))
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	case []map[string]any:
		return v
	default:
		return nil
	}
}

func renderMarkdown(fm map[string]any, instructions string) string {
	// hand-craft YAML to preserve insertion order for readability
	var sb strings.Builder
	sb.WriteString("---\n")
	order := []string{"name", "description", "parameters", "endpoint", "method", "steps"}
	written := map[string]bool{}
	for _, k := range order {
		if v, ok := fm[k]; ok {
			writeYAMLField(&sb, k, v)
			written[k] = true
		}
	}
	for k, v := range fm {
		if !written[k] {
			writeYAMLField(&sb, k, v)
		}
	}
	sb.WriteString("---\n")
	if instructions != "" {
		sb.WriteString("\n")
		sb.WriteString(instructions)
		sb.WriteString("\n")
	}
	return sb.String()
}

func writeYAMLField(sb *strings.Builder, key string, val any) {
	switch v := val.(type) {
	case string:
		sb.WriteString(key + ": " + yamlQuote(v) + "\n")
	case bool:
		if v {
			sb.WriteString(key + ": true\n")
		} else {
			sb.WriteString(key + ": false\n")
		}
	case map[string]any:
		sb.WriteString(key + ":\n")
		for k2, v2 := range v {
			writeYAMLField2(sb, "  "+k2, v2, "  ")
		}
	case []map[string]any:
		sb.WriteString(key + ":\n")
		for _, item := range v {
			first := true
			for k2, v2 := range item {
				prefix := "    "
				if first {
					prefix = "  - "
					first = false
				}
				writeYAMLField2(sb, prefix+k2, v2, "    ")
			}
		}
	default:
		sb.WriteString(fmt.Sprintf("%s: %v\n", key, v))
	}
}

func writeYAMLField2(sb *strings.Builder, key string, val any, indent string) {
	switch v := val.(type) {
	case string:
		sb.WriteString(key + ": " + yamlQuote(v) + "\n")
	case bool:
		sb.WriteString(fmt.Sprintf("%s: %v\n", key, v))
	case map[string]any:
		sb.WriteString(key + ":\n")
		for k2, v2 := range v {
			writeYAMLField2(sb, indent+"  "+k2, v2, indent+"  ")
		}
	default:
		sb.WriteString(fmt.Sprintf("%s: %v\n", key, v))
	}
}

func yamlQuote(s string) string {
	if strings.ContainsAny(s, ":#{}[]|>&!'\"%@`") || strings.Contains(s, "\n") {
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
