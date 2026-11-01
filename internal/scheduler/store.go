// Package scheduler implements a sqlite-backed cron scheduler.  Tasks run
// registered skills (resolved through a Runner) and optionally push results
// to Telegram.
package scheduler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
	// Embedded zone database: per-task time zones must resolve on hosts and
	// images without /usr/share/zoneinfo (~450 KB).
	_ "time/tzdata"

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
	OneShot              bool           `json:"one_shot"`
	Enabled              bool           `json:"enabled"`
	CreatedAt            float64        `json:"created_at"`
	LastRunAt            *float64       `json:"last_run_at"`
	LastResult           *string        `json:"last_result"`
	RunCount             int            `json:"run_count"`

	// Timezone is the IANA zone the cron expression is evaluated in; "" is
	// UTC.
	Timezone string `json:"timezone"`
	// Prompt makes this an agent task: on each fire the agent answers the
	// prompt (with every tool available) instead of one skill being called.
	// Skill is "agent" for such tasks.
	Prompt string `json:"prompt,omitempty"`
	// MaxRetries re-runs a failed run up to this many times, RetryDelaySec
	// apart (doubling each time).
	MaxRetries    int `json:"max_retries"`
	RetryDelaySec int `json:"retry_delay_sec"`
	// RetryAttempt / NextRetryAt describe a pending retry (persisted, so a
	// restart does not lose it).
	RetryAttempt int      `json:"retry_attempt"`
	NextRetryAt  *float64 `json:"next_retry_at"`
}

// AgentSkill is the Skill value of a prompt task.
const AgentSkill = "agent"

// TaskSpec describes a task to create.
type TaskSpec struct {
	Name                 string
	CronExpr             string
	Skill                string
	Args                 map[string]any
	Prompt               string
	NotifyTelegramChatID *int64
	OneShot              bool
	Timezone             string
	MaxRetries           int
	RetryDelaySec        int
}

// Run is one execution in a task's history.
type Run struct {
	ID         int64   `json:"id"`
	TaskID     int64   `json:"task_id"`
	Trigger    string  `json:"trigger"` // cron | retry | manual
	Attempt    int     `json:"attempt"`
	StartedAt  float64 `json:"started_at"`
	FinishedAt float64 `json:"finished_at"`
	OK         bool    `json:"ok"`
	Result     string  `json:"result"`
}

// maxRunsPerTask bounds the history kept for each task.
const maxRunsPerTask = 200

// taskColumns is the SELECT list scanTask expects.
const taskColumns = `id, name, cron_expr, skill, args_json, notify_telegram_chat_id, one_shot, enabled, created_at, last_run_at, last_result, run_count,
	timezone, prompt, max_retries, retry_delay_sec, retry_attempt, next_retry_at`

type Store struct {
	db *sql.DB
}

