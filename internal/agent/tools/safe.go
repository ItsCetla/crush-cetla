package tools

import (
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/charmbracelet/crush/internal/shell"
	"mvdan.cc/sh/v3/syntax"
)

// argCheck validates the arguments of a safe command (everything after the
// command name). It returns false when the arguments could make the command
// execute something or write anywhere.
type argCheck func(args []string) bool

// safeCommands maps read-only commands to the check their arguments must
// pass to run without a permission prompt. Commands that run other commands
// (env, nice, nohup, timeout, time, xargs) or change state (kill, killall,
// set, unset) do not belong here, and neither do commands that reach the
// network (git ls-remote, ping, nslookup).
var safeCommands = map[string]argCheck{
	// System information.
	"cal":      anyArgs,
	"date":     denyOptions("sf", "set", "file"),
	"df":       anyArgs,
	"echo":     anyArgs,
	"free":     anyArgs,
	"groups":   anyArgs,
	"hostname": hostnameArgs,
	"id":       anyArgs,
	"ls":       anyArgs,
	"printenv": anyArgs,
	"ps":       anyArgs,
	"pwd":      anyArgs,
	"top":      anyArgs,
	"type":     anyArgs,
	"uname":    anyArgs,
	"uptime":   anyArgs,
	"whatis":   anyArgs,
	"whereis":  anyArgs,
	"which":    anyArgs,
	"whoami":   anyArgs,

	// File inspection, confined to the working directory like the view
	// tool, which asks before reading anything outside it. (du is here
	// because --files0-from echoes a file's content in its errors.)
	"cat":   confined(anyArgs),
	"du":    confined(anyArgs),
	"egrep": confined(anyArgs),
	"fgrep": confined(anyArgs),
	"find":  confined(denyWords("-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint", "-fprint0", "-fprintf", "-fls")),
	"grep":  confined(anyArgs),
	"head":  confined(anyArgs),
	"rg":    confined(denyOptions("", "pre")),
	"sort":  confined(denyOptions("o", "output", "compress-program")),
	"tail":  confined(anyArgs),
	"tree":  confined(denyOptions("oR", "output")),
	"wc":    confined(anyArgs),

	"git": gitArgs,
	"go":  goArgs,
}

func init() {
	if runtime.GOOS == "windows" {
		safeCommands["ipconfig"] = func(args []string) bool {
			// /release, /renew, /flushdns and friends change state.
			return len(args) == 0 || (len(args) == 1 && strings.EqualFold(args[0], "/all"))
		}
		safeCommands["systeminfo"] = anyArgs
		safeCommands["tasklist"] = anyArgs
		safeCommands["where"] = anyArgs
	}
}

// isSafeReadOnlyCommand reports whether command can run without asking the
// user. It only approves exactly one simple command: no lists, pipes,
// background jobs, subshells, redirections, assignments, expansions or
// substitutions of any kind, and no glob or brace patterns (which could
// expand to option-like file names). The command must be on the safe list
// and its arguments must pass that entry's check. Anything else goes through
// the normal permission prompt.
func isSafeReadOnlyCommand(command string) bool {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil || len(file.Stmts) != 1 {
		return false
	}
	stmt := file.Stmts[0]
	if stmt.Negated || stmt.Background || stmt.Coprocess || stmt.Disown || len(stmt.Redirs) > 0 {
		return false
	}
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Assigns) > 0 || len(call.Args) == 0 {
		return false
	}
	argv := make([]string, 0, len(call.Args))
	for _, w := range call.Args {
		arg, ok := plainWord(w)
		if !ok {
			return false
		}
		argv = append(argv, arg)
	}
	check, ok := safeCommands[argv[0]]
	return ok && check(argv[1:])
}

