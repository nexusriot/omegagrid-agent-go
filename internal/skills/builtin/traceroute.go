package builtin

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/nexusriot/omegagrid-agent-go/internal/skills/netguard"
)

func TracerouteSchema() Skill {
	return Skill{Name: "traceroute", Description: "Trace the network path to an IPv4 host hop by hop (UDP probes, no root needed; Linux only). " +
		"Each hop reports the router address and round-trip time; '*' means no reply.",
		Parameters: map[string]Param{
			"host":     {Type: "string", Description: "Hostname or IPv4 address", Required: true},
			"max_hops": {Type: "number", Description: "Max TTL (default 20, max 30)", Required: false},
			"timeout":  {Type: "number", Description: "Seconds to wait per hop (default 1, max 5)", Required: false},
			"names":    {Type: "boolean", Description: "Reverse-resolve hop addresses (default false)", Required: false},
		}}
}

// tracerouteBasePort is where UDP probes start (classic traceroute's 33434).
const tracerouteBasePort = 33434

// hopResult is what one TTL produced.
type hopResult struct {
	Hop     int     `json:"hop"`
	Address string  `json:"address,omitempty"`
	Name    string  `json:"name,omitempty"`
	RTTms   float64 `json:"rtt_ms,omitempty"`
	Timeout bool    `json:"timeout,omitempty"`
}

// Traceroute is TracerouteContext without cancellation.
func Traceroute() Executor { return withoutContext(TracerouteContext()) }

func TracerouteContext() ContextExecutor {
	return func(ctx context.Context, args map[string]any) (any, error) {
		host := strings.TrimSpace(str(args, "host"))
		if host == "" || !isHostname(host) {
			return map[string]any{"error": fmt.Sprintf("invalid host %q", host)}, nil
		}
		maxHops := min(max(intOr(args, "max_hops", 20), 1), 30)
		wait := time.Duration(clampTimeout(floatOr(args, "timeout", 1), 1, 5) * float64(time.Second))

		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
		if err != nil || len(ips) == 0 {
			return map[string]any{"host": host, "error": "no IPv4 address for host"}, nil
		}
		dst := ips[0].Unmap()
		if err := netguard.CheckAddr(dst); err != nil {
			return map[string]any{"host": host, "error": err.Error()}, nil
		}
		hops, reached, err := traceUDP(ctx, dst, maxHops, wait)
		if err != nil {
			return map[string]any{"host": host, "error": err.Error()}, nil
		}
		if boolOr(args, "names", false) {
			for i := range hops {
				if hops[i].Address == "" {
					continue
				}
				lctx, cancel := context.WithTimeout(ctx, 2*time.Second)
				if names, err := net.DefaultResolver.LookupAddr(lctx, hops[i].Address); err == nil && len(names) > 0 {
					hops[i].Name = strings.TrimSuffix(names[0], ".")
				}
				cancel()
			}
		}
		return map[string]any{"host": host, "address": dst.String(), "reached": reached, "hops": hops}, nil
	}
}
