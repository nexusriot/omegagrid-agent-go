package builtin

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// ── DateTime ─────────────────────────────────────────────────────────────────

func DateTimeSchema() Skill {
	return Skill{Name: "datetime_skill", Description: "Return the current UTC date and time.",
		Parameters: map[string]Param{}}
}

func DateTime() Executor {
	return func(_ map[string]any) (any, error) {
		now := time.Now().UTC()
		return map[string]any{
			"date":           now.Format("2006-01-02"),
			"time":           now.Format("15:04:05"),
			"day_of_week":    now.Weekday().String(),
			"iso":            now.Format(time.RFC3339),
			"unix_timestamp": now.Unix(),
		}, nil
	}
}

// ── Math Eval (safe recursive-descent parser) ─────────────────────────────

func MathEvalSchema() Skill {
	return Skill{Name: "math_eval",
		Description: "Safely evaluate a math expression. Supports +,-,*,/,//,%,**, " +
			"functions: sqrt, pow, exp, log, log2, log10, sin, cos, tan, asin, acos, " +
			"atan, atan2, ceil, floor, fabs, abs, round, min, max, factorial, " +
			"degrees, radians, gcd, hypot. Constants: pi, e, tau, inf.",
		Parameters: map[string]Param{
			"expression": {Type: "string", Description: "Math expression to evaluate", Required: true},
		}}
}

func MathEval() Executor {
	return func(args map[string]any) (any, error) {
		expr := strings.TrimSpace(str(args, "expression"))
		if expr == "" {
			return map[string]any{"error": "expression is required"}, nil
		}
		if len(expr) > 500 {
			return map[string]any{"error": "expression too long (max 500 chars)"}, nil
		}
		result, err := evalExpr(expr)
		if err != nil {
			return map[string]any{"error": err.Error(), "expression": expr}, nil
		}
		return map[string]any{"expression": expr, "result": result}, nil
	}
}

var mathFuncs = map[string]func([]float64) (float64, error){
	"sqrt":    func1(math.Sqrt),
	"exp":     func1(math.Exp),
	"log":     func1(math.Log),
	"log2":    func1(math.Log2),
	"log10":   func1(math.Log10),
	"sin":     func1(math.Sin),
	"cos":     func1(math.Cos),
	"tan":     func1(math.Tan),
	"asin":    func1(math.Asin),
	"acos":    func1(math.Acos),
	"atan":    func1(math.Atan),
	"ceil":    func1(math.Ceil),
	"floor":   func1(math.Floor),
	"fabs":    func1(math.Abs),
	"abs":     func1(math.Abs),
	"degrees": func1(func(x float64) float64 { return x * 180.0 / math.Pi }),
	"radians": func1(func(x float64) float64 { return x * math.Pi / 180.0 }),
	"round":   func1(math.Round),
	"atan2":   func2(math.Atan2),
	"pow":     func2(math.Pow),
	"hypot":   func2(math.Hypot),
	"gcd":     func2(func(a, b float64) float64 { return float64(gcdInt(int64(a), int64(b))) }),
	"min":     func2(math.Min),
	"max":     func2(math.Max),
	"factorial": func(args []float64) (float64, error) {
		if len(args) != 1 {
			return 0, fmt.Errorf("factorial takes 1 argument")
		}
		n := int64(args[0])
		if n < 0 || n > 20 {
			return 0, fmt.Errorf("factorial argument out of range")
		}
		result := int64(1)
		for i := int64(2); i <= n; i++ {
			result *= i
		}
		return float64(result), nil
	},
}

var mathConsts = map[string]float64{
	"pi":  math.Pi,
	"e":   math.E,
	"tau": math.Pi * 2,
	"inf": math.Inf(1),
}

func func1(f func(float64) float64) func([]float64) (float64, error) {
	return func(args []float64) (float64, error) {
		if len(args) != 1 {
			return 0, fmt.Errorf("expected 1 argument")
		}
		return f(args[0]), nil
	}
}

func func2(f func(float64, float64) float64) func([]float64) (float64, error) {
	return func(args []float64) (float64, error) {
		if len(args) != 2 {
			return 0, fmt.Errorf("expected 2 arguments")
		}
		return f(args[0], args[1]), nil
	}
}

func gcdInt(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	if a < 0 {
		return -a
	}
	return a
}

type parser struct {
	s   string
	pos int
}

func evalExpr(s string) (float64, error) {
	p := &parser{s: s}
	v, err := p.parseAddSub()
	if err != nil {
		return 0, err
	}
	p.skipWS()
	if p.pos != len(p.s) {
		return 0, fmt.Errorf("unexpected characters at position %d: %q", p.pos, p.s[p.pos:])
	}
	return v, nil
}

func (p *parser) skipWS() {
	for p.pos < len(p.s) && p.s[p.pos] == ' ' {
		p.pos++
	}
}

