package dialog

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/fleet"
	"github.com/charmbracelet/crush/internal/ui/util"
)

// The Machines tab of the models dialog lists the user's systems (this
// computer and every machine) and lets them choose how each one is used:
// as the main AI, as a helper agents pick automatically, as a helper only
// when asked for by name, or not at all.

const machinesInputPlaceholder = "Choose how each machine is used"

// Row IDs on the Machines tab. Machine rows are "machine:<name>" and
// picker rows "pick:<name>:<choice>".
const (
	machineRowThis = "machine:this"
	machineRowAdd  = "machine:+"
)

// What the picker offers for a machine.
const (
	pickMain    = "main"
	pickAuto    = "auto"
	pickNamed   = "named"
	pickOff     = "off"
	pickDetails = "details"
)

// machinesState is what the Machines tab knows beyond the config.
type machinesState struct {
	statuses []fleet.Status
	checked  bool
	checking bool
	// picking names the machine whose use is being chosen; "" shows
	// the machine list.
	picking string
	saving  bool
}

// SetMachineStatuses shows the result of a machines check on the
// Machines tab.
func (m *Models) SetMachineStatuses(s []fleet.Status, _ time.Time) {
	m.machines.statuses = s
	m.machines.checked = true
	m.machines.checking = false
	if m.modelType == ModelTypeMachines {
		m.setMachineItems()
	}
}

// SetMachinesResult shows the outcome of a change made from the Machines
// tab: on success the picker closes and the list shows the new use.
func (m *Models) SetMachinesResult(_ string, err error) {
	m.machines.saving = false
	if err != nil {
		return
	}
	m.machines.picking = ""
	if m.modelType == ModelTypeMachines {
		m.setMachineItems()
	}
}

// StartMachinesCheck marks a check as running.
func (m *Models) StartMachinesCheck() {
	m.machines.checking = true
}

// enterMachinesTab shows the Machines tab and asks for a check when
// there is something to check.
func (m *Models) enterMachinesTab() Action {
	m.modelType = ModelTypeMachines
	m.machines.picking = ""
	cfg := m.com.Config()
	check := cfg != nil && len(cfg.Machines) > 0 &&
		!m.machines.checking && !m.machines.checked
	if check {
		m.machines.checking = true
	}
	m.setMachineItems()
	if !check {
		return nil
	}
	return ActionCheckMachines{}
}

// machineStatus returns the last check of the named machine, if any.
func (m *Models) machineStatus(name string) *fleet.Status {
	for i := range m.machines.statuses {
		if m.machines.statuses[i].Name == name {
			return &m.machines.statuses[i]
		}
	}
	return nil
}

// mainMachine returns the machine the main AI runs on, or "".
func mainMachine(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	cur := cfg.Models[config.SelectedModelTypeLarge]
	if _, ok := cfg.Machines[cur.Provider]; ok {
		return cur.Provider
	}
	return ""
}

