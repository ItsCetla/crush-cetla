package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
)

// EffortLevels are the --effort values the CLI accepts, lowest first.
var EffortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// ModelInfo is one selectable model from the CLI's initialize reply.
type ModelInfo struct {
	// Value is what --model takes ("default" lets the CLI pick).
	Value         string `json:"value"`
	ResolvedModel string `json:"resolvedModel,omitempty"`
	DisplayName   string `json:"displayName,omitempty"`
	Description   string `json:"description,omitempty"`
	// SupportedEffortLevels lists the --effort values the model takes,
	// when SupportsEffort is set.
	SupportsEffort        bool     `json:"supportsEffort,omitempty"`
	SupportedEffortLevels []string `json:"supportedEffortLevels,omitempty"`
	Disabled              bool     `json:"disabled,omitempty"`
}

// Efforts returns the model's --effort levels that Ta-Da knows, in
// EffortLevels order.
func (m ModelInfo) Efforts() []string {
	if !m.SupportsEffort {
		return nil
	}
	var out []string
	for _, l := range EffortLevels {
		if slices.Contains(m.SupportedEffortLevels, l) {
			out = append(out, l)
		}
	}
	return out
}

// initModels pulls the model list out of the initialize control_response.
// The CLI nests it as response.response.models; a bare response.models is
// accepted too. ok is false when the reply carries no list.
func initModels(response json.RawMessage) (models []ModelInfo, ok bool) {
	var outer struct {
		Subtype  string          `json:"subtype"`
		Response json.RawMessage `json:"response"`
		Models   []ModelInfo     `json:"models"`
	}
	if json.Unmarshal(response, &outer) != nil {
		return nil, false
	}
	if outer.Models != nil {
		return selectable(outer.Models), true
	}
	if outer.Subtype != "success" || len(outer.Response) == 0 {
		return nil, false
	}
	var inner struct {
		Models []ModelInfo `json:"models"`
	}
	if json.Unmarshal(outer.Response, &inner) != nil || inner.Models == nil {
		return nil, false
	}
	return selectable(inner.Models), true
}

// selectable drops disabled and unnamed rows.
func selectable(models []ModelInfo) []ModelInfo {
	out := make([]ModelInfo, 0, len(models))
	for _, m := range models {
		if m.Disabled || m.Value == "" {
			continue
		}
		out = append(out, m)
	}
	return out
}

// LoadModels reads a model list saved by SaveModels. It returns nil when
// the file is missing or unreadable.
func LoadModels(path string) []ModelInfo {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var models []ModelInfo
	if json.Unmarshal(b, &models) != nil {
		return nil
	}
	return selectable(models)
}

// SaveModels writes models to path, replacing it atomically.
func SaveModels(path string, models []ModelInfo) error {
	b, err := json.MarshalIndent(models, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".claude-models-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
