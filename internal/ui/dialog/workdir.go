package dialog

import "strings"

// CDQuery reports whether value is a /cd command ("/cd", "/directory" or
// "/dir") and returns its argument. Multi-line text is never a command.
func CDQuery(value string) (arg string, ok bool) {
	if strings.Contains(value, "\n") {
		return "", false
	}
	name, rest, _ := strings.Cut(strings.TrimSpace(value), " ")
	switch strings.ToLower(name) {
	case "/cd", "/directory", "/dir":
		return strings.TrimSpace(rest), true
	}
	return "", false
}
