package scheduler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestCronRunNotifiesTelegram(t *testing.T) {
	var mu sync.Mutex
	var got []map[string]any
	var paths []string
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		mu.Lock()
		got, paths = append(got, m), append(paths, r.URL.Path)
		mu.Unlock()
		if strings.Contains(r.URL.Path, "bad") {
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer tg.Close()
	old := telegramAPIBase
	telegramAPIBase = tg.URL
	defer func() { telegramAPIBase = old }()

	store := newTestStore(t)
	chat := int64(42)
	task, err := store.Create("disk", "0 * * * *", "echo", map[string]any{"x": "1"}, &chat, false)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(_ context.Context, _ string, args map[string]any) (any, error) {
		return map[string]any{"ok": true, "echo": args["x"]}, nil
	}
	r := NewRunner(store, exec, "tok", 0)
	// A manual run reports back to its caller and sends nothing.
	if run, err := r.RunNow(context.Background(), task.ID); err != nil || !run.OK {
		t.Fatalf("run %+v %v", run, err)
	}
	mu.Lock()
	if len(got) != 0 {
		t.Fatalf("manual run notified: %v", got)
	}
	mu.Unlock()
	if run := r.execute(context.Background(), task, "cron", 0); !run.OK {
		t.Fatalf("cron run %+v", run)
	}
	mu.Lock()
	if len(got) != 1 || paths[0] != "/bottok/sendMessage" || got[0]["chat_id"] != 42.0 ||
		!strings.Contains(got[0]["text"].(string), "Scheduled: disk") {
		t.Fatalf("telegram got %v %v", paths, got)
	}

	mu.Unlock()

	// A failing Bot API is logged, not fatal; long text is cut to 4096 bytes.
	sendTelegram("bad", 1, strings.Repeat("é", 5000))
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || paths[1] != "/botbad/sendMessage" {
		t.Fatalf("second send: %v", paths)
	}
	if n := len(got[1]["text"].(string)); n > 4096 || n < 4000 {
		t.Fatalf("text not truncated: %d bytes", n)
	}
}

func TestScheduleTaskSkillHistory(t *testing.T) {
	skill, store := newTestSkill(t)
	task, err := store.Create("h", "* * * * *", "reminder", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := store.RecordRun(Run{TaskID: task.ID, Trigger: "manual", OK: i != 1, Result: "r"}); err != nil {
			t.Fatal(err)
		}
	}
	m := mustMap(t, skill.Execute(map[string]any{"action": "history", "task_id": float64(task.ID), "limit": 2.0}))
	if m["count"] != 2 {
		t.Fatalf("history %v", m)
	}
	if m := mustMap(t, skill.Execute(map[string]any{"action": "runs"})); m["error"] == nil {
		t.Fatalf("missing task_id accepted: %v", m)
	}
}
