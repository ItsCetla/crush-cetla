package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/crush/internal/shell"
	"mvdan.cc/sh/v3/interp"
)

// Hook is one configured hook.
type Hook struct {
	Event   string
	Name    string
	Command string
}

// HooksCheck reports whether each hook's command exists. Only the first
// command of each hook is checked; the shell runs the rest.
func HooksCheck(hooks []Hook, cwd, home string) Check {
	return Check{ID: "hooks", Name: "Hooks", Run: func(_ context.Context, env *Env) Result {
		if len(hooks) == 0 {
			return Result{Status: StatusSkip, Summary: "No hooks configured"}
		}
		var res Result
		for _, h := range hooks {
			res.Items = append(res.Items, hookItem(env, h, cwd, home))
		}
		res.Summary = plural(len(hooks), "hook", "hooks")
		if Worst(res.Items) == StatusWarn {
			res.Summary += ", some commands are missing"
		}
		return res
	}}
}

func hookItem(env *Env, h Hook, cwd, home string) Item {
	label := h.Event
	if h.Name != "" {
		label += " " + h.Name
	}
	if strings.TrimSpace(h.Command) == "" {
		return Item{Status: StatusWarn, Text: label + ": no command set", Hint: "Give the hook a command, or remove it."}
	}
	cmds := shell.ScriptCommands(h.Command)
	if cmds == nil {
		return Item{
			Status: StatusFail,
			Text:   label + ": the command isn't valid shell",
			Hint:   "Fix the quoting in the hook's command; Ta-Da can't run it.",
		}
	}
	if len(cmds) == 0 {
		return Item{Status: StatusOK, Text: label + ": shell code"}
	}
	argv := cmds[0]
	for {
		inner := shell.UnwrapCommand(argv)
		if len(inner) == 0 {
			break
		}
		argv = inner
	}
	name := argv[0]
	switch {
	case name == "":
		return Item{Status: StatusSkip, Text: label + ": the command is computed at run time, not checked"}
	case interp.IsBuiltin(name):
		return Item{Status: StatusOK, Text: label + ": " + name + " (shell builtin)"}
	}
	if path, ok := findCommand(env, name, cwd, home); ok {
		if fi, err := env.Stat(path); err == nil && fi.Mode().Perm()&0o111 == 0 {
			return Item{
				Status: StatusWarn,
				Text:   label + ": " + path + " isn't executable",
				Hint:   "Run `chmod +x " + path + "`.",
			}
		}
		return Item{Status: StatusOK, Text: label + ": " + path}
	}
	return Item{
		Status: StatusWarn,
		Text:   label + ": `" + name + "` isn't on your PATH",
		Hint:   "Install it, or fix the hook's command; the hook fails until then.",
	}
}

// ToolsCheck reports the programs Ta-Da leans on.
func ToolsCheck() Check {
	return Check{ID: "tools", Name: "Tools", Run: func(_ context.Context, env *Env) Result {
		var res Result
		var found []string
		if path, err := env.LookPath("git"); err == nil {
			found = append(found, "git")
			res.Items = append(res.Items, Item{Status: StatusOK, Text: "git: " + path})
		} else {
			res.Items = append(res.Items, Item{
				Status: StatusWarn,
				Text:   "git isn't on your PATH",
				Hint:   "Install git; Ta-Da uses it for diffs, branches and worktrees.",
			})
		}
		if path, err := env.LookPath("bash"); err == nil {
			found = append(found, "bash")
			res.Items = append(res.Items, Item{Status: StatusOK, Text: "bash: " + path})
		} else {
			res.Items = append(res.Items, Item{Status: StatusOK, Text: "bash isn't installed; Ta-Da's built-in shell runs commands instead"})
		}
		if sh := env.Getenv("SHELL"); sh != "" {
			res.Items = append(res.Items, Item{Status: StatusOK, Text: "$SHELL: " + sh})
		}
		for _, name := range []string{"rg", "fd"} {
			if path, err := env.LookPath(name); err == nil {
				found = append(found, name)
				res.Items = append(res.Items, Item{Status: StatusOK, Text: name + ": " + path})
			}
		}
		res.Summary = strings.Join(found, ", ")
		if len(found) == 0 {
			res.Summary = "git not found"
		}
		return res
	}}
}

// ProjectCheck reports whether cwd is inside a git repository. gitRoot is
// the repository's root, or "" outside one.
func ProjectCheck(cwd, gitRoot string) Check {
	return Check{ID: "project", Name: "Project", Run: func(context.Context, *Env) Result {
		if gitRoot == "" {
			return Result{
				Status:  StatusWarn,
				Summary: cwd + " isn't in a git repository",
				Hint:    "Run `git init` (or start Ta-Da inside a repository) so you can review and undo its changes.",
			}
		}
		res := Result{Status: StatusOK, Summary: "git repository at " + gitRoot}
		if gitRoot != cwd {
			res.Items = append(res.Items, Item{Status: StatusOK, Text: "Working in " + cwd})
		}
		return res
	}}
}

// TerminalInfo describes the terminal.
type TerminalInfo struct {
	// TTY reports that stdout is a terminal.
	TTY           bool
	Width, Height int
	Term          string
	ColorTerm     string
	TermProgram   string
	// Profile is the detected color profile, such as "TrueColor".
	Profile string
}

