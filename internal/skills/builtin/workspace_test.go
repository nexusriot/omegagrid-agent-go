package builtin

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newWorkspace(t *testing.T) (WorkspaceConfig, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "ws")
	return WorkspaceConfig{Dir: dir, MaxBytes: 4096}, dir
}

func TestFileWriteReadList(t *testing.T) {
	w, dir := newWorkspace(t)
	m := runExec(t, FileWrite(w), map[string]any{"path": "/reports/2026/oct.md", "content": "# October\nall good\n"})
	if m["written"] != 19 || m["path"] != "reports/2026/oct.md" {
		t.Fatalf("write = %v", m)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "reports/2026/oct.md")); string(b) != "# October\nall good\n" {
		t.Fatalf("on disk = %q", b)
	}
	runExec(t, FileWrite(w), map[string]any{"path": "reports/2026/oct.md", "content": "more\n", "mode": "append"})
	m = runExec(t, FileRead(w), map[string]any{"path": "reports/2026/oct.md"})
	if m["content"] != "# October\nall good\nmore\n" || m["eof"] != true {
		t.Fatalf("read = %v", m)
	}
	if m := runExec(t, FileWrite(w), map[string]any{"path": "reports/2026/oct.md", "content": "x", "mode": "create"}); m["error"] == nil {
		t.Fatal("mode=create overwrote a file")
	}

	m = runExec(t, FileList(w), map[string]any{"recursive": true})
	if m["count"] != 3 || m["workspace_used_bytes"].(int64) != 24 {
		t.Fatalf("list = %v", m)
	}
	m = runExec(t, FileList(w), map[string]any{})
	if m["count"] != 1 {
		t.Fatalf("non-recursive list = %v", m)
	}
}

func TestFileReadChunksOnRuneBoundary(t *testing.T) {
	w, dir := newWorkspace(t)
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "u.txt"), []byte("aé€"), 0o644) // 1+2+3 bytes
	m := runExec(t, FileRead(w), map[string]any{"path": "u.txt", "max_bytes": 4})
	if m["content"] != "aé" || m["next_offset"] != int64(3) {
		t.Fatalf("chunk = %v", m)
	}
	m = runExec(t, FileRead(w), map[string]any{"path": "u.txt", "offset": 3})
	if m["content"] != "€" || m["eof"] != true {
		t.Fatalf("rest = %v", m)
	}
	_ = os.WriteFile(filepath.Join(dir, "b.bin"), []byte{0, 1, 2, 0xff}, 0o644)
	if m := runExec(t, FileRead(w), map[string]any{"path": "b.bin"}); m["binary"] != true || m["content"] != nil {
		t.Fatalf("binary = %v", m)
	}
}

// The workspace is the boundary: "..", absolute paths and symlinks that
// point outside are all refused by os.Root.
func TestWorkspaceConfinement(t *testing.T) {
	w, dir := newWorkspace(t)
	_ = os.MkdirAll(dir, 0o755)
	outside := filepath.Join(filepath.Dir(dir), "secret.txt")
	_ = os.WriteFile(outside, []byte("secret"), 0o644)
	_ = os.Symlink(outside, filepath.Join(dir, "link.txt"))
	_ = os.Symlink(filepath.Dir(dir), filepath.Join(dir, "up"))

	for _, p := range []string{"../secret.txt", "link.txt", "up/secret.txt", "a/../../secret.txt"} {
		if m := runExec(t, FileRead(w), map[string]any{"path": p}); m["content"] != nil {
			t.Errorf("read %q escaped the workspace: %v", p, m)
		}
	}
	// A leading "/" or ".." is clamped to the workspace root by relPath.
	if m := runExec(t, FileRead(w), map[string]any{"path": "/etc/passwd"}); m["content"] != nil {
		t.Errorf("/etc/passwd read: %v", m)
	}
	if m := runExec(t, FileWrite(w), map[string]any{"path": "up/pwned.txt", "content": "x"}); m["error"] == nil {
		t.Errorf("write through a symlink escaped: %v", m)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "pwned.txt")); err == nil {
		t.Fatal("a file was written outside the workspace")
	}
	m := runExec(t, FileWrite(w), map[string]any{"path": "../../escape.txt", "content": "x"})
	if m["path"] != "escape.txt" {
		t.Fatalf("'..' must clamp to the root: %v", m)
	}
}

