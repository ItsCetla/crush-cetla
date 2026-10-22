package export

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// maxResultLines and maxResultBytes cap how much of a tool result
	// the Markdown and HTML exports show. JSON keeps it all.
	maxResultLines = 40
	maxResultBytes = 4000
	// maxDiffLines caps how much of a diff they show.
	maxDiffLines = 400
)

// truncate cuts s to at most maxLines lines and maxBytes bytes, and says
// how much it left out.
func truncate(s string, maxLines, maxBytes int) (string, string) {
	s = strings.TrimRight(s, "\n")
	lines := strings.Split(s, "\n")
	total := len(lines)
	cut := false
	if total > maxLines {
		lines = lines[:maxLines]
		cut = true
	}
	out := strings.Join(lines, "\n")
	if len(out) > maxBytes {
		// Cut on a character boundary.
		n := maxBytes
		for n > 0 && !utf8.RuneStart(out[n]) {
			n--
		}
		out = out[:n]
		cut = true
	}
	if !cut {
		return out, ""
	}
	shown := strings.Count(out, "\n") + 1
	if hidden := total - shown; hidden > 0 {
		return out, fmt.Sprintf("… %s more %s not shown", commas(int64(hidden)), plural(hidden, "line", "lines"))
	}
	return out, "… the rest of this output is not shown"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// commas formats n with thousands separators: 12,345.
func commas(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func formatCost(c float64) string {
	if c > 0 && c < 0.01 {
		return fmt.Sprintf("$%.4f", c)
	}
	return fmt.Sprintf("$%.2f", c)
}

func formatDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("Mon 2 Jan 2006, 15:04 MST")
}

func formatClock(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("15:04")
}

func (d Document) tokensLine() string {
	total := d.PromptTokens + d.CompletionTokens
	line := fmt.Sprintf("%s (%s in, %s out)", commas(total), commas(d.PromptTokens), commas(d.CompletionTokens))
	if d.EstimatedUsage {
		line += ", estimated"
	}
	return line
}

func (d Document) modelsLine() string {
	if len(d.Models) == 0 {
		return "unknown"
	}
	return strings.Join(d.Models, ", ")
}

func (d Document) filesLine() string {
	if len(d.Files) == 0 {
		return "none"
	}
	var add, rem int
	for _, f := range d.Files {
		add += f.Additions
		rem += f.Removals
	}
	return fmt.Sprintf("%d (+%s −%s)", len(d.Files), commas(int64(add)), commas(int64(rem)))
}

func (h Host) purposes() string {
	if len(h.Purposes) == 0 {
		return "other"
	}
	return strings.Join(h.Purposes, ", ")
}
