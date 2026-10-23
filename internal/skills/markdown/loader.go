// Package markdown loads *.md skill files and executes them as HTTP-endpoint,
// pipeline, or prompt-only skills.  Mirrors Python's MarkdownSkill exactly.
package markdown

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/nexusriot/omegagrid-agent-go/internal/skills/netguard"
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

// MaxPipelineDepth bounds how deeply pipelines may nest through skill steps.
// Without it a pipeline reaching itself (directly or via another pipeline)
// recursed until the goroutine stack overflowed — a fatal runtime error, not a
// panic, so nothing could recover it and the whole process died.
const MaxPipelineDepth = 5

// maxBodyBytes caps how much of an endpoint response is read into memory.
const maxBodyBytes = 2 << 20

// maxTextBody is how much of a non-JSON response body is returned.
const maxTextBody = 4000

// Execute runs the skill as a top-level call; executor is used for pipeline
// skill-steps.
func (s *Skill) Execute(args map[string]any, executor SkillExecutor) (any, error) {
	return s.ExecuteAt(args, executor, 0)
}

// ExecuteAt runs the skill at the given pipeline nesting depth (0 for a
// top-level call). The caller supplies an executor that runs nested skills at
// depth+1; a pipeline at MaxPipelineDepth or deeper refuses to run.
func (s *Skill) ExecuteAt(args map[string]any, executor SkillExecutor, depth int) (any, error) {
	if len(s.steps) > 0 {
		if depth >= MaxPipelineDepth {
			return map[string]any{"error": fmt.Sprintf(
				"pipeline %q exceeds the maximum nesting depth of %d (recursive pipeline?)",
				s.Schema.Name, MaxPipelineDepth)}, nil
		}
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
	cl := netguard.Client(time.Duration(s.timeout * float64(time.Second)))
	var query map[string]any
	if strings.ToUpper(s.method) != "POST" {
		query = args
	}
	resp, err := doRequest(cl, s.method, s.endpoint, nil, query, args)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	defer resp.Body.Close()
	body, truncated := readBody(resp.Body)
	out := map[string]any{"status_code": resp.StatusCode, "body": body}
	if truncated {
		out["truncated"] = true
	}
	return out, nil
}

// readBody reads a capped response body and decodes it as JSON, falling back
// to (at most maxTextBody bytes of) text. truncated reports that the body was
// cut, either by the read cap or the text limit.
func readBody(r io.Reader) (body any, truncated bool) {
	raw, _ := io.ReadAll(io.LimitReader(r, maxBodyBytes+1))
	if len(raw) > maxBodyBytes {
		raw, truncated = raw[:maxBodyBytes], true
	}
	if !truncated && json.Unmarshal(raw, &body) == nil {
		return body, false
	}
	if len(raw) > maxTextBody {
		raw, truncated = raw[:maxTextBody], true
	}
	if truncated {
		raw = trimPartialRune(raw)
	}
	return string(raw), truncated
}

// trimPartialRune drops an incomplete UTF-8 sequence at the end of b: a byte
// limit used to cut multi-byte characters in half, leaving a mangled tail.
func trimPartialRune(b []byte) []byte {
	for i := 1; i <= utf8.UTFMax && i <= len(b); i++ {
		if utf8.RuneStart(b[len(b)-i]) {
			if !utf8.FullRune(b[len(b)-i:]) {
				return b[:len(b)-i]
			}
			return b
		}
	}
	return b
}

// doRequest builds and sends one skill HTTP request. method selects GET/POST,
// query is added as URL parameters and body is JSON-encoded for POST.
//
// The error from http.NewRequest is honoured here: it returns a NIL request
// alongside the error for a URL it cannot parse, and skill endpoints come from
// markdown files whose {{placeholders}} may resolve to anything (or stay
// unresolved). The old `req, _ :=` sites panicked on those.
func doRequest(cl *http.Client, method, endpoint string, headers map[string]any, query, body map[string]any) (*http.Response, error) {
	hdrs := http.Header{"User-Agent": {"OmegaGridAgent/1.0"}}
	for k, v := range headers {
		hdrs.Set(k, fmt.Sprintf("%v", v))
	}

	var reader io.Reader
	post := strings.ToUpper(method) == "POST"
	if post {
		hdrs.Set("Content-Type", "application/json")
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request body: %w", err)
		}
		reader = strings.NewReader(string(b))
	}

	verb := http.MethodGet
	if post {
		verb = http.MethodPost
	}
	req, err := http.NewRequest(verb, endpoint, reader)
	if err != nil {
		return nil, err
	}
	req.Header = hdrs
	if len(query) > 0 {
		q := req.URL.Query()
		for k, v := range query {
			q.Set(k, fmt.Sprintf("%v", v))
		}
		req.URL.RawQuery = q.Encode()
	}
	return cl.Do(req)
}

