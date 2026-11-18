package builtin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func run(t *testing.T, e ContextExecutor, args map[string]any) map[string]any {
	t.Helper()
	res, err := e(context.Background(), args)
	if err != nil {
		t.Fatalf("hard error: %v", err)
	}
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("result is %T", res)
	}
	return m
}

func TestUptimeCheckRecordsHistoryAndStateChanges(t *testing.T) {
	var down atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte("all good"))
	}))
	defer srv.Close()
	exec := UptimeCheckContext(StateDB{Path: filepath.Join(t.TempDir(), "s.db")})

	m := run(t, exec, map[string]any{"url": srv.URL, "body_contains": "good"})
	if m["up"] != true || m["first_check"] != true || m["changed"] != false {
		t.Fatalf("first check: %v", m)
	}
	down.Store(true)
	m = run(t, exec, map[string]any{"url": srv.URL})
	if m["up"] != false || m["changed"] != true || m["state"] != "down" {
		t.Fatalf("down check: %v", m)
	}
	if !strings.Contains(m["reason"].(string), "503") {
		t.Fatalf("reason: %v", m["reason"])
	}
	pct := m["uptime_pct"].(map[string]any)
	if pct["24h"] != 50.0 {
		t.Fatalf("uptime = %v", pct)
	}
	m = run(t, exec, map[string]any{"url": srv.URL})
	if m["changed"] != false {
		t.Fatal("still down must not be a change")
	}
	h := run(t, exec, map[string]any{"url": srv.URL, "action": "history", "history": 2})
	if len(h["recent"].([]map[string]any)) != 2 || h["checks_30d"] != 3 {
		t.Fatalf("history: %v", h)
	}
	if _, ok := h["up"]; ok {
		t.Fatal("history must not run a check")
	}
	f := run(t, exec, map[string]any{"url": srv.URL, "action": "forget"})
	if f["deleted"] != int64(3) {
		t.Fatalf("forget: %v", f)
	}
	if m := run(t, exec, map[string]any{"url": "ftp://x"}); m["error"] == nil {
		t.Fatal("non-http url accepted")
	}
}

func TestSslLabsPollsUntilReady(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n > 1 && r.URL.Query().Get("startNew") != "" {
			t.Error("poll restarted the assessment")
		}
		if n < 3 {
			_, _ = w.Write([]byte(`{"host":"example.com","status":"IN_PROGRESS","endpoints":[{"ipAddress":"1.2.3.4","progress":40}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"host":"example.com","status":"READY","testTime":1700000000000,"endpoints":[
			{"ipAddress":"1.2.3.4","grade":"A+","details":{"protocols":[{"name":"TLS","version":"1.3"}],"forwardSecrecy":4,"hstsPolicy":{"status":"present"}}},
			{"ipAddress":"1.2.3.5","grade":"B","details":{"protocols":[{"name":"TLS","version":"1.0"}],"forwardSecrecy":4}}]}`))
	}))
	defer srv.Close()
	exec := SslLabsGradeContext(SslLabsConfig{BaseURL: srv.URL, PollEvery: 10 * time.Millisecond}, 5)
	m := run(t, exec, map[string]any{"host": "https://Example.com/", "fresh": true})
	if m["grade"] != "B" {
		t.Fatalf("worst grade: %v", m)
	}
	eps := m["endpoints"].([]map[string]any)
	if w, _ := eps[1]["warnings"].([]string); len(w) == 0 || !strings.Contains(w[0], "legacy") {
		t.Fatalf("warnings: %v", eps[1])
	}
	if m := run(t, exec, map[string]any{"host": "10.0.0.1"}); m["error"] == nil {
		t.Fatal("IP accepted")
	}
}

func TestSslLabsReturnsInProgressWhenOutOfTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"host":"example.com","status":"IN_PROGRESS","endpoints":[{"progress":20}]}`))
	}))
	defer srv.Close()
	exec := SslLabsGradeContext(SslLabsConfig{BaseURL: srv.URL, PollEvery: time.Second}, 5)
	m := run(t, exec, map[string]any{"host": "example.com", "wait_sec": 0})
	if m["status"] != "IN_PROGRESS" || m["progress_pct"] != 20 {
		t.Fatalf("%v", m)
	}
}

func TestDnsPropagation(t *testing.T) {
	old := propagationLookup
	defer func() { propagationLookup = old }()
	propagationLookup = func(_ context.Context, server, _, _ string) ([]string, error) {
		switch {
		case strings.HasPrefix(server, "1.1.1.1"), strings.HasPrefix(server, "8.8.8.8"):
			return []string{"203.0.113.9"}, nil
		case strings.HasPrefix(server, "77.88.8.8"):
			return nil, errors.New("i/o timeout")
		default:
			return []string{"198.51.100.1"}, nil
		}
	}
	m := run(t, DnsPropagationContext(), map[string]any{"name": "example.com", "expected": "203.0.113.9"})
	if m["matching"] != 2 || m["answered"] != 15 {
		t.Fatalf("%v", m)
	}
	if m["propagation_pct"] != 13.33 {
		t.Fatalf("pct %v", m["propagation_pct"])
	}
	// Without expected, the majority answer is the reference.
	m = run(t, DnsPropagationContext(), map[string]any{"name": "example.com"})
	if m["majority_answer"] != "198.51.100.1" || m["matching"] != 13 {
		t.Fatalf("%v", m)
	}
}
