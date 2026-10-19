package richtext

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// Theme holds the colors the renderers use. The zero value renders without
// color.
type Theme struct {
	Text, Dim, Header, Border, Stripe, Error color.Color
	// Series is the chart palette, in order.
	Series []color.Color
	// Plain disables all ANSI styling; layout is unchanged.
	Plain bool
}

// DarkTheme is the cetla dark palette.
func DarkTheme() Theme {
	return Theme{
		Text:   lipgloss.Color("#F4EFE6"),
		Dim:    lipgloss.Color("#8E867A"),
		Header: lipgloss.Color("#FFB347"),
		Border: lipgloss.Color("#2E2A25"),
		Stripe: lipgloss.Color("#1A1815"),
		Error:  lipgloss.Color("#F0524F"),
		Series: palette(
			"#FF6A3D", "#FFB347", "#F5D08A", "#3DDC84",
			"#7CB8F0", "#C49BFF", "#5CD6C8", "#F29E9E",
		),
	}
}

// LightTheme is the light counterpart of DarkTheme.
func LightTheme() Theme {
	return Theme{
		Text:   lipgloss.Color("#1F1B16"),
		Dim:    lipgloss.Color("#6B6358"),
		Header: lipgloss.Color("#B35A00"),
		Border: lipgloss.Color("#D9D1C4"),
		Stripe: lipgloss.Color("#F1EBE0"),
		Error:  lipgloss.Color("#C62828"),
		Series: palette(
			"#E8501F", "#C77700", "#A07A1F", "#1F8A50",
			"#2F6FAF", "#7A4FC2", "#1F8A80", "#B5566A",
		),
	}
}

func palette(hex ...string) []color.Color {
	out := make([]color.Color, len(hex))
	for i, h := range hex {
		out[i] = lipgloss.Color(h)
	}
	return out
}

// seriesColor returns the palette color for series i, or nil.
func (th Theme) seriesColor(i int) color.Color {
	if len(th.Series) == 0 {
		return nil
	}
	return th.Series[i%len(th.Series)]
}

// look describes the styling of a run of text.
type look struct {
	fg, bg              color.Color
	bold, italic, faint bool
}

// paint renders s with l unless the theme is plain.
func (th Theme) paint(s string, l look) string {
	if th.Plain || s == "" || l == (look{}) {
		return s
	}
	st := lipgloss.NewStyle().Bold(l.bold).Italic(l.italic).Faint(l.faint)
	if l.fg != nil {
		st = st.Foreground(l.fg)
	}
	if l.bg != nil {
		st = st.Background(l.bg)
	}
	return st.Render(s)
}

func (th Theme) fg(s string, c color.Color) string { return th.paint(s, look{fg: c}) }

func (th Theme) dim(s string) string { return th.fg(s, th.Dim) }

// ErrorLine renders the dim "chart: <error>" note shown under a raw block
// that failed to render.
func ErrorLine(err error, th Theme) string {
	if err == nil {
		return ""
	}
	return th.dim("chart: " + err.Error())
}