// plainWord returns the value of a word whose value cannot change at run
// time: unquoted literals free of escapes, glob, brace and tilde syntax,
// single-quoted strings, and double-quoted literals.
func plainWord(w *syntax.Word) (string, bool) {
	for i, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			if strings.ContainsAny(p.Value, "\\*?[]{}") {
				return "", false
			}
			// A tilde expands at the start of a word and after = or :
			// (HEAD~1 stays literal).
			if (i == 0 && strings.HasPrefix(p.Value, "~")) ||
				strings.Contains(p.Value, "=~") || strings.Contains(p.Value, ":~") {
				return "", false
			}
		case *syntax.SglQuoted:
		case *syntax.DblQuoted:
			for _, inner := range p.Parts {
				if lit, ok := inner.(*syntax.Lit); !ok || strings.Contains(lit.Value, `\`) {
					return "", false
				}
			}
		default:
			return "", false
		}
	}
	return shell.LiteralWord(w)
}

// bannedCommandIn returns the first banned command that command would run,
// looking at every simple command anywhere in it (pipelines, lists,
// subshells, substitutions) and at the commands run through wrappers such
// as env, timeout, xargs or sh -c. It returns the empty string when none is
// found. Commands assembled at run time are caught by the shell's block
// list instead.
func bannedCommandIn(command string) string {
	banned := make(map[string]bool, len(bannedCommands))
	for _, name := range bannedCommands {
		banned[name] = true
	}
	for _, argv := range shell.ScriptCommands(command) {
		for _, variant := range shell.CommandVariants(argv) {
			if name := shell.CommandName(variant[0]); banned[name] {
				return name
			}
		}
	}
	return ""
}

// sameDir reports whether a and b name the same directory.
func sameDir(a, b string) bool {
	if a == b {
		return true
	}
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return false
	}
	if absA == absB {
		return true
	}
	realA, errA := filepath.EvalSymlinks(absA)
	realB, errB := filepath.EvalSymlinks(absB)
	return errA == nil && errB == nil && realA == realB
}

func anyArgs([]string) bool { return true }

// confined additionally requires that no argument names a path outside the
// working directory.
func confined(check argCheck) argCheck {
	return func(args []string) bool {
		return argsStayInWorkingDir(args) && check(args)
	}
}

// argsStayInWorkingDir reports whether no argument points outside the
// working directory: no absolute paths, no ~, no .. components, and no
// option carrying such a path (--file=/etc/passwd, -f/etc/passwd).
func argsStayInWorkingDir(args []string) bool {
	for _, a := range args {
		value := a
		if strings.HasPrefix(a, "-") {
			_, v, ok := strings.Cut(a, "=")
			if !ok {
				// A short option may carry its value attached.
				if strings.ContainsAny(a, `/\~:`) || strings.Contains(a, "..") {
					return false
				}
				continue
			}
			value = v
		}
		if escapesWorkingDir(value) {
			return false
		}
	}
	return true
}

func escapesWorkingDir(p string) bool {
	if strings.HasPrefix(p, "~") || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) ||
		filepath.IsAbs(p) || filepath.VolumeName(p) != "" {
		return true
	}
	return slices.Contains(strings.FieldsFunc(p, func(r rune) bool {
		return r == '/' || r == '\\'
	}), "..")
}

// denyOptions rejects arguments that use any of the given options. Short
// options match anywhere in a bundle (-rno uses -o). Long options also match
// their abbreviations, which getopt_long and git accept (--outp is
// --output). Option parsing stops at "--".
func denyOptions(short string, long ...string) argCheck {
	return func(args []string) bool {
		return !hasOption(args, short, long)
	}
}

func hasOption(args []string, short string, long []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if strings.HasPrefix(a, "--") {
			name, _, _ := strings.Cut(a[2:], "=")
			if name == "" {
				continue
			}
			for _, l := range long {
				if strings.HasPrefix(l, name) {
					return true
				}
			}
			continue
		}
		if len(a) > 1 && a[0] == '-' && strings.ContainsAny(a[1:], short) {
			return true
		}
	}
	return false
}

// denyWords rejects arguments containing any of the given words, for
// commands such as find whose options are whole words.
func denyWords(words ...string) argCheck {
	return func(args []string) bool {
		return !slices.ContainsFunc(args, func(a string) bool {
			return slices.Contains(words, a)
		})
	}
}

// hostnameArgs allows printing the host name but not setting it.
func hostnameArgs(args []string) bool {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return false
		}
	}
	return !hasOption(args, "Fb", []string{"file", "boot"})
}

// gitArgs allows the read-only git subcommands below. Global options are
// refused: they can run programs (-c core.pager=..., --exec-path,
// --config-env) or point git at another repository (-C, --git-dir,
// --work-tree). Only the pager toggles are harmless.
func gitArgs(args []string) bool {
	for len(args) > 0 && (args[0] == "--no-pager" || args[0] == "-P") {
		args = args[1:]
	}
	if len(args) == 0 {
		return false
	}
	check, ok := gitSubcommands[args[0]]
	return ok && check(args[1:])
}

// gitDiffish guards commands that take diff options: --output writes a
// file, --ext-diff runs the configured external diff program, and
// --no-index compares files anywhere on disk.
var gitDiffish = denyOptions("", "output", "ext-diff", "no-index")

var gitSubcommands = map[string]argCheck{
	// --contents shows an arbitrary file; -S and --ignore-revs-file read
	// one and echo its lines in errors.
	"blame": denyOptions("S", "contents", "ignore-revs-file"),
	// Listing only: anything else creates, deletes, renames or
	// reconfigures branches.
	"branch": listingOnly("dDmMcCfut", "delete", "move", "copy", "force", "set-upstream-to",
		"set-upstream", "unset-upstream", "edit-description", "track", "no-track",
		"create-reflog", "recurse-submodules"),
	"config":   gitConfigArgs,
	"describe": anyArgs,
	"diff":     gitDiffish,
	// -O/--open-files-in-pager runs a program; --no-index searches files
	// outside the repository.
	"grep":      denyOptions("O", "open-files-in-pager", "no-index"),
	"log":       gitDiffish,
	"ls-files":  anyArgs,
	"remote":    gitRemoteArgs,
	"rev-parse": anyArgs,
	"shortlog":  gitDiffish,
	"show":      gitDiffish,
	"status":    anyArgs,
	// Listing only: anything else creates, deletes, signs or verifies
	// tags (signing and verifying run gpg).
	"tag": listingOnly("adfFmsuve", "annotate", "delete", "force", "file", "message", "sign",
		"local-user", "edit", "verify", "create-reflog", "cleanup", "trailer"),
}

// listingOnly guards git branch and git tag: the denied options are refused,
// and positional arguments (which name a branch or tag to create) are only
// allowed together with -l/--list, where they are patterns.
func listingOnly(short string, long ...string) argCheck {
	return func(args []string) bool {
		if hasOption(args, short, long) {
			return false
		}
		for _, a := range args {
			if a == "--list" || (len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.ContainsRune(a, 'l')) {
				return true
			}
		}
		for _, a := range args {
			if !strings.HasPrefix(a, "-") || a == "--" {
				return false
			}
		}
		return true
	}
}

// gitConfigArgs allows reading git configuration only. Reading a file
// given with --file is refused too: git parses any INI file, such as
// ~/.aws/credentials, and prints its values.
func gitConfigArgs(args []string) bool {
	if hasOption(args, "ef", []string{
		"edit", "add", "replace-all", "unset", "unset-all", "rename-section",
		"remove-section", "file",
	}) {
		return false
	}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0] == "get" || args[0] == "list"
	}
	return slices.ContainsFunc(args, func(a string) bool {
		switch a {
		case "-l", "--list", "--get", "--get-all", "--get-regexp", "--get-urlmatch", "--get-color", "--get-colorbool":
			return true
		}
		return false
	})
}

// gitRemoteArgs allows listing remotes and their URLs. Every other remote
// subcommand changes configuration or contacts the remote.
func gitRemoteArgs(args []string) bool {
	switch {
	case len(args) == 0:
		return true
	case args[0] == "-v" || args[0] == "--verbose":
		return len(args) == 1
	case args[0] == "get-url":
		return true
	}
	return false
}

// goArgs allows go version and go env, but not go env -w/-u, which write
// the Go environment file.
func goArgs(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "version":
		return argsStayInWorkingDir(args[1:])
	case "env":
		for _, a := range args[1:] {
			if !strings.HasPrefix(a, "-") {
				continue
			}
			name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
			if name == "w" || name == "u" {
				return false
			}
		}
		return true
	}
	return false
}
