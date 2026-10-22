package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestShellConfigOptionBooleans(t *testing.T) {
	store := loadCrushSh(t, `option debug true
option progress false
option auto-lsp false`)

	opts := store.Config().Options
	require.True(t, opts.Debug, "debug should be on")
	require.NotNil(t, opts.Progress)
	require.False(t, *opts.Progress, "progress should be off")
	require.NotNil(t, opts.AutoLSP)
	require.False(t, *opts.AutoLSP, "auto-lsp should be off")
}

// Config phrases this field negatively (disable_auto_summarize) but the
// command exposes it positively. "auto-summarize false" must land as
// disable_auto_summarize = true.
func TestShellConfigOptionPositiveAutoSummarizeFalse(t *testing.T) {
	store := loadCrushSh(t, `option auto-summarize false`)
	require.True(t, store.Config().Options.DisableAutoSummarize, "auto-summarize off => disable_auto_summarize true")
}

// The bare positive form defaults to true, which inverts to disable = false.
func TestShellConfigOptionPositiveAutoSummarizeBare(t *testing.T) {
	store := loadCrushSh(t, `option auto-summarize`)
	require.False(t, store.Config().Options.DisableAutoSummarize, "auto-summarize on => disable_auto_summarize false")
}

// The removed telemetry toggle must still load, as a no-op.
func TestShellConfigOptionObsoleteMetrics(t *testing.T) {
	store := loadCrushSh(t, `option debug
option metrics false`)
	require.True(t, store.Config().Options.Debug)
}

func TestShellConfigOptionUI(t *testing.T) {
	store := loadCrushSh(t, `option ui compact true
option ui diff split
option ui transparent false
option ui mouse false
option ui scrollbar always
option ui completions-max-depth 4
option ui completions-max-items 200`)

	ui := store.Config().Options.TUI
	require.NotNil(t, ui)
	require.True(t, ui.CompactMode)
	require.Equal(t, "split", ui.DiffMode)
	require.NotNil(t, ui.Transparent)
	require.False(t, *ui.Transparent)
	require.NotNil(t, ui.Mouse)
	require.False(t, *ui.Mouse)
	require.Equal(t, "always", ui.Scrollbar)
	require.NotNil(t, ui.Completions.MaxDepth)
	require.Equal(t, 4, *ui.Completions.MaxDepth)
	require.NotNil(t, ui.Completions.MaxItems)
	require.Equal(t, 200, *ui.Completions.MaxItems)
}

func TestShellConfigOptionUIRejectsInvalidValue(t *testing.T) {
	_, err := loadCrushShErr(t, `option ui diff side-by-side`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expects auto, unified, or split")
}

func TestShellConfigOptionAttribution(t *testing.T) {
	store := loadCrushSh(t, `option attribution-trailer-style none
option attribution-generated-with false`)

	attribution := store.Config().Options.Attribution
	require.NotNil(t, attribution)
	require.Equal(t, "none", string(attribution.TrailerStyle))
	require.False(t, attribution.GeneratedWith)
}

func TestShellConfigOptionAttributionTrailerStylePreservesGeneratedWithDefault(t *testing.T) {
	store := loadCrushSh(t, `option attribution-trailer-style co-authored-by`)

	attribution := store.Config().Options.Attribution
	require.NotNil(t, attribution)
	require.Equal(t, "co-authored-by", string(attribution.TrailerStyle))
	require.True(t, attribution.GeneratedWith)
}

func TestShellConfigOptionAttributionGeneratedWithCaseInsensitive(t *testing.T) {
	store := loadCrushSh(t, `option attribution-generated-with YES`)

	require.NotNil(t, store.Config().Options.Attribution)
	require.True(t, store.Config().Options.Attribution.GeneratedWith)
}

func TestShellConfigOptionAttributionRejectsInvalidStyle(t *testing.T) {
	_, err := loadCrushShErr(t, `option attribution-trailer-style bogus`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expects none, co-authored-by, or assisted-by")
}

func TestShellConfigOptionListAppends(t *testing.T) {
	store := loadCrushSh(t, `option disable-skill tada-config
option disable-skill jq`)

	require.Subset(t, store.Config().Options.DisabledSkills, []string{"tada-config", "jq"})
}

// reset wipes values added earlier (or via source) while keeping anything
// added after it — observable in the effective config.
func TestShellConfigOptionReset(t *testing.T) {
	store := loadCrushSh(t, `option skill-path ./inherited-a
option skill-path ./inherited-b
option reset skill-path
option skill-path ./mine`)

	paths := store.Config().Options.SkillsPaths
	require.Contains(t, paths, "./mine")
	require.NotContains(t, paths, "./inherited-a")
	require.NotContains(t, paths, "./inherited-b")
}

func TestShellConfigOptionUnknownKeyFails(t *testing.T) {
	_, err := loadCrushShErr(t, `option bogus-key value`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown key")
}

func TestShellConfigOptionDisableToolRemoved(t *testing.T) {
	_, err := loadCrushShErr(t, `option disable-tool bash`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown key")
}

func TestShellConfigOptionResetRejectsNonList(t *testing.T) {
	_, err := loadCrushShErr(t, `option reset debug`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not one")
}
