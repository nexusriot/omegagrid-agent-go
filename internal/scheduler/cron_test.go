package scheduler

import (
	"testing"
	"time"
)

// TestReferenceDatesWeekday guards the hand-computed calendar the cron tables
// below rely on. If any of these fail, the fixed time.Date values are wrong and
// the OR/weekday assertions cannot be trusted.
func TestReferenceDatesWeekday(t *testing.T) {
	cases := []struct {
		dt   time.Time
		want time.Weekday
	}{
		{time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC), time.Monday},
		{time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC), time.Monday},
		{time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC), time.Wednesday},
		{time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC), time.Sunday},
	}
	for _, c := range cases {
		if got := c.dt.Weekday(); got != c.want {
			t.Errorf("%s weekday = %v, want %v", c.dt.Format("2006-01-02"), got, c.want)
		}
	}
}

func TestMatches(t *testing.T) {
	// Fixed reference instants (no time.Now()).
	monJul20 := time.Date(2026, 7, 20, 9, 5, 0, 0, time.UTC)  // Mon, 09:05, day 20, month 7, weekday 1
	monJun15 := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC) // Mon 15th, 12:00, month 6, weekday 1
	wedJul15 := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC) // Wed 15th, 12:00, month 7, weekday 3
	sunJul19 := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)  // Sun, 00:00, weekday 0
	min20 := time.Date(2026, 7, 20, 9, 20, 0, 0, time.UTC)    // minute 20

	tests := []struct {
		name string
		expr string
		dt   time.Time
		want bool
	}{
		{"all star", "* * * * *", monJul20, true},

		{"all fields exact", "5 9 20 7 1", monJul20, true},
		{"minute mismatch", "6 9 20 7 1", monJul20, false},
		{"hour mismatch", "5 8 20 7 1", monJul20, false},
		{"month mismatch", "5 9 20 6 1", monJul20, false},

		{"minute step hit", "*/5 * * * *", monJul20, true},          // 5 % 5 == 0
		{"minute step miss", "*/20 * * * *", monJul20, false},       // 5 % 20 != 0
		{"minute step every2 miss", "*/2 * * * *", monJul20, false}, // 5 is odd
		{"minute step hit at zero", "*/15 * * * *", monJun15, true}, // minute 0

		{"hour range hit", "* 8-10 * * *", monJul20, true}, // hour 9
		{"hour range miss", "* 10-12 * * *", monJul20, false},
		{"minute range+step hit", "0-30/10 * * * *", min20, true},      // 20 in [0,30], (20)%10==0
		{"minute range+step miss", "0-30/10 * * * *", monJul20, false}, // minute 5

		{"minute list hit", "1,3,5 * * * *", monJul20, true},
		{"minute list miss", "1,3,7 * * * *", monJul20, false},
		{"month list hit", "* * * 6,7,8 *", monJul20, true},   // month 7
		{"weekday list hit", "* * * * 1,3,5", monJul20, true}, // Monday=1

		{"weekday sunday zero", "* * * * 0", sunJul19, true},
		{"weekday monday one", "* * * * 1", monJul20, true},
		{"weekday mismatch", "* * * * 3", monJul20, false}, // Monday is not Wed(3)

		// 7 is accepted as Sunday (standard Vixie cron).
		{"weekday 7 is sunday", "* * * * 7", sunJul19, true},
		{"weekday 7 not monday", "* * * * 7", monJul20, false},
		{"weekday range through 7", "* * * * 6-7", sunJul19, true}, // Sat-Sun includes Sunday

		// Day-of-month and day-of-week are OR-ed when BOTH are restricted.
		{"dom or dow both match", "0 12 15 * 1", monJun15, true},     // 15th AND Monday
		{"dom matches dow does not", "0 12 15 * 1", wedJul15, true},  // 15th (OR) — Wednesday
		{"dow matches dom does not", "0 12 1 * 1", monJun15, true},   // Monday (OR) — not day 1
		{"neither dom nor dow match", "0 12 1 * 2", monJun15, false}, // not day 1, not Tuesday
		{"dom only restricted hit", "0 12 15 * *", monJun15, true},   // dow is *, dom matches
		{"dom only restricted miss", "0 12 20 * *", monJun15, false}, // dow is *, dom does not match
		{"dow only restricted hit", "0 0 * * 0", sunJul19, true},     // dom is *, Sunday matches
		{"dow only restricted 7 hit", "0 0 * * 7", sunJul19, true},   // dom is *, 7==Sunday matches

		// Month and weekday names are accepted (like most cron dialects).
		{"weekday name accepted", "* * * * MON", monJul20, true},
		{"weekday name mismatch", "* * * * fri", monJul20, false},
		{"weekday name range", "* * * * mon-fri", monJul20, true},
		{"month name accepted", "* * * JUL *", monJul20, true},
		{"month name mismatch", "* * * JUN *", monJul20, false},
		{"weekday name list", "* * * * sat,sun", sunJul19, true},

		{"empty expr", "", monJul20, false},
		{"four fields", "* * * *", monJul20, false},
		{"six fields", "* * * * * *", monJul20, false},
		{"non-numeric minute", "x * * * *", monJul20, false},
		{"step zero", "*/0 * * * *", monJul20, false},
		{"step negative", "*/-1 * * * *", monJul20, false},
		{"step non-numeric", "*/x * * * *", monJul20, false},
		{"minute value out of range", "60 * * * *", monJul20, false},
		{"bad range field", "* 5-x * * *", monJul20, false},

		{"leading trailing whitespace", "  * * * * *  ", monJul20, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Matches(tc.expr, tc.dt); got != tc.want {
				t.Errorf("Matches(%q, %s) = %v, want %v",
					tc.expr, tc.dt.Format(time.RFC3339), got, tc.want)
			}
		})
	}
}

