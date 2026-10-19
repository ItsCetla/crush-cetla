package richtext

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

var update = flag.Bool("update", false, "rewrite golden files")

var themes = []struct {
	name string
	th   Theme
}{{"dark", DarkTheme()}, {"light", LightTheme()}}

// assertLayout checks the output invariants shared by every renderer.
func assertLayout(t *testing.T, out string, width int) {
	t.Helper()
	if strings.HasSuffix(out, "\n") {
		t.Errorf("output ends with a newline")
	}
	for i, l := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(l); w > width {
			t.Errorf("line %d is %d wide, max %d: %q", i, w, width, ansi.Strip(l))
		}
	}
}

// assertPlain checks that plain mode has no escapes and the same layout.
func assertPlain(t *testing.T, styled, plain string) {
	t.Helper()
	if strings.Contains(plain, "\x1b") {
		t.Errorf("plain output contains escape sequences")
	}
	if got := ansi.Strip(styled); got != plain {
		t.Errorf("plain layout differs from styled:\nstyled:\n%s\nplain:\n%s", got, plain)
	}
}

// golden compares out with testdata/<name>.golden and its stripped form with
// testdata/<name>.txt, rewriting both with -update.
func golden(t *testing.T, name, out string) {
	t.Helper()
	files := map[string]string{
		filepath.Join("testdata", name+".golden"): out,
		filepath.Join("testdata", name+".txt"):    ansi.Strip(out),
	}
	for path, want := range files {
		if *update {
			if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%v (run with -update)", err)
		}
		if string(got) != want {
			t.Errorf("%s mismatch:\nwant:\n%s\ngot:\n%s", path, ansi.Strip(string(got)), ansi.Strip(want))
		}
	}
}
