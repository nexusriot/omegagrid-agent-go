package scheduler

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "sched.sqlite3"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestStoreOneShotRoundtrip(t *testing.T) {
	store := newTestStore(t)

	task, err := store.Create("remind me", "0 15 * * *", "reminder",
		map[string]any{"message": "take a break"}, nil, true)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !task.OneShot {
		t.Error("created task should have OneShot=true")
	}

	got, err := store.Get(task.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.OneShot {
		t.Error("OneShot not persisted")
	}

	recurring, err := store.Create("ping", "*/5 * * * *", "ping_check", nil, nil, false)
	if err != nil {
		t.Fatalf("Create recurring: %v", err)
	}
	if recurring.OneShot {
		t.Error("recurring task should have OneShot=false")
	}
}

func TestRunnerDisablesOneShotAfterRun(t *testing.T) {
	store := newTestStore(t)
	task, err := store.Create("once", "* * * * *", "reminder",
		map[string]any{"message": "hi"}, nil, true)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	exec := func(name string, args map[string]any) (any, error) {
		return map[string]any{"reminder": args["message"]}, nil
	}
	r := NewRunner(store, exec, "", time.Minute)
	r.runTask(task)

	got, err := store.Get(task.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Enabled {
		t.Error("one-shot task should be disabled after first run")
	}
	if got.RunCount != 1 {
		t.Errorf("run_count = %d, want 1", got.RunCount)
	}
	if got.LastResult == nil {
		t.Error("last_result should be recorded")
	}
}

func TestRunnerKeepsRecurringEnabled(t *testing.T) {
	store := newTestStore(t)
	task, err := store.Create("every5", "*/5 * * * *", "ping_check", nil, nil, false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	exec := func(name string, args map[string]any) (any, error) {
		return map[string]any{"ok": true}, nil
	}
	r := NewRunner(store, exec, "", time.Minute)
	r.runTask(task)

	got, err := store.Get(task.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Enabled {
		t.Error("recurring task must stay enabled after a run")
	}
}

func TestReminderMessage(t *testing.T) {
	rt := &Task{Skill: "reminder"}
	if got := reminderMessage(rt, nil, map[string]any{"reminder": "drink water"}); got != "drink water" {
		t.Errorf("reminderMessage = %q, want %q", got, "drink water")
	}
	if got := reminderMessage(&Task{Skill: "weather"}, nil, map[string]any{"reminder": "x"}); got != "" {
		t.Errorf("non-reminder skill should return empty, got %q", got)
	}
	if got := reminderMessage(rt, errFake, map[string]any{"reminder": "x"}); got != "" {
		t.Errorf("failed run should return empty, got %q", got)
	}
}

type fakeErr struct{}

func (fakeErr) Error() string { return "boom" }

var errFake = fakeErr{}

func TestScheduleTaskSkillCreateOneShot(t *testing.T) {
	store := newTestStore(t)
	skill := &ScheduleTaskSkill{Store: store}

	res := skill.Execute(map[string]any{
		"action":    "create",
		"cron_expr": "0 15 * * *",
		"skill":     "reminder",
		"args":      map[string]any{"message": "standup"},
		"one_shot":  true,
	})
	m, ok := res.(map[string]any)
	if !ok || m["created"] != true {
		t.Fatalf("create failed: %v", res)
	}
	task := m["task"].(*Task)
	if !task.OneShot {
		t.Error("one_shot arg not honoured")
	}

	// LLMs sometimes send booleans as strings.
	res = skill.Execute(map[string]any{
		"action":    "create",
		"cron_expr": "30 9 * * *",
		"skill":     "reminder",
		"one_shot":  "true",
	})
	m = res.(map[string]any)
	if m["created"] != true {
		t.Fatalf("create with string one_shot failed: %v", res)
	}
	if !m["task"].(*Task).OneShot {
		t.Error(`one_shot="true" (string) not coerced`)
	}
}
