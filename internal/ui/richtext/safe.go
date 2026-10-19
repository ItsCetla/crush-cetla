package richtext

import (
	"math"
	"strings"
	"unicode"
)

// maxAbsValue bounds chart values so scale arithmetic stays finite.
const maxAbsValue = 1e300

// cleanText drops control characters (ESC, C0, C1, DEL) so model text
// can't smuggle terminal escape sequences into the rendered block. Tabs
// become spaces; newlines are kept only when keepNewlines is set.
func cleanText(s string, keepNewlines bool) string {
	if strings.IndexFunc(s, unicode.IsControl) < 0 {
		return s
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' && keepNewlines:
			return r
		case r == '\t':
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
}

// toInt rounds f to an int clamped to [lo, hi]; NaN maps to lo.
func toInt(f float64, lo, hi int) int {
	switch {
	case math.IsNaN(f) || f <= float64(lo):
		return lo
	case f >= float64(hi):
		return hi
	}
	return int(math.Round(f))
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }
