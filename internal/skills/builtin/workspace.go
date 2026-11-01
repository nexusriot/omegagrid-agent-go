package builtin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

// WorkspaceConfig is the one directory the file skills may touch.
type WorkspaceConfig struct {
	Dir      string
	MaxBytes int64 // total size cap enforced by file_write; 0 = 100 MiB
}

const (
	maxFileRead  = 256 << 10
	maxFileWrite = 1 << 20
	maxListItems = 500
)

// open returns the workspace as an os.Root, creating it on first use. Every
// path below is resolved through the Root, which refuses anything that
// escapes the directory — "..", absolute paths and symlinks pointing out
// alike — at the system-call level, not by string inspection.
func (w WorkspaceConfig) open() (*os.Root, error) {
	if w.Dir == "" {
		return nil, errors.New("no workspace directory is configured (SKILL_WORKSPACE_DIR)")
	}
	if err := os.MkdirAll(w.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("create workspace: %w", err)
	}
	return os.OpenRoot(w.Dir)
}

func (w WorkspaceConfig) maxBytes() int64 {
	if w.MaxBytes > 0 {
		return w.MaxBytes
	}
	return 100 << 20
}

// relPath normalises a model-supplied path: a leading "/" means the
// workspace root, not the filesystem root.
func relPath(p string) (string, error) {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	p = path.Clean("/" + p)
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		p = "."
	}
	if strings.ContainsRune(p, 0) {
		return "", errors.New("invalid path")
	}
	return p, nil
}

func FileListSchema() Skill {
	return Skill{Name: "file_list", Description: "List files in the agent's workspace directory (the only place the file skills can read or write).",
		Parameters: map[string]Param{
			"path":      {Type: "string", Description: "Directory inside the workspace (default the root)", Required: false},
			"recursive": {Type: "boolean", Description: "Include subdirectories (default false)", Required: false},
		}}
}

func FileList(w WorkspaceConfig) Executor {
	return func(args map[string]any) (any, error) {
		root, err := w.open()
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		defer root.Close()
		dir, err := relPath(str(args, "path"))
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		recursive := boolOr(args, "recursive", false)
		type entry struct {
			Path     string `json:"path"`
			Type     string `json:"type"`
			Size     int64  `json:"size,omitempty"`
			Modified string `json:"modified"`
		}
		var entries []entry
		truncated := false
		err = fs.WalkDir(root.FS(), dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if p == dir {
				return nil
			}
			if len(entries) == maxListItems {
				truncated = true
				return fs.SkipAll
			}
			info, ierr := d.Info()
			if ierr != nil {
				return nil
			}
			e := entry{Path: p, Type: "file", Modified: info.ModTime().UTC().Format(time.RFC3339)}
			switch {
			case d.IsDir():
				e.Type = "dir"
			case d.Type()&fs.ModeSymlink != 0:
				e.Type = "symlink"
			default:
				e.Size = info.Size()
			}
			entries = append(entries, e)
			if d.IsDir() && !recursive {
				return fs.SkipDir
			}
			return nil
		})
		if err != nil {
			return map[string]any{"path": dir, "error": workspaceErr(err)}, nil
		}
		if entries == nil {
			entries = []entry{}
		}
		used, _ := workspaceSize(root)
		return map[string]any{"path": dir, "entries": entries, "count": len(entries), "truncated": truncated,
			"workspace_used_bytes": used, "workspace_limit_bytes": w.maxBytes()}, nil
	}
}

func FileReadSchema() Skill {
	return Skill{Name: "file_read", Description: "Read a text file from the agent's workspace, in chunks of up to 256 KB.",
		Parameters: map[string]Param{
			"path":      {Type: "string", Description: "File path inside the workspace", Required: true},
			"offset":    {Type: "number", Description: "Byte offset to start at (default 0; use next_offset to continue)", Required: false},
			"max_bytes": {Type: "number", Description: "Bytes to read (default 64 KB, max 256 KB)", Required: false},
		}}
}

