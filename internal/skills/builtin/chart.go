package builtin

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strconv"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

func ChartRenderSchema() Skill {
	return Skill{Name: "chart_render", Description: "Render a bar, line or pie chart as a PNG image delivered to the user " +
		"(Telegram photo / web attachment). Give labels plus one series (values) or several (series).",
		Parameters: map[string]Param{
			"type":    {Type: "string", Description: "bar (default) | line | pie", Required: false},
			"title":   {Type: "string", Description: "Chart title", Required: false},
			"labels":  {Type: "array", Description: "Category / x-axis labels", Required: true},
			"values":  {Type: "array", Description: "One series of numbers, same length as labels", Required: false},
			"series":  {Type: "array", Description: `Several series: [{"name":"2025","values":[...]}, ...] (bar/line)`, Required: false},
			"x_label": {Type: "string", Description: "X-axis caption", Required: false},
			"y_label": {Type: "string", Description: "Y-axis caption", Required: false},
			"width":   {Type: "number", Description: "Pixels (default 900, 300-2000)", Required: false},
			"height":  {Type: "number", Description: "Pixels (default 500, 200-1500)", Required: false},
		}}
}

type chartSeries struct {
	Name   string
	Values []float64
}

var chartPalette = []color.RGBA{
	{0x3b, 0x82, 0xf6, 0xff}, {0xf5, 0x9e, 0x0b, 0xff}, {0x10, 0xb9, 0x81, 0xff}, {0xef, 0x44, 0x44, 0xff},
	{0x8b, 0x5c, 0xf6, 0xff}, {0x06, 0xb6, 0xd4, 0xff}, {0xec, 0x48, 0x99, 0xff}, {0x84, 0xcc, 0x16, 0xff},
	{0x64, 0x74, 0x8b, 0xff}, {0xf9, 0x73, 0x16, 0xff},
}

var (
	chartInk   = color.RGBA{0x1f, 0x29, 0x37, 0xff}
	chartMuted = color.RGBA{0x6b, 0x72, 0x80, 0xff}
	chartGrid  = color.RGBA{0xe5, 0xe7, 0xeb, 0xff}
)

const maxChartPoints = 500