func NewStore(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	// busy_timeout matters here: the runner goroutine and HTTP handlers write
	// concurrently, and without it a held write lock surfaces as
	// "database is locked" instead of waiting.
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS scheduled_tasks (
			id                      INTEGER PRIMARY KEY AUTOINCREMENT,
			name                    TEXT NOT NULL,
			cron_expr               TEXT NOT NULL,
			skill                   TEXT NOT NULL,
			args_json               TEXT NOT NULL DEFAULT '{}',
			notify_telegram_chat_id INTEGER,
			one_shot                INTEGER NOT NULL DEFAULT 0,
			enabled                 INTEGER NOT NULL DEFAULT 1,
			created_at              REAL NOT NULL,
			last_run_at             REAL,
			last_result             TEXT,
			run_count               INTEGER NOT NULL DEFAULT 0
		)`); err != nil {
		return nil, err
	}
	// Migrations for databases created before these columns existed. SQLite
	// has no ADD COLUMN IF NOT EXISTS, so the duplicate-column error is
	// expected and ignored on every start after the first.
	for _, col := range []string{
		`one_shot INTEGER NOT NULL DEFAULT 0`,
		`timezone TEXT NOT NULL DEFAULT ''`,
		`prompt TEXT NOT NULL DEFAULT ''`,
		`max_retries INTEGER NOT NULL DEFAULT 0`,
		`retry_delay_sec INTEGER NOT NULL DEFAULT 60`,
		`retry_attempt INTEGER NOT NULL DEFAULT 0`,
		`next_retry_at REAL`,
	} {
		_, _ = db.Exec(`ALTER TABLE scheduled_tasks ADD COLUMN ` + col)
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS task_runs (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id     INTEGER NOT NULL,
			trigger     TEXT NOT NULL,
			attempt     INTEGER NOT NULL DEFAULT 0,
			started_at  REAL NOT NULL,
			finished_at REAL NOT NULL,
			ok          INTEGER NOT NULL,
			result      TEXT
		)`); err != nil {
		return nil, err
	}
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS task_runs_task ON task_runs(task_id, id)`)
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Create(name, cronExpr, skill string, args map[string]any, notifyChat *int64, oneShot bool) (*Task, error) {
	return s.CreateTask(TaskSpec{Name: name, CronExpr: cronExpr, Skill: skill, Args: args, NotifyTelegramChatID: notifyChat, OneShot: oneShot})
}

// CreateTask stores spec. A prompt task gets Skill = AgentSkill.
func (s *Store) CreateTask(spec TaskSpec) (*Task, error) {
	if spec.Args == nil {
		spec.Args = map[string]any{}
	}
	argsJSON, err := json.Marshal(spec.Args)
	if err != nil {
		return nil, fmt.Errorf("marshal args: %w", err)
	}
	if spec.Prompt != "" {
		spec.Skill = AgentSkill
	}
	if spec.RetryDelaySec <= 0 {
		spec.RetryDelaySec = 60
	}
	oneShotI := 0
	if spec.OneShot {
		oneShotI = 1
	}
	now := float64(time.Now().UnixNano()) / 1e9
	res, err := s.db.Exec(
		`INSERT INTO scheduled_tasks (name, cron_expr, skill, args_json, notify_telegram_chat_id, one_shot, created_at,
			timezone, prompt, max_retries, retry_delay_sec) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		spec.Name, spec.CronExpr, spec.Skill, string(argsJSON), spec.NotifyTelegramChatID, oneShotI, now,
		spec.Timezone, spec.Prompt, spec.MaxRetries, spec.RetryDelaySec,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.Get(id)
}

func (s *Store) Get(id int64) (*Task, error) {
	row := s.db.QueryRow(`SELECT `+taskColumns+` FROM scheduled_tasks WHERE id = ?`, id)
	return scanTask(row)
}

func (s *Store) ListAll() ([]*Task, error) {
	return s.list("SELECT " + taskColumns + " FROM scheduled_tasks ORDER BY id")
}

func (s *Store) ListEnabled() ([]*Task, error) {
	return s.list("SELECT " + taskColumns + " FROM scheduled_tasks WHERE enabled = 1 ORDER BY id")
}

func (s *Store) list(query string) ([]*Task, error) {
	return s.listArgs(query)
}

func (s *Store) listArgs(query string, args ...any) ([]*Task, error) {
	rows, err := s.db.Query(query, args...)
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
	result = truncateUTF8(result, 4000)
	_, err := s.db.Exec(
		`UPDATE scheduled_tasks SET last_run_at = ?, last_result = ?, run_count = run_count + 1 WHERE id = ?`,
		float64(time.Now().UnixNano())/1e9, result, id,
	)
	return err
}

