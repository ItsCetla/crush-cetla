package common

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplitDiff(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		mode  string
		width int
		want  bool
	}{
		{"split", 40, true},
		{"split", 300, true},
		{"unified", 40, false},
		{"unified", 300, false},
		{"auto", DiffSplitMinWidth - 1, false},
		{"auto", DiffSplitMinWidth, true},
		{"", DiffSplitMinWidth - 1, false},
		{"", DiffSplitMinWidth, true},
		{"bogus", 300, true},
	} {
		require.Equal(t, tc.want, SplitDiff(tc.mode, tc.width), "mode %q width %d", tc.mode, tc.width)
	}
}
