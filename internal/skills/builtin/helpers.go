package builtin

// Skill and Param are defined locally to avoid an import cycle with the
// parent skills package.  skills/client.go converts these to skills.Skill.
type Skill struct {
	Name        string
	Description string
	Parameters  map[string]Param
	Body        string
}

type Param struct {
	Type        string
	Description string
	Required    bool
}

// Executor is the function signature for skill execution.
type Executor = func(map[string]any) (any, error)

func str(args map[string]any, key string) string {
	if v, ok := args[key]; ok {
		switch x := v.(type) {
		case string:
			return x
		default:
			return ""
		}
	}
	return ""
}

func intOr(args map[string]any, key string, def int) int {
	if v, ok := args[key]; ok {
		switch x := v.(type) {
		case float64:
			return int(x)
		case int:
			return x
		}
	}
	return def
}

func floatOr(args map[string]any, key string, def float64) float64 {
	if v, ok := args[key]; ok {
		switch x := v.(type) {
		case float64:
			return x
		case int:
			return float64(x)
		}
	}
	return def
}

func boolOr(args map[string]any, key string, def bool) bool {
	if v, ok := args[key]; ok {
		switch x := v.(type) {
		case bool:
			return x
		}
	}
	return def
}
