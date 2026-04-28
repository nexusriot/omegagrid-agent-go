package builtin

import (
	"crypto/md5"  //nolint:gosec
	"crypto/rand"
	"crypto/sha1" //nolint:gosec
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/netip"
	"strings"

	"github.com/google/uuid"
)

// ── Base64 ───────────────────────────────────────────────────────────────────

func Base64Schema() Skill {
	return Skill{Name: "base64_skill", Description: "Encode or decode a string using Base64.",
		Parameters: map[string]Param{
			"action": {Type: "string", Description: "'encode' or 'decode'", Required: true},
			"text":   {Type: "string", Description: "Input text", Required: true},
		}}
}

func Base64() Executor {
	return func(args map[string]any) (any, error) {
		action := str(args, "action")
		text := str(args, "text")
		switch strings.ToLower(action) {
		case "encode":
			return map[string]any{"action": "encode", "input": text, "output": base64.StdEncoding.EncodeToString([]byte(text))}, nil
		case "decode":
			dec, err := base64.StdEncoding.DecodeString(text)
			if err != nil {
				return map[string]any{"action": "decode", "input": text, "error": err.Error()}, nil
			}
			return map[string]any{"action": "decode", "input": text, "output": string(dec)}, nil
		default:
			return map[string]any{"error": "action must be 'encode' or 'decode'"}, nil
		}
	}
}

// ── Hash ─────────────────────────────────────────────────────────────────────

func HashSchema() Skill {
	return Skill{Name: "hash_skill", Description: "Hash a string using MD5, SHA1, SHA256, or SHA512.",
		Parameters: map[string]Param{
			"text":      {Type: "string", Description: "Text to hash", Required: true},
			"algorithm": {Type: "string", Description: "md5 / sha1 / sha256 / sha512 (default sha256)", Required: false},
		}}
}

func Hash() Executor {
	return func(args map[string]any) (any, error) {
		text := str(args, "text")
		algo := strings.ToLower(str(args, "algorithm"))
		if algo == "" {
			algo = "sha256"
		}
		var h string
		switch algo {
		case "md5":
			s := md5.Sum([]byte(text)) //nolint:gosec
			h = hex.EncodeToString(s[:])
		case "sha1":
			s := sha1.Sum([]byte(text)) //nolint:gosec
			h = hex.EncodeToString(s[:])
		case "sha256":
			s := sha256.Sum256([]byte(text))
			h = hex.EncodeToString(s[:])
		case "sha512":
			s := sha512.Sum512([]byte(text))
			h = hex.EncodeToString(s[:])
		default:
			return map[string]any{"error": fmt.Sprintf("unsupported algorithm: %s", algo)}, nil
		}
		return map[string]any{"algorithm": algo, "input_length": len(text), "hash": h}, nil
	}
}

// ── UUID Gen ─────────────────────────────────────────────────────────────────

func UuidGenSchema() Skill {
	return Skill{Name: "uuid_gen", Description: "Generate UUIDs (v1/v3/v4/v5).",
		Parameters: map[string]Param{
			"version":   {Type: "number", Description: "UUID version: 1, 3, 4, or 5 (default 4)", Required: false},
			"count":     {Type: "number", Description: "Number to generate 1-50 (default 1)", Required: false},
			"namespace": {Type: "string", Description: "Namespace for v3/v5: dns, url, oid, x500 (default dns)", Required: false},
			"name":      {Type: "string", Description: "Name for v3/v5 (required for those versions)", Required: false},
		}}
}

func UuidGen() Executor {
	return func(args map[string]any) (any, error) {
		version := intOr(args, "version", 4)
		count := intOr(args, "count", 1)
		if count < 1 {
			count = 1
		}
		if count > 50 {
			count = 50
		}

		var ns uuid.UUID
		switch str(args, "namespace") {
		case "url":
			ns = uuid.NameSpaceURL
		case "oid":
			ns = uuid.NameSpaceOID
		case "x500":
			ns = uuid.NameSpaceX500
		default:
			ns = uuid.NameSpaceDNS
		}
		name := str(args, "name")

		var uuids []string
		for i := 0; i < count; i++ {
			var u uuid.UUID
			switch version {
			case 1:
				u = uuid.Must(uuid.NewUUID())
			case 3:
				u = uuid.NewMD5(ns, []byte(name))
			case 5:
				u = uuid.NewSHA1(ns, []byte(name))
			default:
				u = uuid.New()
			}
			uuids = append(uuids, u.String())
		}
		return map[string]any{"version": version, "count": count, "uuids": uuids}, nil
	}
}

// ── Password Gen ─────────────────────────────────────────────────────────────

func PasswordGenSchema() Skill {
	return Skill{Name: "password_gen", Description: "Generate cryptographically secure passwords.",
		Parameters: map[string]Param{
			"length":            {Type: "number", Description: "Password length 8-128 (default 16)", Required: false},
			"count":             {Type: "number", Description: "How many to generate 1-50 (default 1)", Required: false},
			"use_uppercase":     {Type: "boolean", Description: "Include uppercase letters (default true)", Required: false},
			"use_lowercase":     {Type: "boolean", Description: "Include lowercase letters (default true)", Required: false},
			"use_digits":        {Type: "boolean", Description: "Include digits (default true)", Required: false},
			"use_symbols":       {Type: "boolean", Description: "Include symbols (default true)", Required: false},
			"exclude_ambiguous": {Type: "boolean", Description: "Exclude 0/O/l/1/I (default false)", Required: false},
		}}
}

