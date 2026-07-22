package markdown

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

func TestParseFrontmatter(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		wantErr  bool
		wantName string
		wantDesc string
		wantBody string
	}{
		{
			name:     "valid frontmatter and body",
			content:  "---\nname: greeter\ndescription: Greets someone\n---\nSay hello to {{name}}.\n",
			wantName: "greeter",
			wantDesc: "Greets someone",
			wantBody: "Say hello to {{name}}.",
		},
		{
			name:     "missing frontmatter returns whole content as body",
			content:  "Just plain text\nno frontmatter here",
			wantName: "",
			wantDesc: "",
			wantBody: "Just plain text\nno frontmatter here",
		},
		{
			name:     "unterminated frontmatter returns whole content as body, no error",
			content:  "---\nname: foo\ndescription: bar\nbody without closing",
			wantName: "",
			wantDesc: "",
			wantBody: "---\nname: foo\ndescription: bar\nbody without closing",
		},
		{
			name:     "valid delimiters but missing name parses with empty name, no error",
			content:  "---\ndescription: no name\n---\nbody",
			wantName: "",
			wantDesc: "no name",
			wantBody: "body",
		},
		{
			name:    "invalid yaml returns error",
			content: "---\nname: [a, b\n---\nbody",
			wantErr: true,
		},
	}
	// NOTE: parseFrontmatter itself does NOT enforce a required name and does NOT
	// error on unterminated frontmatter (it silently treats the whole input as the
	// body). The missing-name requirement is enforced later in Load.
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fm, body, err := parseFrontmatter(tc.content)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if fm.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", fm.Name, tc.wantName)
			}
			if fm.Description != tc.wantDesc {
				t.Errorf("Description = %q, want %q", fm.Description, tc.wantDesc)
			}
			if body != tc.wantBody {
				t.Errorf("body = %q, want %q", body, tc.wantBody)
			}
		})
	}
}

func TestDotPath(t *testing.T) {
	cases := []struct {
		name string
		obj  any
		path string
		want string
	}{
		{"one level scalar", map[string]any{"temp": 42}, "temp", "42"},
		{"two level string", map[string]any{"a": map[string]any{"b": "hello"}}, "a.b", "hello"},
		{"three level bool", map[string]any{"a": map[string]any{"b": map[string]any{"c": true}}}, "a.b.c", "true"},
		{"missing final key yields <nil>", map[string]any{"a": map[string]any{}}, "a.missing", "<nil>"},
		{"descend into scalar yields empty", map[string]any{"a": "str"}, "a.b", ""},
		{"obj not a map yields empty", "str", "x", ""},
		{"single part map value stringified", map[string]any{"a": map[string]any{"b": 1}}, "a", "map[b:1]"},
	}
	// NOTE: a missing final key returns the string "<nil>" (fmt of a nil value),
	// whereas descending through a non-map intermediate returns "".
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dotPath(tc.obj, tc.path); got != tc.want {
				t.Fatalf("dotPath(%#v, %q) = %q, want %q", tc.obj, tc.path, got, tc.want)
			}
		})
	}
}

