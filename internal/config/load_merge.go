package config

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/shellconfig"
	"github.com/tidwall/gjson"
)

// globalConfigPaths returns the system and user-level config locations, in
// merge order. Only the user config directory contributes a crushrc; the
// data directory is writable machine state and must never be executed as
// Bash. Missing files are skipped when loaded.
func globalConfigPaths() []string {
	return []string{
		systemConfigPath,
		GlobalConfig(),
		shellConfigSibling(GlobalConfig()),
		GlobalConfigData(),
	}
}

// projectConfigNames lists the project config file names, ordered high-to-low
// priority within a directory. LookupBounded returns matches in this order,
// and the later reverse + merge make the earliest listed name win on
// conflict. So: .crushrc beats crushrc, both beat the JSON configs, and
// .crush.json beats crush.json.
var projectConfigNames = []string{
	"." + appName + "rc",
	appName + "rc",
	"." + appName + ".json",
	appName + ".json",
}

// projectWalkConfigs returns the config files found walking up from cwd to
// the project boundary, in merge order (lowest priority first).
func projectWalkConfigs(cwd string) []string {
	found, err := fsext.LookupBounded(cwd, projectBoundary(cwd), projectConfigNames...)
	if err != nil {
		return nil
	}
	// Reverse so the closest config has the highest priority.
	slices.Reverse(found)
	return found
}

// projectConfigPaths returns the project-level config files found by the
// project walk, in merge order, leaving out any that is also a global
// config location (e.g. when Crush runs inside the global data directory).
func projectConfigPaths(cwd string) []string {
	global := make(map[string]bool)
	for _, p := range globalConfigPaths() {
		if p != "" {
			global[normalizeConfigPath(p)] = true
		}
	}
	var out []string
	for _, p := range projectWalkConfigs(cwd) {
		if !global[normalizeConfigPath(p)] {
			out = append(out, p)
		}
	}
	return out
}

// configSource is a config file after reading and, for crushrc, executing.
type configSource struct {
	path  string
	data  []byte
	shell bool
}

// readConfigSources reads every existing config file in paths. Shell configs
// are executed and contribute the JSON they produce.
func readConfigSources(ctx context.Context, paths []string) ([]configSource, error) {
	var sources []configSource
	for _, path := range paths {
		if path == "" {
			continue
		}
		data, err := readFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("failed to open config file %s: %w", path, err)
		}
		if len(data) == 0 {
			continue
		}

		if isShellConfig(path) {
			jsonBytes, err := shellconfig.LoadShellConfig(ctx, path, data)
			if err != nil {
				return nil, fmt.Errorf("failed to load shell config %s: %w", path, err)
			}
			if len(jsonBytes) == 0 {
				continue
			}
			if !json.Valid(jsonBytes) {
				return nil, fmt.Errorf("shell config %s produced invalid JSON", path)
			}
			sources = append(sources, configSource{path: path, data: jsonBytes, shell: true})
			continue
		}
		if !json.Valid(data) {
			return nil, fmt.Errorf("invalid JSON in config file %s", path)
		}
		sources = append(sources, configSource{path: path, data: data})
	}
	return sources, nil
}

// mergeConfigSources merges sources in order (later wins) and returns the
// paths that contributed.
func mergeConfigSources(sources []configSource) (*Config, []string, error) {
	// Track directories that have both crush.json and crushrc to warn
	// about potential confusion, along with the top-level keys each
	// defines so we can report conflicts.
	jsonDirKeys := make(map[string]map[string]bool)
	shDirKeys := make(map[string]map[string]bool)

	configs := make([][]byte, 0, len(sources))
	loaded := make([]string, 0, len(sources))
	for _, src := range sources {
		dir := filepath.Dir(src.path)
		if src.shell {
			addTopLevelKeys(shDirKeys, dir, src.data)
		} else {
			addTopLevelKeys(jsonDirKeys, dir, src.data)
		}
		configs = append(configs, src.data)
		loaded = append(loaded, src.path)
	}

	// Warn if both a JSON config and a crushrc exist in the same directory
	// and define overlapping top-level keys. Disjoint coexistence is
	// intentional and not worth warning about.
	for dir, jKeys := range jsonDirKeys {
		sKeys, ok := shDirKeys[dir]
		if !ok {
			continue
		}
		var conflicts []string
		for k := range jKeys {
			if sKeys[k] {
				conflicts = append(conflicts, k)
			}
		}
		if len(conflicts) > 0 {
			slices.Sort(conflicts)
			slog.Warn("Found both a JSON config and a crushrc in the same directory; merging with crushrc taking precedence",
				"dir", dir, "conflicting_keys", strings.Join(conflicts, ", "))
		}
	}

	cfg, err := loadFromBytes(configs)
	if err != nil {
		return nil, nil, err
	}
	return cfg, loaded, nil
}

