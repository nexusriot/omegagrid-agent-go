package builtin

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func TimeConvertSchema() Skill {
	return Skill{Name: "time_convert", Description: "Convert times between time zones and do date arithmetic. " +
		"action=convert shows a time in other zones; add adds a duration (2h30m, 3d, -1w) or business days; " +
		"diff gives the span between two times, including business days.",
		Parameters: map[string]Param{
			"action":        {Type: "string", Description: "convert (default) | add | diff", Required: false},
			"time":          {Type: "string", Description: "now, 2026-11-03 15:00, 2026-11-03, 15:00, 3pm, or RFC3339 (default now)", Required: false},
			"from_tz":       {Type: "string", Description: "IANA zone the time is written in (default UTC; ignored for RFC3339 with an offset)", Required: false},
			"to_tz":         {Type: "array", Description: "Zones to show the result in (default UTC)", Required: false},
			"duration":      {Type: "string", Description: "add: duration such as 90m, 2h, 3d, 1w2d, -4h", Required: false},
			"business_days": {Type: "number", Description: "add: business days (Mon-Fri) to add, may be negative", Required: false},
			"end":           {Type: "string", Description: "diff: the second time (same formats as time)", Required: false},
		}}
}

// TimeConvert is pure: the clock is read once per call.
func TimeConvert() Executor { return timeConvertAt(time.Now) }

func timeConvertAt(now func() time.Time) Executor {
	return func(args map[string]any) (any, error) {
		fromName := strings.TrimSpace(str(args, "from_tz"))
		from, err := loadZone(fromName)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		zones := strList(args, "to_tz")
		if len(zones) == 0 {
			zones = []string{"UTC"}
		}
		if len(zones) > 12 {
			return map[string]any{"error": "at most 12 target zones"}, nil
		}
		t, err := parseUserTime(str(args, "time"), from, now())
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		action := strings.ToLower(strings.TrimSpace(str(args, "action")))
		out := map[string]any{"input": describeTime(t, t.Location())}
		switch action {
		case "", "convert":
		case "add":
			if d := strings.TrimSpace(str(args, "duration")); d != "" {
				dur, err := parseLongDuration(d)
				if err != nil {
					return map[string]any{"error": "duration: " + err.Error()}, nil
				}
				t = t.Add(dur)
			}
			if n := intOr(args, "business_days", 0); n != 0 {
				if n > 10000 || n < -10000 {
					return map[string]any{"error": "business_days must be within ±10000"}, nil
				}
				t = addBusinessDays(t, n)
			}
		case "diff":
			end, err := parseUserTime(str(args, "end"), from, now())
			if err != nil {
				return map[string]any{"error": "end: " + err.Error()}, nil
			}
			d := end.Sub(t)
			out["end"] = describeTime(end, end.Location())
			out["duration"] = d.String()
			out["hours"] = math.Round(d.Hours()*100) / 100
			out["days"] = math.Round(d.Hours()/24*100) / 100
			out["business_days"] = businessDaysBetween(t, end)
			return out, nil
		default:
			return map[string]any{"error": "action must be convert, add or diff"}, nil
		}
		var results []map[string]any
		for _, z := range zones {
			loc, err := loadZone(z)
			if err != nil {
				return map[string]any{"error": err.Error()}, nil
			}
			results = append(results, describeTime(t, loc))
		}
		out["results"] = results
		return out, nil
	}
}

func loadZone(name string) (*time.Location, error) {
	switch strings.ToUpper(name) {
	case "", "UTC", "Z", "GMT":
		return time.UTC, nil
	case "LOCAL":
		return nil, fmt.Errorf("give an IANA zone such as Europe/Berlin, not Local")
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unknown time zone %q (use an IANA name such as America/New_York)", name)
	}
	return loc, nil
}

var clock12RE = regexp.MustCompile(`^(?i)(\d{1,2})(?::(\d{2}))?\s*(am|pm)$`)

