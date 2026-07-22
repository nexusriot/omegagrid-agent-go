package scheduler

import (
	"strconv"
	"strings"
	"testing"
)

func newTestSkill(t *testing.T) (*ScheduleTaskSkill, *Store) {
	t.Helper()
	store := newTestStore(t)
	return &ScheduleTaskSkill{Store: store}, store
}

func mustMap(t *testing.T, res any) map[string]any {
	t.Helper()
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("result is not map[string]any: %#v", res)
	}
	return m
}

func TestScheduleTaskSkillList(t *testing.T) {
	skill, store := newTestSkill(t)

	t.Run("empty", func(t *testing.T) {
		m := mustMap(t, skill.Execute(map[string]any{"action": "list"}))
		if m["count"] != 0 {
			t.Errorf("count = %v, want 0", m["count"])
		}
		tasks, ok := m["tasks"].([]*Task)
		if !ok {
			t.Fatalf("tasks is not []*Task: %#v", m["tasks"])
		}
		if len(tasks) != 0 {
			t.Errorf("len(tasks) = %d, want 0", len(tasks))
		}
	})

	t.Run("with tasks", func(t *testing.T) {
		if _, err := store.Create("a", "* * * * *", "reminder", nil, nil, false); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if _, err := store.Create("b", "* * * * *", "ping_check", nil, nil, false); err != nil {
			t.Fatalf("Create: %v", err)
		}
		m := mustMap(t, skill.Execute(map[string]any{"action": "list"}))
		if m["count"] != 2 {
			t.Errorf("count = %v, want 2", m["count"])
		}
		if tasks, ok := m["tasks"].([]*Task); !ok || len(tasks) != 2 {
			t.Errorf("tasks = %#v, want 2 entries", m["tasks"])
		}
	})

	t.Run("case-insensitive action with whitespace", func(t *testing.T) {
		m := mustMap(t, skill.Execute(map[string]any{"action": "  LIST "}))
		if _, ok := m["count"]; !ok {
			t.Errorf("expected list result, got %#v", m)
		}
	})
}

func TestScheduleTaskSkillDelete(t *testing.T) {
	skill, store := newTestSkill(t)

	t.Run("float64 id (LLM number)", func(t *testing.T) {
		task, err := store.Create("a", "* * * * *", "reminder", nil, nil, false)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		m := mustMap(t, skill.Execute(map[string]any{
			"action":  "delete",
			"task_id": float64(task.ID),
		}))
		if m["deleted"] != true {
			t.Errorf("deleted = %v, want true", m["deleted"])
		}
		if m["task_id"] != task.ID {
			t.Errorf("task_id = %v, want %d", m["task_id"], task.ID)
		}
	})

	t.Run("string id", func(t *testing.T) {
		task, err := store.Create("b", "* * * * *", "reminder", nil, nil, false)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		m := mustMap(t, skill.Execute(map[string]any{
			"action":  "delete",
			"task_id": strconv.FormatInt(task.ID, 10),
		}))
		if m["deleted"] != true {
			t.Errorf("deleted = %v, want true", m["deleted"])
		}
	})

	t.Run("missing task_id", func(t *testing.T) {
		m := mustMap(t, skill.Execute(map[string]any{"action": "delete"}))
		if _, ok := m["error"]; !ok {
			t.Errorf("expected error, got %#v", m)
		}
	})

	t.Run("not found", func(t *testing.T) {
		m := mustMap(t, skill.Execute(map[string]any{
			"action":  "delete",
			"task_id": float64(987654),
		}))
		if _, ok := m["error"]; !ok {
			t.Errorf("expected not-found error, got %#v", m)
		}
	})
}

