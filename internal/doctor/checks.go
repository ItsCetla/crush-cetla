package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// onlineTimeout bounds one reachability request with --online.
const onlineTimeout = 3 * time.Second

// VersionInfo describes the running build.
type VersionInfo struct {
	Version string
	Commit  string
	// Dev reports a development build, which never checks for updates.
	Dev bool
	// Cached is the newer version the last background check found, or "".
	Cached string
	// Latest asks GitHub for the latest release; only called with --online.
	Latest func(ctx context.Context) (latest string, newer bool, err error)
}

// VersionCheck reports the version and whether a newer one is out.
func VersionCheck(info VersionInfo) Check {
	return Check{ID: "version", Name: "Version", Run: func(ctx context.Context, env *Env) Result {
		summary := "tada " + info.Version
		if c := shortCommit(info.Commit); c != "" {
			summary += " (" + c + ")"
		}
		res := Result{Status: StatusOK, Summary: summary}
		if info.Dev {
			res.Items = append(res.Items, Item{Status: StatusOK, Text: "Development build; update checks are off"})
			return res
		}
		update := func(v string) {
			res.Status = StatusWarn
			res.Summary += ", " + v + " is out"
			res.Hint = "Run `tada update` to install it."
		}
		switch {
		case env.Online && info.Latest != nil:
			latest, newer, err := info.Latest(ctx)
			switch {
			case err != nil:
				res.Items = append(res.Items, Item{
					Status: StatusWarn,
					Text:   "Couldn't check for updates: " + err.Error(),
					Hint:   "Check your network connection, or see https://github.com/ItsCetla/tada/releases.",
				})
				res.Status = StatusWarn
			case newer:
				update(latest)
			case latest != "":
				res.Items = append(res.Items, Item{Status: StatusOK, Text: "Up to date (latest release is " + latest + ")"})
			}
		case info.Cached != "":
			update(info.Cached)
		default:
			res.Items = append(res.Items, Item{Status: StatusOK, Text: "No newer release seen yet; pass --online to ask GitHub now"})
		}
		return res
	}}
}

func shortCommit(c string) string {
	if c == "" || c == "unknown" {
		return ""
	}
	if len(c) > 7 {
		return c[:7]
	}
	return c
}

// ConfigFile is one config file that exists on disk.
type ConfigFile struct {
	// Path is the file's path for display.
	Path string
	// Scope is "system", "global" or "project".
	Scope string
	// JSON reports a tada.json file rather than a tadarc.
	JSON bool
	// State reports a file Ta-Da writes itself (models picked in the TUI and
	// the like), which doesn't count as deprecated.
	State bool
	// Untrusted reports project config that isn't loaded because the
	// project isn't trusted.
	Untrusted bool
	// Err is a parse error, prefixed with line:col when known.
	Err string
}

// ConfigInfo describes the config Ta-Da found and how loading it went.
type ConfigInfo struct {
	Files []ConfigFile
	// LoadErr is why the config didn't load, or "".
	LoadErr string
	// Findings are what loading the config warned about.
	Findings []Item
}

// ConfigCheck reports the config files and any errors in them.
func ConfigCheck(info ConfigInfo) Check {
	return Check{ID: "config", Name: "Config", Run: func(context.Context, *Env) Result {
		var res Result
		var deprecated, untrusted, broken int
		for _, f := range info.Files {
			label := f.Path
			switch {
			case f.Err != "":
				broken++
				res.Items = append(res.Items, Item{
					Status: StatusFail,
					Text:   errAt(label, f.Err),
					Hint:   "Fix the syntax error; Ta-Da won't start until this file parses.",
				})
			case f.Untrusted:
				untrusted++
				res.Items = append(res.Items, Item{
					Status: StatusWarn,
					Text:   label + " isn't loaded: the project isn't trusted",
					Hint:   "Review it, then run `tada trust`.",
				})
			case f.JSON && !f.State:
				deprecated++
				res.Items = append(res.Items, Item{
					Status: StatusWarn,
					Text:   label + " uses tada.json, which is deprecated",
					Hint:   "Move these settings into a tadarc in the same directory (see docs/config/); tada.json still loads for now.",
				})
			default:
				res.Items = append(res.Items, Item{Status: StatusOK, Text: label + " (" + f.Scope + ")"})
			}
		}
		if info.LoadErr != "" && broken == 0 {
			res.Items = append(res.Items, Item{
				Status: StatusFail,
				Text:   info.LoadErr,
				Hint:   "Fix the config error; Ta-Da won't start until the config loads.",
			})
		}
		res.Items = append(res.Items, info.Findings...)

		switch loaded := len(info.Files) - untrusted - broken; {
		case info.LoadErr != "" || broken > 0:
			res.Summary = "The config doesn't load"
		case len(info.Files) == 0:
			res.Status = StatusOK
			res.Summary = "No config files; using the defaults"
		default:
			res.Summary = plural(loaded, "file", "files") + " loaded"
			if untrusted > 0 {
				res.Summary += fmt.Sprintf(", %d skipped (untrusted)", untrusted)
			}
			if deprecated > 0 {
				res.Summary += ", tada.json is deprecated"
			}
		}
		return res
	}}
}

