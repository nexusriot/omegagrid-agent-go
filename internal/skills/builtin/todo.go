package builtin

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func TodoSchema() Skill {
	return Skill{Name: "todo", Description: "A persistent to-do list: add, list, complete, reopen, update and delete items with optional due date, " +
		"priority and tags. list shows open items by default, overdue first. For a reminder at a time, also use schedule_task.",
		Parameters: map[string]Param{
			"action":   {Type: "string", Description: "add | list | done | reopen | update | delete | clear_done", Required: true},
			"text":     {Type: "string", Description: "Item text (add/update)", Required: false},
			"id":       {Type: "number", Description: "Item id (done/reopen/update/delete)", Required: false},
			"due":      {Type: "string", Description: "Due date: 2026-11-20, 2026-11-20 15:00, RFC3339, today, tomorrow, +3d, +2h, or 'none' to clear (add/update)", Required: false},
			"priority": {Type: "string", Description: "low | normal | high (add/update)", Required: false},
			"tags":     {Type: "array", Description: "Tags (add/update); for list, items with this tag", Required: false},
			"status":   {Type: "string", Description: "list: open (default) | done | all | overdue | today", Required: false},
			"query":    {Type: "string", Description: "list: only items whose text contains this", Required: false},
			"timezone": {Type: "string", Description: "IANA zone for parsing and showing due dates (default UTC)", Required: false},
			"limit":    {Type: "number", Description: "list: max items (default 50, max 500)", Required: false},
		}}
}

const todoSchema = `
CREATE TABLE IF NOT EXISTS todos (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  text TEXT NOT NULL,
  due INTEGER,
  priority INTEGER NOT NULL DEFAULT 1,
  tags TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  done_at INTEGER
);`

var priorityNames = []string{"low", "normal", "high"}

func parsePriority(s string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "normal", "medium", "1":
		return 1, nil
	case "low", "0":
		return 0, nil
	case "high", "urgent", "2":
		return 2, nil
	}
	return 0, fmt.Errorf("priority must be low, normal or high")
}

var relDueRE = regexp.MustCompile(`^\+?\s*(\d+)\s*(m|min|h|d|w)$`)

// parseDue reads a due date in loc. clear reports an explicit "none".
func parseDue(s string, loc *time.Location, now time.Time) (t time.Time, clear bool, err error) {
	s = strings.TrimSpace(strings.ToLower(s))
	now = now.In(loc)
	day := func(d time.Time) time.Time { return time.Date(d.Year(), d.Month(), d.Day(), 23, 59, 0, 0, loc) }
	switch s {
	case "":
		return time.Time{}, false, nil
	case "none", "clear", "null":
		return time.Time{}, true, nil
	case "today":
		return day(now), false, nil
	case "tomorrow":
		return day(now.AddDate(0, 0, 1)), false, nil
	}
	if m := relDueRE.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		switch m[2] {
		case "m", "min":
			return now.Add(time.Duration(n) * time.Minute), false, nil
		case "h":
			return now.Add(time.Duration(n) * time.Hour), false, nil
		case "d":
			return day(now.AddDate(0, 0, n)), false, nil
		case "w":
			return day(now.AddDate(0, 0, 7*n)), false, nil
		}
	}
	if t, err := time.Parse(time.RFC3339, strings.ToUpper(s)); err == nil {
		return t, false, nil
	}
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02t15:04", "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, false, nil
		}
	}
	if t, err := time.ParseInLocation("2006-01-02", s, loc); err == nil {
		return day(t), false, nil
	}
	return time.Time{}, false, fmt.Errorf("cannot read due date %q (use 2026-11-20, 2026-11-20 15:00, today, tomorrow, +3d)", s)
}

func normTags(args map[string]any) string {
	tags := strList(args, "tags")
	for i, t := range tags {
		tags[i] = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(t), "#"))
	}
	if len(tags) == 0 {
		return ""
	}
	// Stored as ",a,b," so a LIKE '%,tag,%' matches whole tags only.
	return "," + strings.Join(tags, ",") + ","
}

