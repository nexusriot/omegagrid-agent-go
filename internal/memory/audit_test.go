package memory

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMarshalAuditBlob_SmallValue(t *testing.T) {
	v := map[string]any{"key": "value"}
	out := marshalAuditBlob(v, 0) // 0 = no limit
	if !json.Valid([]byte(out)) {
		t.Fatalf("expected valid JSON, got: %s", out)
	}
	if !strings.Contains(out, "value") {
		t.Errorf("expected content in output, got: %s", out)
	}
}

func TestMarshalAuditBlob_TruncationProducesValidJSON(t *testing.T) {
	// Build a payload that is definitely larger than the limit.
	large := map[string]any{"data": strings.Repeat("abcdef", 200)}
	const limit = 64
	out := marshalAuditBlob(large, limit)

	if !json.Valid([]byte(out)) {
		t.Fatalf("truncated output is not valid JSON: %s", out)
	}

	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("cannot unmarshal truncated output: %v", err)
	}
	if trunc, _ := m["_truncated"].(bool); !trunc {
		t.Errorf("expected _truncated=true in output, got: %v", m)
	}
}

func TestMarshalAuditBlob_TruncationPreservesOriginalSize(t *testing.T) {
	payload := strings.Repeat("x", 500)
	data := map[string]any{"s": payload}
	const limit = 50
	out := marshalAuditBlob(data, limit)

	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	origSize, ok := m["_original_size"].(float64)
	if !ok {
		t.Fatalf("_original_size missing or wrong type: %v", m)
	}
	if int(origSize) == 0 {
		t.Errorf("_original_size should be non-zero")
	}
}

func TestMarshalAuditBlob_NoTruncationWhenFits(t *testing.T) {
	v := map[string]any{"tiny": "val"}
	out := marshalAuditBlob(v, 4096)
	if strings.Contains(out, "_truncated") {
		t.Errorf("unexpected _truncated in output: %s", out)
	}
}

func TestMarshalAuditBlob_StringValue(t *testing.T) {
	out := marshalAuditBlob("hello world", 0)
	if !json.Valid([]byte(out)) {
		t.Fatalf("not valid JSON: %s", out)
	}
}

func TestSafeTruncateUTF8_ShortString(t *testing.T) {
	s := "hello"
	got := safeTruncateUTF8(s, 100)
	if got != s {
		t.Errorf("want %q, got %q", s, got)
	}
}

func TestSafeTruncateUTF8_ASCIIExact(t *testing.T) {
	s := "abcdefgh"
	got := safeTruncateUTF8(s, 4)
	if got != "abcd" {
		t.Errorf("want %q, got %q", "abcd", got)
	}
}

func TestSafeTruncateUTF8_NeverSplitsRune(t *testing.T) {
	// Each Japanese kanji is 3 bytes in UTF-8.
	// With maxBytes=5 we can fit only 1 complete kanji (3 bytes).
	s := "日本語"
	got := safeTruncateUTF8(s, 5)
	if !utf8.ValidString(got) {
		t.Errorf("result is not valid UTF-8: %q", got)
	}
	if len(got) > 5 {
		t.Errorf("result length %d exceeds maxBytes 5", len(got))
	}
}

func TestSafeTruncateUTF8_EmptyString(t *testing.T) {
	got := safeTruncateUTF8("", 10)
	if got != "" {
		t.Errorf("want empty string, got %q", got)
	}
}

func TestUnmarshalAuditBlob_ValidJSON(t *testing.T) {
	s := `{"key":"value","num":42}`
	v := unmarshalAuditBlob(s)
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", v)
	}
	if m["key"] != "value" {
		t.Errorf("key mismatch: %v", m["key"])
	}
}

func TestUnmarshalAuditBlob_InvalidJSON_ReturnsRawString(t *testing.T) {
	s := "not valid json {{"
	v := unmarshalAuditBlob(s)
	if str, ok := v.(string); !ok || str != s {
		t.Errorf("expected raw string %q, got %v (%T)", s, v, v)
	}
}

func TestUnmarshalAuditBlob_Null(t *testing.T) {
	v := unmarshalAuditBlob("null")
	if v != nil {
		t.Errorf("expected nil for JSON null, got %v", v)
	}
}

func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	original := map[string]any{
		"skill":  "shell_command",
		"status": "ok",
		"count":  float64(7),
	}
	marshaled := marshalAuditBlob(original, 0)
	back := unmarshalAuditBlob(marshaled)

	m, ok := back.(map[string]any)
	if !ok {
		t.Fatalf("expected map after round-trip, got %T", back)
	}
	if m["skill"] != "shell_command" {
		t.Errorf("round-trip mismatch: %v", m)
	}
}
