package export

import (
	"bufio"
	"fmt"
	"html"
	"io"
	"strings"

	"github.com/charmbracelet/crush/internal/netlog"
)

func renderMarkdown(w io.Writer, d Document) error {
	bw := bufio.NewWriter(w)
	m := mdWriter{w: bw}

	m.printf("# %s\n\n", mdInline(d.Title))
	if date := formatDate(d.CreatedAt); date != "" {
		m.printf("- **Date:** %s\n", date)
	}
	m.printf("- **%s:** %s\n", plural(len(d.Models), "Model", "Models"), mdInline(d.modelsLine()))
	if d.Project != "" {
		m.printf("- **Project:** %s\n", mdInline(d.Project))
	}
	m.printf("- **Tokens:** %s\n", d.tokensLine())
	m.printf("- **Cost:** %s\n", formatCost(d.Cost))
	m.printf("- **Files changed:** %s\n", d.filesLine())
	if len(d.Files) > 0 {
		m.printf("\n")
		for _, f := range d.Files {
			m.printf("  - `%s` (+%d −%d)\n", f.Path, f.Additions, f.Removals)
		}
	}
	m.printf("\n")

	// Every block ends with a blank line, so a rule can follow directly.
	for _, t := range d.Turns {
		m.printf("---\n\n")
		m.turn(t)
	}

	m.printf("---\n\n")
	if len(d.Hosts) > 0 {
		m.printf("## Network\n\n")
		m.printf("| Host | Used for | Requests | Sent | Received |\n")
		m.printf("| --- | --- | ---: | ---: | ---: |\n")
		for _, h := range d.Hosts {
			name := h.Host
			if h.Local {
				name += " (local)"
			}
			m.printf("| %s | %s | %s | %s | %s |\n", mdCell(name), mdCell(h.purposes()),
				commas(h.Requests), netlog.Bytes(h.BytesOut), netlog.Bytes(h.BytesIn))
		}
		m.printf("\n")
	}
	m.printf("_%s_\n", footerLine(d))
	if m.err != nil {
		return m.err
	}
	return bw.Flush()
}

func footerLine(d Document) string {
	line := "Exported from Ta-Da"
	if date := formatDate(d.ExportedAt); date != "" {
		line += " on " + date
	}
	line += "."
	if d.Redacted {
		line += " Secrets and the home folder were redacted."
	}
	return line
}

func turnLabel(t Turn) string {
	switch {
	case t.Summary:
		return "Summary"
	case t.Role == RoleUser:
		return "User"
	default:
		return "Assistant"
	}
}

type mdWriter struct {
	w   io.Writer
	err error
}

func (m *mdWriter) printf(format string, args ...any) {
	if m.err != nil {
		return
	}
	_, m.err = fmt.Fprintf(m.w, format, args...)
}

func (m *mdWriter) turn(t Turn) {
	heading := turnLabel(t)
	if clock := formatClock(t.Time); clock != "" {
		heading += " · " + clock
	}
	if t.Model != "" {
		heading += " · " + mdInline(t.Model)
	}
	m.printf("## %s\n\n", heading)

	for _, b := range t.Blocks {
		switch b.Kind {
		case BlockText:
			m.printf("%s\n\n", strings.TrimSpace(b.Text))
		case BlockReasoning:
			m.printf("<details>\n<summary>Thinking</summary>\n\n%s\n\n</details>\n\n", strings.TrimSpace(b.Text))
		case BlockTool:
			m.tool(b.Tool)
		case BlockShell:
			m.shell(b.Shell)
		case BlockAttachment:
			m.printf("_Attached: %s_\n\n", mdInline(b.Text))
		case BlockNotice:
			m.printf("> **%s**\n\n", mdInline(b.Text))
		}
	}
}

func (m *mdWriter) tool(c *ToolCall) {
	summary := "<code>" + html.EscapeString(c.Name) + "</code>"
	if c.Summary != "" {
		summary += " " + html.EscapeString(c.Summary)
	}
	if c.Diff != "" {
		summary += fmt.Sprintf(" (+%d −%d)", c.Additions, c.Removals)
	}
	if c.IsError {
		summary += " — failed"
	}
	m.printf("<details>\n<summary>%s</summary>\n\n", summary)

	if c.Diff != "" {
		body, more := truncate(c.Diff, maxDiffLines, maxDiffLines*200)
		m.printf("**Changes to `%s`**\n\n", c.DiffPath)
		m.fenced("diff", body)
		m.more(more)
	} else if c.Input != "" {
		m.printf("**Input**\n\n")
		m.fenced("json", c.Input)
	}

	switch {
	case c.NoResult:
		m.printf("_No result was recorded._\n\n")
	case c.Result != "":
		body, more := truncate(c.Result, maxResultLines, maxResultBytes)
		label := "Result"
		if c.IsError {
			label = "Error"
		}
		m.printf("**%s**\n\n", label)
		m.fenced("", body)
		m.more(more)
	}
	m.printf("</details>\n\n")
}

func (m *mdWriter) shell(s *ShellCall) {
	text := "$ " + s.Command
	if out := strings.TrimRight(s.Output, "\n"); out != "" {
		body, more := truncate(out, maxResultLines, maxResultBytes)
		text += "\n" + body
		if more != "" {
			text += "\n" + more
		}
	}
	m.fenced("console", text)
	if s.ExitCode != 0 {
		m.printf("_Exited with status %d._\n\n", s.ExitCode)
	}
}

func (m *mdWriter) more(note string) {
	if note != "" {
		m.printf("_%s_\n\n", note)
	}
}

// fenced writes body as a code block, with a fence longer than any run
// of backticks inside it.
func (m *mdWriter) fenced(lang, body string) {
	fence := strings.Repeat("`", max(3, longestRun(body, '`')+1))
	m.printf("%s%s\n%s\n%s\n\n", fence, lang, strings.TrimRight(body, "\n"), fence)
}

func longestRun(s string, c rune) int {
	best, cur := 0, 0
	for _, r := range s {
		if r == c {
			cur++
			best = max(best, cur)
		} else {
			cur = 0
		}
	}
	return best
}

// mdInline makes s safe to use on one line of Markdown, such as a
// heading.
func mdInline(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "`", "\\`", "<", "&lt;", ">", "&gt;", "[", `\[`, "]", `\]`)
	return r.Replace(s)
}

func mdCell(s string) string {
	return strings.ReplaceAll(mdInline(s), "|", `\|`)
}
