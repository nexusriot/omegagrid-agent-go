// Package markdown loads *.md skill files and executes them as HTTP-endpoint,
// pipeline, or prompt-only skills.  Mirrors Python's MarkdownSkill exactly.
package markdown

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// SkillExecutor lets pipeline steps invoke other registered skills by name.
type SkillExecutor func(name string, args map[string]any) (any, error)

// Param describes a single skill parameter.
type Param struct {
	Type        string
	Description string
	Required    bool
}

// SkillSchema carries the skill metadata used by the parent skills package.
type SkillSchema struct {
	Name        string
	Description string
	Parameters  map[string]Param
	Body        string
}

// Skill is a parsed markdown skill ready for execution.
type Skill struct {
	Schema   SkillSchema
	body     string // instructions block after frontmatter
	endpoint string
	method   string
	timeout  float64
	steps    []step
}

type step struct {
	Name     string         `yaml:"name"`
	Skill    string         `yaml:"skill"`
	Endpoint string         `yaml:"endpoint"`
	Method   string         `yaml:"method"`
	Headers  map[string]any `yaml:"headers"`
	Params   map[string]any `yaml:"params"`
	Body     map[string]any `yaml:"body"`
	Args     map[string]any `yaml:"args"`
}

// Execute runs the skill; executor is used for pipeline skill-steps.
func (s *Skill) Execute(args map[string]any, executor SkillExecutor) (any, error) {
	if len(s.steps) > 0 {
		return s.execPipeline(args, executor)
	}
	if s.endpoint != "" {
		return s.execSingle(args)
	}
	// prompt-only
	return map[string]any{
		"skill_type":   "prompt_only",
		"instructions": s.body,
		"parameters":   args,
		"directive": "This skill has no external endpoint — YOU are the execution engine. " +
			"Use the instructions above to generate the answer yourself, " +
			"then return it immediately as your type='final' answer. " +
			"Do NOT call this skill again.",
	}, nil
}

func (s *Skill) execSingle(args map[string]any) (any, error) {
	cl := &http.Client{Timeout: time.Duration(s.timeout * float64(time.Second))}
	hdrs := http.Header{"User-Agent": {"OmegaGridAgent/1.0"}}
	var resp *http.Response
	var err error
	if strings.ToUpper(s.method) == "POST" {
		hdrs.Set("Content-Type", "application/json")
		b, _ := json.Marshal(args)
		req, _ := http.NewRequest(http.MethodPost, s.endpoint, strings.NewReader(string(b)))
		req.Header = hdrs
		resp, err = cl.Do(req)
	} else {
		req, _ := http.NewRequest(http.MethodGet, s.endpoint, nil)
		req.Header = hdrs
		q := req.URL.Query()
		for k, v := range args {
			q.Set(k, fmt.Sprintf("%v", v))
		}
		req.URL.RawQuery = q.Encode()
		resp, err = cl.Do(req)
	}
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var body any
	if jerr := json.Unmarshal(raw, &body); jerr != nil {
		body = string(raw[:min(4000, len(raw))])
	}
	return map[string]any{"status_code": resp.StatusCode, "body": body}, nil
}

func (s *Skill) execPipeline(kwargs map[string]any, executor SkillExecutor) (any, error) {
	ctx := map[string]any{}
	var results []any
	cl := &http.Client{Timeout: time.Duration(s.timeout * float64(time.Second))}

	for i, st := range s.steps {
		name := st.Name
		if name == "" {
			name = fmt.Sprintf("step_%d", i+1)
		}
		// skill step
		if st.Skill != "" {
			var parsed any
			if executor == nil {
				parsed = map[string]any{"error": "no skill executor available"}
			} else {
				resolvedArgs := resolveObj(st.Args, kwargs, ctx).(map[string]any)
				var execErr error
				parsed, execErr = executor(st.Skill, resolvedArgs)
				if execErr != nil {
					parsed = map[string]any{"error": execErr.Error(), "skill": st.Skill}
				}
			}
			ctx[name] = parsed
			results = append(results, map[string]any{"step": name, "kind": "skill", "skill": st.Skill, "body": parsed})
			continue
		}
		// http step
		endpoint := resolveStr(st.Endpoint, kwargs, ctx)
		method := strings.ToUpper(st.Method)
		if method == "" {
			method = "GET"
		}
		resolvedParams := resolveObj(st.Params, kwargs, ctx).(map[string]any)
		resolvedBody := resolveObj(st.Body, kwargs, ctx).(map[string]any)
		hdrs := http.Header{"User-Agent": {"OmegaGridAgent/1.0"}}
		for k, v := range st.Headers {
			hdrs.Set(k, fmt.Sprintf("%v", v))
		}
		var parsed any
		var statusCode int
		if method == "POST" {
			hdrs.Set("Content-Type", "application/json")
			b, _ := json.Marshal(resolvedBody)
			req, _ := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(b)))
			req.Header = hdrs
			q := req.URL.Query()
			for k, v := range resolvedParams {
				q.Set(k, fmt.Sprintf("%v", v))
			}
			req.URL.RawQuery = q.Encode()
			resp, err := cl.Do(req)
			if err != nil {
				parsed = map[string]any{"error": err.Error()}
			} else {
				statusCode = resp.StatusCode
				raw, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if jerr := json.Unmarshal(raw, &parsed); jerr != nil {
					parsed = string(raw[:min(4000, len(raw))])
				}
			}
		} else {
			req, _ := http.NewRequest(http.MethodGet, endpoint, nil)
			req.Header = hdrs
			q := req.URL.Query()
			for k, v := range kwargs {
				q.Set(k, fmt.Sprintf("%v", v))
			}
			for k, v := range resolvedParams {
				q.Set(k, fmt.Sprintf("%v", v))
			}
			req.URL.RawQuery = q.Encode()
			resp, err := cl.Do(req)
			if err != nil {
				parsed = map[string]any{"error": err.Error()}
			} else {
				statusCode = resp.StatusCode
				raw, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if jerr := json.Unmarshal(raw, &parsed); jerr != nil {
					parsed = string(raw[:min(4000, len(raw))])
				}
			}
		}
		ctx[name] = parsed
		results = append(results, map[string]any{"step": name, "kind": "http", "status": statusCode, "body": parsed})
	}
	return map[string]any{
		"pipeline":        s.Schema.Name,
		"steps_completed": len(results),
		"results":         results,
		"instructions":    s.body,
	}, nil
}

