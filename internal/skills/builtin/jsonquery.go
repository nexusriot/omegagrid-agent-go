package builtin

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func JsonQuerySchema() Skill {
	return Skill{Name: "json_query", Description: "Pick values out of JSON with a jq-style expression: paths (.a.b, .items[0], .items[], .[\"odd key\"]), " +
		"pipes, and length, keys, values, first, last, sort, sort_by(.x), unique, reverse, add, min, max, type, flatten, " +
		"map(...), select(.x == 1) with == != > < >= <= and contains.",
		Parameters: map[string]Param{
			"json":  {Type: "string", Description: "The JSON document (a string, or an object/array value)", Required: true},
			"query": {Type: "string", Description: "Expression, e.g. .items[] | select(.status == \"open\") | .title", Required: true},
		}}
}

// JsonQuery has nothing to cancel.
func JsonQuery() Executor {
	return func(args map[string]any) (any, error) {
		var doc any
		switch v := args["json"].(type) {
		case string:
			if len(v) > maxBodyBytes {
				return map[string]any{"error": "json is larger than 2 MiB"}, nil
			}
			if err := json.Unmarshal([]byte(v), &doc); err != nil {
				return map[string]any{"error": "invalid JSON: " + err.Error()}, nil
			}
		case nil:
			return map[string]any{"error": "json is required"}, nil
		default:
			doc = v
		}
		query := strings.TrimSpace(str(args, "query"))
		if query == "" {
			query = "."
		}
		f, err := parseJQ(query)
		if err != nil {
			return map[string]any{"error": "query: " + err.Error(), "query": query}, nil
		}
		out, err := f([]any{doc})
		if err != nil {
			return map[string]any{"error": err.Error(), "query": query}, nil
		}
		if out == nil {
			out = []any{}
		}
		res := map[string]any{"query": query, "count": len(out), "results": out}
		if len(out) == 1 {
			res["result"] = out[0]
		}
		return res, nil
	}
}

// jqFilter maps a stream of values to a stream of values.
type jqFilter func(in []any) ([]any, error)

// jqParser is a small recursive-descent parser over the query text.
type jqParser struct {
	s   string
	pos int
}

func parseJQ(q string) (jqFilter, error) {
	p := &jqParser{s: q}
	f, err := p.pipeline()
	if err != nil {
		return nil, err
	}
	p.ws()
	if p.pos != len(p.s) {
		return nil, fmt.Errorf("unexpected %q at %d", p.s[p.pos:], p.pos)
	}
	return f, nil
}

func (p *jqParser) ws() {
	for p.pos < len(p.s) && strings.ContainsRune(" \t\n\r", rune(p.s[p.pos])) {
		p.pos++
	}
}

func (p *jqParser) peek(lit string) bool {
	p.ws()
	return strings.HasPrefix(p.s[p.pos:], lit)
}

func (p *jqParser) eat(lit string) bool {
	if p.peek(lit) {
		p.pos += len(lit)
		return true
	}
	return false
}

func (p *jqParser) pipeline() (jqFilter, error) {
	var stages []jqFilter
	for {
		f, err := p.term()
		if err != nil {
			return nil, err
		}
		stages = append(stages, f)
		if !p.eat("|") {
			break
		}
	}
	return func(in []any) ([]any, error) {
		cur := in
		for _, st := range stages {
			next, err := st(cur)
			if err != nil {
				return nil, err
			}
			cur = next
		}
		return cur, nil
	}, nil
}

func (p *jqParser) ident() string {
	p.ws()
	start := p.pos
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		if c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9' && p.pos > start) {
			p.pos++
			continue
		}
		break
	}
	return p.s[start:p.pos]
}

