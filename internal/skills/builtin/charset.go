package builtin

import (
	"fmt"
	"io"
	"strings"

	"golang.org/x/text/encoding/htmlindex"
)

// decodeCharset wraps in with a decoder for the named charset (any WHATWG
// label: windows-1252, iso-8859-x, windows-1251, koi8-r, shift_jis, gbk,
// …). UTF-8 and ASCII pass through. ISO-8859-1 decodes as windows-1252, as
// browsers do, since mail and feeds labelled latin-1 routinely carry cp1252
// quotes and dashes.
func decodeCharset(label string, in io.Reader) (io.Reader, error) {
	l := strings.ToLower(strings.TrimSpace(label))
	switch l {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		return in, nil
	}
	enc, err := htmlindex.Get(l)
	if err != nil {
		return nil, fmt.Errorf("unsupported charset %q", label)
	}
	return enc.NewDecoder().Reader(in), nil
}
