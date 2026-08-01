package memory

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSafeTruncateUTF8KeepsPrefixWithInvalidBytes(t *testing.T) {
	// Trimming until the whole prefix validated returned "" for any payload
	// carrying an invalid byte, discarding the audit preview entirely.
	in := "head \xff\xfe tail padding padding"
	got := safeTruncateUTF8(in, 20)
	if got == "" {
		t.Fatal("safeTruncateUTF8 dropped the whole preview")
	}
	if !strings.HasPrefix(got, "head ") {
		t.Fatalf("safeTruncateUTF8(%q) = %q, want the leading text kept", in, got)
	}
	if len(got) > 20 {
		t.Fatalf("safeTruncateUTF8 returned %d bytes, want <= 20", len(got))
	}
}

func TestSafeTruncateUTF8NeverSplitsRunes(t *testing.T) {
	in := "日本語テキストです"
	for n := 1; n <= len(in); n++ {
		got := safeTruncateUTF8(in, n)
		if len(got) > n {
			t.Fatalf("safeTruncateUTF8(_, %d) = %d bytes", n, len(got))
		}
		if !utf8.ValidString(got) {
			t.Fatalf("safeTruncateUTF8(_, %d) = %q, invalid UTF-8", n, got)
		}
	}
}

func TestSafeTruncateUTF8NonPositive(t *testing.T) {
	if got := safeTruncateUTF8("abc", 0); got != "" {
		t.Fatalf("safeTruncateUTF8(_, 0) = %q, want empty", got)
	}
	if got := safeTruncateUTF8("abc", -1); got != "" {
		t.Fatalf("safeTruncateUTF8(_, -1) = %q, want empty", got)
	}
}

// An over-limit blob is stored as a valid JSON wrapper carrying a preview, so
// reading it back must not yield a raw string fallback.
func TestMarshalAuditBlobTruncationRoundTrips(t *testing.T) {
	big := map[string]any{"payload": strings.Repeat("日", 500)}
	stored := marshalAuditBlob(big, 64)

	back := unmarshalAuditBlob(stored)
	m, ok := back.(map[string]any)
	if !ok {
		t.Fatalf("truncated blob did not survive a JSON round trip: %T %v", back, back)
	}
	if m["_truncated"] != true {
		t.Fatalf("_truncated = %v, want true", m["_truncated"])
	}
	preview, _ := m["preview"].(string)
	if preview == "" {
		t.Fatal("truncated blob carries no preview")
	}
	if !utf8.ValidString(preview) {
		t.Fatalf("preview is not valid UTF-8: %q", preview)
	}
}

func TestMarshalAuditBlobPassesSmallPayloadsThrough(t *testing.T) {
	stored := marshalAuditBlob(map[string]any{"host": "example.com"}, 65536)
	m, ok := unmarshalAuditBlob(stored).(map[string]any)
	if !ok {
		t.Fatalf("small blob did not round trip: %v", stored)
	}
	if m["host"] != "example.com" {
		t.Fatalf("host = %v, want example.com", m["host"])
	}
}
