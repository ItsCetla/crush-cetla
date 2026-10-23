package dialog

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/control"
	"github.com/charmbracelet/crush/internal/fleet"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// MachinesID is the identifier for the machines dialog.
const MachinesID = "machines"

// ActionOpenMachines opens the machines dialog, on Machine when set.
type ActionOpenMachines struct {
	Machine string
}

// ActionCheckMachines asks the UI to probe every machine and hand the
// result back through SetStatuses.
type ActionCheckMachines struct{}

// ActionSaveMachine asks the UI to run a machines change (add, update or
// remove) and hand the result back through SetResult.
type ActionSaveMachine struct {
	Params fleet.Params
}

// ActionSuggestRoles asks the UI to have the main AI propose a role for
// each machine and hand the result back through SetSuggestions.
type ActionSuggestRoles struct {
	Statuses []fleet.Status
}

// ActionApplyRoles asks the UI to save several role changes at once and
// hand the result back through SetResult.
type ActionApplyRoles struct {
	Params []fleet.Params
}

// MachinesQuery reports whether value is the machines command typed into
// the editor ("/machines", "/machine" or "/fleet").
func MachinesQuery(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "/machines", "/machine", "/fleet":
		return true
	}
	return false
}

// Form fields, in tab order.
const (
	machineFieldName = iota
	machineFieldURL
	machineFieldSlots
	machineFieldHardware
	machineFieldModel
	machineFieldRole
	machineFieldCount
)

var machineFieldLabels = [machineFieldCount]string{"Name", "Address", "Slots", "Hardware", "Model", "Role"}

var machineFieldPlaceholders = [machineFieldCount]string{
	"m1",
	"192.168.1.20 or http://192.168.1.20:11434/v1",
	"how many agents at once, e.g. 4",
	`what it is, e.g. "M1 Max"`,
	"optional: empty uses the first one it lists",
	"optional: its job, e.g. code review and tests",
}

// Machines lists the user's machines with their live status and lets them
// add, change, switch off and remove machines.
type Machines struct {
	com    *common.Common
	width  int
	height int

	statuses []fleet.Status
	loaded   bool
	checking bool
	saving   bool
	updated  time.Time
	notice   string
	err      string

	cursor     int
	confirmDel string // machine armed for removal
	// wanted names the machine to put the cursor on once the first check
	// lands.
	wanted string

	// mainModel names the AI the user talks to, so the screen can show
	// it next to the machines that help it.
	mainModel string

	suggesting    bool
	suggestions   map[string]fleet.Suggestion
	suggestFromAI bool

	form    bool
	editing string // machine being changed; empty when adding
	field   int
	inputs  [machineFieldCount]textinput.Model

	help   help.Model
	keyMap struct {
		Up      key.Binding
		Down    key.Binding
		Add     key.Binding
		Edit    key.Binding
		Toggle  key.Binding
		Remove  key.Binding
		Refresh key.Binding
		Main    key.Binding
		Suggest key.Binding
		Apply   key.Binding
		ApplyAl key.Binding
		Next    key.Binding
		Prev    key.Binding
		Save    key.Binding
		Back    key.Binding
		Close   key.Binding
	}
}

var _ Dialog = (*Machines)(nil)

