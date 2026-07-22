package skills

import (
	"errors"
	"testing"
)

func TestRegistryExecute(t *testing.T) {
	t.Run("success returns executor value", func(t *testing.T) {
		r := newRegistry()
		r.register(Skill{Name: "echo", Description: "echoes"}, func(args map[string]any) (any, error) {
			return args["v"], nil
		})
		got, err := r.execute("echo", map[string]any{"v": "hi"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "hi" {
			t.Fatalf("got %v, want hi", got)
		}
	})

	t.Run("propagates executor error", func(t *testing.T) {
		r := newRegistry()
		wantErr := errors.New("boom")
		r.register(Skill{Name: "fail"}, func(map[string]any) (any, error) {
			return nil, wantErr
		})
		_, err := r.execute("fail", nil)
		if !errors.Is(err, wantErr) {
			t.Fatalf("got %v, want %v", err, wantErr)
		}
	})

	t.Run("missing skill returns errNotFound", func(t *testing.T) {
		r := newRegistry()
		_, err := r.execute("ghost", nil)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		var nf *errNotFound
		if !errors.As(err, &nf) {
			t.Fatalf("expected *errNotFound, got %T", err)
		}
		if nf.name != "ghost" {
			t.Fatalf("errNotFound.name = %q, want ghost", nf.name)
		}
		if got, want := err.Error(), "skill not found: ghost"; got != want {
			t.Fatalf("Error() = %q, want %q", got, want)
		}
	})
}

func TestRegistryRegisterUpsert(t *testing.T) {
	r := newRegistry()
	r.register(Skill{Name: "foo", Description: "first"}, func(map[string]any) (any, error) {
		return "v1", nil
	})
	if n := len(r.list()); n != 1 {
		t.Fatalf("list len = %d, want 1", n)
	}

	r.register(Skill{Name: "foo", Description: "second"}, func(map[string]any) (any, error) {
		return "v2", nil
	})

	got := r.list()
	if len(got) != 1 {
		t.Fatalf("after upsert list len = %d, want 1 (replace by name)", len(got))
	}
	if got[0].Description != "second" {
		t.Fatalf("schema not replaced: Description = %q, want second", got[0].Description)
	}
	res, err := r.execute("foo", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != "v2" {
		t.Fatalf("executor not replaced: got %v, want v2", res)
	}
}

func TestRegistryUnregister(t *testing.T) {
	r := newRegistry()
	r.register(Skill{Name: "foo"}, func(map[string]any) (any, error) { return 1, nil })
	r.register(Skill{Name: "bar"}, func(map[string]any) (any, error) { return 2, nil })

	r.unregister("foo")

	got := r.list()
	if len(got) != 1 {
		t.Fatalf("list len = %d, want 1", len(got))
	}
	if got[0].Name != "bar" {
		t.Fatalf("remaining skill = %q, want bar", got[0].Name)
	}
	if _, err := r.execute("foo", nil); err == nil {
		t.Fatal("expected errNotFound after unregister, got nil")
	}

	r.unregister("does-not-exist")
	if len(r.list()) != 1 {
		t.Fatal("list len changed after no-op unregister of missing name")
	}
}

func TestRegistryList(t *testing.T) {
	r := newRegistry()
	if got := r.list(); len(got) != 0 {
		t.Fatalf("empty registry list len = %d, want 0", len(got))
	}

	names := []string{"a", "b", "c"}
	for _, n := range names {
		r.register(Skill{Name: n}, func(map[string]any) (any, error) { return nil, nil })
	}

	got := r.list()
	if len(got) != len(names) {
		t.Fatalf("list len = %d, want %d", len(got), len(names))
	}
	seen := map[string]bool{}
	for _, s := range got {
		seen[s.Name] = true
	}
	for _, n := range names {
		if !seen[n] {
			t.Fatalf("skill %q missing from list", n)
		}
	}
}
