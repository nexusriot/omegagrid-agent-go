package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

var cronMonthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var cronWeekdayNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

// Matches checks whether a 5-field cron expression fires at dt.
//
// Fields: minute hour day-of-month month day-of-week
// Each field supports:  *, */N, single value, lo-hi range, comma-separated lists.
// Month and weekday also accept three-letter names (jan..dec, sun..sat).
// Day-of-week uses 0=Sunday..6=Saturday; 7 is additionally accepted as Sunday.
//
// Day-of-month and day-of-week follow standard (Vixie) cron semantics: when
// BOTH are restricted (neither is "*"), the expression fires if EITHER matches;
// when only one is restricted, only that one applies.
//
// Returns false on malformed expressions rather than erroring — this prevents
// bad input from killing scheduler ticks.
func Matches(cronExpr string, dt time.Time) bool {
	parts := strings.Fields(strings.TrimSpace(cronExpr))
	if len(parts) != 5 {
		return false
	}
	if !fieldMatches(parts[0], dt.Minute(), 0, nil) {
		return false
	}
	if !fieldMatches(parts[1], dt.Hour(), 0, nil) {
		return false
	}
	if !fieldMatches(parts[3], int(dt.Month()), 1, cronMonthNames) {
		return false
	}

	domRestricted := strings.TrimSpace(parts[2]) != "*"
	dowRestricted := strings.TrimSpace(parts[4]) != "*"
	domMatch := fieldMatches(parts[2], dt.Day(), 1, nil)
	dowMatch := weekdayMatches(parts[4], int(dt.Weekday()))

	if domRestricted && dowRestricted {
		return domMatch || dowMatch
	}
	return domMatch && dowMatch
}

// weekdayMatches matches a day-of-week field against a Sunday=0..Saturday=6
// weekday, treating a literal 7 in the expression as Sunday.
func weekdayMatches(field string, wd int) bool {
	if fieldMatches(field, wd, 0, cronWeekdayNames) {
		return true
	}
	if wd == 0 && fieldMatches(field, 7, 0, cronWeekdayNames) {
		return true
	}
	return false
}

// ValidateCron checks whether a 5-field cron expression is syntactically
// correct and within the allowed ranges. Returns a descriptive error on
// failure so callers can return it to the user verbatim.
func ValidateCron(cronExpr string) error {
	parts := strings.Fields(strings.TrimSpace(cronExpr))
	if len(parts) != 5 {
		return fmt.Errorf("expected 5 fields (minute hour day month weekday), got %d", len(parts))
	}
	type fieldSpec struct {
		name  string
		lo    int
		hi    int
		names map[string]int
	}
	specs := []fieldSpec{
		{"minute", 0, 59, nil},
		{"hour", 0, 23, nil},
		{"day", 1, 31, nil},
		{"month", 1, 12, cronMonthNames},
		{"weekday", 0, 7, cronWeekdayNames},
	}
	for i, field := range parts {
		if err := validateField(field, specs[i].lo, specs[i].hi, specs[i].name, specs[i].names); err != nil {
			return err
		}
	}
	return nil
}

func validateField(field string, lo, hi int, name string, names map[string]int) error {
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		base := part
		if i := strings.Index(part, "/"); i >= 0 {
			s, err := strconv.Atoi(part[i+1:])
			if err != nil || s <= 0 {
				return fmt.Errorf("field %s: invalid step in %q", name, part)
			}
			base = part[:i]
		}
		if base == "*" {
			continue
		}
		if strings.Contains(base, "-") {
			ab := strings.SplitN(base, "-", 2)
			a, err1 := cronAtoi(ab[0], names)
			b, err2 := cronAtoi(ab[1], names)
			if err1 != nil || err2 != nil {
				return fmt.Errorf("field %s: invalid range in %q", name, part)
			}
			if a < lo || b > hi || a > b {
				return fmt.Errorf("field %s: range %d-%d out of bounds [%d-%d]", name, a, b, lo, hi)
			}
			continue
		}
		n, err := cronAtoi(base, names)
		if err != nil {
			return fmt.Errorf("field %s: invalid value %q", name, part)
		}
		if n < lo || n > hi {
			return fmt.Errorf("field %s: value %d out of bounds [%d-%d]", name, n, lo, hi)
		}
	}
	return nil
}

// cronAtoi parses a single cron token, resolving month/weekday names when a
// names map is supplied.
func cronAtoi(tok string, names map[string]int) (int, error) {
	tok = strings.TrimSpace(tok)
	if names != nil {
		if v, ok := names[strings.ToLower(tok)]; ok {
			return v, nil
		}
	}
	return strconv.Atoi(tok)
}

func fieldMatches(field string, value, lo int, names map[string]int) bool {
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		step := 1
		if i := strings.Index(part, "/"); i >= 0 {
			s, err := strconv.Atoi(part[i+1:])
			if err != nil || s <= 0 {
				continue
			}
			step = s
			part = part[:i]
		}
		switch {
		case part == "*":
			if (value-lo)%step == 0 {
				return true
			}
		case strings.Contains(part, "-"):
			ab := strings.SplitN(part, "-", 2)
			a, err1 := cronAtoi(ab[0], names)
			b, err2 := cronAtoi(ab[1], names)
			if err1 != nil || err2 != nil {
				continue
			}
			if value >= a && value <= b && (value-a)%step == 0 {
				return true
			}
		default:
			n, err := cronAtoi(part, names)
			if err != nil {
				continue
			}
			if n == value {
				return true
			}
		}
	}
	return false
}
