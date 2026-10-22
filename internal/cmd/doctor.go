package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/doctor"
	"github.com/charmbracelet/crush/internal/home"
	"github.com/charmbracelet/crush/internal/update"
	"github.com/charmbracelet/crush/internal/version"
	powernapconfig "github.com/charmbracelet/x/powernap/pkg/config"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
	"github.com/tidwall/gjson"
	"mvdan.cc/sh/v3/syntax"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check your config, providers, tools and environment",
	Long: `Run quick health checks and say what to fix: config files and their
syntax, provider credentials and the selected models, local model servers,
language servers, MCP servers, hooks, permissions, git, the terminal, the
data directory and the session database.

It never prints a secret: credentials only show where they come from. It
contacts nothing but localhost unless you pass --online, which sends one
HEAD request per provider and MCP host (never a chat request, never a key)
and asks GitHub for the latest release.

Exits with status 1 when a check fails.`,
	Example: `
# Check this project's setup
tada doctor

# Also check that provider hosts answer and whether an update is out
tada doctor --online

# Show every finding, not just the ones that need attention
tada doctor -v

# Machine-readable
tada doctor --json
  `,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cwd, err := ResolveCwd(cmd)
		if err != nil {
			return err
		}
		dataDir, _ := cmd.Flags().GetString("data-dir")
		asJSON, _ := cmd.Flags().GetBool("json")
		online, _ := cmd.Flags().GetBool("online")
		verbose, _ := cmd.Flags().GetBool("verbose")

		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		report := runDoctor(ctx, doctorOptions{Cwd: cwd, DataDir: dataDir, Online: online})
		if err := writeDoctor(cmd.OutOrStdout(), report, asJSON, verbose); err != nil {
			return err
		}
		if report.HasFail() {
			os.Exit(1)
		}
		return nil
	},
}

func init() {
	doctorCmd.Flags().Bool("json", false, "Print JSON")
	doctorCmd.Flags().Bool("online", false, "Also check that provider and MCP hosts answer, and look for updates")
	doctorCmd.Flags().BoolP("verbose", "v", false, "Show every finding, not just the ones that need attention")
	rootCmd.AddCommand(doctorCmd)
}

// writeDoctor prints the report: JSON, colors on a terminal, or plain text.
func writeDoctor(w io.Writer, r doctor.Report, asJSON, verbose bool) error {
	if asJSON {
		return doctor.WriteJSON(w, r)
	}
	if f, ok := w.(*os.File); ok && f == os.Stdout && term.IsTerminal(f.Fd()) {
		return doctor.WriteText(colorprofile.NewWriter(f, os.Environ()), r, doctor.ColorStyles(), verbose)
	}
	return doctor.WriteText(w, r, doctor.PlainStyles(), verbose)
}

// doctorOptions are the inputs of a doctor run.
type doctorOptions struct {
	Cwd     string
	DataDir string
	Online  bool
}