// setMachineItems fills the list for the Machines tab: the machine rows,
// or the picker for the machine being configured.
func (m *Models) setMachineItems() {
	t := m.com.Styles
	cfg := m.com.Config()
	m.input.Reset()
	m.list.SetFilter("")

	if m.machines.picking != "" {
		group, selected := m.pickerGroup(m.machines.picking)
		m.list.SetGroups(group)
		m.list.SetSelectedItem(selected)
		m.scrollMachinesList()
		if !m.isOnboarding {
			m.input.Placeholder = fmt.Sprintf("How should %s be used?", m.machines.picking)
		}
		return
	}

	onMachine := mainMachine(cfg)
	cur := config.SelectedModel{}
	if cfg != nil {
		cur = cfg.Models[config.SelectedModelTypeLarge]
	}
	thisInfo := "main AI: " + MainModelName(cur)
	if onMachine != "" {
		thisInfo = "main AI runs on " + onMachine
	}
	if cur.Model == "" && cur.Provider == "" {
		thisInfo = "no main AI chosen yet"
	}

	group := NewModelGroup(t, "Your machines", false)
	group.AppendItems(newMachineRow(t, machineRow{
		id:    machineRowThis,
		label: "this computer",
		info:  thisInfo,
	}))
	for _, mc := range fleet.Machines(cfg) {
		label := mc.Name
		if mc.Hardware != "" {
			label = fmt.Sprintf("%s (%s)", mc.Name, mc.Hardware)
		}
		group.AppendItems(newMachineRow(t, machineRow{
			id:      "machine:" + mc.Name,
			label:   label,
			info:    m.machineRowInfo(mc, mc.Name == onMachine),
			machine: mc.Name,
		}))
	}
	addLabel := "Add a machine…"
	if cfg != nil && len(cfg.Machines) > 0 {
		addLabel = "Add or edit machines…"
	}
	group.AppendItems(newMachineRow(t, machineRow{
		id:    machineRowAdd,
		label: addLabel,
		info:  "roles, addresses, slots",
	}))

	// Keep the cursor on the same row across refreshes, and on the
	// machine's row when coming back from its picker.
	selectedID := ""
	if item, ok := m.list.SelectedItem().(*ModelItem); ok && item.row != nil {
		selectedID = item.row.id
		if item.row.pick != "" {
			selectedID = "machine:" + item.row.machine
		}
	}
	m.list.SetGroups(group)
	m.list.SetSelected(0)
	if selectedID != "" {
		m.list.SetSelectedItem(selectedID)
	}
	m.scrollMachinesList()
	if !m.isOnboarding {
		m.input.Placeholder = machinesInputPlaceholder
	}
}

// scrollMachinesList keeps the group title in view. Before the first draw
// the list has no height, and scrolling to the selected row would then pin
// it to the top and hide the title and the rows above it; the Machines tab
// is short, so it starts at the top and only scrolls once it has a size.
func (m *Models) scrollMachinesList() {
	m.list.ScrollToTop()
	if m.list.Height() > 0 {
		m.list.ScrollToSelected()
	}
}

// machineRowInfo describes how a machine is used and whether it answers:
// "auto · online, 2 free", "by name · offline", "off".
func (m *Models) machineRowInfo(mc fleet.Machine, isMain bool) string {
	var parts []string
	if isMain {
		parts = append(parts, "main AI")
	}
	parts = append(parts, useLabel(mc.MachineConfig))
	mode := strings.Join(parts, ", ")
	if mc.Disabled {
		return mode
	}
	st := m.machineStatus(mc.Name)
	switch {
	case st == nil && m.machines.checking:
		return mode + " · checking…"
	case st == nil:
		return mode
	case st.Online:
		return fmt.Sprintf("%s · online, %d free", mode, st.Free())
	default:
		return mode + " · offline"
	}
}

// useLabel is the short name of a machine's use: auto, by name or off.
func useLabel(mc config.MachineConfig) string {
	switch {
	case mc.Disabled:
		return "off"
	case mc.UseMode() == config.MachineUseNamed:
		return "by name"
	default:
		return "auto"
	}
}

// pickerGroup lists the ways the named machine can be used, marking the
// current one. It returns the group and the ID of the row to select.
func (m *Models) pickerGroup(name string) (ModelGroup, string) {
	t := m.com.Styles
	cfg := m.com.Config()
	var mc config.MachineConfig
	if cfg != nil {
		mc = cfg.Machines[name]
	}
	isMain := mainMachine(cfg) == name
	use := useLabel(mc)

	id := func(pick string) string { return "pick:" + name + ":" + pick }
	current := func(yes bool) string {
		if yes {
			return "current"
		}
		return ""
	}

	group := NewModelGroup(t, "How should "+name+" be used?", false)
	selected := ""
	if cfg != nil && cfg.MachineModel(name) != "" {
		group.AppendItems(newMachineRow(t, machineRow{
			id:      id(pickMain),
			label:   "Main AI: run Ta-Da on " + name,
			info:    current(isMain),
			machine: name,
			pick:    pickMain,
		}))
		if isMain {
			selected = id(pickMain)
		}
	}
	helpers := []struct {
		pick, label, use string
	}{
		{pickAuto, "Helper, picked automatically when it has a free slot", "auto"},
		{pickNamed, "Helper, only when asked for by name", "by name"},
	}
	for _, h := range helpers {
		group.AppendItems(newMachineRow(t, machineRow{
			id:      id(h.pick),
			label:   h.label,
			info:    current(use == h.use),
			machine: name,
			pick:    h.pick,
		}))
		if use == h.use && selected == "" {
			selected = id(h.pick)
		}
	}
	// Switching off the machine the main AI runs on would leave the
	// user without one; change the main AI first.
	if !isMain {
		group.AppendItems(newMachineRow(t, machineRow{
			id:      id(pickOff),
			label:   "Off: keep it configured but don't use it",
			info:    current(mc.Disabled),
			machine: name,
			pick:    pickOff,
		}))
		if mc.Disabled {
			selected = id(pickOff)
		}
	}
	group.AppendItems(newMachineRow(t, machineRow{
		id:      id(pickDetails),
		label:   "Role and details…",
		info:    "address, slots, model, role",
		machine: name,
		pick:    pickDetails,
	}))
	if selected == "" {
		selected = group.Items[0].ID()
	}
	return group, selected
}

