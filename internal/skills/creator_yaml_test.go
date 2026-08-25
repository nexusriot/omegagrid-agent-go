package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nexusriot/omegagrid-agent-go/internal/config"
	"github.com/nexusriot/omegagrid-agent-go/internal/skills/markdown"
)

// newCreator returns a client plus the directory skill_creator writes into.
func newCreator(t *testing.T) (*Client, string) {
	t.Helper()
	dir := t.TempDir()
	c, err := New(config.Config{SkillsDir: dir})
	if err != nil {
		t.Fatalf("skills.New: %v", err)
	}
	return c, dir
}

func create(t *testing.T, c *Client, args map[string]any) map[string]any {
	t.Helper()
	args["action"] = "create"
	res, err := c.Execute("skill_creator", args)
	if err != nil {
		t.Fatalf("skill_creator create: %v", err)
	}
	m, _ := res.(map[string]any)
	if msg, ok := m["error"]; ok {
		t.Fatalf("create reported an error: %v", msg)
	}
	return m
}

// The YAML frontmatter is hand-written, so the only real guarantee is that what
// skill_creator writes can be loaded back with its schema intact.
func TestCreatedEndpointSkillRoundTrips(t *testing.T) {
	c, dir := newCreator(t)

	got := create(t, c, map[string]any{
		"name":        "ssl_check",
		"description": "Check a TLS certificate: expiry, issuer & chain",
		"endpoint":    "https://api.example.com/tls?host={{host}}",
		"method":      "get",
		"parameters_schema": map[string]any{
			"host": map[string]any{"type": "string", "description": "Hostname to check", "required": true},
			"port": map[string]any{"type": "number", "description": "Port (default 443)", "required": false},
		},
		"instructions": "Summarise the certificate for the user.",
	})
	if got["type"] != "endpoint" {
		t.Fatalf("type = %v, want endpoint", got["type"])
	}

	s, err := markdown.Load(filepath.Join(dir, "ssl_check.md"))
	if err != nil {
		t.Fatalf("generated file does not load: %v", err)
	}
	if s.Schema.Name != "ssl_check" {
		t.Fatalf("name = %q", s.Schema.Name)
	}
	// A description containing a colon must survive YAML quoting.
	if s.Schema.Description != "Check a TLS certificate: expiry, issuer & chain" {
		t.Fatalf("description = %q", s.Schema.Description)
	}
	if p := s.Schema.Parameters["host"]; p.Type != "string" || !p.Required || p.Description != "Hostname to check" {
		t.Fatalf("host param = %+v", p)
	}
	if p := s.Schema.Parameters["port"]; p.Type != "number" || p.Required {
		t.Fatalf("port param = %+v", p)
	}
	if !strings.Contains(s.Schema.Body, "Summarise the certificate") {
		t.Fatalf("instructions lost: %q", s.Schema.Body)
	}

	// And it is callable immediately, without a restart.
	list, _ := c.List()
	var found bool
	for _, sk := range list {
		if sk.Name == "ssl_check" {
			found = true
		}
	}
	if !found {
		t.Fatal("created skill was not hot-registered")
	}
}

