package opencode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	envRefRe  = regexp.MustCompile(`\{env:([^}]+)\}`)
	fileRefRe = regexp.MustCompile(`\{file:[^}]+\}`)
)

// substituter applies OpenCode's {env:NAME} and {file:PATH} substitutions
// to raw config text before it is parsed, matching
// packages/core/src/config/variable.ts.
type substituter struct {
	getenv func(string) string
	home   string
}

// substitute rewrites text. source names the document in warnings; dir is
// the directory relative {file:...} paths resolve against.
//
// {env:NAME} is replaced by the variable's raw value (not JSON-escaped), or
// "" when unset. It runs first, over the whole text.
//
// {file:PATH} is replaced by the file's trimmed contents, JSON-string
// escaped so it can sit inside a JSON string. A reference whose line starts
// with // (ignoring leading whitespace) is left untouched. "~/" expands to
// the home directory and relative paths resolve against dir. A missing or
// unreadable file substitutes "" and records a warning.
func (s substituter) substitute(text, source, dir string) (string, []string) {
	text = envRefRe.ReplaceAllStringFunc(text, func(m string) string {
		return s.getenv(m[len("{env:") : len(m)-1])
	})
	if !strings.Contains(text, "{file:") {
		return text, nil
	}

	var (
		out      strings.Builder
		warnings []string
		cursor   int
	)
	for _, loc := range fileRefRe.FindAllStringIndex(text, -1) {
		start, end := loc[0], loc[1]
		token := text[start:end]
		out.WriteString(text[cursor:start])
		cursor = end

		lineStart := strings.LastIndexByte(text[:start], '\n') + 1
		if strings.HasPrefix(strings.TrimLeft(text[lineStart:start], " \t\r\f\v"), "//") {
			out.WriteString(token)
			continue
		}

		path := s.resolveFilePath(token[len("{file:"):len(token)-1], dir)
		content, err := os.ReadFile(path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: file reference %q is missing or unreadable", source, token))
			content = nil
		}
		out.WriteString(jsonEscape(strings.TrimSpace(string(content))))
	}
	out.WriteString(text[cursor:])
	return out.String(), warnings
}

func (s substituter) resolveFilePath(path, dir string) string {
	if strings.HasPrefix(path, "~/") {
		path = filepath.Join(s.home, path[2:])
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(dir, path)
}

// jsonEscape returns s encoded as a JSON string without the surrounding
// quotes, like JSON.stringify(s).slice(1, -1).
func jsonEscape(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	b := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	return string(b[1 : len(b)-1])
}
