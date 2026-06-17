package telegram

import (
	"strings"
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
