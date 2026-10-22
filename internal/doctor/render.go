package doctor

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/exp/charmtone"
)

// Styles controls how WriteText renders a report.
type Styles struct {
	Title lipgloss.Style
	Name  lipgloss.Style
	// Detail renders the text of ok and skipped items.
	Detail lipgloss.Style
	Hint   lipgloss.Style
	Footer lipgloss.Style
	Status map[Status]lipgloss.Style
	// Marks are the per-status symbols, all the same width.
	Marks map[Status]string
	// Arrow starts a hint line.
	Arrow string
}

// PlainStyles renders ASCII without colors, for pipes and logs.
func PlainStyles() Styles {
	return Styles{
		Marks: map[Status]string{
			StatusOK:   "[ok]  ",
			StatusWarn: "[warn]",
			StatusFail: "[fail]",
			StatusSkip: "[skip]",
		},
		Arrow: "->",
	}
}

// ColorStyles renders with the Charmtone colors the rest of Ta-Da uses.
func ColorStyles() Styles {
	fg := func(c charmtone.Key) lipgloss.Style {
		return lipgloss.NewStyle().Foreground(c)
	}
	return Styles{
		Title:  fg(charmtone.Charple).Bold(true),
		Name:   lipgloss.NewStyle().Bold(true),
		Detail: fg(charmtone.Squid),
		Hint:   fg(charmtone.Malibu),
		Footer: fg(charmtone.Squid),
		Status: map[Status]lipgloss.Style{
			StatusOK:   fg(charmtone.Guac),
			StatusWarn: fg(charmtone.Mustard),
			StatusFail: fg(charmtone.Sriracha).Bold(true),
			StatusSkip: fg(charmtone.Squid),
		},
		Marks: map[Status]string{
			StatusOK:   "✓",
			StatusWarn: "!",
			StatusFail: "✕",
			StatusSkip: "-",
		},
		Arrow: "→",
	}
}

func (s Styles) mark(st Status) string {
	return s.Status[st].Render(s.Marks[st])
}

// WriteText prints the report for people. Without verbose it lists only the
// findings that need attention.
func WriteText(w io.Writer, r Report, s Styles, verbose bool) error {
	var b strings.Builder
	title := "Ta-Da doctor"
	if r.Version != "" {
		title += " " + r.Version
	}
	b.WriteString(s.Title.Render(title) + "\n\n")

	width := 0
	for _, res := range r.Results {
		width = max(width, lipgloss.Width(res.Name))
	}
	markWidth := lipgloss.Width(s.Marks[StatusOK])
	itemIndent := strings.Repeat(" ", 2+markWidth+1+2)
	hintIndent := itemIndent + strings.Repeat(" ", markWidth+1)

	hidden := false
	for _, res := range r.Results {
		pad := strings.Repeat(" ", width-lipgloss.Width(res.Name))
		fmt.Fprintf(&b, "  %s %s%s  %s\n", s.mark(res.Status), s.Name.Render(res.Name), pad, res.Summary)

		showHint := func(indent, hint string) {
			if hint != "" {
				fmt.Fprintf(&b, "%s%s\n", indent, s.Hint.Render(s.Arrow+" "+hint))
			}
		}
		needsFix := res.Status == StatusWarn || res.Status == StatusFail
		if needsFix || verbose {
			showHint(itemIndent, res.Hint)
		}
		for _, it := range res.Items {
			attention := it.Status == StatusWarn || it.Status == StatusFail
			if !attention && !verbose {
				hidden = true
				continue
			}
			// An item that only repeats the summary adds its hint, if any.
			if it.Text == res.Summary {
				if it.Hint != res.Hint {
					showHint(itemIndent, it.Hint)
				}
				continue
			}
			text := it.Text
			if !attention {
				text = s.Detail.Render(text)
			}
			fmt.Fprintf(&b, "%s%s %s\n", itemIndent, s.mark(it.Status), text)
			showHint(hintIndent, it.Hint)
		}
	}

	b.WriteString("\n" + s.Footer.Render(footer(r)) + "\n")
	if hidden {
		b.WriteString(s.Footer.Render("Run `tada doctor -v` to see every finding.") + "\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func footer(r Report) string {
	parts := []string{fmt.Sprintf("%d ok", r.OK)}
	if r.Warn > 0 {
		parts = append(parts, plural(r.Warn, "warning", "warnings"))
	}
	if r.Fail > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", r.Fail))
	}
	if r.Skip > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", r.Skip))
	}
	return strings.Join(parts, ", ") + "."
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// WriteJSON prints the report as indented JSON.
func WriteJSON(w io.Writer, r Report) error {
	if r.Results == nil {
		r.Results = []Result{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
