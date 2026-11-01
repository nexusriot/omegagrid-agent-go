package scheduler

import (
	"context"
	"errors"
	"fmt"
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
	exec := func(context.Context, string, map[string]any) (any, error) {
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
	r := NewRunner(store, func(context.Context, string, map[string]any) (any, error) { return "ok", nil }, "", time.Minute)
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
	r := NewRunner(store, func(context.Context, string, map[string]any) (any, error) { return nil, nil }, "", time.Minute)
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
	r := NewRunner(store, func(context.Context, string, map[string]any) (any, error) {
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

// Runner A ran the 12:00 slot; runner B's ticker drifted past 12:00, so B's
// next tick swept [12:00, now] and matched 12:00. B used to claim `now`
// instead of the slot that matched, which was newer than A's last_run_at, so
// it won the claim and ran the slot a second time.
func TestCatchupClaimsTheMatchedSlot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sched.sqlite3")
	a, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	now := time.Now().UTC().Truncate(time.Minute)
	prev := now.Add(-time.Minute)
	cron := fmt.Sprintf("%d %d %d %d *", prev.Minute(), prev.Hour(), prev.Day(), int(prev.Month()))
	task, err := a.Create("daily", cron, "noop", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if won, err := a.ClaimRun(task.ID, prev); !won || err != nil {
		t.Fatalf("A's claim = %v, %v", won, err)
	}

	var runs atomic.Int32
	rb := NewRunner(b, func(context.Context, string, map[string]any) (any, error) { runs.Add(1); return "ok", nil }, "", time.Minute)
	rb.lastMinute = prev.Add(-time.Minute)
	rb.tick()
	if n := runs.Load(); n != 0 {
		t.Fatalf("B re-ran a slot A had already claimed (%d run(s))", n)
	}
}

// A one-shot stayed enabled until its run finished, so a process that died
// mid-run left it armed for the next match.
func TestClaimDisablesOneShotAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sched.sqlite3")
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.Create("remind", "0 15 * * *", "noop", nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	m := time.Date(2026, 10, 30, 15, 0, 0, 0, time.UTC)
	if won, err := s.ClaimRun(task.ID, m); !won || err != nil {
		t.Fatalf("ClaimRun = %v, %v", won, err)
	}
	// Simulate the crash: the run never finishes, the store is reopened.
	_ = s.Close()
	s, err = NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Fatal("one-shot still enabled after its claim")
	}
	if won, _ := s.ClaimRun(task.ID, m.Add(24*time.Hour)); won {
		t.Fatal("one-shot claimed again the next day")
	}

	// A recurring task stays enabled through its claim.
	rec, _ := s.Create("daily", "0 15 * * *", "noop", nil, nil, false)
	_, _ = s.ClaimRun(rec.ID, m)
	if got, _ := s.Get(rec.ID); !got.Enabled {
		t.Fatal("recurring task disabled by its claim")
	}
}

// Stop cancels the context a running task's skill sees, so a hung skill no
// longer outlives shutdown.
func TestStopCancelsRunningTask(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Create("hang", "* * * * *", "slow", nil, nil, false); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	r := NewRunner(store, func(ctx context.Context, _ string, _ map[string]any) (any, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}, "", time.Hour)
	r.Start()
	<-started
	if !r.StopTimeout(2 * time.Second) {
		t.Fatal("Stop did not cancel the running task")
	}
}
