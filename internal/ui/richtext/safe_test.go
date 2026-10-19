package richtext

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChartStripsControlCharacters(t *testing.T) {
	t.Parallel()
	body := `{"type":"bar","title":"T\u001b]0;pwned\u0007itle","unit":"m\u001b[2Js",` +
		`"labels":["a\u001b[31m","b\u009b1m"],"series":[{"name":"x\u001bc","values":[1,2]},{"name":"y","values":[3,4]}]}`
	for _, th := range []Theme{DarkTheme(), LightTheme(), plainTheme()} {
		out, err := RenderChart(body, 80, th)
		require.NoError(t, err)
		require.NotContains(t, out, "pwned\a")
		require.NotContains(t, out, "\x1b]")
		require.NotContains(t, out, "\x1b[2J")
		require.NotContains(t, out, "\x1bc")
		require.NotContains(t, out, "\u009b")
		require.NotContains(t, out, "\a")
	}
}

func TestTableStripsControlCharacters(t *testing.T) {
	t.Parallel()
	src := "| a\x1b]0;x\x07 | b |\n|---|---|\n| \x1b[2Jv\tw | \u009b1m2 |\n"
	for _, width := range []int{12, 80} {
		out, err := RenderTable(src, width, plainTheme())
		require.NoError(t, err)
		require.NotContains(t, out, "\x1b")
		require.NotContains(t, out, "\a")
		require.NotContains(t, out, "\u009b")
		require.NotContains(t, out, "\t")
	}
}

func TestChartRejectsOutOfRangeValues(t *testing.T) {
	t.Parallel()
	_, err := RenderChart(`{"type":"bar","series":[{"values":[1,1e301]}]}`, 80, DarkTheme())
	require.ErrorContains(t, err, "out of range")
}

func TestChartExtremeValuesDoNotPanic(t *testing.T) {
	t.Parallel()
	sets := []string{
		"[1e300,-1e300]",
		"[1e300,1e300,1e300]",
		"[-1e300,0]",
		"[1e-320,0]",
		"[5e-324,1e-323,0]",
		"[-5e-324,5e-324]",
		"[1e300,1e-300]",
		"[1e17,100000000000000016]",
		"[0,0,0]",
		"[null,1e300,null]",
	}
	for _, kind := range []string{"bar", "hbar", "line", "spark"} {
		for _, vals := range sets {
			body := fmt.Sprintf(`{"type":%q,"series":[{"name":"a","values":%s},{"name":"b","values":%s}]}`, kind, vals, vals)
			for _, width := range []int{20, 60, 100} {
				out, err := RenderChart(body, width, DarkTheme())
				if err != nil {
					continue
				}
				for _, line := range strings.Split(out, "\n") {
					require.LessOrEqual(t, len([]rune(line)), 4000, "%s %s", kind, vals)
				}
			}
		}
	}
}

func TestToIntClamps(t *testing.T) {
	t.Parallel()
	require.Equal(t, 0, toInt(math.NaN(), 0, 7))
	require.Equal(t, 7, toInt(math.Inf(1), 0, 7))
	require.Equal(t, 0, toInt(math.Inf(-1), 0, 7))
	require.Equal(t, 3, toInt(3.4, 0, 7))
}

func plainTheme() Theme {
	th := DarkTheme()
	th.Plain = true
	return th
}
