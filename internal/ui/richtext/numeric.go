package richtext

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// numberRe matches table cells that read as numbers: optional parens, sign
// and currency, digits with optional thousands separators and decimals, and
// an optional short suffix.
var numberRe = regexp.MustCompile(
	`^(\()?[+\-−]?[$€£¥]?[+\-−]?(\d{1,3}(?:,\d{3})+|\d+)?(\.\d+)?` +
		`(%|x|×|[kKMBbT]|bn|ms|s|h|pp|bps)?(\))?$`,
)

// isPlaceholder reports whether a cell is an empty-value marker that should
// not stop a column from being numeric.
func isPlaceholder(s string) bool {
	switch strings.TrimSpace(s) {
	case "", "-", "–", "—", "n/a", "N/A", "NA":
		return true
	}
	return false
}

// numParts returns the display widths of s before and from its decimal point
// (or from where it would be). ok is false when s is not a number.
func numParts(s string) (left, right int, ok bool) {
	m := numberRe.FindStringSubmatchIndex(s)
	if m == nil {
		return 0, 0, false
	}
	hasInt, hasFrac := m[4] >= 0, m[6] >= 0
	if !hasInt && !hasFrac {
		return 0, 0, false
	}
	if (m[2] >= 0) != (m[10] >= 0) {
		return 0, 0, false
	}
	dot := m[5] // end of the integer part
	if hasFrac {
		dot = m[6]
	}
	return ansi.StringWidth(s[:dot]), ansi.StringWidth(s[dot:]), true
}

// isNumericColumn reports whether every non-placeholder cell is a number and
// at least one is.
func isNumericColumn(cells []string) bool {
	seen := false
	for _, c := range cells {
		if isPlaceholder(c) {
			continue
		}
		if _, _, ok := numParts(strings.TrimSpace(c)); !ok {
			return false
		}
		seen = true
	}
	return seen
}
