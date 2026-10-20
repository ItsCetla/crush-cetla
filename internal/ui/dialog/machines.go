package dialog

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/control"
	"github.com/charmbracelet/crush/internal/fleet"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// MachinesID is the identifier for the machines dialog.
const MachinesID = "machines"

// ActionOpenMachines opens the machines dialog.
type ActionOpenMachines struct{}

// ActionCheckMachines asks the UI to probe every machine and hand the
// result back through SetStatuses.
type ActionCheckMachines struct{}

// ActionSaveMachine asks the UI to run a machines change (add, update or
// remove) and hand the result back through SetResult.
type ActionSaveMachine struct {
	Params fleet.Params
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
	machineFieldCount
)

var machineFieldLabels = [machineFieldCount]string{"Name", "Address", "Slots", "Hardware", "Model"}

var machineFieldPlaceholders = [machineFieldCount]string{
	"m1",
	"192.168.1.20 or http://192.168.1.20:11434/v1",
	"how many agents at once, e.g. 4",
	`what it is, e.g. "M1 Max"`,
	"optional: empty uses the first one it lists",
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
	km.Next = b([]string{"tab", "down"}, "tab", "next field")
	km.Prev = b([]string{"shift+tab", "up"}, "shift+tab", "prev field")
	km.Save = b([]string{"enter", "ctrl+s"}, "enter", "next/save")
	km.Back = b([]string{"esc"}, "esc", "cancel")
	km.Close = CloseKey
	return d, ActionCheckMachines{}
}

// ID implements Dialog.
func (d *Machines) ID() string { return MachinesID }

// SetStatuses shows the result of a check.
func (d *Machines) SetStatuses(s []fleet.Status, at time.Time) {
	d.statuses = s
	d.loaded = true
	d.checking = false
	d.updated = at
	d.cursor = max(0, min(d.cursor, len(s)-1))
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
		d.inputs[machineFieldSlots].SetValue(strconv.Itoa(m.SlotCount()))
		d.inputs[machineFieldHardware].SetValue(m.Hardware)
		d.inputs[machineFieldModel].SetValue(m.MachineConfig.Model)
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
	d.height = max(6, min(13, area.Dy()-8))
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

// summary is the title's "3 online, 5 of 7 slots free" note.
func (d *Machines) summary(t *styles.Styles) string {
	if d.form || !d.loaded || len(d.statuses) == 0 {
		return ""
	}
	online, free, total := 0, 0, 0
	for _, s := range d.statuses {
		if s.Online {
			online++
			free += s.Free()
			total += s.SlotCount()
		}
	}
	return " " + t.Control.Muted.Render(fmt.Sprintf("%d online · %d of %d slots free", online, free, total))
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

	// Leave room for the selected machine's details below the list.
	rows := max(1, d.height-6)
	start := 0
	if d.cursor >= rows {
		start = d.cursor - rows + 1
	}
	var lines []string
	for i := start; i < len(d.statuses) && i < start+rows; i++ {
		lines = append(lines, d.machineLine(t, d.statuses[i], i == d.cursor, nameW, hwW, width))
	}
	if m := d.selected(); m != nil {
		lines = append(lines, "")
		lines = append(lines, d.detailRows(t, *m, width)...)
	}
	return lines
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
		lines = append(lines, kv(t, "Status", "online, ready for agents"))
	}
	if len(s.Tags) > 0 {
		lines = append(lines, kv(t, "Good for", trunc(strings.Join(s.Tags, ", "))))
	}
	return lines
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
		return []key.Binding{km.Add, km.Refresh, km.Close}
	}
	return []key.Binding{km.Add, km.Edit, km.Toggle, km.Remove, km.Refresh, km.Close}
}

// FullHelp implements help.KeyMap.
func (d *Machines) FullHelp() [][]key.Binding {
	return [][]key.Binding{d.ShortHelp()}
}