func TestScheduleTaskSkillDeleteAll(t *testing.T) {
	skill, store := newTestSkill(t)
	for i := 0; i < 2; i++ {
		if _, err := store.Create("t", "* * * * *", "reminder", nil, nil, false); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	t.Run("deletes everything", func(t *testing.T) {
		m := mustMap(t, skill.Execute(map[string]any{"action": "delete_all"}))
		if m["deleted_all"] != true {
			t.Errorf("deleted_all = %v, want true", m["deleted_all"])
		}
		if m["deleted_count"] != int64(2) {
			t.Errorf("deleted_count = %v, want 2", m["deleted_count"])
		}
	})

	t.Run("alias deleteall", func(t *testing.T) {
		m := mustMap(t, skill.Execute(map[string]any{"action": "deleteall"}))
		if m["deleted_all"] != true {
			t.Errorf("alias 'deleteall' not routed: %#v", m)
		}
	})

	t.Run("alias delete-all", func(t *testing.T) {
		m := mustMap(t, skill.Execute(map[string]any{"action": "delete-all"}))
		if m["deleted_all"] != true {
			t.Errorf("alias 'delete-all' not routed: %#v", m)
		}
	})
}

func TestScheduleTaskSkillEnableDisable(t *testing.T) {
	skill, store := newTestSkill(t)
	task, err := store.Create("t", "* * * * *", "reminder", nil, nil, false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	t.Run("disable", func(t *testing.T) {
		m := mustMap(t, skill.Execute(map[string]any{
			"action":  "disable",
			"task_id": float64(task.ID),
		}))
		if m["ok"] != true || m["enabled"] != false {
			t.Errorf("disable result = %#v", m)
		}
		got, err := store.Get(task.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Enabled {
			t.Error("task should be disabled in store")
		}
	})

	t.Run("enable", func(t *testing.T) {
		m := mustMap(t, skill.Execute(map[string]any{
			"action":  "enable",
			"task_id": float64(task.ID),
		}))
		if m["ok"] != true || m["enabled"] != true {
			t.Errorf("enable result = %#v", m)
		}
		got, err := store.Get(task.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !got.Enabled {
			t.Error("task should be enabled in store")
		}
	})

	t.Run("missing task_id", func(t *testing.T) {
		m := mustMap(t, skill.Execute(map[string]any{"action": "enable"}))
		if _, ok := m["error"]; !ok {
			t.Errorf("expected error, got %#v", m)
		}
	})

	t.Run("not found", func(t *testing.T) {
		m := mustMap(t, skill.Execute(map[string]any{
			"action":  "disable",
			"task_id": float64(987654),
		}))
		if _, ok := m["error"]; !ok {
			t.Errorf("expected not-found error, got %#v", m)
		}
	})
}

func TestScheduleTaskSkillUnknownAction(t *testing.T) {
	skill, _ := newTestSkill(t)
	m := mustMap(t, skill.Execute(map[string]any{"action": "frobnicate"}))
	errStr, ok := m["error"].(string)
	if !ok {
		t.Fatalf("expected error string, got %#v", m)
	}
	if !strings.Contains(errStr, "Unknown action") {
		t.Errorf("error = %q, want it to mention 'Unknown action'", errStr)
	}
}

func TestAsBool(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want bool
	}{
		{"bool true", true, true},
		{"bool false", false, false},
		{"string true", "true", true},
		{"string upper TRUE", "TRUE", true},
		{"string padded yes", "  yes  ", true},
		{"string one", "1", true},
		{"string false", "false", false},
		{"string zero", "0", false},
		{"string no", "no", false},
		{"string arbitrary", "maybe", false},
		{"float64 non-zero", float64(1), true},
		{"float64 zero", float64(0), false},
		{"int non-zero", 2, true},
		{"int zero", 0, false},
		{"nil", nil, false},
		{"unhandled type", []int{1}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := asBool(tc.in); got != tc.want {
				t.Errorf("asBool(%#v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestAsInt64(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want int64
	}{
		{"nil", nil, 0},
		{"int", 5, 5},
		{"int64", int64(7), 7},
		{"float64 truncates", float64(3.9), 3},
		{"float64 whole", float64(42), 42},
		{"string number", "123", 123},
		{"string negative", "-5", -5},
		{"string leading number", "12abc", 12},
		{"string non-numeric", "abc", 0},
		{"unhandled type bool", true, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := asInt64(tc.in); got != tc.want {
				t.Errorf("asInt64(%#v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestAsString(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"nil", nil, ""},
		{"string", "hello", "hello"},
		{"float64 whole", float64(3), "3"},
		{"float64 fraction", float64(1.5), "1.5"},
		{"int", 5, "5"},
		{"bool", true, "true"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := asString(tc.in); got != tc.want {
				t.Errorf("asString(%#v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