func loadFromConfigPaths(ctx context.Context, configPaths []string) (*Config, []string, error) {
	sources, err := readConfigSources(ctx, configPaths)
	if err != nil {
		return nil, nil, err
	}
	return mergeConfigSources(sources)
}

// addTopLevelKeys records the top-level JSON keys present in data into the
// set for dir.
func addTopLevelKeys(m map[string]map[string]bool, dir string, data []byte) {
	keys := m[dir]
	if keys == nil {
		keys = make(map[string]bool)
		m[dir] = keys
	}
	gjson.ParseBytes(data).ForEach(func(key, _ gjson.Result) bool {
		keys[key.String()] = true
		return true
	})
}

// loadParams are the inputs of the shared config load pipeline.
type loadParams struct {
	workingDir string
	// dataDir is passed to setDefaults: the --data-dir override on the
	// first load, the resolved data directory on reloads.
	dataDir string
	// trustDataDir is the --data-dir override as given on the command
	// line. It locates the workspace config for the trust decision, which
	// must not depend on project config.
	trustDataDir string
}

// mergedConfig is the result of the shared config load pipeline.
type mergedConfig struct {
	cfg *Config
	// configPaths lists every discovered config location, loaded or not,
	// for staleness tracking.
	configPaths   []string
	loadedPaths   []string
	workspacePath string
	trust         ProjectTrust
	openCode      *openCodeImport
}

// loadMergedConfig discovers, gates, reads and merges config files: system
// and global config first, then project config (only when the project is
// trusted), then the workspace config with the highest priority (only when
// the trust decision covers it). It is shared by Load and
// reloadFromDiskLocked so both apply the trust gate identically.
func loadMergedConfig(ctx context.Context, p loadParams) (*mergedConfig, error) {
	globalPaths := globalConfigPaths()
	projectPaths := projectConfigPaths(p.workingDir)

	globalSources, err := readConfigSources(ctx, globalPaths)
	if err != nil {
		return nil, err
	}

	// Decide trust before reading any project file, from inputs a project
	// cannot influence.
	globalCfg, _, err := mergeConfigSources(globalSources)
	if err != nil {
		return nil, err
	}
	trust := evaluateProjectTrust(newTrustInput(p.workingDir, p.trustDataDir, globalCfg.Options))

	sources := globalSources
	if trust.Trusted {
		projectSources, err := readConfigSources(ctx, projectPaths)
		if err != nil {
			return nil, err
		}
		sources = slices.Concat(globalSources, projectSources)
	} else {
		slog.Warn("Ignoring untrusted project config; review it, then run `crush trust`",
			"root", trust.Root, "files", trust.DisplayFiles())
	}

	cfg, loadedPaths, err := mergeConfigSources(sources)
	if err != nil {
		return nil, err
	}

	// Import from a local OpenCode install. Its project-level files are
	// part of the trust fingerprint (see openCodeProjectFiles) and are
	// only read for trusted projects. MCP servers go in before setDefaults,
	// which drops partial MCP entries it can't place.
	openCode := loadOpenCode(cfg.Options, p.workingDir, trust.Trusted)
	cfg.applyOpenCodeMCP(openCode)

	cfg.setDefaults(p.workingDir, p.dataDir)

	// Load the workspace config last so it has the highest priority.
	workspacePath := workspaceConfigPath(cfg.Options.DataDirectory)
	if wsData, err := readFile(workspacePath); err == nil && len(wsData) > 0 {
		switch {
		case !trust.Allows(workspacePath):
			if trust.Trusted {
				// Typically a workspace config that project config moved
				// with options.data_directory after the trust decision.
				slog.Warn("Ignoring workspace config not covered by the project trust decision",
					"path", workspacePath)
			}
		case !json.Valid(wsData):
			return nil, fmt.Errorf("invalid JSON in config file %s", workspacePath)
		default:
			merged, mergeErr := loadFromBytes(append([][]byte{mustMarshalConfig(cfg)}, wsData))
			if mergeErr == nil {
				// Preserve defaults that setDefaults already applied.
				dataDir := cfg.Options.DataDirectory
				*cfg = *merged
				cfg.setDefaults(p.workingDir, dataDir)
				loadedPaths = append(loadedPaths, workspacePath)
			}
		}
	}

	// Rules, skills, tool toggles, permissions and commands go in last so
	// they add to the defaults and the workspace config.
	cfg.applyOpenCodeContext(openCode, p.workingDir)

	return &mergedConfig{
		cfg:           cfg,
		configPaths:   slices.Concat(globalPaths, projectPaths, openCode.stalenessPaths()),
		loadedPaths:   loadedPaths,
		workspacePath: workspacePath,
		trust:         trust,
		openCode:      openCode,
	}, nil
}
