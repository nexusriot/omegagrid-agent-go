// Package examples ships ready-made markdown pipeline skills. They are
// installed into SKILLS_DIR on first start (SKILLS_INSTALL_EXAMPLES, default
// on) and from then on belong to the user: an edited copy is never
// overwritten, and a deleted one is not reinstalled.
package examples

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed *.md
var files embed.FS

// markerFile records which examples were installed once already.
const markerFile = ".examples-installed"

// Names lists the bundled examples (file names).
func Names() []string {
	entries, _ := fs.ReadDir(files, ".")
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// Source returns the bundled content of one example.
func Source(name string) ([]byte, error) { return files.ReadFile(name) }

// Install copies each bundled example into dir unless it is already there
// or was installed before (and since deleted by the user). It returns the
// names written.
func Install(dir string) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	done := map[string]bool{}
	if b, err := os.ReadFile(filepath.Join(dir, markerFile)); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				done[l] = true
			}
		}
	}
	var written []string
	for _, name := range Names() {
		if done[name] {
			continue
		}
		dst := filepath.Join(dir, name)
		if _, err := os.Stat(dst); err == nil {
			done[name] = true
			continue
		}
		b, err := files.ReadFile(name)
		if err != nil {
			return written, err
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return written, err
		}
		done[name] = true
		written = append(written, name)
	}
	names := make([]string, 0, len(done))
	for n := range done {
		names = append(names, n)
	}
	sort.Strings(names)
	err := os.WriteFile(filepath.Join(dir, markerFile), []byte(strings.Join(names, "\n")+"\n"), 0o644)
	return written, err
}