// Credential says where a provider's credentials come from.
type Credential string

const (
	CredEnv        Credential = "env"
	CredConfig     Credential = "config"
	CredCommand    Credential = "command"
	CredOAuth      Credential = "oauth"
	CredClaudeCode Credential = "claude_code"
	CredAWS        Credential = "aws"
	CredVertex     Credential = "vertex"
	CredLocal      Credential = "local"
	CredMissing    Credential = "missing"
)

// ProviderInfo describes one provider Ta-Da can use. It never holds a
// secret.
type ProviderInfo struct {
	ID         string
	Credential Credential
	// EnvName is the variable the API key comes from, for CredEnv.
	EnvName string
	// BaseURL is the API endpoint with any credentials removed.
	BaseURL      string
	OAuthExpired bool
}

// ProvidersInfo describes the configured providers.
type ProvidersInfo struct {
	Providers []ProviderInfo
	// Skipped are providers in the config that Ta-Da couldn't use.
	Skipped []Item
}

// ProvidersCheck reports where each provider's credentials come from and,
// with --online, whether its API host answers. It never sends a key or a
// chat request.
func ProvidersCheck(info ProvidersInfo) Check {
	return Check{ID: "providers", Name: "Providers", Run: func(ctx context.Context, env *Env) Result {
		var res Result
		if len(info.Providers) == 0 {
			res.Status = StatusFail
			res.Summary = "No providers configured"
			res.Hint = "Run `tada` to sign in, set an API key such as ANTHROPIC_API_KEY or OPENAI_API_KEY, or add a provider to your tadarc."
			res.Items = info.Skipped
			return res
		}
		ids := make([]string, 0, len(info.Providers))
		for _, p := range info.Providers {
			ids = append(ids, p.ID)
			res.Items = append(res.Items, providerItem(p))
		}
		res.Items = append(res.Items, info.Skipped...)
		res.Summary = strings.Join(ids, ", ")
		if env.Online {
			res.Items = append(res.Items, reachProviders(ctx, env, info.Providers)...)
		}
		return res
	}}
}

func providerItem(p ProviderInfo) Item {
	it := Item{Status: StatusOK}
	switch p.Credential {
	case CredEnv:
		it.Text = p.ID + ": API key set via $" + p.EnvName
	case CredConfig:
		it.Text = p.ID + ": API key from config"
	case CredCommand:
		it.Text = p.ID + ": API key from a command in the config"
	case CredOAuth:
		it.Text = p.ID + ": signed in (OAuth)"
		if p.OAuthExpired {
			it.Text += "; the token expired and is refreshed on next use"
		}
	case CredClaudeCode:
		it.Text = p.ID + ": uses the Claude Code CLI's login"
	case CredAWS:
		it.Text = p.ID + ": AWS credentials from the environment"
	case CredVertex:
		it.Text = p.ID + ": Google Cloud project from $VERTEXAI_PROJECT"
	case CredLocal:
		it.Text = p.ID + ": local, no key needed"
	default:
		it.Status = StatusWarn
		if p.EnvName != "" {
			it.Text = p.ID + ": no API key; $" + p.EnvName + " isn't set"
			it.Hint = "Set $" + p.EnvName + ", or change api_key for " + p.ID + " in your tadarc."
			break
		}
		it.Text = p.ID + ": no API key"
		it.Hint = "Set api_key for " + p.ID + " in your tadarc, for example to $" + strings.ToUpper(envSafe(p.ID)) + "_API_KEY."
	}
	return it
}

func envSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, s)
}

