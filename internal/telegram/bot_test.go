package telegram

import (
	"strings"
	"sync"
	"testing"
)

func TestSplitForTelegram(t *testing.T) {
	t.Run("short text is one chunk", func(t *testing.T) {
		got := splitForTelegram("hello")
		if len(got) != 1 || got[0] != "hello" {
			t.Fatalf("got %#v", got)
		}
	})

	t.Run("empty text yields no chunks", func(t *testing.T) {
		if got := splitForTelegram(""); got != nil {
			t.Fatalf("expected nil, got %#v", got)
		}
	})

	t.Run("every chunk fits the limit and round-trips", func(t *testing.T) {
		// Many short lines that together exceed the limit.
		var sb strings.Builder
		for i := 0; i < 2000; i++ {
			sb.WriteString("line of text\n")
		}
		in := sb.String()
		chunks := splitForTelegram(in)
		if len(chunks) < 2 {
			t.Fatalf("expected multiple chunks, got %d", len(chunks))
		}
		for i, c := range chunks {
			if n := len([]rune(c)); n > telegramMaxLen {
				t.Fatalf("chunk %d has %d runes, exceeds limit %d", i, n, telegramMaxLen)
			}
		}
		if joined := strings.Join(chunks, "\n"); joined != in {
			t.Fatalf("round-trip mismatch: len(in)=%d len(joined)=%d", len(in), len(joined))
		}
	})

	t.Run("a single oversized line is hard-split by runes", func(t *testing.T) {
		// Multi-byte runes ensure we never split mid-rune.
		long := strings.Repeat("é", telegramMaxLen+500)
		chunks := splitForTelegram(long)
		if len(chunks) < 2 {
			t.Fatalf("expected multiple chunks, got %d", len(chunks))
		}
		total := 0
		for i, c := range chunks {
			rs := []rune(c)
			if len(rs) > telegramMaxLen {
				t.Fatalf("chunk %d has %d runes, exceeds limit", i, len(rs))
			}
			total += len(rs)
		}
		if total != telegramMaxLen+500 {
			t.Fatalf("lost runes: got %d want %d", total, telegramMaxLen+500)
		}
	})
}

func TestRenderStatus(t *testing.T) {
	got := renderStatus([]Event{
		{Event: "thinking", Step: 1},
		{Event: "tool_call", Step: 1, Tool: "weather", Why: "need the forecast"},
		{Event: "tool_result", Step: 1, Tool: "weather", ElapsedS: 0.42},
		{Event: "thinking", Step: 2},
	})
	for _, want := range []string{
		"Thinking (step 1)", "Calling weather", "need the forecast",
		"weather done (0.4s)", "Thinking (step 2)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("status is missing %q:\n%s", want, got)
		}
	}
	if lines := strings.Count(got, "\n") + 1; lines != 4 {
		t.Fatalf("status has %d lines, want 4:\n%s", lines, got)
	}
}

func TestRenderStatusToolCallWithoutReason(t *testing.T) {
	got := renderStatus([]Event{{Event: "tool_call", Tool: "ping_check"}})
	if !strings.Contains(got, "Calling ping_check") {
		t.Fatalf("status = %q", got)
	}
	// No reason means no dangling separator.
	if strings.Contains(got, "—") {
		t.Fatalf("status has an empty reason separator: %q", got)
	}
}

func TestRenderStatusEmpty(t *testing.T) {
	if got := renderStatus(nil); got != "Processing..." {
		t.Fatalf("renderStatus(nil) = %q, want %q", got, "Processing...")
	}
	// Unknown event kinds contribute no lines and must not blank the status.
	if got := renderStatus([]Event{{Event: "final", Answer: "x"}}); got != "Processing..." {
		t.Fatalf("renderStatus(final) = %q", got)
	}
}

// A long run accumulates more status lines than Telegram will accept in one
// message; every later edit would then fail and the status would look frozen.
func TestRenderStatusCapsLineCount(t *testing.T) {
	var evs []Event
	for i := 1; i <= 200; i++ {
		evs = append(evs, Event{Event: "thinking", Step: i})
	}
	got := renderStatus(evs)

	lines := strings.Split(got, "\n")
	if len(lines) > 26 {
		t.Fatalf("status kept %d lines, want at most 26", len(lines))
	}
	if lines[0] != "…" {
		t.Fatalf("truncated status does not start with an ellipsis: %q", lines[0])
	}
	// The most recent step must survive; the oldest must not.
	if !strings.Contains(got, "step 200") {
		t.Fatalf("status dropped the newest step:\n%s", got)
	}
	if strings.Contains(got, "step 1)") {
		t.Fatalf("status kept the oldest step:\n%s", got)
	}
	if len(got) > telegramMaxLen {
		t.Fatalf("status is %d chars, over the %d limit", len(got), telegramMaxLen)
	}
}

func TestSessionMapIsPerChat(t *testing.T) {
	b := &Bot{sessions: map[int64]int{}}

	if got := b.getSession(111); got != 0 {
		t.Fatalf("unknown chat session = %d, want 0 (new session)", got)
	}
	b.setSession(111, 42)
	b.setSession(222, 43)
	if got := b.getSession(111); got != 42 {
		t.Fatalf("chat 111 session = %d, want 42", got)
	}
	if got := b.getSession(222); got != 43 {
		t.Fatalf("chat 222 session = %d, want 43", got)
	}

	// /new and /start reset by storing 0, which must forget the chat entirely
	// rather than pin it to session zero.
	b.setSession(111, 0)
	if got := b.getSession(111); got != 0 {
		t.Fatalf("reset session = %d, want 0", got)
	}
	if _, present := b.sessions[111]; present {
		t.Fatal("resetting a session left an entry in the map")
	}
	if got := b.getSession(222); got != 43 {
		t.Fatalf("resetting chat 111 disturbed chat 222: %d", got)
	}
}

func TestSessionMapConcurrentAccess(t *testing.T) {
	// Each Telegram update is handled on its own goroutine, so the session map
	// is genuinely shared. Run with -race.
	b := &Bot{sessions: map[int64]int{}}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			chat := int64(i % 5)
			b.setSession(chat, i+1)
			_ = b.getSession(chat)
		}(i)
	}
	wg.Wait()
}
