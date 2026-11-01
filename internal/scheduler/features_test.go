package scheduler

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A task's cron is evaluated on its own wall clock: "0 9 * * *" in
// Europe/Berlin fires at 08:00 UTC in winter, not 09:00 UTC.
func TestTimezoneShiftsTheMatch(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	winter8 := time.Date(2026, 1, 15, 8, 0, 0, 0, time.UTC)
	winter9 := time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC)
	if _, ok := latestMatchIn("0 9 * * *", []time.Time{winter8}, berlin); !ok {
		t.Error("09:00 Berlin must match 08:00 UTC in January")
	}
	if _, ok := latestMatchIn("0 9 * * *", []time.Time{winter9}, berlin); ok {
		t.Error("09:00 Berlin must not match 09:00 UTC in January")
	}
	summer7 := time.Date(2026, 7, 15, 7, 0, 0, 0, time.UTC)
	if _, ok := latestMatchIn("0 9 * * *", []time.Time{summer7}, berlin); !ok {
		t.Error("09:00 Berlin must match 07:00 UTC in July (CEST)")
	}
	if (&Task{Timezone: "Nope/Nowhere"}).Location() != time.UTC {
		t.Error("an unknown zone must fall back to UTC")
	}
}

func TestValidateSpec(t *testing.T) {
	ok := []TaskSpec{
		{CronExpr: "0 9 * * *", Skill: "reminder"},
		{CronExpr: "0 9 * * *", Prompt: "summarise", Timezone: "America/New_York", MaxRetries: 3},
	}
	for _, s := range ok {
		if err := ValidateSpec(s); err != nil {
			t.Errorf("%+v: %v", s, err)
		}
	}
	bad := map[string]TaskSpec{
		"no action":     {CronExpr: "0 9 * * *"},
		"both":          {CronExpr: "0 9 * * *", Skill: "weather", Prompt: "x"},
		"agent no text": {CronExpr: "0 9 * * *", Skill: AgentSkill},
		"bad zone":      {CronExpr: "0 9 * * *", Skill: "x", Timezone: "Mars/Olympus"},
		"retries":       {CronExpr: "0 9 * * *", Skill: "x", MaxRetries: 11},
		"bad cron":      {CronExpr: "99 * * * *", Skill: "x"},
	}
	for name, s := range bad {
		if err := ValidateSpec(s); err == nil {
			t.Errorf("%s: accepted %+v", name, s)
		}
	}
}

// A failed run arms a persisted retry with a doubling delay; the retry runs
// on a later tick, and success clears it. Retries never outlive max_retries.
func TestFailedRunIsRetried(t *testing.T) {
	store := newTestStore(t)
	task, err := store.CreateTask(TaskSpec{Name: "flaky", CronExpr: "0 0 1 1 *", Skill: "flaky", MaxRetries: 2, RetryDelaySec: 30})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	r := NewRunner(store, func(context.Context, string, map[string]any) (any, error) {
		if calls.Add(1) < 3 {
			return nil, errors.New("transient")
		}
		return "ok", nil
	}, "", time.Minute)

	r.runTask(task)
	got, _ := store.Get(task.ID)
	if got.RetryAttempt != 1 || got.NextRetryAt == nil {
		t.Fatalf("after first failure: attempt=%d next=%v", got.RetryAttempt, got.NextRetryAt)
	}
	delay := time.Until(time.Unix(0, int64(*got.NextRetryAt*1e9)))
	if delay < 25*time.Second || delay > 31*time.Second {
		t.Fatalf("first retry in %v, want ~30s", delay)
	}

	r.runDueRetries(time.Now()) // not due yet
	if calls.Load() != 1 {
		t.Fatal("a retry ran before it was due")
	}
	r.runDueRetries(time.Now().Add(time.Minute))
	got, _ = store.Get(task.ID)
	if calls.Load() != 2 || got.RetryAttempt != 2 {
		t.Fatalf("after second failure: calls=%d attempt=%d", calls.Load(), got.RetryAttempt)
	}
	if d := time.Until(time.Unix(0, int64(*got.NextRetryAt*1e9))); d < 55*time.Second {
		t.Fatalf("second retry in %v, want the delay doubled", d)
	}
	r.runDueRetries(time.Now().Add(5 * time.Minute))
	got, _ = store.Get(task.ID)
	if calls.Load() != 3 || got.RetryAttempt != 0 || got.NextRetryAt != nil {
		t.Fatalf("success must clear the retry: calls=%d %+v", calls.Load(), got)
	}
	runs, _ := store.ListRuns(task.ID, 10)
	if len(runs) != 3 || runs[0].Trigger != "retry" || !runs[0].OK || runs[2].OK || runs[2].Trigger != "cron" {
		t.Fatalf("history = %+v", runs)
	}
}

