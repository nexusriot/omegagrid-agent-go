package builtin

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nexusriot/omegagrid-agent-go/internal/skills/netguard"
)

func DnsLookupSchema() Skill {
	return Skill{Name: "dns_lookup", Description: "Perform a DNS lookup for a domain.",
		Parameters: map[string]Param{
			"domain":      {Type: "string", Description: "Domain to look up", Required: true},
			"record_type": {Type: "string", Description: "Record type: A, AAAA, MX, TXT, CNAME, NS (default A)", Required: false},
		}}
}

var dnsRecordTypes = map[string]bool{"A": true, "AAAA": true, "MX": true, "TXT": true, "CNAME": true, "NS": true}

// DnsLookup is DnsLookupContext without cancellation.
func DnsLookup() Executor {
	return withoutContext(DnsLookupContext())
}

func DnsLookupContext() ContextExecutor {
	return func(ctx context.Context, args map[string]any) (any, error) {
		domain := str(args, "domain")
		if domain == "" {
			return map[string]any{"error": "domain is required"}, nil
		}
		if !isHostname(domain) {
			return map[string]any{"error": fmt.Sprintf("invalid domain: %q", domain)}, nil
		}
		rtype := strings.ToUpper(strings.TrimSpace(str(args, "record_type")))
		if rtype == "" {
			rtype = "A"
		}
		// Validated before the dig branch, not only in the stdlib fallback:
		// rtype is an argv entry too, and "@10.0.0.53" made dig query that
		// server directly — past netguard and SKILL_ALLOW_PRIVATE_NETWORKS.
		if !dnsRecordTypes[rtype] {
			return map[string]any{"error": fmt.Sprintf("unsupported record type: %s (use A, AAAA, MX, TXT, CNAME or NS)", rtype)}, nil
		}

		var records []string
		var method string

		// Try dig first; fall back to stdlib. The isHostname guard above is what
		// makes passing `domain` as an argv entry safe: dig reads leading "-"/"@"
		// tokens as its own options (`-f <file>`, `@<server>`), so an unvalidated
		// domain from the model could redirect the query or read a local file.
		if digPath, err := exec.LookPath("dig"); err == nil {
			out, err := exec.CommandContext(ctx, digPath, "+short", "+time=5", domain, rtype).Output()
			if err == nil {
				method = "dig"
				for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
					if l := strings.TrimSpace(line); l != "" {
						records = append(records, l)
					}
				}
			}
		}

		if method == "" {
			method = "stdlib"
			switch rtype {
			case "A":
				addrs, _ := net.DefaultResolver.LookupHost(ctx, domain)
				for _, a := range addrs {
					if net.ParseIP(a).To4() != nil {
						records = append(records, a)
					}
				}
			case "AAAA":
				addrs, _ := net.DefaultResolver.LookupHost(ctx, domain)
				for _, a := range addrs {
					if net.ParseIP(a).To4() == nil {
						records = append(records, a)
					}
				}
			case "MX":
				mxs, _ := net.DefaultResolver.LookupMX(ctx, domain)
				for _, mx := range mxs {
					records = append(records, fmt.Sprintf("%d %s", mx.Pref, mx.Host))
				}
			case "TXT":
				txts, _ := net.DefaultResolver.LookupTXT(ctx, domain)
				records = append(records, txts...)
			case "NS":
				nss, _ := net.DefaultResolver.LookupNS(ctx, domain)
				for _, ns := range nss {
					records = append(records, ns.Host)
				}
			case "CNAME":
				cname, _ := net.DefaultResolver.LookupCNAME(ctx, domain)
				if cname != "" {
					records = append(records, cname)
				}
			default:
				return map[string]any{"error": fmt.Sprintf("unsupported record type: %s", rtype)}, nil
			}
		}

		return map[string]any{
			"domain":  domain,
			"type":    rtype,
			"records": records,
			"count":   len(records),
			"method":  method,
		}, nil
	}
}

// isHostname reports whether s is a plausible DNS name or IP literal: labels of
// letters, digits and hyphens, no leading dash, nothing shell- or argv-special.
func isHostname(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	s = strings.TrimSuffix(s, ".")
	if net.ParseIP(s) != nil {
		return true
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
				(c >= '0' && c <= '9') || c == '-' || c == '_'
			if !ok {
				return false
			}
		}
	}
	return true
}

func PingCheckSchema() Skill {
	return Skill{Name: "ping_check", Description: "Check if a host is reachable (TCP connect).",
		Parameters: map[string]Param{
			"host":    {Type: "string", Description: "Hostname or IP", Required: true},
			"port":    {Type: "number", Description: "TCP port (default 80)", Required: false},
			"timeout": {Type: "number", Description: "Timeout in seconds (default 5)", Required: false},
		}}
}

// PingCheck is PingCheckContext without cancellation.
func PingCheck() Executor {
	return withoutContext(PingCheckContext())
}

