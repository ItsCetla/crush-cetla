// Package fakecli writes a stand-in `claude` CLI for tests. It never runs
// a model: it prints canned stream-json and logs what it was sent.
package fakecli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// StderrPrefix marks a step line that goes to stderr instead of stdout.
const StderrPrefix = "!stderr "

// Write creates a fake CLI in a temp dir and returns its path and the dir.
//
// The fake reads the initialize request and the user message, then prints
// each step in turn. Between steps it reads one more line from stdin (the
// answer to a control request). Every argument and stdin line is logged
// for Args and Input.
func Write(t *testing.T, steps ...[]string) (bin, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake claude CLI needs /bin/sh")
	}
	dir = t.TempDir()
	q := func(p string) string { return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'" }
	stdinLog := q(filepath.Join(dir, "stdin.log"))

	var sh strings.Builder
	sh.WriteString("#!/bin/sh\n")
	fmt.Fprintf(&sh, "printf '%%s\\n' \"$*\" > %s\n", q(filepath.Join(dir, "args.log")))
	readLine := fmt.Sprintf("IFS= read -r line && printf '%%s\\n' \"$line\" >> %s\n", stdinLog)
	sh.WriteString(readLine)
	sh.WriteString(readLine)
	for i, step := range steps {
		if i > 0 {
			sh.WriteString(readLine)
		}
		var out, errOut strings.Builder
		for _, l := range step {
			if rest, ok := strings.CutPrefix(l, StderrPrefix); ok {
				errOut.WriteString(rest + "\n")
			} else {
				out.WriteString(l + "\n")
			}
		}
		outFile := filepath.Join(dir, fmt.Sprintf("step%d.out", i))
		errFile := filepath.Join(dir, fmt.Sprintf("step%d.err", i))
		must(t, os.WriteFile(outFile, []byte(out.String()), 0o644))
		must(t, os.WriteFile(errFile, []byte(errOut.String()), 0o644))
		fmt.Fprintf(&sh, "cat %s >&2\ncat %s\n", q(errFile), q(outFile))
	}
	bin = filepath.Join(dir, "claude")
	must(t, os.WriteFile(bin, []byte(sh.String()), 0o755))
	return bin, dir
}

// Input returns the JSON lines the fake read from stdin.
func Input(t *testing.T, dir string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "stdin.log"))
	must(t, err)
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		must(t, json.Unmarshal([]byte(l), &m))
		out = append(out, m)
	}
	return out
}

// Args returns the command line the fake was started with.
func Args(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "args.log"))
	must(t, err)
	return strings.TrimSpace(string(b))
}

// UserText returns the text of the user message the fake was sent.
func UserText(t *testing.T, dir string) string {
	t.Helper()
	for _, m := range Input(t, dir) {
		if m["type"] != "user" {
			continue
		}
		msg, _ := m["message"].(map[string]any)
		switch c := msg["content"].(type) {
		case string:
			return c
		case []any:
			var b strings.Builder
			for _, part := range c {
				if p, ok := part.(map[string]any); ok && p["type"] == "text" {
					s, _ := p["text"].(string)
					b.WriteString(s)
				}
			}
			return b.String()
		}
	}
	return ""
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