// runDoctor gathers what the checks need and runs them. Loading the config
// is what Ta-Da does at startup: it evaluates tadarc files and resolves API
// keys, but doctor itself never resolves a key that runs a command and never
// prints a resolved value.
func runDoctor(ctx context.Context, opts doctorOptions) doctor.Report {
	homeDir := home.Dir()
	wanted := savedModels(config.GlobalConfigData())
	store, records, loadErr := loadConfigQuietly(opts.Cwd, opts.DataDir)

	var cfg *config.Config
	var trust config.ProjectTrust
	if loadErr == nil {
		cfg = store.Config()
		trust = store.ProjectTrust()
	} else if t, err := config.EvaluateProjectTrust(ctx, opts.Cwd, opts.DataDir); err == nil {
		trust = t
	}
	envNames := knownEnvNames(store)
	skipped, findings := sortLoadRecords(records, envNames)

	dataDir := opts.DataDir
	if cfg != nil && cfg.Options != nil && cfg.Options.DataDirectory != "" {
		dataDir = cfg.Options.DataDirectory
	}
	if dataDir == "" {
		dataDir = filepath.Join(opts.Cwd, ".tada")
	}
	dataDir, _ = filepath.Abs(dataDir)

	checks := []doctor.Check{
		doctor.VersionCheck(versionInfo()),
		doctor.ConfigCheck(doctor.ConfigInfo{
			Files:    configFiles(opts.Cwd, dataDir, trust),
			LoadErr:  loadErrText(loadErr),
			Findings: findings,
		}),
	}
	if cfg != nil {
		providers := providerInfos(cfg, store.Resolver(), store.KnownProviders())
		configured, detected := lspServers(cfg, opts.Cwd)
		checks = append(checks,
			doctor.ProvidersCheck(doctor.ProvidersInfo{Providers: providers, Skipped: skipped}),
			doctor.ModelsCheck(modelsInfo(cfg, len(providers) > 0, wanted)),
			doctor.LocalModelsCheck(localServers(providers)),
			doctor.PermissionsCheck(permissionsInfo(cfg)),
			doctor.MCPCheck(mcpServers(cfg), opts.Cwd),
			doctor.LSPCheck(configured, detected),
			doctor.HooksCheck(hookList(cfg), opts.Cwd, homeDir),
		)
	} else {
		checks = append(checks,
			skippedCheck("providers", "Providers"),
			skippedCheck("models", "Models"),
			doctor.LocalModelsCheck(localServers(nil)),
			skippedCheck("permissions", "Permissions"),
			skippedCheck("mcp", "MCP"),
			skippedCheck("lsp", "LSP"),
			skippedCheck("hooks", "Hooks"),
		)
	}
	checks = append(checks,
		doctor.ToolsCheck(),
		doctor.ProjectCheck(opts.Cwd, gitRoot(ctx, opts.Cwd)),
		doctor.TerminalCheck(terminalInfo()),
		doctor.DataDirCheck(dataDirInfo(dataDir)),
		doctor.DatabaseCheck(databaseInfo(ctx, filepath.Join(dataDir, "tada.db"))),
	)

	r := doctor.Run(ctx, doctor.SystemEnv(opts.Online), checks)
	r.Version = version.Display()
	return r
}

func skippedCheck(id, name string) doctor.Check {
	return doctor.Check{ID: id, Name: name, Run: func(context.Context, *doctor.Env) doctor.Result {
		return doctor.Result{Status: doctor.StatusSkip, Summary: "Skipped because the config doesn't load"}
	}}
}

func versionInfo() doctor.VersionInfo {
	info := doctor.VersionInfo{
		Version: version.Display(),
		Commit:  version.Commit,
		Dev:     update.IsDevVersion(version.Version),
	}
	if info.Dev {
		return info
	}
	if v, ok := update.LoadState(updateStatePath()).Available(version.Version); ok {
		info.Cached = v
	}
	info.Latest = func(ctx context.Context) (string, bool, error) {
		res, err := update.New(version.Version).Check(ctx)
		return res.Latest, res.Newer, err
	}
	return info
}

// savedModels reads the models the TUI saved, before loading the config
// replaces one that's no longer offered.
func savedModels(path string) map[config.SelectedModelType]config.SelectedModel {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	out := map[config.SelectedModelType]config.SelectedModel{}
	for _, t := range []config.SelectedModelType{config.SelectedModelTypeLarge, config.SelectedModelTypeSmall} {
		m := gjson.GetBytes(data, "models."+string(t))
		if m.Exists() {
			out[t] = config.SelectedModel{
				Provider: m.Get("provider").String(),
				Model:    m.Get("model").String(),
			}
		}
	}
	return out
}

// loadRecord is one warning logged while the config loaded.
type loadRecord struct {
	Msg   string
	Attrs map[string]string
}

type recordState struct {
	mu   sync.Mutex
	recs []loadRecord
}

// recordHandler keeps the warnings config.Load logs, so doctor can report
// them instead of losing them to the discarded log.
type recordHandler struct {
	state *recordState
	attrs []slog.Attr
}

