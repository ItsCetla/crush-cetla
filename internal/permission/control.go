package permission

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/crush/internal/shell"
	"mvdan.cc/sh/v3/syntax"
)

// Class grades how much a permission request can change. Classes are
// ordered from least to most consequential; when two signals disagree the
// stricter class wins.
type Class string

const (
	// ClassRead only looks at files.
	ClassRead Class = "read"
	// ClassWrite changes files inside the project or runs a local command.
	ClassWrite Class = "write"
	// ClassOutside writes outside the project, or to a protected path
	// inside it such as .git, CI workflows or Ta-Da's project config.
	ClassOutside Class = "outside"
	// ClassNetwork talks to another machine.
	ClassNetwork Class = "network"
	// ClassDelete removes data or history, or stops processes.
	ClassDelete Class = "delete"
	// ClassSpend goes past a spending budget.
	ClassSpend Class = "spend"
)

// Classes lists every class from least to most consequential.
var Classes = []Class{ClassRead, ClassWrite, ClassOutside, ClassNetwork, ClassDelete, ClassSpend}

// Rank orders classes; unknown classes rank as write.
func (c Class) Rank() int {
	if i := slices.Index(Classes, c); i >= 0 {
		return i
	}
	return 1
}

// Valid reports whether c is a known class.
func (c Class) Valid() bool { return slices.Contains(Classes, c) }

// Label is the short human name shown in the UI.
func (c Class) Label() string {
	switch c {
	case ClassRead:
		return "read"
	case ClassWrite:
		return "edit"
	case ClassOutside:
		return "outside or protected"
	case ClassNetwork:
		return "network"
	case ClassDelete:
		return "delete"
	case ClassSpend:
		return "spend"
	}
	return string(c)
}

// Stricter returns the more consequential of two classes.
func Stricter(a, b Class) Class {
	if b.Rank() > a.Rank() {
		return b
	}
	return a
}

// BudgetToolName is the pseudo tool name of a budget confirmation.
const BudgetToolName = "budget"

// CommandTarget is implemented by the params of tools that run a shell
// command, so the command text can be graded.
type CommandTarget interface {
	TargetCommand() string
}

// WorkdirTarget is implemented by the params of tools that run in a
// directory other than the project root.
type WorkdirTarget interface {
	TargetWorkdir() string
}

var (
	cmdStart = "(?:^|[\\s;&|()`$]|\\bsudo\\s+|\\bxargs\\s+)"
	cmdEnd   = "(?:\\s|$|;|&|\\|)"

	deleteCommandRE = regexp.MustCompile(`(?i)` + cmdStart +
		`(?:rm|rmdir|unlink|shred|dd|mkfs(?:\.\w+)?|truncate|kill|pkill|killall|srm)` + cmdEnd +
		`|\bgit\s+(?:clean|reset\s+--hard|push\s+(?:.*\s)?(?:-f|--force(?:-with-lease)?)|branch\s+(?:.*\s)?-D|stash\s+(?:drop|clear)|checkout\s+--\s|restore\s)` +
		`|\bdrop\s+(?:table|database|schema)\b|\bdelete\s+from\b|\bfind\b.*\s-delete\b`)

	networkCommandRE = regexp.MustCompile(`(?i)` + cmdStart +
		`(?:curl|wget|ssh|scp|sftp|ftp|rsync|nc|ncat|telnet|gh|aws|gcloud|az|kubectl|flyctl|vercel|netlify|heroku)` + cmdEnd +
		`|\bgit\s+(?:push|pull|fetch|clone|remote\s+update|submodule\s+update)\b` +
		`|\b(?:npm|pnpm|yarn|bun)\s+(?:install|i|add|publish|update|upgrade)\b|\bnpx\s` +
		`|\bpip3?\s+install\b|\buv\s+(?:add|sync|pip\s+install)\b|\bbrew\s+(?:install|upgrade|update)\b` +
		`|\bgo\s+(?:get|install|mod\s+download)\b|\bcargo\s+(?:install|publish|update)\b` +
		`|\bdocker\s+(?:push|pull|login)\b|\bdeploy\b|https?://`)

	deleteNameRE  = regexp.MustCompile(`(?i)(?:delete|remove|destroy|drop|purge|wipe|erase|kill|terminate)`)
	networkNameRE = regexp.MustCompile(`(?i)(?:fetch|http|web|browse|browser|search|send|post|upload|download|email|mail|slack|deploy|publish|tweet|message|request)`)
)

// networkTools send data off the machine. "machines" is here because
// saving a machine decides which URL later receives code and prompts.
var networkTools = []string{"fetch", "download", "sourcegraph", "agentic_fetch", "web_fetch", "web_search", "machines", "machine_ask"}

var readTools = []string{"view", "ls", "glob", "grep", "list_mcp_resources", "read_mcp_resource"}

