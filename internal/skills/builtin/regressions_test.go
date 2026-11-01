package builtin

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	qrcode "github.com/skip2/go-qrcode"
)

// http.NewRequest returns a nil request together with its error for a URL it
// cannot parse. Dereferencing that nil panicked, and inside a parallel tool
// batch the panic runs on its own goroutine and kills the process.
func TestHttpHealthMalformedURLDoesNotPanic(t *testing.T) {
	for _, bad := range []string{
		"http://exa mple.com/health",
		"ht!tp://example.com",
		"http://[::1",
	} {
		t.Run(bad, func(t *testing.T) {
			res, err := HttpHealth()(map[string]any{"url": bad})
			if err != nil {
				t.Fatalf("HttpHealth returned a hard error: %v", err)
			}
			m := res.(map[string]any)
			if m["ok"] != false {
				t.Fatalf("ok = %v, want false", m["ok"])
			}
			if msg, _ := m["error"].(string); msg == "" {
				t.Fatalf("no error message reported for %q: %v", bad, m)
			}
		})
	}
}

func TestHttpHealthInvalidMethodDoesNotPanic(t *testing.T) {
	res, err := HttpHealth()(map[string]any{"url": "http://example.com", "method": "GE T"})
	if err != nil {
		t.Fatalf("HttpHealth returned a hard error: %v", err)
	}
	if m := res.(map[string]any); m["ok"] != false {
		t.Fatalf("ok = %v, want false", m["ok"])
	}
}

func TestQrGenerateAcceptsLowercaseErrorCorrection(t *testing.T) {
	for _, level := range []string{"l", "m", "q", "h", "L", "M", " h "} {
		res, err := QrGenerate()(map[string]any{"data": "hello", "error_correction": level})
		if err != nil {
			t.Fatalf("QrGenerate(%q): %v", level, err)
		}
		m := res.(map[string]any)
		if msg, ok := m["error"]; ok {
			t.Fatalf("QrGenerate(%q) rejected a valid level: %v", level, msg)
		}
		if m["image_base64"] == "" {
			t.Fatalf("QrGenerate(%q) produced no image", level)
		}
	}
	// Genuinely wrong levels are still rejected.
	res, _ := QrGenerate()(map[string]any{"data": "hello", "error_correction": "Z"})
	if _, ok := res.(map[string]any)["error"]; !ok {
		t.Fatal("QrGenerate accepted an invalid error_correction level")
	}
}

func TestParsePortsRejectsOutOfRange(t *testing.T) {
	// "1-1000000" used to expand into a million-element slice before the caller
	// trimmed it back to 1024.
	for _, spec := range []string{"1-1000000", "70000", "0", "-5", "0-10"} {
		if got, err := parsePorts(spec); err == nil {
			t.Fatalf("parsePorts(%q) = %d ports, want an error", spec, len(got))
		}
	}
	if _, err := parsePorts("1-65535"); err != nil {
		t.Fatalf("parsePorts(full range) unexpectedly failed: %v", err)
	}
}

func TestIsHostname(t *testing.T) {
	valid := []string{"example.com", "sub.domain.example.com", "localhost",
		"example.com.", "1.2.3.4", "2001:db8::1", "my_host", "a-b.example"}
	for _, h := range valid {
		if !isHostname(h) {
			t.Fatalf("isHostname(%q) = false, want true", h)
		}
	}
	// dig reads leading "-" and "@" tokens as its own options, so these must
	// never reach its argv.
	invalid := []string{"", "-f/etc/passwd", "@8.8.8.8", "example.com -f /etc/passwd",
		"exa mple.com", "example.com;id", "a..b", "-example.com", "example-.com"}
	for _, h := range invalid {
		if isHostname(h) {
			t.Fatalf("isHostname(%q) = true, want false", h)
		}
	}
}

func TestDnsLookupRejectsDigOptions(t *testing.T) {
	res, err := DnsLookup()(map[string]any{"domain": "-f/etc/passwd"})
	if err != nil {
		t.Fatalf("DnsLookup: %v", err)
	}
	if _, ok := res.(map[string]any)["error"]; !ok {
		t.Fatalf("DnsLookup accepted a dig option as a domain: %v", res)
	}
}