// selectMachineRow acts on enter (or ctrl+e when edit is set) on a
// Machines tab row. It reports false for model rows.
func (m *Models) selectMachineRow(item *ModelItem, edit bool) (Action, bool) {
	row := item.row
	if row == nil {
		return nil, false
	}
	switch {
	case row.id == machineRowThis:
		// This computer's main AI is chosen on the Large Task tab.
		m.modelType = ModelTypeLarge
		m.machines.picking = ""
		if err := m.setProviderItems(); err != nil {
			return util.ReportError(err), true
		}
		return nil, true
	case row.id == machineRowAdd:
		return ActionOpenMachines{}, true
	case row.pick != "":
		return m.pickMachineUse(row.machine, row.pick), true
	case edit:
		return ActionOpenMachines{Machine: row.machine}, true
	default:
		m.machines.picking = row.machine
		m.setMachineItems()
		return nil, true
	}
}

// pickMachineUse applies a picker choice for the named machine.
func (m *Models) pickMachineUse(name, pick string) Action {
	switch pick {
	case pickDetails:
		return ActionOpenMachines{Machine: name}
	case pickMain:
		return m.selectMachineAsMain(name)
	}
	if m.machines.saving {
		return nil
	}
	off := pick == pickOff
	p := fleet.Params{Action: "update", Name: name, Disabled: &off}
	switch pick {
	case pickAuto:
		p.Use = config.MachineUseAuto
	case pickNamed:
		p.Use = config.MachineUseNamed
	}
	m.machines.saving = true
	return ActionSaveMachine{Params: p}
}

// selectMachineAsMain makes the named machine's model the main AI.
func (m *Models) selectMachineAsMain(name string) Action {
	cfg := m.com.Config()
	if cfg == nil {
		return nil
	}
	pc, ok := cfg.MachineProvider(name)
	modelID := cfg.MachineModel(name)
	if !ok || modelID == "" {
		return util.ReportError(fmt.Errorf("machine %q has no model to run yet", name))
	}
	model := cfg.MachineModelDefaults(name, modelID, 0)
	for _, candidate := range pc.Models {
		if candidate.ID == modelID {
			model = candidate
			break
		}
	}
	item := NewModelItem(m.com.Styles, pc.ToProvider(), model, ModelTypeLarge, false)
	return ActionSelectModel{
		Provider:  item.prov,
		Model:     item.SelectedModel(),
		ModelType: config.SelectedModelTypeLarge,
	}
}

// machineRowSelected reports whether the selected row is a machine, so
// the help can offer its details.
func (m *Models) machineRowSelected() bool {
	item, ok := m.list.SelectedItem().(*ModelItem)
	return ok && item.row != nil && item.row.machine != "" && item.row.pick == ""
}

// closeMachinePicker goes back from the picker to the machine list. It
// reports false when the picker isn't open.
func (m *Models) closeMachinePicker() bool {
	if m.modelType != ModelTypeMachines || m.machines.picking == "" {
		return false
	}
	m.machines.picking = ""
	m.setMachineItems()
	return true
}