// builtinTools are the tools whose class is known from the name alone;
// any other tool name is matched by keywords.
var builtinTools = []string{"bash", "edit", "multiedit", "write", "lsp_rename", "lsp_replace_symbol", "artifact", "artifact_data", "artifact_comments", "mission"}

var (
	deleteCommands = []string{
		"rm", "rmdir", "unlink", "shred", "dd", "truncate", "kill", "pkill",
		"killall", "srm", "wipefs", "fdisk", "diskutil",
	}
	networkCommands = []string{
		"curl", "wget", "ssh", "scp", "sftp", "ftp", "rsync", "nc", "ncat",
		"telnet", "gh", "aws", "gcloud", "az", "kubectl", "flyctl", "vercel",
		"netlify", "heroku", "npx", "bunx",
	}
	interpreters = []string{
		"python", "python2", "python3", "perl", "ruby", "node", "deno", "bun",
		"php", "osascript", "pwsh", "powershell", "awk", "gawk", "mawk",
		"nawk", "lua", "luajit", "tclsh", "wish", "expect", "rscript",
		"julia", "jshell", "groovy", "elixir", "erl", "racket", "guile",
	}

	// deleteCodeRE and networkCodeRE spot deletes and network calls in
	// code handed to an interpreter, such as `python -c` or a heredoc.
	deleteCodeRE = regexp.MustCompile(`(?i)\b(?:os\.(?:remove|unlink|rmdir|removedirs)|shutil\.rmtree|rmtree|unlink(?:Sync)?|rm(?:Sync|dir|dirSync)?\s*\(|fs\.(?:promises\.)?rm|rm_r[f]?|rm_rf|File\.delete|FileUtils\.rm|Remove-Item|send2trash|\.unlink\(|kill\s*\()` +
		// A shell command inside the code, as in awk's system("rm x").
		`|["'\x60]\s*(?:sudo\s+)?(?:/\S*/)?(?:rm|rmdir|unlink|shred|dd|truncate|kill|pkill|killall|srm)\s`)
	// protectedCommandRE spots a command that touches a protected project
	// path (see protectedDirs) or changes git config, which can point git
	// at hooks that run later.
	protectedCommandRE = regexp.MustCompile(`(?i)(?:^|[\s'"=:/<>(])\.(?:git|tada|claude|github|vscode|idea|husky)(?:[/\\'"\s;&|)]|$)` +
		`|(?:^|[\s'"=:/<>(])\.?tada(?:rc|\.json)\b|\.mcp\.json\b|\.envrc\b|\bgit\s+(?:-C\s+\S+\s+)?config\b`)
	networkCodeRE = regexp.MustCompile(`(?i)(?:urllib|requests\.|httpx|http\.client|aiohttp|socket\.|fetch\s*\(|XMLHttpRequest|https?\.(?:get|request)|Net::HTTP|LWP::|Invoke-WebRequest|Invoke-RestMethod|https?://)`)
)

// ClassifyCommand grades a shell command. It matches the raw text and also
// parses the script, so a delete can't hide behind a path (/bin/rm), quotes
// ("rm"), a backslash (\rm), a wrapper (sudo, env, xargs, sh -c) or an
// interpreter one-liner. A script that doesn't parse, or runs a command
// whose name is only known at run time ($cmd, "$(...)"), grades as a delete
// so it always asks.
func ClassifyCommand(command string) Class {
	class := ClassWrite
	switch {
	case deleteCommandRE.MatchString(command):
		return ClassDelete
	case networkCommandRE.MatchString(command):
		class = ClassNetwork
	}
	if strings.TrimSpace(command) == "" {
		return class
	}
	if _, err := syntax.NewParser().Parse(strings.NewReader(command), ""); err != nil {
		return ClassDelete
	}
	cmds := shell.ScriptCommands(command)
	interpreted := false
	for _, argv := range cmds {
		for _, variant := range shell.CommandVariants(argv) {
			if variant[0] == "" {
				return ClassDelete
			}
			name := strings.ToLower(shell.CommandName(variant[0]))
			switch {
			case slices.Contains(deleteCommands, name) || strings.HasPrefix(name, "mkfs"):
				return ClassDelete
			case slices.Contains(networkCommands, name):
				class = ClassNetwork
			case isInterpreter(name):
				interpreted = true
			}
		}
	}
	if protectedCommandRE.MatchString(command) || touchesProtected(command) {
		class = Stricter(class, ClassOutside)
	}
	if interpreted {
		// The code may sit in an argument or in a heredoc, so check the
		// whole command.
		if deleteCodeRE.MatchString(command) {
			return ClassDelete
		}
		if networkCodeRE.MatchString(command) {
			class = ClassNetwork
		}
	}
	return class
}

