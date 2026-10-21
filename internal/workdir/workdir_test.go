package workdir

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFromFallsBackWithoutDir(t *testing.T) {
	t.Parallel()
	if got := From(context.Background(), "/launch"); got != "/launch" {
		t.Fatalf("From = %q, want /launch", got)
	}
	if Has(context.Background()) {
		t.Fatal("Has = true for a bare context")
	}
	//nolint:staticcheck // a nil context must not panic.
	if got := From(nil, "/launch"); got != "/launch" {
		t.Fatalf("From(nil) = %q, want /launch", got)
	}
}

func TestWithCarriesCleanDir(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "proj")
	ctx := With(context.Background(), dir+string(filepath.Separator))
	if got := From(ctx, "/launch"); got != dir {
		t.Fatalf("From = %q, want %q", got, dir)
	}
	if !Has(ctx) {
		t.Fatal("Has = false after With")
	}
	if With(ctx, "") != ctx {
		t.Fatal("With(\"\") changed the context")
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	sub := filepath.Join(base, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(base, "file.txt")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Resolve(base, "sub")
	if err != nil || got != sub {
		t.Fatalf("Resolve(sub) = %q, %v; want %q", got, err, sub)
	}
	got, err = Resolve(sub, "..")
	if err != nil || got != base {
		t.Fatalf("Resolve(..) = %q, %v; want %q", got, err, base)
	}
	got, err = Resolve("/elsewhere", sub)
	if err != nil || got != sub {
		t.Fatalf("Resolve(abs) = %q, %v; want %q", got, err, sub)
	}

	home, err := os.UserHomeDir()
	if err == nil {
		got, err = Resolve(base, "~")
		if err != nil || got != filepath.Clean(home) {
			t.Fatalf("Resolve(~) = %q, %v; want %q", got, err, home)
		}
	}

	for _, arg := range []string{"missing", "file.txt"} {
		if _, err := Resolve(base, arg); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Resolve(%q) err = %v, want ErrInvalid", arg, err)
		}
	}
}

func TestCheck(t *testing.T) {
	t.Parallel()
	if err := Check(""); err != nil {
		t.Fatalf("Check(\"\") = %v, want nil", err)
	}
	if err := Check(t.TempDir()); err != nil {
		t.Fatalf("Check(dir) = %v, want nil", err)
	}
	if err := Check("relative/path"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Check(relative) = %v, want ErrInvalid", err)
	}
}
