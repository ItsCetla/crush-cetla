// Package export turns a session into a file people can share: Markdown,
// a single self-contained HTML page, or JSON. It works on plain data (a
// session, its messages and its file history) so it can be tested
// without a database.
package export

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/netlog"
	"github.com/charmbracelet/crush/internal/session"
)

// Format is an export file format.
type Format string

const (
	// FormatMarkdown is GitHub-flavored Markdown.
	FormatMarkdown Format = "md"
	// FormatHTML is one self-contained HTML page.
	FormatHTML Format = "html"
	// FormatJSON is the export's document model as JSON.
	FormatJSON Format = "json"
)

// ParseFormat reads a format name as people type it.
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), ".")) {
	case "", "md", "markdown":
		return FormatMarkdown, nil
	case "html", "htm":
		return FormatHTML, nil
	case "json":
		return FormatJSON, nil
	default:
		return "", fmt.Errorf("unknown export format %q (want md, html or json)", s)
	}
}

// Ext is the file name extension for the format, with its dot.
func (f Format) Ext() string {
	return "." + string(f)
}

// Data is everything an export is made from.
type Data struct {
	Session  session.Session
	Messages []message.Message
	// Files is the session's file history, used for the list of changed
	// files. It may be empty.
	Files []history.File
	// WorkingDir is the folder the session worked in. Paths under it are
	// shown relative to it.
	WorkingDir string
	// Hosts are the network hosts the session talked to. When empty the
	// export has no network footer.
	Hosts []netlog.Host
	// ExportedAt is when the export was made. Zero means now.
	ExportedAt time.Time
}

// Options tune an export.
type Options struct {
	// Redact scrubs API keys, tokens and other secrets, and shows the
	// home directory as "~".
	Redact bool
	// HomeDir is the home directory to hide when Redact is set.
	HomeDir string
	// Location is the time zone timestamps are shown in. Nil means the
	// local time zone.
	Location *time.Location
}

// Render writes d to w in format f.
func Render(w io.Writer, f Format, d Data, o Options) error {
	doc := Build(d, o)
	switch f {
	case FormatMarkdown:
		return renderMarkdown(w, doc)
	case FormatHTML:
		return renderHTML(w, doc)
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		return enc.Encode(doc)
	default:
		return fmt.Errorf("unknown export format %q", f)
	}
}

// FileName is a file name for the export of s in format f, such as
// "fix-the-flaky-test-1a2b3c4.html".
func FileName(s session.Session, f Format) string {
	slug := slugify(s.Title)
	if slug == "" {
		slug = "session"
	}
	hash := session.HashID(s.ID)
	if len(hash) > 7 {
		hash = hash[:7]
	}
	return slug + "-" + hash + f.Ext()
}

func slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 48 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}
