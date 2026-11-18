package builtin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

func CalendarIcsSchema() Skill {
	return Skill{Name: "calendar_ics", Description: "Read events from an iCalendar (.ics) feed or file — Google/Outlook/iCloud 'secret address' URLs, " +
		"webcal:// links, a workspace file or a configured calendar name — expanding recurring events, for a time window (default the next 7 days).",
		Parameters: map[string]Param{
			"source":   {Type: "string", Description: "ICS URL (https/webcal), workspace path (*.ics), or a name from SKILL_CALENDARS; default: the first configured calendar", Required: false},
			"from":     {Type: "string", Description: "Window start: RFC3339, 2026-11-20, today, tomorrow (default now)", Required: false},
			"days":     {Type: "number", Description: "Window length in days (default 7, max 366)", Required: false},
			"query":    {Type: "string", Description: "Only events whose summary, location or description contains this", Required: false},
			"timezone": {Type: "string", Description: "IANA zone to show times in (default UTC)", Required: false},
			"limit":    {Type: "number", Description: "Max events (default 50, max 500)", Required: false},
		}}
}

// CalendarConfig names calendars (SKILL_CALENDARS="work=https://…,home=…")
// so secret feed URLs live in the environment, not in chat history.
type CalendarConfig struct {
	Named     map[string]string
	Order     []string // names in configuration order (first = default)
	Workspace WorkspaceConfig
}

// ParseNamedURLs reads "name=url,name2=url2".
func ParseNamedURLs(entries []string) (map[string]string, []string) {
	out := map[string]string{}
	var order []string
	for _, e := range entries {
		name, u, ok := strings.Cut(e, "=")
		name, u = strings.ToLower(strings.TrimSpace(name)), strings.TrimSpace(u)
		if !ok || name == "" || u == "" {
			continue
		}
		if _, dup := out[name]; !dup {
			order = append(order, name)
		}
		out[name] = u
	}
	return out, order
}

const maxICSBytes = 8 << 20

func CalendarIcsContext(c CalendarConfig, timeoutSec float64) ContextExecutor {
	return func(ctx context.Context, args map[string]any) (any, error) {
		loc := time.UTC
		if tz := strings.TrimSpace(str(args, "timezone")); tz != "" {
			l, err := time.LoadLocation(tz)
			if err != nil {
				return map[string]any{"error": "unknown timezone " + tz}, nil
			}
			loc = l
		}
		now := time.Now().In(loc)
		from := now
		if f := strings.TrimSpace(str(args, "from")); f != "" {
			switch strings.ToLower(f) {
			case "today":
				from = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
			case "tomorrow":
				from = time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, loc)
			default:
				t, err := time.Parse(time.RFC3339, f)
				if err != nil {
					if t, err = time.ParseInLocation("2006-01-02", f, loc); err != nil {
						return map[string]any{"error": "from must be RFC3339, YYYY-MM-DD, today or tomorrow"}, nil
					}
				}
				from = t
			}
		}
		days := intOr(args, "days", 7)
		if days <= 0 || days > 366 {
			days = 7
		}
		to := from.AddDate(0, 0, days)
		limit := intOr(args, "limit", 50)
		if limit <= 0 || limit > 500 {
			limit = 50
		}

		source := strings.TrimSpace(str(args, "source"))
		label := source
		if source == "" {
			if len(c.Order) == 0 {
				return map[string]any{"error": "source is required (no calendars configured in SKILL_CALENDARS)"}, nil
			}
			label = c.Order[0]
			source = c.Named[label]
		} else if u, ok := c.Named[strings.ToLower(source)]; ok {
			source = u
		}
		raw, err := c.load(ctx, source, timeoutSec)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		cal, err := parseICS(raw)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		evs := cal.expand(from, to, loc)
		if q := strings.ToLower(strings.TrimSpace(str(args, "query"))); q != "" {
			kept := evs[:0]
			for _, e := range evs {
				if strings.Contains(strings.ToLower(e.Summary+" "+e.Location+" "+e.Description), q) {
					kept = append(kept, e)
				}
			}
			evs = kept
		}
		total := len(evs)
		if len(evs) > limit {
			evs = evs[:limit]
		}
		out := make([]map[string]any, 0, len(evs))
		for _, e := range evs {
			m := map[string]any{"summary": e.Summary, "all_day": e.AllDay}
			if e.AllDay {
				m["start"] = e.Start.Format("2006-01-02")
				// DTEND of an all-day event is exclusive.
				if last := e.End.AddDate(0, 0, -1); last.After(e.Start) {
					m["end"] = last.Format("2006-01-02")
				}
			} else {
				m["start"] = e.Start.In(loc).Format(time.RFC3339)
				m["end"] = e.End.In(loc).Format(time.RFC3339)
			}
			if e.Location != "" {
				m["location"] = e.Location
			}
			if e.Description != "" {
				m["description"] = truncateStr(e.Description, 300)
			}
			if e.Recurring {
				m["recurring"] = true
			}
			out = append(out, m)
		}
		res := map[string]any{
			"calendar": label, "from": from.Format(time.RFC3339), "to": to.Format(time.RFC3339),
			"timezone": loc.String(), "count": len(out), "events": out,
		}
		if cal.Name != "" {
			res["calendar_name"] = cal.Name
		}
		if total > len(out) {
			res["truncated"] = total - len(out)
		}
		return res, nil
	}
}