func PingCheckContext() ContextExecutor {
	return func(ctx context.Context, args map[string]any) (any, error) {
		host := str(args, "host")
		if host == "" {
			return map[string]any{"error": "host is required"}, nil
		}
		port := intOr(args, "port", 80)
		if port < 1 || port > 65535 {
			return map[string]any{"host": host, "reachable": false, "error": fmt.Sprintf("port %d outside 1-65535", port)}, nil
		}
		timeout := clampTimeout(floatOr(args, "timeout", 5), 5, maxTimeoutSec)

		t0 := time.Now()
		addrs, err := net.DefaultResolver.LookupHost(ctx, host)
		dnsMs := time.Since(t0).Milliseconds()
		if err != nil {
			return map[string]any{"host": host, "reachable": false, "error": err.Error(), "dns_ms": dnsMs}, nil
		}
		if len(addrs) == 0 {
			return map[string]any{"host": host, "reachable": false, "error": "no addresses returned for host", "dns_ms": dnsMs}, nil
		}
		resolvedIP := addrs[0]

		t1 := time.Now()
		conn, err := netguard.Dialer(time.Duration(timeout*float64(time.Second))).DialContext(ctx, "tcp", net.JoinHostPort(resolvedIP, strconv.Itoa(port)))
		connectMs := time.Since(t1).Milliseconds()
		if err != nil {
			out := map[string]any{
				"host": host, "resolved_ip": resolvedIP, "port": port,
				"reachable": false, "dns_ms": dnsMs, "connect_ms": connectMs,
				"total_ms": dnsMs + connectMs,
			}
			// A refused destination is not "unreachable" — say why.
			var blocked *netguard.ErrBlocked
			if errors.As(err, &blocked) {
				out["error"] = blocked.Error()
			}
			return out, nil
		}
		_ = conn.Close()
		return map[string]any{
			"host": host, "resolved_ip": resolvedIP, "port": port,
			"reachable": true, "dns_ms": dnsMs, "connect_ms": connectMs,
			"total_ms": dnsMs + connectMs,
		}, nil
	}
}

func PortScanSchema() Skill {
	return Skill{Name: "port_scan", Description: "Scan TCP ports on a host.",
		Parameters: map[string]Param{
			"host":    {Type: "string", Description: "Hostname or IP", Required: true},
			"ports":   {Type: "string", Description: "Comma-separated ports or range, e.g. '22,80,443' or '1-1024' (default common ports)", Required: false},
			"timeout": {Type: "number", Description: "Per-port timeout in seconds (default 2)", Required: false},
		}}
}

// PortScan is PortScanContext without cancellation.
func PortScan() Executor {
	return withoutContext(PortScanContext())
}

func PortScanContext() ContextExecutor {
	return func(ctx context.Context, args map[string]any) (any, error) {
		host := str(args, "host")
		if host == "" {
			return map[string]any{"error": "host is required"}, nil
		}
		portsStr := str(args, "ports")
		if portsStr == "" {
			portsStr = "21,22,25,53,80,110,143,443,993,995,3306,3389,5432,6379,8080,8443,9200"
		}
		// Per-port, so the cap is tighter than other skills': 1024 ports at 100
		// concurrent dials already take ~10 rounds of this timeout.
		timeout := clampTimeout(floatOr(args, "timeout", 2), 2, maxPortScanTimeoutSec)

		ports, err := parsePorts(portsStr)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}

		// Resolve once and scan the address: a policy refusal then surfaces as
		// an error instead of every port silently reading as closed, and the
		// target cannot re-resolve elsewhere mid-scan.
		target, err := resolveScanTarget(ctx, host)
		if err != nil {
			return map[string]any{"host": host, "error": err.Error()}, nil
		}

		t0 := time.Now()
		var mu sync.Mutex
		var open []int
		var wg sync.WaitGroup
		sem := make(chan struct{}, 100) // max 100 concurrent

		for _, p := range ports {
			wg.Add(1)
			sem <- struct{}{}
			go func(port int) {
				defer wg.Done()
				defer func() { <-sem }()
				addr := net.JoinHostPort(target, strconv.Itoa(port))
				conn, err := netguard.Dialer(time.Duration(timeout*float64(time.Second))).DialContext(ctx, "tcp", addr)
				if err == nil {
					_ = conn.Close()
					mu.Lock()
					open = append(open, port)
					mu.Unlock()
				}
			}(p)
		}
		wg.Wait()

		sortInts(open)
		return map[string]any{
			"host":          host,
			"resolved_ip":   target,
			"open_ports":    open,
			"closed_count":  len(ports) - len(open),
			"scanned_count": len(ports),
			"elapsed_s":     time.Since(t0).Seconds(),
		}, nil
	}
}

const maxPortScanTimeoutSec = 10

// resolveScanTarget resolves host to one address and vets it against the
// network policy.
func resolveScanTarget(ctx context.Context, host string) (string, error) {
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return "", err
	}
	if len(addrs) == 0 {
		return "", fmt.Errorf("no addresses returned for host")
	}
	ip, err := netip.ParseAddr(addrs[0])
	if err != nil {
		return "", err
	}
	if err := netguard.CheckAddr(ip); err != nil {
		return "", err
	}
	return ip.String(), nil
}

