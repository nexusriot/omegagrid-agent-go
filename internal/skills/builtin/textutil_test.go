package builtin

import (
	"strings"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 10, 31, 9, 30, 0, 0, time.UTC) // a Saturday

func tc(t *testing.T, args map[string]any) map[string]any {
	t.Helper()
	return runExec(t, timeConvertAt(func() time.Time { return fixedNow }), args)
}

func TestTimeConvert(t *testing.T) {
	m := tc(t, map[string]any{"time": "2026-11-03 15:00", "from_tz": "Asia/Tokyo", "to_tz": []any{"Europe/Berlin", "America/New_York", "UTC"}})
	r := m["results"].([]map[string]any)
	if r[0]["time"] != "2026-11-03 07:00:00" || r[1]["time"] != "2026-11-03 01:00:00" || r[2]["time"] != "2026-11-03 06:00:00" {
		t.Fatalf("convert = %v", r)
	}
	if r[1]["abbrev"] != "EST" || r[1]["offset"] != "-05:00" {
		t.Fatalf("DST already ended in New York on Nov 3: %v", r[1])
	}
	m = tc(t, map[string]any{"time": "3pm", "from_tz": "Europe/London", "to_tz": "UTC"})
	if got := m["results"].([]map[string]any)[0]["time"]; got != "2026-10-31 15:00:00" {
		t.Fatalf("3pm London on Oct 31 (GMT) = %v", got)
	}
	m = tc(t, map[string]any{"time": "12am"})
	if got := m["results"].([]map[string]any)[0]["time"]; got != "2026-10-31 00:00:00" {
		t.Fatalf("12am = %v", got)
	}
	m = tc(t, map[string]any{"action": "add", "time": "2026-10-30 10:00", "business_days": 1})
	if got := m["results"].([]map[string]any)[0]; got["weekday"] != "Monday" || got["time"] != "2026-11-02 10:00:00" {
		t.Fatalf("Friday + 1 business day = %v", got)
	}
	m = tc(t, map[string]any{"action": "add", "time": "2026-11-02 10:00", "business_days": -1})
	if got := m["results"].([]map[string]any)[0]["weekday"]; got != "Friday" {
		t.Fatalf("Monday - 1 business day = %v", got)
	}
	m = tc(t, map[string]any{"action": "add", "time": "2026-10-31T00:00:00Z", "duration": "1w2d3h"})
	if got := m["results"].([]map[string]any)[0]["time"]; got != "2026-11-09 03:00:00" {
		t.Fatalf("1w2d3h = %v", got)
	}
	m = tc(t, map[string]any{"action": "diff", "time": "2026-10-30", "end": "2026-11-06"})
	if m["days"] != 7.0 || m["business_days"] != 5 {
		t.Fatalf("diff = %v", m)
	}
	for _, bad := range []map[string]any{{"from_tz": "Mars/Base"}, {"time": "soonish"}, {"time": "13pm"}, {"action": "fly"}, {"action": "add", "duration": "5 parsecs"}} {
		if m := tc(t, bad); m["error"] == nil {
			t.Errorf("accepted %v: %v", bad, m)
		}
	}
}

func TestRegexTest(t *testing.T) {
	m := runExec(t, RegexTest(), map[string]any{"pattern": `(?P<user>\w+)@(\w+)\.com`, "text": "a@b.com, C@D.com", "replace": "${user} at $2"})
	matches := m["matches"].([]map[string]any)
	if m["match_count"] != 2 || matches[1]["match"] != "C@D.com" || matches[0]["groups"].(map[string]any)["user"] != "a" || matches[0]["groups"].(map[string]any)["2"] != "b" {
		t.Fatalf("regex = %v", m)
	}
	if m["replaced"] != "a at b, C at D" {
		t.Fatalf("replaced = %v", m["replaced"])
	}
	if m := runExec(t, RegexTest(), map[string]any{"pattern": "^b", "text": "a\nb", "flags": "m"}); m["match_count"] != 1 {
		t.Fatalf("m flag = %v", m)
	}
	if m := runExec(t, RegexTest(), map[string]any{"pattern": "(a", "text": "a"}); m["valid"] != false {
		t.Fatalf("bad pattern = %v", m)
	}
	if m := runExec(t, RegexTest(), map[string]any{"pattern": `(\w+)\1`, "text": "aa"}); m["valid"] != false || !strings.Contains(m["error"].(string), "escape") {
		t.Fatalf("backreference (not RE2) = %v", m)
	}
	if m := runExec(t, RegexTest(), map[string]any{"pattern": "a", "text": "a", "flags": "x"}); m["error"] == nil {
		t.Fatal("unknown flag accepted")
	}
}

func TestTextDiff(t *testing.T) {
	a := "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n"
	b := "one\nTWO\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\neleven\n"
	m := runExec(t, TextDiff(), map[string]any{"a": a, "b": b, "context": 1})
	d := m["diff"].(string)
	if m["added"] != 2 || m["removed"] != 1 {
		t.Fatalf("counts = %v", m)
	}
	want := "--- a\n+++ b\n@@ -1,3 +1,3 @@\n one\n-two\n+TWO\n three\n@@ -10,1 +10,2 @@\n ten\n+eleven\n"
	if d != want {
		t.Fatalf("diff =\n%s\nwant\n%s", d, want)
	}
	if m := runExec(t, TextDiff(), map[string]any{"a": "x\n", "b": "x"}); m["identical"] != true {
		t.Fatalf("trailing newline difference = %v", m)
	}
	if m := runExec(t, TextDiff(), map[string]any{"a": strings.Repeat("x\n", maxDiffLines+1), "b": ""}); m["error"] == nil {
		t.Fatal("oversized input accepted")
	}
}

func TestUnitConvert(t *testing.T) {
	cases := []struct {
		v        float64
		from, to string
		want     float64
	}{
		{1, "mi", "km", 1.609344},
		{100, "C", "F", 212},
		{-40, "F", "c", -40},
		{0, "K", "C", -273.15},
		{1, "GiB", "MB", 1073.741824},
		{100, "Mbps", "MB/s", 12.5},
		{1, "MB", "Mbit", 8},
		{1, "pounds", "kg", 0.45359237},
		{60, "mph", "km/h", 96.56064},
		{1, "kWh", "kJ", 3600},
		{1, "atm", "psi", 14.6959487755},
	}
	for _, c := range cases {
		m := runExec(t, UnitConvert(), map[string]any{"value": c.v, "from": c.from, "to": c.to})
		got, _ := m["result"].(float64)
		if diff := got - c.want; diff > 1e-6 || diff < -1e-6 {
			t.Errorf("%v %s → %s = %v, want %v (%v)", c.v, c.from, c.to, got, c.want, m)
		}
	}
	for _, bad := range []map[string]any{
		{"value": 1, "from": "kg", "to": "m"}, {"value": 1, "from": "parsec", "to": "m"},
		{"value": -500, "from": "C", "to": "K"}, {"value": "x", "from": "m", "to": "km"}, {"value": 1, "from": "mb", "to": "MB"},
	} {
		if m := runExec(t, UnitConvert(), bad); m["error"] == nil {
			t.Errorf("accepted %v: %v", bad, m)
		}
	}
}
