package observability

import (
	"sync"
	"testing"
	"time"
)

func TestTimer_BasicMark(t *testing.T) {
	timer := NewTimer()
	time.Sleep(5 * time.Millisecond)
	timer.Mark("step1")
	time.Sleep(5 * time.Millisecond)
	timer.Mark("step2")

	m := timer.AsMap()
	if _, ok := m["step1"]; !ok {
		t.Error("expected 'step1' in marks")
	}
	if _, ok := m["step2"]; !ok {
		t.Error("expected 'step2' in marks")
	}
	if _, ok := m["total_s"]; !ok {
		t.Error("expected 'total_s' in AsMap output")
	}
	if m["total_s"] <= 0 {
		t.Errorf("total_s should be positive, got %v", m["total_s"])
	}
}

// TestTimer_ConcurrentMark verifies there is no data race when multiple
// goroutines call Mark simultaneously.  Run with -race to catch races.
func TestTimer_ConcurrentMark(t *testing.T) {
	timer := NewTimer()
	var wg sync.WaitGroup
	const goroutines = 50
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			// Alternate between marking and reading so both code-paths
			// are exercised concurrently.
			if n%2 == 0 {
				timer.Mark("concurrent")
			} else {
				_ = timer.AsMap()
			}
		}(i)
	}
	wg.Wait()
}

func TestTimer_AsMapDoesNotMutateOriginal(t *testing.T) {
	timer := NewTimer()
	timer.Mark("a")

	m1 := timer.AsMap()
	m1["injected"] = 999.0

	m2 := timer.AsMap()
	if _, ok := m2["injected"]; ok {
		t.Error("AsMap returned the internal map (mutation leaked)")
	}
}

func TestRound6(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{0.0, 0.0},
		{1.0, 1.0},
		{0.1234567, 0.123457},
		{0.123456, 0.123456},
	}
	for _, tc := range cases {
		got := round6(tc.in)
		if got != tc.want {
			t.Errorf("round6(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