func (h *recordHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelWarn }

func (h *recordHandler) Handle(_ context.Context, r slog.Record) error {
	rec := loadRecord{Msg: r.Message, Attrs: map[string]string{}}
	for _, a := range h.attrs {
		rec.Attrs[a.Key] = a.Value.String()
	}
	r.Attrs(func(a slog.Attr) bool {
		rec.Attrs[a.Key] = a.Value.String()
		return true
	})
	h.state.mu.Lock()
	h.state.recs = append(h.state.recs, rec)
	h.state.mu.Unlock()
	return nil
}

func (h *recordHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &recordHandler{state: h.state, attrs: slices.Concat(h.attrs, attrs)}
}

func (h *recordHandler) WithGroup(string) slog.Handler { return h }

// loadConfigQuietly loads the config the way Ta-Da does at startup and
// returns the warnings it logged.
func loadConfigQuietly(cwd, dataDir string) (*config.ConfigStore, []loadRecord, error) {
	state := &recordState{}
	prev := slog.Default()
	slog.SetDefault(slog.New(&recordHandler{state: state}))
	defer slog.SetDefault(prev)

	store, err := config.Load(cwd, dataDir, false)
	state.mu.Lock()
	defer state.mu.Unlock()
	return store, slices.Clone(state.recs), err
}

// ignoredLoadWarnings are load warnings that other checks already cover or
// that aren't problems.
var ignoredLoadWarnings = []string{
	"No git repository detected",
	"Detected Apple Terminal",
	"Ignoring untrusted project config",
	"Ignoring workspace config not covered",
	"No providers configured",
	"Using large model as small model",
	"Provider is missing API key, this might be OK for local providers",
	"Default ",
}

var credentialsInURL = regexp.MustCompile(`(://)[^/@\s]+@`)

// redactText drops credentials from URLs in s.
func redactText(s string) string {
	return credentialsInURL.ReplaceAllString(s, "$1")
}

// sortLoadRecords turns load warnings into provider items (providers Ta-Da
// skipped) and config findings (everything else).
func sortLoadRecords(recs []loadRecord, envNames map[string]string) (skipped, findings []doctor.Item) {
	for _, r := range recs {
		if slices.ContainsFunc(ignoredLoadWarnings, func(p string) bool { return strings.HasPrefix(r.Msg, p) }) {
			continue
		}
		p := r.Attrs["provider"]
		warn := func(text, hint string) {
			skipped = append(skipped, doctor.Item{Status: doctor.StatusWarn, Text: text, Hint: hint})
		}
		switch r.Msg {
		case "Skipping provider due to missing API key":
			if env := envNames[p]; env != "" {
				warn(p+": skipped, $"+env+" isn't set", "Set $"+env+", or remove providers."+p+" from your config.")
			} else {
				warn(p+": skipped, no API key", "Set api_key for "+p+" in your tadarc.")
			}
		case "Skipping Vertex AI provider due to missing credentials":
			warn("vertexai: skipped, $VERTEXAI_PROJECT or $VERTEXAI_LOCATION isn't set", "Set both, or remove providers.vertexai from your config.")
		case "Skipping Azure provider due to missing API endpoint":
			warn("azure: skipped, no API endpoint", "Set $AZURE_OPENAI_API_ENDPOINT, or remove providers.azure from your config.")
		case "Skipping Bedrock provider due to missing AWS credentials":
			warn("bedrock: skipped, no AWS credentials", "Set AWS_PROFILE (or AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY), or remove providers.bedrock from your config.")
		case "Skipping custom provider due to unsupported provider type":
			warn(p+": skipped, its type isn't supported", "Set providers."+p+".type to openai-compat, openai or anthropic.")
		case "Skipping custom provider due to missing API endpoint":
			warn(p+": skipped, no base_url", "Set providers."+p+".base_url.")
		case "Model discovery failed":
			warn(p+": couldn't list its models: "+redactText(r.Attrs["error"]), "Check that the server is running and that base_url is right.")
		case "Skipping provider with no models after failed discovery", "Skipping custom provider because the provider has no models":
			warn(p+": skipped, it has no models", "List the models under providers."+p+".models, or check its base_url.")
		case "Continuing with the previously known providers":
			warn("The provider catalog didn't load in full: "+redactText(r.Attrs["error"]), "Run `tada update-providers` to refresh it.")
		default:
			findings = append(findings, doctor.Item{Status: doctor.StatusWarn, Text: describeRecord(r)})
		}
	}
	return skipped, findings
}