func TestWorkspaceQuota(t *testing.T) {
	w, _ := newWorkspace(t)
	if m := runExec(t, FileWrite(w), map[string]any{"path": "a", "content": strings.Repeat("x", 3000)}); m["error"] != nil {
		t.Fatal(m)
	}
	if m := runExec(t, FileWrite(w), map[string]any{"path": "b", "content": strings.Repeat("x", 2000)}); m["error"] == nil {
		t.Fatal("quota not enforced")
	}
	// Overwriting a file counts only the difference.
	if m := runExec(t, FileWrite(w), map[string]any{"path": "a", "content": strings.Repeat("y", 4000)}); m["error"] != nil {
		t.Fatalf("overwrite within quota refused: %v", m)
	}
	if m := runExec(t, FileWrite(w), map[string]any{"path": "big", "content": strings.Repeat("x", maxFileWrite+1)}); m["error"] == nil {
		t.Fatal("oversized single write accepted")
	}
	if m := runExec(t, FileList(WorkspaceConfig{}), map[string]any{}); m["error"] == nil {
		t.Fatal("unconfigured workspace accepted")
	}
}

// minimalPDF builds a valid one-font PDF with one page per text.
func minimalPDF(pages ...string) []byte {
	var b bytes.Buffer
	var offsets []int
	obj := func(body string) {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}
	b.WriteString("%PDF-1.4\n")
	n := len(pages)
	kids := ""
	for i := range pages {
		kids += fmt.Sprintf("%d 0 R ", 4+2*i)
	}
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, n))
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	for i, text := range pages {
		stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", 5+2*i))
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return b.Bytes()
}

func TestPdfText(t *testing.T) {
	w, dir := newWorkspace(t)
	w.MaxBytes = 1 << 20
	_ = os.MkdirAll(dir, 0o755)
	doc := minimalPDF("Quarterly report", "Revenue grew", "Outlook stable")
	_ = os.WriteFile(filepath.Join(dir, "r.pdf"), doc, 0o644)

	m := runExec(t, PdfText(w, 5), map[string]any{"path": "r.pdf"})
	text, _ := m["text"].(string)
	if m["page_count"] != 3 || !strings.Contains(text, "Quarterly report") || !strings.Contains(text, "[page 3]") {
		t.Fatalf("pdf = %v", m)
	}
	m = runExec(t, PdfText(w, 5), map[string]any{"path": "r.pdf", "pages": "2"})
	if text := m["text"].(string); !strings.Contains(text, "Revenue grew") || strings.Contains(text, "Quarterly") {
		t.Fatalf("page 2 = %v", m)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(doc) }))
	defer srv.Close()
	if m := runExec(t, PdfText(w, 5), map[string]any{"url": srv.URL}); !strings.Contains(fmt.Sprint(m["text"]), "Outlook stable") {
		t.Fatalf("url = %v", m)
	}

	_ = os.WriteFile(filepath.Join(dir, "fake.pdf"), []byte("hello"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "broken.pdf"), []byte("%PDF-1.4\ngarbage garbage"), 0o644)
	for _, args := range []map[string]any{
		{"path": "fake.pdf"}, {"path": "broken.pdf"}, {"path": "r.pdf", "pages": "9"},
		{"path": "r.pdf", "pages": "3-1"}, {}, {"path": "../x.pdf"},
	} {
		if m := runExec(t, PdfText(w, 5), args); m["error"] == nil {
			t.Errorf("%v accepted: %v", args, m)
		}
	}
}