func TestDecodeBase64AnyAcceptsAllVariants(t *testing.T) {
	// "sub?ject" encodes to a value containing a URL-safe-only character.
	cases := map[string]string{
		"padded standard":   "c3ViP2plY3Q=",
		"raw standard":      "c3ViP2plY3Q",
		"url-safe padded":   "Pz8_Pw==",
		"url-safe raw":      "Pz8_Pw",
		"plain ascii":       "aGVsbG8=",
		"plain ascii unpad": "aGVsbG8",
	}
	for name, in := range cases {
		if _, err := decodeBase64Any(in); err != nil {
			t.Fatalf("%s: decodeBase64Any(%q) failed: %v", name, in, err)
		}
	}
	if _, err := decodeBase64Any("!!!not base64!!!"); err == nil {
		t.Fatal("decodeBase64Any accepted garbage")
	}
}

func TestBase64SkillDecodesURLSafe(t *testing.T) {
	res, err := Base64()(map[string]any{"action": "decode", "text": "Pz8_Pw"})
	if err != nil {
		t.Fatalf("Base64: %v", err)
	}
	m := res.(map[string]any)
	if msg, ok := m["error"]; ok {
		t.Fatalf("URL-safe base64 rejected: %v", msg)
	}
	if m["output"] != "????" {
		t.Fatalf("output = %v, want \"????\"", m["output"])
	}
}

func TestCronScheduleRejectsOutOfRangeFields(t *testing.T) {
	// An out-of-range field produced an empty next_runs list with no hint that
	// the expression could never fire.
	for _, expr := range []string{"99 * * * *", "* 25 * * *", "* * 32 * *", "* * * 13 *", "* * * * 8"} {
		res, err := CronSchedule()(map[string]any{"expression": expr})
		if err != nil {
			t.Fatalf("CronSchedule(%q): %v", expr, err)
		}
		if _, ok := res.(map[string]any)["error"]; !ok {
			t.Fatalf("CronSchedule(%q) accepted an impossible expression: %v", expr, res)
		}
	}
}

func TestCronScheduleAcceptsValidExpressions(t *testing.T) {
	for _, expr := range []string{"*/15 * * * *", "0 9 * * mon-fri", "30 3 1 jan *", "0 0 * * 7"} {
		res, err := CronSchedule()(map[string]any{"expression": expr})
		if err != nil {
			t.Fatalf("CronSchedule(%q): %v", expr, err)
		}
		m := res.(map[string]any)
		if msg, ok := m["error"]; ok {
			t.Fatalf("CronSchedule(%q) rejected a valid expression: %v", expr, msg)
		}
		runs, _ := m["next_runs"].([]string)
		if len(runs) == 0 {
			t.Fatalf("CronSchedule(%q) produced no next runs", expr)
		}
	}
}

// max_chars counts characters, so byte-slicing a multi-byte page left a broken
// rune at the tail and returned fewer characters than requested.
func TestWebScrapeTruncatesByRunes(t *testing.T) {
	page := strings.Repeat("日", 200)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	res, err := WebScrape(5)(map[string]any{"url": srv.URL, "max_chars": 10})
	if err != nil {
		t.Fatalf("WebScrape: %v", err)
	}
	m := res.(map[string]any)
	text, _ := m["text"].(string)

	if !utf8.ValidString(text) {
		t.Fatalf("web_scrape returned invalid UTF-8: %q", text)
	}
	if n := utf8.RuneCountInString(text); n != 10 {
		t.Fatalf("web_scrape returned %d characters, want 10 (got %q)", n, text)
	}
	if m["truncated"] != true {
		t.Fatalf("truncated = %v, want true", m["truncated"])
	}
}

func TestWebScrapeKeepsShortPageIntact(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><head><title>t</title></head><body><p>héllo wörld</p></body></html>"))
	}))
	defer srv.Close()

	res, err := WebScrape(5)(map[string]any{"url": srv.URL, "max_chars": 4000})
	if err != nil {
		t.Fatalf("WebScrape: %v", err)
	}
	m := res.(map[string]any)
	if text, _ := m["text"].(string); text != "héllo wörld" {
		t.Fatalf("text = %q, want %q", text, "héllo wörld")
	}
	if m["truncated"] != false {
		t.Fatalf("truncated = %v, want false", m["truncated"])
	}
}