// reachProviders sends one HEAD request per distinct API host. Local
// providers are left to the local models check.
func reachProviders(ctx context.Context, env *Env, providers []ProviderInfo) []Item {
	var items []Item
	seen := map[string]bool{}
	for _, p := range providers {
		u, err := url.Parse(p.BaseURL)
		if p.Credential == CredLocal || p.BaseURL == "" || err != nil || u.Host == "" || seen[u.Host] {
			continue
		}
		seen[u.Host] = true
		items = append(items, reach(ctx, env, bareURL(u), u.Host, "Check the provider's base_url and your network or proxy settings."))
	}
	return items
}

// reach reports whether a HEAD request to rawURL gets any HTTP answer.
func reach(ctx context.Context, env *Env, rawURL, label, hint string) Item {
	ctx, cancel := context.WithTimeout(ctx, onlineTimeout)
	defer cancel()
	status, _, err := env.HTTP(ctx, "HEAD", rawURL)
	if err != nil {
		return Item{Status: StatusWarn, Text: "Can't reach " + label + ": " + shortErr(err), Hint: hint}
	}
	return Item{Status: StatusOK, Text: fmt.Sprintf("%s answered (HTTP %d)", label, status)}
}

// bareURL drops the credentials, query and fragment from u, so a reachability
// request never carries a key that was put in the URL.
func bareURL(u *url.URL) string {
	c := *u
	c.User = nil
	c.RawQuery = ""
	c.ForceQuery = false
	c.Fragment = ""
	return c.String()
}

// shortErr drops the request line that net/http puts in front of errors.
func shortErr(err error) string {
	if ue, ok := err.(*url.Error); ok {
		err = ue.Err
	}
	if err == context.DeadlineExceeded {
		return "timed out"
	}
	return err.Error()
}

// ModelInfo describes one selected model.
type ModelInfo struct {
	Provider string
	Model    string
	// Found reports that the provider offers the model.
	Found bool
	// FallbackFrom is the provider/model the config asked for, when it
	// wasn't available and Ta-Da picked another.
	FallbackFrom string
}

// ModelsInfo describes the selected large and small models.
type ModelsInfo struct {
	// Providers reports that at least one provider is configured.
	Providers bool
	Large     ModelInfo
	Small     ModelInfo
}

// ModelsCheck reports whether the selected models exist.
func ModelsCheck(info ModelsInfo) Check {
	return Check{ID: "models", Name: "Models", Run: func(context.Context, *Env) Result {
		if !info.Providers {
			return Result{Status: StatusSkip, Summary: "No providers, so no models to check"}
		}
		var res Result
		for _, m := range []struct {
			kind string
			ModelInfo
		}{{"large", info.Large}, {"small", info.Small}} {
			name := m.Provider + "/" + m.Model
			switch {
			case m.Model == "":
				res.Items = append(res.Items, Item{
					Status: StatusFail,
					Text:   "No " + m.kind + " model selected",
					Hint:   "Pick one from the command palette (ctrl+p) in Ta-Da, or set models." + m.kind + " in your tadarc.",
				})
			case !m.Found:
				res.Items = append(res.Items, Item{
					Status: StatusFail,
					Text:   m.kind + ": " + name + " isn't offered by " + m.Provider,
					Hint:   "Run `tada models` to list the models you can use.",
				})
			case m.FallbackFrom != "":
				res.Items = append(res.Items, Item{
					Status: StatusWarn,
					Text:   m.kind + ": " + m.FallbackFrom + " isn't available, so Ta-Da switched to " + name,
					Hint:   "Run `tada models` to list the models you can use, then set models." + m.kind + ".",
				})
			default:
				res.Items = append(res.Items, Item{Status: StatusOK, Text: m.kind + ": " + name})
			}
		}
		res.Summary = "large " + info.Large.Provider + "/" + info.Large.Model
		if info.Small != info.Large {
			res.Summary += ", small " + info.Small.Provider + "/" + info.Small.Model
		}
		if info.Large.Model == "" {
			res.Summary = "No model selected"
		}
		return res
	}}
}

// LocalServer is a local model server to probe.
type LocalServer struct {
	// Name is "Ollama", "LM Studio" or the provider's ID.
	Name string
	// ListURL lists the server's models: Ollama's /api/tags or an
	// OpenAI-compatible /v1/models.
	ListURL string
	// Ollama reports that ListURL answers in Ollama's format.
	Ollama bool
	// Provider is the ID of the provider that uses the server, or "".
	Provider string
	// Remote reports a server on another machine, which is only probed
	// with --online.
	Remote bool
}

