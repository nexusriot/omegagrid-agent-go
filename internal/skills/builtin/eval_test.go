package builtin

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

func runExec(t *testing.T, e Executor, args map[string]any) map[string]any {
	t.Helper()
	res, err := e(args)
	if err != nil {
		t.Fatalf("executor returned unexpected error: %v", err)
	}
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("executor result is %T, want map[string]any", res)
	}
	return m
}

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func assertSubset(t *testing.T, got, want map[string]any) {
	t.Helper()
	for k, wv := range want {
		gv, ok := got[k]
		if !ok {
			t.Errorf("missing key %q in result", k)
			continue
		}
		if !reflect.DeepEqual(gv, wv) {
			t.Errorf("key %q = %#v (%T), want %#v (%T)", k, gv, gv, wv, wv)
		}
	}
}

func mathResult(t *testing.T, expr string) float64 {
	t.Helper()
	m := runExec(t, MathEval(), map[string]any{"expression": expr})
	if e, isErr := m["error"]; isErr {
		t.Fatalf("eval(%q) returned error: %v", expr, e)
	}
	if want := strings.TrimSpace(expr); m["expression"] != want {
		t.Errorf("eval(%q) expression key = %#v, want %q", expr, m["expression"], want)
	}
	v, ok := m["result"].(float64)
	if !ok {
		t.Fatalf("eval(%q) result missing or not float64: %#v", expr, m["result"])
	}
	return v
}

func TestMathEvalValues(t *testing.T) {
	cases := []struct {
		name string
		expr string
		want float64
	}{
		{"add", "1 + 2", 3},
		{"sub", "10 - 4", 6},
		{"mul", "6 * 7", 42},
		{"true_div", "7 / 2", 3.5},
		{"floor_div", "7 // 2", 3},
		{"floor_div_negative", "-7 // 2", -4},
		{"modulo", "10 % 3", 1},
		{"power", "2 ** 10", 1024},
		{"precedence_mul_over_add", "2 + 3 * 4", 14},
		{"precedence_pow_over_mul", "2 * 3 ** 2", 18},
		{"parens_override", "(2 + 3) * 4", 20},
		{"nested_parens", "((1 + 2) * (3 + 4))", 21},
		{"unary_minus", "-5 + 2", -3},
		{"unary_minus_parens", "-(3 + 2)", -5},
		{"unary_plus", "+4", 4},
		{"binary_plus_unary", "2 + -3", -1},
		// NOTE: Python evaluates -2**2 == -4 (** binds tighter than unary minus).
		// This parser applies unary minus to the base first, yielding pow(-2,2)=4.
		{"neg_binds_base_in_pow", "-2 ** 2", 4},
		{"pow_with_negative_exp", "2 ** -1", 0.5},
		{"scientific_notation", "1.5e3", 1500},
		{"underscore_separator", "1_000 + 1", 1001},
		{"sqrt", "sqrt(16)", 4},
		{"pow_fn", "pow(2, 10)", 1024},
		{"exp_fn", "exp(0)", 1},
		{"log_is_natural", "log(e)", 1},
		{"log2", "log2(8)", 3},
		{"log10", "log10(1000)", 3},
		{"sin_zero", "sin(0)", 0},
		{"cos_zero", "cos(0)", 1},
		{"ceil", "ceil(4.1)", 5},
		{"floor_fn", "floor(4.9)", 4},
		{"abs", "abs(-7)", 7},
		{"fabs", "fabs(-7.5)", 7.5},
		{"round", "round(2.5)", 3},
		{"factorial", "factorial(5)", 120},
		{"factorial_zero", "factorial(0)", 1},
		{"gcd", "gcd(12, 18)", 6},
		{"min", "min(3, 7)", 3},
		{"max", "max(3, 7)", 7},
		{"nested_calls", "max(min(10, 4), 2)", 4},
		{"const_pi", "pi", math.Pi},
		{"const_e", "e", math.E},
		{"const_tau", "tau", math.Pi * 2},
		{"whitespace_tolerated", "  3   +   4  ", 7},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mathResult(t, c.expr)
			if !approxEqual(got, c.want) {
				t.Errorf("eval(%q) = %v, want %v", c.expr, got, c.want)
			}
		})
	}
}