func (s *Skill) execPipeline(kwargs map[string]any, executor SkillExecutor) (any, error) {
	ctx := map[string]any{}
	var results []any
	cl := netguard.Client(time.Duration(s.timeout * float64(time.Second)))

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
		// Escaped: a value substituted into the URL must stay data. Raw, an
		// argument such as "@evil.example/" or "x/../../admin?" rewrote the
		// host, path or query the skill author wrote.
		endpoint := resolveStrEscaped(st.Endpoint, kwargs, ctx, escapeURLValue)
		method := strings.ToUpper(st.Method)
		if method == "" {
			method = "GET"
		}
		resolvedParams := resolveObj(st.Params, kwargs, ctx).(map[string]any)
		resolvedBody := resolveObj(st.Body, kwargs, ctx).(map[string]any)
		// GET steps also inherit the skill's own kwargs as query parameters;
		// explicit step params win on conflict.
		query := resolvedParams
		if method != "POST" {
			query = make(map[string]any, len(kwargs)+len(resolvedParams))
			for k, v := range kwargs {
				query[k] = v
			}
			for k, v := range resolvedParams {
				query[k] = v
			}
		}

		var parsed any
		var statusCode int
		resp, err := doRequest(cl, method, endpoint, st.Headers, query, resolvedBody)
		if err != nil {
			parsed = map[string]any{"error": err.Error()}
		} else {
			statusCode = resp.StatusCode
			parsed, _ = readBody(resp.Body)
			resp.Body.Close()
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
	return resolveStrEscaped(text, params, ctx, nil)
}

// resolveStrEscaped is resolveStr with every substituted value passed through
// escape (when non-nil). Unresolved placeholders are left as written.
func resolveStrEscaped(text string, params, ctx map[string]any, escape func(string) string) string {
	return placeholderRE.ReplaceAllStringFunc(text, func(m string) string {
		key := m[2 : len(m)-2]
		v := resolveValue(key, params, ctx)
		if escape == nil || v == m {
			return v
		}
		return escape(v)
	})
}

// escapeURLValue percent-encodes everything outside RFC 3986's unreserved
// set. url.PathEscape alone is not enough: it leaves '@', ':', '&', '=' and
// '+' intact, so "https://api.example.com{{p}}" with p="@evil.example" moved
// the request to evil.example. Space becomes %20, valid in path and query.
func escapeURLValue(v string) string {
	return strings.ReplaceAll(url.QueryEscape(v), "+", "%20")
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

// parseFrontmatter splits a "---" delimited YAML header from the body. The
// closing delimiter must be a line of its own: matching any "---" substring
// ended the header inside a value (description: "a --- b"), producing a YAML
// error or a silently truncated schema.
func parseFrontmatter(content string) (frontmatter, string, error) {
	content = strings.TrimSpace(strings.ReplaceAll(content, "\r\n", "\n"))
	if !strings.HasPrefix(content, "---\n") {
		return frontmatter{}, content, nil
	}
	rest := content[len("---\n"):]
	var yamlStr, body string
	switch {
	case strings.HasPrefix(rest, "---\n") || rest == "---":
		yamlStr, body = "", strings.TrimPrefix(rest, "---")
	default:
		end := strings.Index(rest, "\n---\n")
		switch {
		case end >= 0:
			yamlStr, body = rest[:end], rest[end+len("\n---\n"):]
		case strings.HasSuffix(rest, "\n---"):
			yamlStr, body = strings.TrimSuffix(rest, "\n---"), ""
		default:
			return frontmatter{}, content, nil
		}
	}
	body = strings.TrimSpace(body)
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
	schema := fmToSkillSchema(fm)
	// Surface the instructions block in the schema so the agent's system
	// prompt can preview it (formatSkillLine shows the first lines of Body).
	schema.Body = body
	return &Skill{
		Schema:   schema,
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
