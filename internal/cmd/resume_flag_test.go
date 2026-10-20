package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLooksLikeSessionID(t *testing.T) {
	t.Parallel()

	require.True(t, looksLikeSessionID("abc1234"))
	require.True(t, looksLikeSessionID("2f4c1d9e-0000-4000-8000-000000000000"))
	require.False(t, looksLikeSessionID("abc123"))
	require.False(t, looksLikeSessionID("auth bug fix"))
	require.False(t, looksLikeSessionID(""))
}

func TestJoinResumeArgs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in, want []string
	}{
		{[]string{"-r"}, []string{"-r"}},
		{[]string{"--resume", "-d"}, []string{"--resume", "-d"}},
		{[]string{"-r", "artifact"}, []string{"--resume=artifact"}},
		{[]string{"--resume", "auth", "bug", "-d"}, []string{"--resume=auth bug", "-d"}},
		{[]string{"--resume=x", "y"}, []string{"--resume=x", "y"}},
		{[]string{"run", "--", "-r", "hi"}, []string{"run", "--", "-r", "hi"}},
		{[]string{"session", "list"}, []string{"session", "list"}},
		{[]string{"run", "explain", "-r", "flag"}, []string{"run", "explain", "-r", "flag"}},
		{[]string{}, []string{}},
	}
	for _, c := range cases {
		require.Equal(t, c.want, joinResumeArgs(c.in), "args %q", c.in)
	}
}

func TestResumeArgsReachRoot(t *testing.T) {
	t.Parallel()

	// "artifact" alone would otherwise resolve to the artifacts command.
	c, rest, err := rootCmd.Find(joinResumeArgs([]string{"--resume", "artifact"}))
	require.NoError(t, err)
	require.Equal(t, rootCmd, c)
	require.Equal(t, []string{"--resume=artifact"}, rest)
}