// NewMachines creates the machines dialog. The caller loads it by
// handling the returned action.
func NewMachines(com *common.Common) (*Machines, ActionCheckMachines) {
	t := com.Styles
	d := &Machines{com: com, checking: true}
	d.help = help.New()
	d.help.Styles = t.DialogHelpStyles()
	for i := range d.inputs {
		in := textinput.New()
		in.SetVirtualCursor(true)
		in.SetStyles(t.TextInput)
		in.CharLimit = 200
		in.Placeholder = machineFieldPlaceholders[i]
		d.inputs[i] = in
	}

	b := func(keys []string, k, desc string) key.Binding {
		return key.NewBinding(key.WithKeys(keys...), key.WithHelp(k, desc))
	}
	km := &d.keyMap
	km.Up = b([]string{"up", "k"}, "↑/k", "up")
	km.Down = b([]string{"down", "j"}, "↓/j", "down")
	km.Add = b([]string{"a", "n"}, "a", "add")
	km.Edit = b([]string{"e", "enter"}, "e", "edit")
	km.Toggle = b([]string{"space", " "}, "space", "on/off")
	km.Remove = b([]string{"x", "d", "delete"}, "x", "remove")
	km.Refresh = b([]string{"r"}, "r", "check again")
	km.Main = b([]string{"m"}, "m", "main AI")
	km.Suggest = b([]string{"s"}, "s", "suggest roles")
	km.Apply = b([]string{"y"}, "y", "use role")
	km.ApplyAl = b([]string{"Y"}, "Y", "use all")
	km.Next = b([]string{"tab", "down"}, "tab", "next field")
	km.Prev = b([]string{"shift+tab", "up"}, "shift+tab", "prev field")
	km.Save = b([]string{"enter", "ctrl+s"}, "enter", "next/save")
	km.Back = b([]string{"esc"}, "esc", "cancel")
	km.Close = CloseKey
	if com != nil && com.Workspace != nil {
		if cfg := com.Config(); cfg != nil {
			d.SetMainModel(cfg.Models[config.SelectedModelTypeLarge])
		}
	}
	return d, ActionCheckMachines{}
}

// SetMainModel names the AI the user talks to.
func (d *Machines) SetMainModel(m config.SelectedModel) {
	d.mainModel = MainModelName(m)
}

// MainModelName is how the machines screen and the suggestion prompt
// name the main AI: provider/model, or "" when none is set.
func MainModelName(m config.SelectedModel) string {
	if m.Model == "" {
		return m.Provider
	}
	if m.Provider == "" {
		return m.Model
	}
	return m.Provider + "/" + m.Model
}

// SetSuggestions shows the roles proposed for the machines. fromAI says
// the main AI answered; otherwise they come from each machine's
// resources and err, if set, says why the AI couldn't.
func (d *Machines) SetSuggestions(sugs []fleet.Suggestion, fromAI bool, err error) {
	d.suggesting = false
	d.suggestFromAI = fromAI
	d.suggestions = make(map[string]fleet.Suggestion, len(sugs))
	for _, s := range sugs {
		d.suggestions[s.Name] = s
	}
	d.err = ""
	switch {
	case len(sugs) == 0 && err != nil:
		d.err = "Couldn't suggest roles: " + err.Error()
	case len(sugs) == 0:
		d.notice = "Nothing to suggest yet; add a machine first."
	case fromAI:
		d.notice = "Roles suggested by your main AI · y uses one, Y uses them all"
	case err != nil:
		d.notice = "Roles from each machine's resources (your main AI couldn't answer: " +
			strings.Join(strings.Fields(err.Error()), " ") + ") · y uses one, Y uses them all"
	default:
		d.notice = "Roles from each machine's resources · y uses one, Y uses them all"
	}
}

// Suggestion returns the role proposed for the machine, if any.
func (d *Machines) Suggestion(name string) (fleet.Suggestion, bool) {
	s, ok := d.suggestions[name]
	return s, ok
}

// ID implements Dialog.
func (d *Machines) ID() string { return MachinesID }

// SetStatuses shows the result of a check.
func (d *Machines) SetStatuses(s []fleet.Status, at time.Time) {
	d.statuses = s
	d.loaded = true
	d.checking = false
	d.updated = at
	if d.wanted != "" {
		for i, st := range s {
			if st.Name == d.wanted {
				d.cursor = i
			}
		}
		d.wanted = ""
	}
	d.cursor = max(0, min(d.cursor, len(s)-1))
}

// SelectMachine puts the cursor on the named machine, now if the list is
// loaded and otherwise when it is.
func (d *Machines) SelectMachine(name string) {
	if name == "" {
		return
	}
	d.wanted = name
	if d.loaded {
		d.SetStatuses(d.statuses, d.updated)
	}
}

// SetResult shows the outcome of a change. On success the form closes; on
// failure it stays open so the user can fix it.
func (d *Machines) SetResult(out string, err error) {
	d.saving = false
	if err != nil {
		d.err = err.Error()
		d.notice = ""
		return
	}
	d.err = ""
	// The fleet tool writes for the model; point at the key instead.
	out = strings.ReplaceAll(out, "set one with update", "press e to set one")
	d.notice = strings.Join(strings.Fields(out), " ")
	d.form = false
	d.confirmDel = ""
}

