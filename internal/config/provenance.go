package config

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/tidwall/gjson"
)

// FieldSourceKind says which layer of config set a field.
type FieldSourceKind int

// Config layers, lowest priority first.
const (
	// FieldSourceDefault means no config file sets the field.
	FieldSourceDefault FieldSourceKind = iota
	// FieldSourceGlobal is the system or user config (tada.json, tadarc)
	// or the global data config the app writes to.
	FieldSourceGlobal
	// FieldSourceProject is a project config (tadarc or tada.json found
	// walking up from the working directory).
	FieldSourceProject
	// FieldSourceWorkspace is the workspace config in the project data
	// directory (.tada/tada.json).
	FieldSourceWorkspace
)

// FieldSource is the config file that sets a field last, so its value is
// the one in effect (before environment variables).
type FieldSource struct {
	Kind FieldSourceKind
	// Path is the file, or "" for FieldSourceDefault.
	Path string
	// Shell reports whether the file is a tadarc.
	Shell bool
}

// FieldSources reports, for each dotted config key (as passed to
// SetConfigField), which config file sets it last. It reads the same files
// in the same order as Load and applies the same project trust gate:
// untrusted project config is never executed or read. dataDir is the
// resolved project data directory. Keys no file sets are left out of the
// result.
func FieldSources(ctx context.Context, workingDir, dataDir string, keys []string) (map[string]FieldSource, error) {
	globalSources, err := readConfigSources(ctx, globalConfigPaths())
	if err != nil {
		return nil, err
	}
	globalCfg, _, err := mergeConfigSources(globalSources)
	if err != nil {
		return nil, err
	}
	trust := evaluateProjectTrust(newTrustInput(workingDir, dataDir, globalCfg.Options))

	type layer struct {
		configSource
		kind FieldSourceKind
	}
	var layers []layer
	for _, s := range globalSources {
		layers = append(layers, layer{s, FieldSourceGlobal})
	}
	if trust.Trusted {
		projectSources, err := readConfigSources(ctx, projectConfigPaths(workingDir))
		if err != nil {
			return nil, err
		}
		for _, s := range projectSources {
			layers = append(layers, layer{s, FieldSourceProject})
		}
	}
	if dataDir != "" {
		wsPath := workspaceConfigPath(dataDir)
		if data, err := readFile(wsPath); err == nil && len(data) > 0 && trust.Allows(wsPath) && json.Valid(data) {
			layers = append(layers, layer{configSource{path: wsPath, data: data}, FieldSourceWorkspace})
		}
	}

	out := make(map[string]FieldSource, len(keys))
	for _, key := range keys {
		for _, l := range slices.Backward(layers) {
			if gjson.GetBytes(l.data, key).Exists() {
				out[key] = FieldSource{Kind: l.kind, Path: l.path, Shell: l.shell}
				break
			}
		}
	}
	return out, nil
}
