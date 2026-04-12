package scheduler

import (
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