func ChartRender() Executor {
	return func(args map[string]any) (any, error) {
		labels := strList(args, "labels")
		if len(labels) == 0 {
			return map[string]any{"error": "labels is required"}, nil
		}
		if len(labels) > maxChartPoints {
			return map[string]any{"error": fmt.Sprintf("at most %d labels", maxChartPoints)}, nil
		}
		series, err := chartSeriesArg(args, len(labels))
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		kind := strings.ToLower(strings.TrimSpace(str(args, "type")))
		if kind == "" {
			kind = "bar"
		}
		w := clampInt(intOr(args, "width", 900), 300, 2000)
		h := clampInt(intOr(args, "height", 500), 200, 1500)
		c := newCanvas(w, h)
		title := str(args, "title")

		switch kind {
		case "bar", "line":
			c.axesChart(kind, title, labels, series, str(args, "x_label"), str(args, "y_label"))
		case "pie":
			for _, v := range series[0].Values {
				if v < 0 {
					return map[string]any{"error": "pie values must not be negative"}, nil
				}
			}
			c.pie(title, labels, series[0].Values)
		default:
			return map[string]any{"error": "type must be bar, line or pie"}, nil
		}

		var buf bytes.Buffer
		if err := png.Encode(&buf, c.img); err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		return map[string]any{
			"type": kind, "points": len(labels), "series": len(series),
			"image_base64": base64.StdEncoding.EncodeToString(buf.Bytes()),
			"image_format": "png", "size_bytes": buf.Len(),
			"width": w, "height": h,
		}, nil
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func toFloats(v any) ([]float64, error) {
	var raw []any
	switch x := v.(type) {
	case []any:
		raw = x
	case []float64:
		return x, nil
	case string:
		var arr []any
		if err := json.Unmarshal([]byte(x), &arr); err == nil {
			raw = arr
		} else {
			for _, p := range strings.Split(x, ",") {
				raw = append(raw, strings.TrimSpace(p))
			}
		}
	default:
		return nil, fmt.Errorf("values must be an array of numbers")
	}
	out := make([]float64, 0, len(raw))
	for _, e := range raw {
		switch n := e.(type) {
		case float64:
			out = append(out, n)
		case int:
			out = append(out, float64(n))
		case string:
			f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
			if err != nil {
				return nil, fmt.Errorf("%q is not a number", n)
			}
			out = append(out, f)
		case nil:
			out = append(out, math.NaN())
		default:
			return nil, fmt.Errorf("values must be numbers")
		}
	}
	return out, nil
}

func chartSeriesArg(args map[string]any, n int) ([]chartSeries, error) {
	var out []chartSeries
	if raw, ok := args["series"]; ok && raw != nil {
		var list []any
		switch x := raw.(type) {
		case []any:
			list = x
		case string:
			if err := json.Unmarshal([]byte(x), &list); err != nil {
				return nil, fmt.Errorf("series must be an array of {name, values}")
			}
		case map[string]any: // {"2025": [...], "2026": [...]}
			for name, v := range x {
				list = append(list, map[string]any{"name": name, "values": v})
			}
		}
		for i, e := range list {
			m, ok := e.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("series[%d] must be an object with name and values", i)
			}
			vals, err := toFloats(m["values"])
			if err != nil {
				return nil, fmt.Errorf("series[%d]: %v", i, err)
			}
			name, _ := m["name"].(string)
			if name == "" {
				name = fmt.Sprintf("series %d", i+1)
			}
			out = append(out, chartSeries{name, vals})
		}
	}
	if len(out) == 0 {
		vals, err := toFloats(args["values"])
		if err != nil {
			return nil, fmt.Errorf("values (or series) is required: %v", err)
		}
		out = []chartSeries{{"", vals}}
	}
	if len(out) > len(chartPalette) {
		return nil, fmt.Errorf("at most %d series", len(chartPalette))
	}
	for _, s := range out {
		if len(s.Values) != n {
			return nil, fmt.Errorf("series %q has %d values for %d labels", s.Name, len(s.Values), n)
		}
	}
	return out, nil
}

// ─── drawing ─────────────────────────────────────────────────────────────────

type canvas struct {
	img  *image.RGBA
	face font.Face
}

func newCanvas(w, h int) *canvas {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	return &canvas{img: img, face: basicfont.Face7x13}
}

func (c *canvas) textWidth(s string) int { return font.MeasureString(c.face, s).Round() }

// text draws s with its baseline-left at (x, y); scale 2 doubles the glyphs.
func (c *canvas) text(x, y int, s string, col color.Color, scale int) {
	if scale <= 1 {
		d := &font.Drawer{Dst: c.img, Src: image.NewUniform(col), Face: c.face, Dot: fixed.P(x, y)}
		d.DrawString(s)
		return
	}
	w, h := c.textWidth(s)+2, 16
	tmp := image.NewRGBA(image.Rect(0, 0, w, h))
	d := &font.Drawer{Dst: tmp, Src: image.NewUniform(col), Face: c.face, Dot: fixed.P(0, 12)}
	d.DrawString(s)
	for ty := 0; ty < h; ty++ {
		for tx := 0; tx < w; tx++ {
			p := tmp.RGBAAt(tx, ty)
			if p.A == 0 {
				continue
			}
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					c.blend(x+tx*scale+dx, y-12*scale+ty*scale+dy, p)
				}
			}
		}
	}
}

func (c *canvas) blend(x, y int, p color.RGBA) {
	if !(image.Point{x, y}.In(c.img.Bounds())) {
		return
	}
	if p.A == 0xff {
		c.img.SetRGBA(x, y, p)
		return
	}
	dst := c.img.RGBAAt(x, y)
	a := uint32(p.A)
	mix := func(s, d uint8) uint8 { return uint8((uint32(s)*0xff + uint32(d)*(0xff-a)) / 0xff) }
	c.img.SetRGBA(x, y, color.RGBA{mix(p.R, dst.R), mix(p.G, dst.G), mix(p.B, dst.B), 0xff})
}

// textRotated draws s vertically (bottom to top) centred on (cx, cy).
func (c *canvas) textRotated(cx, cy int, s string, col color.Color) {
	w, h := c.textWidth(s)+2, 16
	tmp := image.NewRGBA(image.Rect(0, 0, w, h))
	d := &font.Drawer{Dst: tmp, Src: image.NewUniform(col), Face: c.face, Dot: fixed.P(0, 12)}
	d.DrawString(s)
	for ty := 0; ty < h; ty++ {
		for tx := 0; tx < w; tx++ {
			if p := tmp.RGBAAt(tx, ty); p.A != 0 {
				c.blend(cx-h/2+ty, cy+w/2-tx, p)
			}
		}
	}
}

func (c *canvas) rect(x0, y0, x1, y1 int, col color.RGBA) {
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	draw.Draw(c.img, image.Rect(x0, y0, x1, y1), &image.Uniform{col}, image.Point{}, draw.Src)
}

