package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Cancelling the context must abort an in-flight completion instead of
// waiting out the (minutes-long) client timeout.
func TestCompleteJSONContextCancels(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	c := NewOpenAIChat("k", srv.URL, "m", "", "", nil, 60)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	_, _, err := Complete(ctx, c, []Message{{Role: "user", Content: "hi"}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if time.Since(t0) > 5*time.Second {
		t.Fatal("cancellation did not abort the request promptly")
	}
}

func TestReadBodyCapped(t *testing.T) {
	if _, err := readBody(strings.NewReader(strings.Repeat("x", maxResponseBytes+1))); err == nil {
		t.Fatal("oversized body accepted")
	}
}