func describeRecord(r loadRecord) string {
	text := strings.TrimSuffix(r.Msg, ".")
	if len(r.Attrs) == 0 {
		return text
	}
	keys := make([]string, 0, len(r.Attrs))
	for k := range r.Attrs {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+": "+redactText(r.Attrs[k]))
	}
	return text + " (" + strings.Join(parts, ", ") + ")"
}

// loadErrText is the load error without the list of paths config.Load puts
// in front of it.
func loadErrText(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if _, rest, ok := strings.Cut(msg, "]: "); ok {
		msg = rest
	}
	return redactText(msg)
}

var (
	pureEnvRef = regexp.MustCompile(`^\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?$`)
	envRef     = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)`)
)

// knownEnvNames maps each catalog provider to the variable its key comes
// from.
func knownEnvNames(store *config.ConfigStore) map[string]string {
	out := map[string]string{}
	if store == nil {
		return out
	}
	for _, p := range store.KnownProviders() {
		if m := pureEnvRef.FindStringSubmatch(p.APIKey); m != nil {
			out[string(p.ID)] = m[1]
		}
	}
	return out
}

// isCommandValue reports whether resolving s would run a command.
func isCommandValue(s string) bool {
	return strings.Contains(s, "$(") || strings.Contains(s, "`")
}

// credentialOf says where a provider's credentials come from. It resolves a
// key only when the key is a bare variable reference, so it never runs a
// command, and it returns only the variable's name.
func credentialOf(p config.ProviderConfig, resolve func(string) (string, error)) (doctor.Credential, string) {
	switch {
	case p.OAuthToken != nil:
		return doctor.CredOAuth, ""
	case p.IsClaudeCode():
		return doctor.CredClaudeCode, ""
	}
	key := p.APIKeyTemplate
	if key == "" {
		key = p.APIKey
	}
	envName := ""
	switch m := pureEnvRef.FindStringSubmatch(key); {
	case isCommandValue(key):
		return doctor.CredCommand, ""
	case m != nil:
		if v, err := resolve(key); err == nil && v != "" {
			return doctor.CredEnv, m[1]
		}
		envName = m[1]
	case strings.Contains(key, "$"):
		if m := envRef.FindStringSubmatch(key); m != nil {
			return doctor.CredEnv, m[1]
		}
		return doctor.CredConfig, ""
	case key != "":
		return doctor.CredConfig, ""
	}
	switch {
	case p.Type == catwalk.TypeBedrock || strings.HasPrefix(p.ID, "bedrock"):
		return doctor.CredAWS, ""
	case p.Type == catwalk.TypeVertexAI || p.ID == "vertexai":
		return doctor.CredVertex, ""
	case isLocalProvider(p.ID, p.BaseURL):
		return doctor.CredLocal, ""
	}
	return doctor.CredMissing, envName
}

// localProviderIDs are providers that run on this machine by default.
var localProviderIDs = []string{"ollama", "lmstudio", "llamacpp", "omlx"}

func isLocalProvider(id, baseURL string) bool {
	if slices.Contains(localProviderIDs, id) {
		return true
	}
	u, err := url.Parse(baseURL)
	return err == nil && isLocalHost(u.Hostname())
}

// isLocalHost reports a loopback, private or .local host.
func isLocalHost(host string) bool {
	switch {
	case host == "":
		return false
	case host == "localhost", strings.HasSuffix(host, ".localhost"), strings.HasSuffix(host, ".local"):
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified())
}

func isLoopbackHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// displayURL drops credentials and the query from a base URL, and anything
// that isn't a plain http(s) URL.
func displayURL(raw string) string {
	if raw == "" || strings.Contains(raw, "$") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	return u.String()
}

func providerInfos(cfg *config.Config, resolver config.VariableResolver, known []catwalk.Provider) []doctor.ProviderInfo {
	endpoints := map[string]string{}
	for _, k := range known {
		endpoints[string(k.ID)] = k.APIEndpoint
	}
	resolve := func(s string) (string, error) {
		if resolver == nil {
			return "", errors.New("no resolver")
		}
		return resolver.ResolveValue(s)
	}
	var out []doctor.ProviderInfo
	for id, p := range cfg.Providers.Seq2() {
		if p.Disable {
			continue
		}
		if p.ID == "" {
			p.ID = id
		}
		cred, envName := credentialOf(p, resolve)
		info := doctor.ProviderInfo{
			ID:         p.ID,
			Credential: cred,
			EnvName:    envName,
			BaseURL:    displayURL(p.BaseURL),
		}
		if info.BaseURL == "" {
			info.BaseURL = displayURL(endpoints[p.ID])
		}
		if p.OAuthToken != nil {
			info.OAuthExpired = p.OAuthToken.IsExpired()
		}
		out = append(out, info)
	}
	slices.SortFunc(out, func(a, b doctor.ProviderInfo) int { return strings.Compare(a.ID, b.ID) })
	return out
}

func modelsInfo(cfg *config.Config, hasProviders bool, wanted map[config.SelectedModelType]config.SelectedModel) doctor.ModelsInfo {
	one := func(t config.SelectedModelType) doctor.ModelInfo {
		sel := cfg.Models[t]
		mi := doctor.ModelInfo{Provider: sel.Provider, Model: sel.Model}
		mi.Found = sel.Model != "" && cfg.GetModel(sel.Provider, sel.Model) != nil
		w, ok := wanted[t]
		if ok && w.Model != "" && (w.Provider != sel.Provider || w.Model != sel.Model) &&
			cfg.GetModel(w.Provider, w.Model) == nil {
			mi.FallbackFrom = w.Provider + "/" + w.Model
		}
		return mi
	}
	return doctor.ModelsInfo{
		Providers: hasProviders,
		Large:     one(config.SelectedModelTypeLarge),
		Small:     one(config.SelectedModelTypeSmall),
	}
}

// localServers lists the model servers to ask for their models: the
// configured local providers, plus Ollama and LM Studio at their default
// ports when no provider points there.
func localServers(providers []doctor.ProviderInfo) []doctor.LocalServer {
	var out []doctor.LocalServer
	covered := map[string]bool{}
	for _, p := range providers {
		if p.Credential != doctor.CredLocal || p.BaseURL == "" {
			continue
		}
		u, err := url.Parse(p.BaseURL)
		if err != nil {
			continue
		}
		covered[u.Host] = true
		name := p.ID
		switch p.ID {
		case "ollama":
			name = "Ollama"
		case "lmstudio":
			name = "LM Studio"
		}
		out = append(out, doctor.LocalServer{
			Name:     name,
			ListURL:  strings.TrimRight(p.BaseURL, "/") + "/models",
			Provider: p.ID,
			Remote:   !isLoopbackHost(u.Hostname()),
		})
	}
	defaults := []doctor.LocalServer{
		{Name: "Ollama", ListURL: "http://localhost:11434/api/tags", Ollama: true},
		{Name: "LM Studio", ListURL: "http://localhost:1234/v1/models"},
	}
	for _, d := range defaults {
		u, _ := url.Parse(d.ListURL)
		if covered[u.Host] || covered["127.0.0.1:"+u.Port()] {
			continue
		}
		out = append(out, d)
	}
	return out
}

