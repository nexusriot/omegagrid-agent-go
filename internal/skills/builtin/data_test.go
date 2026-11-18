package builtin

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"fmt"
	"image/png"
	"path/filepath"
	"strings"
	"testing"
)

func newWorkspaceDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE users(id INTEGER PRIMARY KEY, name TEXT, created TEXT)`,
		`INSERT INTO users(name, created) VALUES('ann','2026-01-01'),('bob','2026-02-01'),('cy; drop','2026-03-01')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestSqlQueryWorkspaceSQLite(t *testing.T) {
	dir := newWorkspaceDB(t)
	exec := SqlQueryContext(SqlConfig{Workspace: WorkspaceConfig{Dir: dir}})
	m := run(t, exec, map[string]any{"database": "app.db", "query": "SELECT name FROM users WHERE name LIKE '%;%' ORDER BY id"})
	if m["error"] != nil || m["row_count"] != 1 {
		t.Fatalf("%v", m)
	}
	m = run(t, exec, map[string]any{"database": "app.db", "query": "SELECT * FROM users", "limit": 2})
	if m["truncated"] != true || m["row_count"] != 2 {
		t.Fatalf("limit: %v", m)
	}
	tb := run(t, exec, map[string]any{"database": "app.db", "action": "tables"})
	if !strings.Contains(fmtAny(tb["tables"]), "users") {
		t.Fatalf("tables: %v", tb)
	}
	for _, q := range []string{
		"DELETE FROM users",
		"SELECT 1; DELETE FROM users",
		"PRAGMA query_only = 0",
		"WITH x AS (SELECT 1) DELETE FROM users", // passes the keyword check; the DB must refuse it
	} {
		m := run(t, exec, map[string]any{"database": "app.db", "query": q})
		if m["error"] == nil {
			t.Errorf("%q was not refused: %v", q, m)
		}
	}
	m = run(t, exec, map[string]any{"database": "app.db", "query": "SELECT COUNT(*) AS n FROM users"})
	if fmtAny(m["rows"]) != "[[3]]" {
		t.Fatalf("rows changed: %v", m)
	}
	if m := run(t, exec, map[string]any{"database": "../../etc/passwd", "query": "SELECT 1"}); m["error"] == nil {
		t.Fatal("path escape")
	}
}

func TestSqlQueryNamedSQLiteAndDatabases(t *testing.T) {
	dir := newWorkspaceDB(t)
	named, order := ParseNamedURLs([]string{"main=sqlite:" + filepath.Join(dir, "app.db"), "pg=postgres://u:secret@db/x"})
	exec := SqlQueryContext(SqlConfig{Named: named, Order: order})
	m := run(t, exec, map[string]any{"query": "SELECT name FROM users ORDER BY id LIMIT 1"})
	if fmtAny(m["rows"]) != "[[ann]]" {
		t.Fatalf("%v", m)
	}
	d := run(t, exec, map[string]any{"action": "databases"})
	if s := fmtAny(d); strings.Contains(s, "secret") || !strings.Contains(s, "postgres") {
		t.Fatalf("databases leaked DSN or missed type: %s", s)
	}
}

func TestChartRender(t *testing.T) {
	for _, args := range []map[string]any{
		{"type": "bar", "title": "Requests", "labels": []any{"mon", "tue", "wed"}, "values": []any{3.0, -1.0, 8.0}},
		{"type": "line", "labels": "a,b,c,d", "series": []any{
			map[string]any{"name": "p50", "values": []any{1.0, 2.0, nil, 4.0}},
			map[string]any{"name": "p95", "values": "[3,4,5,9]"},
		}, "y_label": "ms", "x_label": "day", "width": 400, "height": 300},
		{"type": "pie", "labels": []any{"a", "b"}, "values": []any{1.0, 3.0}},
	} {
		res, _ := ChartRender()(args)
		m := res.(map[string]any)
		if m["error"] != nil {
			t.Fatalf("%v: %v", args["type"], m["error"])
		}
		raw, _ := base64.StdEncoding.DecodeString(m["image_base64"].(string))
		img, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if img.Bounds().Dx() != m["width"] {
			t.Fatalf("width %d", img.Bounds().Dx())
		}
	}
	for _, bad := range []map[string]any{
		{"labels": []any{"a"}, "values": []any{1.0, 2.0}},
		{"labels": []any{"a"}},
		{"type": "pie", "labels": []any{"a"}, "values": []any{-1.0}},
		{"type": "radar", "labels": []any{"a"}, "values": []any{1.0}},
	} {
		res, _ := ChartRender()(bad)
		if res.(map[string]any)["error"] == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}

func TestYamlQuery(t *testing.T) {
	src := "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: web}\nspec: {replicas: 3}\n---\nkind: Service\nmetadata: {name: web-svc}\n"
	res, _ := YamlQuery(WorkspaceConfig{})(map[string]any{"yaml": src, "query": `.[] | select(.kind == "Deployment") | .spec.replicas`})
	m := res.(map[string]any)
	if m["result"] != 3.0 || m["documents"] != 2 {
		t.Fatalf("%v", m)
	}
	res, _ = YamlQuery(WorkspaceConfig{})(map[string]any{"yaml": "a: {b: [1, 2]}\n", "query": ".a", "output": "yaml"})
	if y := res.(map[string]any)["yaml"]; y != "b:\n  - 1\n  - 2\n" {
		t.Fatalf("yaml out %q", y)
	}
	res, _ = YamlQuery(WorkspaceConfig{})(map[string]any{"yaml": "a: [", "query": "."})
	if res.(map[string]any)["error"] == nil {
		t.Fatal("bad yaml accepted")
	}
}

func fmtAny(v any) string { return fmt.Sprint(v) }
