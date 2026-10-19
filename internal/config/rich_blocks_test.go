package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRichBlocksEnabled(t *testing.T) {
	off, on := false, true
	cases := []struct {
		name string
		opts *TUIOptions
		env  string
		want bool
	}{
		{"nil options", nil, "", true},
		{"unset", &TUIOptions{}, "", true},
		{"explicit on", &TUIOptions{RichBlocks: &on}, "", true},
		{"explicit off", &TUIOptions{RichBlocks: &off}, "", false},
		{"env 1", &TUIOptions{}, "1", false},
		{"env true", &TUIOptions{RichBlocks: &on}, "true", false},
		{"env 0", &TUIOptions{}, "0", true},
		{"env false", &TUIOptions{}, "FALSE", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TADA_NO_RICH", tc.env)
			require.Equal(t, tc.want, tc.opts.RichBlocksEnabled())
		})
	}
}
