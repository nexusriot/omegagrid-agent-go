package builtin

import (
	"strings"
	"testing"
)

func TestBase64(t *testing.T) {
	enc := runExec(t, Base64(), map[string]any{"action": "encode", "text": "hello"})
	assertSubset(t, enc, map[string]any{"action": "encode", "input": "hello", "output": "aGVsbG8="})

	dec := runExec(t, Base64(), map[string]any{"action": "decode", "text": "aGVsbG8="})
	assertSubset(t, dec, map[string]any{"action": "decode", "input": "aGVsbG8=", "output": "hello"})

	t.Run("roundtrip", func(t *testing.T) {
		enc := runExec(t, Base64(), map[string]any{"action": "encode", "text": "The quick brown fox"})
		dec := runExec(t, Base64(), map[string]any{"action": "decode", "text": enc["output"].(string)})
		if dec["output"] != "The quick brown fox" {
			t.Fatalf("roundtrip = %#v", dec["output"])
		}
	})

	t.Run("decode_error", func(t *testing.T) {
		m := runExec(t, Base64(), map[string]any{"action": "decode", "text": "!!!not base64!!!"})
		if _, ok := m["error"]; !ok {
			t.Fatalf("expected error key, got %#v", m)
		}
	})

	t.Run("bad_action", func(t *testing.T) {
		m := runExec(t, Base64(), map[string]any{"action": "frobnicate", "text": "x"})
		if m["error"] != "action must be 'encode' or 'decode'" {
			t.Fatalf("error = %#v", m["error"])
		}
	})
}