func FileRead(w WorkspaceConfig) Executor {
	return func(args map[string]any) (any, error) {
		p, err := relPath(str(args, "path"))
		if err != nil || p == "." {
			return map[string]any{"error": "path is required"}, nil
		}
		root, err := w.open()
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		defer root.Close()
		f, err := root.Open(p)
		if err != nil {
			return map[string]any{"path": p, "error": workspaceErr(err)}, nil
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return map[string]any{"path": p, "error": workspaceErr(err)}, nil
		}
		if !info.Mode().IsRegular() {
			return map[string]any{"path": p, "error": "not a regular file"}, nil
		}
		offset := int64(max(intOr(args, "offset", 0), 0))
		n := min(max(intOr(args, "max_bytes", 64<<10), 1), maxFileRead)
		buf := make([]byte, n)
		got, err := f.ReadAt(buf, offset)
		if err != nil && !errors.Is(err, io.EOF) {
			return map[string]any{"path": p, "error": err.Error()}, nil
		}
		buf = buf[:got]
		next := offset + int64(got)
		if next < info.Size() {
			// Do not end a chunk inside a rune; the next call resumes there.
			trimmed := trimPartialRune(buf)
			next -= int64(len(buf) - len(trimmed))
			buf = trimmed
		}
		out := map[string]any{"path": p, "size": info.Size(), "offset": offset, "bytes": len(buf), "eof": next >= info.Size()}
		if next < info.Size() {
			out["next_offset"] = next
		}
		if !utf8.Valid(buf) || bytes.IndexByte(buf, 0) >= 0 {
			out["binary"] = true
			out["note"] = "binary file; use pdf_text for PDFs"
			return out, nil
		}
		out["content"] = string(buf)
		return out, nil
	}
}

func FileWriteSchema() Skill {
	return Skill{Name: "file_write", Description: "Write a text file in the agent's workspace (creating directories as needed): a report, notes, a config. " +
		"Up to 1 MB per call; the workspace has a total size limit.",
		Parameters: map[string]Param{
			"path":    {Type: "string", Description: "File path inside the workspace", Required: true},
			"content": {Type: "string", Description: "Text to write", Required: true},
			"mode":    {Type: "string", Description: "overwrite (default), append, or create (fail if it exists)", Required: false},
		}}
}

func FileWrite(w WorkspaceConfig) Executor {
	return func(args map[string]any) (any, error) {
		p, err := relPath(str(args, "path"))
		if err != nil || p == "." {
			return map[string]any{"error": "path is required"}, nil
		}
		content, ok := args["content"].(string)
		if !ok {
			return map[string]any{"error": "content must be a string"}, nil
		}
		if len(content) > maxFileWrite {
			return map[string]any{"error": fmt.Sprintf("content is %d bytes; at most %d per call (append in parts)", len(content), maxFileWrite)}, nil
		}
		flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
		switch mode := strings.ToLower(strings.TrimSpace(str(args, "mode"))); mode {
		case "", "overwrite":
		case "append":
			flags = os.O_WRONLY | os.O_CREATE | os.O_APPEND
		case "create":
			flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
		default:
			return map[string]any{"error": "mode must be overwrite, append or create"}, nil
		}
		root, err := w.open()
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		defer root.Close()

		used, err := workspaceSize(root)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		var existing int64
		if info, err := root.Stat(p); err == nil {
			if !info.Mode().IsRegular() {
				return map[string]any{"path": p, "error": "not a regular file"}, nil
			}
			if flags&os.O_TRUNC != 0 {
				existing = info.Size()
			}
		}
		if used-existing+int64(len(content)) > w.maxBytes() {
			return map[string]any{"path": p, "error": fmt.Sprintf("workspace limit of %d bytes would be exceeded (%d used)", w.maxBytes(), used)}, nil
		}
		if dir := path.Dir(p); dir != "." {
			if err := root.MkdirAll(dir, 0o755); err != nil {
				return map[string]any{"path": p, "error": workspaceErr(err)}, nil
			}
		}
		f, err := root.OpenFile(p, flags, 0o644)
		if err != nil {
			return map[string]any{"path": p, "error": workspaceErr(err)}, nil
		}
		_, werr := f.WriteString(content)
		cerr := f.Close()
		if werr != nil || cerr != nil {
			return map[string]any{"path": p, "error": fmt.Sprint(errors.Join(werr, cerr))}, nil
		}
		info, _ := root.Stat(p)
		size := int64(0)
		if info != nil {
			size = info.Size()
		}
		return map[string]any{"path": p, "written": len(content), "size": size}, nil
	}
}

// workspaceSize sums regular file sizes under the root.
func workspaceSize(root *os.Root) (int64, error) {
	var total int64
	err := fs.WalkDir(root.FS(), ".", func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total, err
}

// workspaceErr words the errors a model can act on.
func workspaceErr(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "no such file or directory in the workspace"
	case errors.Is(err, fs.ErrExist):
		return "already exists (mode=create)"
	case strings.Contains(err.Error(), "escapes"):
		return "path is outside the workspace"
	}
	return err.Error()
}

func PdfTextSchema() Skill {
	return Skill{Name: "pdf_text", Description: "Extract the text of a PDF from the workspace or a URL, optionally a page range.",
		Parameters: map[string]Param{
			"path":      {Type: "string", Description: "PDF inside the workspace", Required: false},
			"url":       {Type: "string", Description: "http(s) URL of a PDF (up to 20 MB) — instead of path", Required: false},
			"pages":     {Type: "string", Description: "Page range such as 1-3 or 2 (default all)", Required: false},
			"max_chars": {Type: "number", Description: "Max characters returned (default 20000, max 200000)", Required: false},
		}}
}

