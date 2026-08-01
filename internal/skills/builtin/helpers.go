package builtin

import (
	"encoding/json"
	"strconv"
	"strings"
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
