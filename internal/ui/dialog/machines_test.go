package dialog

import (
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/fleet"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func newTestMachines(t *testing.T) *Machines {
	t.Helper()
	s := styles.CharmtonePantera()
	d, act := NewMachines(&common.Common{Styles: &s})
	require.Equal(t, ActionCheckMachines{}, act)
	d.width, d.height = 84, 18
	return d
}

func testStatuses() []fleet.Status {
	return []fleet.Status{
		{
			Machine: fleet.Machine{Name: "m1", Model: "qwen3:8b", MachineConfig: config.MachineConfig{
				URL: "http://192.168.1.20:11434/v1", Type: "ollama", Hardware: "M1 Max", Slots: 4,
			}},
			Busy:   1,
			Online: true,
		},
		{
			Machine: fleet.Machine{Name: "pc", MachineConfig: config.MachineConfig{
				URL: "http://192.168.1.30:11434/v1", Slots: 2,
			}},
			Error: "connection refused",
		},
	}
}

func typeText(d *Machines, s string) {
	for _, r := range s {
		d.HandleMsg(keyMsg(r))
	}
}

func enter() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyEnter} }

func TestMachinesQuery(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"/machines", "/machine", " /FLEET "} {
		require.True(t, MachinesQuery(v), v)
	}
	for _, v := range []string{"machines", "/machines m1", "/resume"} {
		require.False(t, MachinesQuery(v), v)
	}
}

func TestMachineURL(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, url, typ string }{
		{"192.168.1.20", "http://192.168.1.20:11434/v1", "ollama"},
		{"192.168.1.20:11434", "http://192.168.1.20:11434/v1", "ollama"},
		{"http://192.168.1.20:11434/v1/", "http://192.168.1.20:11434/v1", "ollama"},
		{"10.0.0.5:1234", "http://10.0.0.5:1234/v1", "lmstudio"},
		{"https://box.local:8080/v1", "https://box.local:8080/v1", ""},
		{"box.local:8080", "http://box.local:8080", ""},
	}
	for _, c := range cases {
		u, typ, err := MachineURL(c.in)
		require.NoError(t, err, c.in)
		require.Equal(t, c.url, u, c.in)
		require.Equal(t, c.typ, typ, c.in)
	}
	_, _, err := MachineURL("ftp://box")
	require.Error(t, err)
}

func TestMachines_AddForm(t *testing.T) {
	t.Parallel()
	d := newTestMachines(t)
	d.SetStatuses(nil, time.Now())

	d.HandleMsg(keyMsg('a'))
	require.True(t, d.form)
	typeText(d, "m2")
	d.HandleMsg(enter())
	typeText(d, "192.168.1.40")
	d.HandleMsg(enter())
	typeText(d, "3")
	d.HandleMsg(enter())
	typeText(d, "RTX 4090")
	d.HandleMsg(enter())
	d.HandleMsg(enter()) // model: keep the default
	typeText(d, "code review and tests")
	act := d.HandleMsg(enter())

	require.Equal(t, ActionSaveMachine{Params: fleet.Params{
		Action:   "add",
		Name:     "m2",
		URL:      "http://192.168.1.40:11434/v1",
		Type:     "ollama",
		Slots:    3,
		Hardware: "RTX 4090",
		Role:     "code review and tests",
	}}, act)

	// A failed save keeps the form open with the error.
	d.SetResult("", errors.New("machine m2 already exists"))
	require.True(t, d.form)
	require.Contains(t, ansi.Strip(d.content(time.Now())), "machine m2 already exists")

	d.SetResult("Saved m2. m2 (RTX 4090): 3 slots.", nil)
	require.False(t, d.form)
}

func TestMachines_FormNeedsAddress(t *testing.T) {
	t.Parallel()
	d := newTestMachines(t)
	d.HandleMsg(keyMsg('a'))
	typeText(d, "m2")
	d.HandleMsg(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	require.Nil(t, d.HandleMsg(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}))
	require.Contains(t, d.err, "address")
}