// TerminalCheck reports the terminal's size and color support.
func TerminalCheck(info TerminalInfo) Check {
	return Check{ID: "terminal", Name: "Terminal", Run: func(context.Context, *Env) Result {
		var res Result
		if !info.TTY {
			res.Status = StatusSkip
			res.Summary = "Not a terminal (output is redirected)"
			return res
		}
		parts := []string{fmt.Sprintf("%dx%d", info.Width, info.Height)}
		if info.TermProgram != "" {
			parts = append(parts, info.TermProgram)
		}
		if info.Term != "" {
			parts = append(parts, "TERM="+info.Term)
		}
		if info.Profile != "" {
			parts = append(parts, info.Profile)
		}
		res.Summary = strings.Join(parts, ", ")

		if info.Width < 80 || info.Height < 24 {
			res.Items = append(res.Items, Item{
				Status: StatusWarn,
				Text:   fmt.Sprintf("The terminal is %dx%d; Ta-Da is cramped below 80x24", info.Width, info.Height),
				Hint:   "Make the window bigger.",
			})
		}
		switch info.Term {
		case "", "dumb":
			res.Items = append(res.Items, Item{
				Status: StatusWarn,
				Text:   "TERM is " + cmpOr(info.Term, "unset") + ", so the TUI may not draw correctly",
				Hint:   "Set TERM to your terminal's value, such as xterm-256color.",
			})
		}
		truecolor := info.Profile == "TrueColor" || info.ColorTerm == "truecolor" || info.ColorTerm == "24bit"
		switch {
		case truecolor:
			res.Items = append(res.Items, Item{Status: StatusOK, Text: "Truecolor supported"})
		case info.TermProgram == "Apple_Terminal":
			res.Items = append(res.Items, Item{Status: StatusOK, Text: "Terminal.app shows 256 colors; Ta-Da adapts its theme"})
		default:
			res.Items = append(res.Items, Item{
				Status: StatusWarn,
				Text:   "No truecolor support detected, so colors are approximated",
				Hint:   "If your terminal supports it, set COLORTERM=truecolor.",
			})
		}
		return res
	}}
}

func cmpOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// DataDirInfo describes the data directory.
type DataDirInfo struct {
	Path   string
	Exists bool
	// WriteErr is why a test file couldn't be written, or "".
	WriteErr string
	// FreeBytes is the free disk space, or -1 when unknown.
	FreeBytes int64
}

// minFree is the free space below which the data directory check warns.
const minFree = 1 << 30

// DataDirCheck reports whether the data directory is writable and has room.
func DataDirCheck(info DataDirInfo) Check {
	return Check{ID: "data_dir", Name: "Data dir", Run: func(context.Context, *Env) Result {
		res := Result{Status: StatusOK, Summary: info.Path}
		if info.WriteErr != "" {
			res.Status = StatusFail
			res.Summary = info.Path + " isn't writable"
			res.Items = append(res.Items, Item{Status: StatusFail, Text: info.WriteErr})
			res.Hint = "Fix the directory's permissions, or pick another with --data-dir or options.data_directory."
			return res
		}
		if !info.Exists {
			res.Items = append(res.Items, Item{Status: StatusOK, Text: "Not created yet; Ta-Da creates it on first run"})
		}
		if info.FreeBytes >= 0 {
			it := Item{Status: StatusOK, Text: HumanBytes(info.FreeBytes) + " free"}
			if info.FreeBytes < minFree {
				it.Status = StatusWarn
				it.Text = "Only " + HumanBytes(info.FreeBytes) + " free on this disk"
				it.Hint = "Free up some space; sessions and logs need room to grow."
				res.Status = StatusWarn
			}
			res.Items = append(res.Items, it)
		}
		return res
	}}
}

// DatabaseInfo describes the session database.
type DatabaseInfo struct {
	Path   string
	Exists bool
	Size   int64
	// OpenErr is why the database couldn't be opened or read, or "".
	OpenErr  string
	Sessions int
}

// DatabaseCheck reports whether the session database opens.
func DatabaseCheck(info DatabaseInfo) Check {
	return Check{ID: "database", Name: "Database", Run: func(context.Context, *Env) Result {
		switch {
		case !info.Exists:
			return Result{Status: StatusOK, Summary: "No database yet; Ta-Da creates it on first run"}
		case info.OpenErr != "":
			return Result{
				Status:  StatusFail,
				Summary: "The session database doesn't open",
				Hint:    "Quit any other Ta-Da using it and try again; if it's corrupt, move " + info.Path + " aside to start fresh.",
				Items:   []Item{{Status: StatusFail, Text: info.OpenErr}},
			}
		}
		return Result{
			Status:  StatusOK,
			Summary: fmt.Sprintf("%s, %s", HumanBytes(info.Size), plural(info.Sessions, "session", "sessions")),
			Items:   []Item{{Status: StatusOK, Text: info.Path}},
		}
	}}
}

// HumanBytes formats n bytes with a binary unit.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// errAt joins a file name and an error as path:line:col: message when the
// error starts with a position, and as path: message otherwise.
func errAt(path, err string) string {
	if err != "" && err[0] >= '0' && err[0] <= '9' {
		return path + ":" + err
	}
	return path + ": " + err
}

// ValidateJSON reports a syntax error in data, prefixed with line:col.
func ValidateJSON(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	var v any
	err := json.Unmarshal(data, &v)
	if err == nil {
		return nil
	}
	var syn *json.SyntaxError
	if errors.As(err, &syn) {
		line, col := lineCol(data, syn.Offset)
		return fmt.Errorf("%d:%d: %s", line, col, syn.Error())
	}
	var typ *json.UnmarshalTypeError
	if errors.As(err, &typ) {
		line, col := lineCol(data, typ.Offset)
		return fmt.Errorf("%d:%d: %s", line, col, typ.Error())
	}
	return err
}

func lineCol(data []byte, offset int64) (int, int) {
	offset = min(max(offset, 0), int64(len(data)))
	before := data[:offset]
	line := bytes.Count(before, []byte("\n")) + 1
	col := int(offset) - bytes.LastIndexByte(before, '\n') - 1
	return line, max(col, 1)
}