func TestMathEvalInfConstant(t *testing.T) {
	m := runExec(t, MathEval(), map[string]any{"expression": "inf"})
	got, ok := m["result"].(float64)
	if !ok || !math.IsInf(got, 1) {
		t.Fatalf("eval(inf) = %#v, want +Inf", m["result"])
	}
}

func TestMathEvalErrors(t *testing.T) {
	cases := []struct {
		name         string
		expr         string
		wantContains string
	}{
		{"division_by_zero", "1 / 0", "division by zero"},
		{"floor_division_by_zero", "5 // 0", "division by zero"},
		{"modulo_by_zero", "5 % 0", "modulo by zero"},
		{"trailing_operator", "2 +", "end of expression"},
		{"unexpected_char", "3 @ 4", "unexpected characters"},
		{"unbalanced_paren", "(1 + 2", "expected ')'"},
		{"unknown_function", "wat(2)", "unknown function"},
		{"unknown_identifier", "xyz", "unknown identifier"},
		{"wrong_arity_single", "sqrt()", "1 argument"},
		{"wrong_arity_double", "min(3)", "2 arguments"},
		{"factorial_out_of_range", "factorial(21)", "out of range"},
		{"factorial_negative", "factorial(-1)", "out of range"},
		// NOTE: chained power (a ** b ** c) is unsupported and errors out;
		// Python treats it as right-associative. Documenting actual behavior.
		{"chained_power", "2 ** 3 ** 2", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := runExec(t, MathEval(), map[string]any{"expression": c.expr})
			errStr, ok := m["error"].(string)
			if !ok {
				t.Fatalf("eval(%q) expected error key, got %#v", c.expr, m)
			}
			if _, hasResult := m["result"]; hasResult {
				t.Errorf("eval(%q) unexpectedly returned a result: %#v", c.expr, m["result"])
			}
			if c.wantContains != "" && !strings.Contains(errStr, c.wantContains) {
				t.Errorf("eval(%q) error = %q, want it to contain %q", c.expr, errStr, c.wantContains)
			}
		})
	}
}

func TestMathEvalEmptyExpression(t *testing.T) {
	cases := []string{"", "   "}
	for _, expr := range cases {
		t.Run("expr_"+expr, func(t *testing.T) {
			m := runExec(t, MathEval(), map[string]any{"expression": expr})
			if got := m["error"]; got != "expression is required" {
				t.Errorf("error = %#v, want %q", got, "expression is required")
			}
		})
	}
}

func TestMathEvalLengthGuard(t *testing.T) {
	long := strings.Repeat("1+", 250) + "1" // 501 chars, otherwise valid
	if len(long) != 501 {
		t.Fatalf("test setup: expression length = %d, want 501", len(long))
	}
	m := runExec(t, MathEval(), map[string]any{"expression": long})
	errStr, ok := m["error"].(string)
	if !ok || !strings.Contains(errStr, "too long") {
		t.Fatalf("expected 'too long' error, got %#v", m)
	}

	ok500 := strings.Repeat("1+", 249) + "11" // 500 chars, under the limit
	if len(ok500) != 500 {
		t.Fatalf("test setup: expression length = %d, want 500", len(ok500))
	}
	m2 := runExec(t, MathEval(), map[string]any{"expression": ok500})
	if _, isErr := m2["error"]; isErr {
		t.Errorf("500-char expression should be accepted, got error %#v", m2["error"])
	}
}

func TestDateTime(t *testing.T) {
	m := runExec(t, DateTime(), map[string]any{})
	for _, k := range []string{"date", "time", "day_of_week", "iso", "unix_timestamp"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %q", k)
		}
	}
	if _, ok := m["unix_timestamp"].(int64); !ok {
		t.Errorf("unix_timestamp type = %T, want int64", m["unix_timestamp"])
	}
	if d, ok := m["date"].(string); !ok || len(d) != 10 {
		t.Errorf("date = %#v, want a YYYY-MM-DD string", m["date"])
	}
	if ts, ok := m["time"].(string); !ok || len(ts) != 8 {
		t.Errorf("time = %#v, want an HH:MM:SS string", m["time"])
	}
}