func (c CalendarConfig) load(ctx context.Context, source string, timeoutSec float64) (string, error) {
	low := strings.ToLower(source)
	if strings.HasPrefix(low, "webcal://") {
		source = "https://" + source[len("webcal://"):]
		low = strings.ToLower(source)
	}
	if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("User-Agent", "OmegaGridAgent/1.0")
		resp, err := httpClient(timeoutSec).Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return "", fmt.Errorf("calendar feed returned HTTP %d", resp.StatusCode)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, maxICSBytes+1))
		if err != nil {
			return "", err
		}
		if len(b) > maxICSBytes {
			return "", fmt.Errorf("calendar is larger than %d MiB", maxICSBytes>>20)
		}
		return string(b), nil
	}
	root, err := c.Workspace.open()
	if err != nil {
		return "", err
	}
	defer root.Close()
	p, err := relPath(source)
	if err != nil {
		return "", err
	}
	f, err := root.Open(p)
	if err != nil {
		return "", fmt.Errorf("source is neither a URL, a configured calendar nor a workspace file: %v", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxICSBytes+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxICSBytes {
		return "", fmt.Errorf("calendar is larger than %d MiB", maxICSBytes>>20)
	}
	return string(b), nil
}

// ─── iCalendar parsing ───────────────────────────────────────────────────────

type icsProp struct {
	Name   string
	Params map[string]string
	Value  string
}

type icsEvent struct {
	UID          string
	Summary      string
	Location     string
	Description  string
	Start, End   time.Time
	AllDay       bool
	Duration     time.Duration
	RRule        map[string]string
	ExDates      map[int64]bool
	RecurrenceID *time.Time
	Cancelled    bool
	Recurring    bool
}

type icsCalendar struct {
	Name   string
	Events []*icsEvent
}

// unfoldICS joins continuation lines (RFC 5545 §3.1).
func unfoldICS(raw string) []string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	var out []string
	for _, l := range strings.Split(raw, "\n") {
		if (strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t")) && len(out) > 0 {
			out[len(out)-1] += l[1:]
			continue
		}
		out = append(out, l)
	}
	return out
}

func parseProp(line string) (icsProp, bool) {
	// The value starts at the first ':' outside a quoted parameter value.
	inQ := false
	colon := -1
	for i, r := range line {
		if r == '"' {
			inQ = !inQ
		} else if r == ':' && !inQ {
			colon = i
			break
		}
	}
	if colon < 0 {
		return icsProp{}, false
	}
	head, val := line[:colon], line[colon+1:]
	parts := strings.Split(head, ";")
	p := icsProp{Name: strings.ToUpper(strings.TrimSpace(parts[0])), Params: map[string]string{}, Value: val}
	for _, kv := range parts[1:] {
		k, v, _ := strings.Cut(kv, "=")
		p.Params[strings.ToUpper(k)] = strings.Trim(v, `"`)
	}
	return p, true
}

