package observability

import (
	"sync"
	"time"
)

// Timer records labeled durations between Mark() calls.
//
// It is safe for concurrent use: a single Timer may be shared by the agent
// loop and the parallel tool goroutines without risking a "concurrent map
// writes" fatal error.
type Timer struct {
	mu    sync.Mutex
	start time.Time
	last  time.Time
	marks map[string]float64
}

func NewTimer() *Timer {
	now := time.Now()
	return &Timer{start: now, last: now, marks: map[string]float64{}}
}

func (t *Timer) Mark(name string) {
	now := time.Now()
	t.mu.Lock()
	t.marks[name] = round6(now.Sub(t.last).Seconds())
	t.last = now
	t.mu.Unlock()
}

func (t *Timer) AsMap() map[string]float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]float64, len(t.marks)+1)
	for k, v := range t.marks {
		out[k] = v
	}
	out["total_s"] = round6(time.Since(t.start).Seconds())
	return out
}

func round6(v float64) float64 {
	return float64(int64(v*1e6+0.5)) / 1e6
}
