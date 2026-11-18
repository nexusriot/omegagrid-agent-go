package builtin

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"unicode"
)

// SQL over workspace files: sql_query loads CSV / TSV / JSONL files into an
// in-memory SQLite database, one table per file, so the model can join,
// group and filter them with ordinary SQL. csv_query stays for one-table
// filters that need no SQL.

const (
	sqlFileMaxBytes = 64 << 20 // per file
	sqlFileMaxRows  = 500_000  // all files together
	sqlFileMaxCols  = 500
)

// isTableFile reports whether p names a file sql_query can load as a table.
func isTableFile(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".csv", ".tsv", ".jsonl", ".ndjson":
		return true
	}
	return false
}

// loadedTable describes one file loaded as a table.
type loadedTable struct {
	Table   string   `json:"table"`
	File    string   `json:"file"`
	Columns []string `json:"columns"` // "name TYPE"
	Rows    int      `json:"rows"`
}

// tableName turns "data/Sales 2025.csv" into "sales_2025".
func tableName(file string, taken map[string]bool) string {
	stem := strings.TrimSuffix(path.Base(file), path.Ext(file))
	var b strings.Builder
	for _, r := range strings.ToLower(stem) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	name := strings.Trim(b.String(), "_")
	if name == "" || unicode.IsDigit(rune(name[0])) {
		name = "t_" + name
	}
	base := name
	for i := 2; taken[name]; i++ {
		name = fmt.Sprintf("%s_%d", base, i)
	}
	taken[name] = true
	return name
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// openTableFiles loads files into a fresh in-memory SQLite database and
// returns it switched to query_only. The caller closes it.
func openTableFiles(ctx context.Context, ws WorkspaceConfig, files []string) (*sql.DB, []loadedTable, error) {
	root, err := ws.open()
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()

	var id [8]byte
	_, _ = rand.Read(id[:])
	// A named shared-cache memory database lives as long as one connection
	// to it does; MaxOpenConns(1) keeps exactly that connection.
	db, err := sql.Open("sqlite", "file:sqlq"+hex.EncodeToString(id[:])+"?mode=memory&cache=shared")
	if err != nil {
		return nil, nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(err error) (*sql.DB, []loadedTable, error) {
		_ = db.Close()
		return nil, nil, err
	}

	taken := map[string]bool{}
	var tables []loadedTable
	total := 0
	for _, f := range files {
		rel, err := relPath(f)
		if err != nil {
			return fail(err)
		}
		if !isTableFile(rel) {
			return fail(fmt.Errorf("%s: only .csv, .tsv, .jsonl and .ndjson files can be queried", f))
		}
		fi, err := root.Stat(rel)
		if err != nil {
			return fail(fmt.Errorf("%s: %v", f, err))
		}
		if fi.Size() > sqlFileMaxBytes {
			return fail(fmt.Errorf("%s is %d MB; the limit is %d MB", f, fi.Size()>>20, sqlFileMaxBytes>>20))
		}
		fh, err := root.Open(rel)
		if err != nil {
			return fail(err)
		}
		var header []string
		var rows [][]any
		if ext := strings.ToLower(path.Ext(rel)); ext == ".jsonl" || ext == ".ndjson" {
			header, rows, err = readJSONLines(fh, sqlFileMaxRows-total)
		} else {
			header, rows, err = readDelimited(fh, ext == ".tsv", sqlFileMaxRows-total)
		}
		fh.Close()
		if err != nil {
			return fail(fmt.Errorf("%s: %v", f, err))
		}
		total += len(rows)
		t := loadedTable{Table: tableName(rel, taken), File: rel, Rows: len(rows)}
		cols, err := createAndFill(ctx, db, t.Table, header, rows)
		if err != nil {
			return fail(fmt.Errorf("%s: %v", f, err))
		}
		t.Columns = cols
		tables = append(tables, t)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA query_only = 1"); err != nil {
		return fail(err)
	}
	return db, tables, nil
}

// readDelimited reads a CSV/TSV file with a header row; values stay strings
// ("" becomes NULL) and are typed per column by createAndFill.
func readDelimited(r io.Reader, tsv bool, maxRows int) ([]string, [][]any, error) {
	br := bufio.NewReader(r)
	if b, _ := br.Peek(3); string(b) == "\ufeff" {
		_, _ = br.Discard(3)
	}
	cr := csv.NewReader(br)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	if tsv {
		cr.Comma = '\t'
	} else if b, _ := br.Peek(4096); detectSemicolon(string(b)) {
		cr.Comma = ';'
	}
	header, err := cr.Read()
	if err != nil {
		return nil, nil, fmt.Errorf("header: %v", err)
	}
	if len(header) > sqlFileMaxCols {
		return nil, nil, fmt.Errorf("more than %d columns", sqlFileMaxCols)
	}
	var rows [][]any
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		if len(rows) >= maxRows {
			return nil, nil, fmt.Errorf("more than %d rows in total", sqlFileMaxRows)
		}
		row := make([]any, len(header))
		for i := range header {
			if i < len(rec) && rec[i] != "" {
				row[i] = rec[i]
			}
		}
		rows = append(rows, row)
	}
	return header, rows, nil
}

// detectSemicolon is true for a European-style CSV whose header has more ";"
// than "," separators.
func detectSemicolon(head string) bool {
	line, _, _ := strings.Cut(head, "\n")
	return strings.Count(line, ";") > strings.Count(line, ",")
}

// readJSONLines reads one JSON object per line; the columns are the union
// of keys in first-seen order, nested values are stored as JSON text.
func readJSONLines(r io.Reader, maxRows int) ([]string, [][]any, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	idx := map[string]int{}
	var header []string
	var objs []map[string]any
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		var m map[string]any
		dec := json.NewDecoder(strings.NewReader(text))
		dec.UseNumber()
		if err := dec.Decode(&m); err != nil {
			return nil, nil, fmt.Errorf("line %d: not a JSON object", line)
		}
		if len(objs) >= maxRows {
			return nil, nil, fmt.Errorf("more than %d rows in total", sqlFileMaxRows)
		}
		for k := range m {
			if _, ok := idx[k]; !ok {
				if len(header) >= sqlFileMaxCols {
					return nil, nil, fmt.Errorf("more than %d distinct keys", sqlFileMaxCols)
				}
				idx[k] = len(header)
				header = append(header, k)
			}
		}
		objs = append(objs, m)
	}
	if err := sc.Err(); err != nil {
		return nil, nil, err
	}
	if len(header) == 0 {
		return nil, nil, fmt.Errorf("no JSON objects found")
	}
	rows := make([][]any, len(objs))
	for i, m := range objs {
		row := make([]any, len(header))
		for k, v := range m {
			switch x := v.(type) {
			case nil:
			case string:
				row[idx[k]] = x
			case json.Number:
				row[idx[k]] = x.String()
			case bool:
				if x {
					row[idx[k]] = "1"
				} else {
					row[idx[k]] = "0"
				}
			default:
				b, _ := json.Marshal(x)
				row[idx[k]] = string(b)
			}
		}
		rows[i] = row
	}
	return header, rows, nil
}