func TestHash(t *testing.T) {
	// Known vectors for the input "abc".
	cases := []struct {
		algo string
		want string
	}{
		{"md5", "900150983cd24fb0d6963f7d28e17f72"},
		{"sha1", "a9993e364706816aba3e25717850c26c9cd0d89d"},
		{"sha256", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
		{"sha512", "ddaf35a193617abacc417349ae20413112e6fa4e89a97ea20a9eeee64b55d39a2192992a274fc1a836ba3c23a3feebbd454d4423643ce80e2a9ac94fa54ca49f"},
	}
	for _, c := range cases {
		t.Run(c.algo, func(t *testing.T) {
			m := runExec(t, Hash(), map[string]any{"text": "abc", "algorithm": c.algo})
			assertSubset(t, m, map[string]any{"algorithm": c.algo, "input_length": 3, "hash": c.want})
		})
	}

	t.Run("default_sha256", func(t *testing.T) {
		m := runExec(t, Hash(), map[string]any{"text": "abc"})
		assertSubset(t, m, map[string]any{"algorithm": "sha256",
			"hash": "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"})
	})

	t.Run("unsupported_algo", func(t *testing.T) {
		m := runExec(t, Hash(), map[string]any{"text": "abc", "algorithm": "crc32"})
		if s, _ := m["error"].(string); !strings.Contains(s, "unsupported algorithm") {
			t.Fatalf("error = %#v", m["error"])
		}
	})
}

func TestUuidGen(t *testing.T) {
	t.Run("v4_count", func(t *testing.T) {
		m := runExec(t, UuidGen(), map[string]any{"version": 4, "count": 3})
		ids := m["uuids"].([]string)
		if len(ids) != 3 {
			t.Fatalf("want 3 uuids, got %d", len(ids))
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if len(id) != 36 {
				t.Errorf("malformed uuid %q", id)
			}
			if seen[id] {
				t.Errorf("duplicate v4 uuid %q", id)
			}
			seen[id] = true
		}
	})

	t.Run("default_count_one", func(t *testing.T) {
		m := runExec(t, UuidGen(), map[string]any{})
		if ids := m["uuids"].([]string); len(ids) != 1 {
			t.Fatalf("default count = %d, want 1", len(ids))
		}
		if m["version"] != 4 {
			t.Fatalf("default version = %#v, want 4", m["version"])
		}
	})

	t.Run("v3_v5_deterministic", func(t *testing.T) {
		a := runExec(t, UuidGen(), map[string]any{"version": 5, "namespace": "dns", "name": "example.com"})
		b := runExec(t, UuidGen(), map[string]any{"version": 5, "namespace": "dns", "name": "example.com"})
		if a["uuids"].([]string)[0] != b["uuids"].([]string)[0] {
			t.Fatalf("v5 not deterministic: %v vs %v", a["uuids"], b["uuids"])
		}
		v3 := runExec(t, UuidGen(), map[string]any{"version": 3, "namespace": "dns", "name": "example.com"})
		if v3["uuids"].([]string)[0] == a["uuids"].([]string)[0] {
			t.Fatal("v3 and v5 should differ for the same namespace+name")
		}
	})

	t.Run("v3_v5_require_name", func(t *testing.T) {
		for _, v := range []int{3, 5} {
			m := runExec(t, UuidGen(), map[string]any{"version": v})
			if m["error"] != "name is required for UUID v3/v5" {
				t.Errorf("v%d without name: error = %#v", v, m["error"])
			}
		}
	})

	t.Run("count_clamped", func(t *testing.T) {
		m := runExec(t, UuidGen(), map[string]any{"count": 999})
		if ids := m["uuids"].([]string); len(ids) != 50 {
			t.Fatalf("count clamp high = %d, want 50", len(ids))
		}
	})
}

func TestPasswordGen(t *testing.T) {
	t.Run("default_length_16", func(t *testing.T) {
		m := runExec(t, PasswordGen(), map[string]any{})
		pws := m["passwords"].([]string)
		if len(pws) != 1 || len(pws[0]) != 16 {
			t.Fatalf("default: count=%d len=%d", len(pws), len(pws[0]))
		}
	})

	t.Run("length_clamped", func(t *testing.T) {
		lo := runExec(t, PasswordGen(), map[string]any{"length": 2})
		if got := len(lo["passwords"].([]string)[0]); got != 8 {
			t.Errorf("length floor = %d, want 8", got)
		}
		hi := runExec(t, PasswordGen(), map[string]any{"length": 9999})
		if got := len(hi["passwords"].([]string)[0]); got != 128 {
			t.Errorf("length ceil = %d, want 128", got)
		}
	})

	t.Run("count", func(t *testing.T) {
		m := runExec(t, PasswordGen(), map[string]any{"count": 5, "length": 12})
		if pws := m["passwords"].([]string); len(pws) != 5 {
			t.Fatalf("count = %d, want 5", len(pws))
		}
	})

	t.Run("no_classes_error", func(t *testing.T) {
		m := runExec(t, PasswordGen(), map[string]any{
			"use_uppercase": false, "use_lowercase": false,
			"use_digits": false, "use_symbols": false,
		})
		if m["error"] != "no character classes selected" {
			t.Fatalf("error = %#v", m["error"])
		}
	})

	t.Run("exclude_ambiguous", func(t *testing.T) {
		m := runExec(t, PasswordGen(), map[string]any{
			"length": 128, "count": 20, "use_symbols": false, "exclude_ambiguous": true,
		})
		for _, pw := range m["passwords"].([]string) {
			if strings.ContainsAny(pw, "0O1lI") {
				t.Fatalf("ambiguous char in %q", pw)
			}
		}
	})

	t.Run("digits_only", func(t *testing.T) {
		m := runExec(t, PasswordGen(), map[string]any{
			"length": 20, "use_uppercase": false, "use_lowercase": false,
			"use_digits": true, "use_symbols": false,
		})
		pw := m["passwords"].([]string)[0]
		for _, r := range pw {
			if r < '0' || r > '9' {
				t.Fatalf("non-digit %q in %q", r, pw)
			}
		}
	})
}

func TestCidrCalc(t *testing.T) {
	t.Run("missing_cidr", func(t *testing.T) {
		m := runExec(t, CidrCalc(), map[string]any{})
		if m["error"] != "cidr is required" {
			t.Fatalf("error = %#v", m["error"])
		}
	})

	t.Run("invalid_cidr", func(t *testing.T) {
		m := runExec(t, CidrCalc(), map[string]any{"cidr": "not-a-cidr"})
		if s, _ := m["error"].(string); !strings.Contains(s, "invalid CIDR") {
			t.Fatalf("error = %#v", m["error"])
		}
	})

	t.Run("ipv4_24", func(t *testing.T) {
		m := runExec(t, CidrCalc(), map[string]any{"cidr": "192.168.1.0/24"})
		assertSubset(t, m, map[string]any{
			"cidr":              "192.168.1.0/24",
			"version":           "IPv4",
			"network_address":   "192.168.1.0",
			"prefix_length":     24,
			"total_addresses":   uint64(256),
			"usable_hosts":      int64(254),
			"first_host":        "192.168.1.1",
			"last_host":         "192.168.1.254",
			"broadcast_address": "192.168.1.255",
			"is_private":        true,
		})
	})

	t.Run("check_ip", func(t *testing.T) {
		in := runExec(t, CidrCalc(), map[string]any{"cidr": "192.168.1.0/24", "check_ip": "192.168.1.5"})
		if in["check_ip_in_network"] != true {
			t.Errorf("192.168.1.5 in /24 = %#v, want true", in["check_ip_in_network"])
		}
		out := runExec(t, CidrCalc(), map[string]any{"cidr": "192.168.1.0/24", "check_ip": "10.0.0.1"})
		if out["check_ip_in_network"] != false {
			t.Errorf("10.0.0.1 in /24 = %#v, want false", out["check_ip_in_network"])
		}
	})

	t.Run("slash31_point_to_point", func(t *testing.T) {
		m := runExec(t, CidrCalc(), map[string]any{"cidr": "192.168.1.0/31"})
		assertSubset(t, m, map[string]any{
			"total_addresses": uint64(2),
			"usable_hosts":    int64(2),
			"first_host":      "192.168.1.0",
			"last_host":       "192.168.1.1",
		})
	})

	t.Run("slash32_single_host", func(t *testing.T) {
		m := runExec(t, CidrCalc(), map[string]any{"cidr": "192.168.1.5/32"})
		assertSubset(t, m, map[string]any{
			"total_addresses": uint64(1),
			"usable_hosts":    int64(1),
			"first_host":      "192.168.1.5",
			"last_host":       "192.168.1.5",
		})
	})

	t.Run("ipv6_large_prefix", func(t *testing.T) {
		m := runExec(t, CidrCalc(), map[string]any{"cidr": "2001:db8::/32"})
		assertSubset(t, m, map[string]any{
			"version":         "IPv6",
			"first_host":      "N/A (IPv6)",
			"total_addresses": "2^96",
		})
		if _, ok := m["broadcast_address"]; ok {
			t.Error("IPv6 result should not carry a broadcast_address")
		}
	})
}