func (p *jqParser) term() (jqFilter, error) {
	p.ws()
	if p.pos >= len(p.s) {
		return nil, fmt.Errorf("expected an expression at the end")
	}
	if p.s[p.pos] == '.' {
		return p.path()
	}
	name := p.ident()
	if name == "" {
		return nil, fmt.Errorf("unexpected %q at %d", p.s[p.pos:], p.pos)
	}
	switch name {
	case "select":
		if !p.eat("(") {
			return nil, fmt.Errorf("select needs (condition)")
		}
		cond, err := p.condition()
		if err != nil {
			return nil, err
		}
		if !p.eat(")") {
			return nil, fmt.Errorf("missing ) after select")
		}
		return func(in []any) ([]any, error) {
			var out []any
			for _, v := range in {
				ok, err := cond(v)
				if err != nil {
					return nil, err
				}
				if ok {
					out = append(out, v)
				}
			}
			return out, nil
		}, nil
	case "map", "sort_by":
		if !p.eat("(") {
			return nil, fmt.Errorf("%s needs (expression)", name)
		}
		inner, err := p.pipeline()
		if err != nil {
			return nil, err
		}
		if !p.eat(")") {
			return nil, fmt.Errorf("missing ) after %s", name)
		}
		if name == "map" {
			return func(in []any) ([]any, error) {
				var out []any
				for _, v := range in {
					arr, ok := v.([]any)
					if !ok {
						return nil, fmt.Errorf("map needs an array, got %s", jqType(v))
					}
					var mapped []any
					for _, e := range arr {
						r, err := inner([]any{e})
						if err != nil {
							return nil, err
						}
						mapped = append(mapped, r...)
					}
					out = append(out, nonNilSlice(mapped))
				}
				return out, nil
			}, nil
		}
		return func(in []any) ([]any, error) {
			var out []any
			for _, v := range in {
				arr, ok := v.([]any)
				if !ok {
					return nil, fmt.Errorf("sort_by needs an array, got %s", jqType(v))
				}
				keys := make([]any, len(arr))
				for i, e := range arr {
					r, err := inner([]any{e})
					if err != nil {
						return nil, err
					}
					if len(r) > 0 {
						keys[i] = r[0]
					}
				}
				idx := make([]int, len(arr))
				for i := range idx {
					idx[i] = i
				}
				sort.SliceStable(idx, func(a, b int) bool { return jqCompare(keys[idx[a]], keys[idx[b]]) < 0 })
				sorted := make([]any, len(arr))
				for i, j := range idx {
					sorted[i] = arr[j]
				}
				out = append(out, sorted)
			}
			return out, nil
		}, nil
	}
	fn, ok := jqFuncs[name]
	if !ok {
		return nil, fmt.Errorf("unknown function %q", name)
	}
	return func(in []any) ([]any, error) {
		var out []any
		for _, v := range in {
			r, err := fn(v)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			out = append(out, r)
		}
		return out, nil
	}, nil
}

// path parses ".", ".a", ".a.b", ".a[0]", ".a[]", `.["k"]`, ".[1:3]".
func (p *jqParser) path() (jqFilter, error) {
	p.ws()
	var segs []jqFilter
	p.pos++ // the leading '.'
	for {
		if p.pos < len(p.s) && (isIdentStart(p.s[p.pos]) || p.s[p.pos] == '"') {
			key, err := p.key()
			if err != nil {
				return nil, err
			}
			segs = append(segs, jqField(key))
		}
		if p.pos < len(p.s) && p.s[p.pos] == '[' {
			p.pos++
			seg, err := p.bracket()
			if err != nil {
				return nil, err
			}
			segs = append(segs, seg)
			continue
		}
		if p.pos < len(p.s) && p.s[p.pos] == '.' && p.pos+1 < len(p.s) && (isIdentStart(p.s[p.pos+1]) || p.s[p.pos+1] == '"' || p.s[p.pos+1] == '[') {
			p.pos++
			continue
		}
		break
	}
	return func(in []any) ([]any, error) {
		cur := in
		for _, s := range segs {
			next, err := s(cur)
			if err != nil {
				return nil, err
			}
			cur = next
		}
		return cur, nil
	}, nil
}

