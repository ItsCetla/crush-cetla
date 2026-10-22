package cmd

import (
	"bytes"
	"testing"

	"github.com/charmbracelet/crush/internal/discover"
	"github.com/stretchr/testify/require"
)

func TestPrintLocalServers(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	printLocalServers(&buf, []discover.LocalServer{
		{
			Kind:         discover.KindOllama,
			Name:         "Ollama",
			Version:      "0.12.3",
			Root:         "http://localhost:11434",
			BaseURL:      "http://localhost:11434/v1",
			ProviderType: "ollama",
			ProviderID:   "ollama",
		},
		{
			Kind:         discover.KindLMStudio,
			Name:         "LM Studio",
			Root:         "http://localhost:1234",
			BaseURL:      "http://localhost:1234/v1",
			ProviderType: "lmstudio",
			ProviderID:   "lmstudio",
			Models:       []discover.LocalModel{{ID: "qwen/qwen3-coder-30b", ParameterSize: "30B"}},
		},
	})
	out := buf.String()
	require.Contains(t, out, "Ollama 0.12.3 at http://localhost:11434")
	require.Contains(t, out, "ollama pull")
	require.Contains(t, out, "qwen/qwen3-coder-30b (30B)")
	require.Contains(t, out, `provider add lmstudio --name "LM Studio" --type lmstudio --base-url "http://localhost:1234/v1"`)

	buf.Reset()
	printLocalServers(&buf, nil)
	require.Contains(t, buf.String(), "No local model servers found")
}