// createAndFill creates the table with a type per column (INTEGER when every
// value is an integer, REAL when every value is a number, else TEXT) and
// inserts the rows. It returns the "name TYPE" column list.
func createAndFill(ctx context.Context, db *sql.DB, table string, header []string, rows [][]any) ([]string, error) {
	names := make([]string, len(header))
	seen := map[string]bool{}
	for i, h := range header {
		n := strings.TrimSpace(h)
		if n == "" {
			n = fmt.Sprintf("col%d", i+1)
		}
		base := n
		for j := 2; seen[strings.ToLower(n)]; j++ {
			n = fmt.Sprintf("%s_%d", base, j)
		}
		seen[strings.ToLower(n)] = true
		names[i] = n
	}
	types := make([]string, len(names))
	for c := range names {
		isInt, isNum, has := true, true, false
		for _, r := range rows {
			s, ok := r[c].(string)
			if !ok {
				continue
			}
			has = true
			s = strings.TrimSpace(s)
			if _, err := strconv.ParseInt(s, 10, 64); err != nil {
				isInt = false
				if _, err := strconv.ParseFloat(s, 64); err != nil {
					isNum = false
					break
				}
			}
		}
		switch {
		case has && isInt:
			types[c] = "INTEGER"
		case has && isNum:
			types[c] = "REAL"
		default:
			types[c] = "TEXT"
		}
	}
	defs := make([]string, len(names))
	cols := make([]string, len(names))
	for i, n := range names {
		defs[i] = quoteIdent(n) + " " + types[i]
		cols[i] = n + " " + types[i]
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+quoteIdent(table)+" ("+strings.Join(defs, ", ")+")"); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return cols, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // committed below on success
	ph := strings.TrimSuffix(strings.Repeat("?,", len(names)), ",")
	stmt, err := tx.PrepareContext(ctx, "INSERT INTO "+quoteIdent(table)+" VALUES ("+ph+")")
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	for _, r := range rows {
		vals := make([]any, len(r))
		for i, v := range r {
			s, ok := v.(string)
			if !ok {
				continue
			}
			switch types[i] {
			case "INTEGER":
				vals[i], _ = strconv.ParseInt(strings.TrimSpace(s), 10, 64)
			case "REAL":
				vals[i], _ = strconv.ParseFloat(strings.TrimSpace(s), 64)
			default:
				vals[i] = s
			}
		}
		if _, err := stmt.ExecContext(ctx, vals...); err != nil {
			return nil, err
		}
	}
	return cols, tx.Commit()
}
