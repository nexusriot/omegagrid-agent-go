package examples

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nexusriot/omegagrid-agent-go/internal/skills/markdown"
)

func TestExamplesParse(t *testing.T) {
	if len(Names()) < 5 {
		t.Fatalf("examples: %v", Names())
	}
	for _, n := range Names() {
		b, _ := Source(n)
		sk, err := markdown.Parse(string(b), n)
		if err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		if len(sk.Steps()) == 0 {
			t.Errorf("%s has no skill steps", n)
		}
		if sk.Schema.Name+".md" != n {
			t.Errorf("%s declares name %q", n, sk.Schema.Name)
		}
	}
}

func TestInstallOnceAndRespectDeletion(t *testing.T) {
	dir := t.TempDir()
	w, err := Install(dir)
	if err != nil || len(w) != len(Names()) {
		t.Fatalf("first install: %v %v", w, err)
	}
	// User edits one and deletes another.
	edited := filepath.Join(dir, "site_check.md")
	_ = os.WriteFile(edited, []byte("mine"), 0o644)
	_ = os.Remove(filepath.Join(dir, "domain_report.md"))
	w, err = Install(dir)
	if err != nil || len(w) != 0 {
		t.Fatalf("second install wrote %v %v", w, err)
	}
	if b, _ := os.ReadFile(edited); string(b) != "mine" {
		t.Fatal("edited example overwritten")
	}
	if _, err := os.Stat(filepath.Join(dir, "domain_report.md")); err == nil {
		t.Fatal("deleted example reinstalled")
	}
}