// line draws a segment of the given thickness.
func (c *canvas) line(x0, y0, x1, y1 float64, thick int, col color.RGBA) {
	steps := int(math.Max(math.Abs(x1-x0), math.Abs(y1-y0))) + 1
	r := thick / 2
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		x := int(math.Round(x0 + (x1-x0)*t))
		y := int(math.Round(y0 + (y1-y0)*t))
		for dy := -r; dy <= r; dy++ {
			for dx := -r; dx <= r; dx++ {
				c.blend(x+dx, y+dy, col)
			}
		}
	}
}

func (c *canvas) disc(cx, cy, rad int, col color.RGBA) {
	for dy := -rad; dy <= rad; dy++ {
		for dx := -rad; dx <= rad; dx++ {
			if dx*dx+dy*dy <= rad*rad {
				c.blend(cx+dx, cy+dy, col)
			}
		}
	}
}

func fitText(c *canvas, s string, maxW int) string {
	if c.textWidth(s) <= maxW {
		return s
	}
	// The bitmap font has no "…" glyph.
	r := []rune(s)
	for len(r) > 1 && c.textWidth(string(r)+"...") > maxW {
		r = r[:len(r)-1]
	}
	return string(r) + "..."
}

// niceTicks returns rounded axis bounds and a tick step (1/2/5 × 10^k).
func niceTicks(lo, hi float64, want int) (float64, float64, float64) {
	if hi == lo {
		if hi == 0 {
			hi = 1
		} else {
			lo, hi = math.Min(0, lo), math.Max(0, hi)
			if lo == hi {
				hi = lo + 1
			}
		}
	}
	raw := (hi - lo) / float64(want)
	mag := math.Pow(10, math.Floor(math.Log10(raw)))
	step := mag
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if m*mag >= raw {
			step = m * mag
			break
		}
	}
	return math.Floor(lo/step) * step, math.Ceil(hi/step) * step, step
}

func fmtTick(v, step float64) string {
	if math.Abs(v) >= 1e6 {
		return strconv.FormatFloat(v/1e6, 'f', -1, 64) + "M"
	}
	if math.Abs(v) >= 1e4 {
		return strconv.FormatFloat(v/1e3, 'f', -1, 64) + "k"
	}
	dec := 0
	if step < 1 {
		dec = int(math.Ceil(-math.Log10(step)))
	}
	return strconv.FormatFloat(v, 'f', dec, 64)
}

func (c *canvas) title(t string) int {
	if t == "" {
		return 16
	}
	W := c.img.Bounds().Dx()
	t = fitText(c, t, (W-20)/2)
	c.text((W-c.textWidth(t)*2)/2, 32, t, chartInk, 2)
	return 48
}

