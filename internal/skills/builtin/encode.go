package builtin

import (
	"crypto/md5" //nolint:gosec
	"crypto/rand"
	"crypto/sha1" //nolint:gosec
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

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
			dec, err := decodeBase64Any(text)
			if err != nil {
				return map[string]any{"action": "decode", "input": text, "error": err.Error()}, nil
			}
			return map[string]any{"action": "decode", "input": text, "output": string(dec)}, nil
		default:
			return map[string]any{"error": "action must be 'encode' or 'decode'"}, nil
		}
	}
}

// decodeBase64Any accepts all four common base64 spellings: standard and
// URL-safe alphabets, padded or not. Only padded-standard used to decode, so a
// URL-safe token (a JWT segment, an API key) came back as an error even though
// the input was perfectly valid base64.
func decodeBase64Any(text string) ([]byte, error) {
	text = strings.TrimSpace(text)
	encodings := []*base64.Encoding{
		base64.StdEncoding, base64.URLEncoding,
		base64.RawStdEncoding, base64.RawURLEncoding,
	}
	var firstErr error
	for _, enc := range encodings {
		dec, err := enc.DecodeString(text)
		if err == nil {
			return dec, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, firstErr
}

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
		if (version == 3 || version == 5) && name == "" {
			return map[string]any{"error": "name is required for UUID v3/v5"}, nil
		}

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

func JwtInspectSchema() Skill {
	return Skill{Name: "jwt_inspect", Description: "Decode a JWT header and payload without verifying the signature. Flags alg=none and exp/nbf timing.",
		Parameters: map[string]Param{
			"token": {Type: "string", Description: "JWT string (header.payload.signature)", Required: true},
		}}
}

func JwtInspect() Executor {
	return func(args map[string]any) (any, error) {
		token := strings.TrimSpace(str(args, "token"))
		if token == "" {
			return map[string]any{"error": "token is required"}, nil
		}
		// Strip optional "Bearer " prefix models often leave on.
		if len(token) > 7 && strings.EqualFold(token[:7], "bearer ") {
			token = strings.TrimSpace(token[7:])
		}
		parts := strings.Split(token, ".")
		if len(parts) < 2 || len(parts) > 3 {
			return map[string]any{"error": "token must have 2 or 3 dot-separated segments"}, nil
		}

		headerRaw, err := decodeBase64Any(parts[0])
		if err != nil {
			return map[string]any{"error": "header decode: " + err.Error()}, nil
		}
		payloadRaw, err := decodeBase64Any(parts[1])
		if err != nil {
			return map[string]any{"error": "payload decode: " + err.Error()}, nil
		}

		var header any
		if err := json.Unmarshal(headerRaw, &header); err != nil {
			return map[string]any{"error": "header is not JSON: " + err.Error(), "header_raw": string(headerRaw)}, nil
		}
		var payload any
		if err := json.Unmarshal(payloadRaw, &payload); err != nil {
			return map[string]any{"error": "payload is not JSON: " + err.Error(), "payload_raw": string(payloadRaw)}, nil
		}

		out := map[string]any{
			"header":            header,
			"payload":           payload,
			"segments":          len(parts),
			"signature_present": len(parts) == 3 && parts[2] != "",
			"verified":          false, // intentionally never verifies
		}
		if len(parts) == 3 {
			out["signature_b64"] = parts[2]
			out["signature_length"] = len(parts[2])
		}

		// Structured flags from common claims — helps the model without it
		// re-parsing JSON itself.
		var warnings []string
		if hm, ok := header.(map[string]any); ok {
			// alg is reported whatever its value; "none" additionally raises a
			// flag. Leaving it out for the "none" case hid the very field a
			// reader looks for first.
			if alg, _ := hm["alg"].(string); alg != "" {
				out["alg"] = alg
				if strings.EqualFold(alg, "none") {
					out["alg_none"] = true
					warnings = append(warnings, "alg is 'none' (unsigned token)")
				}
			}
			if kid, ok := hm["kid"]; ok {
				out["kid"] = kid
			}
		}
		if pm, ok := payload.(map[string]any); ok {
			now := time.Now().Unix()
			if exp, ok := jwtNumericClaim(pm["exp"]); ok {
				out["exp"] = exp
				out["exp_rfc3339"] = time.Unix(exp, 0).UTC().Format(time.RFC3339)
				out["expired"] = now >= exp
				if now >= exp {
					warnings = append(warnings, "token is expired")
				}
			}
			if nbf, ok := jwtNumericClaim(pm["nbf"]); ok {
				out["nbf"] = nbf
				out["nbf_rfc3339"] = time.Unix(nbf, 0).UTC().Format(time.RFC3339)
				out["not_yet_valid"] = now < nbf
				if now < nbf {
					warnings = append(warnings, "token not yet valid (nbf)")
				}
			}
			if iat, ok := jwtNumericClaim(pm["iat"]); ok {
				out["iat"] = iat
				out["iat_rfc3339"] = time.Unix(iat, 0).UTC().Format(time.RFC3339)
			}
			for _, k := range []string{"iss", "sub", "aud", "jti"} {
				if v, ok := pm[k]; ok {
					out[k] = v
				}
			}
		}
		if len(warnings) > 0 {
			out["warnings"] = warnings
		}
		return out, nil
	}
}

// jwtNumericClaim accepts JSON numbers and numeric strings (LLMs sometimes
// re-emit claims as strings after round-tripping).
func jwtNumericClaim(v any) (int64, bool) {
	switch x := v.(type) {
	case float64:
		return int64(x), true
	case int:
		return int64(x), true
	case int64:
		return x, true
	case json.Number:
		i, err := x.Int64()
		return i, err == nil
	case string:
		i, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return i, err == nil
	}
	return 0, false
}

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
		hostBits := addr.BitLen() - bits

		// total as uint64 overflows for IPv6 prefixes shorter than /64
		// (shift >= 64 wraps to 0); report those as a power-of-two string.
		var total any
		var total64 uint64
		if hostBits < 64 {
			total64 = uint64(1) << hostBits
			total = total64
		} else {
			total = fmt.Sprintf("2^%d", hostBits)
		}

		var firstHost, lastHost, broadcast string
		var usable any
		if addr.Is4() {
			base := prefix.Addr().As4()
			bc := firstAddrPlus(base, total64-1)
			broadcast = netip.AddrFrom4(bc).String()
			if total64 >= 4 {
				// Normal subnet: network + broadcast are reserved.
				first := firstAddrPlus(base, 1)
				last := firstAddrPlus(base, total64-2)
				firstHost = netip.AddrFrom4(first).String()
				lastHost = netip.AddrFrom4(last).String()
				usable = int64(total64) - 2
			} else {
				// /31 (point-to-point, RFC 3021) and /32 (single host): every
				// address is usable; the old total-2 arithmetic underflowed here.
				firstHost = netip.AddrFrom4(base).String()
				lastHost = broadcast
				usable = int64(total64)
			}
		} else {
			firstHost = "N/A (IPv6)"
			lastHost = "N/A (IPv6)"
			usable = total // IPv6 has no network/broadcast reservation
		}

		out := map[string]any{
			"cidr":            cidr,
			"version":         map[bool]string{true: "IPv4", false: "IPv6"}[addr.Is4()],
			"network_address": prefix.Addr().String(),
			"prefix_length":   bits,
			"total_addresses": total,
			"usable_hosts":    usable,
			"first_host":      firstHost,
			"last_host":       lastHost,
			"is_private":      addr.IsPrivate(),
			"is_global":       addr.IsGlobalUnicast(),
			"is_multicast":    addr.IsMulticast(),
			"is_loopback":     addr.IsLoopback(),
			"is_link_local":   addr.IsLinkLocalUnicast(),
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
