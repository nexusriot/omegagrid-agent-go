package builtin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTodoLifecycle(t *testing.T) {
	exec := TodoContext(StateDB{Path: filepath.Join(t.TempDir(), "s.db")})
	a := run(t, exec, map[string]any{"action": "add", "text": "renew TLS cert", "due": "2000-01-01", "priority": "high", "tags": []any{"#ops"}})
	item := a["added"].(map[string]any)
	if item["overdue"] != true || item["priority"] != "high" {
		t.Fatalf("add: %v", a)
	}
	run(t, exec, map[string]any{"action": "add", "text": "buy milk", "tags": "home"})
	run(t, exec, map[string]any{"action": "add", "text": "100% done_today", "due": "+3d"})

	l := run(t, exec, map[string]any{"action": "list"})
	items := l["items"].([]map[string]any)
	if l["count"] != 3 || items[0]["text"] != "renew TLS cert" || l["overdue_total"] != 1 {
		t.Fatalf("list order: %v", l)
	}
	if l := run(t, exec, map[string]any{"action": "list", "tags": "ops"}); l["count"] != 1 {
		t.Fatalf("tag filter: %v", l)
	}
	// LIKE wildcards in the query are literal.
	if l := run(t, exec, map[string]any{"action": "list", "query": "100%"}); l["count"] != 1 {
		t.Fatalf("query: %v", l)
	}
	if l := run(t, exec, map[string]any{"action": "list", "query": "_"}); l["count"] != 1 {
		t.Fatalf("underscore query: %v", l)
	}
	id := item["id"]
	if d := run(t, exec, map[string]any{"action": "done", "id": id}); d["item"].(map[string]any)["done"] != true {
		t.Fatalf("done: %v", d)
	}
	if l := run(t, exec, map[string]any{"action": "list"}); l["count"] != 2 {
		t.Fatalf("open after done: %v", l)
	}
	u := run(t, exec, map[string]any{"action": "update", "id": id, "due": "none", "text": "renew cert"})
	if _, has := u["item"].(map[string]any)["due"]; has {
		t.Fatalf("due not cleared: %v", u)
	}
	if c := run(t, exec, map[string]any{"action": "clear_done"}); c["deleted"] != int64(1) {
		t.Fatalf("clear_done: %v", c)
	}
	if e := run(t, exec, map[string]any{"action": "done", "id": 999}); e["error"] == nil {
		t.Fatal("missing id accepted")
	}
	if e := run(t, exec, map[string]any{"action": "add", "text": "x", "due": "someday"}); e["error"] == nil {
		t.Fatal("bad due accepted")
	}
}

const testICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nX-WR-CALNAME:Work\r\n" +
	// Weekly standup Mon+Wed 09:00 Berlin, one Wednesday excluded.
	"BEGIN:VEVENT\r\nUID:standup\r\nSUMMARY:Standup\r\nDTSTART;TZID=Europe/Berlin:20261102T090000\r\n" +
	"DTEND;TZID=Europe/Berlin:20261102T091500\r\nRRULE:FREQ=WEEKLY;BYDAY=MO,WE\r\n" +
	"EXDATE;TZID=Europe/Berlin:20261118T090000\r\nEND:VEVENT\r\n" +
	// The Monday 16th instance moved to 10:00.
	"BEGIN:VEVENT\r\nUID:standup\r\nRECURRENCE-ID;TZID=Europe/Berlin:20261116T090000\r\nSUMMARY:Standup (moved)\r\n" +
	"DTSTART;TZID=Europe/Berlin:20261116T100000\r\nDTEND;TZID=Europe/Berlin:20261116T101500\r\nEND:VEVENT\r\n" +
	// All-day holiday with a folded description.
	"BEGIN:VEVENT\r\nUID:h\r\nSUMMARY:Holiday\r\nDTSTART;VALUE=DATE:20261119\r\nDTEND;VALUE=DATE:20261120\r\n" +
	"DESCRIPTION:Office closed\\, enjoy\r\n  the day\r\nBEGIN:VALARM\r\nSUMMARY:alarm-not-event\r\nEND:VALARM\r\nEND:VEVENT\r\n" +
	// Monthly on the last Friday, 3 times.
	"BEGIN:VEVENT\r\nUID:m\r\nSUMMARY:Retro\r\nDTSTART:20261030T150000Z\r\nDURATION:PT1H\r\nRRULE:FREQ=MONTHLY;BYDAY=-1FR;COUNT=3\r\nEND:VEVENT\r\n" +
	// Cancelled.
	"BEGIN:VEVENT\r\nUID:c\r\nSUMMARY:Cancelled thing\r\nDTSTART:20261117T120000Z\r\nSTATUS:CANCELLED\r\nEND:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

func TestCalendarIcsExpansion(t *testing.T) {
	cal, err := parseICS(testICS)
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	evs := cal.expand(from, from.AddDate(0, 0, 14), time.UTC)
	var got []string
	for _, e := range evs {
		got = append(got, e.Start.UTC().Format("01-02 15:04")+" "+e.Summary)
	}
	want := []string{
		"11-16 09:00 Standup (moved)", // 10:00 Berlin = 09:00 UTC
		"11-19 00:00 Holiday",
		"11-23 08:00 Standup",
		"11-25 08:00 Standup",
		"11-27 15:00 Retro",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !strings.Contains(evs[1].Description, "closed, enjoy the day") {
		t.Fatalf("unfold/unescape: %q", evs[1].Description)
	}
	// COUNT=3 from October: Oct 30, Nov 27, Dec 25 — none in January.
	jan := cal.expand(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC), time.UTC)
	for _, e := range jan {
		if e.Summary == "Retro" {
			t.Fatal("COUNT not honoured")
		}
	}
}

func TestCalendarIcsSkillFromWorkspace(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "work.ics"), []byte(testICS), 0o644); err != nil {
		t.Fatal(err)
	}
	exec := CalendarIcsContext(CalendarConfig{Workspace: WorkspaceConfig{Dir: dir}}, 5)
	m := run(t, exec, map[string]any{"source": "work.ics", "from": "2026-11-16", "days": 10, "timezone": "Europe/Berlin", "query": "standup"})
	// Mon 16 (moved), Wed 18 excluded by EXDATE, Mon 23, Wed 25.
	if m["count"] != 3 || m["calendar_name"] != "Work" {
		t.Fatalf("%v", m)
	}
	ev := m["events"].([]map[string]any)[0]
	if ev["start"] != "2026-11-16T10:00:00+01:00" {
		t.Fatalf("start %v", ev["start"])
	}
	if m := run(t, exec, map[string]any{}); m["error"] == nil {
		t.Fatal("no source accepted without configured calendars")
	}
	if m := run(t, exec, map[string]any{"source": "../etc/passwd"}); m["error"] == nil {
		t.Fatal("path escape")
	}
	_ = context.Background
}