func (c *canvas) axesChart(kind, title string, labels []string, series []chartSeries, xLabel, yLabel string) {
	W, H := c.img.Bounds().Dx(), c.img.Bounds().Dy()
	top := c.title(title)
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, s := range series {
		for _, v := range s.Values {
			if math.IsNaN(v) {
				continue
			}
			lo, hi = math.Min(lo, v), math.Max(hi, v)
		}
	}
	if math.IsInf(lo, 1) {
		lo, hi = 0, 1
	}
	if kind == "bar" {
		lo, hi = math.Min(lo, 0), math.Max(hi, 0)
	}
	ymin, ymax, step := niceTicks(lo, hi, 5)

	// Left margin fits the widest tick label.
	left := 20
	for v := ymin; v <= ymax+step/2; v += step {
		left = max(left, c.textWidth(fmtTick(v, step))+16)
	}
	if yLabel != "" {
		left += 18
	}
	legendH := 0
	if len(series) > 1 {
		legendH = 22
	}
	right, bottom := W-24, H-40-legendH
	if xLabel != "" {
		bottom -= 16
	}
	if bottom-top < 60 || right-left < 60 {
		return
	}
	yOf := func(v float64) float64 { return float64(bottom) - (v-ymin)/(ymax-ymin)*float64(bottom-top) }

	// Grid + tick labels.
	for v := ymin; v <= ymax+step/2; v += step {
		y := int(math.Round(yOf(v)))
		c.rect(left, y, right, y+1, chartGrid)
		l := fmtTick(v, step)
		c.text(left-8-c.textWidth(l), y+4, l, chartMuted, 1)
	}
	zero := int(math.Round(yOf(math.Max(ymin, math.Min(0, ymax)))))
	c.rect(left, zero, right, zero+1, chartMuted)
	c.rect(left, top, left+1, bottom, chartMuted)

	n := len(labels)
	slot := float64(right-left) / float64(n)
	xOf := func(i int) float64 { return float64(left) + slot*(float64(i)+0.5) }

	// X labels, thinned so they do not overlap.
	maxLabel := 0
	for _, l := range labels {
		maxLabel = max(maxLabel, c.textWidth(l))
	}
	every := 1
	if slot > 0 {
		every = max(1, int(math.Ceil(float64(min(maxLabel, 120)+8)/slot)))
	}
	for i, l := range labels {
		if i%every != 0 {
			continue
		}
		l = fitText(c, l, max(int(slot*float64(every))-6, 20))
		c.text(int(xOf(i))-c.textWidth(l)/2, bottom+16, l, chartMuted, 1)
	}

	switch kind {
	case "bar":
		group := slot * 0.8
		bw := group / float64(len(series))
		for si, s := range series {
			col := chartPalette[si]
			for i, v := range s.Values {
				if math.IsNaN(v) {
					continue
				}
				x0 := xOf(i) - group/2 + bw*float64(si)
				x1 := x0 + math.Max(bw-1, 1)
				c.rect(int(x0), int(math.Round(yOf(v))), int(x1), zero, col)
			}
		}
	case "line":
		for si, s := range series {
			col := chartPalette[si]
			prev := -1
			for i, v := range s.Values {
				if math.IsNaN(v) {
					prev = -1
					continue
				}
				if prev >= 0 {
					c.line(xOf(prev), yOf(s.Values[prev]), xOf(i), yOf(v), 3, col)
				}
				prev = i
			}
			if n <= 60 {
				for i, v := range s.Values {
					if !math.IsNaN(v) {
						c.disc(int(xOf(i)), int(yOf(v)), 3, col)
					}
				}
			}
		}
	}

	if xLabel != "" {
		c.text((left+right-c.textWidth(xLabel))/2, bottom+34, xLabel, chartInk, 1)
	}
	if yLabel != "" {
		c.textRotated(12, (top+bottom)/2, fitText(c, yLabel, bottom-top), chartInk)
	}
	if len(series) > 1 {
		c.legend(left, H-14, series)
	}
}

func (c *canvas) legend(x, y int, series []chartSeries) {
	for i, s := range series {
		c.rect(x, y-9, x+10, y+1, chartPalette[i])
		c.text(x+14, y, s.Name, chartInk, 1)
		x += 14 + c.textWidth(s.Name) + 18
	}
}

func (c *canvas) pie(title string, labels []string, vals []float64) {
	W, H := c.img.Bounds().Dx(), c.img.Bounds().Dy()
	top := c.title(title)
	var total float64
	for _, v := range vals {
		if !math.IsNaN(v) {
			total += v
		}
	}
	if total <= 0 {
		c.text(20, top+20, "no data (all values are zero)", chartMuted, 1)
		return
	}
	legendW := 0
	for i, l := range labels {
		legendW = max(legendW, c.textWidth(fmt.Sprintf("%s  %.1f%%", l, 100*vals[i]/total)))
	}
	legendW = min(legendW+30, W/2)
	rad := min((W-legendW-60)/2, (H-top-20)/2)
	if rad < 20 {
		rad = 20
	}
	// Centre the pie + legend block horizontally.
	x0 := max((W-(2*rad+40+legendW))/2, 10)
	cx, cy := x0+rad, top+(H-top)/2

	// Slice boundaries as cumulative angles, starting at 12 o'clock.
	bounds := make([]float64, len(vals)+1)
	for i, v := range vals {
		if math.IsNaN(v) {
			v = 0
		}
		bounds[i+1] = bounds[i] + v/total*2*math.Pi
	}
	for dy := -rad; dy <= rad; dy++ {
		for dx := -rad; dx <= rad; dx++ {
			if dx*dx+dy*dy > rad*rad {
				continue
			}
			a := math.Atan2(float64(dx), float64(-dy))
			if a < 0 {
				a += 2 * math.Pi
			}
			for i := 0; i < len(vals); i++ {
				if a >= bounds[i] && a < bounds[i+1] {
					c.blend(cx+dx, cy+dy, chartPalette[i%len(chartPalette)])
					break
				}
			}
		}
	}
	// Legend.
	lx, ly := cx+rad+40, cy-len(labels)*20/2+10
	for i, l := range labels {
		if ly > H-8 {
			break
		}
		c.rect(lx, ly-9, lx+10, ly+1, chartPalette[i%len(chartPalette)])
		pct := fmt.Sprintf("  %.1f%%", 100*vals[i]/total)
		txt := fitText(c, l, max(legendW-30-c.textWidth(pct), 40)) + pct
		c.text(lx+16, ly, txt, chartInk, 1)
		ly += 20
	}
}
