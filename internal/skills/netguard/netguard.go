// Package netguard is the SSRF guard shared by every skill that opens an
// outbound connection. Skill arguments come from the LLM, so a URL or host is
// attacker-influenced the moment a prompt injection lands in a scraped page or
// a tool result.
//
// The check runs in net.Dialer.Control, i.e. against the address actually being
// connected to after DNS resolution. Validating the hostname up front is not
// enough: a name can resolve to 169.254.169.254 (or re-resolve to it between a
// check and the dial — DNS rebinding), and an allowed URL can redirect to a
// forbidden one. Control sees every one of those dials, redirects included.
//
// Caveat: when an HTTP(S) proxy is configured through the environment, the
// transport dials the proxy, so the guard vets the proxy address and not the
// final target.
package netguard

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync/atomic"
	"syscall"
	"time"
)

// allowPrivate gates loopback, RFC1918/ULA and CGNAT targets. It defaults to
// true — the agent is used for LAN administration — and is turned off from
// config (SKILL_ALLOW_PRIVATE_NETWORKS=false) by skills.New.
var allowPrivate atomic.Bool

func init() { allowPrivate.Store(true) }

// SetAllowPrivate sets whether loopback and private ranges may be dialled.
// Link-local, metadata, unspecified and multicast addresses are refused
// regardless.
func SetAllowPrivate(v bool) {
	allowPrivate.Store(v)
	// Pooled connections were vetted under the previous policy.
	shared.CloseIdleConnections()
}

// AllowPrivate reports the current private-network policy.
func AllowPrivate() bool { return allowPrivate.Load() }

var (
	// AWS IMDS over IPv6; the IPv4 endpoint is covered by link-local.
	ec2MetadataV6 = netip.MustParseAddr("fd00:ec2::254")
	// "This network": Linux routes 0.x.x.x to the local host.
	thisNetwork = netip.MustParsePrefix("0.0.0.0/8")
	// RFC 6598 shared address space (carrier-grade NAT).
	cgnat = netip.MustParsePrefix("100.64.0.0/10")
)

// ErrBlocked is returned (wrapped) for a refused destination.
type ErrBlocked struct {
	Addr   netip.Addr
	Reason string
}

func (e *ErrBlocked) Error() string {
	return fmt.Sprintf("destination %s is blocked (%s)", e.Addr, e.Reason)
}

// CheckAddr reports whether ip may be dialled under the current policy.
func CheckAddr(ip netip.Addr) error {
	return checkAddr(ip, allowPrivate.Load())
}

func checkAddr(ip netip.Addr, allowPriv bool) error {
	ip = ip.Unmap() // ::ffff:169.254.169.254 is the same host
	switch {
	case !ip.IsValid():
		return &ErrBlocked{Addr: ip, Reason: "invalid address"}
	case ip.IsUnspecified() || thisNetwork.Contains(ip):
		return &ErrBlocked{Addr: ip, Reason: "unspecified address"}
	case ip.IsLinkLocalUnicast():
		return &ErrBlocked{Addr: ip, Reason: "link-local / cloud metadata"}
	case ip == ec2MetadataV6:
		return &ErrBlocked{Addr: ip, Reason: "cloud metadata"}
	case ip.IsMulticast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast():
		return &ErrBlocked{Addr: ip, Reason: "multicast"}
	}
	if allowPriv {
		return nil
	}
	switch {
	case ip.IsLoopback():
		return &ErrBlocked{Addr: ip, Reason: "loopback; set SKILL_ALLOW_PRIVATE_NETWORKS=true to allow"}
	case ip.IsPrivate() || cgnat.Contains(ip):
		return &ErrBlocked{Addr: ip, Reason: "private network; set SKILL_ALLOW_PRIVATE_NETWORKS=true to allow"}
	}
	return nil
}

// control is the net.Dialer.Control hook. address is the resolved "ip:port".
func control(network, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return &ErrBlocked{Reason: fmt.Sprintf("unparseable dial address %q", address)}
	}
	return CheckAddr(ap.Addr())
}

// Dialer returns a net.Dialer whose every connection attempt is vetted.
// timeout <= 0 means no dial timeout, as with net.Dialer.
func Dialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second, Control: control}
}

// DialTimeout is net.DialTimeout through the guarded dialer.
func DialTimeout(network, address string, timeout time.Duration) (net.Conn, error) {
	return Dialer(timeout).Dial(network, address)
}

// DialContext dials through the guarded dialer.
func DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return Dialer(30*time.Second).DialContext(ctx, network, address)
}

// Transport returns a fresh http.Transport (http.DefaultTransport's settings)
// that dials through the guard.
func Transport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = DialContext
	return t
}

// shared is the transport behind Client. Skills such as http_health build a
// client per call; sharing one transport keeps their idle connections pooled
// instead of leaking a pool per call.
var shared = Transport()

// Client returns an http.Client with the guarded transport.
func Client(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: shared}
}