func TodoContext(db StateDB) ContextExecutor {
	return func(ctx context.Context, args map[string]any) (any, error) {
		conn, err := db.open(todoSchema)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		loc := time.UTC
		if tz := strings.TrimSpace(str(args, "timezone")); tz != "" {
			if loc, err = time.LoadLocation(tz); err != nil {
				return map[string]any{"error": "unknown timezone " + tz}, nil
			}
		}
		now := time.Now()
		id := intOr(args, "id", 0)
		switch action := strings.ToLower(strings.TrimSpace(str(args, "action"))); action {
		case "add":
			text := strings.TrimSpace(str(args, "text"))
			if text == "" {
				return map[string]any{"error": "text is required"}, nil
			}
			if len(text) > 2000 {
				return map[string]any{"error": "text is longer than 2000 characters"}, nil
			}
			prio, err := parsePriority(str(args, "priority"))
			if err != nil {
				return map[string]any{"error": err.Error()}, nil
			}
			due, _, err := parseDue(str(args, "due"), loc, now)
			if err != nil {
				return map[string]any{"error": err.Error()}, nil
			}
			var dueVal any
			if !due.IsZero() {
				dueVal = due.Unix()
			}
			res, err := conn.ExecContext(ctx, `INSERT INTO todos(text, due, priority, tags, created_at) VALUES(?,?,?,?,?)`,
				text, dueVal, prio, normTags(args), now.Unix())
			if err != nil {
				return map[string]any{"error": err.Error()}, nil
			}
			newID, _ := res.LastInsertId()
			item, _ := todoGet(ctx, conn, newID, loc)
			return map[string]any{"added": item}, nil

		case "list", "":
			return todoList(ctx, conn, args, loc, now)

		case "done", "reopen":
			if id <= 0 {
				return map[string]any{"error": "id is required"}, nil
			}
			var doneAt any
			if action == "done" {
				doneAt = now.Unix()
			}
			return todoExec(ctx, conn, loc, int64(id), `UPDATE todos SET done_at = ? WHERE id = ?`, doneAt, id)

		case "update":
			if id <= 0 {
				return map[string]any{"error": "id is required"}, nil
			}
			var sets []string
			var vals []any
			if text := strings.TrimSpace(str(args, "text")); text != "" {
				sets, vals = append(sets, "text = ?"), append(vals, text)
			}
			if p := str(args, "priority"); p != "" {
				prio, err := parsePriority(p)
				if err != nil {
					return map[string]any{"error": err.Error()}, nil
				}
				sets, vals = append(sets, "priority = ?"), append(vals, prio)
			}
			if d := str(args, "due"); d != "" {
				due, clear, err := parseDue(d, loc, now)
				if err != nil {
					return map[string]any{"error": err.Error()}, nil
				}
				if clear {
					sets = append(sets, "due = NULL")
				} else {
					sets, vals = append(sets, "due = ?"), append(vals, due.Unix())
				}
			}
			if _, ok := args["tags"]; ok {
				sets, vals = append(sets, "tags = ?"), append(vals, normTags(args))
			}
			if len(sets) == 0 {
				return map[string]any{"error": "nothing to update (give text, due, priority or tags)"}, nil
			}
			vals = append(vals, id)
			return todoExec(ctx, conn, loc, int64(id), `UPDATE todos SET `+strings.Join(sets, ", ")+` WHERE id = ?`, vals...)

		case "delete":
			if id <= 0 {
				return map[string]any{"error": "id is required"}, nil
			}
			item, err := todoGet(ctx, conn, int64(id), loc)
			if err != nil {
				return map[string]any{"error": err.Error()}, nil
			}
			if _, err := conn.ExecContext(ctx, `DELETE FROM todos WHERE id = ?`, id); err != nil {
				return map[string]any{"error": err.Error()}, nil
			}
			return map[string]any{"deleted": item}, nil

		case "clear_done":
			res, err := conn.ExecContext(ctx, `DELETE FROM todos WHERE done_at IS NOT NULL`)
			if err != nil {
				return map[string]any{"error": err.Error()}, nil
			}
			n, _ := res.RowsAffected()
			return map[string]any{"deleted": n}, nil
		default:
			return map[string]any{"error": "action must be add, list, done, reopen, update, delete or clear_done"}, nil
		}
	}
}

