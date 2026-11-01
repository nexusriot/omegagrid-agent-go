//go:build linux

package builtin

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"golang.org/x/sys/unix"
)

// traceUDP sends one UDP probe per TTL from an unprivileged socket with
// IP_RECVERR set: the kernel queues the ICMP "time exceeded" (from a router)
// or "port unreachable" (from the target) on the socket's error queue, with
// the address of the host that sent it. No raw socket, so no root.
func traceUDP(ctx context.Context, dst netip.Addr, maxHops int, wait time.Duration) ([]hopResult, bool, error) {
	var hops []hopResult
	for ttl := 1; ttl <= maxHops; ttl++ {
		if err := ctx.Err(); err != nil {
			return hops, false, err
		}
		hop, done, err := probeHop(ctx, dst, ttl, wait)
		if err != nil {
			return hops, false, err
		}
		hops = append(hops, hop)
		if done {
			return hops, true, nil
		}
	}
	return hops, false, nil
}

func probeHop(ctx context.Context, dst netip.Addr, ttl int, wait time.Duration) (hopResult, bool, error) {
	hop := hopResult{Hop: ttl}
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, unix.IPPROTO_UDP)
	if err != nil {
		return hop, false, fmt.Errorf("socket: %w", err)
	}
	defer unix.Close(fd)
	if err := unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_TTL, ttl); err != nil {
		return hop, false, err
	}
	if err := unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_RECVERR, 1); err != nil {
		return hop, false, err
	}
	sa := &unix.SockaddrInet4{Port: tracerouteBasePort + ttl, Addr: dst.As4()}
	start := time.Now()
	if err := unix.Sendto(fd, []byte("omegagrid-traceroute"), 0, sa); err != nil {
		return hop, false, fmt.Errorf("send: %w", err)
	}
	deadline := start.Add(wait)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			hop.Timeout = true
			return hop, false, nil
		}
		slice := min(left, 100*time.Millisecond) // re-check ctx between polls
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLERR}}
		n, err := unix.Poll(fds, int(slice.Milliseconds())+1)
		if err != nil && !errors.Is(err, unix.EINTR) {
			return hop, false, err
		}
		if ctx.Err() != nil {
			return hop, false, ctx.Err()
		}
		if n == 0 || fds[0].Revents&unix.POLLERR == 0 {
			continue
		}
		from, icmpType, ok := readErrQueue(fd)
		if !ok {
			continue
		}
		hop.Address = from.String()
		hop.RTTms = float64(time.Since(start).Microseconds()) / 1000
		// ICMP type 3 is destination unreachable: the probe reached the
		// target (port unreachable) or something final refused it.
		return hop, icmpType == 3, nil
	}
}

// readErrQueue pops one extended error and returns the ICMP sender and type.
func readErrQueue(fd int) (netip.Addr, uint8, bool) {
	buf := make([]byte, 512)
	oob := make([]byte, 512)
	_, oobn, _, _, err := unix.Recvmsg(fd, buf, oob, unix.MSG_ERRQUEUE|unix.MSG_DONTWAIT)
	if err != nil {
		return netip.Addr{}, 0, false
	}
	msgs, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil {
		return netip.Addr{}, 0, false
	}
	for _, m := range msgs {
		if m.Header.Level != unix.SOL_IP || m.Header.Type != unix.IP_RECVERR {
			continue
		}
		// struct sock_extended_err (16 bytes), then the offender sockaddr_in.
		if len(m.Data) < 16+8 {
			continue
		}
		origin, icmpType := m.Data[4], m.Data[5]
		if origin != unix.SO_EE_ORIGIN_ICMP && origin != unix.SO_EE_ORIGIN_LOCAL {
			continue
		}
		family := binary.NativeEndian.Uint16(m.Data[16:18])
		if family != unix.AF_INET {
			continue
		}
		addr := netip.AddrFrom4([4]byte(m.Data[20:24]))
		return addr, icmpType, true
	}
	return netip.Addr{}, 0, false
}
