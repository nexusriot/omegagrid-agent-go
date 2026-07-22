package mcp

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestJSONSchema(t *testing.T) {
	cases := []struct {
		name            string
		param           Param
		wantType        string
		wantRequiredKey bool
	}{
		{"explicit type + required", Param{Type: "number", Description: "a num", Required: true}, "number", true},
		{"empty type defaults to string", Param{Type: "", Description: "d", Required: false}, "string", false},
		{"boolean type not required", Param{Type: "boolean", Description: "flag"}, "boolean", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schema := jsonSchema(map[string]Param{"p": tc.param})

			if schema["type"] != "object" {
				t.Errorf("top-level type = %v, want object", schema["type"])
			}
			props, ok := schema["properties"].(map[string]any)
			if !ok {
				t.Fatalf("properties is %T, want map[string]any", schema["properties"])
			}
			p, ok := props["p"].(map[string]any)
			if !ok {
				t.Fatalf("property p is %T, want map[string]any", props["p"])
			}
			if p["type"] != tc.wantType {
				t.Errorf("property type = %v, want %v", p["type"], tc.wantType)
			}
			if p["description"] != tc.param.Description {
				t.Errorf("property description = %v, want %v", p["description"], tc.param.Description)
			}

			req, has := schema["required"]
			if has != tc.wantRequiredKey {
				// NOTE: the "required" key is omitted entirely when no param is
				// required (rather than being present as an empty list).
				t.Fatalf("required key present = %v, want %v (required=%v)", has, tc.wantRequiredKey, req)
			}
			if tc.wantRequiredKey {
				rs, ok := req.([]string)
				if !ok {
					t.Fatalf("required is %T, want []string", req)
				}
				if len(rs) != 1 || rs[0] != "p" {
					t.Errorf("required = %v, want [p]", rs)
				}
			}
		})
	}

	t.Run("multiple required params populate the list", func(t *testing.T) {
		schema := jsonSchema(map[string]Param{
			"a": {Type: "string", Required: true},
			"b": {Type: "string", Required: true},
			"c": {Type: "string", Required: false},
		})
		req, ok := schema["required"].([]string)
		if !ok {
			t.Fatalf("required is %T, want []string", schema["required"])
		}
		got := map[string]bool{}
		for _, r := range req {
			got[r] = true
		}
		if len(req) != 2 || !got["a"] || !got["b"] {
			t.Errorf("required = %v, want exactly [a b] in some order", req)
		}
	})
}

func TestParamsFromSchema(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want map[string]Param
	}{
		{"empty raw yields nil", "", nil},
		{"invalid json yields nil", "{not valid", nil},
		{
			"basic with required",
			`{"type":"object","properties":{"a":{"type":"number","description":"da"},"b":{"type":"string","description":"db"}},"required":["a"]}`,
			map[string]Param{
				"a": {Type: "number", Description: "da", Required: true},
				"b": {Type: "string", Description: "db", Required: false},
			},
		},
		{
			"missing type defaults to string",
			`{"properties":{"x":{"description":"dx"}}}`,
			map[string]Param{"x": {Type: "string", Description: "dx"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := paramsFromSchema(json.RawMessage(tc.raw))
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("paramsFromSchema(%q) = %#v, want %#v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParamsFromSchemaRoundTrip(t *testing.T) {
	orig := map[string]Param{
		"q": {Type: "string", Description: "query", Required: true},
		"n": {Type: "number", Description: "count", Required: false},
	}
	schema := jsonSchema(orig)
	b, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	got := paramsFromSchema(b)
	if !reflect.DeepEqual(got, orig) {
		t.Errorf("round trip = %#v, want %#v", got, orig)
	}
}

func TestResultText(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"string passthrough", "hello", "hello"},
		{"empty string", "", ""},
		{"string that looks like json passes through unquoted", `{"x":1}`, `{"x":1}`},
		{"int pretty printed", 42, "42"},
		{"bool pretty printed", true, "true"},
		{"map pretty printed", map[string]any{"a": 1}, "{\n  \"a\": 1\n}"},
		{"slice pretty printed", []int{1, 2}, "[\n  1,\n  2\n]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resultText(tc.in); got != tc.want {
				t.Errorf("resultText(%#v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
