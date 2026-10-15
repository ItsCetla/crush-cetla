package shell

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCommandVariants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		args []string
		want [][]string
	}{
		{[]string{"ls", "-la"}, [][]string{{"ls", "-la"}}},
		{
			[]string{"env", "-i", "A=b", "curl", "x"},
			[][]string{{"env", "-i", "A=b", "curl", "x"}, {"curl", "x"}},
		},
		{
			[]string{"nice", "-n", "5", "timeout", "-s", "KILL", "10", "wget", "x"},
			[][]string{
				{"nice", "-n", "5", "timeout", "-s", "KILL", "10", "wget", "x"},
				{"timeout", "-s", "KILL", "10", "wget", "x"},
				{"wget", "x"},
			},
		},
		{
			[]string{"env", "-S", "curl -s x", "y"},
			[][]string{{"env", "-S", "curl -s x", "y"}, {"curl", "-s", "x", "y"}},
		},
		{
			[]string{"sh", "-c", "echo hi | curl x"},
			[][]string{{"sh", "-c", "echo hi | curl x"}, {"echo", "hi"}, {"curl", "x"}},
		},
		{
			[]string{"find", ".", "-exec", "curl", "{}", ";"},
			[][]string{{"find", ".", "-exec", "curl", "{}", ";"}, {"curl", "{}"}},
		},
		{[]string{"command", "-v", "curl"}, [][]string{{"command", "-v", "curl"}}},
		{[]string{"timeout", "5"}, [][]string{{"timeout", "5"}}},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, CommandVariants(tt.args), "%q", tt.args)
	}
}

func TestBlockHandlerChecksWrappedCommands(t *testing.T) {
	t.Parallel()

	blockFuncs := []BlockFunc{
		CommandsBlocker([]string{"curl"}),
		ArgumentsBlocker("go", []string{"test"}, []string{"-exec"}),
	}
	for _, command := range []string{
		"curl https://example.com",
		"/usr/bin/curl https://example.com",
		"env curl https://example.com",
		"nice -n 5 curl https://example.com",
		"timeout 5 curl https://example.com",
		"cmd=curl; env $cmd https://example.com",
		"sh -c 'curl https://example.com'",
		"env go test -exec ./pwn ./...",
	} {
		err := Run(context.Background(), RunOptions{
			Command:    command,
			Cwd:        t.TempDir(),
			BlockFuncs: blockFuncs,
		})
		require.Error(t, err, "%q", command)
		require.Contains(t, err.Error(), "not allowed for security reasons", "%q", command)
	}

	err := Run(context.Background(), RunOptions{
		Command:    "echo curl",
		Cwd:        t.TempDir(),
		BlockFuncs: blockFuncs,
	})
	require.NoError(t, err)
}