// box_size and border are documented in modules/pixels-per-module, but the old
// implementation passed boxSize*(modules+2*border) as the total image size
// against a bitmap that already contained a fixed 4-module quiet zone: box_size
// was only approximately the module size, and `border` merely scaled the whole
// image instead of widening the border.
func TestQrGenerateHonoursBoxSizeAndBorder(t *testing.T) {
	run := func(boxSize, border int) (modules, sizePx int, raw []byte) {
		t.Helper()
		res, err := QrGenerate()(map[string]any{
			"data": "hello", "box_size": boxSize, "border": border,
		})
		if err != nil {
			t.Fatalf("QrGenerate: %v", err)
		}
		m := res.(map[string]any)
		if msg, ok := m["error"]; ok {
			t.Fatalf("QrGenerate reported an error: %v", msg)
		}
		decoded, derr := base64.StdEncoding.DecodeString(m["image_base64"].(string))
		if derr != nil {
			t.Fatalf("image is not valid base64: %v", derr)
		}
		return m["modules"].(int), m["image_size_px"].(int), decoded
	}

	modules, sizePx, raw := run(10, 4)
	if want := 10*modules + 2*4*10; sizePx != want {
		t.Fatalf("image_size_px = %d, want boxSize*modules + 2*border*boxSize = %d", sizePx, want)
	}

	// The reported size must match the actual PNG.
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode PNG: %v", err)
	}
	if got := img.Bounds().Dx(); got != sizePx {
		t.Fatalf("PNG width %d != reported image_size_px %d", got, sizePx)
	}
	if img.Bounds().Dy() != img.Bounds().Dx() {
		t.Fatalf("QR image is not square: %v", img.Bounds())
	}

	// A wider border must add exactly the border pixels, not rescale the symbol.
	_, wideSizePx, _ := run(10, 8)
	if want := sizePx + 2*4*10; wideSizePx != want {
		t.Fatalf("border 4→8 gave %d px, want %d", wideSizePx, want)
	}

	// The corners belong to the quiet zone and must be white.
	for _, p := range []image.Point{
		img.Bounds().Min,
		{X: img.Bounds().Max.X - 1, Y: img.Bounds().Min.Y},
		{X: img.Bounds().Min.X, Y: img.Bounds().Max.Y - 1},
		{X: img.Bounds().Max.X - 1, Y: img.Bounds().Max.Y - 1},
	} {
		r, g, b, _ := img.At(p.X, p.Y).RGBA()
		if r != 0xffff || g != 0xffff || b != 0xffff {
			t.Fatalf("pixel %v is not white: %v", p, img.At(p.X, p.Y))
		}
	}

	// A version-1 symbol is 21 modules per side, quiet zone excluded.
	if modules != 21 {
		t.Fatalf("modules = %d, want 21 for a short version-1 payload", modules)
	}
}

// The padded image must reproduce go-qrcode's own module rendering pixel for
// pixel — padding must never resample or shift the symbol.
func TestPadImagePreservesSymbolPixels(t *testing.T) {
	qr, err := qrcode.New("hello", qrcode.Medium)
	if err != nil {
		t.Fatalf("qrcode.New: %v", err)
	}
	qr.DisableBorder = true
	modules := len(qr.Bitmap())
	src := qr.Image(10 * modules)

	const pad = 40
	out := padImage(src, pad)

	sb := src.Bounds()
	if out.Bounds().Dx() != sb.Dx()+2*pad {
		t.Fatalf("padded width %d, want %d", out.Bounds().Dx(), sb.Dx()+2*pad)
	}
	for y := sb.Min.Y; y < sb.Max.Y; y++ {
		for x := sb.Min.X; x < sb.Max.X; x++ {
			wr, wg, wb, _ := src.At(x, y).RGBA()
			gr, gg, gb, _ := out.At(x-sb.Min.X+pad, y-sb.Min.Y+pad).RGBA()
			if wr != gr || wg != gg || wb != gb {
				t.Fatalf("symbol pixel (%d,%d) changed: %v vs %v", x, y,
					src.At(x, y), out.At(x-sb.Min.X+pad, y-sb.Min.Y+pad))
			}
		}
	}
}

// A negative or zero max_chars — a value an LLM can and does emit — used to
// panic on the rune slice ("slice bounds out of range [:-1]"), killing the
// process from inside a parallel tool batch. It must fall back to the default.
func TestWebScrapeNonPositiveMaxCharsUsesDefault(t *testing.T) {
	page := strings.Repeat("a", 50)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	for _, maxChars := range []any{-1, 0, -4000, "-1"} {
		res, err := WebScrape(5)(map[string]any{"url": srv.URL, "max_chars": maxChars})
		if err != nil {
			t.Fatalf("WebScrape(max_chars=%v): %v", maxChars, err)
		}
		m := res.(map[string]any)
		if text, _ := m["text"].(string); text != page {
			t.Fatalf("max_chars=%v: text = %q, want the full page", maxChars, text)
		}
		if m["truncated"] != false {
			t.Fatalf("max_chars=%v: truncated = %v, want false", maxChars, m["truncated"])
		}
	}
}