const maxPDFBytes = 20 << 20

// PdfText is PdfTextContext without cancellation.
func PdfText(w WorkspaceConfig, timeoutSec float64) Executor {
	return withoutContext(PdfTextContext(w, timeoutSec))
}

func PdfTextContext(w WorkspaceConfig, timeoutSec float64) ContextExecutor {
	cl := httpClient(timeoutSec)
	return func(ctx context.Context, args map[string]any) (any, error) {
		var data []byte
		src := str(args, "url")
		switch {
		case src != "":
			if err := checkHTTPURL(src); err != nil {
				return map[string]any{"error": err.Error()}, nil
			}
			resp, err := getWithRetry(ctx, cl, src, http.Header{"User-Agent": {"OmegaGridAgent/1.0"}})
			if err != nil {
				return map[string]any{"error": err.Error()}, nil
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 400 {
				return map[string]any{"url": src, "error": fmt.Sprintf("HTTP %d", resp.StatusCode)}, nil
			}
			b, truncated, err := readCapped(resp.Body, maxPDFBytes)
			if err != nil || truncated {
				return map[string]any{"url": src, "error": "PDF larger than 20 MB or unreadable"}, nil
			}
			data = b
		case str(args, "path") != "":
			p, err := relPath(str(args, "path"))
			if err != nil {
				return map[string]any{"error": err.Error()}, nil
			}
			src = p
			root, err := w.open()
			if err != nil {
				return map[string]any{"error": err.Error()}, nil
			}
			defer root.Close()
			f, err := root.Open(p)
			if err != nil {
				return map[string]any{"path": p, "error": workspaceErr(err)}, nil
			}
			defer f.Close()
			b, truncated, err := readCapped(f, maxPDFBytes)
			if err != nil || truncated {
				return map[string]any{"path": p, "error": "PDF larger than 20 MB or unreadable"}, nil
			}
			data = b
		default:
			return map[string]any{"error": "path or url is required"}, nil
		}
		maxChars := min(max(intOr(args, "max_chars", 20000), 1), 200000)
		return extractPDF(data, src, str(args, "pages"), maxChars), nil
	}
}

// extractPDF runs the parser behind a recover: a malformed file must come
// back as an error result, not a crashed tool.
func extractPDF(data []byte, src, pages string, maxChars int) (out map[string]any) {
	defer func() {
		if r := recover(); r != nil {
			out = map[string]any{"source": src, "error": fmt.Sprintf("unreadable PDF: %v", r)}
		}
	}()
	if !bytes.HasPrefix(bytes.TrimLeft(data, "\x00\t\r\n "), []byte("%PDF")) {
		return map[string]any{"source": src, "error": "not a PDF file"}
	}
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return map[string]any{"source": src, "error": "unreadable PDF: " + err.Error()}
	}
	n := r.NumPage()
	first, last, err := pageRange(pages, n)
	if err != nil {
		return map[string]any{"source": src, "error": err.Error(), "page_count": n}
	}
	var b strings.Builder
	truncated := false
	for i := first; i <= last; i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		text, err := p.GetPlainText(nil)
		if err != nil {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "[page %d]\n%s", i, strings.TrimSpace(text))
		if utf8.RuneCountInString(b.String()) > maxChars {
			truncated = true
			break
		}
	}
	text := b.String()
	if utf8.RuneCountInString(text) > maxChars {
		text = string([]rune(text)[:maxChars])
		truncated = true
	}
	out = map[string]any{"source": src, "page_count": n, "pages": fmt.Sprintf("%d-%d", first, last), "text": text, "truncated": truncated}
	if strings.TrimSpace(strings.NewReplacer("[page", "").Replace(text)) == "" || len(strings.Fields(text)) <= (last-first+1)*2 {
		out["note"] = "little or no text found — the PDF may be scanned images (no OCR here)"
	}
	return out
}

func pageRange(spec string, n int) (int, int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return 1, n, nil
	}
	lo, hi, isRange := strings.Cut(spec, "-")
	a, err1 := strconv.Atoi(strings.TrimSpace(lo))
	b := a
	var err2 error
	if isRange {
		b, err2 = strconv.Atoi(strings.TrimSpace(hi))
	}
	if err1 != nil || err2 != nil || a < 1 || b < a {
		return 0, 0, fmt.Errorf("invalid page range %q", spec)
	}
	if a > n {
		return 0, 0, fmt.Errorf("page %d is past the end (%d pages)", a, n)
	}
	return a, min(b, n), nil
}