func todoExec(ctx context.Context, conn *sql.DB, loc *time.Location, id int64, q string, vals ...any) (any, error) {
	res, err := conn.ExecContext(ctx, q, vals...)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return map[string]any{"error": fmt.Sprintf("no item with id %d", id)}, nil
	}
	item, err := todoGet(ctx, conn, id, loc)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	return map[string]any{"item": item}, nil
}

const todoCols = `id, text, due, priority, tags, created_at, done_at`

func todoGet(ctx context.Context, conn *sql.DB, id int64, loc *time.Location) (map[string]any, error) {
	rows, err := conn.QueryContext(ctx, `SELECT `+todoCols+` FROM todos WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanTodos(rows, loc, time.Now())
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("no item with id %d", id)
	}
	return items[0], nil
}

func scanTodos(rows *sql.Rows, loc *time.Location, now time.Time) ([]map[string]any, error) {
	var out []map[string]any
	for rows.Next() {
		var (
			id, created, prio int64
			text, tags        string
			due, done         sql.NullInt64
		)
		if err := rows.Scan(&id, &text, &due, &prio, &tags, &created, &done); err != nil {
			return nil, err
		}
		item := map[string]any{"id": id, "text": text, "priority": priorityNames[min(max(prio, 0), 2)],
			"created_at": time.Unix(created, 0).In(loc).Format(time.RFC3339), "done": done.Valid}
		if t := strings.Trim(tags, ","); t != "" {
			item["tags"] = strings.Split(t, ",")
		}
		if due.Valid {
			d := time.Unix(due.Int64, 0).In(loc)
			item["due"] = d.Format(time.RFC3339)
			if !done.Valid && d.Before(now) {
				item["overdue"] = true
			}
		}
		if done.Valid {
			item["done_at"] = time.Unix(done.Int64, 0).In(loc).Format(time.RFC3339)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func todoList(ctx context.Context, conn *sql.DB, args map[string]any, loc *time.Location, now time.Time) (any, error) {
	var where []string
	var vals []any
	status := strings.ToLower(strings.TrimSpace(str(args, "status")))
	switch status {
	case "", "open":
		where = append(where, "done_at IS NULL")
	case "done":
		where = append(where, "done_at IS NOT NULL")
	case "all":
	case "overdue":
		where, vals = append(where, "done_at IS NULL AND due IS NOT NULL AND due < ?"), append(vals, now.Unix())
	case "today":
		n := now.In(loc)
		end := time.Date(n.Year(), n.Month(), n.Day(), 23, 59, 59, 0, loc)
		where, vals = append(where, "done_at IS NULL AND due IS NOT NULL AND due <= ?"), append(vals, end.Unix())
	default:
		return map[string]any{"error": "status must be open, done, all, overdue or today"}, nil
	}
	if q := strings.TrimSpace(str(args, "query")); q != "" {
		where, vals = append(where, "text LIKE ? ESCAPE '\\'"), append(vals, "%"+likeEscape(q)+"%")
	}
	for _, t := range strList(args, "tags") {
		t = strings.ToLower(strings.TrimPrefix(t, "#"))
		where, vals = append(where, "tags LIKE ? ESCAPE '\\'"), append(vals, "%,"+likeEscape(t)+",%")
	}
	limit := intOr(args, "limit", 50)
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	q := `SELECT ` + todoCols + ` FROM todos`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	// Open items: overdue/soonest due first, then priority, then newest.
	q += ` ORDER BY (done_at IS NOT NULL), (due IS NULL), due, priority DESC, id DESC LIMIT ?`
	vals = append(vals, limit)
	rows, err := conn.QueryContext(ctx, q, vals...)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	defer rows.Close()
	items, err := scanTodos(rows, loc, now)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	if items == nil {
		items = []map[string]any{}
	}
	var open, overdue int
	_ = conn.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(due IS NOT NULL AND due < ?),0) FROM todos WHERE done_at IS NULL`, now.Unix()).Scan(&open, &overdue)
	return map[string]any{"count": len(items), "items": items, "open_total": open, "overdue_total": overdue}, nil
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