func TestValidateCron(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{"star", "* * * * *", false},
		{"step", "*/5 * * * *", false},
		{"exact daily", "0 15 * * *", false},
		{"range and list", "0 0 1-15 * 1,3,5", false},
		{"range with step", "0-30/10 9-17 * * 1-5", false},
		{"max bounds", "59 23 31 12 6", false},
		{"min bounds", "0 0 1 1 0", false},
		{"weekday seven accepted", "* * * * 7", false},
		{"weekday name accepted", "* * * * MON", false},
		{"weekday name range accepted", "0 9 * * mon-fri", false},
		{"month name accepted", "0 9 1 jan *", false},

		{"empty", "", true},
		{"four fields", "* * * *", true},
		{"six fields", "* * * * * *", true},
		{"step zero", "*/0 * * * *", true},
		{"step negative", "*/-3 * * * *", true},
		{"step non-numeric", "*/x * * * *", true},
		{"minute too big", "60 * * * *", true},
		{"hour too big", "* 24 * * *", true},
		{"day zero", "* * 0 * *", true},
		{"day too big", "* * 32 * *", true},
		{"month zero", "* * * 0 *", true},
		{"month too big", "* * * 13 *", true},
		{"weekday eight rejected", "* * * * 8", true},
		{"reversed range", "5-2 * * * *", true},
		{"range upper out of bounds", "1-99 * * * *", true},
		{"bad value", "x * * * *", true},
		{"bad range value", "a-5 * * * *", true},
		{"list bad member", "1,2,x * * * *", true},
		{"unknown weekday name", "* * * * xyz", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCron(tc.expr)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateCron(%q) error = %v, wantErr %v", tc.expr, err, tc.wantErr)
			}
		})
	}
}