func (p *parser) parseAddSub() (float64, error) {
	left, err := p.parseMulDiv()
	if err != nil {
		return 0, err
	}
	for {
		p.skipWS()
		if p.pos >= len(p.s) {
			break
		}
		op := p.s[p.pos]
		if op != '+' && op != '-' {
			break
		}
		p.pos++
		right, err := p.parseMulDiv()
		if err != nil {
			return 0, err
		}
		if op == '+' {
			left += right
		} else {
			left -= right
		}
	}
	return left, nil
}

func (p *parser) parseMulDiv() (float64, error) {
	left, err := p.parsePow()
	if err != nil {
		return 0, err
	}
	for {
		p.skipWS()
		if p.pos >= len(p.s) {
			break
		}
		if p.pos+1 < len(p.s) && p.s[p.pos] == '/' && p.s[p.pos+1] == '/' {
			p.pos += 2
			right, err := p.parsePow()
			if err != nil {
				return 0, err
			}
			if right == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			left = math.Floor(left / right)
			continue
		}
		op := p.s[p.pos]
		if op != '*' && op != '/' && op != '%' {
			break
		}
		p.pos++
		right, err := p.parsePow()
		if err != nil {
			return 0, err
		}
		switch op {
		case '*':
			left *= right
		case '/':
			if right == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			left /= right
		case '%':
			if right == 0 {
				return 0, fmt.Errorf("modulo by zero")
			}
			left = math.Mod(left, right)
		}
	}
	return left, nil
}

func (p *parser) parsePow() (float64, error) {
	base, err := p.parseUnary()
	if err != nil {
		return 0, err
	}
	p.skipWS()
	if p.pos+1 < len(p.s) && p.s[p.pos] == '*' && p.s[p.pos+1] == '*' {
		p.pos += 2
		exp, err := p.parseUnary()
		if err != nil {
			return 0, err
		}
		return math.Pow(base, exp), nil
	}
	return base, nil
}

func (p *parser) parseUnary() (float64, error) {
	p.skipWS()
	if p.pos < len(p.s) && p.s[p.pos] == '-' {
		p.pos++
		v, err := p.parsePrimary()
		return -v, err
	}
	if p.pos < len(p.s) && p.s[p.pos] == '+' {
		p.pos++
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (float64, error) {
	p.skipWS()
	if p.pos >= len(p.s) {
		return 0, fmt.Errorf("unexpected end of expression")
	}
	// parentheses
	if p.s[p.pos] == '(' {
		p.pos++
		v, err := p.parseAddSub()
		if err != nil {
			return 0, err
		}
		p.skipWS()
		if p.pos >= len(p.s) || p.s[p.pos] != ')' {
			return 0, fmt.Errorf("expected ')'")
		}
		p.pos++
		return v, nil
	}
	// number
	if unicode.IsDigit(rune(p.s[p.pos])) || p.s[p.pos] == '.' {
		return p.parseNumber()
	}
	// identifier (function or constant)
	if unicode.IsLetter(rune(p.s[p.pos])) || p.s[p.pos] == '_' {
		name := p.parseIdent()
		p.skipWS()
		if p.pos < len(p.s) && p.s[p.pos] == '(' {
			return p.parseCall(name)
		}
		if v, ok := mathConsts[name]; ok {
			return v, nil
		}
		return 0, fmt.Errorf("unknown identifier: %s", name)
	}
	return 0, fmt.Errorf("unexpected character %q at position %d", p.s[p.pos], p.pos)
}

func (p *parser) parseNumber() (float64, error) {
	start := p.pos
	for p.pos < len(p.s) && (unicode.IsDigit(rune(p.s[p.pos])) || p.s[p.pos] == '.' || p.s[p.pos] == 'e' || p.s[p.pos] == 'E' || p.s[p.pos] == '_') {
		p.pos++
	}
	tok := strings.ReplaceAll(p.s[start:p.pos], "_", "")
	return strconv.ParseFloat(tok, 64)
}

func (p *parser) parseIdent() string {
	start := p.pos
	for p.pos < len(p.s) && (unicode.IsLetter(rune(p.s[p.pos])) || unicode.IsDigit(rune(p.s[p.pos])) || p.s[p.pos] == '_') {
		p.pos++
	}
	return p.s[start:p.pos]
}

func (p *parser) parseCall(name string) (float64, error) {
	p.pos++ // consume '('
	var args []float64
	for {
		p.skipWS()
		if p.pos < len(p.s) && p.s[p.pos] == ')' {
			p.pos++
			break
		}
		v, err := p.parseAddSub()
		if err != nil {
			return 0, err
		}
		args = append(args, v)
		p.skipWS()
		if p.pos < len(p.s) && p.s[p.pos] == ',' {
			p.pos++
		}
	}
	fn, ok := mathFuncs[name]
	if !ok {
		return 0, fmt.Errorf("unknown function: %s", name)
	}
	return fn(args)
}

// ── Cron Schedule ─────────────────────────────────────────────────────────

func CronScheduleSchema() Skill {
	return Skill{Name: "cron_schedule", Description: "Parse a cron expression, explain it, and calculate next run times.",
		Parameters: map[string]Param{
			"expression": {Type: "string", Description: "5-field cron expression, e.g. '*/15 * * * *'", Required: true},
			"count":      {Type: "number", Description: "Number of next runs to show (default 5, max 20)", Required: false},
		}}
}

func CronSchedule() Executor {
	return func(args map[string]any) (any, error) {
		expr := strings.TrimSpace(str(args, "expression"))
		if expr == "" {
			return map[string]any{"error": "expression is required"}, nil
		}
		count := intOr(args, "count", 5)
		if count < 1 {
			count = 1
		}
		if count > 20 {
			count = 20
		}

		parts := strings.Fields(expr)
		if len(parts) != 5 {
			return map[string]any{"error": fmt.Sprintf("expected 5 fields, got %d", len(parts))}, nil
		}

		fieldNames := []string{"minute", "hour", "day_of_month", "month", "day_of_week"}
		fieldRanges := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}}
		parsed := make([]map[int]bool, 5)
		for i, p := range parts {
			vs, err := parseCronField(p, fieldRanges[i][0], fieldRanges[i][1], i)
			if err != nil {
				return map[string]any{"error": err.Error()}, nil
			}
			parsed[i] = vs
		}

		explanation := cronExplain(parts)
		nextRuns := cronNextRuns(parsed, count)
		fields := make(map[string]string, 5)
		for i, n := range fieldNames {
			fields[n] = parts[i]
		}
		return map[string]any{
			"expression":  expr,
			"explanation": explanation,
			"fields":      fields,
			"next_runs":   nextRuns,
		}, nil
	}
}

var monthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
var dowNames = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

func parseCronField(field string, lo, hi, idx int) (map[int]bool, error) {
	out := map[int]bool{}
	for _, part := range strings.Split(field, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if idx == 3 {
			for k, v := range monthNames {
				part = strings.ReplaceAll(part, k, strconv.Itoa(v))
			}
		} else if idx == 4 {
			for k, v := range dowNames {
				part = strings.ReplaceAll(part, k, strconv.Itoa(v))
			}
		}
		step := 1
		if strings.Contains(part, "/") {
			lr := strings.SplitN(part, "/", 2)
			part = lr[0]
			s, err := strconv.Atoi(lr[1])
			if err != nil {
				return nil, fmt.Errorf("invalid step in %q", field)
			}
			step = s
		}
		if part == "*" {
			for i := lo; i <= hi; i += step {
				out[i] = true
			}
		} else if strings.Contains(part, "-") {
			lr := strings.SplitN(part, "-", 2)
			a, err1 := strconv.Atoi(lr[0])
			b, err2 := strconv.Atoi(lr[1])
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("invalid range in %q", field)
			}
			for i := a; i <= b; i += step {
				out[i] = true
			}
		} else {
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("invalid value %q in field", part)
			}
			out[n] = true
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("field %q produced no values", field)
	}
	return out, nil
}

func cronExplain(parts []string) string {
	minute, hour, dom, month, dow := parts[0], parts[1], parts[2], parts[3], parts[4]
	var pieces []string
	switch {
	case minute == "*":
		pieces = append(pieces, "every minute")
	case strings.HasPrefix(minute, "*/"):
		pieces = append(pieces, fmt.Sprintf("every %s minutes", minute[2:]))
	default:
		pieces = append(pieces, "at minute "+minute)
	}
	switch {
	case hour == "*":
		pieces = append(pieces, "of every hour")
	case strings.HasPrefix(hour, "*/"):
		pieces = append(pieces, fmt.Sprintf("every %s hours", hour[2:]))
	default:
		pieces = append(pieces, "at hour "+hour)
	}
	if dom != "*" {
		pieces = append(pieces, "on day "+dom+" of the month")
	}
	if month != "*" {
		pieces = append(pieces, "in month "+month)
	}
	if dow != "*" {
		dayMap := map[string]string{"0": "Sunday", "1": "Monday", "2": "Tuesday", "3": "Wednesday", "4": "Thursday", "5": "Friday", "6": "Saturday"}
		var days []string
		for _, d := range strings.Split(dow, ",") {
			if name, ok := dayMap[strings.TrimSpace(d)]; ok {
				days = append(days, name)
			} else {
				days = append(days, strings.TrimSpace(d))
			}
		}
		pieces = append(pieces, "on "+strings.Join(days, ", "))
	}
	return strings.Join(pieces, ", ")
}

func cronNextRuns(parsed []map[int]bool, count int) []string {
	now := time.Now().UTC()
	now = now.Truncate(time.Minute).Add(time.Minute)
	var results []string
	limit := 366 * 24 * 60
	for checked := 0; len(results) < count && checked < limit; checked++ {
		dow := int(now.Weekday()) // 0=Sun
		if parsed[0][now.Minute()] && parsed[1][now.Hour()] &&
			parsed[2][now.Day()] && parsed[3][int(now.Month())] && parsed[4][dow] {
			results = append(results, now.Format(time.RFC3339))
		}
		now = now.Add(time.Minute)
	}
	return results
}
