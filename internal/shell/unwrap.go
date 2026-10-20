package shell

import (
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// maxUnwrapDepth bounds how deeply nested wrappers and inline scripts are
// followed, so a pathological command cannot make the analysis loop.
const maxUnwrapDepth = 8

// CommandName returns the name a block list should match for argv[0]: its
// base name, without the .exe suffix on Windows.
func CommandName(arg string) string {
	name := filepath.Base(arg)
	if runtime.GOOS == "windows" {
		name = strings.TrimSuffix(strings.ToLower(name), ".exe")
	}
	return name
}

// CommandVariants returns args followed by the argv of every command that
// args runs on its behalf: wrapper commands (env, nice, nohup, timeout,
// xargs, ...) are unwrapped, `sh -c` / `bash -c` scripts and `eval`
// arguments are parsed, and `find -exec` commands are extracted. Block
// lists check every variant so that a banned command cannot hide behind a
// wrapper.
func CommandVariants(args []string) [][]string {
	var out [][]string
	collectVariants(args, 0, &out)
	return out
}

func collectVariants(args []string, depth int, out *[][]string) {
	if len(args) == 0 || depth > maxUnwrapDepth {
		return
	}
	*out = append(*out, args)

	name := CommandName(args[0])
	switch {
	case shellNames[name]:
		if script, ok := shellScriptArg(args[1:]); ok {
			for _, inner := range ScriptCommands(script) {
				collectVariants(inner, depth+1, out)
			}
		}
	case name == "eval":
		for _, inner := range ScriptCommands(strings.Join(args[1:], " ")) {
			collectVariants(inner, depth+1, out)
		}
	case name == "find":
		for _, e := range FindExecs(args[1:]) {
			collectVariants(e.Argv, depth+1, out)
		}
	default:
		if spec, ok := wrapperCommands[name]; ok {
			collectVariants(spec.unwrap(args[1:]), depth+1, out)
		}
	}
}

// InlineScripts returns the shell scripts that args runs as code: a
// `sh -c` script or an `eval` line, also when run through a wrapper (sudo,
// env, xargs, ...) or find -exec. Only the first level is returned; a
// caller that inspects a script finds the scripts nested in it. A script
// whose text is only known at run time is returned as "".
func InlineScripts(args []string) []string {
	var out []string
	collectScripts(args, 0, &out)
	return out
}

func collectScripts(args []string, depth int, out *[]string) {
	if len(args) == 0 {
		return
	}
	if depth > maxUnwrapDepth {
		*out = append(*out, "")
		return
	}
	name := CommandName(args[0])
	switch {
	case shellNames[name]:
		if script, ok := shellScriptArg(args[1:]); ok {
			*out = append(*out, script)
		}
	case name == "eval":
		if slices.Contains(args[1:], "") {
			*out = append(*out, "")
		} else {
			*out = append(*out, strings.Join(args[1:], " "))
		}
	case name == "find":
		for _, e := range FindExecs(args[1:]) {
			collectScripts(e.Argv, depth+1, out)
		}
	default:
		if spec, ok := wrapperCommands[name]; ok {
			collectScripts(spec.unwrap(args[1:]), depth+1, out)
		}
	}
}

// UnwrapCommand returns the command that the wrapper command args runs
// (sudo, env, xargs, ...), or nil when args isn't a wrapper or runs none.
func UnwrapCommand(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	spec, ok := wrapperCommands[CommandName(args[0])]
	if !ok {
		return nil
	}
	return spec.unwrap(args[1:])
}

// ScriptCommands parses script as shell code and returns the argv of every
// simple command in it, including those in pipelines, lists, subshells and
// command or process substitutions. Brace expansions are expanded the way
// the shell does, so {rm,-rf,x} yields rm -rf x. Words that are not plain
// literals (parameter expansions, substitutions) are returned as empty
// strings. A script that does not parse yields nil.
func ScriptCommands(script string) [][]string {
	file, err := syntax.NewParser().Parse(strings.NewReader(script), "")
	if err != nil {
		return nil
	}
	var cmds [][]string
	syntax.Walk(file, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		var argv []string
		for _, w := range call.Args {
			argv = append(argv, ExpandedWords(w)...)
		}
		cmds = append(cmds, argv)
		return true
	})
	return cmds
}