func TestMachines_EditKeepsName(t *testing.T) {
	t.Parallel()
	d := newTestMachines(t)
	d.SetStatuses(testStatuses(), time.Now())

	d.HandleMsg(keyMsg('e'))
	require.Equal(t, "m1", d.editing)
	require.Equal(t, machineFieldURL, d.field)
	act := d.HandleMsg(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	save, ok := act.(ActionSaveMachine)
	require.True(t, ok)
	require.Equal(t, "update", save.Params.Action)
	require.Equal(t, "m1", save.Params.Name)
	require.Equal(t, 4, save.Params.Slots)
	require.Empty(t, save.Params.Type)

	// Esc leaves the form without saving.
	d.saving = false
	d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.False(t, d.form)
}

func TestMachines_ToggleAndRemove(t *testing.T) {
	t.Parallel()
	d := newTestMachines(t)
	d.SetStatuses(testStatuses(), time.Now())

	act := d.HandleMsg(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	save, ok := act.(ActionSaveMachine)
	require.True(t, ok)
	require.Equal(t, "update", save.Params.Action)
	require.NotNil(t, save.Params.Disabled)
	require.True(t, *save.Params.Disabled)
	d.SetResult("Saved m1.", nil)

	d.HandleMsg(keyMsg('j'))
	require.Nil(t, d.HandleMsg(keyMsg('x')))
	require.Contains(t, d.notice, "Press x again to remove pc")
	require.Equal(t, ActionSaveMachine{Params: fleet.Params{Action: "remove", Name: "pc"}}, d.HandleMsg(keyMsg('x')))
}

func TestMachines_RemoveNeedsSecondPress(t *testing.T) {
	t.Parallel()
	d := newTestMachines(t)
	d.SetStatuses(testStatuses(), time.Now())
	d.HandleMsg(keyMsg('x'))
	d.HandleMsg(keyMsg('j'))
	// Moving away disarms it, so the next x only arms pc.
	require.Nil(t, d.HandleMsg(keyMsg('x')))
}

func TestMachines_Render(t *testing.T) {
	t.Parallel()
	d := newTestMachines(t)
	now := time.Now()

	require.Contains(t, ansi.Strip(d.content(now)), "Checking your machines")

	d.SetStatuses(nil, now)
	empty := ansi.Strip(d.content(now))
	require.Contains(t, empty, "No machines yet")
	require.Contains(t, empty, "OLLAMA_HOST=0.0.0.0")

	d.SetStatuses(testStatuses(), now)
	out := ansi.Strip(d.content(now))
	require.Contains(t, out, "1 online · 3 of 4 slots free")
	require.Contains(t, out, "m1")
	require.Contains(t, out, "3 of 4 free")
	require.Contains(t, out, "offline")
	require.Contains(t, out, "http://192.168.1.20:11434/v1")
	for _, l := range splitLines(out) {
		require.LessOrEqual(t, ansi.StringWidth(l), 84, l)
	}
}

func TestMachines_AddFormSavesRole(t *testing.T) {
	t.Parallel()
	d := newTestMachines(t)
	d.HandleMsg(keyMsg('a'))
	typeText(d, "m3")
	d.HandleMsg(enter())
	typeText(d, "192.168.1.50")
	for range 4 {
		d.HandleMsg(enter())
	}
	typeText(d, "long file reads and summaries")
	act := d.HandleMsg(enter())
	save, ok := act.(ActionSaveMachine)
	require.True(t, ok, "expected ActionSaveMachine, got %T", act)
	require.Equal(t, "long file reads and summaries", save.Params.Role)
}

func TestMachines_SuggestAndApply(t *testing.T) {
	t.Parallel()
	d := newTestMachines(t)
	d.SetStatuses(testStatuses(), time.Now())

	act := d.HandleMsg(keyMsg('s'))
	sug, ok := act.(ActionSuggestRoles)
	require.True(t, ok, "expected ActionSuggestRoles, got %T", act)
	require.Len(t, sug.Statuses, len(testStatuses()))
	require.Nil(t, d.HandleMsg(keyMsg('s')), "a second press while asking does nothing")

	d.SetSuggestions([]fleet.Suggestion{
		{Name: "m1", Role: "code review and tests", Reason: "coder model"},
		{Name: "pc", Role: "quick lookups and drafts", Reason: "small"},
	}, true, nil)
	require.Contains(t, d.notice, "main AI")

	act = d.HandleMsg(keyMsg('y'))
	save, ok := act.(ActionSaveMachine)
	require.True(t, ok, "expected ActionSaveMachine, got %T", act)
	require.Equal(t, "update", save.Params.Action)
	require.Equal(t, "m1", save.Params.Name)
	require.Equal(t, "code review and tests", save.Params.Role)
	d.SetResult("ok", nil)

	act = d.HandleMsg(keyMsg('Y'))
	all, ok := act.(ActionApplyRoles)
	require.True(t, ok, "expected ActionApplyRoles, got %T", act)
	require.Len(t, all.Params, 2)
}

func TestMachines_ApplyWithoutSuggestion(t *testing.T) {
	t.Parallel()
	d := newTestMachines(t)
	d.SetStatuses(testStatuses(), time.Now())
	require.Nil(t, d.HandleMsg(keyMsg('y')))
	require.Contains(t, d.notice, "press s")
}

func TestMachines_MainOpensModels(t *testing.T) {
	t.Parallel()
	d := newTestMachines(t)
	act := d.HandleMsg(keyMsg('m'))
	open, ok := act.(ActionOpenDialog)
	require.True(t, ok, "expected ActionOpenDialog, got %T", act)
	require.Equal(t, ModelsID, open.DialogID)
}

func TestMachines_RenderShowsMainAndRole(t *testing.T) {
	t.Parallel()
	d := newTestMachines(t)
	d.SetMainModel(config.SelectedModel{Provider: "anthropic", Model: "claude-sonnet-4-5"})
	sts := testStatuses()
	sts[0].Role = "code review and tests"
	d.SetStatuses(sts, time.Now())
	d.SetSuggestions([]fleet.Suggestion{{Name: "m1", Role: "parallel search and bulk edits", Reason: "four slots"}}, false, nil)
	out := ansi.Strip(d.content(time.Now()))
	require.Contains(t, out, "Main AI")
	require.Contains(t, out, "anthropic/claude-sonnet-4-5")
	require.Contains(t, out, "code review and tests")
	require.Contains(t, out, "parallel search and bulk edits")
	require.Contains(t, out, "four slots")
}

func TestMainModelName(t *testing.T) {
	t.Parallel()
	require.Equal(t, "", MainModelName(config.SelectedModel{}))
	require.Equal(t, "ollama", MainModelName(config.SelectedModel{Provider: "ollama"}))
	require.Equal(t, "ollama/qwen3", MainModelName(config.SelectedModel{Provider: "ollama", Model: "qwen3"}))
}