func PasswordGen() Executor {
	return func(args map[string]any) (any, error) {
		length := intOr(args, "length", 16)
		if length < 8 {
			length = 8
		}
		if length > 128 {
			length = 128
		}
		count := intOr(args, "count", 1)
		if count < 1 {
			count = 1
		}
		if count > 50 {
			count = 50
		}
		upper := boolOr(args, "use_uppercase", true)
		lower := boolOr(args, "use_lowercase", true)
		digits := boolOr(args, "use_digits", true)
		symbols := boolOr(args, "use_symbols", true)
		noAmb := boolOr(args, "exclude_ambiguous", false)

		var pool strings.Builder
		if upper {
			chars := "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
			if noAmb {
				chars = strings.NewReplacer("O", "", "I", "").Replace(chars)
			}
			pool.WriteString(chars)
		}
		if lower {
			chars := "abcdefghijklmnopqrstuvwxyz"
			if noAmb {
				chars = strings.NewReplacer("l", "").Replace(chars)
			}
			pool.WriteString(chars)
		}
		if digits {
			chars := "0123456789"
			if noAmb {
				chars = strings.NewReplacer("0", "", "1", "").Replace(chars)
			}
			pool.WriteString(chars)
		}
		if symbols {
			pool.WriteString("!@#$%^&*()-_=+[]{}|;:,.<>?")
		}
		charset := pool.String()
		if charset == "" {
			return map[string]any{"error": "no character classes selected"}, nil
		}

		passwords := make([]string, count)
		for i := range passwords {
			p := make([]byte, length)
			for j := range p {
				idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
				p[j] = charset[idx.Int64()]
			}
			passwords[i] = string(p)
		}
		return map[string]any{
			"length":            length,
			"count":             count,
			"passwords":         passwords,
			"exclude_ambiguous": noAmb,
		}, nil
	}
}

// ── CIDR Calc ────────────────────────────────────────────────────────────────

func CidrCalcSchema() Skill {
	return Skill{Name: "cidr_calc", Description: "Calculate CIDR network details.",
		Parameters: map[string]Param{
			"cidr":     {Type: "string", Description: "CIDR notation, e.g. '192.168.1.0/24'", Required: true},
			"check_ip": {Type: "string", Description: "Optional IP to check against the CIDR", Required: false},
		}}
}

func CidrCalc() Executor {
	return func(args map[string]any) (any, error) {
		cidr := str(args, "cidr")
		if cidr == "" {
			return map[string]any{"error": "cidr is required"}, nil
		}
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			return map[string]any{"error": fmt.Sprintf("invalid CIDR: %v", err)}, nil
		}
		prefix = prefix.Masked()
		addr := prefix.Addr()
		bits := prefix.Bits()
		total := uint64(1) << (addr.BitLen() - bits)

		var firstHost, lastHost, broadcast string
		if addr.Is4() {
			net4 := prefix.Masked()
			first := net4.Addr().As4()
			first[3]++
			last := firstAddrPlus(net4.Addr().As4(), total-2)
			bc := firstAddrPlus(net4.Addr().As4(), total-1)
			firstHost = netip.AddrFrom4(first).String()
			lastHost = netip.AddrFrom4(last).String()
			broadcast = netip.AddrFrom4(bc).String()
		} else {
			firstHost = "N/A (IPv6)"
			lastHost = "N/A (IPv6)"
		}

		usable := int64(total) - 2
		if usable < 0 {
			usable = 0
		}

		out := map[string]any{
			"cidr":             cidr,
			"version":          map[bool]string{true: "IPv4", false: "IPv6"}[addr.Is4()],
			"network_address":  prefix.Addr().String(),
			"prefix_length":    bits,
			"total_addresses":  total,
			"usable_hosts":     usable,
			"first_host":       firstHost,
			"last_host":        lastHost,
			"is_private":       addr.IsPrivate(),
			"is_global":        addr.IsGlobalUnicast(),
			"is_multicast":     addr.IsMulticast(),
			"is_loopback":      addr.IsLoopback(),
			"is_link_local":    addr.IsLinkLocalUnicast(),
		}
		if addr.Is4() {
			out["broadcast_address"] = broadcast
		}
		checkIP := str(args, "check_ip")
		if checkIP != "" {
			parsed, err := netip.ParseAddr(checkIP)
			out["check_ip"] = checkIP
			out["check_ip_in_network"] = err == nil && prefix.Contains(parsed)
		}
		return out, nil
	}
}

func firstAddrPlus(base [4]byte, n uint64) [4]byte {
	val := uint64(base[0])<<24 | uint64(base[1])<<16 | uint64(base[2])<<8 | uint64(base[3])
	val += n
	return [4]byte{byte(val >> 24), byte(val >> 16), byte(val >> 8), byte(val)}
}