// probeTimeout bounds a request to a local server nobody configured.
const probeTimeout = 300 * time.Millisecond

// LocalModelsCheck asks local model servers which models they have.
func LocalModelsCheck(servers []LocalServer) Check {
	return Check{ID: "local_models", Name: "Local models", Run: func(ctx context.Context, env *Env) Result {
		var res Result
		var found []string
		for _, s := range servers {
			it, up := probeLocal(ctx, env, s)
			if up {
				found = append(found, s.Name)
			}
			res.Items = append(res.Items, it)
		}
		switch {
		case len(found) > 0:
			res.Summary = strings.Join(found, ", ") + " running"
		case Worst(res.Items) == StatusWarn:
			res.Summary = "A configured local server isn't running"
		default:
			res.Summary = "No local model server running"
		}
		return res
	}}
}

func probeLocal(ctx context.Context, env *Env, s LocalServer) (Item, bool) {
	if s.Remote && !env.Online {
		return Item{
			Status: StatusSkip,
			Text:   "Provider " + cmpOr(s.Provider, s.Name) + " uses " + hostOf(s.ListURL) + "; pass --online to check it",
		}, false
	}
	timeout := probeTimeout
	if s.Provider != "" {
		timeout = time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	where := hostOf(s.ListURL)
	status, body, err := env.HTTP(ctx, "GET", s.ListURL)
	if err != nil || status != 200 {
		if s.Provider == "" {
			return Item{Status: StatusSkip, Text: "Nothing answers at " + where + " (" + s.Name + ")"}, false
		}
		return Item{
			Status: StatusWarn,
			Text:   "Provider " + s.Provider + " uses " + where + ", but nothing answers there",
			Hint:   startHint(s),
		}, false
	}
	n, ok := countModels(body, s.Ollama)
	if !ok {
		return Item{Status: StatusWarn, Text: s.Name + " at " + where + " answered with something that isn't a model list", Hint: "Check that the provider's base_url points at the server's API."}, true
	}
	text := fmt.Sprintf("%s at %s has %s", s.Name, where, plural(n, "model", "models"))
	if s.Provider == "" {
		text += " (no provider uses it; see Local Models in the README)"
	}
	if n == 0 {
		hint := "Download a model in " + s.Name + "."
		if s.Ollama {
			hint = "Pull one with `ollama pull <model>`."
		}
		return Item{Status: StatusWarn, Text: text, Hint: hint}, true
	}
	return Item{Status: StatusOK, Text: text}, true
}

func startHint(s LocalServer) string {
	switch {
	case s.Ollama:
		return "Start it with `ollama serve`, or fix the provider's base_url."
	case s.Name == "LM Studio":
		return "Start the local server in LM Studio (Developer tab), or fix the provider's base_url."
	}
	return "Start the server, or fix the provider's base_url."
}

func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	return u.Host
}

// LSPServer is a language server Ta-Da may start.
type LSPServer struct {
	Name    string
	Command string
	// Disabled reports a server turned off in the config.
	Disabled bool
	// Marker is the root marker that made Ta-Da pick an auto-discovered
	// server, or "" for a configured one.
	Marker string
}

// LSPCheck reports whether language server commands are installed.
func LSPCheck(configured, detected []LSPServer) Check {
	return Check{ID: "lsp", Name: "LSP", Run: func(_ context.Context, env *Env) Result {
		var res Result
		ready := 0
		for _, s := range configured {
			switch {
			case s.Disabled:
				res.Items = append(res.Items, Item{Status: StatusSkip, Text: s.Name + ": disabled"})
			case s.Command == "":
				res.Items = append(res.Items, Item{Status: StatusWarn, Text: s.Name + ": no command set", Hint: "Set lsp." + s.Name + ".command in your tadarc."})
			default:
				if path, err := env.LookPath(s.Command); err == nil {
					ready++
					res.Items = append(res.Items, Item{Status: StatusOK, Text: s.Name + ": " + path})
				} else {
					res.Items = append(res.Items, Item{
						Status: StatusWarn,
						Text:   s.Name + ": `" + s.Command + "` isn't on your PATH",
						Hint:   installHint(s.Command, "or fix lsp."+s.Name+".command."),
					})
				}
			}
		}
		for _, s := range detected {
			if path, err := env.LookPath(s.Command); err == nil {
				ready++
				res.Items = append(res.Items, Item{Status: StatusOK, Text: s.Name + ": " + path + " (auto, found " + s.Marker + ")"})
			} else {
				res.Items = append(res.Items, Item{
					Status: StatusSkip,
					Text:   s.Name + " would help with this project (found " + s.Marker + "), but `" + s.Command + "` isn't installed",
					Hint:   installHint(s.Command, "to get code intelligence for it."),
				})
			}
		}
		switch {
		case len(res.Items) == 0:
			res.Status = StatusSkip
			res.Summary = "No language servers configured or detected"
		case Worst(res.Items) == StatusWarn:
			res.Summary = fmt.Sprintf("%d ready, some commands are missing", ready)
		default:
			res.Summary = fmt.Sprintf("%d ready", ready)
			if ready == 0 {
				res.Status = StatusSkip
				res.Summary = "None installed for this project"
			}
		}
		return res
	}}
}