// touchesProtected parses a script and reports whether it names a protected
// project path once quoting is removed (.gi”t, ".git"/hooks, .g?t), assigns
// one to a variable, runs `git config`, or redirects output to a path that
// is only known at run time.
func touchesProtected(script string) bool {
	file, err := syntax.NewParser().Parse(strings.NewReader(script), "")
	if err != nil {
		return true
	}
	found := false
	syntax.Walk(file, func(node syntax.Node) bool {
		if found {
			return false
		}
		switch n := node.(type) {
		case *syntax.Redirect:
			if n.Word != nil && writesTo(n.Op) {
				if _, ok := shell.LiteralWord(n.Word); !ok {
					found = true
				}
			}
		case *syntax.CallExpr:
			if len(n.Args) > 0 {
				argv := make([]string, len(n.Args))
				for i, w := range n.Args {
					argv[i], _ = shell.LiteralWord(w)
				}
				for _, v := range shell.CommandVariants(argv) {
					if strings.ToLower(shell.CommandName(v[0])) == "git" &&
						gitSubcommand(v[1:]) == "config" {
						found = true
					}
				}
			}
		case *syntax.Word:
			if lit, ok := shell.LiteralWord(n); ok && protectedWord(lit) {
				found = true
			}
		}
		return !found
	})
	return found
}

// writesTo reports whether a redirect writes to its target.
func writesTo(op syntax.RedirOperator) bool {
	switch op {
	case syntax.RdrOut, syntax.AppOut, syntax.RdrInOut, syntax.RdrClob,
		syntax.RdrAll, syntax.AppAll:
		return true
	}
	return false
}

// gitSubcommand returns the first argument of a git command line that
// isn't a global option.
func gitSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-C" || a == "-c" || a == "--git-dir" || a == "--work-tree" ||
			a == "--namespace" || a == "--exec-path":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return a
		}
	}
	return ""
}

// protectedWord reports whether an unquoted shell word names a protected
// project path, in any segment and after an option's "=". A glob segment
// counts when it could match a protected name; like the shell, a pattern
// only matches a leading dot that it spells out.
func protectedWord(word string) bool {
	parts := strings.FieldsFunc(strings.ToLower(word), func(r rune) bool {
		return r == '/' || r == '\\' || r == '='
	})
	for i, part := range parts {
		part, _, _ = strings.Cut(part, ":")
		part = strings.TrimRight(part, ". ")
		if part == "" {
			continue
		}
		names := protectedDirs
		if i == len(parts)-1 {
			names = append(slices.Clip(names), protectedNames...)
		}
		for _, name := range names {
			if part == name {
				return true
			}
			if strings.ContainsAny(part, "*?[") &&
				(name[0] != '.' || part[0] == '.') {
				if ok, _ := path.Match(part, name); ok {
					return true
				}
			}
		}
	}
	return false
}

// isInterpreter reports whether name runs code it is given, including
// versioned names such as python3.12.
func isInterpreter(name string) bool {
	for _, i := range interpreters {
		if name == i || (strings.HasPrefix(name, i) &&
			strings.Trim(name[len(i):], "0123456789.") == "") {
			return true
		}
	}
	return false
}

// Classify grades a request from the tool's declared kind and from the
// text of what it's about to do, and keeps the stricter of the two.
func Classify(opts CreatePermissionRequest, workingDir string) Class {
	declared := ClassWrite
	switch {
	case opts.ToolName == BudgetToolName:
		return ClassSpend
	case slices.Contains(networkTools, opts.ToolName):
		declared = ClassNetwork
	case slices.Contains(readTools, opts.ToolName):
		declared = ClassRead
	case opts.Action == "delete":
		declared = ClassDelete
	case opts.Action == "read" || opts.Action == "list":
		declared = ClassRead
	}

	if t, ok := opts.Params.(FileTarget); ok && t.TargetFile() != "" {
		if !editableInProject(workingDir, absUnder(workingDir, t.TargetFile())) {
			declared = Stricter(declared, ClassOutside)
		}
	}

	var text string
	if t, ok := opts.Params.(CommandTarget); ok {
		text = t.TargetCommand()
		if w, ok := opts.Params.(WorkdirTarget); ok && w.TargetWorkdir() != "" &&
			!withinDir(workingDir, absUnder(workingDir, w.TargetWorkdir())) {
			declared = Stricter(declared, ClassOutside)
		}
		return Stricter(declared, ClassifyCommand(text))
	}

	if !slices.Contains(builtinTools, opts.ToolName) && !slices.Contains(networkTools, opts.ToolName) &&
		!slices.Contains(readTools, opts.ToolName) {
		// MCP and other unknown tools: judge by their name, since the
		// arguments are free-form.
		switch {
		case deleteNameRE.MatchString(opts.ToolName):
			declared = Stricter(declared, ClassDelete)
		case networkNameRE.MatchString(opts.ToolName):
			declared = Stricter(declared, ClassNetwork)
		}
	}
	return declared
}

