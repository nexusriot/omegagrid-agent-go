package builtin

import (
	"encoding/json"
	"testing"
)

const jqDoc = `{"items":[{"title":"b","status":"open","n":3,"tags":["x","y"]},{"title":"a","status":"closed","n":1,"tags":[]},{"title":"c","status":"open","n":2,"tags":["y"]}],
"meta":{"total":3,"odd key":"v"}}`

func jq(t *testing.T, query string) any {
	t.Helper()
	m := runExec(t, JsonQuery(), map[string]any{"json": jqDoc, "query": query})
	if e, ok := m["error"]; ok {
		t.Fatalf("%s: %v", query, e)
	}
	if r, ok := m["result"]; ok {
		return r
	}
	return m["results"]
}

func asJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestJsonQuery(t *testing.T) {
	cases := map[string]string{
		".meta.total":      `3`,
		`.meta["odd key"]`: `"v"`,
		`.meta."odd key"`:  `"v"`,
		".items[0].title":  `"b"`,
		".items[-1].title": `"c"`,
		".items[].title":   `["b","a","c"]`,
		".items | length":  `3`,
		`.items[] | select(.status == "open") | .title`:  `["b","c"]`,
		`.items[] | select(.n >= 2) | .n`:                `[3,2]`,
		`.items[] | select(.tags contains "x") | .title`: `"b"`,
		`.items[] | select(.tags | length) | .title`:     `["b","a","c"]`,
		".items | map(.n) | add":                         `6`,
		".items | map(.n) | max":                         `3`,
		".items | sort_by(.title) | map(.title)":         `["a","b","c"]`,
		".items | map(.tags) | flatten | unique":         `["x","y"]`,
		".meta | keys":                                   `["odd key","total"]`,
		".items[1:] | map(.title)":                       `["a","c"]`,
		".items | first | .title":                        `"b"`,
		".missing.deeper":                                `null`,
		".items[0].tags | type":                          `"array"`,
		".":                                              ``,
	}
	for q, want := range cases {
		got := asJSON(jq(t, q))
		if want != "" && got != want {
			t.Errorf("%s = %s, want %s", q, got, want)
		}
	}
	for _, bad := range []string{".items[", "select(", "frobnicate", ".items | map(", ".meta.total | keys", ".items[] | select(.n == foo)"} {
		m := runExec(t, JsonQuery(), map[string]any{"json": jqDoc, "query": bad})
		if m["error"] == nil {
			t.Errorf("%q accepted: %v", bad, m)
		}
	}
	if m := runExec(t, JsonQuery(), map[string]any{"json": "{nope", "query": "."}); m["error"] == nil {
		t.Error("invalid JSON accepted")
	}
	// An already-decoded object works too (models pass objects directly).
	m := runExec(t, JsonQuery(), map[string]any{"json": map[string]any{"a": []any{1.0, 2.0}}, "query": ".a | add"})
	if m["result"] != 3.0 {
		t.Errorf("object input = %v", m)
	}
}