func TestCronSchedule(t *testing.T) {
	t.Run("fields_and_keys", func(t *testing.T) {
		m := runExec(t, CronSchedule(), map[string]any{"expression": "*/15 0 1 * MON"})
		if e, isErr := m["error"]; isErr {
			t.Fatalf("unexpected error: %v", e)
		}
		if m["expression"] != "*/15 0 1 * MON" {
			t.Errorf("expression = %#v", m["expression"])
		}
		if _, ok := m["explanation"].(string); !ok {
			t.Errorf("explanation type = %T, want string", m["explanation"])
		}
		fields, ok := m["fields"].(map[string]string)
		if !ok {
			t.Fatalf("fields type = %T, want map[string]string", m["fields"])
		}
		wantFields := map[string]string{
			"minute": "*/15", "hour": "0", "day_of_month": "1", "month": "*", "day_of_week": "MON",
		}
		for k, v := range wantFields {
			if fields[k] != v {
				t.Errorf("fields[%q] = %q, want %q", k, fields[k], v)
			}
		}
		if _, ok := m["next_runs"].([]string); !ok {
			t.Errorf("next_runs type = %T, want []string", m["next_runs"])
		}
	})

	countCases := []struct {
		name  string
		count any
		want  int
	}{
		{"count_explicit", 3, 3},
		{"count_clamp_low", 0, 1},
		{"count_clamp_high", 25, 20},
	}
	for _, c := range countCases {
		t.Run(c.name, func(t *testing.T) {
			m := runExec(t, CronSchedule(), map[string]any{"expression": "* * * * *", "count": c.count})
			runs, ok := m["next_runs"].([]string)
			if !ok {
				t.Fatalf("next_runs type = %T", m["next_runs"])
			}
			if len(runs) != c.want {
				t.Errorf("len(next_runs) = %d, want %d", len(runs), c.want)
			}
		})
	}

	t.Run("count_default_is_five", func(t *testing.T) {
		m := runExec(t, CronSchedule(), map[string]any{"expression": "* * * * *"})
		runs, _ := m["next_runs"].([]string)
		if len(runs) != 5 {
			t.Errorf("len(next_runs) = %d, want 5 (default)", len(runs))
		}
	})

	t.Run("month_name", func(t *testing.T) {
		m := runExec(t, CronSchedule(), map[string]any{"expression": "0 0 1 jan *"})
		if e, isErr := m["error"]; isErr {
			t.Fatalf("unexpected error: %v", e)
		}
		fields := m["fields"].(map[string]string)
		if fields["month"] != "jan" {
			t.Errorf("fields[month] = %q, want %q", fields["month"], "jan")
		}
		runs := m["next_runs"].([]string)
		if len(runs) < 1 {
			t.Fatalf("expected at least one next run for a Jan 1 schedule")
		}
		if !strings.Contains(runs[0], "-01-01T00:00:00") {
			t.Errorf("next run = %q, want a Jan 1 midnight timestamp", runs[0])
		}
	})

	weekdayCases := []struct {
		name string
		expr string
	}{
		{"weekday_name_upper", "0 9 * * MON"},
		{"weekday_name_lower", "0 9 * * fri"},
	}
	for _, c := range weekdayCases {
		t.Run(c.name, func(t *testing.T) {
			m := runExec(t, CronSchedule(), map[string]any{"expression": c.expr})
			if e, isErr := m["error"]; isErr {
				t.Fatalf("unexpected error for %q: %v", c.expr, e)
			}
			if runs := m["next_runs"].([]string); len(runs) < 1 {
				t.Errorf("expected at least one next run for %q", c.expr)
			}
		})
	}

	errCases := []struct {
		name         string
		expr         string
		wantContains string
	}{
		{"too_few_fields", "* * *", "expected 5 fields"},
		{"too_many_fields", "* * * * * *", "expected 5 fields"},
		{"invalid_value", "bad * * * *", "invalid value"},
		{"empty", "", "expression is required"},
	}
	for _, c := range errCases {
		t.Run(c.name, func(t *testing.T) {
			m := runExec(t, CronSchedule(), map[string]any{"expression": c.expr})
			errStr, ok := m["error"].(string)
			if !ok {
				t.Fatalf("expected error key for %q, got %#v", c.expr, m)
			}
			if !strings.Contains(errStr, c.wantContains) {
				t.Errorf("error = %q, want it to contain %q", errStr, c.wantContains)
			}
		})
	}
}