// ClaimRun atomically marks task id as run for the given scheduled minute and
// reports whether this caller won it. The UPDATE only matches while
// last_run_at is older than minute, so when two processes share the database
// (gateway plus a CLI, or two gateway replicas) exactly one of them executes a
// given minute; everyone else sees RowsAffected == 0 and skips. last_run_at is
// set to the scheduled minute rather than the completion time so a task that
// runs longer than a minute is not mistaken for having covered the next one.
//
// A one-shot task is disabled in the same statement. Disabling it only after
// the run left it enabled while the skill executed, so a crash or an
// abandoned shutdown mid-run made it fire again at the next match ("remind me
// at 3pm" repeating tomorrow), and a second process could claim it too.
func (s *Store) ClaimRun(id int64, minute time.Time) (bool, error) {
	at := float64(minute.UnixNano()) / 1e9
	res, err := s.db.Exec(
		`UPDATE scheduled_tasks
		 SET last_run_at = ?, enabled = CASE WHEN one_shot = 1 THEN 0 ELSE enabled END
		 WHERE id = ? AND enabled = 1 AND (last_run_at IS NULL OR last_run_at < ?)`,
		at, id, at,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// RecordResult stores a finished run's result and bumps run_count. It leaves
// last_run_at alone — ClaimRun already set it to the scheduled minute.
func (s *Store) RecordResult(id int64, result string) error {
	result = truncateUTF8(result, 4000)
	_, err := s.db.Exec(
		`UPDATE scheduled_tasks SET last_result = ?, run_count = run_count + 1 WHERE id = ?`,
		result, id,
	)
	return err
}

func (s *Store) SetEnabled(id int64, enabled bool) (bool, error) {
	v := 0
	if enabled {
		v = 1
	}
	// Disabling a task also cancels a pending retry: "stop this task" must
	// not leave one more run queued.
	q := `UPDATE scheduled_tasks SET enabled = ? WHERE id = ?`
	if !enabled {
		q = `UPDATE scheduled_tasks SET enabled = ?, retry_attempt = 0, next_retry_at = NULL WHERE id = ?`
	}
	res, err := s.db.Exec(q, v, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) Delete(id int64) (bool, error) {
	_, _ = s.db.Exec(`DELETE FROM task_runs WHERE task_id = ?`, id)
	res, err := s.db.Exec(`DELETE FROM scheduled_tasks WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteAll removes every scheduled task and returns the number deleted.
func (s *Store) DeleteAll() (int64, error) {
	_, _ = s.db.Exec(`DELETE FROM task_runs`)
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
		oneShotI   int
		enabledI   int
		lastRunAt  sql.NullFloat64
		lastResult sql.NullString
		nextRetry  sql.NullFloat64
	)
	if err := row.Scan(&t.ID, &t.Name, &t.CronExpr, &t.Skill, &argsJSON, &notifyChat, &oneShotI, &enabledI, &t.CreatedAt, &lastRunAt, &lastResult, &t.RunCount,
		&t.Timezone, &t.Prompt, &t.MaxRetries, &t.RetryDelaySec, &t.RetryAttempt, &nextRetry); err != nil {
		return nil, err
	}
	if nextRetry.Valid {
		v := nextRetry.Float64
		t.NextRetryAt = &v
	}
	t.OneShot = oneShotI != 0
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

// Location returns the task's time zone (UTC when unset or unknown).
func (t *Task) Location() *time.Location {
	if t.Timezone == "" {
		return time.UTC
	}
	if loc, err := time.LoadLocation(t.Timezone); err == nil {
		return loc
	}
	return time.UTC
}

// ValidateTimezone accepts "" (UTC) or an IANA zone name.
func ValidateTimezone(tz string) error {
	if tz == "" {
		return nil
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return fmt.Errorf("unknown time zone %q", tz)
	}
	return nil
}

// RecordRun appends one execution to the task's history and prunes the
// history to maxRunsPerTask.
func (s *Store) RecordRun(r Run) (int64, error) {
	okI := 0
	if r.OK {
		okI = 1
	}
	res, err := s.db.Exec(
		`INSERT INTO task_runs (task_id, trigger, attempt, started_at, finished_at, ok, result) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.TaskID, r.Trigger, r.Attempt, r.StartedAt, r.FinishedAt, okI, truncateUTF8(r.Result, 4000),
	)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	_, _ = s.db.Exec(`DELETE FROM task_runs WHERE task_id = ? AND id <= (
		SELECT id FROM task_runs WHERE task_id = ? ORDER BY id DESC LIMIT 1 OFFSET ?)`,
		r.TaskID, r.TaskID, maxRunsPerTask)
	return id, nil
}

// ListRuns returns a task's newest runs first.
func (s *Store) ListRuns(taskID int64, limit int) ([]Run, error) {
	if limit <= 0 || limit > maxRunsPerTask {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT id, task_id, trigger, attempt, started_at, finished_at, ok, COALESCE(result, '')
		FROM task_runs WHERE task_id = ? ORDER BY id DESC LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Run, 0)
	for rows.Next() {
		var r Run
		var okI int
		if err := rows.Scan(&r.ID, &r.TaskID, &r.Trigger, &r.Attempt, &r.StartedAt, &r.FinishedAt, &okI, &r.Result); err != nil {
			return nil, err
		}
		r.OK = okI != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// ScheduleRetry arms a retry of attempt at the given time.
func (s *Store) ScheduleRetry(id int64, attempt int, at time.Time) error {
	_, err := s.db.Exec(`UPDATE scheduled_tasks SET retry_attempt = ?, next_retry_at = ? WHERE id = ?`,
		attempt, float64(at.UnixNano())/1e9, id)
	return err
}

// ClearRetry drops any pending retry (a run succeeded or retries ran out).
func (s *Store) ClearRetry(id int64) error {
	_, err := s.db.Exec(`UPDATE scheduled_tasks SET retry_attempt = 0, next_retry_at = NULL WHERE id = ?`, id)
	return err
}

// DueRetries lists tasks with a pending retry due at or before now.
func (s *Store) DueRetries(now time.Time) ([]*Task, error) {
	return s.listArgs("SELECT "+taskColumns+" FROM scheduled_tasks WHERE next_retry_at IS NOT NULL AND next_retry_at <= ? ORDER BY id",
		float64(now.UnixNano())/1e9)
}

// ClaimRetry takes a pending retry atomically, so only one process runs it.
func (s *Store) ClaimRetry(id int64, at float64) (bool, error) {
	res, err := s.db.Exec(`UPDATE scheduled_tasks SET next_retry_at = NULL WHERE id = ? AND next_retry_at = ?`, id, at)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// MaxRetriesLimit bounds TaskSpec.MaxRetries.
const MaxRetriesLimit = 10

// ValidateSpec checks a task before it is stored, so a malformed one is
// refused instead of silently never firing.
func ValidateSpec(spec TaskSpec) error {
	if spec.CronExpr == "" {
		return errors.New("cron_expr is required (e.g. '*/5 * * * *')")
	}
	if err := ValidateCron(spec.CronExpr); err != nil {
		return fmt.Errorf("invalid cron_expr: %w", err)
	}
	if spec.Skill == "" && spec.Prompt == "" {
		return errors.New("either skill or prompt is required")
	}
	if spec.Skill != "" && spec.Skill != AgentSkill && spec.Prompt != "" {
		return errors.New("give skill or prompt, not both")
	}
	if spec.Skill == AgentSkill && spec.Prompt == "" {
		return errors.New("an agent task needs a prompt")
	}
	if err := ValidateTimezone(spec.Timezone); err != nil {
		return err
	}
	if spec.MaxRetries < 0 || spec.MaxRetries > MaxRetriesLimit {
		return fmt.Errorf("max_retries must be 0-%d", MaxRetriesLimit)
	}
	if spec.RetryDelaySec < 0 || spec.RetryDelaySec > 86400 {
		return errors.New("retry_delay_sec must be 0-86400")
	}
	return nil
}