// Checking reports whether a check is running.
func (d *Machines) Checking() bool { return d.checking }

// StartCheck marks a check as running.
func (d *Machines) StartCheck() { d.checking = true }

func (d *Machines) selected() *fleet.Status {
	if d.cursor < 0 || d.cursor >= len(d.statuses) {
		return nil
	}
	return &d.statuses[d.cursor]
}

// HandleMsg implements Dialog.
func (d *Machines) HandleMsg(msg tea.Msg) Action {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		if d.form {
			var cmd tea.Cmd
			d.inputs[d.field], cmd = d.inputs[d.field].Update(msg)
			if cmd != nil {
				return ActionCmd{cmd}
			}
		}
		return nil
	}
	if d.form {
		return d.handleFormKey(k)
	}
	km := &d.keyMap
	m := d.selected()
	if !key.Matches(k, km.Remove) {
		d.confirmDel = ""
	}
	switch {
	case key.Matches(k, km.Close):
		return ActionClose{}
	case key.Matches(k, km.Up):
		d.cursor = max(0, d.cursor-1)
	case key.Matches(k, km.Down):
		d.cursor = max(0, min(d.cursor+1, len(d.statuses)-1))
	case key.Matches(k, km.Refresh):
		if d.checking {
			return nil
		}
		d.checking = true
		d.notice, d.err = "", ""
		return ActionCheckMachines{}
	case key.Matches(k, km.Main):
		return ActionOpenDialog{DialogID: ModelsID}
	case key.Matches(k, km.Suggest):
		if d.suggesting || d.saving || len(d.statuses) == 0 {
			return nil
		}
		d.suggesting = true
		d.notice, d.err = "", ""
		return ActionSuggestRoles{Statuses: slices.Clone(d.statuses)}
	case key.Matches(k, km.Apply):
		if m == nil || d.saving {
			return nil
		}
		s, ok := d.suggestions[m.Name]
		if !ok {
			d.notice = "No suggestion for " + m.Name + " yet; press s to ask."
			return nil
		}
		d.saving = true
		d.notice, d.err = "", ""
		return ActionSaveMachine{Params: fleet.Params{Action: "update", Name: m.Name, Role: s.Role}}
	case key.Matches(k, km.ApplyAl):
		if d.saving {
			return nil
		}
		var ps []fleet.Params
		for _, st := range d.statuses {
			if s, ok := d.suggestions[st.Name]; ok && s.Role != st.Role {
				ps = append(ps, fleet.Params{Action: "update", Name: st.Name, Role: s.Role})
			}
		}
		if len(ps) == 0 {
			d.notice = "No new roles to use; press s to ask for suggestions."
			return nil
		}
		d.saving = true
		d.notice, d.err = "", ""
		return ActionApplyRoles{Params: ps}
	case key.Matches(k, km.Add):
		d.openForm(nil)
	case key.Matches(k, km.Edit):
		// With nothing to change yet, enter starts adding one.
		d.openForm(m)
	case key.Matches(k, km.Toggle):
		if m == nil || d.saving {
			return nil
		}
		off := !m.Disabled
		d.saving = true
		return ActionSaveMachine{Params: fleet.Params{Action: "update", Name: m.Name, Disabled: &off}}
	case key.Matches(k, km.Remove):
		if m == nil || d.saving {
			return nil
		}
		if d.confirmDel != m.Name {
			d.confirmDel = m.Name
			d.err = ""
			d.notice = fmt.Sprintf("Press x again to remove %s.", m.Name)
			return nil
		}
		d.saving = true
		return ActionSaveMachine{Params: fleet.Params{Action: "remove", Name: m.Name}}
	}
	return nil
}

