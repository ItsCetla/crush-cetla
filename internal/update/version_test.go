package update

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompareVersions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		a, b string
		want int
	}{
		{"0.3.0", "0.2.9", 1},
		{"0.2.9", "0.3.0", -1},
		{"1.0.0", "1.0.0", 0},
		{"v1.0.0", "1.0.0", 0},
		{"1.0.0", "v1.0.0", 0},
		{"v1.2.0", "v1.10.0", -1},
		{"1.0", "1.0.0", 0},
		{"1.0.1", "1.0", 1},
		{"1.0.0", "1.0.0-rc.1", 1},
		{"1.0.0-rc.1", "1.0.0", -1},
		{"v1.0.0", "1.0.0-rc.1", 1},
		{"1.0.0-rc.2", "1.0.0-rc.10", -1},
		{"1.0.0-beta", "1.0.0-alpha", 1},
		{"1.0.0-alpha.1", "1.0.0-alpha", 1},
		{"1.0.0-alpha", "1.0.0-1", 1},
		{"1.1.0-rc.1", "1.0.0", 1},
	}
	for _, tt := range tests {
		t.Run(tt.a+"_vs_"+tt.b, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, CompareVersions(tt.a, tt.b))
		})
	}
}

func TestValidVersion(t *testing.T) {
	t.Parallel()
	require.True(t, ValidVersion("1.2.3"))
	require.True(t, ValidVersion("v0.3.0-rc.1"))
	require.False(t, ValidVersion(""))
	require.False(t, ValidVersion("devel"))
	require.False(t, ValidVersion("nightly"))
}

func TestIsDevVersion(t *testing.T) {
	t.Parallel()
	dev := []string{
		"", "devel", "(devel)", "unknown", "nightly",
		"0.1.0-dev.abc1234", "0.0.0-1760000000",
		"v0.0.0-20261019120000-abcdef123456",
		"0.2.0-0.20261019120000-abcdef123456+dirty",
	}
	for _, v := range dev {
		require.True(t, IsDevVersion(v), v)
	}
	release := []string{"0.1.0", "v0.3.0", "1.0.0-rc.1", "2.4.1-beta.2"}
	for _, v := range release {
		require.False(t, IsDevVersion(v), v)
	}
}
