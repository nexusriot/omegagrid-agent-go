package builtin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	_ "github.com/jackc/pgx/v5/stdlib" // "pgx" database/sql driver
)

func SqlQuerySchema() Skill {
	return Skill{Name: "sql_query", Description: "Run a READ-ONLY SQL query (SELECT / WITH / EXPLAIN / SHOW) against a configured database " +
		"(PostgreSQL or SQLite, named in SKILL_SQL_DATABASES), a SQLite file in the workspace, or workspace CSV / TSV / JSONL files " +
		"(each file becomes a table named after it, so files can be joined). action=tables lists tables and columns; " +
		"action=databases lists the configured names. Writes are refused by the database itself.",
		Parameters: map[string]Param{
			"database": {Type: "string", Description: "Configured name, or a workspace SQLite file (e.g. data/app.db); default: the first configured database", Required: false},
			"files":    {Type: "array", Description: "Workspace CSV/TSV/JSONL files to query instead of a database, e.g. [\"data/orders.csv\",\"data/customers.jsonl\"] → tables orders, customers", Required: false},
			"query":    {Type: "string", Description: "One SQL statement", Required: false},
			"action":   {Type: "string", Description: "query (default) | tables | databases", Required: false},
			"limit":    {Type: "number", Description: "Max rows returned (default 100, max 1000)", Required: false},
		}}
}

// SqlConfig lists the databases sql_query may read.
type SqlConfig struct {
	Named     map[string]string // name → DSN (postgres://… or sqlite:/path)
	Order     []string
	Workspace WorkspaceConfig
	Timeout   time.Duration // per query; default 20s
}

const (
	sqlMaxRows      = 1000
	sqlMaxCellRunes = 2000
	sqlMaxBytes     = 256 << 10
)

// sqlLeadRE is the statement's first keyword (after comments).
var sqlLeadRE = regexp.MustCompile(`(?is)^\s*(?:(?:--[^\n]*\n)|(?:/\*.*?\*/)|\s)*([a-z]+)`)

var sqlReadVerbs = map[string]bool{"select": true, "with": true, "explain": true, "show": true, "values": true, "table": true, "pragma": true, "describe": true}

// checkReadOnlySQL is the first line of defence; the database's own
// read-only mode (query_only / READ ONLY transaction) is the real one.
func checkReadOnlySQL(q string, sqlite bool) error {
	m := sqlLeadRE.FindStringSubmatch(q)
	if m == nil {
		return fmt.Errorf("empty query")
	}
	verb := strings.ToLower(m[1])
	if !sqlReadVerbs[verb] || (verb == "pragma" && !sqlite) {
		return fmt.Errorf("only read-only statements are allowed (SELECT, WITH, EXPLAIN, SHOW, VALUES), not %s", strings.ToUpper(verb))
	}
	if verb == "pragma" && strings.Contains(q, "=") {
		return fmt.Errorf("PRAGMA assignments are not allowed")
	}
	// One statement only: a ";" followed by anything but whitespace/comments.
	if i := strings.Index(stripSQLStrings(q), ";"); i >= 0 {
		rest := strings.TrimSpace(stripSQLStrings(q)[i+1:])
		if rest != "" && !strings.HasPrefix(rest, "--") {
			return fmt.Errorf("only one statement per call")
		}
	}
	return nil
}

// stripSQLStrings blanks out '…' and "…" literals so a ';' inside one does
// not count as a statement separator.
func stripSQLStrings(q string) string {
	b := []byte(q)
	var quote byte
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case quote == 0 && (c == '\'' || c == '"'):
			quote = c
		case quote != 0 && c == quote:
			quote = 0
		case quote != 0:
			b[i] = ' '
		}
	}
	return string(b)
}

type sqlTarget struct {
	label  string
	driver string // "pgx" | "sqlite"
	dsn    string
}

