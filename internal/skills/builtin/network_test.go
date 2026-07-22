package builtin

import (
	"reflect"
	"testing"
)

func TestParsePorts(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    []int
		wantErr bool
	}{
		{"single", "80", []int{80}, false},
		{"list", "80,443,8080", []int{80, 443, 8080}, false},
		{"range", "20-23", []int{20, 21, 22, 23}, false},
		{"mixed", "22,80-82", []int{22, 80, 81, 82}, false},
		{"whitespace", " 80 , 443 ", []int{80, 443}, false},
		{"non_numeric", "abc", nil, true},
		{"reversed_range", "10-5", nil, true},
		{"bad_in_list", "80,bad", nil, true},
		{"trailing_comma", "80,", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parsePorts(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("parsePorts(%q) = %v, want error", c.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePorts(%q) unexpected error: %v", c.in, err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("parsePorts(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestParseWhois(t *testing.T) {
	raw := `% IANA WHOIS server comment
# another comment
Registrar: Example Registrar, Inc.
Creation Date: 2020-01-01T00:00:00Z
Created: 1999-01-01T00:00:00Z
Name Server: NS1.EXAMPLE.COM
Name Server: NS2.EXAMPLE.COM
Domain Status: clientTransferProhibited
Status: active
this line has no colon
Registry Expiry Date: 2030-01-01T00:00:00Z
`
	out := parseWhois(raw)

	t.Run("first_value_wins", func(t *testing.T) {
		if out["registrar"] != "Example Registrar, Inc." {
			t.Errorf("registrar = %#v", out["registrar"])
		}
		// "Creation Date" precedes "Created" (both map to creation_date); first wins.
		if out["creation_date"] != "2020-01-01T00:00:00Z" {
			t.Errorf("creation_date = %#v", out["creation_date"])
		}
		if out["expiry_date"] != "2030-01-01T00:00:00Z" {
			t.Errorf("expiry_date = %#v", out["expiry_date"])
		}
	})

	t.Run("multi_value_accumulates", func(t *testing.T) {
		if ns, _ := out["nameservers"].([]string); !reflect.DeepEqual(ns, []string{"NS1.EXAMPLE.COM", "NS2.EXAMPLE.COM"}) {
			t.Errorf("nameservers = %#v", out["nameservers"])
		}
		// Both "Domain Status" and "Status" map to status.
		if st, _ := out["status"].([]string); !reflect.DeepEqual(st, []string{"clientTransferProhibited", "active"}) {
			t.Errorf("status = %#v", out["status"])
		}
	})

	t.Run("comment_and_bare_lines_skipped", func(t *testing.T) {
		for _, k := range []string{"iana", "another", "this line has no colon"} {
			if _, ok := out[k]; ok {
				t.Errorf("unexpected key %q parsed from a comment/bare line", k)
			}
		}
	})
}
