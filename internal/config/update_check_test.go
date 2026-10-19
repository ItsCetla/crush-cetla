package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOptionsUpdateCheckEnabled(t *testing.T) {
	on, off := true, false

	t.Setenv("TADA_NO_UPDATE_CHECK", "")
	require.True(t, (*Options)(nil).UpdateCheckEnabled(), "default on")
	require.True(t, (&Options{}).UpdateCheckEnabled(), "default on")
	require.True(t, (&Options{UpdateCheck: &on}).UpdateCheckEnabled())
	require.False(t, (&Options{UpdateCheck: &off}).UpdateCheckEnabled())

	for _, v := range []string{"0", "false", "False"} {
		t.Setenv("TADA_NO_UPDATE_CHECK", v)
		require.True(t, (&Options{}).UpdateCheckEnabled(), v)
	}
	for _, v := range []string{"1", "true", "yes"} {
		t.Setenv("TADA_NO_UPDATE_CHECK", v)
		require.False(t, (&Options{}).UpdateCheckEnabled(), v)
		require.False(t, (&Options{UpdateCheck: &on}).UpdateCheckEnabled(), v)
	}
}
