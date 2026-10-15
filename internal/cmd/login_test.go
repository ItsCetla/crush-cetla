package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoginCmd_Aliases(t *testing.T) {
	t.Parallel()

	require.Equal(t, "auth", loginCmd.Aliases[0])
}

func TestLoginCmd_ForceFlag(t *testing.T) {
	t.Parallel()

	flag := loginCmd.Flags().Lookup("force")
	require.NotNil(t, flag)
	require.Equal(t, "f", flag.Shorthand)
}

func TestLoginCmd_ValidArgs(t *testing.T) {
	t.Parallel()

	validPlatforms := map[string]bool{}
	for _, p := range loginCmd.ValidArgs {
		validPlatforms[p] = true
	}
	require.False(t, validPlatforms["hyper"], "Charm Hyper is not a supported platform")
	require.True(t, validPlatforms["copilot"])
	require.True(t, validPlatforms["openai"])
	require.True(t, validPlatforms["chatgpt"])
	require.True(t, validPlatforms["grok"])
}

func TestLoginCmd_PickerOffersSupportedPlatforms(t *testing.T) {
	t.Parallel()

	var ids []string
	for _, p := range loginPlatforms {
		ids = append(ids, p.id)
	}
	require.Equal(t, []string{"copilot", "openai", "grok"}, ids)
}

func TestLoginCmd_RejectsUnknownPlatform(t *testing.T) {
	t.Parallel()

	for _, platform := range []string{"hyper", "nope"} {
		err := loginCmd.RunE(loginCmd, []string{platform})
		require.ErrorContains(t, err, "unknown platform: "+platform)
		require.ErrorContains(t, err, "available platforms: copilot, openai, grok")
	}
}