func TestFieldMatches(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value int
		lo    int
		names map[string]int
		want  bool
	}{
		{"star", "*", 5, 0, nil, true},
		{"star step hit", "*/5", 10, 0, nil, true},
		{"star step miss", "*/5", 7, 0, nil, false},
		{"star step zero skipped", "*/0", 5, 0, nil, false},
		{"star step negative skipped", "*/-2", 5, 0, nil, false},
		{"exact hit", "5", 5, 0, nil, true},
		{"exact miss", "5", 6, 0, nil, false},
		{"range hit", "1-5", 3, 0, nil, true},
		{"range miss above", "1-5", 6, 0, nil, false},
		{"range miss below", "1-5", 0, 0, nil, false},
		{"list hit", "1,3,5", 3, 0, nil, true},
		{"list miss", "1,3,5", 4, 0, nil, false},
		{"range step hit", "1-10/2", 5, 0, nil, true},   // (5-1)%2==0
		{"range step miss", "1-10/2", 4, 0, nil, false}, // (4-1)%2==1
		{"invalid token", "abc", 5, 0, nil, false},
		{"invalid range", "1-x", 5, 0, nil, false},
		{"empty field", "", 5, 0, nil, false},

		{"weekday name hit", "mon", 1, 0, cronWeekdayNames, true},
		{"weekday name miss", "mon", 2, 0, cronWeekdayNames, false},
		{"weekday name range hit", "mon-fri", 3, 0, cronWeekdayNames, true},
		{"month name hit", "jul", 7, 1, cronMonthNames, true},
		{"name field without names map", "mon", 1, 0, nil, false},

		// '*/N' anchors the step at lo, so with lo=1 (day-of-month/month) it
		// matches lo, lo+N, ... i.e. odd days for */2.
		{"star step anchored at lo hit", "*/2", 3, 1, nil, true},   // (3-1)%2==0
		{"star step anchored at lo miss", "*/2", 2, 1, nil, false}, // (2-1)%2==1

		// A single value silently ignores any step suffix.
		{"single value ignores step", "5/3", 5, 0, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := fieldMatches(tc.field, tc.value, tc.lo, tc.names); got != tc.want {
				t.Errorf("fieldMatches(%q, %d, %d) = %v, want %v",
					tc.field, tc.value, tc.lo, got, tc.want)
			}
		})
	}
}

func TestWeekdayMatches(t *testing.T) {
	tests := []struct {
		name  string
		field string
		wd    int
		want  bool
	}{
		{"zero matches sunday", "0", 0, true},
		{"seven matches sunday", "7", 0, true},
		{"seven does not match monday", "7", 1, false},
		{"name sun matches sunday", "sun", 0, true},
		{"range 6-7 matches sunday", "6-7", 0, true},
		{"range 6-7 matches saturday", "6-7", 6, true},
		{"star matches any", "*", 3, true},
		{"list with seven", "3,7", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := weekdayMatches(tc.field, tc.wd); got != tc.want {
				t.Errorf("weekdayMatches(%q, %d) = %v, want %v", tc.field, tc.wd, got, tc.want)
			}
		})
	}
}

func TestValidateField(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		lo      int
		hi      int
		fname   string
		names   map[string]int
		wantErr bool
	}{
		{"star", "*", 0, 59, "minute", nil, false},
		{"star step", "*/5", 0, 59, "minute", nil, false},
		{"exact", "5", 0, 59, "minute", nil, false},
		{"range", "1-5", 0, 59, "minute", nil, false},
		{"list", "1,3,5", 0, 59, "minute", nil, false},
		{"range step", "1-10/2", 0, 59, "minute", nil, false},
		{"weekday seven", "7", 0, 7, "weekday", cronWeekdayNames, false},
		{"weekday name", "mon", 0, 7, "weekday", cronWeekdayNames, false},
		{"month name", "jul", 1, 12, "month", cronMonthNames, false},

		{"step zero", "*/0", 0, 59, "minute", nil, true},
		{"step negative", "*/-1", 0, 59, "minute", nil, true},
		{"step non-numeric", "*/x", 0, 59, "minute", nil, true},
		{"value too big", "60", 0, 59, "minute", nil, true},
		{"value below lo", "0", 1, 31, "day", nil, true},
		{"range upper out of bounds", "1-99", 0, 59, "minute", nil, true},
		{"reversed range", "5-2", 0, 59, "minute", nil, true},
		{"bad value", "x", 0, 59, "minute", nil, true},
		{"bad range value", "a-5", 0, 59, "minute", nil, true},
		{"unknown name", "xyz", 0, 7, "weekday", cronWeekdayNames, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateField(tc.field, tc.lo, tc.hi, tc.fname, tc.names)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateField(%q, %d, %d, %q) error = %v, wantErr %v",
					tc.field, tc.lo, tc.hi, tc.fname, err, tc.wantErr)
			}
		})
	}
}
