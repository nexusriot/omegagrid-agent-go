package netguard

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestCheckAddrPolicy(t *testing.T) {
	tests := []struct {
		ip          string
		allowedPriv bool // verdict with private networks allowed
		allowedPub  bool // verdict with private networks blocked
	}{
		{"8.8.8.8", true, true},
		{"2606:4700::1111", true, true},
		// Always blocked.
		{"169.254.169.254", false, false},
		{"::ffff:169.254.169.254", false, false},
		{"fe80::1", false, false},
		{"fd00:ec2::254", false, false},
		{"0.0.0.0", false, false},
		{"0.1.2.3", false, false},
		{"::", false, false},
		{"224.0.0.1", false, false},
		{"ff02::1", false, false},
		// Blocked only when private networks are off.
		{"127.0.0.1", true, false},
		{"::1", true, false},
		{"10.1.2.3", true, false},
		{"172.16.0.1", true, false},
		{"192.168.1.1", true, false},
		{"fd12:3456::1", true, false},
		{"100.64.0.1", true, false},
		{"::ffff:10.0.0.1", true, false},
	}
	for _, tt := range tests {
		ip := netip.MustParseAddr(tt.ip)
		if got := checkAddr(ip, true) == nil; got != tt.allowedPriv {
			t.Errorf("checkAddr(%s, allowPrivate=true) allowed=%v, want %v", tt.ip, got, tt.allowedPriv)
		}
		if got := checkAddr(ip, false) == nil; got != tt.allowedPub {
			t.Errorf("checkAddr(%s, allowPrivate=false) allowed=%v, want %v", tt.ip, got, tt.allowedPub)
		}
	}
}

func withAllowPrivate(t *testing.T, v bool) {
	t.Helper()
	prev := AllowPrivate()
	SetAllowPrivate(v)
	t.Cleanup(func() { SetAllowPrivate(prev) })
}

// The check must run on the resolved address at dial time: "localhost" is a
// name, and only the dial hook sees that it lands on 127.0.0.1.
func TestClientBlocksLoopbackByNameWhenPrivateDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	withAllowPrivate(t, false)
	_, err := Client(5 * time.Second).Get("http://localhost:" + port + "/")
	var blocked *ErrBlocked
	if !errors.As(err, &blocked) {
		t.Fatalf("GET localhost with private networks blocked: err = %v, want *ErrBlocked", err)
	}

	SetAllowPrivate(true)
	resp, err := Client(5 * time.Second).Get(srv.URL)
	if err != nil {
		t.Fatalf("GET loopback with private networks allowed: %v", err)
	}
	resp.Body.Close()
}

// A permitted first hop must not be able to redirect into a forbidden one.
func TestClientBlocksRedirectToMetadata(t *testing.T) {
	withAllowPrivate(t, true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer srv.Close()
	_, err := Client(5 * time.Second).Get(srv.URL)
	var blocked *ErrBlocked
	if !errors.As(err, &blocked) {
		t.Fatalf("redirect to metadata: err = %v, want *ErrBlocked", err)
	}
}

func TestDialTimeoutBlocksMetadata(t *testing.T) {
	withAllowPrivate(t, true)
	_, err := DialTimeout("tcp", "169.254.169.254:80", time.Second)
	var blocked *ErrBlocked
	if !errors.As(err, &blocked) {
		t.Fatalf("dial metadata: err = %v, want *ErrBlocked", err)
	}
}