func (c SqlConfig) resolve(name string) (sqlTarget, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		if len(c.Order) == 0 {
			return sqlTarget{}, fmt.Errorf("database is required (none configured in SKILL_SQL_DATABASES; or name a workspace SQLite file)")
		}
		name = c.Order[0]
	}
	if dsn, ok := c.Named[strings.ToLower(name)]; ok {
		return dsnTarget(strings.ToLower(name), dsn)
	}
	// A workspace SQLite file.
	root, err := c.Workspace.open()
	if err != nil {
		return sqlTarget{}, fmt.Errorf("unknown database %q and no workspace: %v", name, err)
	}
	defer root.Close()
	rel, err := relPath(name)
	if err != nil {
		return sqlTarget{}, err
	}
	// Stat through the Root: it refuses paths (and symlinks) leaving it.
	fi, err := root.Stat(rel)
	if err != nil {
		return sqlTarget{}, fmt.Errorf("unknown database %q (not a configured name nor a workspace file)", name)
	}
	if fi.IsDir() {
		return sqlTarget{}, fmt.Errorf("%s is a directory", name)
	}
	abs, err := filepath.Abs(filepath.Join(c.Workspace.Dir, filepath.FromSlash(rel)))
	if err != nil {
		return sqlTarget{}, err
	}
	return sqlTarget{label: rel, driver: "sqlite", dsn: sqliteReadOnlyDSN(abs)}, nil
}

func sqliteReadOnlyDSN(path string) string {
	return "file:" + (&url.URL{Path: path}).EscapedPath() + "?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(3000)"
}

func dsnTarget(label, dsn string) (sqlTarget, error) {
	low := strings.ToLower(dsn)
	switch {
	case strings.HasPrefix(low, "postgres://"), strings.HasPrefix(low, "postgresql://"):
		return sqlTarget{label: label, driver: "pgx", dsn: dsn}, nil
	case strings.HasPrefix(low, "sqlite:"):
		p := strings.TrimPrefix(strings.TrimPrefix(dsn[len("sqlite:"):], "//"), "file:")
		if i := strings.IndexByte(p, '?'); i >= 0 {
			p = p[:i]
		}
		return sqlTarget{label: label, driver: "sqlite", dsn: sqliteReadOnlyDSN(p)}, nil
	}
	return sqlTarget{}, fmt.Errorf("database %q: DSN must start with postgres:// or sqlite: (e.g. sqlite:/data/app.db)", label)
}

func SqlQueryContext(c SqlConfig) ContextExecutor {
	return func(ctx context.Context, args map[string]any) (any, error) {
		action := strings.ToLower(strings.TrimSpace(str(args, "action")))
		if action == "databases" {
			names := append([]string(nil), c.Order...)
			sort.Strings(names)
			out := make([]map[string]any, 0, len(names))
			for _, n := range names {
				t, err := dsnTarget(n, c.Named[n])
				kind := "invalid"
				if err == nil {
					kind = map[string]string{"pgx": "postgres", "sqlite": "sqlite"}[t.driver]
				}
				out = append(out, map[string]any{"name": n, "type": kind})
			}
			return map[string]any{"databases": out, "note": "Workspace SQLite files can also be named by path."}, nil
		}
		files := strList(args, "files")
		if db := strings.TrimSpace(str(args, "database")); len(files) == 0 && isTableFile(db) {
			files = []string{db}
		}
		if len(files) > 0 {
			return c.queryFiles(ctx, action, files, args)
		}
		target, err := c.resolve(str(args, "database"))
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		query := strings.TrimSpace(str(args, "query"))
		switch action {
		case "", "query":
			if query == "" {
				return map[string]any{"error": "query is required"}, nil
			}
		case "tables":
			if target.driver == "sqlite" {
				query = `SELECT m.name AS table_name, p.name AS column_name, p.type AS data_type
FROM sqlite_master m JOIN pragma_table_info(m.name) p
WHERE m.type IN ('table','view') AND m.name NOT LIKE 'sqlite_%' ORDER BY m.name, p.cid`
			} else {
				query = `SELECT table_schema || '.' || table_name AS table_name, column_name, data_type
FROM information_schema.columns WHERE table_schema NOT IN ('pg_catalog','information_schema')
ORDER BY table_schema, table_name, ordinal_position`
			}
		default:
			return map[string]any{"error": "action must be query, tables or databases"}, nil
		}
		if err := checkReadOnlySQL(query, target.driver == "sqlite"); err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		limit := intOr(args, "limit", 100)
		if limit <= 0 || limit > sqlMaxRows {
			limit = 100
		}
		if action == "tables" {
			limit = 5000
		}
		timeout := c.Timeout
		if timeout <= 0 {
			timeout = 20 * time.Second
		}
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		cols, rows, truncated, err := runReadOnly(ctx, target, query, limit, timeout)
		if err != nil {
			return map[string]any{"error": err.Error(), "database": target.label}, nil
		}
		if action == "tables" {
			tables := map[string][]string{}
			var order []string
			for _, r := range rows {
				t := fmt.Sprint(r[0])
				if _, ok := tables[t]; !ok {
					order = append(order, t)
				}
				tables[t] = append(tables[t], fmt.Sprintf("%v %v", r[1], r[2]))
			}
			out := make([]map[string]any, 0, len(order))
			for _, t := range order {
				out = append(out, map[string]any{"table": t, "columns": tables[t]})
			}
			return map[string]any{"database": target.label, "tables": out}, nil
		}
		res := map[string]any{"database": target.label, "columns": cols, "rows": rows, "row_count": len(rows)}
		if truncated {
			res["truncated"] = true
			res["note"] = fmt.Sprintf("only the first %d rows are shown; add LIMIT/aggregates or raise limit", len(rows))
		}
		return res, nil
	}
}

