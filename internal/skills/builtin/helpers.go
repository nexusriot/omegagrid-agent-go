package builtin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Skill and Param are defined locally to avoid an import cycle with the
// parent skills package.  skills/client.go converts these to skills.Skill.
type Skill struct {
	Name        string
	Description string
	Parameters  map[string]Param
}

type Param struct {
	Type        string
	Description string
	Required    bool
}

// Executor is the function signature for skill execution.
type Executor = func(map[string]any) (any, error)

// The accessors below deliberately accept the wrong JSON type for a parameter.
// Every arg map originates from an LLM, which routinely quotes numbers
// ("port": "443"), unquotes strings ("ports": 443) and spells booleans as words
// ("use_symbols": "true"). Insisting on the declared type silently substituted
// the default instead — port_scan would scan its stock port list, ping_check
// would talk to port 80, and nothing in the result said why.

// str returns args[key] as a string, rendering scalars (numbers, booleans)
// rather than discarding them. Composite values (objects, arrays) yield "".
func str(args map[string]any, key string) string {
	switch x := args[key].(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case json.Number:
		return x.String()
	default:
		return ""
	}
}

func intOr(args map[string]any, key string, def int) int {
	if f, ok := numArg(args[key]); ok {
		return int(f)
	}
	return def
}

func floatOr(args map[string]any, key string, def float64) float64 {
	if f, ok := numArg(args[key]); ok {
		return f
	}
	return def
}

func boolOr(args map[string]any, key string, def bool) bool {
	switch x := args[key].(type) {
	case bool:
		return x
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "1", "yes", "on":
			return true
		case "false", "0", "no", "off":
			return false
		}
	case float64:
		return x != 0
	case int:
		return x != 0
	}
	return def
}

// numArg coerces a JSON scalar to float64, reporting whether it was numeric.
func numArg(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}

// maxTimeoutSec bounds every user-supplied timeout. A model asking for
// timeout=86400 would otherwise park the agent step (and one of the parallel
// tool slots) for a day on a blackholed host.
const maxTimeoutSec = 60

// clampTimeout returns v bounded to (0, max], substituting def for a
// non-positive or non-numeric value. A zero timeout is not "fast": for
// net.DialTimeout and http.Client it means no timeout at all.
func clampTimeout(v, def, max float64) float64 {
	if !(v > 0) { // also catches NaN
		return def
	}
	if v > max {
		return max
	}
	return v
}

// maxBodyBytes caps how much of a remote response body a skill reads into
// memory. A hostile or merely huge endpoint could otherwise exhaust the
// process with one tool call.
const maxBodyBytes = 2 << 20

// readCapped reads at most limit bytes of r, reporting whether more remained.
func readCapped(r io.Reader, limit int64) ([]byte, bool, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if int64(len(b)) > limit {
		return b[:limit], true, err
	}
	return b, false, err
}

// checkHTTPURL accepts only absolute http(s) URLs with a host. The skills that
// take a URL from the model are HTTP clients; anything else is a mistake or
// an attempt to reach something they were not meant to.
func checkHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url: %v", err)
	}
	if s := strings.ToLower(u.Scheme); s != "http" && s != "https" {
		return fmt.Errorf("unsupported url scheme %q (only http and https are allowed)", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("invalid url: missing host")
	}
	return nil
}

// trimPartialRune drops an incomplete UTF-8 sequence at the end of b, as left
// by cutting a buffer at a byte limit. Bytes before it are untouched.
func trimPartialRune(b []byte) []byte {
	for i := 1; i <= utf8.UTFMax && i <= len(b); i++ {
		if utf8.RuneStart(b[len(b)-i]) {
			if !utf8.FullRune(b[len(b)-i:]) {
				return b[:len(b)-i]
			}
			return b
		}
	}
	return b
}
