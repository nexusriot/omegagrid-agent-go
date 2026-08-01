package builtin

import "testing"

// LLMs routinely send the wrong JSON type for a parameter. Substituting the
// default silently made skills operate on data nobody asked for: port_scan fell
// back to its stock port list, ping_check dialled port 80.
func TestStrCoercesScalars(t *testing.T) {
	cases := []struct {
		name string
		val  any
		want string
	}{
		{"string", "443", "443"},
		{"float_integral", float64(443), "443"},
		{"float_fractional", 4.5, "4.5"},
		{"int", 443, "443"},
		{"int64", int64(443), "443"},
		{"bool", true, "true"},
		{"nil", nil, ""},
		{"object", map[string]any{"a": 1}, ""},
		{"array", []any{1, 2}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := str(map[string]any{"k": c.val}, "k"); got != c.want {
				t.Fatalf("str(%v) = %q, want %q", c.val, got, c.want)
			}
		})
	}
	if got := str(map[string]any{}, "missing"); got != "" {
		t.Fatalf("str(missing key) = %q, want empty", got)
	}
}

func TestIntOrCoercesStrings(t *testing.T) {
	if got := intOr(map[string]any{"port": "8443"}, "port", 80); got != 8443 {
		t.Fatalf("intOr(\"8443\") = %d, want 8443", got)
	}
	if got := intOr(map[string]any{"port": " 22 "}, "port", 80); got != 22 {
		t.Fatalf("intOr(\" 22 \") = %d, want 22", got)
	}
	if got := intOr(map[string]any{"port": float64(443)}, "port", 80); got != 443 {
		t.Fatalf("intOr(443.0) = %d, want 443", got)
	}
	if got := intOr(map[string]any{"port": "not a number"}, "port", 80); got != 80 {
		t.Fatalf("intOr(garbage) = %d, want the default 80", got)
	}
	if got := intOr(map[string]any{}, "port", 80); got != 80 {
		t.Fatalf("intOr(missing) = %d, want the default 80", got)
	}
}

func TestFloatOrCoercesStrings(t *testing.T) {
	if got := floatOr(map[string]any{"timeout": "2.5"}, "timeout", 30); got != 2.5 {
		t.Fatalf("floatOr(\"2.5\") = %v, want 2.5", got)
	}
	if got := floatOr(map[string]any{"timeout": 7}, "timeout", 30); got != 7 {
		t.Fatalf("floatOr(7) = %v, want 7", got)
	}
	if got := floatOr(map[string]any{"timeout": true}, "timeout", 30); got != 30 {
		t.Fatalf("floatOr(bool) = %v, want the default 30", got)
	}
}

func TestBoolOrCoercesWords(t *testing.T) {
	truthy := []any{true, "true", "TRUE", " yes ", "1", "on", float64(1), 3}
	for _, v := range truthy {
		if !boolOr(map[string]any{"k": v}, "k", false) {
			t.Fatalf("boolOr(%v) = false, want true", v)
		}
	}
	falsy := []any{false, "false", "No", "0", "off", float64(0), 0}
	for _, v := range falsy {
		if boolOr(map[string]any{"k": v}, "k", true) {
			t.Fatalf("boolOr(%v) = true, want false", v)
		}
	}
	// Unrecognised values keep the default rather than silently reading false.
	if !boolOr(map[string]any{"k": "maybe"}, "k", true) {
		t.Fatal("boolOr(garbage) discarded the default")
	}
	if !boolOr(map[string]any{}, "k", true) {
		t.Fatal("boolOr(missing) discarded the default")
	}
}

// End-to-end: a string port must actually reach the scan, not the default list.
func TestPortScanAcceptsStringPorts(t *testing.T) {
	res, err := PortScan()(map[string]any{
		"host":    "127.0.0.1",
		"ports":   "1",
		"timeout": "0.05",
	})
	if err != nil {
		t.Fatalf("PortScan: %v", err)
	}
	m := res.(map[string]any)
	if got := m["scanned_count"]; got != 1 {
		t.Fatalf("scanned_count = %v, want 1 (string timeout must not reset the port list)", got)
	}
}