// openForm opens the add form, or the edit form for m.
func (d *Machines) openForm(m *fleet.Status) {
	d.form = true
	d.err, d.notice = "", ""
	d.editing = ""
	for i := range d.inputs {
		d.inputs[i].Reset()
		d.inputs[i].Blur()
	}
	d.field = machineFieldName
	if m != nil {
		d.editing = m.Name
		d.inputs[machineFieldName].SetValue(m.Name)
		d.inputs[machineFieldURL].SetValue(m.URL)
		// The saved value, not the live one: the form edits settings.
		d.inputs[machineFieldSlots].SetValue(strconv.Itoa(m.MachineConfig.SlotCount()))
		d.inputs[machineFieldHardware].SetValue(m.Hardware)
		d.inputs[machineFieldModel].SetValue(m.MachineConfig.Model)
		d.inputs[machineFieldRole].SetValue(m.Role)
		d.field = machineFieldURL
	}
	d.inputs[d.field].Focus()
}

func (d *Machines) focusField(i int) {
	first := machineFieldName
	if d.editing != "" {
		// The name can't change; it's how the machine is saved.
		first = machineFieldURL
	}
	if i < first {
		i = machineFieldCount - 1
	}
	if i >= machineFieldCount {
		i = first
	}
	d.inputs[d.field].Blur()
	d.field = i
	d.inputs[d.field].Focus()
}

func (d *Machines) handleFormKey(k tea.KeyPressMsg) Action {
	km := &d.keyMap
	switch {
	case key.Matches(k, km.Back):
		d.form = false
		d.err = ""
		return nil
	case k.String() == "enter" && d.field < machineFieldCount-1:
		d.focusField(d.field + 1)
		return nil
	case key.Matches(k, km.Save):
		if d.saving {
			return nil
		}
		p, err := d.formParams()
		if err != nil {
			d.err = err.Error()
			return nil
		}
		d.err = ""
		d.saving = true
		return ActionSaveMachine{Params: p}
	case key.Matches(k, km.Next):
		d.focusField(d.field + 1)
		return nil
	case key.Matches(k, km.Prev):
		d.focusField(d.field - 1)
		return nil
	}
	var cmd tea.Cmd
	d.inputs[d.field], cmd = d.inputs[d.field].Update(k)
	d.err = ""
	if cmd != nil {
		return ActionCmd{cmd}
	}
	return nil
}

// formParams turns the form into a machines change.
func (d *Machines) formParams() (fleet.Params, error) {
	val := func(i int) string { return strings.TrimSpace(d.inputs[i].Value()) }
	p := fleet.Params{
		Action:   "add",
		Name:     val(machineFieldName),
		Hardware: val(machineFieldHardware),
		Model:    val(machineFieldModel),
		Role:     val(machineFieldRole),
	}
	if d.editing != "" {
		p.Action = "update"
		p.Name = d.editing
	}
	if p.Name == "" {
		return p, fmt.Errorf("give the machine a short name, like m1 or pc")
	}
	addr := val(machineFieldURL)
	if addr == "" && p.Action == "add" {
		return p, fmt.Errorf("add the machine's address: its IP, or the URL of its model server")
	}
	if addr != "" {
		u, typ, err := MachineURL(addr)
		if err != nil {
			return p, err
		}
		p.URL = u
		if p.Action == "add" {
			p.Type = typ
		}
	}
	if s := val(machineFieldSlots); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return p, fmt.Errorf("slots is how many agents it runs at once: a number from 1 up")
		}
		p.Slots = n
	}
	return p, nil
}

// MachineURL turns what the user typed into a model server base URL. A
// bare host or IP is taken to be Ollama on its usual port. It also returns
// the server type the port suggests, or "" when it can't tell.
func MachineURL(addr string) (string, string, error) {
	addr = strings.TrimSpace(addr)
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	u, err := url.Parse(addr)
	if err != nil || u.Host == "" {
		return "", "", fmt.Errorf("%q doesn't look like an address; try 192.168.1.20 or http://192.168.1.20:11434/v1", addr)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", fmt.Errorf("the address must start with http:// or https://")
	}
	path := strings.TrimRight(u.Path, "/")
	if u.Port() == "" && path == "" {
		u.Host = net.JoinHostPort(u.Hostname(), "11434")
	}
	var typ string
	switch u.Port() {
	case "11434":
		typ = "ollama"
	case "1234":
		typ = "lmstudio"
	}
	if path == "" && typ != "" {
		path = "/v1"
	}
	u.Path = path
	return strings.TrimRight(u.String(), "/"), typ, nil
}