func absUnder(root, p string) string {
	if p == "" || filepath.IsAbs(p) || root == "" {
		return p
	}
	return filepath.Join(root, p)
}

// Policy decides which classes always ask and how many actions of each
// class an auto mode may approve per session before asking again.
type Policy struct {
	// AlwaysAsk lists classes that ask even in Auto and Edits mode and
	// even when the action was approved for the session.
	AlwaysAsk []Class `json:"always_ask"`
	// AutoBudget caps, per session, the actions of a class that Auto and
	// Edits mode approve before asking. A class that isn't listed has no
	// cap.
	AutoBudget map[Class]int `json:"auto_budget,omitempty"`
}

// DefaultPolicy always asks before deleting and has no auto budgets.
func DefaultPolicy() Policy {
	return Policy{AlwaysAsk: []Class{ClassDelete}}
}

// Asks reports whether class always asks.
func (p Policy) Asks(class Class) bool {
	return slices.Contains(p.AlwaysAsk, class) || class == ClassSpend
}

// Decision is the outcome of a request.
type Decision string

const (
	DecisionAllowed  Decision = "allowed"
	DecisionDenied   Decision = "denied"
	DecisionCanceled Decision = "canceled"
	DecisionEvent    Decision = "event"
)

// Via says who or what made a decision.
type Via string

const (
	ViaUser      Via = "you"
	ViaSession   Via = "allowed for session"
	ViaAllowlist Via = "allowed_tools"
	ViaProject   Via = "project_allowed_tools"
	ViaHook      Via = "hook"
	ViaAuto      Via = "auto mode"
	ViaEdits     Via = "edits mode"
	ViaHeadless  Via = "non-interactive run"
	ViaTakeBack  Via = "take back"
)

// TakeBackToolName is the tool name of the take-back event in the log.
const TakeBackToolName = "take_back"

// Action is one entry of the action log.
type Action struct {
	At          time.Time `json:"at"`
	SessionID   string    `json:"session_id,omitempty"`
	ToolCallID  string    `json:"tool_call_id,omitempty"`
	ToolName    string    `json:"tool_name"`
	Action      string    `json:"action,omitempty"`
	Description string    `json:"description,omitempty"`
	Path        string    `json:"path,omitempty"`
	Class       Class     `json:"class,omitempty"`
	Decision    Decision  `json:"decision"`
	Via         Via       `json:"via,omitempty"`
}

// Summary is a one-line description of the action, at most 80 runes.
func (a Action) Summary() string {
	s := a.Description
	if s == "" {
		s = strings.TrimSpace(a.ToolName + " " + a.Action)
	}
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 80 {
		s = string(r[:79]) + "…"
	}
	return s
}

// ControlState is a snapshot of the control settings.
type ControlState struct {
	Auto        bool      `json:"auto"`
	Edits       bool      `json:"edits"`
	TakenBackAt time.Time `json:"taken_back_at,omitzero"`
	Waiting     bool      `json:"waiting"`
	Policy      Policy    `json:"policy"`
}

// TakenBackSince reports whether the person took control back after t,
// so a denial can be explained as that rather than a plain "no".
func TakenBackSince(svc Service, t time.Time) bool {
	c, ok := svc.(Controller)
	if !ok {
		return false
	}
	at := c.ControlState().TakenBackAt
	return !at.IsZero() && !at.Before(t)
}

// Controller is implemented by the permission service. It grades
// requests, keeps the action log and lets the person take control back.
// It's a separate interface so existing Service mocks stay valid.
type Controller interface {
	// SetEditsMode turns on approving edits inside the project without
	// asking.
	SetEditsMode(on bool)
	EditsMode() bool
	// TakeBack turns Auto and Edits mode off, forgets every approval
	// remembered for a session and denies the request that's waiting.
	TakeBack()
	ControlState() ControlState
	SetPolicy(p Policy)
	// Actions returns the most recent log entries, newest first. An
	// empty sessionID returns entries of every session.
	Actions(sessionID string, limit int) []Action
	// SetRecorder registers a function that receives every new log
	// entry, for example to save it. It's called synchronously.
	SetRecorder(fn func(Action))
}

// reasonFor explains why a request that might otherwise run on its own
// is asking.
func reasonFor(class Class, alwaysAsk bool, budget int) string {
	switch {
	case class == ClassSpend:
		return "A spending budget was reached."
	case alwaysAsk:
		return fmt.Sprintf("%s actions always ask (permissions.always_ask).", capitalize(class.Label()))
	case budget > 0:
		return fmt.Sprintf("Auto mode used its %d %s actions for this session (permissions.auto_budget).", budget, class.Label())
	}
	return ""
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
