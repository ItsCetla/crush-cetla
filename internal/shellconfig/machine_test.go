package shellconfig

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMachineAdd(t *testing.T) {
	t.Parallel()

	result := loadScript(t, `machine add m1 --url http://192.168.1.20:11434/v1 --type ollama \
  --model qwen2.5-coder:14b --slots 4 --hardware "M1 Max 64GB" --tag fast --tag code
machine add gpu --url http://192.168.1.30:1234/v1 --slots 2 --disabled true --use named`)

	machines := result["machines"].(map[string]any)
	m1 := machines["m1"].(map[string]any)
	require.Equal(t, "http://192.168.1.20:11434/v1", m1["url"])
	require.Equal(t, "ollama", m1["type"])
	require.Equal(t, "qwen2.5-coder:14b", m1["model"])
	require.EqualValues(t, 4, m1["slots"])
	require.Equal(t, "M1 Max 64GB", m1["hardware"])
	require.Equal(t, []any{"fast", "code"}, m1["tags"])

	gpu := machines["gpu"].(map[string]any)
	require.EqualValues(t, 2, gpu["slots"])
	require.Equal(t, true, gpu["disabled"])
	require.Equal(t, "named", gpu["use"])
}

func TestMachineRemove(t *testing.T) {
	t.Parallel()

	result := loadScript(t, `machine add m1 --url http://a/v1
machine add gpu --url http://b/v1
machine rm m1`)

	machines := result["machines"].(map[string]any)
	require.NotContains(t, machines, "m1")
	require.Contains(t, machines, "gpu")
}

func TestMachineUnknownSubcommand(t *testing.T) {
	t.Parallel()

	path := t.TempDir() + "/tadarc"
	_, err := LoadShellConfig(t.Context(), path, []byte(`machine m1 --url http://a/v1`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown subcommand")
}