// parseUserTime reads the formats people and models actually write, in loc
// unless the text carries its own offset.
func parseUserTime(s string, loc *time.Location, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "now") {
		return now.In(loc), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, l := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(l, s, loc); err == nil {
			return t, nil
		}
	}
	day := now.In(loc)
	at := func(h, m int) time.Time { return time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, loc) }
	for _, l := range []string{"15:04", "15:04:05"} {
		if t, err := time.Parse(l, s); err == nil {
			return at(t.Hour(), t.Minute()).Add(time.Duration(t.Second()) * time.Second), nil
		}
	}
	if m := clock12RE.FindStringSubmatch(s); m != nil {
		h, _ := strconv.Atoi(m[1])
		mi, _ := strconv.Atoi(m[2])
		if h < 1 || h > 12 || mi > 59 {
			return time.Time{}, fmt.Errorf("invalid time %q", s)
		}
		if strings.EqualFold(m[3], "pm") && h != 12 {
			h += 12
		} else if strings.EqualFold(m[3], "am") && h == 12 {
			h = 0
		}
		return at(h, mi), nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 1e8 {
		return time.Unix(n, 0).In(loc), nil
	}
	return time.Time{}, fmt.Errorf("cannot read time %q (try 2026-11-03 15:00, 15:00, 3pm or RFC3339)", s)
}

func describeTime(t time.Time, loc *time.Location) map[string]any {
	lt := t.In(loc)
	name, offset := lt.Zone()
	return map[string]any{
		"zone":     loc.String(),
		"time":     lt.Format("2006-01-02 15:04:05"),
		"weekday":  lt.Weekday().String(),
		"abbrev":   name,
		"offset":   fmt.Sprintf("%+03d:%02d", offset/3600, abs(offset%3600)/60),
		"rfc3339":  lt.Format(time.RFC3339),
		"unix":     t.Unix(),
		"is_dst":   lt.IsDST(),
		"readable": lt.Format("Mon 2 Jan 2006 15:04 MST"),
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func isWeekend(t time.Time) bool { return t.Weekday() == time.Saturday || t.Weekday() == time.Sunday }

// addBusinessDays moves n weekdays forward (or back), keeping the time of
// day; a start on a weekend counts from the next (or previous) weekday.
func addBusinessDays(t time.Time, n int) time.Time {
	step := 1
	if n < 0 {
		step, n = -1, -n
	}
	for n > 0 {
		t = t.AddDate(0, 0, step)
		if !isWeekend(t) {
			n--
		}
	}
	return t
}

// businessDaysBetween counts weekdays from a's date (exclusive) to b's date
// (inclusive); negative when b is before a.
func businessDaysBetween(a, b time.Time) int {
	sign := 1
	if b.Before(a) {
		a, b, sign = b, a, -1
	}
	da := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, time.UTC)
	db := time.Date(b.In(a.Location()).Year(), b.In(a.Location()).Month(), b.In(a.Location()).Day(), 0, 0, 0, 0, time.UTC)
	n := 0
	for d := da.AddDate(0, 0, 1); !d.After(db); d = d.AddDate(0, 0, 1) {
		if !isWeekend(d) {
			n++
		}
	}
	return n * sign
}

func RegexTestSchema() Skill {
	return Skill{Name: "regex_test", Description: "Test a regular expression (RE2 syntax, as Go uses) against sample text: " +
		"matches with positions and capture groups, and optionally the text after a replacement.",
		Parameters: map[string]Param{
			"pattern":     {Type: "string", Description: "Regular expression", Required: true},
			"text":        {Type: "string", Description: "Text to search (up to 100 KB)", Required: true},
			"flags":       {Type: "string", Description: "Any of i (ignore case), m (multi-line ^$), s (dot matches newline)", Required: false},
			"replace":     {Type: "string", Description: "Replacement; $1 / ${name} refer to groups", Required: false},
			"max_matches": {Type: "number", Description: "Max matches to list (default 50, max 500)", Required: false},
		}}
}

func RegexTest() Executor {
	return func(args map[string]any) (any, error) {
		pattern := str(args, "pattern")
		text := str(args, "text")
		if pattern == "" {
			return map[string]any{"error": "pattern is required"}, nil
		}
		if len(pattern) > 2000 || len(text) > 100<<10 {
			return map[string]any{"error": "pattern is limited to 2000 bytes and text to 100 KB"}, nil
		}
		flags := strings.ToLower(str(args, "flags"))
		for _, f := range flags {
			if !strings.ContainsRune("ims", f) {
				return map[string]any{"error": fmt.Sprintf("unknown flag %q (use i, m, s)", f)}, nil
			}
		}
		full := pattern
		if flags != "" {
			full = "(?" + flags + ")" + pattern
		}
		re, err := regexp.Compile(full)
		if err != nil {
			return map[string]any{"valid": false, "error": err.Error()}, nil
		}
		limit := min(max(intOr(args, "max_matches", 50), 1), 500)
		names := re.SubexpNames()
		all := re.FindAllStringSubmatchIndex(text, -1)
		var matches []map[string]any
		for _, idx := range all {
			if len(matches) == limit {
				break
			}
			m := map[string]any{"match": text[idx[0]:idx[1]], "start": idx[0], "end": idx[1]}
			if len(idx) > 2 {
				groups := map[string]any{}
				for g := 1; g*2 < len(idx); g++ {
					key := strconv.Itoa(g)
					if names[g] != "" {
						key = names[g]
					}
					if idx[2*g] < 0 {
						groups[key] = nil
					} else {
						groups[key] = text[idx[2*g]:idx[2*g+1]]
					}
				}
				m["groups"] = groups
			}
			matches = append(matches, m)
		}
		out := map[string]any{"valid": true, "pattern": full, "match_count": len(all), "matches": nonNilMaps(matches)}
		if _, ok := args["replace"]; ok {
			out["replaced"] = re.ReplaceAllString(text, str(args, "replace"))
		}
		return out, nil
	}
}

func nonNilMaps(a []map[string]any) []map[string]any {
	if a == nil {
		return []map[string]any{}
	}
	return a
}

func TextDiffSchema() Skill {
	return Skill{Name: "text_diff", Description: "Unified diff between two texts (line based), with counts of added and removed lines.",
		Parameters: map[string]Param{
			"a":       {Type: "string", Description: "Original text", Required: true},
			"b":       {Type: "string", Description: "Changed text", Required: true},
			"context": {Type: "number", Description: "Unchanged lines around each change (default 3)", Required: false},
		}}
}

const maxDiffLines = 3000

func TextDiff() Executor {
	return func(args map[string]any) (any, error) {
		a, b := splitLines(str(args, "a")), splitLines(str(args, "b"))
		if len(a) > maxDiffLines || len(b) > maxDiffLines {
			return map[string]any{"error": fmt.Sprintf("each text is limited to %d lines", maxDiffLines)}, nil
		}
		ctxLines := min(max(intOr(args, "context", 3), 0), 20)
		ops := diffLines(a, b)
		added, removed := 0, 0
		for _, o := range ops {
			switch o.kind {
			case '+':
				added++
			case '-':
				removed++
			}
		}
		return map[string]any{
			"identical": added == 0 && removed == 0,
			"added":     added,
			"removed":   removed,
			"diff":      unifiedDiff(ops, ctxLines),
		}, nil
	}
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(strings.ReplaceAll(s, "\r\n", "\n"), "\n"), "\n")
}

