package bootstrap

import (
	"reflect"
	"testing"

	"github.com/nexusriot/omegagrid-agent-go/internal/skills"
)

func TestParseMCPEntry(t *testing.T) {
	cases := []struct {
		name        string
		entry       string
		wantName    string
		wantURL     string
		wantHeaders map[string]string
		wantErr     string
	}{
		{
			name:     "url without headers",
			entry:    "gh=https://api.example.com/mcp",
			wantName: "gh",
			wantURL:  "https://api.example.com/mcp",
		},
		{
			name:        "url with authorization header",
			entry:       "gh=https://api.example.com/mcp|Authorization: Bearer TOKEN123",
			wantName:    "gh",
			wantURL:     "https://api.example.com/mcp",
			wantHeaders: map[string]string{"Authorization": "Bearer TOKEN123"},
		},
		{
			name:     "surrounding whitespace is trimmed",
			entry:    "  gh  =  https://h/mcp  ",
			wantName: "gh",
			wantURL:  "https://h/mcp",
		},
		{
			// NOTE: a "|suffix" with no colon is silently dropped (no header,
			// no error) rather than being treated as an invalid entry.
			name:     "header suffix without colon is ignored",
			entry:    "gh=https://h/mcp|nocolon",
			wantName: "gh",
			wantURL:  "https://h/mcp",
		},
		{name: "empty string", entry: "", wantErr: "expected name=url"},
		{name: "missing equals sign", entry: "noequals", wantErr: "expected name=url"},
		{name: "empty name", entry: "=https://h/mcp", wantErr: "expected name=url"},
		{name: "empty url", entry: "gh=", wantErr: "expected name=url"},
		{
			// NOTE: an empty url followed by a header hits the distinct trailing
			// "empty url" branch (different message than the early name=url check),
			// and any parsed header is discarded (headers returned nil).
			name:    "empty url with header suffix",
			entry:   "gh=|Authorization: Bearer X",
			wantErr: "empty url",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, url, headers, err := parseMCPEntry(tc.entry)

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("err = nil, want %q", tc.wantErr)
				}
				if err.Error() != tc.wantErr {
					t.Errorf("err = %q, want %q", err.Error(), tc.wantErr)
				}
				if name != "" || url != "" || headers != nil {
					t.Errorf("on error got name=%q url=%q headers=%v, want all empty/nil", name, url, headers)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if name != tc.wantName {
				t.Errorf("name = %q, want %q", name, tc.wantName)
			}
			if url != tc.wantURL {
				t.Errorf("url = %q, want %q", url, tc.wantURL)
			}
			if !reflect.DeepEqual(headers, tc.wantHeaders) {
				t.Errorf("headers = %v, want %v", headers, tc.wantHeaders)
			}
		})
	}
}

func TestSkillSchemaFromMap(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want skills.Skill
	}{
		{
			name: "full schema with nested parameters",
			in: map[string]any{
				"name":        "web_search",
				"description": "Search the web",
				"parameters": map[string]any{
					"query": map[string]any{"type": "string", "description": "the query", "required": true},
					"limit": map[string]any{"type": "number", "description": "max results"},
				},
			},
			want: skills.Skill{
				Name:        "web_search",
				Description: "Search the web",
				Parameters: map[string]skills.Param{
					"query": {Type: "string", Description: "the query", Required: true},
					"limit": {Type: "number", Description: "max results", Required: false},
				},
			},
		},
		{
			name: "missing name and description default to empty",
			in:   map[string]any{"parameters": map[string]any{}},
			want: skills.Skill{Name: "", Description: "", Parameters: map[string]skills.Param{}},
		},
		{
			// Parameters is always initialised, so it is a non-nil empty map even
			// when the input has no "parameters" key.
			name: "no parameters key yields empty non-nil map",
			in:   map[string]any{"name": "n", "description": "d"},
			want: skills.Skill{Name: "n", Description: "d", Parameters: map[string]skills.Param{}},
		},
		{
			// NOTE: a parameter whose value is not a map[string]any still produces
			// an entry (a zero-valued Param) rather than being skipped.
			name: "non-map parameter value yields a zero Param",
			in:   map[string]any{"name": "n", "parameters": map[string]any{"bad": "notamap"}},
			want: skills.Skill{Name: "n", Description: "", Parameters: map[string]skills.Param{"bad": {}}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SkillSchemaFromMap(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("SkillSchemaFromMap() = %+v, want %+v", got, tc.want)
			}
			if got.Parameters == nil {
				t.Errorf("Parameters is nil, want non-nil map")
			}
		})
	}
}
