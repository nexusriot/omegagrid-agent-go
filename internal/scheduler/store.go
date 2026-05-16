// Package scheduler implements a sqlite-backed cron scheduler.  Tasks run
// registered skills (resolved through a Runner) and optionally push results
// to Telegram.
package scheduler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Task is a single scheduled job.
type Task struct {
	ID                   int64          `json:"id"`
	Name                 string         `json:"name"`
	CronExpr             string         `json:"cron_expr"`
	Skill                string         `json:"skill"`
	Args                 map[string]any `json:"args"`
	NotifyTelegramChatID *int64         `json:"notify_telegram_chat_id"`
	Enabled              bool           `json:"enabled"`
	CreatedAt            float64        `json:"created_at"`
	LastRunAt            *float64       `json:"last_run_at"`
	LastResult           *string        `json:"last_result"`
	RunCount             int            `json:"run_count"`
}

type Store struct {
	db *sql.DB
}

func NewStore(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS scheduled_tasks (
			id                      INTEGER PRIMARY KEY AUTOINCREMENT,
			name                    TEXT NOT NULL,
			cron_expr               TEXT NOT NULL,
			skill                   TEXT NOT NULL,
			args_json               TEXT NOT NULL DEFAULT '{}',
			notify_telegram_chat_id INTEGER,
			enabled                 INTEGER NOT NULL DEFAULT 1,
			created_at              REAL NOT NULL,
			last_run_at             REAL,
			last_result             TEXT,
			run_count               INTEGER NOT NULL DEFAULT 0
		)`); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Create(name, cronExpr, skill string, args map[string]any, notifyChat *int64) (*Task, error) {
	if args == nil {
		args = map[string]any{}
	}
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("marshal args: %w", err)
	}
	now := float64(time.Now().UnixNano()) / 1e9
	res, err := s.db.Exec(
		`INSERT INTO scheduled_tasks (name, cron_expr, skill, args_json, notify_telegram_chat_id, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		name, cronExpr, skill, string(argsJSON), notifyChat, now,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.Get(id)
}

func (s *Store) Get(id int64) (*Task, error) {
	row := s.db.QueryRow(`SELECT id, name, cron_expr, skill, args_json, notify_telegram_chat_id, enabled, created_at, last_run_at, last_result, run_count FROM scheduled_tasks WHERE id = ?`, id)
	return scanTask(row)
}

func (s *Store) ListAll() ([]*Task, error) {
	return s.list("SELECT id, name, cron_expr, skill, args_json, notify_telegram_chat_id, enabled, created_at, last_run_at, last_result, run_count FROM scheduled_tasks ORDER BY id")
}

func (s *Store) ListEnabled() ([]*Task, error) {
	return s.list("SELECT id, name, cron_expr, skill, args_json, notify_telegram_chat_id, enabled, created_at, last_run_at, last_result, run_count FROM scheduled_tasks WHERE enabled = 1 ORDER BY id")
}

func (s *Store) list(query string) ([]*Task, error) {
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*Task, 0) // never nil — serialises as [] not null
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) UpdateLastRun(id int64, result string) error {
	if len(result) > 4000 {
		result = result[:4000]
	}
	_, err := s.db.Exec(
		`UPDATE scheduled_tasks SET last_run_at = ?, last_result = ?, run_count = run_count + 1 WHERE id = ?`,
		float64(time.Now().UnixNano())/1e9, result, id,
	)
	return err
}

func (s *Store) SetEnabled(id int64, enabled bool) (bool, error) {
	v := 0
	if enabled {
		v = 1
	}
	res, err := s.db.Exec(`UPDATE scheduled_tasks SET enabled = ? WHERE id = ?`, v, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) Delete(id int64) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM scheduled_tasks WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteAll removes every scheduled task and returns the number deleted.
func (s *Store) DeleteAll() (int64, error) {
	res, err := s.db.Exec(`DELETE FROM scheduled_tasks`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// scanRow lets scanTask consume either *sql.Row or *sql.Rows.
type scanRow interface {
	Scan(dest ...any) error
}

func scanTask(row scanRow) (*Task, error) {
	var (
		t          Task
		argsJSON   string
		notifyChat sql.NullInt64
		enabledI   int
		lastRunAt  sql.NullFloat64
		lastResult sql.NullString
	)
	if err := row.Scan(&t.ID, &t.Name, &t.CronExpr, &t.Skill, &argsJSON, &notifyChat, &enabledI, &t.CreatedAt, &lastRunAt, &lastResult, &t.RunCount); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(argsJSON), &t.Args); err != nil {
		return nil, fmt.Errorf("decode args_json: %w", err)
	}
	if notifyChat.Valid {
		v := notifyChat.Int64
		t.NotifyTelegramChatID = &v
	}
	t.Enabled = enabledI != 0
	if lastRunAt.Valid {
		v := lastRunAt.Float64
		t.LastRunAt = &v
	}
	if lastResult.Valid {
		v := lastResult.String
		t.LastResult = &v
	}
	return &t, nil
}