func permissionsInfo(cfg *config.Config) doctor.PermissionsInfo {
	p := cfg.Permissions
	if p == nil {
		return doctor.PermissionsInfo{}
	}
	return doctor.PermissionsInfo{
		AllowedTools:        p.AllowedTools,
		ProjectAllowedTools: p.ProjectAllowedTools,
		AlwaysAsk:           p.AlwaysAsk,
		AutoBudget:          p.AutoBudget,
	}
}

func mcpServers(cfg *config.Config) []doctor.MCPServer {
	var out []doctor.MCPServer
	for _, m := range cfg.MCP.Sorted() {
		s := doctor.MCPServer{
			Name:     m.Name,
			Type:     string(m.MCP.Type),
			Command:  m.MCP.Command,
			Disabled: m.MCP.Disabled,
		}
		if s.Type == "" {
			s.Type = string(config.MCPStdio)
		}
		switch {
		case isCommandValue(m.MCP.URL) || strings.Contains(m.MCP.Command, "$"):
			s.Dynamic = true
		case strings.Contains(m.MCP.URL, "$"):
			// Only variable references are left; resolving them runs nothing.
			s.Dynamic = true
		default:
			s.URL = m.MCP.URL
		}
		out = append(out, s)
	}
	return out
}

// skipAutoStartCommands mirrors the commands internal/lsp never starts on
// its own because they are too generic.
var skipAutoStartCommands = []string{
	"buck2", "buf", "cue", "dart", "deno", "dotnet", "dprint", "gleam",
	"java", "julia", "koka", "node", "npx", "perl", "plz", "python",
	"python3", "R", "racket", "rome", "rubocop", "ruff", "scarb", "solc",
	"stylua", "swipl", "tflint",
}

// lspServers lists the configured language servers and the ones Ta-Da
// would start on its own for this project. A detected server is listed
// when its command is installed or doctor knows how to install it.
func lspServers(cfg *config.Config, cwd string) (configured, detected []doctor.LSPServer) {
	names := map[string]bool{}
	commands := map[string]bool{}
	for _, l := range cfg.LSP.Sorted() {
		names[l.Name] = true
		commands[l.LSP.Command] = true
		configured = append(configured, doctor.LSPServer{
			Name:     l.Name,
			Command:  l.LSP.Command,
			Disabled: l.LSP.Disabled,
		})
	}
	if cfg.Options != nil && cfg.Options.AutoLSP != nil && !*cfg.Options.AutoLSP {
		return configured, nil
	}
	m := powernapconfig.NewManager()
	if err := m.LoadDefaults(); err != nil {
		return configured, nil
	}
	servers := m.GetServers()
	sorted := make([]string, 0, len(servers))
	for name := range servers {
		sorted = append(sorted, name)
	}
	slices.Sort(sorted)
	for _, name := range sorted {
		s := servers[name]
		if s == nil || names[name] || commands[s.Command] || slices.Contains(skipAutoStartCommands, s.Command) {
			continue
		}
		marker := matchMarker(cwd, s.RootMarkers)
		if marker == "" {
			continue
		}
		if _, err := exec.LookPath(s.Command); err != nil && !doctor.HasInstallHint(s.Command) {
			continue
		}
		commands[s.Command] = true
		detected = append(detected, doctor.LSPServer{Name: name, Command: s.Command, Marker: marker})
	}
	return configured, detected
}

func matchMarker(cwd string, markers []string) string {
	for _, marker := range markers {
		if marker == ".git" {
			continue
		}
		if matches, _ := filepath.Glob(filepath.Join(cwd, marker)); len(matches) > 0 {
			return filepath.Base(matches[0])
		}
	}
	return ""
}

func hookList(cfg *config.Config) []doctor.Hook {
	events := make([]string, 0, len(cfg.Hooks))
	for e := range cfg.Hooks {
		events = append(events, e)
	}
	slices.Sort(events)
	var out []doctor.Hook
	for _, e := range events {
		for _, h := range cfg.Hooks[e] {
			out = append(out, doctor.Hook{Event: e, Name: h.Name, Command: h.Command})
		}
	}
	return out
}

