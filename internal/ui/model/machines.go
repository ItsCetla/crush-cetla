package model

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/pkg/catwalk"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/fleet"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/workspace"
)

// machinesStatusMsg delivers a machines check for the machines dialog.
type machinesStatusMsg struct {
	statuses []fleet.Status
	at       time.Time
}

// machinesResultMsg delivers the outcome of a machines change.
type machinesResultMsg struct {
	out string
	err error
}

// machineStore lets the fleet tool save changes through the workspace, so
// they work the same in client/server mode.
type machineStore struct {
	ws workspace.Workspace
}

var _ fleet.Store = machineStore{}

func (s machineStore) Config() *config.Config { return s.ws.Config() }

func (s machineStore) KnownProviders() []catwalk.Provider {
	// The error is advisory: it still returns what it has on disk.
	ps, _ := config.Providers(s.ws.Config())
	return ps
}

// HasConfigField reports global keys only. The workspace can't look into
// the files, and the screen saves to the global config anyway. A machine
// defined elsewhere is switched off instead of removed.
func (s machineStore) HasConfigField(scope config.Scope, _ string) bool {
	return scope == config.ScopeGlobal
}

func (s machineStore) SetConfigField(scope config.Scope, key string, value any) error {
	return s.ws.SetConfigField(scope, key, value)
}

func (s machineStore) RemoveConfigField(scope config.Scope, key string) error {
	return s.ws.RemoveConfigField(scope, key)
}

// openMachinesDialog opens the machines screen and starts a check.
func (m *UI) openMachinesDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.MachinesID) {
		m.dialog.CloseDialog(dialog.MachinesID)
	}
	d, _ := dialog.NewMachines(m.com)
	m.dialog.OpenDialog(d)
	return m.machinesCheckCmd()
}

// machinesCheckCmd probes every machine off-thread.
func (m *UI) machinesCheckCmd() tea.Cmd {
	if m.com == nil || m.com.Workspace == nil {
		return nil
	}
	cfg := m.com.Workspace.Config()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		fleet.ForgetProbes()
		return machinesStatusMsg{statuses: fleet.Default().Check(ctx, cfg), at: time.Now()}
	}
}

// machinesSaveCmd runs a machines change off-thread, then checks again so
// the list shows the new state.
func (m *UI) machinesSaveCmd(p fleet.Params) tea.Cmd {
	if m.com == nil || m.com.Workspace == nil {
		return nil
	}
	store := machineStore{ws: m.com.Workspace}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		out, err := fleet.RunTool(ctx, store, fleet.Default(), p)
		return machinesResultMsg{out: out, err: err}
	}
}

// machinesDialog returns the open machines dialog, if any.
func (m *UI) machinesDialog() *dialog.Machines {
	if !m.dialog.ContainsDialog(dialog.MachinesID) {
		return nil
	}
	d, _ := m.dialog.Dialog(dialog.MachinesID).(*dialog.Machines)
	return d
}

// handleMachinesStatus shows a finished check.
func (m *UI) handleMachinesStatus(msg machinesStatusMsg) {
	if d := m.machinesDialog(); d != nil {
		d.SetStatuses(msg.statuses, msg.at)
	}
}

// handleMachinesResult shows a finished change and refreshes the list.
func (m *UI) handleMachinesResult(msg machinesResultMsg) tea.Cmd {
	d := m.machinesDialog()
	if d == nil {
		return nil
	}
	d.SetResult(msg.out, msg.err)
	if msg.err != nil {
		return nil
	}
	d.StartCheck()
	return m.machinesCheckCmd()
}