var placeholderRE = regexp.MustCompile(`\{\{(.+?)\}\}`)

func resolveStr(text string, params, ctx map[string]any) string {
	return placeholderRE.ReplaceAllStringFunc(text, func(m string) string {
		key := m[2 : len(m)-2]
		return resolveValue(key, params, ctx)
	})
}

func resolveValue(key string, params, ctx map[string]any) string {
	if v, ok := params[key]; ok {
		return fmt.Sprintf("%v", v)
	}
	parts := strings.SplitN(key, ".", 2)
	if obj, ok := ctx[parts[0]]; ok {
		if len(parts) == 1 {
			return fmt.Sprintf("%v", obj)
		}
		return dotPath(obj, parts[1])
	}
	return "{{" + key + "}}"
}

func dotPath(obj any, path string) string {
	parts := strings.Split(path, ".")
	cur := obj
	for _, p := range parts {
		switch v := cur.(type) {
		case map[string]any:
			cur = v[p]
		default:
			return ""
		}
	}
	return fmt.Sprintf("%v", cur)
}

func resolveObj(obj any, params, ctx map[string]any) any {
	switch v := obj.(type) {
	case nil:
		return map[string]any{}
	case string:
		return resolveStr(v, params, ctx)
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, val := range v {
			out[k] = resolveObj(val, params, ctx)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = resolveObj(item, params, ctx)
		}
		return out
	default:
		return v
	}
}

type frontmatter struct {
	Name        string                    `yaml:"name"`
	Description string                    `yaml:"description"`
	Parameters  map[string]map[string]any `yaml:"parameters"`
	Endpoint    string                    `yaml:"endpoint"`
	Method      string                    `yaml:"method"`
	Timeout     float64                   `yaml:"timeout"`
	Steps       []step                    `yaml:"steps"`
}

func parseFrontmatter(content string) (frontmatter, string, error) {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "---") {
		return frontmatter{}, content, nil
	}
	end := strings.Index(content[3:], "---")
	if end < 0 {
		return frontmatter{}, content, nil
	}
	yamlStr := content[3 : end+3]
	body := strings.TrimSpace(content[end+6:])
	var fm frontmatter
	if err := yaml.Unmarshal([]byte(yamlStr), &fm); err != nil {
		return fm, body, err
	}
	return fm, body, nil
}

func fmToSkillSchema(fm frontmatter) SkillSchema {
	params := make(map[string]Param, len(fm.Parameters))
	for k, v := range fm.Parameters {
		p := Param{}
		if t, ok := v["type"].(string); ok {
			p.Type = t
		}
		if d, ok := v["description"].(string); ok {
			p.Description = d
		}
		if r, ok := v["required"].(bool); ok {
			p.Required = r
		}
		params[k] = p
	}
	return SkillSchema{
		Name:        fm.Name,
		Description: fm.Description,
		Parameters:  params,
	}
}

// Load reads a single .md file and returns a *Skill, or error.
func Load(path string) (*Skill, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fm, body, err := parseFrontmatter(string(raw))
	if err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}
	if fm.Name == "" {
		return nil, fmt.Errorf("missing name field")
	}
	if fm.Description == "" {
		fm.Description = fmt.Sprintf("Skill from %s", filepath.Base(path))
	}
	method := strings.ToUpper(fm.Method)
	if method == "" {
		method = "GET"
	}
	timeout := fm.Timeout
	if timeout <= 0 {
		timeout = 30
	}
	return &Skill{
		Schema:   fmToSkillSchema(fm),
		body:     body,
		endpoint: fm.Endpoint,
		method:   method,
		timeout:  timeout,
		steps:    fm.Steps,
	}, nil
}

// LoadDir loads all *.md files from dir, returning one Skill per valid file.
func LoadDir(dir string) ([]*Skill, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Skill
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		s, err := Load(filepath.Join(dir, e.Name()))
		if err != nil {
			continue // skip invalid files silently
		}
		out = append(out, s)
	}
	return out, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