type diffOp struct {
	kind   byte // ' ', '-', '+'
	text   string
	ai, bi int // 1-based line numbers in a / b (0 when absent)
}

// diffLines is a longest-common-subsequence line diff. Quadratic, which the
// line cap keeps to a few MB.
func diffLines(a, b []string) []diffOp {
	n, m := len(a), len(b)
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			ops = append(ops, diffOp{' ', a[i], i + 1, j + 1})
			i++
			j++
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
			// Deletions first on a tie, the way diff(1) orders a change.
			ops = append(ops, diffOp{'-', a[i], i + 1, 0})
			i++
		default:
			ops = append(ops, diffOp{'+', b[j], 0, j + 1})
			j++
		}
	}
	return ops
}

func unifiedDiff(ops []diffOp, ctxLines int) string {
	var b strings.Builder
	b.WriteString("--- a\n+++ b\n")
	for i := 0; i < len(ops); {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		start := max(i-ctxLines, 0)
		end := i
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == ' ' {
				run++
			}
			if run-end > 2*ctxLines || run == len(ops) {
				end = min(end+ctxLines, len(ops))
				break
			}
			end = run
		}
		aStart, bStart, aCount, bCount := 0, 0, 0, 0
		for _, o := range ops[start:end] {
			if o.kind != '+' {
				if aStart == 0 {
					aStart = o.ai
				}
				aCount++
			}
			if o.kind != '-' {
				if bStart == 0 {
					bStart = o.bi
				}
				bCount++
			}
		}
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", aStart, aCount, bStart, bCount)
		for _, o := range ops[start:end] {
			b.WriteByte(o.kind)
			b.WriteString(o.text)
			b.WriteByte('\n')
		}
		i = end
	}
	return b.String()
}