var installHints = map[string]string{
	"gopls":                      "go install golang.org/x/tools/gopls@latest",
	"rust-analyzer":              "rustup component add rust-analyzer",
	"typescript-language-server": "npm install -g typescript typescript-language-server",
	"pyright-langserver":         "npm install -g pyright",
	"clangd":                     "install clangd from your package manager (it ships with LLVM)",
	"zls":                        "install zls from https://github.com/zigtools/zls",
	"lua-language-server":        "install lua-language-server from your package manager",
	"bash-language-server":       "npm install -g bash-language-server",
	"yaml-language-server":       "npm install -g yaml-language-server",
}

// HasInstallHint reports whether doctor knows how to install command, so an
// auto-detected server that isn't installed is worth mentioning.
func HasInstallHint(command string) bool {
	_, ok := installHints[command]
	return ok
}

func installHint(command, rest string) string {
	if how, ok := installHints[command]; ok {
		return "Install it (" + how + ") " + rest
	}
	return "Install `" + command + "` and make sure it's on your PATH, " + rest
}

// MCPServer is a configured MCP server.
type MCPServer struct {
	Name string
	// Type is "stdio", "http" or "sse".
	Type    string
	Command string
	// URL is the resolved URL for http and sse servers.
	URL string
	// Dynamic reports a command or URL computed by the shell, which isn't
	// checked.
	Dynamic  bool
	Disabled bool
}

// MCPCheck reports whether MCP server commands exist and URLs parse.
func MCPCheck(servers []MCPServer, cwd string) Check {
	return Check{ID: "mcp", Name: "MCP", Run: func(ctx context.Context, env *Env) Result {
		if len(servers) == 0 {
			return Result{Status: StatusSkip, Summary: "No MCP servers configured"}
		}
		var res Result
		names := make([]string, 0, len(servers))
		for _, s := range servers {
			names = append(names, s.Name)
			res.Items = append(res.Items, mcpItem(ctx, env, s, cwd))
		}
		res.Summary = strings.Join(names, ", ")
		return res
	}}
}

func mcpItem(ctx context.Context, env *Env, s MCPServer, cwd string) Item {
	switch {
	case s.Disabled:
		return Item{Status: StatusSkip, Text: s.Name + ": disabled"}
	case s.Dynamic:
		return Item{Status: StatusSkip, Text: s.Name + ": computed by a command, not checked"}
	}
	if s.Type == "http" || s.Type == "sse" {
		u, err := url.Parse(s.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return Item{
				Status: StatusWarn,
				Text:   s.Name + ": " + s.Type + " URL " + redactURL(s.URL) + " isn't a valid http(s) URL",
				Hint:   "Set mcp." + s.Name + ".url to a full URL such as https://example.com/mcp.",
			}
		}
		if env.Online {
			it := reach(ctx, env, bareURL(u), u.Host, "Check the server's URL and your network.")
			it.Text = s.Name + ": " + it.Text
			return it
		}
		return Item{Status: StatusOK, Text: s.Name + ": " + s.Type + " " + redactURL(s.URL)}
	}
	if s.Command == "" {
		return Item{Status: StatusWarn, Text: s.Name + ": no command set", Hint: "Set mcp." + s.Name + ".command in your tadarc."}
	}
	if path, ok := findCommand(env, s.Command, cwd, ""); ok {
		return Item{Status: StatusOK, Text: s.Name + ": " + path}
	}
	return Item{
		Status: StatusWarn,
		Text:   s.Name + ": `" + s.Command + "` isn't on your PATH",
		Hint:   "Install it or fix mcp." + s.Name + ".command.",
	}
}