// maxBraceWords caps how many words a brace expansion may produce before
// the analysis gives up on it.
const maxBraceWords = 1024

// ExpandedWords brace-expands w and returns the literal value of each
// resulting word, with "" for a word that isn't a plain literal. An
// expansion too large to follow yields a single "".
func ExpandedWords(w *syntax.Word) []string {
	word := &syntax.Word{Parts: slices.Clone(w.Parts)}
	if !syntax.SplitBraces(word) {
		lit, _ := LiteralWord(w)
		return []string{lit}
	}
	var out []string
	for exp, err := range expand.BracesSeq(nil, word) {
		if err != nil || len(out) >= maxBraceWords {
			return []string{""}
		}
		lit, _ := LiteralWord(exp)
		out = append(out, lit)
	}
	return out
}

// LiteralWord returns the value of a word made only of unquoted literals,
// single-quoted strings and double-quoted literals, with quoting and
// backslash escapes removed. ok is false when the word contains any
// expansion or substitution, whose value is only known at run time.
func LiteralWord(w *syntax.Word) (value string, ok bool) {
	var sb strings.Builder
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			sb.WriteString(unescapeUnquoted(p.Value))
		case *syntax.SglQuoted:
			if p.Dollar {
				return "", false
			}
			sb.WriteString(p.Value)
		case *syntax.DblQuoted:
			if p.Dollar {
				return "", false
			}
			for _, inner := range p.Parts {
				lit, isLit := inner.(*syntax.Lit)
				if !isLit {
					return "", false
				}
				sb.WriteString(unescapeDoubleQuoted(lit.Value))
			}
		default:
			return "", false
		}
	}
	return sb.String(), true
}

func unescapeUnquoted(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			if s[i] == '\n' {
				continue
			}
		}
		sb.WriteByte(s[i])
	}
	return sb.String()
}

func unescapeDoubleQuoted(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("$`\"\\\n", s[i+1]) >= 0 {
			i++
			if s[i] == '\n' {
				continue
			}
		}
		sb.WriteByte(s[i])
	}
	return sb.String()
}

// shellNames lists shells whose -c argument is a script to inspect.
var shellNames = map[string]bool{
	"ash": true, "bash": true, "dash": true, "ksh": true, "mksh": true,
	"posh": true, "sh": true, "yash": true, "zsh": true,
}

// shellScriptArg returns the script passed to a shell with -c.
func shellScriptArg(args []string) (string, bool) {
	hasC := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--" || a == "-":
			if hasC && i+1 < len(args) {
				return args[i+1], true
			}
			return "", false
		case strings.HasPrefix(a, "--"):
			if a == "--rcfile" || a == "--init-file" {
				i++
			}
		case len(a) > 1 && (a[0] == '-' || a[0] == '+'):
			for _, c := range a[1:] {
				if c == 'c' {
					hasC = true
				}
				if c == 'o' || c == 'O' {
					i++
					break
				}
			}
		default:
			if hasC {
				return a, true
			}
			return "", false
		}
	}
	return "", false
}

// FindExec is a command that find runs for each match.
type FindExec struct {
	// Argv is the command, with {} standing for the matched path.
	Argv []string
	// Dir is set for -execdir and -okdir, which run the command in the
	// matched file's directory.
	Dir bool
}

// FindExecs returns the commands find runs via -exec, -execdir, -ok and
// -okdir.
func FindExecs(args []string) []FindExec {
	var cmds []FindExec
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-exec", "-execdir", "-ok", "-okdir":
		default:
			continue
		}
		dir := strings.HasSuffix(args[i], "dir")
		start := i + 1
		end := start
		for end < len(args) && args[end] != ";" && args[end] != "+" {
			end++
		}
		if end > start {
			cmds = append(cmds, FindExec{Argv: args[start:end], Dir: dir})
		}
		i = end
	}
	return cmds
}

