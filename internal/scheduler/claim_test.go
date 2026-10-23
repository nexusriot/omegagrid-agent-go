package scheduler

import (
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestClaimRunIsOncePerMinute(t *testing.T) {
	store := newTestStore(t)
	task, err := store.Create("t", "* * * * *", "reminder", nil, nil, false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	minute := time.Now().UTC().Truncate(time.Minute)

	if won, err := store.ClaimRun(task.ID, minute); err != nil || !won {
		t.Fatalf("first claim = %v, %v; want true", won, err)
	}
	if won, _ := store.ClaimRun(task.ID, minute); won {
		t.Fatal("the same minute was claimed twice")
	}
	if won, _ := store.ClaimRun(task.ID, minute.Add(time.Minute)); !won {
		t.Fatal("the next minute could not be claimed")
	}

	if _, err := store.SetEnabled(task.ID, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if won, _ := store.ClaimRun(task.ID, minute.Add(2*time.Minute)); won {
		t.Fatal("a disabled task was claimed")
	}
}

// Two processes on one database (gateway + CLI local mode used to both start
// a runner) must not both execute the same minute.
func TestTwoRunnersOnOneDatabaseRunATaskOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sched.sqlite3")
	a, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer a.Close()
	b, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer b.Close()
	if _, err := a.Create("every minute", "* * * * *", "noop", nil, nil, false); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var runs atomic.Int32
	exec := func(string, map[string]any) (any, error) {
		runs.Add(1)
		return "ok", nil
	}
	ra := NewRunner(a, exec, "", time.Minute)
	rb := NewRunner(b, exec, "", time.Minute)
	var wg sync.WaitGroup
	for _, r := range []*Runner{ra, rb} {
		wg.Add(1)
		go func(r *Runner) { defer wg.Done(); r.tick() }(r)
	}
	wg.Wait()

	if n := runs.Load(); n != 1 {
		t.Fatalf("task ran %d times across two runners, want 1", n)
	}
}

// last_run_at must be the scheduled minute, not the completion time: a run
// that finished in the following minute used to make that minute look
// already done, so a slow every-minute task silently skipped every other run.
func TestLastRunAtIsTheScheduledMinute(t *testing.T) {
	store := newTestStore(t)
	task, err := store.Create("t", "* * * * *", "noop", nil, nil, false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	r := NewRunner(store, func(string, map[string]any) (any, error) { return "ok", nil }, "", time.Minute)
	before := time.Now().UTC().Truncate(time.Minute)
	r.tick()

	got, err := store.Get(task.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LastRunAt == nil {
		t.Fatal("LastRunAt not set")
	}
	at := time.Unix(0, int64(*got.LastRunAt*1e9)).UTC()
	if !at.Equal(at.Truncate(time.Minute)) || at.Before(before) {
		t.Fatalf("LastRunAt = %s, want the tick's whole minute (>= %s)", at, before)
	}
	if got.RunCount != 1 || got.LastResult == nil {
		t.Fatalf("result not recorded: count=%d result=%v", got.RunCount, got.LastResult)
	}
}

// A failed ListEnabled must not mark the swept minutes as evaluated.
func TestListFailureKeepsMinutesPending(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sched.sqlite3"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	r := NewRunner(store, func(string, map[string]any) (any, error) { return nil, nil }, "", time.Minute)
	prev := time.Now().UTC().Truncate(time.Minute).Add(-5 * time.Minute)
	r.lastMinute = prev
	_ = store.Close() // ListEnabled now fails

	r.tick()
	if !r.lastMinute.Equal(prev) {
		t.Fatalf("lastMinute advanced to %s after a failed list, want %s", r.lastMinute, prev)
	}
}

func TestStopIsIdempotentAndBounded(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Create("hang", "* * * * *", "hang", nil, nil, false); err != nil {
		t.Fatalf("Create: %v", err)
	}
	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	r := NewRunner(store, func(string, map[string]any) (any, error) {
		once.Do(func() { close(started) })
		<-release
		return nil, nil
	}, "", time.Hour)
	r.Start()
	<-started

	t0 := time.Now()
	if r.StopTimeout(50 * time.Millisecond) {
		t.Fatal("StopTimeout reported a clean stop while a task was still running")
	}
	if time.Since(t0) > 2*time.Second {
		t.Fatal("StopTimeout did not honour its bound")
	}
	close(release)
	if !r.StopTimeout(2 * time.Second) { // second call must not panic on close
		t.Fatal("runner did not stop after the task finished")
	}

	// A runner that was never started stops immediately.
	if !NewRunner(store, nil, "", time.Minute).StopTimeout(time.Second) {
		t.Fatal("stopping an unstarted runner blocked")
	}
}

func TestRedactTelegramErrDropsToken(t *testing.T) {
	const token = "123:secret"
	err := &url.Error{Op: "Post", URL: "https://api.telegram.org/bot" + token + "/sendMessage", Err: errors.New("dial tcp: timeout")}
	got := redactTelegramErr(err, token)
	if strings.Contains(got, token) || strings.Contains(got, "secret") {
		t.Fatalf("token leaked: %q", got)
	}
	if !strings.Contains(got, "dial tcp: timeout") {
		t.Fatalf("underlying error lost: %q", got)
	}
}