// findCommand resolves name the way a shell would: paths relative to cwd
// (and ~/ to home), bare names on PATH.
func findCommand(env *Env, name, cwd, home string) (string, bool) {
	if strings.HasPrefix(name, "~/") && home != "" {
		name = filepath.Join(home, name[2:])
	}
	if strings.ContainsRune(name, '/') || strings.ContainsRune(name, filepath.Separator) {
		if !filepath.IsAbs(name) {
			name = filepath.Join(cwd, name)
		}
		fi, err := env.Stat(name)
		if err != nil || fi.IsDir() {
			return name, false
		}
		return name, true
	}
	path, err := env.LookPath(name)
	return path, err == nil
}

// redactURL drops credentials and the query from rawURL.
func redactURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "(unparseable)"
	}
	u.User = nil
	if u.RawQuery != "" {
		u.RawQuery = "…"
	}
	return u.String()
}

// PermissionsInfo describes the permission settings.
type PermissionsInfo struct {
	AllowedTools        []string
	ProjectAllowedTools []string
	// AlwaysAsk is nil when unset, which means the default (delete).
	AlwaysAsk  []string
	AutoBudget map[string]int
}

// PermissionsCheck reports settings that let actions run without asking.
func PermissionsCheck(info PermissionsInfo) Check {
	return Check{ID: "permissions", Name: "Permissions", Run: func(context.Context, *Env) Result {
		var res Result
		if len(info.AllowedTools) == 0 {
			res.Items = append(res.Items, Item{Status: StatusOK, Text: "No tools are allowed without asking"})
		} else {
			res.Items = append(res.Items, Item{Status: StatusOK, Text: "Allowed without asking: " + strings.Join(info.AllowedTools, ", ")})
		}
		if slices.Contains(info.AllowedTools, "bash") {
			res.Items = append(res.Items, Item{
				Status: StatusWarn,
				Text:   "bash is in allowed_tools, so any shell command runs without asking",
				Hint:   "Remove bash from permissions.allowed_tools; use Auto mode (ctrl+y) when you want it for a while.",
			})
		}
		if len(info.ProjectAllowedTools) > 0 {
			res.Items = append(res.Items, Item{Status: StatusOK, Text: "Allowed inside the project: " + strings.Join(info.ProjectAllowedTools, ", ")})
		}
		switch {
		case info.AlwaysAsk == nil:
			res.Items = append(res.Items, Item{Status: StatusOK, Text: "Deletes always ask, even in Auto mode (the default)"})
		case len(info.AlwaysAsk) == 0:
			res.Items = append(res.Items, Item{
				Status: StatusWarn,
				Text:   "always_ask is empty, so Auto mode deletes files without asking",
				Hint:   "Remove permissions.always_ask to restore the default (delete).",
			})
		default:
			res.Items = append(res.Items, Item{Status: StatusOK, Text: "Always ask for: " + strings.Join(info.AlwaysAsk, ", ")})
		}
		if len(info.AutoBudget) > 0 {
			keys := make([]string, 0, len(info.AutoBudget))
			for k, v := range info.AutoBudget {
				keys = append(keys, fmt.Sprintf("%s %d", k, v))
			}
			slices.Sort(keys)
			res.Items = append(res.Items, Item{Status: StatusOK, Text: "Auto mode budget per session: " + strings.Join(keys, ", ")})
		}
		res.Items = append(res.Items, Item{Status: StatusOK, Text: "Auto mode is off at start; `tada --auto` or ctrl+y turns it on"})
		res.Summary = "Defaults"
		if len(info.AllowedTools) > 0 || len(info.ProjectAllowedTools) > 0 || info.AlwaysAsk != nil || len(info.AutoBudget) > 0 {
			res.Summary = "Customized"
		}
		if Worst(res.Items) == StatusWarn {
			res.Summary += "; some actions skip the prompt"
		}
		return res
	}}
}

// countModels counts the models in a model list: Ollama's {"models": [...]}
// or an OpenAI-compatible {"data": [...]}.
func countModels(body []byte, ollama bool) (int, bool) {
	var list struct {
		Models *[]json.RawMessage `json:"models"`
		Data   *[]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return 0, false
	}
	models := list.Data
	if ollama && list.Models != nil || models == nil {
		models = list.Models
	}
	if models == nil {
		return 0, false
	}
	return len(*models), true
}
