package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Matches checks whether a 5-field cron expression fires at dt.
//
// Fields: minute hour day-of-month month day-of-week
// Each field supports:  *, */N, single value, lo-hi range, comma-separated lists.
// Day-of-week uses 0=Sunday..6=Saturday (matches the Python implementation).
//
// Returns false on malformed expressions rather than erroring — this matches
// the original Python behaviour and prevents bad input from killing ticks.
func Matches(cronExpr string, dt time.Time) bool {
	parts := strings.Fields(strings.TrimSpace(cronExpr))
	if len(parts) != 5 {
		return false
	}
	values := []int{
		dt.Minute(),
		dt.Hour(),
		dt.Day(),
		int(dt.Month()),
		int(dt.Weekday()), // Sunday=0
	}
	ranges := [][2]int{
		{0, 59},
		{0, 23},
		{1, 31},
		{1, 12},
		{0, 6},
	}
	for i, field := range parts {
		if !fieldMatches(field, values[i], ranges[i][0], ranges[i][1]) {
			return false
		}
	}
	return true
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
		name string
		lo   int
		hi   int
	}
	specs := []fieldSpec{
		{"minute", 0, 59},
		{"hour", 0, 23},
		{"day", 1, 31},
		{"month", 1, 12},
		{"weekday", 0, 6},
	}
	for i, field := range parts {
		if err := validateField(field, specs[i].lo, specs[i].hi, specs[i].name); err != nil {
			return err
		}
	}
	return nil
}

func validateField(field string, lo, hi int, name string) error {
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
			a, err1 := strconv.Atoi(ab[0])
			b, err2 := strconv.Atoi(ab[1])
			if err1 != nil || err2 != nil {
				return fmt.Errorf("field %s: invalid range in %q", name, part)
			}
			if a < lo || b > hi || a > b {
				return fmt.Errorf("field %s: range %d-%d out of bounds [%d-%d]", name, a, b, lo, hi)
			}
			continue
		}
		n, err := strconv.Atoi(base)
		if err != nil {
			return fmt.Errorf("field %s: invalid value %q", name, part)
		}
		if n < lo || n > hi {
			return fmt.Errorf("field %s: value %d out of bounds [%d-%d]", name, n, lo, hi)
		}
	}
	return nil
}

func fieldMatches(field string, value, lo, hi int) bool {
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
			a, err1 := strconv.Atoi(ab[0])
			b, err2 := strconv.Atoi(ab[1])
			if err1 != nil || err2 != nil {
				continue
			}
			if value >= a && value <= b && (value-a)%step == 0 {
				return true
			}
		default:
			n, err := strconv.Atoi(part)
			if err != nil {
				continue
			}
			if n == value {
				return true
			}
		}
		_ = lo
		_ = hi
	}
	return false
}