func UnitConvertSchema() Skill {
	return Skill{Name: "unit_convert", Description: "Convert between units of length, mass, temperature, time, data size (SI kB/MB and IEC KiB/MiB), " +
		"data rate, speed, area, volume, pressure and energy.",
		Parameters: map[string]Param{
			"value": {Type: "number", Description: "Amount to convert", Required: true},
			"from":  {Type: "string", Description: "Unit, e.g. km, mi, lb, C, F, MiB, GB, Mbps, MB/s, km/h, kWh", Required: true},
			"to":    {Type: "string", Description: "Target unit in the same category", Required: true},
		}}
}

type unitDef struct {
	category string
	factor   float64 // to the category's base unit
}

// units maps exact spellings; unitAliases maps case-insensitive spellings
// that are unambiguous (data units are case-sensitive: Mb ≠ MB).
var units = map[string]unitDef{
	"mm": {"length", 0.001}, "cm": {"length", 0.01}, "m": {"length", 1}, "km": {"length", 1000},
	"in": {"length", 0.0254}, "ft": {"length", 0.3048}, "yd": {"length", 0.9144}, "mi": {"length", 1609.344}, "nmi": {"length", 1852},
	"mg": {"mass", 1e-6}, "g": {"mass", 0.001}, "kg": {"mass", 1}, "t": {"mass", 1000},
	"oz": {"mass", 0.028349523125}, "lb": {"mass", 0.45359237}, "st": {"mass", 6.35029318},
	"ms": {"time", 0.001}, "s": {"time", 1}, "min": {"time", 60}, "h": {"time", 3600}, "d": {"time", 86400}, "wk": {"time", 604800},
	"bit": {"data", 0.125}, "B": {"data", 1},
	"kB": {"data", 1e3}, "KB": {"data", 1e3}, "MB": {"data", 1e6}, "GB": {"data", 1e9}, "TB": {"data", 1e12}, "PB": {"data", 1e15},
	"KiB": {"data", 1 << 10}, "MiB": {"data", 1 << 20}, "GiB": {"data", 1 << 30}, "TiB": {"data", 1 << 40}, "PiB": {"data", 1 << 50},
	"kbit": {"data", 125}, "Mbit": {"data", 125e3}, "Gbit": {"data", 125e6}, "Tbit": {"data", 125e9},
	"bps": {"rate", 1}, "kbps": {"rate", 1e3}, "Mbps": {"rate", 1e6}, "Gbps": {"rate", 1e9}, "Tbps": {"rate", 1e12},
	"B/s": {"rate", 8}, "kB/s": {"rate", 8e3}, "KB/s": {"rate", 8e3}, "MB/s": {"rate", 8e6}, "GB/s": {"rate", 8e9},
	"KiB/s": {"rate", 8 * 1024}, "MiB/s": {"rate", 8 * 1 << 20}, "GiB/s": {"rate", 8 * 1 << 30},
	"m/s": {"speed", 1}, "km/h": {"speed", 1 / 3.6}, "mph": {"speed", 0.44704}, "kn": {"speed", 0.514444}, "ft/s": {"speed", 0.3048},
	"mm2": {"area", 1e-6}, "cm2": {"area", 1e-4}, "m2": {"area", 1}, "km2": {"area", 1e6}, "ha": {"area", 1e4},
	"ft2": {"area", 0.09290304}, "in2": {"area", 0.00064516}, "acre": {"area", 4046.8564224}, "mi2": {"area", 2589988.110336},
	"ml": {"volume", 0.001}, "l": {"volume", 1}, "m3": {"volume", 1000}, "tsp": {"volume", 0.00492892159375},
	"tbsp": {"volume", 0.0147867647812}, "floz": {"volume", 0.0295735295625}, "cup": {"volume", 0.2365882365},
	"pt": {"volume", 0.473176473}, "qt": {"volume", 0.946352946}, "gal": {"volume", 3.785411784},
	"Pa": {"pressure", 1}, "kPa": {"pressure", 1000}, "MPa": {"pressure", 1e6}, "bar": {"pressure", 1e5}, "mbar": {"pressure", 100},
	"psi": {"pressure", 6894.757293168}, "atm": {"pressure", 101325}, "mmHg": {"pressure", 133.322387415},
	"J": {"energy", 1}, "kJ": {"energy", 1000}, "MJ": {"energy", 1e6}, "cal": {"energy", 4.184}, "kcal": {"energy", 4184},
	"Wh": {"energy", 3600}, "kWh": {"energy", 3.6e6}, "MWh": {"energy", 3.6e9}, "BTU": {"energy", 1055.05585262},
}