func TestCreatedPipelineSkillRoundTrips(t *testing.T) {
	c, dir := newCreator(t)

	got := create(t, c, map[string]any{
		"name":        "geo_weather",
		"description": "Geocode a city then fetch its weather",
		"parameters_schema": map[string]any{
			"city": map[string]any{"type": "string", "description": "City name", "required": true},
		},
		"steps": []any{
			map[string]any{
				"name":     "geocode",
				"endpoint": "https://geocoding-api.open-meteo.com/v1/search",
				"params":   map[string]any{"name": "{{city}}", "count": "1"},
			},
			map[string]any{
				"name":  "summarise",
				"skill": "datetime_skill",
				"args":  map[string]any{"at": "{{geocode.results.0.name}}"},
			},
		},
	})
	if got["type"] != "pipeline" {
		t.Fatalf("type = %v, want pipeline", got["type"])
	}
	if got["steps_count"] != 2 {
		t.Fatalf("steps_count = %v", got["steps_count"])
	}

	raw, err := os.ReadFile(filepath.Join(dir, "geo_weather.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Placeholders must not be mangled by the YAML quoter.
	for _, want := range []string{"{{city}}", "{{geocode.results.0.name}}", "datetime_skill"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("generated YAML lost %q:\n%s", want, raw)
		}
	}

	s, err := markdown.Load(filepath.Join(dir, "geo_weather.md"))
	if err != nil {
		t.Fatalf("generated pipeline does not load: %v\n%s", err, raw)
	}
	if s.Schema.Name != "geo_weather" {
		t.Fatalf("name = %q", s.Schema.Name)
	}
	if _, ok := s.Schema.Parameters["city"]; !ok {
		t.Fatalf("parameters lost: %v", s.Schema.Parameters)
	}
}

// A prompt-only skill (no endpoint, no steps) tells the model to answer itself.
func TestCreatedPromptOnlySkill(t *testing.T) {
	c, _ := newCreator(t)

	got := create(t, c, map[string]any{
		"name":         "haiku_writer",
		"description":  "Write a haiku about a topic",
		"instructions": "Write a 5-7-5 haiku about {{topic}}.",
		"parameters_schema": map[string]any{
			"topic": map[string]any{"type": "string", "description": "Subject", "required": true},
		},
	})
	if got["type"] != "prompt-only" {
		t.Fatalf("type = %v, want prompt-only", got["type"])
	}

	res, err := c.Execute("haiku_writer", map[string]any{"topic": "go routines"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	m, _ := res.(map[string]any)
	if m["skill_type"] != "prompt_only" {
		t.Fatalf("skill_type = %v", m["skill_type"])
	}
	if !strings.Contains(m["instructions"].(string), "5-7-5") {
		t.Fatalf("instructions = %v", m["instructions"])
	}
	// The directive is what stops the model calling the skill in a loop.
	if !strings.Contains(m["directive"].(string), "Do NOT call this skill again") {
		t.Fatalf("directive = %v", m["directive"])
	}
}

// Descriptions and values full of YAML metacharacters must not corrupt the file.
func TestCreatedSkillEscapesYAMLMetacharacters(t *testing.T) {
	c, dir := newCreator(t)

	nasty := `weird: value #comment [with] {braces} 'quotes' "dquotes" | > & ! % @`
	create(t, c, map[string]any{
		"name":        "tricky",
		"description": nasty,
		"parameters_schema": map[string]any{
			"q": map[string]any{"type": "string", "description": nasty, "required": true},
		},
	})

	s, err := markdown.Load(filepath.Join(dir, "tricky.md"))
	if err != nil {
		raw, _ := os.ReadFile(filepath.Join(dir, "tricky.md"))
		t.Fatalf("file with metacharacters does not load: %v\n%s", err, raw)
	}
	if s.Schema.Description != nasty {
		t.Fatalf("description round-trip failed:\n got %q\nwant %q", s.Schema.Description, nasty)
	}
	if s.Schema.Parameters["q"].Description != nasty {
		t.Fatalf("parameter description round-trip failed: %q", s.Schema.Parameters["q"].Description)
	}
}

// Re-creating a skill overwrites it and reports "updated", so the model can fix
// a skill it just got wrong.
func TestCreateTwiceReportsUpdated(t *testing.T) {
	c, dir := newCreator(t)

	first := create(t, c, map[string]any{"name": "dup", "description": "first version"})
	if first["status"] != "created" {
		t.Fatalf("status = %v, want created", first["status"])
	}

	second := create(t, c, map[string]any{"name": "dup", "description": "second version"})
	if second["status"] != "updated" {
		t.Fatalf("status = %v, want updated", second["status"])
	}

	s, err := markdown.Load(filepath.Join(dir, "dup.md"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Schema.Description != "second version" {
		t.Fatalf("description = %q, want the rewritten one", s.Schema.Description)
	}
}

func TestCreateValidatesInput(t *testing.T) {
	c, _ := newCreator(t)
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"no name", map[string]any{"action": "create", "description": "d"}, "name is required"},
		{"no description", map[string]any{"action": "create", "name": "x_y"}, "description is required"},
		{"bad name", map[string]any{"action": "create", "name": "9bad", "description": "d"}, "invalid skill name"},
		{"name too short", map[string]any{"action": "create", "name": "a", "description": "d"}, "invalid skill name"},
		{"name with dash", map[string]any{"action": "create", "name": "a-b", "description": "d"}, "invalid skill name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := c.Execute("skill_creator", tc.args)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			msg, _ := res.(map[string]any)["error"].(string)
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("error = %q, want it to mention %q", msg, tc.want)
			}
		})
	}
}

func TestSkillCreatorList(t *testing.T) {
	c, dir := newCreator(t)

	// An empty (or missing) skills directory lists nothing rather than erroring.
	res, err := c.Execute("skill_creator", map[string]any{"action": "list"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	m, _ := res.(map[string]any)
	if m["count"] != 0 {
		t.Fatalf("count = %v on an empty dir", m["count"])
	}

	create(t, c, map[string]any{"name": "alpha_skill", "description": "the first one"})
	create(t, c, map[string]any{"name": "beta_skill", "description": "the second one"})
	// A non-markdown file and a bad .md must not break the listing.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore me"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.md"), []byte("no frontmatter here"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err = c.Execute("skill_creator", map[string]any{"action": "list"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	m, _ = res.(map[string]any)
	entries, _ := m["dynamic_skills"].([]map[string]any)
	if len(entries) != 3 {
		t.Fatalf("listed %d entries, want 3 (two good + one flagged): %v", len(entries), entries)
	}
	var good, broken int
	for _, e := range entries {
		if e["error"] != nil {
			broken++
			continue
		}
		good++
		if e["name"] == nil || e["file"] == nil {
			t.Fatalf("entry missing fields: %v", e)
		}
	}
	if good != 2 || broken != 1 {
		t.Fatalf("good=%d broken=%d, want 2/1", good, broken)
	}
}

func TestSkillCreatorUnknownAction(t *testing.T) {
	c, _ := newCreator(t)
	res, err := c.Execute("skill_creator", map[string]any{"action": "explode"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	msg, _ := res.(map[string]any)["error"].(string)
	if !strings.Contains(msg, "unknown action") {
		t.Fatalf("error = %q", msg)
	}
}

func TestSkillCreatorShowMissingFile(t *testing.T) {
	c, _ := newCreator(t)
	res, err := c.Execute("skill_creator", map[string]any{"action": "show", "name": "ghost_skill"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	msg, _ := res.(map[string]any)["error"].(string)
	if !strings.Contains(msg, "not found") {
		t.Fatalf("error = %q", msg)
	}

	res, _ = c.Execute("skill_creator", map[string]any{"action": "delete", "name": "ghost_skill"})
	msg, _ = res.(map[string]any)["error"].(string)
	if !strings.Contains(msg, "not found") {
		t.Fatalf("delete error = %q", msg)
	}
}

// parameters_schema arrives in whatever shape the model felt like emitting.
func TestNormalizeParamsSchemaShapes(t *testing.T) {
	// Shorthand: {"city": "string"}
	got := normalizeParamsSchema(map[string]any{"city": "string"})
	entry, _ := got["city"].(map[string]any)
	if entry["type"] != "string" || entry["required"] != false {
		t.Fatalf("shorthand form = %v", got)
	}

	// Full form is passed through untouched.
	full := map[string]any{"city": map[string]any{"type": "string", "required": true, "description": "d"}}
	got = normalizeParamsSchema(full)
	entry, _ = got["city"].(map[string]any)
	if entry["required"] != true {
		t.Fatalf("full form = %v", got)
	}

	// Anything else degrades to a described string rather than being dropped.
	got = normalizeParamsSchema(map[string]any{"n": 42})
	entry, _ = got["n"].(map[string]any)
	if entry["type"] != "string" {
		t.Fatalf("scalar form = %v", got)
	}

	if normalizeParamsSchema(nil) != nil {
		t.Fatal("nil schema should stay nil")
	}
	if normalizeParamsSchema("not a map") != nil {
		t.Fatal("a non-map schema should be ignored")
	}
}

func TestNormalizeStepsShapes(t *testing.T) {
	got := normalizeSteps([]any{
		map[string]any{"name": "a"},
		"not a step", // silently skipped
		map[string]any{"name": "b"},
	})
	if len(got) != 2 {
		t.Fatalf("steps = %v, want the two usable ones", got)
	}
	if got := normalizeSteps([]map[string]any{{"name": "a"}}); len(got) != 1 {
		t.Fatalf("typed slice = %v", got)
	}
	if normalizeSteps(nil) != nil {
		t.Fatal("nil steps should stay nil")
	}
	if normalizeSteps("nope") != nil {
		t.Fatal("a non-array steps value should be ignored")
	}
}

func TestRegisterAddsRuntimeSkill(t *testing.T) {
	c, _ := newCreator(t)
	c.Register("remote_echo", "[MCP:remote] echo", map[string]Param{
		"text": {Type: "string", Description: "what to echo", Required: true},
	}, func(args map[string]any) (any, error) {
		return map[string]any{"echoed": args["text"]}, nil
	})

	res, err := c.Execute("remote_echo", map[string]any{"text": "hi"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.(map[string]any)["echoed"] != "hi" {
		t.Fatalf("result = %v", res)
	}

	list, _ := c.List()
	for _, s := range list {
		if s.Name == "remote_echo" {
			if !s.Parameters["text"].Required {
				t.Fatalf("registered params lost the required flag: %v", s.Parameters)
			}
			return
		}
	}
	t.Fatal("registered skill missing from List()")
}

// YAML indicator characters in a description or a step argument used to produce
// frontmatter that yaml.v3 refuses to parse ("*" opens an alias, a leading "-"
// a sequence). skill_creator wrote the file anyway, failed to register it, and
// left a permanently unloadable skill behind in SKILLS_DIR.
func TestCreatedSkillWithYAMLIndicatorsRoundTrips(t *testing.T) {
	c, dir := newCreator(t)

	cases := []struct {
		name, description string
	}{
		{"cron_helper", "*/5 * * * * schedule explainer"},
		{"dash_helper", "- see the docs for details"},
		{"question_helper", "? unknown state handler"},
		{"comma_helper", ",leading comma"},
		{"trailing_helper", "trailing space "},
		{"quote_helper", "it's a 'quoted' description"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			create(t, c, map[string]any{
				"name":        tc.name,
				"description": tc.description,
				"endpoint":    "https://example.com/api",
				"parameters_schema": map[string]any{
					"cron": map[string]any{"type": "string", "description": "*/5 * * * *", "required": true},
				},
			})

			loaded, err := markdown.Load(filepath.Join(dir, tc.name+".md"))
			if err != nil {
				raw, _ := os.ReadFile(filepath.Join(dir, tc.name+".md"))
				t.Fatalf("generated frontmatter does not parse: %v\n%s", err, raw)
			}
			// scCreate trims the incoming description, so compare against that.
			want := strings.TrimSpace(tc.description)
			if loaded.Schema.Description != want {
				t.Errorf("description = %q, want %q", loaded.Schema.Description, want)
			}
			if p, ok := loaded.Schema.Parameters["cron"]; !ok {
				t.Errorf("cron parameter lost, got %v", loaded.Schema.Parameters)
			} else if p.Description != "*/5 * * * *" || !p.Required {
				t.Errorf("cron param = %+v, want the asterisk description and required=true", p)
			}

			// The skill must also be callable straight away.
			if _, err := c.Execute(tc.name, map[string]any{}); err != nil {
				t.Errorf("created skill is not registered: %v", err)
			}
		})
	}
}

// A list-valued entry inside parameters_schema was flattened by "%v" into
// "[a b]", which YAML reads back as the single string "a b".
func TestCreatedSkillPreservesListValues(t *testing.T) {
	c, dir := newCreator(t)
	create(t, c, map[string]any{
		"name":        "enum_helper",
		"description": "picks a value",
		"endpoint":    "https://example.com/api",
		"parameters_schema": map[string]any{
			"mode": map[string]any{
				"type": "string",
				"enum": []any{"fast", "slow"},
			},
		},
	})

	raw, err := os.ReadFile(filepath.Join(dir, "enum_helper.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(raw), `["fast","slow"]`) {
		t.Errorf("enum list not preserved in frontmatter:\n%s", raw)
	}
	if _, err := markdown.Load(filepath.Join(dir, "enum_helper.md")); err != nil {
		t.Fatalf("generated frontmatter does not parse: %v\n%s", err, raw)
	}
}