// Draw implements Dialog.
func (d *Machines) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := d.com.Styles
	d.width = max(0, min(84, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	// Title, footer, help and the frame take about eight rows.
	d.height = max(8, min(16, area.Dy()-8))
	DrawCenter(scr, area, d.content(time.Now()))
	return nil
}

func (d *Machines) innerWidth() int {
	return max(0, d.width-d.com.Styles.Dialog.View.GetHorizontalFrameSize())
}

func (d *Machines) content(now time.Time) string {
	t := d.com.Styles
	inner := d.innerWidth()
	rc := NewRenderContext(t, d.width)
	rc.Title = "Machines"
	switch {
	case d.form && d.editing != "":
		rc.Title = "Change " + d.editing
	case d.form:
		rc.Title = "Add a Machine"
	}
	rc.TitleInfo = d.summary(t)

	var body []string
	if d.form {
		body = d.formRows(t, inner-2)
	} else {
		body = d.listRows(t, inner-2)
	}
	rc.AddPart(d.frame(body, inner))
	rc.AddPart(d.footer(t, now, inner))
	rc.Help = renderDialogHelp(t, &d.help, d, inner)
	return rc.Render()
}

// summary is the title's "2 ready · 5 of 6 agents free" note.
func (d *Machines) summary(t *styles.Styles) string {
	if d.form || !d.loaded || len(d.statuses) == 0 {
		return ""
	}
	ready, total := fleet.Ready(d.statuses)
	free := 0
	for _, s := range d.statuses {
		if s.Online && s.Usable() {
			free += s.Free()
		}
	}
	return " " + t.Control.Muted.Render(fmt.Sprintf("%d ready · %d of %d agent%s free", ready, free, total, plural(total)))
}

// frame pads or clips body to the dialog height so it doesn't jump.
func (d *Machines) frame(body []string, inner int) string {
	if len(body) > d.height {
		body = body[:d.height]
	}
	for len(body) < d.height {
		body = append(body, "")
	}
	pad := lipgloss.NewStyle().Padding(0, 1)
	out := make([]string, len(body))
	for i, l := range body {
		out[i] = pad.Render(ansi.Truncate(l, max(0, inner-2), "…"))
	}
	return strings.Join(out, "\n")
}

func (d *Machines) listRows(t *styles.Styles, width int) []string {
	if !d.loaded {
		return []string{t.Control.Muted.Render("Checking your machines…")}
	}
	if len(d.statuses) == 0 {
		return d.emptyRows(t)
	}
	nameW := 4
	hwW := 0
	for _, s := range d.statuses {
		nameW = max(nameW, ansi.StringWidth(s.Name))
		hwW = max(hwW, ansi.StringWidth(s.Hardware))
	}
	nameW = min(nameW, 16)
	hwW = min(hwW, 18)

	lines := []string{d.mainLine(t, width), ""}
	var details []string
	if m := d.selected(); m != nil {
		details = append(details, "")
		details = append(details, d.detailRows(t, *m, width)...)
	}
	// The selected machine's details go below the list; the list gets
	// whatever is left.
	rows := max(1, d.height-len(lines)-len(details))
	start := 0
	if d.cursor >= rows {
		start = d.cursor - rows + 1
	}
	for i := start; i < len(d.statuses) && i < start+rows; i++ {
		lines = append(lines, d.machineLine(t, d.statuses[i], i == d.cursor, nameW, hwW, width))
	}
	return append(lines, details...)
}

// mainLine names the AI the machines help, and how to change it.
func (d *Machines) mainLine(t *styles.Styles, width int) string {
	name := d.mainModel
	if name == "" {
		name = t.Control.Muted.Render("none chosen")
	} else {
		name = t.Control.Value.Render(ansi.Truncate(name, max(0, width-24), "…"))
	}
	return t.Control.Label.Render("Main AI  ") + name + t.Control.Muted.Render("  m to change")
}

func (d *Machines) machineLine(t *styles.Styles, s fleet.Status, sel bool, nameW, hwW, width int) string {
	var mark, state string
	switch {
	case s.Disabled:
		mark = t.Control.Off.Render("–")
		state = t.Control.Muted.Render("off")
	case !d.checking && !s.Online:
		mark = t.Control.Denied.Render("✕")
		state = t.Control.Alert.Render("offline")
	case s.Online:
		mark = t.Control.Allowed.Render("●")
		slots := fmt.Sprintf("%d of %d free", s.Free(), s.SlotCount())
		if s.Free() == 0 {
			state = t.Control.Warn.Render(slots)
		} else {
			state = t.Control.Value.Render(slots)
		}
	default:
		mark = t.Control.Muted.Render("…")
		state = t.Control.Muted.Render("checking")
	}
	cur := "  "
	if sel {
		cur = t.Control.Allowed.Render(">") + " "
	}
	name := fmt.Sprintf("%-*s", nameW, ansi.Truncate(s.Name, nameW, "…"))
	if sel {
		name = t.Control.Value.Render(name)
	}
	hw := ""
	if hwW > 0 {
		hw = t.Control.Muted.Render(fmt.Sprintf("%-*s", hwW, ansi.Truncate(s.Hardware, hwW, "…"))) + "  "
	}
	line := cur + mark + " " + name + "  " + hw + state
	if s.Model != "" && !s.Disabled {
		line += t.Control.Muted.Render("  " + s.Model)
	}
	return ansi.Truncate(line, width, "…")
}

func (d *Machines) detailRows(t *styles.Styles, s fleet.Status, width int) []string {
	trunc := func(v string) string { return ansi.Truncate(v, max(0, width-11), "…") }
	lines := []string{kv(t, "Address", trunc(s.URL))}
	if s.Type != "" {
		lines[0] += t.Control.Muted.Render(" (" + string(s.Type) + ")")
	}
	model := s.Model
	if model == "" {
		model = t.Control.Muted.Render("none yet; it'll use the first one the server lists")
	}
	lines = append(lines, kv(t, "Model", trunc(model)))
	switch {
	case s.Disabled:
		lines = append(lines, kv(t, "Status", t.Control.Muted.Render("switched off; space turns it back on")))
	case s.Error != "" && !d.checking:
		lines = append(lines, kv(t, "Status", t.Control.Alert.Render(trunc(s.Error))))
	case s.Online && s.Busy > 0:
		lines = append(lines, kv(t, "Status", fmt.Sprintf("online, %d agent%s working", s.Busy, plural(s.Busy))))
	case s.Online:
		lines = append(lines, kv(t, "Status", fmt.Sprintf("online, ready for %d agent%s", s.SlotCount(), plural(s.SlotCount()))))
	}
	if !s.Disabled {
		lines = append(lines, d.agentRows(t, s)...)
	}
	if len(s.Tags) > 0 {
		lines = append(lines, kv(t, "Good for", trunc(strings.Join(s.Tags, ", "))))
	}
	role := s.Role
	if role == "" {
		role = t.Control.Muted.Render("none; s asks your main AI what it's best at")
	}
	lines = append(lines, kv(t, "Role", trunc(role)))
	if sug, ok := d.suggestions[s.Name]; ok && sug.Role != s.Role {
		lines = append(lines, kv(t, "Suggested", t.Control.Allowed.Render(trunc(sug.Role))+t.Control.Muted.Render("  y to use it")))
		if sug.Reason != "" {
			lines = append(lines, kv(t, "", t.Control.Muted.Render(trunc(sug.Reason))))
		}
	}
	return lines
}

// agentRows says how many agents the machine runs at once and where that
// number came from, with a hint when the server runs fewer than the
// settings ask for.
func (d *Machines) agentRows(t *styles.Styles, s fleet.Status) []string {
	n := s.SlotCount()
	val := fmt.Sprintf("%d at once", n)
	if s.Live.Slots > 0 {
		val += t.Control.Muted.Render(" (the server says so)")
	} else {
		val += t.Control.Muted.Render(" (from settings)")
	}
	switch {
	case s.Live.ContextWindow > 0:
		val += t.Control.Muted.Render(fmt.Sprintf(" · %s context each", humanTokens(s.Live.ContextWindow)))
	case s.ContextWindow > 0:
		val += t.Control.Muted.Render(fmt.Sprintf(" · %s context", humanTokens(s.ContextWindow)))
	}
	lines := []string{kv(t, "Agents", val)}
	if want := s.MachineConfig.SlotCount(); s.Live.Slots > 0 && want > s.Live.Slots {
		lines = append(lines, kv(t, "", t.Control.Warn.Render(fmt.Sprintf(
			"settings say %d; restart llama-server with --parallel %d to run them all", want, want))))
	}
	return lines
}

// humanTokens renders a token count as 16K or 1.5M.
func humanTokens(n int64) string {
	switch {
	case n >= 1_000_000 && n%1_000_000 == 0:
		return fmt.Sprintf("%dM", n/1_000_000)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1024 && n%1024 == 0:
		return fmt.Sprintf("%dK", n/1024)
	case n >= 1000:
		return fmt.Sprintf("%dK", n/1000)
	}
	return strconv.FormatInt(n, 10)
}

func (d *Machines) emptyRows(t *styles.Styles) []string {
	m := t.Control.Muted
	return []string{
		"No machines yet.",
		"",
		m.Render("Machines are your other computers running local models (Ollama,"),
		m.Render("LM Studio, llama.cpp). Agents hand side work to them, each running"),
		m.Render("as many agents at once as it has slots."),
		"",
		m.Render("For Ollama, start it with ") + t.Control.Value.Render("OLLAMA_HOST=0.0.0.0") +
			m.Render(" so other computers"),
		m.Render("can reach it, then press ") + t.Control.Value.Render("a") + m.Render(" and type its IP."),
	}
}

func (d *Machines) formRows(t *styles.Styles, width int) []string {
	labelW := 9
	var lines []string
	for i := range d.inputs {
		label := fmt.Sprintf("%-*s", labelW, machineFieldLabels[i])
		if i == d.field {
			label = t.Control.Allowed.Render(label)
		} else {
			label = t.Control.Label.Render(label)
		}
		if i == machineFieldName && d.editing != "" {
			lines = append(lines, label+" "+t.Control.Value.Render(d.editing))
			continue
		}
		d.inputs[i].SetWidth(max(10, width-labelW-4))
		lines = append(lines, label+" "+d.inputs[i].View())
	}
	m := t.Control.Muted
	lines = append(lines, "",
		m.Render("An IP on its own means Ollama on port 11434. Machine agents can"),
		m.Render("only read your code; they never edit files or run commands."),
	)
	return lines
}

func (d *Machines) footer(t *styles.Styles, now time.Time, inner int) string {
	var line string
	switch {
	case d.saving:
		line = t.Control.Muted.Render("Saving and checking…")
	case d.suggesting:
		line = t.Control.Muted.Render("Asking your main AI which role fits each machine…")
	case d.err != "":
		line = t.Control.Alert.Render("! " + d.err)
	case d.notice != "":
		line = t.Control.Muted.Render(d.notice)
	case d.checking:
		line = t.Control.Muted.Render("Checking…")
	case d.form:
		line = t.Control.Muted.Render("Saved to your global config.")
	case !d.updated.IsZero():
		line = t.Control.Muted.Render("Checked " + control.Ago(d.updated, now) + " · r to check again")
	}
	return lipgloss.NewStyle().Padding(0, 1).Render(ansi.Truncate(line, max(0, inner-2), "…"))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// ShortHelp implements help.KeyMap.
func (d *Machines) ShortHelp() []key.Binding {
	km := &d.keyMap
	if d.form {
		return []key.Binding{km.Save, km.Next, km.Back}
	}
	if len(d.statuses) == 0 {
		return []key.Binding{km.Add, km.Main, km.Refresh, km.Close}
	}
	h := []key.Binding{km.Add, km.Edit, km.Suggest}
	if m := d.selected(); m != nil {
		if s, ok := d.suggestions[m.Name]; ok && s.Role != m.Role {
			h = append(h, km.Apply, km.ApplyAl)
		}
	}
	return append(h, km.Main, km.Toggle, km.Remove, km.Refresh, km.Close)
}

// FullHelp implements help.KeyMap.
func (d *Machines) FullHelp() [][]key.Binding {
	return [][]key.Binding{d.ShortHelp()}
}