// configFiles lists the config files that exist, with a static syntax
// check: JSON is parsed and tadarc is parsed as shell, never run.
func configFiles(cwd, dataDir string, trust config.ProjectTrust) []doctor.ConfigFile {
	global := config.GlobalConfig()
	globalPaths := map[string]string{
		"/etc/tada/tada.json": "system",
		global:                "global",
		filepath.Join(filepath.Dir(global), "tadarc"): "global",
		config.GlobalConfigData():                     "global",
	}
	workspace := filepath.Join(dataDir, "tada.json")
	paths := append(config.ProjectConfigs(cwd), workspace)

	var out []doctor.ConfigFile
	seen := map[string]bool{}
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		f := doctor.ConfigFile{
			Path: displayPath(path, cwd),
			JSON: strings.HasSuffix(path, ".json"),
		}
		scope, isGlobal := globalPaths[path]
		switch {
		case isGlobal:
			f.Scope = scope
			f.State = path == config.GlobalConfigData()
		case path == workspace:
			f.Scope = "workspace"
			f.State = true
			f.Untrusted = !trust.Allows(path)
		default:
			f.Scope = "project"
			f.Untrusted = !trust.Trusted
		}
		if f.JSON {
			if err := doctor.ValidateJSON(data); err != nil {
				f.Err = err.Error()
			}
		} else if _, err := syntax.NewParser().Parse(bytes.NewReader(data), ""); err != nil {
			f.Err = strings.TrimPrefix(err.Error(), ":")
		}
		out = append(out, f)
	}
	return out
}

// displayPath shortens path relative to cwd or home.
func displayPath(path, cwd string) string {
	if rel, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	if h := home.Dir(); h != "" {
		if rel, err := filepath.Rel(h, path); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.Join("~", rel)
		}
	}
	return path
}

func gitRoot(ctx context.Context, cwd string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	root := strings.TrimSpace(string(out))
	// Compare against cwd with symlinks resolved, as git reports them.
	if real, err := filepath.EvalSymlinks(cwd); err == nil && real == root {
		return cwd
	}
	return root
}

func terminalInfo() doctor.TerminalInfo {
	info := doctor.TerminalInfo{
		TTY:         term.IsTerminal(os.Stdout.Fd()),
		Term:        os.Getenv("TERM"),
		ColorTerm:   os.Getenv("COLORTERM"),
		TermProgram: os.Getenv("TERM_PROGRAM"),
	}
	if info.TTY {
		info.Width, info.Height, _ = term.GetSize(os.Stdout.Fd())
		info.Profile = colorprofile.Detect(os.Stdout, os.Environ()).String()
	}
	return info
}

func dataDirInfo(dir string) doctor.DataDirInfo {
	info := doctor.DataDirInfo{Path: dir, FreeBytes: -1}
	fi, err := os.Stat(dir)
	info.Exists = err == nil && fi.IsDir()

	// Test writing in the directory, or in the nearest parent that exists
	// when Ta-Da hasn't created it yet.
	probe := dir
	for {
		if fi, err := os.Stat(probe); err == nil && fi.IsDir() {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}
	f, err := os.CreateTemp(probe, ".tada-doctor-*")
	if err != nil {
		info.WriteErr = err.Error()
	} else {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}
	info.FreeBytes = doctor.FreeBytes(probe)
	return info
}

func databaseInfo(ctx context.Context, path string) doctor.DatabaseInfo {
	info := doctor.DatabaseInfo{Path: path}
	fi, err := os.Stat(path)
	if err != nil {
		return info
	}
	info.Exists = true
	info.Size = fi.Size()
	conn, err := db.ConnectReadOnly(ctx, path)
	if err != nil {
		info.OpenErr = err.Error()
		return info
	}
	defer conn.Close()
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM sessions").Scan(&info.Sessions); err != nil {
		info.OpenErr = fmt.Sprintf("can't read sessions: %v", err)
	}
	return info
}