func isIdentStart(c byte) bool { return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func (p *jqParser) key() (string, error) {
	if p.s[p.pos] == '"' {
		return p.stringLit()
	}
	return p.ident(), nil
}

func (p *jqParser) stringLit() (string, error) {
	start := p.pos
	p.pos++
	for p.pos < len(p.s) {
		switch p.s[p.pos] {
		case '\\':
			p.pos += 2
			continue
		case '"':
			p.pos++
			var s string
			if err := json.Unmarshal([]byte(p.s[start:p.pos]), &s); err != nil {
				return "", fmt.Errorf("bad string %s", p.s[start:p.pos])
			}
			return s, nil
		}
		p.pos++
	}
	return "", fmt.Errorf("unterminated string")
}

func (p *jqParser) bracket() (jqFilter, error) {
	p.ws()
	if p.eat("]") {
		return func(in []any) ([]any, error) {
			var out []any
			for _, v := range in {
				switch x := v.(type) {
				case []any:
					out = append(out, x...)
				case map[string]any:
					for _, k := range sortedKeys(x) {
						out = append(out, x[k])
					}
				case nil:
				default:
					return nil, fmt.Errorf("cannot iterate over %s", jqType(v))
				}
			}
			return out, nil
		}, nil
	}
	if p.peek(`"`) {
		k, err := p.stringLit()
		if err != nil {
			return nil, err
		}
		if !p.eat("]") {
			return nil, fmt.Errorf("missing ]")
		}
		return jqField(k), nil
	}
	start := p.pos
	for p.pos < len(p.s) && p.s[p.pos] != ']' {
		p.pos++
	}
	if p.pos >= len(p.s) {
		return nil, fmt.Errorf("missing ]")
	}
	inner := strings.TrimSpace(p.s[start:p.pos])
	p.pos++
	if lo, hi, ok := strings.Cut(inner, ":"); ok {
		return jqSlice(strings.TrimSpace(lo), strings.TrimSpace(hi))
	}
	n, err := strconv.Atoi(inner)
	if err != nil {
		return nil, fmt.Errorf("bad index [%s]", inner)
	}
	return func(in []any) ([]any, error) {
		var out []any
		for _, v := range in {
			arr, ok := v.([]any)
			if !ok {
				if v == nil {
					out = append(out, nil)
					continue
				}
				return nil, fmt.Errorf("cannot index %s with a number", jqType(v))
			}
			i := n
			if i < 0 {
				i += len(arr)
			}
			if i < 0 || i >= len(arr) {
				out = append(out, nil)
			} else {
				out = append(out, arr[i])
			}
		}
		return out, nil
	}, nil
}

func jqSlice(lo, hi string) (jqFilter, error) {
	parse := func(s string, def int) (int, bool, error) {
		if s == "" {
			return def, false, nil
		}
		n, err := strconv.Atoi(s)
		return n, true, err
	}
	return func(in []any) ([]any, error) {
		var out []any
		for _, v := range in {
			arr, ok := v.([]any)
			if !ok {
				return nil, fmt.Errorf("cannot slice %s", jqType(v))
			}
			a, _, err := parse(lo, 0)
			if err != nil {
				return nil, err
			}
			b, _, err := parse(hi, len(arr))
			if err != nil {
				return nil, err
			}
			if a < 0 {
				a += len(arr)
			}
			if b < 0 {
				b += len(arr)
			}
			a, b = min(max(a, 0), len(arr)), min(max(b, 0), len(arr))
			if a > b {
				a = b
			}
			out = append(out, append([]any{}, arr[a:b]...))
		}
		return out, nil
	}, nil
}

func jqField(key string) jqFilter {
	return func(in []any) ([]any, error) {
		var out []any
		for _, v := range in {
			switch x := v.(type) {
			case map[string]any:
				out = append(out, x[key])
			case nil:
				out = append(out, nil)
			default:
				return nil, fmt.Errorf("cannot get .%s of %s", key, jqType(v))
			}
		}
		return out, nil
	}
}

// condition parses "path [op literal]" for select.
func (p *jqParser) condition() (func(any) (bool, error), error) {
	lhs, err := p.pipelineUntilOp()
	if err != nil {
		return nil, err
	}
	p.ws()
	var op string
	for _, cand := range []string{"==", "!=", ">=", "<=", ">", "<", "contains"} {
		if p.eat(cand) {
			op = cand
			break
		}
	}
	if op == "" {
		return func(v any) (bool, error) {
			r, err := lhs([]any{v})
			if err != nil {
				return false, err
			}
			return len(r) > 0 && jqTruthy(r[0]), nil
		}, nil
	}
	rhs, err := p.literal()
	if err != nil {
		return nil, err
	}
	return func(v any) (bool, error) {
		r, err := lhs([]any{v})
		if err != nil {
			return false, err
		}
		var l any
		if len(r) > 0 {
			l = r[0]
		}
		switch op {
		case "==":
			return jqCompare(l, rhs) == 0, nil
		case "!=":
			return jqCompare(l, rhs) != 0, nil
		case ">":
			return jqCompare(l, rhs) > 0, nil
		case "<":
			return jqCompare(l, rhs) < 0, nil
		case ">=":
			return jqCompare(l, rhs) >= 0, nil
		case "<=":
			return jqCompare(l, rhs) <= 0, nil
		default: // contains
			ls, _ := l.(string)
			rs, _ := rhs.(string)
			if arr, ok := l.([]any); ok {
				for _, e := range arr {
					if jqCompare(e, rhs) == 0 {
						return true, nil
					}
				}
				return false, nil
			}
			return strings.Contains(strings.ToLower(ls), strings.ToLower(rs)), nil
		}
	}, nil
}

// pipelineUntilOp parses the left side of a comparison: a path optionally
// piped through functions, stopping at a comparison operator or ')'.
func (p *jqParser) pipelineUntilOp() (jqFilter, error) {
	var stages []jqFilter
	for {
		f, err := p.term()
		if err != nil {
			return nil, err
		}
		stages = append(stages, f)
		if !p.eat("|") {
			break
		}
	}
	return func(in []any) ([]any, error) {
		cur := in
		for _, st := range stages {
			next, err := st(cur)
			if err != nil {
				return nil, err
			}
			cur = next
		}
		return cur, nil
	}, nil
}

func (p *jqParser) literal() (any, error) {
	p.ws()
	if p.pos >= len(p.s) {
		return nil, fmt.Errorf("expected a value")
	}
	if p.s[p.pos] == '"' {
		return p.stringLit()
	}
	start := p.pos
	for p.pos < len(p.s) && !strings.ContainsRune(" )\t\n", rune(p.s[p.pos])) {
		p.pos++
	}
	tok := p.s[start:p.pos]
	var v any
	if err := json.Unmarshal([]byte(tok), &v); err != nil {
		return nil, fmt.Errorf("bad value %q (quote strings)", tok)
	}
	return v, nil
}

var jqFuncs = map[string]func(any) (any, error){
	"length": func(v any) (any, error) {
		switch x := v.(type) {
		case []any:
			return float64(len(x)), nil
		case map[string]any:
			return float64(len(x)), nil
		case string:
			return float64(len([]rune(x))), nil
		case nil:
			return float64(0), nil
		case float64:
			if x < 0 {
				return -x, nil
			}
			return x, nil
		}
		return nil, fmt.Errorf("%s has no length", jqType(v))
	},
	"keys": func(v any) (any, error) {
		switch x := v.(type) {
		case map[string]any:
			ks := sortedKeys(x)
			out := make([]any, len(ks))
			for i, k := range ks {
				out[i] = k
			}
			return out, nil
		case []any:
			out := make([]any, len(x))
			for i := range x {
				out[i] = float64(i)
			}
			return out, nil
		}
		return nil, fmt.Errorf("%s has no keys", jqType(v))
	},
	"values": func(v any) (any, error) {
		if m, ok := v.(map[string]any); ok {
			out := []any{}
			for _, k := range sortedKeys(m) {
				out = append(out, m[k])
			}
			return out, nil
		}
		if a, ok := v.([]any); ok {
			return a, nil
		}
		return nil, fmt.Errorf("%s has no values", jqType(v))
	},
	"first": func(v any) (any, error) { a, err := jqArray(v); return jqAt(a, 0), err },
	"last":  func(v any) (any, error) { a, err := jqArray(v); return jqAt(a, len(a)-1), err },
	"reverse": func(v any) (any, error) {
		a, err := jqArray(v)
		out := make([]any, len(a))
		for i := range a {
			out[i] = a[len(a)-1-i]
		}
		return out, err
	},
	"sort": func(v any) (any, error) {
		a, err := jqArray(v)
		out := append([]any{}, a...)
		sort.SliceStable(out, func(i, j int) bool { return jqCompare(out[i], out[j]) < 0 })
		return out, err
	},
	"unique": func(v any) (any, error) {
		a, err := jqArray(v)
		out := append([]any{}, a...)
		sort.SliceStable(out, func(i, j int) bool { return jqCompare(out[i], out[j]) < 0 })
		var u []any
		for i, e := range out {
			if i == 0 || jqCompare(e, out[i-1]) != 0 {
				u = append(u, e)
			}
		}
		return nonNilSlice(u), err
	},
	"add": func(v any) (any, error) {
		a, err := jqArray(v)
		if err != nil || len(a) == 0 {
			return nil, err
		}
		switch a[0].(type) {
		case string:
			var b strings.Builder
			for _, e := range a {
				s, _ := e.(string)
				b.WriteString(s)
			}
			return b.String(), nil
		case []any:
			var out []any
			for _, e := range a {
				x, _ := e.([]any)
				out = append(out, x...)
			}
			return out, nil
		}
		sum := 0.0
		for _, e := range a {
			f, ok := e.(float64)
			if !ok && e != nil {
				return nil, fmt.Errorf("cannot add %s", jqType(e))
			}
			sum += f
		}
		return sum, nil
	},
	"min":  func(v any) (any, error) { return jqExtreme(v, -1) },
	"max":  func(v any) (any, error) { return jqExtreme(v, 1) },
	"type": func(v any) (any, error) { return jqType(v), nil },
	"flatten": func(v any) (any, error) {
		a, err := jqArray(v)
		var out []any
		for _, e := range a {
			if inner, ok := e.([]any); ok {
				out = append(out, inner...)
			} else {
				out = append(out, e)
			}
		}
		return nonNilSlice(out), err
	},
	"not": func(v any) (any, error) { return !jqTruthy(v), nil },
}

func jqArray(v any) ([]any, error) {
	a, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("needs an array, got %s", jqType(v))
	}
	return a, nil
}

