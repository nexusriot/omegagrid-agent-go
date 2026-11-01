//go:build !linux

package builtin

import (
	"context"
	"errors"
	"net/netip"
	"time"
)

func traceUDP(context.Context, netip.Addr, int, time.Duration) ([]hopResult, bool, error) {
	return nil, false, errors.New("traceroute needs Linux (unprivileged ICMP error queue)")
}
