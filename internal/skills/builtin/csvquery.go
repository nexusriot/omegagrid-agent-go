package builtin

import (
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func CsvQuerySchema() Skill {
	return Skill{Name: "csv_query", Description: "Filter, sort and aggregate a CSV table (first row is the header) given inline or as a workspace file. " +
		"where takes conditions such as \"status == open\", \"amount > 100\", \"name contains acme\"; group_by + aggregate gives counts, sums, averages.",
		Parameters: map[string]Param{
			"csv":       {Type: "string", Description: "CSV text (or use path)", Required: false},
			"path":      {Type: "string", Description: "CSV file inside the workspace", Required: false},
			"delimiter": {Type: "string", Description: "Field separator: , ; tab or | (default: detected)", Required: false},
			"columns":   {Type: "array", Description: "Columns to return (default all)", Required: false},
			"where":     {Type: "array", Description: "Conditions, all must hold: \"col op value\" with ==, !=, >, <, >=, <=, contains", Required: false},
			"sort_by":   {Type: "string", Description: "Column to sort by (numeric when both values are numbers)", Required: false},
			"desc":      {Type: "boolean", Description: "Sort descending", Required: false},
			"group_by":  {Type: "string", Description: "Column to group rows by", Required: false},
			"aggregate": {Type: "array", Description: "With group_by (or alone, over all rows): count, sum:col, avg:col, min:col, max:col", Required: false},
			"limit":     {Type: "number", Description: "Max rows returned (default 50, max 1000)", Required: false},
		}}
}

const maxCSVRows = 100000

var csvCondRE = regexp.MustCompile(`^\s*(.+?)\s*(==|!=|>=|<=|>|<|\bcontains\b)\s*(.*?)\s*$`)

type csvCond struct {
	col, op, val string
}

func CsvQuery(w WorkspaceConfig) Executor {
	return func(args map[string]any) (any, error) {
		raw := str(args, "csv")
		if p := str(args, "path"); p != "" && raw == "" {
			res, err := FileRead(w)(map[string]any{"path": p, "max_bytes": maxFileRead})
			if err != nil {
				return nil, err
			}
			m := res.(map[string]any)
			if e, ok := m["error"]; ok {
				return map[string]any{"error": e}, nil
			}
			if m["eof"] != true {
				return map[string]any{"error": fmt.Sprintf("csv files are read up to %d KB", maxFileRead>>10)}, nil
			}
			raw, _ = m["content"].(string)
		}
		if strings.TrimSpace(raw) == "" {
			return map[string]any{"error": "csv or path is required"}, nil
		}
		delim, err := csvDelimiter(str(args, "delimiter"), raw)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		r := csv.NewReader(strings.NewReader(strings.TrimPrefix(raw, "\ufeff")))
		r.Comma = delim
		r.FieldsPerRecord = -1
		r.TrimLeadingSpace = true
		header, err := r.Read()
		if err != nil {
			return map[string]any{"error": "header: " + err.Error()}, nil
		}
		for i := range header {
			header[i] = strings.TrimSpace(header[i])
		}
		colIdx := map[string]int{}
		for i, h := range header {
			colIdx[strings.ToLower(h)] = i
		}
		col := func(name string) (int, error) {
			i, ok := colIdx[strings.ToLower(strings.TrimSpace(name))]
			if !ok {
				return 0, fmt.Errorf("no column %q (columns: %s)", name, strings.Join(header, ", "))
			}
			return i, nil
		}

		var conds []csvCond
		for _, w := range strList(args, "where") {
			m := csvCondRE.FindStringSubmatch(w)
			if m == nil {
				return map[string]any{"error": fmt.Sprintf("cannot read condition %q (use: column op value)", w)}, nil
			}
			if _, err := col(m[1]); err != nil {
				return map[string]any{"error": err.Error()}, nil
			}
			conds = append(conds, csvCond{m[1], m[2], strings.Trim(m[3], `"'`)})
		}

		var rows [][]string
		for {
			rec, err := r.Read()
			if err == io.EOF {
				break
			}
			if err != nil {
				return map[string]any{"error": err.Error()}, nil
			}
			if len(rows) == maxCSVRows {
				return map[string]any{"error": fmt.Sprintf("more than %d rows", maxCSVRows)}, nil
			}
			for len(rec) < len(header) {
				rec = append(rec, "")
			}
			keep := true
			for _, c := range conds {
				i, _ := col(c.col)
				if !csvMatch(rec[i], c.op, c.val) {
					keep = false
					break
				}
			}
			if keep {
				rows = append(rows, rec)
			}
		}
		total := len(rows)

		aggs := strList(args, "aggregate")
		if g := str(args, "group_by"); g != "" || len(aggs) > 0 {
			gi := -1
			if g != "" {
				if gi, err = col(g); err != nil {
					return map[string]any{"error": err.Error()}, nil
				}
			}
			if len(aggs) == 0 {
				aggs = []string{"count"}
			}
			groups, err := csvAggregate(rows, gi, aggs, col)
			if err != nil {
				return map[string]any{"error": err.Error()}, nil
			}
			return map[string]any{"columns": header, "matched_rows": total, "group_by": g, "groups": groups}, nil
		}

		if sb := str(args, "sort_by"); sb != "" {
			si, err := col(sb)
			if err != nil {
				return map[string]any{"error": err.Error()}, nil
			}
			desc := boolOr(args, "desc", false)
			sort.SliceStable(rows, func(a, b int) bool {
				c := csvCompare(rows[a][si], rows[b][si])
				if desc {
					return c > 0
				}
				return c < 0
			})
		}
		limit := min(max(intOr(args, "limit", 50), 1), 1000)
		if len(rows) > limit {
			rows = rows[:limit]
		}
		sel := header
		if cs := strList(args, "columns"); len(cs) > 0 {
			sel = nil
			for _, c := range cs {
				if _, err := col(c); err != nil {
					return map[string]any{"error": err.Error()}, nil
				}
				sel = append(sel, header[colIdx[strings.ToLower(strings.TrimSpace(c))]])
			}
		}
		out := make([]map[string]string, 0, len(rows))
		for _, rec := range rows {
			o := map[string]string{}
			for _, c := range sel {
				o[c] = rec[colIdx[strings.ToLower(c)]]
			}
			out = append(out, o)
		}
		return map[string]any{"columns": sel, "matched_rows": total, "returned": len(out), "rows": out}, nil
	}
}

func csvDelimiter(spec, raw string) (rune, error) {
	switch strings.ToLower(spec) {
	case ",":
		return ',', nil
	case ";":
		return ';', nil
	case "tab", "\\t", "\t":
		return '\t', nil
	case "|":
		return '|', nil
	case "":
		first, _, _ := strings.Cut(raw, "\n")
		best, bestN := ',', 0
		for _, d := range []rune{',', ';', '\t', '|'} {
			if n := strings.Count(first, string(d)); n > bestN {
				best, bestN = d, n
			}
		}
		return best, nil
	}
	return 0, fmt.Errorf("delimiter must be , ; tab or |")
}

func csvNumber(s string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(s), ",", ""), 64)
	return f, err == nil && !math.IsNaN(f)
}