// cron_schedule must agree with the scheduler's evaluator on N/S steps and on
// Vixie's "*-prefixed day field is unrestricted" rule.
func TestCronScheduleStepAndStarDaySemantics(t *testing.T) {
	res, err := CronSchedule()(map[string]any{"expression": "5/15 * * * *", "count": 8})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.(map[string]any)["next_runs"].([]string) {
		ts, _ := time.Parse(time.RFC3339, r)
		if ts.Minute()%15 != 5 {
			t.Errorf("5/15 next run %s is not at :05/:20/:35/:50", r)
		}
	}
	res, err = CronSchedule()(map[string]any{"expression": "0 0 */2 * 1", "count": 5})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.(map[string]any)["next_runs"].([]string) {
		ts, _ := time.Parse(time.RFC3339, r)
		if ts.Weekday() != time.Monday || ts.Day()%2 != 1 {
			t.Errorf("*/2 dom + Monday next run %s is not an odd-numbered Monday", r)
		}
	}
}

// "80,80,80" listed an open port three times; thousands of repeated
// "1-65535" parts expanded ~65k ports each before the 1024 cap applied.
func TestParsePortsDedupesAndCapsWhileExpanding(t *testing.T) {
	got, err := parsePorts("80,80, 443,80")
	if err != nil || len(got) != 2 || got[0] != 80 || got[1] != 443 {
		t.Fatalf("parsePorts dedupe = %v, %v; want [80 443]", got, err)
	}
	spec := strings.TrimSuffix(strings.Repeat("1-65535,", 3000), ",")
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	got, err = parsePorts(spec)
	runtime.ReadMemStats(&m1)
	if err != nil || len(got) != maxScanPorts {
		t.Fatalf("len = %d, %v; want %d", len(got), err, maxScanPorts)
	}
	if alloc := m1.TotalAlloc - m0.TotalAlloc; alloc > 16<<20 {
		t.Fatalf("parsing allocated %d MB", alloc>>20)
	}
}

// record_type is an argv entry for dig; "@10.0.0.53" made dig query that
// server directly, bypassing the private-network guard.
func TestDNSLookupRejectsUnknownRecordTypes(t *testing.T) {
	for _, rt := range []string{"@10.0.0.53", "AXFR", "+tcp", "ANY", "-f/etc/passwd"} {
		m := runExec(t, DnsLookup(), map[string]any{"domain": "example.com", "record_type": rt})
		if msg, _ := m["error"].(string); !strings.Contains(msg, "unsupported record type") {
			t.Errorf("record_type %q: got %v, want an unsupported-type error", rt, m)
		}
	}
}

// Unsupported versions returned v4 UUIDs labelled with the requested version.
func TestUUIDGenVersions(t *testing.T) {
	for _, v := range []int{2, 6, 8, 0} {
		m := runExec(t, UuidGen(), map[string]any{"version": v})
		if _, ok := m["error"]; !ok {
			t.Errorf("version %d: got %v, want an error", v, m)
		}
	}
	m := runExec(t, UuidGen(), map[string]any{"version": 7})
	u, err := uuid.Parse(m["uuids"].([]string)[0])
	if err != nil || u.Version() != 7 {
		t.Fatalf("v7 = %v (%v), version %d", m, err, u.Version())
	}
}

func TestPingCheckValidatesPort(t *testing.T) {
	for _, p := range []int{0, 70000, -1} {
		m := runExec(t, PingCheck(), map[string]any{"host": "127.0.0.1", "port": p})
		if msg, _ := m["error"].(string); !strings.Contains(msg, "outside 1-65535") {
			t.Errorf("port %d: got %v", p, m)
		}
	}
}

// identity_file comes from the model: /dev/zero used to be read until the
// process ran out of memory, and a FIFO blocked forever.
func TestReadKeyFileRejectsNonRegularAndHuge(t *testing.T) {
	if _, err := readKeyFile("/dev/zero"); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("/dev/zero: err = %v", err)
	}
	big := filepath.Join(t.TempDir(), "big")
	if err := os.WriteFile(big, make([]byte, maxKeyFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readKeyFile(big); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("oversized key: err = %v", err)
	}
}
