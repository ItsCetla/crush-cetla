package opencode

import (
	"regexp"
	"unicode/utf8"
)

// envNameRe matches the environment variable names ToCatwalk will
// reference.
var envNameRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// maxTemplateDepth bounds ${A:-${B:-...}} nesting.
const maxTemplateDepth = 8

// SafeTemplate reports whether s is safe to hand to Ta-Da's shell-style
// value expander: literal text plus plain environment references only.
//
// Allowed references are $NAME, ${NAME} and ${NAME:-DEFAULT}, where NAME
// matches [A-Za-z_][A-Za-z0-9_]* (but not a lone "_") and DEFAULT is
// itself a safe template. Everything that could run a command or read
// anything but a named variable is rejected: command and arithmetic
// substitution ($(...), $((...)), backticks), every other parameter
// operator (${#A}, ${!A}, ${A:=x}, ${A/x/y}, ${A-x}, ...), special
// parameters ($1, $$, $?, ...), quotes and backslashes (which change how
// the rest is parsed), control characters and invalid UTF-8.
func SafeTemplate(s string) bool {
	end, ok := scanTemplate(s, 0, false, 0)
	return ok && end == len(s)
}

// scanTemplate validates s from i. In nested mode it stops before the
// closing brace of a ${NAME:-...} default and returns its index.
func scanTemplate(s string, i int, nested bool, depth int) (int, bool) {
	if depth > maxTemplateDepth {
		return i, false
	}
	for i < len(s) {
		c := s[i]
		switch {
		case c == '}' && nested:
			return i, true
		case c == '$':
			next, ok := scanReference(s, i, depth)
			if !ok {
				return i, false
			}
			i = next
		case c == '`' || c == '\\' || c == '\'' || c == '"':
			return i, false
		case c < 0x20 || c == 0x7f:
			return i, false
		case c >= utf8.RuneSelf:
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && size <= 1 {
				return i, false
			}
			i += size
		default:
			i++
		}
	}
	return i, !nested
}

// scanReference validates the reference starting at s[i] == '$' and
// returns the index just after it.
func scanReference(s string, i, depth int) (int, bool) {
	i++
	if i >= len(s) {
		return i, false
	}
	if s[i] != '{' {
		end := scanName(s, i)
		if !validName(s[i:end]) {
			return i, false
		}
		return end, true
	}
	i++
	end := scanName(s, i)
	if !validName(s[i:end]) {
		return i, false
	}
	i = end
	if i < len(s) && s[i] == '}' {
		return i + 1, true
	}
	if i+1 < len(s) && s[i] == ':' && s[i+1] == '-' {
		closing, ok := scanTemplate(s, i+2, true, depth+1)
		if !ok || closing >= len(s) || s[closing] != '}' {
			return i, false
		}
		return closing + 1, true
	}
	return i, false
}

func scanName(s string, i int) int {
	for i < len(s) {
		c := s[i]
		if c == '_' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') {
			i++
			continue
		}
		break
	}
	return i
}

func validName(name string) bool {
	if name == "" || name == "_" {
		return false
	}
	c := name[0]
	return c == '_' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

// envTemplate references the first set variable among names, e.g. "$A" or
// "${A:-${B}}". It returns "" for no names.
func envTemplate(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return "$" + names[0]
	}
	out := "${" + names[len(names)-1] + "}"
	for i := len(names) - 2; i >= 0; i-- {
		out = "${" + names[i] + ":-" + out + "}"
	}
	return out
}

// templateRefs returns the variable names referenced by a safe template.
func templateRefs(s string) map[string]bool {
	refs := map[string]bool{}
	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			continue
		}
		j := i + 1
		if j < len(s) && s[j] == '{' {
			j++
		}
		end := scanName(s, j)
		if end > j {
			refs[s[j:end]] = true
		}
	}
	return refs
}
