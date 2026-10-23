package dialog

import (
	"image"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/fleet"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// newTestMachinesTab builds a models dialog outside onboarding with one
// machine, tabs over to the Machines tab and returns it.
func newTestMachinesTab(t *testing.T) (*Models, *config.Config) {
	t.Helper()
	s := styles.CharmtonePantera()
	cfg := &config.Config{
		Options: &config.Options{DisableDefaultProviders: true},
		Machines: config.Machines{
			"pc": {URL: "http://192.168.1.30:8080/v1", Model: "bonsai", Hardware: "RTX 3060 Ti", Slots: 2},
		},
		Providers: csync.NewMapFrom(map[string]config.ProviderConfig{
			"pc": {
				ID: "pc", Name: "pc", Type: catwalk.TypeOpenAICompat,
				BaseURL: "http://192.168.1.30:8080/v1",
				Models:  []catwalk.Model{{ID: "bonsai", Name: "bonsai"}},
			},
		}),
	}
	m, err := NewModels(&common.Common{Styles: &s, Workspace: &localWorkspace{cfg: cfg}}, false)
	require.NoError(t, err)

	// Large → Small → Machines.
	tab := tea.KeyPressMsg{Code: tea.KeyTab}
	require.Nil(t, m.HandleMsg(tab))
	require.Equal(t, ModelTypeSmall, m.modelType)
	require.Equal(t, ActionCheckMachines{}, m.HandleMsg(tab))
	require.Equal(t, ModelTypeMachines, m.modelType)
	return m, cfg
}

func drawModels(m *Models) string {
	scr := uv.NewScreenBuffer(100, 30)
	m.Draw(scr, image.Rect(0, 0, 100, 30))
	return ansi.Strip(scr.Render())
}

func selectedRowID(t *testing.T, m *Models) string {
	t.Helper()
	item, ok := m.list.SelectedItem().(*ModelItem)
	require.True(t, ok)
	return item.ID()
}

func TestModelsMachinesTabLists(t *testing.T) {
	t.Parallel()

	m, cfg := newTestMachinesTab(t)
	out := drawModels(m)
	require.Contains(t, out, "Your machines")
	require.Contains(t, out, "this computer")
	require.Contains(t, out, "no main AI chosen yet")
	require.Contains(t, out, "pc (RTX 3060 Ti)")
	require.Contains(t, out, "auto · checking…")
	require.Contains(t, out, "Add or edit machines…")
	require.Equal(t, machineRowThis, selectedRowID(t, m))

	// A finished check shows whether the machine answers.
	pc := fleet.Machines(cfg)[0]
	m.SetMachineStatuses([]fleet.Status{{Machine: pc, Online: true, Busy: 1}}, time.Now())
	require.Contains(t, drawModels(m), "auto · online, 1 free")

	// Shift+tab goes back to the Small tab and tab returns.
	require.Nil(t, m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}))
	require.Equal(t, ModelTypeSmall, m.modelType)
	// The check already ran, so entering again doesn't ask for another.
	require.Nil(t, m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab}))
	require.Equal(t, ModelTypeMachines, m.modelType)
}

