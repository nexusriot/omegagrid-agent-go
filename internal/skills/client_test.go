package skills

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nexusriot/omegagrid-agent-go/internal/config"
	"github.com/nexusriot/omegagrid-agent-go/internal/skills/markdown"
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

// Every value skill_creator writes must read back unchanged — including the
// YAML-hostile ones that broke or silently altered the hand-rolled writer
// (a leading "*" in a cron string, a value that looks like a bool, a newline
// that single quotes folded into a space, an embedded "---" line).
func TestRenderMarkdownRoundTripsHostileValues(t *testing.T) {
	values := []string{
		"hello", "", "with space", "*/5 * * * *", "- see docs", "? maybe", ",oops",
		"trailing ", "yes", "42", "has:colon", "a#b", "100%", "a@b", "[x]", "it's",
		"a\nb", "a\n---\nb", "line1\nline2\n", "{{city}}",
	}
	dir := t.TempDir()
	for i, v := range values {
		content, err := renderMarkdown(map[string]any{
			"name":        "rt",
			"description": v + "d", // description must be non-empty
			"parameters":  map[string]any{"p": map[string]any{"type": "string", "description": v}},
		}, "")
		if err != nil {
			t.Fatalf("render %q: %v", v, err)
		}
		path := filepath.Join(dir, fmt.Sprintf("rt%d.md", i))
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		sk, err := markdown.Load(path)
		if err != nil {
			t.Fatalf("value %q: generated file does not load: %v\n%s", v, err, content)
		}
		if sk.Schema.Description != v+"d" || sk.Schema.Parameters["p"].Description != v {
			t.Errorf("value %q came back as description=%q param=%q", v, sk.Schema.Description, sk.Schema.Parameters["p"].Description)
		}
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
		func(_ context.Context, args map[string]any) (any, error) {
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

// Every built-in is registered and reserved: 61 from builtin/ plus
// skill_creator. A skill added to builtin/ but not to registerBuiltins (or
// the reverse) changes this number.
func TestBuiltinInventory(t *testing.T) {
	c, err := New(config.Config{SkillsDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	list, _ := c.List()
	n := 0
	for _, s := range list {
		if s.Kind == "builtin" {
			n++
		}
	}
	if n != 62 {
		t.Fatalf("%d built-in skills registered, want 62", n)
	}
	for _, name := range []string{"rss_fetch", "notify", "traceroute", "json_query", "github", "file_write", "pdf_text",
		"uptime_check", "ssl_labs_grade", "dns_propagation", "calendar_ics", "imap_search", "todo",
		"sql_query", "chart_render", "yaml_query", "prometheus_query", "openapi"} {
		if !c.reg.isReserved(name) {
			t.Errorf("%s is not reserved against markdown skills", name)
		}
	}
}