func csvCompare(a, b string) int {
	fa, oka := csvNumber(a)
	fb, okb := csvNumber(b)
	if oka && okb {
		switch {
		case fa < fb:
			return -1
		case fa > fb:
			return 1
		}
		return 0
	}
	return strings.Compare(strings.ToLower(a), strings.ToLower(b))
}

func csvMatch(cell, op, val string) bool {
	switch op {
	case "contains":
		return strings.Contains(strings.ToLower(cell), strings.ToLower(val))
	case "==":
		return csvCompare(cell, val) == 0
	case "!=":
		return csvCompare(cell, val) != 0
	case ">":
		return csvCompare(cell, val) > 0
	case "<":
		return csvCompare(cell, val) < 0
	case ">=":
		return csvCompare(cell, val) >= 0
	case "<=":
		return csvCompare(cell, val) <= 0
	}
	return false
}

func csvAggregate(rows [][]string, gi int, aggs []string, col func(string) (int, error)) ([]map[string]any, error) {
	type spec struct {
		label, fn string
		ci        int
	}
	var specs []spec
	for _, a := range aggs {
		fn, c, _ := strings.Cut(strings.ToLower(strings.TrimSpace(a)), ":")
		s := spec{label: a, fn: fn, ci: -1}
		switch fn {
		case "count":
		case "sum", "avg", "min", "max":
			i, err := col(c)
			if err != nil {
				return nil, err
			}
			s.ci = i
		default:
			return nil, fmt.Errorf("unknown aggregate %q (count, sum:col, avg:col, min:col, max:col)", a)
		}
		specs = append(specs, s)
	}
	order := []string{}
	byKey := map[string][][]string{}
	for _, r := range rows {
		k := ""
		if gi >= 0 {
			k = r[gi]
		}
		if _, ok := byKey[k]; !ok {
			order = append(order, k)
		}
		byKey[k] = append(byKey[k], r)
	}
	if len(order) == 0 && gi < 0 {
		order = []string{""}
	}
	var out []map[string]any
	for _, k := range order {
		g := map[string]any{}
		if gi >= 0 {
			g["group"] = k
		}
		for _, s := range specs {
			if s.fn == "count" {
				g[s.label] = len(byKey[k])
				continue
			}
			var vals []float64
			for _, r := range byKey[k] {
				if f, ok := csvNumber(r[s.ci]); ok {
					vals = append(vals, f)
				}
			}
			if len(vals) == 0 {
				g[s.label] = nil
				continue
			}
			sum, lo, hi := 0.0, vals[0], vals[0]
			for _, v := range vals {
				sum += v
				lo, hi = math.Min(lo, v), math.Max(hi, v)
			}
			switch s.fn {
			case "sum":
				g[s.label] = sum
			case "avg":
				g[s.label] = math.Round(sum/float64(len(vals))*1e6) / 1e6
			case "min":
				g[s.label] = lo
			case "max":
				g[s.label] = hi
			}
		}
		out = append(out, g)
	}
	if gi >= 0 && len(specs) > 0 {
		first := specs[0].label
		sort.SliceStable(out, func(a, b int) bool {
			fa, _ := numArg(out[a][first])
			fb, _ := numArg(out[b][first])
			return fa > fb
		})
	}
	return out, nil
}
