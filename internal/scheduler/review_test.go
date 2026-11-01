package scheduler

import (
	"context"
	"testing"
	"time"
)

func TestSingleValueUnitStepMatchesRemainder(t *testing.T) {
	for _, expr := range []string{"5/1 * * * *", "0 5/1 * * *", "0 0 5/1 * *", "0 0 1 5/1 *", "0 0 * * mon/1"} {
		if err := ValidateCron(expr); err != nil {
			t.Fatal(err)
		}
	}
	for minute := 0; minute < 60; minute++ {
		dt := time.Date(2026, 1, 1, 0, minute, 0, 0, time.UTC)
		if got := Matches("5/1 * * * *", dt); got != (minute >= 5) {
			t.Errorf("minute %d: got %v, want %v", minute, got, minute >= 5)
		}
		if got := Matches("5 * * * *", dt); got != (minute == 5) {
			t.Errorf("bare value changed at minute %d", minute)
		}
	}
}

func TestShutdownLeavesQueuedTasksUnclaimed(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(map[bool]string{false: "cron", true: "retry"}[retry], func(t *testing.T) {
			store := newTestStore(t)
			var ids []int64
			for i := 0; i < 2; i++ {
				task, err := store.Create("queued", "* * * * *", "x", nil, nil, true)
				if err != nil {
					t.Fatal(err)
				}
				ids = append(ids, task.ID)
				if retry {
					if err := store.ScheduleRetry(task.ID, 1, time.Now().Add(-time.Minute)); err != nil {
						t.Fatal(err)
					}
				}
			}
			calls := 0
			var r *Runner
			r = NewRunner(store, func(context.Context, string, map[string]any) (any, error) {
				calls++
				r.cancel() // shutdown while the first task completes
				return "ok", nil
			}, "", time.Minute)
			if retry {
				r.runDueRetries(time.Now())
			} else {
				r.tick()
			}
			if calls != 1 {
				t.Errorf("executed %d tasks after shutdown, want 1", calls)
			}
			queued, err := store.Get(ids[1])
			if err != nil {
				t.Fatal(err)
			}
			if queued.LastRunAt != nil || queued.RunCount != 0 || !queued.Enabled {
				t.Errorf("queued task was consumed: %+v", queued)
			}
			if retry && queued.NextRetryAt == nil {
				t.Error("queued retry was consumed")
			}
		})
	}
}