func TestRetriesStopAtTheLimitAndOnDisable(t *testing.T) {
	store := newTestStore(t)
	task, _ := store.CreateTask(TaskSpec{Name: "dead", CronExpr: "0 0 1 1 *", Skill: "x", MaxRetries: 1})
	r := NewRunner(store, func(context.Context, string, map[string]any) (any, error) { return nil, errors.New("down") }, "", time.Minute)
	r.runTask(task)
	r.runDueRetries(time.Now().Add(time.Hour))
	if got, _ := store.Get(task.ID); got.NextRetryAt != nil {
		t.Fatal("a retry was armed past max_retries")
	}

	task2, _ := store.CreateTask(TaskSpec{Name: "dead2", CronExpr: "0 0 1 1 *", Skill: "x", MaxRetries: 3})
	r.runTask(task2)
	if _, err := store.SetEnabled(task2.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Get(task2.ID); got.NextRetryAt != nil {
		t.Fatal("disabling a task left its retry armed")
	}
}

func TestPromptTaskRunsTheAgent(t *testing.T) {
	store := newTestStore(t)
	task, err := store.CreateTask(TaskSpec{Name: "digest", CronExpr: "0 8 * * *", Prompt: "summarise the news"})
	if err != nil || task.Skill != AgentSkill {
		t.Fatalf("create = %+v, %v", task, err)
	}
	r := NewRunner(store, func(context.Context, string, map[string]any) (any, error) {
		t.Fatal("a prompt task called a skill")
		return nil, nil
	}, "", time.Minute)

	run, _ := r.RunNow(context.Background(), task.ID)
	if run.OK || !strings.Contains(run.Result, "agent") {
		t.Fatalf("without an agent: %+v", run)
	}
	var got string
	r.SetAgent(func(_ context.Context, prompt string) (string, error) { got = prompt; return "Markets were calm.", nil })
	run, _ = r.RunNow(context.Background(), task.ID)
	if !run.OK || run.Result != "Markets were calm." || got != "summarise the news" || run.Trigger != "manual" {
		t.Fatalf("run = %+v, prompt %q", run, got)
	}
	after, _ := store.Get(task.ID)
	if after.LastRunAt != nil {
		t.Fatal("run-now moved last_run_at, which shifts the cron schedule")
	}
	if after.LastResult == nil || *after.LastResult != "Markets were calm." || after.RunCount != 2 {
		t.Fatalf("last_result/run_count = %v/%d", after.LastResult, after.RunCount)
	}
}

// Run-now of a one-shot reminder must not disable it: the user is testing
// the reminder, not consuming it.
func TestRunNowKeepsOneShotArmed(t *testing.T) {
	store := newTestStore(t)
	task, _ := store.CreateTask(TaskSpec{Name: "r", CronExpr: "0 15 * * *", Skill: "reminder", OneShot: true})
	r := NewRunner(store, func(context.Context, string, map[string]any) (any, error) { return "ok", nil }, "", time.Minute)
	if _, err := r.RunNow(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Get(task.ID); !got.Enabled {
		t.Fatal("run-now disabled the one-shot")
	}
}

func TestRunHistoryIsPruned(t *testing.T) {
	store := newTestStore(t)
	task, _ := store.Create("t", "* * * * *", "x", nil, nil, false)
	for i := 0; i < maxRunsPerTask+15; i++ {
		if _, err := store.RecordRun(Run{TaskID: task.ID, Trigger: "cron", OK: true, Result: "r"}); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	_ = store.db.QueryRow(`SELECT COUNT(*) FROM task_runs WHERE task_id = ?`, task.ID).Scan(&n)
	if n != maxRunsPerTask {
		t.Fatalf("kept %d runs, want %d", n, maxRunsPerTask)
	}
	if _, err := store.Delete(task.ID); err != nil {
		t.Fatal(err)
	}
	_ = store.db.QueryRow(`SELECT COUNT(*) FROM task_runs WHERE task_id = ?`, task.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("deleting the task left %d runs", n)
	}
}

// A database from before these columns existed opens and keeps its tasks.
func TestLegacySchemaIsMigrated(t *testing.T) {
	path := t.TempDir() + "/old.sqlite3"
	old, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = old.db.Exec(`DROP TABLE scheduled_tasks`)
	_, _ = old.db.Exec(`DROP TABLE task_runs`)
	if _, err := old.db.Exec(`CREATE TABLE scheduled_tasks (
		id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, cron_expr TEXT NOT NULL, skill TEXT NOT NULL,
		args_json TEXT NOT NULL DEFAULT '{}', notify_telegram_chat_id INTEGER, enabled INTEGER NOT NULL DEFAULT 1,
		created_at REAL NOT NULL, last_run_at REAL, last_result TEXT, run_count INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	_, _ = old.db.Exec(`INSERT INTO scheduled_tasks (name, cron_expr, skill, created_at) VALUES ('legacy', '0 9 * * *', 'weather', 1)`)
	_ = old.Close()

	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tasks, err := s.ListAll()
	if err != nil || len(tasks) != 1 || tasks[0].Name != "legacy" || tasks[0].RetryDelaySec != 60 || tasks[0].Timezone != "" {
		t.Fatalf("migrated tasks = %+v, %v", tasks, err)
	}
}
