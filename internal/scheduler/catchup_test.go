package scheduler

import (
	"path/filepath"
	"testing"
	"time"
)

func minuteAt(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts.UTC()
}

func TestPendingMinutesFirstTick(t *testing.T) {
	r := &Runner{}
	now := minuteAt(t, "2026-07-30 12:00")
	got := r.pendingMinutes(now)
	if len(got) != 1 || !got[0].Equal(now) {
		t.Fatalf("first tick = %v, want [%v]", got, now)
	}
}

func TestPendingMinutesFillsDriftGap(t *testing.T) {
	// A 60s ticker drifts: fire at 12:00:59.8, next fire at 12:02:00.1. The
	// 12:01 minute was never evaluated and anything scheduled for it was lost.
	r := &Runner{lastMinute: minuteAt(t, "2026-07-30 12:00")}
	got := r.pendingMinutes(minuteAt(t, "2026-07-30 12:03"))

	want := []string{"2026-07-30 12:01", "2026-07-30 12:02", "2026-07-30 12:03"}
	if len(got) != len(want) {
		t.Fatalf("pendingMinutes = %v, want %d entries", got, len(want))
	}
	for i, w := range want {
		if !got[i].Equal(minuteAt(t, w)) {
			t.Fatalf("pendingMinutes[%d] = %v, want %v", i, got[i], w)
		}
	}
}

func TestPendingMinutesSameMinuteTwice(t *testing.T) {
	// Sub-minute tick intervals must not re-evaluate a minute already handled.
	now := minuteAt(t, "2026-07-30 12:00")
	r := &Runner{lastMinute: now}
	got := r.pendingMinutes(now)
	if len(got) != 1 || !got[0].Equal(now) {
		t.Fatalf("pendingMinutes = %v, want [%v]", got, now)
	}
}

func TestPendingMinutesCapsCatchup(t *testing.T) {
	// A host asleep for a day must not replay a day of cron minutes.
	r := &Runner{lastMinute: minuteAt(t, "2026-07-29 12:00")}
	now := minuteAt(t, "2026-07-30 12:00")
	got := r.pendingMinutes(now)
	if len(got) != maxCatchupMinutes {
		t.Fatalf("len(pendingMinutes) = %d, want %d", len(got), maxCatchupMinutes)
	}
	if !got[len(got)-1].Equal(now) {
		t.Fatalf("last minute = %v, want %v", got[len(got)-1], now)
	}
}

func TestMatchesAny(t *testing.T) {
	minutes := []time.Time{
		minuteAt(t, "2026-07-30 12:01"),
		minuteAt(t, "2026-07-30 12:02"),
	}
	if !matchesAny("2 12 * * *", minutes) {
		t.Fatal("expected 12:02 expression to match the swept window")
	}
	if matchesAny("5 12 * * *", minutes) {
		t.Fatal("12:05 expression must not match a 12:01-12:02 window")
	}
}

// A task whose cron matches several swept minutes must still run only once.
func TestTickRunsMissedTaskExactlyOnce(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sched.sqlite3"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	if _, err := store.Create("every minute", "* * * * *", "reminder",
		map[string]any{"message": "hi"}, nil, false); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var runs int
	r := NewRunner(store, func(string, map[string]any) (any, error) {
		runs++
		return map[string]any{"ok": true}, nil
	}, "", time.Minute)

	// Pretend the previous tick was five minutes ago.
	r.lastMinute = time.Now().UTC().Truncate(time.Minute).Add(-5 * time.Minute)
	r.tick()

	if runs != 1 {
		t.Fatalf("task ran %d times across a 5-minute catch-up, want 1", runs)
	}

	// The same minute must not fire again on the following tick.
	r.tick()
	if runs != 1 {
		t.Fatalf("task ran %d times after a repeat tick in the same minute, want 1", runs)
	}
}

func TestTruncateUTF8KeepsContentWithInvalidBytes(t *testing.T) {
	// Binary-ish payloads used to come back empty: the old loop trimmed until
	// the whole prefix validated, and an invalid byte early on ate everything.
	in := "result: \xff\xfe ok"
	got := truncateUTF8(in, 100)
	if got == "" {
		t.Fatal("truncateUTF8 dropped the entire payload")
	}
	if !containsAll(got, "result:", "ok") {
		t.Fatalf("truncateUTF8(%q) = %q, want the readable text preserved", in, got)
	}
	if !isValidUTF8(got) {
		t.Fatalf("truncateUTF8 returned invalid UTF-8: %q", got)
	}
}

func TestTruncateUTF8DoesNotSplitRunes(t *testing.T) {
	in := "日本語テキスト"
	got := truncateUTF8(in, 7) // 7 bytes = 2 full runes + 1 partial
	if !isValidUTF8(got) {
		t.Fatalf("truncateUTF8 split a rune: %q", got)
	}
	if len(got) > 7 {
		t.Fatalf("truncateUTF8 returned %d bytes, want <= 7", len(got))
	}
	if got != "日本" {
		t.Fatalf("truncateUTF8 = %q, want %q", got, "日本")
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == 0xFFFD {
			return false
		}
	}
	return true
}