func runReadOnly(ctx context.Context, t sqlTarget, query string, limit int, timeout time.Duration) ([]string, [][]any, bool, error) {
	db, err := sql.Open(t.driver, t.dsn)
	if err != nil {
		return nil, nil, false, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, false, fmt.Errorf("connect: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // read-only; nothing to keep
	if t.driver == "pgx" {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", timeout.Milliseconds())); err != nil {
			return nil, nil, false, err
		}
	}
	rs, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, nil, false, err
	}
	return scanSQLRows(rs, limit)
}

// scanSQLRows reads at most limit rows (and about sqlMaxBytes of JSON) from
// rs and closes it.
func scanSQLRows(rs *sql.Rows, limit int) ([]string, [][]any, bool, error) {
	defer rs.Close()
	cols, err := rs.Columns()
	if err != nil {
		return nil, nil, false, err
	}
	var rows [][]any
	size := 0
	truncated := false
	for rs.Next() {
		if len(rows) >= limit || size > sqlMaxBytes {
			truncated = true
			break
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return nil, nil, false, err
		}
		for i, v := range vals {
			vals[i] = sqlCell(v)
			b, _ := json.Marshal(vals[i])
			size += len(b)
		}
		rows = append(rows, vals)
	}
	if err := rs.Err(); err != nil {
		return nil, nil, false, err
	}
	if rows == nil {
		rows = [][]any{}
	}
	return cols, rows, truncated, nil
}

func sqlCell(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		if utf8.Valid(x) {
			return clipRunes(string(x), sqlMaxCellRunes)
		}
		return fmt.Sprintf("<%d bytes binary>", len(x))
	case string:
		return clipRunes(x, sqlMaxCellRunes)
	case time.Time:
		return x.Format(time.RFC3339Nano)
	case bool, int64, int32, int, float64, float32:
		return x
	default:
		if b, err := json.Marshal(x); err == nil && len(b) < 4096 {
			var out any
			if json.Unmarshal(b, &out) == nil {
				return out
			}
		}
		return clipRunes(fmt.Sprint(x), sqlMaxCellRunes)
	}
}

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// queryFiles runs a query (or lists the tables) over workspace table files.
func (c SqlConfig) queryFiles(ctx context.Context, action string, files []string, args map[string]any) (any, error) {
	if len(files) > 10 {
		return map[string]any{"error": "at most 10 files per query"}, nil
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	db, tables, err := openTableFiles(ctx, c.Workspace, files)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	defer db.Close()
	if action == "tables" {
		return map[string]any{"tables": tables}, nil
	}
	if action != "" && action != "query" {
		return map[string]any{"error": "action must be query or tables"}, nil
	}
	query := strings.TrimSpace(str(args, "query"))
	if query == "" {
		return map[string]any{"error": "query is required", "tables": tables}, nil
	}
	if err := checkReadOnlySQL(query, true); err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	limit := intOr(args, "limit", 100)
	if limit <= 0 || limit > sqlMaxRows {
		limit = 100
	}
	rs, err := db.QueryContext(ctx, query)
	if err != nil {
		// Show the schema with the error: most failures are a wrong column.
		return map[string]any{"error": err.Error(), "tables": tables}, nil
	}
	cols, rows, truncated, err := scanSQLRows(rs, limit)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	names := make([]string, len(tables))
	for i, t := range tables {
		names[i] = t.Table
	}
	res := map[string]any{"tables": names, "columns": cols, "rows": rows, "row_count": len(rows)}
	if truncated {
		res["truncated"] = true
		res["note"] = fmt.Sprintf("only the first %d rows are shown; add LIMIT/aggregates or raise limit", len(rows))
	}
	return res, nil
}