var unitAliases = map[string]string{
	"meter": "m", "meters": "m", "metre": "m", "kilometer": "km", "kilometers": "km", "inch": "in", "inches": "in",
	"foot": "ft", "feet": "ft", "yard": "yd", "yards": "yd", "mile": "mi", "miles": "mi",
	"gram": "g", "grams": "g", "kilogram": "kg", "kilograms": "kg", "pound": "lb", "pounds": "lb", "lbs": "lb", "ounce": "oz", "ounces": "oz", "tonne": "t",
	"sec": "s", "second": "s", "seconds": "s", "minute": "min", "minutes": "min", "hour": "h", "hours": "h", "hr": "h",
	"day": "d", "days": "d", "week": "wk", "weeks": "wk", "byte": "B", "bytes": "B", "bits": "bit",
	"kmh": "km/h", "kph": "km/h", "knot": "kn", "knots": "kn", "mps": "m/s",
	"liter": "l", "liters": "l", "litre": "l", "litres": "l", "milliliter": "ml", "gallon": "gal", "gallons": "gal",
	"m^2": "m2", "m²": "m2", "km²": "km2", "ft²": "ft2", "m^3": "m3", "m³": "m3", "acres": "acre", "hectare": "ha",
	"kwh": "kWh", "wh": "Wh", "btu": "BTU", "pa": "Pa", "kpa": "kPa", "mmhg": "mmHg",
}

var temperatureUnits = map[string]string{"c": "C", "°c": "C", "celsius": "C", "f": "F", "°f": "F", "fahrenheit": "F", "k": "K", "kelvin": "K"}

func lookupUnit(u string) (string, unitDef, bool) {
	u = strings.TrimSpace(u)
	if d, ok := units[u]; ok {
		return u, d, true
	}
	if a, ok := unitAliases[strings.ToLower(u)]; ok {
		return a, units[a], true
	}
	if d, ok := units[strings.ToLower(u)]; ok && d.category != "data" && d.category != "rate" {
		return strings.ToLower(u), d, true
	}
	return "", unitDef{}, false
}

func UnitConvert() Executor {
	return func(args map[string]any) (any, error) {
		v, ok := numArg(args["value"])
		if !ok || math.IsNaN(v) || math.IsInf(v, 0) {
			return map[string]any{"error": "value must be a number"}, nil
		}
		from, to := str(args, "from"), str(args, "to")
		if tf, ok := temperatureUnits[strings.ToLower(strings.TrimSpace(from))]; ok {
			tt, ok := temperatureUnits[strings.ToLower(strings.TrimSpace(to))]
			if !ok {
				return map[string]any{"error": fmt.Sprintf("cannot convert temperature to %q (use C, F or K)", to)}, nil
			}
			k := toKelvin(v, tf)
			if k < 0 {
				return map[string]any{"error": "below absolute zero"}, nil
			}
			return unitResult(v, tf, fromKelvin(k, tt), tt, "temperature"), nil
		}
		fn, fd, ok := lookupUnit(from)
		if !ok {
			return map[string]any{"error": fmt.Sprintf("unknown unit %q", from)}, nil
		}
		tn, td, ok := lookupUnit(to)
		if !ok {
			return map[string]any{"error": fmt.Sprintf("unknown unit %q", to)}, nil
		}
		if fd.category != td.category {
			return map[string]any{"error": fmt.Sprintf("cannot convert %s (%s) to %s (%s)", fn, fd.category, tn, td.category)}, nil
		}
		return unitResult(v, fn, v*fd.factor/td.factor, tn, fd.category), nil
	}
}

func unitResult(v float64, from string, r float64, to, cat string) map[string]any {
	rounded, _ := strconv.ParseFloat(strconv.FormatFloat(r, 'g', 12, 64), 64)
	return map[string]any{"value": v, "from": from, "result": rounded, "to": to, "category": cat,
		"text": fmt.Sprintf("%s %s = %s %s", strconv.FormatFloat(v, 'g', -1, 64), from, strconv.FormatFloat(rounded, 'g', 10, 64), to)}
}

func toKelvin(v float64, u string) float64 {
	switch u {
	case "C":
		return v + 273.15
	case "F":
		return (v-32)*5/9 + 273.15
	}
	return v
}

func fromKelvin(k float64, u string) float64 {
	switch u {
	case "C":
		return k - 273.15
	case "F":
		return (k-273.15)*9/5 + 32
	}
	return k
}