var icsUnescape = strings.NewReplacer(`\n`, "\n", `\N`, "\n", `\,`, ",", `\;`, ";", `\\`, `\`)

func parseICS(raw string) (*icsCalendar, error) {
	if !strings.Contains(strings.ToUpper(raw[:min(len(raw), 4096)]), "BEGIN:VCALENDAR") {
		return nil, fmt.Errorf("not an iCalendar file (no BEGIN:VCALENDAR)")
	}
	cal := &icsCalendar{}
	var cur *icsEvent
	depth := 0 // nesting inside VEVENT (VALARM etc.)
	for _, line := range unfoldICS(raw) {
		p, ok := parseProp(line)
		if !ok {
			continue
		}
		switch p.Name {
		case "BEGIN":
			if strings.EqualFold(p.Value, "VEVENT") && cur == nil {
				cur = &icsEvent{ExDates: map[int64]bool{}}
			} else if cur != nil {
				depth++
			}
			continue
		case "END":
			if cur != nil && depth > 0 {
				depth--
			} else if cur != nil && strings.EqualFold(p.Value, "VEVENT") {
				if !cur.Start.IsZero() {
					cal.Events = append(cal.Events, cur)
				}
				cur = nil
			}
			continue
		case "X-WR-CALNAME":
			if cur == nil {
				cal.Name = icsUnescape.Replace(p.Value)
			}
			continue
		}
		if cur == nil || depth > 0 {
			continue
		}
		switch p.Name {
		case "UID":
			cur.UID = p.Value
		case "SUMMARY":
			cur.Summary = icsUnescape.Replace(p.Value)
		case "LOCATION":
			cur.Location = icsUnescape.Replace(p.Value)
		case "DESCRIPTION":
			cur.Description = icsUnescape.Replace(p.Value)
		case "STATUS":
			cur.Cancelled = strings.EqualFold(p.Value, "CANCELLED")
		case "DTSTART":
			t, allDay, err := parseICSTime(p)
			if err == nil {
				cur.Start, cur.AllDay = t, allDay
			}
		case "DTEND":
			if t, _, err := parseICSTime(p); err == nil {
				cur.End = t
			}
		case "DURATION":
			cur.Duration = parseICSDuration(p.Value)
		case "RRULE":
			cur.RRule = map[string]string{}
			for _, kv := range strings.Split(p.Value, ";") {
				k, v, _ := strings.Cut(kv, "=")
				cur.RRule[strings.ToUpper(k)] = strings.ToUpper(v)
			}
		case "EXDATE":
			for _, v := range strings.Split(p.Value, ",") {
				q := p
				q.Value = v
				if t, _, err := parseICSTime(q); err == nil {
					cur.ExDates[t.Unix()] = true
				}
			}
		case "RECURRENCE-ID":
			if t, _, err := parseICSTime(p); err == nil {
				cur.RecurrenceID = &t
			}
		}
	}
	return cal, nil
}

// parseICSTime reads DATE / DATE-TIME values: UTC ("Z"), with TZID, or
// floating (treated as UTC). An unknown TZID (Outlook's Windows zone names)
// also falls back to UTC rather than dropping the event.
func parseICSTime(p icsProp) (time.Time, bool, error) {
	v := strings.TrimSpace(p.Value)
	if strings.EqualFold(p.Params["VALUE"], "DATE") || len(v) == 8 {
		t, err := time.ParseInLocation("20060102", v, time.UTC)
		return t, true, err
	}
	if strings.HasSuffix(v, "Z") {
		t, err := time.Parse("20060102T150405Z", v)
		return t, false, err
	}
	loc := time.UTC
	if tz := p.Params["TZID"]; tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		} else if l, ok := windowsZones[tz]; ok {
			if l2, err := time.LoadLocation(l); err == nil {
				loc = l2
			}
		}
	}
	t, err := time.ParseInLocation("20060102T150405", v, loc)
	return t, false, err
}

// windowsZones maps the Outlook zone names seen most often to IANA.
var windowsZones = map[string]string{
	"W. Europe Standard Time": "Europe/Berlin", "Central Europe Standard Time": "Europe/Budapest",
	"Romance Standard Time": "Europe/Paris", "GMT Standard Time": "Europe/London",
	"E. Europe Standard Time": "Europe/Chisinau", "FLE Standard Time": "Europe/Kiev",
	"Russian Standard Time": "Europe/Moscow", "Arabian Standard Time": "Asia/Dubai",
	"Eastern Standard Time": "America/New_York", "Central Standard Time": "America/Chicago",
	"Mountain Standard Time": "America/Denver", "Pacific Standard Time": "America/Los_Angeles",
	"India Standard Time": "Asia/Kolkata", "China Standard Time": "Asia/Shanghai",
	"Tokyo Standard Time": "Asia/Tokyo", "AUS Eastern Standard Time": "Australia/Sydney",
	"UTC": "UTC", "Coordinated Universal Time": "UTC",
}

// parseICSDuration reads P[n]W / P[n]DT[n]H[n]M[n]S.
func parseICSDuration(v string) time.Duration {
	v = strings.ToUpper(strings.TrimSpace(v))
	neg := strings.HasPrefix(v, "-")
	v = strings.TrimLeft(v, "+-")
	v = strings.TrimPrefix(v, "P")
	var d time.Duration
	num := ""
	inTime := false
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
			num += string(r)
		case r == 'T':
			inTime = true
		default:
			n, _ := strconv.Atoi(num)
			num = ""
			switch {
			case r == 'W':
				d += time.Duration(n) * 7 * 24 * time.Hour
			case r == 'D':
				d += time.Duration(n) * 24 * time.Hour
			case r == 'H' && inTime:
				d += time.Duration(n) * time.Hour
			case r == 'M' && inTime:
				d += time.Duration(n) * time.Minute
			case r == 'S' && inTime:
				d += time.Duration(n) * time.Second
			}
		}
	}
	if neg {
		d = -d
	}
	return d
}

type calEvent struct {
	Summary, Location, Description string
	Start, End                     time.Time
	AllDay, Recurring              bool
}

// maxOccurrenceScan bounds RRULE expansion per event.
const maxOccurrenceScan = 20000

// expand returns the event instances overlapping [from, to), sorted.
func (cal *icsCalendar) expand(from, to time.Time, loc *time.Location) []calEvent {
	// Overrides (RECURRENCE-ID) replace one instance of their series.
	overrides := map[string]bool{}
	for _, e := range cal.Events {
		if e.RecurrenceID != nil {
			overrides[e.UID+"\x00"+strconv.FormatInt(e.RecurrenceID.Unix(), 10)] = true
		}
	}
	var out []calEvent
	add := func(e *icsEvent, start time.Time, recurring bool) {
		dur := e.Duration
		if !e.End.IsZero() {
			dur = e.End.Sub(e.Start)
		}
		if dur <= 0 {
			if e.AllDay {
				dur = 24 * time.Hour
			} else {
				dur = 0
			}
		}
		end := start.Add(dur)
		if e.AllDay {
			// All-day dates are calendar days in the viewer's zone.
			start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc)
			end = start.Add(dur)
		}
		if !end.After(from) && !(dur == 0 && !start.Before(from)) || !start.Before(to) {
			return
		}
		out = append(out, calEvent{Summary: e.Summary, Location: e.Location, Description: e.Description,
			Start: start, End: end, AllDay: e.AllDay, Recurring: recurring})
	}
	for _, e := range cal.Events {
		if e.Cancelled {
			continue
		}
		if e.RRule == nil || e.RecurrenceID != nil {
			add(e, e.Start, e.RecurrenceID != nil)
			continue
		}
		for _, occ := range rruleOccurrences(e, to) {
			if e.ExDates[occ.Unix()] || overrides[e.UID+"\x00"+strconv.FormatInt(occ.Unix(), 10)] {
				continue
			}
			add(e, occ, true)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

var icsWeekdays = map[string]time.Weekday{"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday,
	"WE": time.Wednesday, "TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday}

type byDay struct {
	n  int // 0 = every; 1 = first; -1 = last
	wd time.Weekday
}

func parseByDay(s string) []byDay {
	var out []byDay
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if len(part) < 2 {
			continue
		}
		wd, ok := icsWeekdays[part[len(part)-2:]]
		if !ok {
			continue
		}
		n, _ := strconv.Atoi(part[:len(part)-2])
		out = append(out, byDay{n, wd})
	}
	return out
}

// rruleOccurrences lists the series' starts from DTSTART up to (not
// including) until, honouring FREQ, INTERVAL, COUNT, UNTIL, BYDAY,
// BYMONTHDAY and BYMONTH (the subset calendar apps emit for ordinary
// repeating events).
func rruleOccurrences(e *icsEvent, until time.Time) []time.Time {
	r := e.RRule
	start := e.Start
	interval, _ := strconv.Atoi(r["INTERVAL"])
	if interval <= 0 {
		interval = 1
	}
	count, _ := strconv.Atoi(r["COUNT"])
	if u := r["UNTIL"]; u != "" {
		if t, _, err := parseICSTime(icsProp{Value: u, Params: map[string]string{}}); err == nil {
			if len(u) == 8 {
				t = t.Add(24*time.Hour - time.Second)
			}
			if t.Before(until) {
				until = t.Add(time.Second)
			}
		}
	}
	days := parseByDay(r["BYDAY"])
	var monthDays []int
	for _, s := range strings.Split(r["BYMONTHDAY"], ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n != 0 {
			monthDays = append(monthDays, n)
		}
	}
	var months []time.Month
	for _, s := range strings.Split(r["BYMONTH"], ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n >= 1 && n <= 12 {
			months = append(months, time.Month(n))
		}
	}
	at := func(y int, m time.Month, d int) time.Time {
		return time.Date(y, m, d, start.Hour(), start.Minute(), start.Second(), 0, start.Location())
	}

	var out []time.Time
	emitted := 0
	emit := func(t time.Time) bool { // false = stop
		if t.Before(start) {
			return true
		}
		if !t.Before(until) {
			return false
		}
		emitted++
		if count > 0 && emitted > count {
			return false
		}
		out = append(out, t)
		return true
	}

	switch r["FREQ"] {
	case "DAILY":
		for i := 0; i < maxOccurrenceScan; i++ {
			t := start.AddDate(0, 0, i*interval)
			if len(days) > 0 && !weekdayIn(t.Weekday(), days) {
				if !t.Before(until) {
					break
				}
				continue
			}
			if !emit(t) {
				break
			}
		}
	case "WEEKLY":
		wds := []time.Weekday{start.Weekday()}
		if len(days) > 0 {
			wds = wds[:0]
			for _, d := range days {
				wds = append(wds, d.wd)
			}
		}
		// Weeks start on Monday (WKST default).
		off := (int(start.Weekday()) + 6) % 7
		weekStart := at(start.Year(), start.Month(), start.Day()-off)
		sort.Slice(wds, func(i, j int) bool { return (int(wds[i])+6)%7 < (int(wds[j])+6)%7 })
	weekly:
		for w := 0; w < maxOccurrenceScan; w++ {
			base := weekStart.AddDate(0, 0, 7*w*interval)
			if base.After(until) {
				break
			}
			for _, wd := range wds {
				if !emit(base.AddDate(0, 0, (int(wd)+6)%7)) {
					break weekly
				}
			}
		}
	case "MONTHLY":
	monthly:
		for m := 0; m < maxOccurrenceScan/10; m++ {
			first := time.Date(start.Year(), start.Month()+time.Month(m*interval), 1, 0, 0, 0, 0, start.Location())
			if first.After(until) {
				break
			}
			for _, t := range monthInstances(first, days, monthDays, start.Day(), at) {
				if !emit(t) {
					break monthly
				}
			}
		}
	case "YEARLY":
		if len(months) == 0 {
			months = []time.Month{start.Month()}
		}
	yearly:
		for y := 0; y < 500; y++ {
			year := start.Year() + y*interval
			if year > until.Year() {
				break
			}
			for _, mo := range months {
				first := time.Date(year, mo, 1, 0, 0, 0, 0, start.Location())
				md := monthDays
				if len(days) == 0 && len(md) == 0 {
					md = []int{start.Day()}
				}
				for _, t := range monthInstances(first, days, md, start.Day(), at) {
					if !emit(t) {
						break yearly
					}
				}
			}
		}
	default:
		emit(start)
	}
	return out
}

func weekdayIn(wd time.Weekday, days []byDay) bool {
	for _, d := range days {
		if d.wd == wd {
			return true
		}
	}
	return false
}

// monthInstances lists the matching days of the month that begins at first,
// in order: BYDAY (with optional ordinal), else BYMONTHDAY, else the series'
// own day of month (months without that day are skipped, as RFC 5545 says).
func monthInstances(first time.Time, days []byDay, monthDays []int, defDay int, at func(int, time.Month, int) time.Time) []time.Time {
	y, m := first.Year(), first.Month()
	last := time.Date(y, m+1, 0, 0, 0, 0, 0, first.Location()).Day()
	var dayNums []int
	switch {
	case len(days) > 0:
		for _, bd := range days {
			var matches []int
			for d := 1; d <= last; d++ {
				if time.Date(y, m, d, 0, 0, 0, 0, first.Location()).Weekday() == bd.wd {
					matches = append(matches, d)
				}
			}
			switch {
			case bd.n == 0:
				dayNums = append(dayNums, matches...)
			case bd.n > 0 && bd.n <= len(matches):
				dayNums = append(dayNums, matches[bd.n-1])
			case bd.n < 0 && -bd.n <= len(matches):
				dayNums = append(dayNums, matches[len(matches)+bd.n])
			}
		}
		if len(monthDays) > 0 { // BYDAY ∩ BYMONTHDAY
			keep := dayNums[:0]
			for _, d := range dayNums {
				for _, md := range monthDays {
					if md == d || (md < 0 && last+md+1 == d) {
						keep = append(keep, d)
					}
				}
			}
			dayNums = keep
		}
	case len(monthDays) > 0:
		for _, md := range monthDays {
			d := md
			if md < 0 {
				d = last + md + 1
			}
			if d >= 1 && d <= last {
				dayNums = append(dayNums, d)
			}
		}
	default:
		if defDay <= last {
			dayNums = []int{defDay}
		}
	}
	sort.Ints(dayNums)
	out := make([]time.Time, 0, len(dayNums))
	prev := 0
	for _, d := range dayNums {
		if d != prev {
			out = append(out, at(y, m, d))
		}
		prev = d
	}
	return out
}
