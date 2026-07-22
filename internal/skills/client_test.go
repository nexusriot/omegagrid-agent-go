package skills

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nexusriot/omegagrid-agent-go/internal/config"
)

func TestSafeNameChars(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"two chars ok", "ab", true},
		{"single char too short", "a", false},
		{"empty too short", "", false},
		{"valid with digits and underscore", "my_skill_2", true},
		{"uppercase accepted", "MySkill", true},
		{"leading digit rejected", "9abc", false},
		{"leading underscore rejected", "_abc", false},
		{"hyphen rejected", "a-b", false},
		{"space rejected", "a b", false},
		{"punctuation rejected", "ab!", false},
		{"length 49 ok", "a" + strings.Repeat("b", 48), true},
		{"length 50 rejected", "a" + strings.Repeat("b", 49), false},
	}
	// NOTE: safeNameChars accepts uppercase letters even though the skill_creator
	// schema text advertises "lowercase, underscores". The validator only enforces
	// [a-zA-Z][a-zA-Z0-9_]* and length 2-49.
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := safeNameChars(tc.in); got != tc.want {
				t.Fatalf("safeNameChars(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestYamlQuote(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain unquoted", "hello", "hello"},
		{"empty unquoted", "", ""},
		{"space not special", "with space", "with space"},
		{"colon quoted", "has:colon", "'has:colon'"},
		{"hash quoted", "a#b", "'a#b'"},
		{"percent quoted", "100%", "'100%'"},
		{"at quoted", "a@b", "'a@b'"},
		{"bracket quoted", "[x]", "'[x]'"},
		{"single quote doubled", "it's", "'it''s'"},
		{"newline quoted", "a\nb", "'a\nb'"},
	}
	// NOTE: a plain space is NOT in yamlQuote's special-char set, so "with space"
	// is returned unquoted.
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := yamlQuote(tc.in); got != tc.want {
				t.Fatalf("yamlQuote(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeParamsSchema(t *testing.T) {
	t.Run("nil returns nil", func(t *testing.T) {
		if got := normalizeParamsSchema(nil); got != nil {
			t.Fatalf("got %v, want nil", got)
		}
	})
	t.Run("string value expands to full param", func(t *testing.T) {
		got := normalizeParamsSchema(map[string]any{"city": "string"})
		want := map[string]any{"city": map[string]any{"type": "string", "description": "city", "required": false}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})
	t.Run("map value passed through", func(t *testing.T) {
		inner := map[string]any{"type": "int", "required": true}
		got := normalizeParamsSchema(map[string]any{"n": inner})
		want := map[string]any{"n": inner}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})
	t.Run("other value stringified", func(t *testing.T) {
		got := normalizeParamsSchema(map[string]any{"x": 42})
		want := map[string]any{"x": map[string]any{"type": "string", "description": "42", "required": false}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})
	t.Run("non-map raw returns nil", func(t *testing.T) {
		if got := normalizeParamsSchema("not a map"); got != nil {
			t.Fatalf("string raw: got %v, want nil", got)
		}
		if got := normalizeParamsSchema([]any{1, 2}); got != nil {
			t.Fatalf("slice raw: got %v, want nil", got)
		}
	})
}

func TestNormalizeSteps(t *testing.T) {
	t.Run("nil returns nil", func(t *testing.T) {
		if got := normalizeSteps(nil); got != nil {
			t.Fatalf("got %v, want nil", got)
		}
	})
	t.Run("filters non-map items from []any", func(t *testing.T) {
		raw := []any{
			map[string]any{"name": "s1"},
			"not a map",
			map[string]any{"name": "s2"},
		}
		got := normalizeSteps(raw)
		want := []map[string]any{{"name": "s1"}, {"name": "s2"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})
	t.Run("[]map[string]any passed through", func(t *testing.T) {
		raw := []map[string]any{{"a": "b"}}
		got := normalizeSteps(raw)
		if !reflect.DeepEqual(got, raw) {
			t.Fatalf("got %#v, want %#v", got, raw)
		}
	})
	t.Run("non-slice returns nil", func(t *testing.T) {
		if got := normalizeSteps("x"); got != nil {
			t.Fatalf("string raw: got %v, want nil", got)
		}
		if got := normalizeSteps(map[string]any{"a": 1}); got != nil {
			t.Fatalf("map raw: got %v, want nil", got)
		}
	})
}

func TestClientNewListExecute(t *testing.T) {
	dir := t.TempDir()
	content := "---\nname: mytool\ndescription: My custom tool\n---\nDo the thing with {{input}}.\n"
	if err := os.WriteFile(filepath.Join(dir, "mytool.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	c, err := New(config.Config{SkillsDir: dir, SkillHTTPTimeout: 30})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	skills, err := c.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	names := map[string]bool{}
	for _, s := range skills {
		names[s.Name] = true
	}
	for _, want := range []string{"mytool", "skill_creator", "weather", "http_request"} {
		if !names[want] {
			t.Fatalf("List missing %q; got %v", want, names)
		}
	}

	res, err := c.Execute("mytool", map[string]any{"input": "x"})
	if err != nil {
		t.Fatalf("Execute mytool: %v", err)
	}
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("Execute result type = %T, want map[string]any", res)
	}
	if m["skill_type"] != "prompt_only" {
		t.Fatalf("skill_type = %v, want prompt_only", m["skill_type"])
	}

	_, err = c.Execute("does_not_exist", nil)
	if err == nil {
		t.Fatal("expected error for missing skill")
	}
	var nf *errNotFound
	if !errors.As(err, &nf) {
		t.Fatalf("got %T, want *errNotFound", err)
	}
}

func TestClientRegister(t *testing.T) {
	dir := t.TempDir()
	c, err := New(config.Config{SkillsDir: dir, SkillHTTPTimeout: 30})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	called := false
	c.Register("custom_tool", "desc", map[string]Param{"a": {Type: "string", Required: true}},
		func(args map[string]any) (any, error) {
			called = true
			return args["a"], nil
		})

	got, err := c.Execute("custom_tool", map[string]any{"a": "ok"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !called {
		t.Fatal("registered executor was not called")
	}
	if got != "ok" {
		t.Fatalf("got %v, want ok", got)
	}
}