func TestResolveValue(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
		ctx    map[string]any
		key    string
		want   string
	}{
		{"param present", map[string]any{"city": "Paris"}, nil, "city", "Paris"},
		{"param non-string stringified", map[string]any{"n": 42}, nil, "n", "42"},
		{"ctx single scalar", nil, map[string]any{"s": "res"}, "s", "res"},
		{"ctx dotted path", nil, map[string]any{"step1": map[string]any{"a": map[string]any{"b": "deep"}}}, "step1.a.b", "deep"},
		{"ctx single map value stringified", nil, map[string]any{"step1": map[string]any{"x": 1}}, "step1", "map[x:1]"},
		{"missing yields passthrough braces", nil, nil, "unknown", "{{unknown}}"},
		{"param takes precedence over ctx", map[string]any{"x": "P"}, map[string]any{"x": "C"}, "x", "P"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveValue(tc.key, tc.params, tc.ctx); got != tc.want {
				t.Fatalf("resolveValue(%q) = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}

func TestResolveStr(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		params map[string]any
		ctx    map[string]any
		want   string
	}{
		{"single placeholder", "Hello {{name}}", map[string]any{"name": "World"}, nil, "Hello World"},
		{"multiple placeholders", "{{a}} and {{b}}", map[string]any{"a": "1", "b": "2"}, nil, "1 and 2"},
		{"dotted from ctx", "Temp is {{s.t}}", nil, map[string]any{"s": map[string]any{"t": 20}}, "Temp is 20"},
		{"unresolved left intact", "{{missing}}", nil, nil, "{{missing}}"},
		{"no placeholder", "plain text", nil, nil, "plain text"},
		{"mixed resolved and unresolved", "{{name}} {{missing}}", map[string]any{"name": "X"}, nil, "X {{missing}}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveStr(tc.text, tc.params, tc.ctx); got != tc.want {
				t.Fatalf("resolveStr(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

func TestResolveObj(t *testing.T) {
	t.Run("nil becomes empty non-nil map", func(t *testing.T) {
		got := resolveObj(nil, nil, nil)
		want := map[string]any{}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
		if got == nil {
			t.Fatal("got nil, want non-nil empty map")
		}
	})
	t.Run("string resolved", func(t *testing.T) {
		got := resolveObj("{{x}}", map[string]any{"x": "v"}, nil)
		if got != "v" {
			t.Fatalf("got %v, want v", got)
		}
	})
	t.Run("map resolved recursively preserving non-strings", func(t *testing.T) {
		got := resolveObj(map[string]any{"greet": "Hi {{n}}", "count": 5}, map[string]any{"n": "Bob"}, nil)
		want := map[string]any{"greet": "Hi Bob", "count": 5}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})
	t.Run("slice resolved recursively", func(t *testing.T) {
		got := resolveObj([]any{"{{a}}", "static", 7}, map[string]any{"a": "X"}, nil)
		want := []any{"X", "static", 7}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})
	t.Run("int passthrough", func(t *testing.T) {
		if got := resolveObj(42, nil, nil); got != 42 {
			t.Fatalf("got %v, want 42", got)
		}
	})
	t.Run("bool passthrough", func(t *testing.T) {
		if got := resolveObj(true, nil, nil); got != true {
			t.Fatalf("got %v, want true", got)
		}
	})
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()

	t.Run("valid prompt-only skill", func(t *testing.T) {
		p := writeFile(t, dir, "greeter.md", "---\nname: greeter\ndescription: Greets someone\n---\nSay hello to {{name}}.\n")
		s, err := Load(p)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if s.Schema.Name != "greeter" {
			t.Errorf("Name = %q, want greeter", s.Schema.Name)
		}
		if s.Schema.Description != "Greets someone" {
			t.Errorf("Description = %q, want Greets someone", s.Schema.Description)
		}
		if s.Schema.Body != "Say hello to {{name}}." {
			t.Errorf("Schema.Body = %q, want %q", s.Schema.Body, "Say hello to {{name}}.")
		}
		if s.endpoint != "" {
			t.Errorf("endpoint = %q, want empty", s.endpoint)
		}
		if s.method != "GET" {
			t.Errorf("method = %q, want GET (default)", s.method)
		}
		if s.timeout != 30 {
			t.Errorf("timeout = %v, want 30 (default)", s.timeout)
		}
		if len(s.steps) != 0 {
			t.Errorf("steps = %v, want none", s.steps)
		}
	})

	t.Run("missing name errors", func(t *testing.T) {
		p := writeFile(t, dir, "noname.md", "---\ndescription: x\n---\nbody")
		_, err := Load(p)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "missing name") {
			t.Fatalf("error = %q, want it to mention missing name", err.Error())
		}
	})

	t.Run("nonexistent file errors", func(t *testing.T) {
		_, err := Load(filepath.Join(dir, "does-not-exist.md"))
		if err == nil {
			t.Fatal("expected error for missing file, got nil")
		}
	})

	t.Run("description defaults from filename", func(t *testing.T) {
		p := writeFile(t, dir, "nodesc.md", "---\nname: nodesc\n---\nbody")
		s, err := Load(p)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if s.Schema.Description != "Skill from nodesc.md" {
			t.Fatalf("Description = %q, want %q", s.Schema.Description, "Skill from nodesc.md")
		}
	})

	t.Run("endpoint method uppercased and timeout parsed", func(t *testing.T) {
		p := writeFile(t, dir, "api.md", "---\nname: api\ndescription: d\nendpoint: http://example.invalid/x\nmethod: post\ntimeout: 5\n---\nbody")
		s, err := Load(p)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if s.endpoint != "http://example.invalid/x" {
			t.Errorf("endpoint = %q", s.endpoint)
		}
		if s.method != "POST" {
			t.Errorf("method = %q, want POST", s.method)
		}
		if s.timeout != 5 {
			t.Errorf("timeout = %v, want 5", s.timeout)
		}
	})
}

func TestLoadDir(t *testing.T) {
	t.Run("missing dir returns nil nil", func(t *testing.T) {
		got, err := LoadDir(filepath.Join(t.TempDir(), "does-not-exist"))
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if got != nil {
			t.Fatalf("got %v, want nil", got)
		}
	})

	t.Run("skips non-md, invalid files and subdirs; loads valid", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "valid1.md", "---\nname: alpha\ndescription: A\n---\nbody a")
		writeFile(t, dir, "valid2.md", "---\nname: beta\ndescription: B\n---\nbody b")
		writeFile(t, dir, "note.txt", "not a skill file")
		writeFile(t, dir, "bad.md", "---\ndescription: no name here\n---\nbody")
		if err := os.Mkdir(filepath.Join(dir, "nested"), 0o755); err != nil {
			t.Fatal(err)
		}

		got, err := LoadDir(dir)
		if err != nil {
			t.Fatalf("LoadDir: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("loaded %d skills, want 2", len(got))
		}
		names := map[string]bool{}
		for _, s := range got {
			names[s.Schema.Name] = true
		}
		if !names["alpha"] || !names["beta"] {
			t.Fatalf("loaded names = %v, want alpha and beta", names)
		}
	})

	t.Run("empty dir returns no skills", func(t *testing.T) {
		got, err := LoadDir(t.TempDir())
		if err != nil {
			t.Fatalf("LoadDir: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("loaded %d skills, want 0", len(got))
		}
	})
}

func TestSkillExecutePromptOnly(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "greeter.md", "---\nname: greeter\ndescription: Greets someone\n---\nSay hello to {{name}}.\n")
	s, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	args := map[string]any{"name": "Bob"}
	res, err := s.Execute(args, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map[string]any", res)
	}
	if m["skill_type"] != "prompt_only" {
		t.Errorf("skill_type = %v, want prompt_only", m["skill_type"])
	}
	if m["instructions"] != "Say hello to {{name}}." {
		t.Errorf("instructions = %v (placeholders are NOT resolved for prompt-only)", m["instructions"])
	}
	if !reflect.DeepEqual(m["parameters"], args) {
		t.Errorf("parameters = %#v, want %#v", m["parameters"], args)
	}
	if d, _ := m["directive"].(string); d == "" {
		t.Error("directive should be a non-empty string")
	}
}