func jqAt(a []any, i int) any {
	if i < 0 || i >= len(a) {
		return nil
	}
	return a[i]
}

func jqExtreme(v any, sign int) (any, error) {
	a, err := jqArray(v)
	if err != nil || len(a) == 0 {
		return nil, err
	}
	best := a[0]
	for _, e := range a[1:] {
		if jqCompare(e, best)*sign > 0 {
			best = e
		}
	}
	return best, nil
}

func jqTruthy(v any) bool { return v != nil && v != false }

func jqType(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64, int, int64, json.Number:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return fmt.Sprintf("%T", v)
}

// jqCompare orders values like jq: null < false < true < numbers < strings
// < arrays < objects, numbers numerically, strings lexically.
func jqCompare(a, b any) int {
	rank := func(v any) int {
		switch x := v.(type) {
		case nil:
			return 0
		case bool:
			if x {
				return 2
			}
			return 1
		case float64, int, int64:
			return 3
		case string:
			return 4
		case []any:
			return 5
		default:
			return 6
		}
	}
	ra, rb := rank(a), rank(b)
	if ra != rb {
		return ra - rb
	}
	switch x := a.(type) {
	case float64, int, int64:
		fa, _ := numArg(x)
		fb, _ := numArg(b)
		switch {
		case fa < fb:
			return -1
		case fa > fb:
			return 1
		}
		return 0
	case string:
		return strings.Compare(x, b.(string))
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return strings.Compare(string(ja), string(jb))
}

func sortedKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func nonNilSlice(a []any) []any {
	if a == nil {
		return []any{}
	}
	return a
}
