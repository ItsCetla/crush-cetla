package chat

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
)

const (
	wrapDiffOld = "function Copy-Tree($t) {\n" +
		"    foreach ($f in (Get-ChildItem $t.s)) {\n" +
		"        Copy-Item $f $t.d\n" +
		"    }\n" +
		"}\n"
	wrapDiffNew = "function Copy-Tree($t) {\n" +
		"    foreach ($f in (Get-ChildItem $t.s -Recurse -File -Force -EA SilentlyContinue)) {  # plus a long trailing comment that definitely overflows the column\n" +
		"        Copy-Item $f $t.d\n" +
		"    }\n" +
		"}\n"
)

func TestToolOutputDiffContentWraps(t *testing.T) {
	t.Parallel()

	for _, width := range []int{160, 100} {
		t.Run(fmt.Sprintf("Width%d", width), func(t *testing.T) {
			t.Parallel()

			sty := styles.CharmtonePantera()
			out := toolOutputDiffContent(&sty, "copy.ps1", wrapDiffOld, wrapDiffNew, width, true)
			plain := ansi.Strip(out)
			if os.Getenv("CRUSH_PRINT_DIFF") != "" {
				t.Logf("width %d:\n%s", width, plain)
			}

			for i, line := range strings.Split(out, "\n") {
				if w := ansi.StringWidth(line); w > width {
					t.Errorf("line %d: width %d exceeds %d: %q", i, w, width, ansi.Strip(line))
				}
				if s := ansi.Strip(line); !strings.Contains(s, "@@") && strings.Contains(s, "…") {
					t.Errorf("line %d is truncated: %q", i, s)
				}
			}
			for _, word := range []string{"-Recurse", "SilentlyContinue))", "overflows", "column"} {
				if !strings.Contains(plain, word) {
					t.Errorf("expected %q in wrapped output", word)
				}
			}
		})
	}
}
