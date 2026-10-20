// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): features/magic-context/tagger.ts, hooks/magic-context/tag-messages.ts, tag-content-primitives.ts.

package magiccontext

import (
	"regexp"
	"strconv"
	"strings"
)

// Tag prefix handling (hooks/magic-context/tag-content-primitives.ts).

var (
	tagPrefixRe       = regexp.MustCompile(`^(?:§\d+§\s*)+`)
	malformedPrefixRe = regexp.MustCompile(`^(?:§\d+">§(?:\d+§)?\s*)+`)
	completePairRe    = regexp.MustCompile(`§\d+§`)
	malformedGlobalRe = regexp.MustCompile(`§\d+">(?:§(?:\d+§)?)?`)
	// danglingRe matches `§\d+[^\s§\w.]?`; the `(?!\.\d)` lookahead of the
	// original is applied by hand in danglingMatches.
	danglingRe = regexp.MustCompile(`§\d+[^\s§\w.]?`)
	leadingWS  = regexp.MustCompile(`^\s*`)
)

// TagPrefix renders "§N§ ".
func TagPrefix(n int) string { return "§" + strconv.Itoa(n) + "§ " }

// PrependTag replaces any existing tag prefix with §n§.
func PrependTag(n int, v string) string { return TagPrefix(n) + StripTagPrefix(v) }

// TagPlaceholder is the text left by a drop.
func TagPlaceholder(n int) string { return "[dropped §" + strconv.Itoa(n) + "§]" }

// danglingMatches returns [start,end) spans of dangling markers, skipping
// matches followed by ".<digit>" (so "§1.5" style text survives).
func danglingMatches(s string, anchored bool) [][2]int {
	var out [][2]int
	idx := danglingRe.FindAllStringIndex(s, -1)
	for _, m := range idx {
		if anchored && m[0] != 0 && (len(out) == 0 || out[len(out)-1][1] != m[0]) {
			break
		}
		// Re-derive the digit run end to apply the lookahead.
		j := m[0] + len("§")
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j+1 < len(s) && s[j] == '.' && s[j+1] >= '0' && s[j+1] <= '9' {
			if anchored {
				break
			}
			continue
		}
		end := m[1]
		if anchored {
			// swallow trailing whitespace like `\s*`
			end += len(leadingWS.FindString(s[end:]))
		}
		out = append(out, [2]int{m[0], end})
	}
	return out
}

func stripDanglingPrefix(s string) string {
	ms := danglingMatches(s, true)
	if len(ms) == 0 {
		return s
	}
	return s[ms[len(ms)-1][1]:]
}

func stripDanglingGlobal(s string) string {
	ms := danglingMatches(s, false)
	if len(ms) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range ms {
		b.WriteString(s[last:m[0]])
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// StripTagPrefix removes leading (possibly malformed or dangling) tag
// markers, up to 8 passes.
func StripTagPrefix(v string) string {
	for range 8 {
		next := malformedPrefixRe.ReplaceAllString(v, "")
		next = tagPrefixRe.ReplaceAllString(next, "")
		next = stripDanglingPrefix(next)
		if next == v {
			break
		}
		v = next
	}
	return v
}

// StripPersistedText removes every tag marker from assistant text before it
// is stored, so history never carries §N§ bytes the model echoed.
func StripPersistedText(v string) string {
	if !strings.Contains(v, "§") {
		return v
	}
	v = tagPrefixRe.ReplaceAllString(v, "")
	v = completePairRe.ReplaceAllString(v, "")
	v = malformedGlobalRe.ReplaceAllString(v, "")
	v = stripDanglingGlobal(v)
	v = strings.ReplaceAll(v, "§", "")
	return strings.TrimSpace(v)
}