func TestModelsMachinesPicker(t *testing.T) {
	t.Parallel()

	m, _ := newTestMachinesTab(t)
	require.Nil(t, m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown}))
	require.Equal(t, "machine:pc", selectedRowID(t, m))

	// Enter opens the picker on the current choice.
	require.Nil(t, m.HandleMsg(enter()))
	out := drawModels(m)
	require.Contains(t, out, "How should pc be used?")
	require.Contains(t, out, "Main AI: run Ta-Da on pc")
	require.Contains(t, out, "Helper, picked automatically when it has a free slot")
	require.Contains(t, out, "Helper, only when asked for by name")
	require.Contains(t, out, "Off: keep it configured but don't use it")
	require.Contains(t, out, "Role and details…")
	require.Equal(t, "pick:pc:auto", selectedRowID(t, m))

	// Choosing "by name" saves that use and switches the machine on.
	require.Nil(t, m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown}))
	action := m.HandleMsg(enter())
	save, ok := action.(ActionSaveMachine)
	require.True(t, ok)
	require.Equal(t, "update", save.Params.Action)
	require.Equal(t, "pc", save.Params.Name)
	require.Equal(t, config.MachineUseNamed, save.Params.Use)
	require.NotNil(t, save.Params.Disabled)
	require.False(t, *save.Params.Disabled)

	// A second enter while saving does nothing; success closes the picker.
	require.Nil(t, m.HandleMsg(enter()))
	m.SetMachinesResult("Updated pc.", nil)
	require.Contains(t, drawModels(m), "Your machines")
	require.Equal(t, "machine:pc", selectedRowID(t, m))

	// Off keeps the machine but disables it.
	require.Nil(t, m.HandleMsg(enter()))
	m.list.SetSelectedItem("pick:pc:off")
	save, ok = m.HandleMsg(enter()).(ActionSaveMachine)
	require.True(t, ok)
	require.True(t, *save.Params.Disabled)
	require.Empty(t, save.Params.Use)
	m.SetMachinesResult("", nil)

	// Main AI picks the machine's model as the large model.
	require.Nil(t, m.HandleMsg(enter()))
	m.list.SetSelectedItem("pick:pc:main")
	selected, ok := m.HandleMsg(enter()).(ActionSelectModel)
	require.True(t, ok)
	require.Equal(t, catwalk.InferenceProvider("pc"), selected.Provider.ID)
	require.Equal(t, "pc", selected.Model.Provider)
	require.Equal(t, "bonsai", selected.Model.Model)
	require.Equal(t, config.SelectedModelTypeLarge, selected.ModelType)

	// Details opens the machines screen on that machine.
	m.list.SetSelectedItem("pick:pc:details")
	require.Equal(t, ActionOpenMachines{Machine: "pc"}, m.HandleMsg(enter()))

	// Esc goes back to the list; a second esc closes the dialog.
	require.Nil(t, m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape}))
	require.Equal(t, "machine:pc", selectedRowID(t, m))
	require.Equal(t, ActionClose{}, m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape}))
}

func TestModelsMachinesRows(t *testing.T) {
	t.Parallel()

	m, _ := newTestMachinesTab(t)

	// ctrl+e on a machine opens its details.
	m.list.SetSelectedItem("machine:pc")
	require.Equal(t, ActionOpenMachines{Machine: "pc"}, m.HandleMsg(tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl}))

	// The add row opens the machines screen.
	m.list.SetSelectedItem(machineRowAdd)
	require.Equal(t, ActionOpenMachines{}, m.HandleMsg(enter()))

	// This computer's main AI is chosen on the Large tab.
	m.list.SetSelectedItem(machineRowThis)
	require.Nil(t, m.HandleMsg(enter()))
	require.Equal(t, ModelTypeLarge, m.modelType)
}

func TestModelsMachinesShowsMainMachine(t *testing.T) {
	t.Parallel()

	m, cfg := newTestMachinesTab(t)
	cfg.Models = map[config.SelectedModelType]config.SelectedModel{
		config.SelectedModelTypeLarge: {Provider: "pc", Model: "bonsai"},
	}
	cfg.Machines["pc"] = config.MachineConfig{
		URL: "http://192.168.1.30:8080/v1", Model: "bonsai", Use: config.MachineUseNamed,
	}
	m.setMachineItems()
	out := drawModels(m)
	require.Contains(t, out, "main AI runs on pc")
	require.Contains(t, out, "main AI, by name")

	// The main machine can't be switched off from here.
	m.list.SetSelectedItem("machine:pc")
	require.Nil(t, m.HandleMsg(enter()))
	require.Equal(t, "pick:pc:main", selectedRowID(t, m))
	require.NotContains(t, drawModels(m), "Off: keep it configured")
}

func TestMachines_SelectMachine(t *testing.T) {
	t.Parallel()

	d := newTestMachines(t)
	d.SelectMachine("pc")
	d.SetStatuses(testStatuses(), time.Now())
	require.Equal(t, 1, d.cursor)

	// Once loaded, the cursor moves right away.
	d.SelectMachine("m1")
	require.Equal(t, 0, d.cursor)
	d.SelectMachine("")
	require.Equal(t, 0, d.cursor)
}
