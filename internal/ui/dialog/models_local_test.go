package dialog

import (
	"image"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/discover"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/workspace"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// localWorkspace stands in for the real workspace. Saving a provider
// "reloads" by adding it with the models its server would list.
type localWorkspace struct {
	workspace.Workspace
	cfg *config.Config

	mu     sync.Mutex
	saved  map[string]any
	models []catwalk.Model
}

func (w *localWorkspace) Config() *config.Config { return w.cfg }

func (w *localWorkspace) SetConfigFields(_ config.Scope, fields map[string]any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.saved = fields
	for k := range fields {
		rest, ok := strings.CutPrefix(k, "providers.")
		if !ok {
			continue
		}
		id, field, _ := strings.Cut(rest, ".")
		if field != "name" {
			continue
		}
		prefix := "providers." + id + "."
		w.cfg.Providers.Set(id, config.ProviderConfig{
			ID:      id,
			Name:    fields[prefix+"name"].(string),
			Type:    catwalk.Type(fields[prefix+"type"].(string)),
			BaseURL: fields[prefix+"base_url"].(string),
			Models:  w.models,
		})
	}
	return nil
}

func newTestLocalModels(t *testing.T, providers map[string]config.ProviderConfig) (*Models, *localWorkspace) {
	t.Helper()
	s := styles.CharmtonePantera()
	cfg := &config.Config{
		// No default catalog, so nothing is fetched.
		Options:   &config.Options{DisableDefaultProviders: true},
		Providers: csync.NewMapFrom(providers),
	}
	ws := &localWorkspace{cfg: cfg}
	m, err := NewModels(&common.Common{Styles: &s, Workspace: ws}, true)
	require.NoError(t, err)
	return m, ws
}

func testLocalServers() []discover.LocalServer {
	return []discover.LocalServer{
		{
			Kind:         discover.KindOllama,
			Name:         "Ollama",
			Version:      "0.12.3",
			Root:         "http://localhost:11434",
			BaseURL:      "http://localhost:11434/v1",
			ProviderType: "ollama",
			ProviderID:   "ollama",
			Models: []discover.LocalModel{
				{ID: "qwen2.5-coder:7b", ParameterSize: "7.6B"},
				{ID: "llama3.2:3b"},
			},
		},
		{
			Kind:         discover.KindLMStudio,
			Name:         "LM Studio",
			Root:         "http://localhost:1234",
			BaseURL:      "http://localhost:1234/v1",
			ProviderType: "lmstudio",
			ProviderID:   "lmstudio",
			Models:       []discover.LocalModel{{ID: "qwen/qwen3-coder-30b"}},
		},
		{
			// Running, but nothing downloaded yet.
			Kind:         discover.KindLlamaCpp,
			Name:         "llama.cpp",
			Root:         "http://localhost:8080",
			BaseURL:      "http://localhost:8080",
			ProviderType: "llamacpp",
			ProviderID:   "llamacpp",
		},
	}
}

func TestModelsLocalServers(t *testing.T) {
	t.Parallel()

	m, ws := newTestLocalModels(t, map[string]config.ProviderConfig{
		// Same ID as the Ollama server but on another computer, so the
		// local one is still offered under a fresh ID.
		"ollama": {ID: "ollama", Name: "Ollama", Type: "ollama", BaseURL: "http://192.168.1.20:11434/v1", Models: []catwalk.Model{{ID: "big", Name: "big"}}},
		// LM Studio is already set up (spelled 127.0.0.1), so it isn't
		// offered again.
		"studio": {ID: "studio", Name: "Studio", Type: "lmstudio", BaseURL: "http://127.0.0.1:1234/v1", Models: []catwalk.Model{{ID: "x", Name: "x"}}},
	})

	action := m.HandleMsg(localServersMsg{servers: testLocalServers()})
	require.Nil(t, action)
	require.Len(t, m.localPicks, 2)

	// During onboarding the first local model is ready to pick.
	item, ok := m.list.SelectedItem().(*ModelItem)
	require.True(t, ok)
	require.Equal(t, "ollama-2:qwen2.5-coder:7b", item.ID())

	scr := uv.NewScreenBuffer(100, 40)
	m.Draw(scr, image.Rect(0, 0, 100, 40))
	out := ansi.Strip(scr.Render())
	require.Contains(t, out, "Ollama 0.12.3 · running on this computer")
	require.Contains(t, out, "qwen2.5-coder:7b (7.6B)")
	require.NotContains(t, out, "LM Studio")
	require.NotContains(t, out, "llama.cpp")

	// Picking it saves the server as a provider, then selects the model.
	ws.models = []catwalk.Model{{ID: "qwen2.5-coder:7b", Name: "qwen2.5-coder:7b"}}
	action = m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	cmd, ok := action.(ActionCmd)
	require.True(t, ok)
	require.NotNil(t, cmd.Cmd)

	// A second enter while saving does nothing.
	require.Nil(t, m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter}))

	msg := cmd.Cmd()
	require.Equal(t, map[string]any{
		"providers.ollama-2.name":     "Ollama",
		"providers.ollama-2.type":     "ollama",
		"providers.ollama-2.base_url": "http://localhost:11434/v1",
		"providers.ollama-2.disable":  false,
	}, ws.saved)

	action = m.HandleMsg(msg)
	selected, ok := action.(ActionSelectModel)
	require.True(t, ok)
	require.Equal(t, catwalk.InferenceProvider("ollama-2"), selected.Provider.ID)
	require.Equal(t, "ollama-2", selected.Model.Provider)
	require.Equal(t, "qwen2.5-coder:7b", selected.Model.Model)
	require.Equal(t, config.SelectedModelTypeLarge, selected.ModelType)
}

func TestModelsLocalSavedButModelMissing(t *testing.T) {
	t.Parallel()

	m, _ := newTestLocalModels(t, nil)
	m.HandleMsg(localServersMsg{servers: testLocalServers()})

	// The reload didn't list the model, e.g. the server stopped.
	action := m.HandleMsg(localProviderSavedMsg{providerID: "ollama", modelID: "gone"})
	_, ok := action.(ActionCmd)
	require.True(t, ok)
}

func TestModelsNoLocalServers(t *testing.T) {
	t.Parallel()

	m, _ := newTestLocalModels(t, nil)
	require.Nil(t, m.HandleMsg(localServersMsg{}))
	require.Empty(t, m.localPicks)
}

func TestHostPort(t *testing.T) {
	t.Parallel()

	require.Equal(t, "localhost:11434", hostPort("http://127.0.0.1:11434/v1"))
	require.Equal(t, "localhost:11434", hostPort("http://localhost:11434"))
	require.Equal(t, "localhost:11434", hostPort("http://[::1]:11434/v1/"))
	require.Equal(t, "example.com:443", hostPort("https://example.com/v1"))
	require.Empty(t, hostPort("$OLLAMA_URL"))
}

func TestUniqueProviderID(t *testing.T) {
	t.Parallel()

	require.Equal(t, "ollama", uniqueProviderID("ollama", map[string]bool{}))
	require.Equal(t, "ollama-3", uniqueProviderID("ollama", map[string]bool{"ollama": true, "ollama-2": true}))
	require.Equal(t, "local", uniqueProviderID("", map[string]bool{}))
}
