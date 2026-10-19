package richtext

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

var goldenTables = []struct{ name, src string }{
	{"portfolio", `| Ticker | Company | Shares | Price | Change | Weight |
|---|---|---:|---:|---:|:-:|
| AAPL | Apple Inc. | 120 | $198.40 | +1.25% | 18.2% |
| MSFT | Microsoft Corporation | 85 | $440.10 | -0.40% | 27.5% |
| NVDA | NVIDIA | 1,200 | $131.7 | +3.1% | 30% |
| BRK.B | Berkshire Hathaway Class B | 15 | $472.00 | — | 5.1% |
| CASH | Money market sweep | | $1.00 | 0% | 19.2% |
`},
	{"inline", "| Feature | Status | Notes |\n|:--|:-:|--|\n| **Tables** | _done_ | Uses `lipgloss/table` with a pipe: `a\\|b` |\n| *Charts* | **wip** | Braille line plots, [docs](https://example.com) and \\*escapes\\* |\n| `Split` | done | goldmark AST walk |\n"},
	{"prose", `| Step | Description |
|---|---|
| 1 | Install the CLI with your package manager of choice and make sure it is on your PATH before continuing. |
| 2 | Run the setup wizard, which asks for an API key and writes a config file to your home directory. |
| 3 | Start a session. |
`},
	{"wide", `| Region | Q1 | Q2 | Q3 | Q4 | FY | YoY | Margin | Headcount | Offices |
|---|---|---|---|---|---|---|---|---|---|
| North America | 1,204.5 | 1,310.2 | 1,288.9 | 1,450.0 | 5,253.6 | 12.4% | 31.2% | 4,210 | 14 |
| Europe, Middle East and Africa | 802.1 | 845.7 | 790.3 | 910.8 | 3,348.9 | 8.1% | 27.9% | 2,980 | 22 |
| Asia Pacific | 640.0 | 702.4 | 755.1 | 820.6 | 2,918.1 | 19.8% | 25.0% | 3,105 | 17 |
`},
	{"unicode", `| 名前 | 都市 | 点数 |
|---|---|---|
| 山田太郎 | 東京 | 98.5 |
| Zoë | Zürich 🇨🇭 | 7 |
| José | São Paulo | 100 |
`},
}

func TestRenderTableGolden(t *testing.T) {
	t.Parallel()
	for _, tc := range goldenTables {
		for _, width := range []int{60, 100, 160} {
			for _, th := range themes {
				name := fmt.Sprintf("table_%s_%d_%s", tc.name, width, th.name)
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					out, err := RenderTable(tc.src, width, th.th)
					if err != nil {
						t.Fatal(err)
					}
					assertLayout(t, out, width)
					plain := th.th
					plain.Plain = true
					p, _ := RenderTable(tc.src, width, plain)
					assertPlain(t, out, p)
					golden(t, name, out)
				})
			}
		}
	}
}

func TestRenderTableWidths(t *testing.T) {
	t.Parallel()
	for _, tc := range goldenTables {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for width := 8; width <= 180; width++ {
				out, err := RenderTable(tc.src, width, DarkTheme())
				if err != nil {
					t.Fatalf("width %d: %v", width, err)
				}
				assertLayout(t, out, width)
			}
		})
	}
}

func TestRenderTableNumbersNeverTruncated(t *testing.T) {
	t.Parallel()
	src := goldenTables[3].src
	for _, width := range []int{60, 100} {
		out, _ := RenderTable(src, width, Theme{Plain: true})
		for _, v := range []string{"1,204.5", "5,253.6", "3,348.9", "19.8%", "4,210"} {
			if !strings.Contains(out, v) {
				t.Errorf("width %d: %s missing", width, v)
			}
		}
	}
}

func TestRenderTableDecimalAlign(t *testing.T) {
	t.Parallel()
	src := "| x |\n|---|\n| 1.5 |\n| 10 |\n| 100.25 |\n| — |\n"
	out, err := RenderTable(src, 40, Theme{Plain: true})
	if err != nil {
		t.Fatal(err)
	}
	var dots []int
	for _, l := range strings.Split(out, "\n") {
		for _, v := range []string{"1.5", "10", "100.25"} {
			if strings.Contains(l, " "+v+" ") {
				i := strings.Index(l, v) + strings.IndexByte(v+".", '.')
				dots = append(dots, ansi.StringWidth(l[:i]))
			}
		}
	}
	if len(dots) != 3 || dots[0] != dots[1] || dots[1] != dots[2] {
		t.Errorf("decimal points not aligned: %v\n%s", dots, out)
	}
}

func TestRenderTableStacked(t *testing.T) {
	t.Parallel()
	out, err := RenderTable(goldenTables[3].src, 30, Theme{Plain: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "╭") || !strings.Contains(out, "Region:") {
		t.Errorf("want stacked layout, got:\n%s", out)
	}
}

func TestRenderTableErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		src   string
		width int
		want  string
	}{
		{"| a |\n|---|\n", 4, "too narrow"},
		{"just text", 40, "not a table"},
		{"| a | b |\n| 1 | 2 |\n", 40, "missing delimiter row"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			if _, err := RenderTable(tt.src, tt.width, DarkTheme()); err == nil || err.Error() != tt.want {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestSplitRow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want []string
	}{
		{"| a | b |", []string{"a", "b"}},
		{"a | b", []string{"a", "b"}},
		{`| a \| b | c |`, []string{`a \| b`, "c"}},
		{"| `x|y` | z |", []string{"`x|y`", "z"}},
		{"| | |", []string{"", ""}},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got := splitRow(tt.in)
			if strings.Join(got, "\x00") != strings.Join(tt.want, "\x00") {
				t.Errorf("splitRow(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNumericColumn(t *testing.T) {
	t.Parallel()
	tests := []struct {
		cells []string
		want  bool
	}{
		{[]string{"1", "2.5", "1,000"}, true},
		{[]string{"$1.2k", "-3%", "(45.00)", "—"}, true},
		{[]string{"1.5x", "120ms", "n/a"}, true},
		{[]string{"1", "two"}, false},
		{[]string{"—", ""}, false},
		{[]string{"1,00"}, false},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.cells, ","), func(t *testing.T) {
			t.Parallel()
			if got := isNumericColumn(tt.cells); got != tt.want {
				t.Errorf("isNumericColumn(%q) = %v, want %v", tt.cells, got, tt.want)
			}
		})
	}
}

func TestParseInline(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"**bold** and _it_", "bold and it"},
		{"`code *x*`", "code *x*"},
		{`\*lit\*`, "*lit*"},
		{"[text](http://x)", "text"},
		{"snake_case_name", "snake_case_name"},
		{"2 * 3 * 4", "2 * 3 * 4"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			if got := parseInline(tt.in).plain(); got != tt.want {
				t.Errorf("plain(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
