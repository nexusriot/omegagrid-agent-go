package scheduler

import (
	"strings"
	"testing"
)

func TestStoreSetEnabled(t *testing.T) {
	store := newTestStore(t)
	task, err := store.Create("t", "* * * * *", "reminder", nil, nil, false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !task.Enabled {
		t.Fatalf("newly created task should be enabled by default")
	}

	t.Run("disable", func(t *testing.T) {
		ok, err := store.SetEnabled(task.ID, false)
		if err != nil {
			t.Fatalf("SetEnabled: %v", err)
		}
		if !ok {
			t.Error("SetEnabled should report a row was affected")
		}
		got, err := store.Get(task.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Enabled {
			t.Error("task should be disabled")
		}
	})

	t.Run("re-enable", func(t *testing.T) {
		ok, err := store.SetEnabled(task.ID, true)
		if err != nil {
			t.Fatalf("SetEnabled: %v", err)
		}
		if !ok {
			t.Error("SetEnabled should report a row was affected")
		}
		got, err := store.Get(task.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !got.Enabled {
			t.Error("task should be enabled")
		}
	})

	t.Run("missing id", func(t *testing.T) {
		ok, err := store.SetEnabled(999999, false)
		if err != nil {
			t.Fatalf("SetEnabled on missing id should not error: %v", err)
		}
		if ok {
			t.Error("SetEnabled on missing id should report false (no rows affected)")
		}
	})
}

func TestStoreDelete(t *testing.T) {
	store := newTestStore(t)
	task, err := store.Create("t", "* * * * *", "reminder", nil, nil, false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	t.Run("existing", func(t *testing.T) {
		ok, err := store.Delete(task.ID)
		if err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if !ok {
			t.Error("Delete should report a row was removed")
		}
		if _, err := store.Get(task.ID); err == nil {
			t.Error("Get after Delete should return an error")
		}
	})

	t.Run("missing id", func(t *testing.T) {
		ok, err := store.Delete(task.ID)
		if err != nil {
			t.Fatalf("Delete on missing id should not error: %v", err)
		}
		if ok {
			t.Error("Delete on missing id should report false")
		}
	})
}

func TestStoreDeleteAll(t *testing.T) {
	store := newTestStore(t)

	t.Run("empty store", func(t *testing.T) {
		n, err := store.DeleteAll()
		if err != nil {
			t.Fatalf("DeleteAll: %v", err)
		}
		if n != 0 {
			t.Errorf("DeleteAll on empty store = %d, want 0", n)
		}
	})

	t.Run("with tasks", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			if _, err := store.Create("t", "* * * * *", "reminder", nil, nil, false); err != nil {
				t.Fatalf("Create: %v", err)
			}
		}
		n, err := store.DeleteAll()
		if err != nil {
			t.Fatalf("DeleteAll: %v", err)
		}
		if n != 3 {
			t.Errorf("DeleteAll = %d, want 3", n)
		}
		all, err := store.ListAll()
		if err != nil {
			t.Fatalf("ListAll: %v", err)
		}
		if len(all) != 0 {
			t.Errorf("ListAll after DeleteAll = %d tasks, want 0", len(all))
		}
	})
}

func TestStoreListAllAndEnabled(t *testing.T) {
	store := newTestStore(t)

	t.Run("empty is non-nil", func(t *testing.T) {
		all, err := store.ListAll()
		if err != nil {
			t.Fatalf("ListAll: %v", err)
		}
		if all == nil {
			t.Error("ListAll should return a non-nil slice so it serialises as []")
		}
		if len(all) != 0 {
			t.Errorf("ListAll on empty = %d, want 0", len(all))
		}
		enabled, err := store.ListEnabled()
		if err != nil {
			t.Fatalf("ListEnabled: %v", err)
		}
		if enabled == nil {
			t.Error("ListEnabled should return a non-nil slice")
		}
	})

	t.Run("filters by enabled and orders by id", func(t *testing.T) {
		t1, err := store.Create("first", "* * * * *", "reminder", nil, nil, false)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		t2, err := store.Create("second", "* * * * *", "ping_check", nil, nil, false)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if _, err := store.SetEnabled(t2.ID, false); err != nil {
			t.Fatalf("SetEnabled: %v", err)
		}

		all, err := store.ListAll()
		if err != nil {
			t.Fatalf("ListAll: %v", err)
		}
		if len(all) != 2 {
			t.Fatalf("ListAll = %d, want 2", len(all))
		}
		if all[0].ID > all[1].ID {
			t.Errorf("ListAll not ordered by id: %d then %d", all[0].ID, all[1].ID)
		}

		enabled, err := store.ListEnabled()
		if err != nil {
			t.Fatalf("ListEnabled: %v", err)
		}
		if len(enabled) != 1 {
			t.Fatalf("ListEnabled = %d, want 1", len(enabled))
		}
		if enabled[0].ID != t1.ID {
			t.Errorf("ListEnabled returned id %d, want the enabled task %d", enabled[0].ID, t1.ID)
		}
	})
}

func TestStoreUpdateLastRun(t *testing.T) {
	store := newTestStore(t)

	t.Run("records result and increments run count", func(t *testing.T) {
		task, err := store.Create("t", "* * * * *", "reminder", nil, nil, false)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := store.UpdateLastRun(task.ID, "hello"); err != nil {
			t.Fatalf("UpdateLastRun: %v", err)
		}
		got, err := store.Get(task.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.LastResult == nil || *got.LastResult != "hello" {
			t.Errorf("LastResult = %v, want %q", got.LastResult, "hello")
		}
		if got.RunCount != 1 {
			t.Errorf("RunCount = %d, want 1", got.RunCount)
		}
		if got.LastRunAt == nil {
			t.Error("LastRunAt should be set")
		}

		if err := store.UpdateLastRun(task.ID, "again"); err != nil {
			t.Fatalf("UpdateLastRun: %v", err)
		}
		got, err = store.Get(task.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.RunCount != 2 {
			t.Errorf("RunCount = %d, want 2", got.RunCount)
		}
		if got.LastResult == nil || *got.LastResult != "again" {
			t.Errorf("LastResult = %v, want %q", got.LastResult, "again")
		}
	})

	t.Run("truncates results longer than 4000 chars", func(t *testing.T) {
		task, err := store.Create("t", "* * * * *", "reminder", nil, nil, false)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		big := strings.Repeat("x", 5000)
		if err := store.UpdateLastRun(task.ID, big); err != nil {
			t.Fatalf("UpdateLastRun: %v", err)
		}
		got, err := store.Get(task.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.LastResult == nil {
			t.Fatal("LastResult should be set")
		}
		if len(*got.LastResult) != 4000 {
			t.Errorf("truncated length = %d, want 4000", len(*got.LastResult))
		}
	})

	t.Run("exactly 4000 chars is kept intact", func(t *testing.T) {
		task, err := store.Create("t", "* * * * *", "reminder", nil, nil, false)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		exact := strings.Repeat("y", 4000)
		if err := store.UpdateLastRun(task.ID, exact); err != nil {
			t.Fatalf("UpdateLastRun: %v", err)
		}
		got, err := store.Get(task.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.LastResult == nil || len(*got.LastResult) != 4000 {
			t.Errorf("length = %v, want 4000", got.LastResult)
		}
	})
}