// wrapperSpec describes how a wrapper command's own options are laid out,
// so the command it runs can be located.
type wrapperSpec struct {
	// shortValue lists short options that take a value, either attached
	// (-n5) or as the next argument (-n 5).
	shortValue string
	// longValue lists long options that take the next argument as their
	// value when not written as --opt=value.
	longValue []string
	// operands is the number of positional operands before the command
	// (timeout's duration, chroot's new root).
	operands int
	// assignments makes NAME=VALUE words before the command be skipped.
	assignments bool
	// splitString names the short option whose value is itself a command
	// line (env -S).
	splitString byte
	// lookupOnly lists short options that make the wrapper only look the
	// command up instead of running it (command -v).
	lookupOnly string
}

// wrapperCommands maps commands that run another command to how their
// options are laid out.
var wrapperCommands = map[string]wrapperSpec{
	"builtin":    {},
	"caffeinate": {shortValue: "tw"},
	"chroot":     {longValue: []string{"userspec", "groups"}, operands: 1},
	"command":    {lookupOnly: "vV"},
	"doas":       {shortValue: "uC"},
	"env":        {shortValue: "uCSP", longValue: []string{"unset", "chdir", "split-string"}, assignments: true, splitString: 'S'},
	"exec":       {shortValue: "a"},
	"ionice":     {shortValue: "cnp", longValue: []string{"class", "classdata", "pid", "pgid", "uid"}},
	"nice":       {shortValue: "n", longValue: []string{"adjustment"}},
	"nohup":      {},
	"setsid":     {},
	"stdbuf":     {shortValue: "ioe", longValue: []string{"input", "output", "error"}},
	"sudo":       {shortValue: "CDghpRrTUu", longValue: []string{"close-from", "chdir", "group", "host", "prompt", "chroot", "role", "type", "command-timeout", "other-user", "user"}},
	"taskset":    {operands: 1},
	"time":       {shortValue: "fo", longValue: []string{"format", "output"}},
	"timeout":    {shortValue: "sk", longValue: []string{"signal", "kill-after"}, operands: 1},
	"unbuffer":   {},
	"watch":      {shortValue: "nq", longValue: []string{"interval"}},
	"xargs":      {shortValue: "adEILnPs", longValue: []string{"arg-file", "delimiter", "max-args", "max-procs", "max-chars", "process-slot-var"}},
}

// unwrap returns the command a wrapper runs, or nil when there is none.
func (w wrapperSpec) unwrap(args []string) []string {
	var split []string
	i := 0
options:
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--":
			i++
			break options
		case w.assignments && isAssignment(a):
			i++
		case strings.HasPrefix(a, "--"):
			name, value, hasValue := strings.Cut(a[2:], "=")
			i++
			if hasValue {
				if w.splitString != 0 && name == "split-string" {
					split = strings.Fields(value)
				}
				continue
			}
			if slices.Contains(w.longValue, name) {
				if w.splitString != 0 && name == "split-string" && i < len(args) {
					split = strings.Fields(args[i])
				}
				i++
			}
		case len(a) > 1 && a[0] == '-':
			i++
			for j := 1; j < len(a); j++ {
				c := a[j]
				if strings.IndexByte(w.lookupOnly, c) >= 0 {
					return nil
				}
				if strings.IndexByte(w.shortValue, c) < 0 {
					continue
				}
				value := a[j+1:]
				if value == "" && i < len(args) {
					value = args[i]
					i++
				}
				if c == w.splitString {
					split = strings.Fields(value)
				}
				break
			}
		default:
			break options
		}
	}
	i += w.operands
	if i > len(args) {
		i = len(args)
	}
	return append(split, args[i:]...)
}

// isAssignment reports whether a is a NAME=VALUE environment assignment.
func isAssignment(a string) bool {
	name, _, ok := strings.Cut(a, "=")
	if !ok || name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}