// parsePorts expands a ports specification into individual port numbers.
// Bounds are enforced while parsing: an out-of-range range such as "1-1000000"
// used to expand into a million-element slice before the caller trimmed it back
// to 1024, and negative or zero ports produced dial addresses that could never
// connect. Duplicates are dropped ("80,80,80" listed an open port three times
// and inflated the counts), and expansion stops at maxScanPorts, so thousands
// of repeated "1-65535" parts can no longer allocate gigabytes first.
func parsePorts(s string) ([]int, error) {
	const minPort, maxPort = 1, 65535
	inRange := func(p int) bool { return p >= minPort && p <= maxPort }

	var out []int
	seen := make(map[int]bool)
	add := func(p int) {
		if !seen[p] && len(out) < maxScanPorts {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if strings.Contains(part, "-") {
			lr := strings.SplitN(part, "-", 2)
			lo, err1 := strconv.Atoi(strings.TrimSpace(lr[0]))
			hi, err2 := strconv.Atoi(strings.TrimSpace(lr[1]))
			if err1 != nil || err2 != nil || lo > hi {
				return nil, fmt.Errorf("invalid port range: %s", part)
			}
			if !inRange(lo) || !inRange(hi) {
				return nil, fmt.Errorf("port range %s outside %d-%d", part, minPort, maxPort)
			}
			for p := lo; p <= hi && len(out) < maxScanPorts; p++ {
				add(p)
			}
		} else {
			p, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("invalid port: %s", part)
			}
			if !inRange(p) {
				return nil, fmt.Errorf("port %d outside %d-%d", p, minPort, maxPort)
			}
			add(p)
		}
	}
	return out, nil
}

// maxScanPorts is the most ports one port_scan call probes.
const maxScanPorts = 1024

func sortInts(a []int) {
	// simple insertion sort for small slices
	for i := 1; i < len(a); i++ {
		x := a[i]
		j := i - 1
		for j >= 0 && a[j] > x {
			a[j+1] = a[j]
			j--
		}
		a[j+1] = x
	}
}

func WhoisLookupSchema() Skill {
	return Skill{Name: "whois_lookup", Description: "Perform a WHOIS lookup for a domain.",
		Parameters: map[string]Param{
			"domain": {Type: "string", Description: "Domain to look up", Required: true},
		}}
}

// WhoisLookup is WhoisLookupContext without cancellation.
func WhoisLookup() Executor {
	return withoutContext(WhoisLookupContext())
}

func WhoisLookupContext() ContextExecutor {
	return func(ctx context.Context, args map[string]any) (any, error) {
		domain := str(args, "domain")
		if domain == "" {
			return map[string]any{"error": "domain is required"}, nil
		}
		// The domain is written verbatim onto the WHOIS connection; a CR/LF in
		// it would smuggle extra query lines.
		if !isHostname(domain) {
			return map[string]any{"error": fmt.Sprintf("invalid domain: %q", domain)}, nil
		}
		raw, err := whoisQuery(ctx, "whois.iana.org", domain)
		if err != nil {
			return map[string]any{"domain": domain, "error": err.Error()}, nil
		}
		// find refer: line to get authoritative server
		server := ""
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(strings.ToLower(line), "refer:") {
				server = strings.TrimSpace(line[6:])
				break
			}
			if strings.HasPrefix(strings.ToLower(line), "whois:") {
				server = strings.TrimSpace(line[6:])
				break
			}
		}
		// The referral comes from the remote response; only follow it when it
		// is a plain hostname (whoisQuery's guarded dial vets where it lands).
		if server != "" && isHostname(server) {
			if r2, err := whoisQuery(ctx, server, domain); err == nil {
				raw = r2
			}
		}
		parsed := parseWhois(raw)
		parsed["domain"] = domain
		parsed["raw"] = raw
		return parsed, nil
	}
}

func whoisQuery(ctx context.Context, server, domain string) (string, error) {
	conn, err := netguard.Dialer(10*time.Second).DialContext(ctx, "tcp", net.JoinHostPort(server, "43"))
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	fmt.Fprintf(conn, "%s\r\n", domain)
	sc := bufio.NewScanner(io.LimitReader(conn, maxBodyBytes))
	var sb strings.Builder
	for sc.Scan() {
		sb.WriteString(sc.Text())
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

func parseWhois(raw string) map[string]any {
	out := map[string]any{}
	fieldMap := map[string]string{
		"registrar":            "registrar",
		"creation date":        "creation_date",
		"created":              "creation_date",
		"expiry date":          "expiry_date",
		"expiration date":      "expiry_date",
		"registry expiry date": "expiry_date",
		"updated date":         "updated_date",
		"last updated":         "updated_date",
		"name server":          "nameservers",
		"nserver":              "nameservers",
		"domain status":        "status",
		"status":               "status",
	}
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "%") || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:idx]))
		val := strings.TrimSpace(line[idx+1:])
		if mapped, ok := fieldMap[key]; ok {
			if mapped == "nameservers" || mapped == "status" {
				existing, _ := out[mapped].([]string)
				out[mapped] = append(existing, val)
			} else {
				if _, exists := out[mapped]; !exists {
					out[mapped] = val
				}
			}
		}
	}
	return out
}
