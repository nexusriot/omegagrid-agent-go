package builtin

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// padImage surrounds src with a white quiet zone of pad pixels on every side.
// The symbol pixels are copied verbatim, so module rendering (and therefore
// scannability) stays exactly as go-qrcode produced it.
func padImage(src image.Image, pad int) image.Image {
	if pad <= 0 {
		return src
	}
	b := src.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx()+2*pad, b.Dy()+2*pad))
	draw.Draw(out, out.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	draw.Draw(out, image.Rect(pad, pad, pad+b.Dx(), pad+b.Dy()), src, b.Min, draw.Src)
	return out
}

func QrGenerateSchema() Skill {
	return Skill{Name: "qr_generate", Description: "Generate a QR code image (returned as base64 PNG).",
		Parameters: map[string]Param{
			"data":             {Type: "string", Description: "Text or URL to encode (max 4000 chars)", Required: true},
			"error_correction": {Type: "string", Description: "L, M, Q, or H (default M)", Required: false},
			"box_size":         {Type: "number", Description: "Pixel size per module 1-40 (default 10)", Required: false},
			"border":           {Type: "number", Description: "Border width in modules 1-20 (default 4)", Required: false},
		}}
}

func QrGenerate() Executor {
	return func(args map[string]any) (any, error) {
		data := str(args, "data")
		if data == "" {
			return map[string]any{"error": "data is required"}, nil
		}
		if len(data) > 4000 {
			return map[string]any{"error": "data too long (max 4000 chars)"}, nil
		}

		levelMap := map[string]qrcode.RecoveryLevel{
			"L": qrcode.Low, "M": qrcode.Medium, "Q": qrcode.High, "H": qrcode.Highest,
		}
		// Case-insensitive: "l"/"m"/"q"/"h" are at least as likely from an LLM
		// as the documented upper-case spellings, and used to be rejected.
		levelKey := strings.ToUpper(strings.TrimSpace(str(args, "error_correction")))
		if levelKey == "" {
			levelKey = "M"
		}
		level, ok := levelMap[levelKey]
		if !ok {
			return map[string]any{"error": fmt.Sprintf("invalid error_correction: %s (use L/M/Q/H)", levelKey)}, nil
		}

		boxSize := intOr(args, "box_size", 10)
		if boxSize < 1 {
			boxSize = 1
		}
		if boxSize > 40 {
			boxSize = 40
		}
		border := intOr(args, "border", 4)
		if border < 1 {
			border = 1
		}
		if border > 20 {
			border = 20
		}

		qr, err := qrcode.New(data, level)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		// Render the symbol without go-qrcode's fixed 4-module quiet zone and
		// add our own, so box_size and border mean what the schema says.
		// Previously the size argument was boxSize*(modules+2*border) against a
		// bitmap that already included the quiet zone, which made box_size only
		// approximately the pixels-per-module and left `border` doing nothing but
		// scaling the whole image — a 4-module border no matter what was asked.
		qr.DisableBorder = true

		modules := len(qr.Bitmap())
		img := padImage(qr.Image(boxSize*modules), border*boxSize)

		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return map[string]any{"error": err.Error()}, nil
		}

		b64 := base64.StdEncoding.EncodeToString(buf.Bytes())
		return map[string]any{
			"data":             data,
			"data_length":      len(data),
			"error_correction": levelKey,
			"box_size":         boxSize,
			"border":           border,
			"modules":          modules, // symbol modules per side, quiet zone excluded
			"image_size_px":    img.Bounds().Dx(),
			"image_format":     "png",
			"image_base64":     b64,
			"data_uri":         "data:image/png;base64," + b64,
			"size_bytes":       buf.Len(),
		}, nil
	}
}
