package scheduler

import (
	"path/filepath"
	"strings"
	"testing"
)

// A skill that panics used to unwind runTask and land in the tick's recover,
// which aborted the whole tick: the panicking task lost its last_result and
// every task queued behind it in that minute silently never ran.
func TestPanickingTaskDoesNotAbortTheTick(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sched.sqlite3"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	boom, err := store.Create("boom", "* * * * *", "panics", nil, nil, false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	later, err := store.Create("later", "* * * * *", "ok", nil, nil, false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	var ran []string
	r := NewRunner(store, func(skill string, _ map[string]any) (any, error) {
		ran = append(ran, skill)
		if skill == "panics" {
			panic("skill exploded")
		}
		return map[string]any{"ok": true}, nil
	}, "", 0)

	r.tick()

	if len(ran) != 2 {
		t.Fatalf("executed %v, want both tasks to run", ran)
	}

	got, err := store.Get(boom.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.RunCount != 1 {
		t.Errorf("panicking task run_count = %d, want 1", got.RunCount)
	}
	if got.LastResult == nil || !strings.Contains(*got.LastResult, "crashed") {
		t.Errorf("panicking task last_result = %v, want the crash recorded", got.LastResult)
	}

	got, err = store.Get(later.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.RunCount != 1 {
		t.Errorf("following task run_count = %d, want 1 (it must not be skipped)", got.RunCount)
	}
}
