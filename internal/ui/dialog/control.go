package dialog

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/control"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// ControlID is the identifier for the control cockpit dialog.
const ControlID = "control"

// ActionControl asks the UI to run control commands. The UI fills in the
// session and routes each reply back through SetReply.
type ActionControl struct {
	Cmds []control.Command
}

// ActionTakeBack asks the UI to take control back: Auto and Edits off,
// and anything waiting denied.
type ActionTakeBack struct{}

// ActionSetEdits asks the UI to turn Edits mode on or off.
type ActionSetEdits struct {
	On bool
}

// ActionOpenControl opens the control cockpit.
type ActionOpenControl struct{}

// ControlPage is one page of the cockpit.
type ControlPage int

const (
	ControlPageOverview ControlPage = iota
	ControlPageCost
	ControlPageActions
	ControlPageMissions
)

var controlPageNames = []string{"Overview", "Cost", "Actions", "Missions"}

func (p ControlPage) String() string { return controlPageNames[p] }

// controlInput says what the cockpit's text input is collecting.
type controlInput int

const (
	controlInputNone controlInput = iota
	controlInputNewMission
	controlInputCriterion
	controlInputNote
)

// controlActionsLimit is how many log entries the Actions page loads.
const controlActionsLimit = 200

// Control is the cockpit: who's in control, what the agent has done, what it
// costs, and the missions it's working on.
type Control struct {
	com    *common.Common
	width  int
	height int
	page   ControlPage

	snap     *control.Snapshot
	report   *control.Report
	actions  []permission.Action
	missions []control.Mission
	loaded   map[control.Op]bool
	updated  time.Time
	err      error
	notice   string

	scroll     int // first visible row on the Actions page
	cursor     int // selected mission
	detail     bool
	critCursor int
	confirmDel string // mission id armed for deletion

	input    textinput.Model
	inputFor controlInput

	help   help.Model
	keyMap struct {
		Next     key.Binding
		Prev     key.Binding
		Page     key.Binding
		Up       key.Binding
		Down     key.Binding
		Refresh  key.Binding
		Edits    key.Binding
		TakeBack key.Binding
		Open     key.Binding
		New      key.Binding
		Use      key.Binding
		Check    key.Binding
		Add      key.Binding
		Note     key.Binding
		Pause    key.Binding
		Done     key.Binding
		Delete   key.Binding
		Submit   key.Binding
		Back     key.Binding
		Close    key.Binding
	}
}

var _ Dialog = (*Control)(nil)

// NewControl creates the cockpit showing snap, which may be nil until the
// first reply arrives. The caller runs the commands in the returned
// action to load the pages.
func NewControl(com *common.Common, snap *control.Snapshot, page ControlPage) (*Control, ActionControl) {
	t := com.Styles
	c := &Control{
		com:    com,
		snap:   snap,
		page:   page,
		loaded: map[control.Op]bool{},
	}
	if snap != nil {
		c.updated = snap.At
	}
	c.help = help.New()
	c.help.Styles = t.DialogHelpStyles()

	c.input = textinput.New()
	c.input.SetVirtualCursor(true)
	c.input.SetStyles(t.TextInput)
	c.input.CharLimit = 200

	b := func(keys []string, k, desc string) key.Binding {
		return key.NewBinding(key.WithKeys(keys...), key.WithHelp(k, desc))
	}
	c.keyMap.Next = b([]string{"tab", "right", "l"}, "tab", "next page")
	c.keyMap.Prev = b([]string{"shift+tab", "left", "h"}, "shift+tab", "prev page")
	c.keyMap.Page = b([]string{"1", "2", "3", "4"}, "1-4", "page")
	c.keyMap.Up = b([]string{"up", "k"}, "↑/k", "up")
	c.keyMap.Down = b([]string{"down", "j"}, "↓/j", "down")
	c.keyMap.Refresh = b([]string{"r"}, "r", "refresh")
	c.keyMap.Edits = b([]string{"e"}, "e", "edits on/off")
	c.keyMap.TakeBack = b([]string{"ctrl+q", "T"}, "ctrl+q", "take back")
	c.keyMap.Open = b([]string{"enter"}, "enter", "open")
	c.keyMap.New = b([]string{"n"}, "n", "new")
	c.keyMap.Use = b([]string{"s"}, "s", "use here")
	c.keyMap.Check = b([]string{"space", " "}, "space", "check")
	c.keyMap.Add = b([]string{"a"}, "a", "add item")
	c.keyMap.Note = b([]string{"n"}, "n", "note")
	c.keyMap.Pause = b([]string{"p"}, "p", "pause/resume")
	c.keyMap.Done = b([]string{"d"}, "d", "done/reopen")
	c.keyMap.Delete = b([]string{"x"}, "x", "delete")
	c.keyMap.Submit = b([]string{"enter"}, "enter", "save")
	c.keyMap.Back = b([]string{"esc"}, "esc", "back")
	c.keyMap.Close = CloseKey
	return c, c.refresh()
}

// ID implements Dialog.
func (c *Control) ID() string { return ControlID }

// Page returns the current page.
func (c *Control) Page() ControlPage { return c.page }

// refresh reloads every page.
func (c *Control) refresh() ActionControl {
	return ActionControl{Cmds: []control.Command{
		{Op: control.OpUsage},
		{Op: control.OpActions, Limit: controlActionsLimit},
		{Op: control.OpMissions},
	}}
}

// SetSnapshot replaces the overview snapshot, for example after the UI's
// background refresh.
func (c *Control) SetSnapshot(s *control.Snapshot) {
	if s == nil {
		return
	}
	c.snap = s
	c.updated = s.At
}

// SetReply stores the answer to a command the cockpit (or the UI) sent.
func (c *Control) SetReply(op control.Op, r control.Reply, err error) {
	if err != nil {
		c.err = err
		c.notice = ""
		return
	}
	c.err = nil
	c.loaded[op] = true
	if r.Snapshot != nil {
		c.SetSnapshot(r.Snapshot)
	}
	if r.Report != nil {
		c.report = r.Report
	}
	if r.Message != "" {
		c.notice = r.Message
	}
	switch op {
	case control.OpActions:
		c.actions = r.Actions
		c.scroll = min(c.scroll, max(0, len(c.actions)-1))
	case control.OpMissions:
		c.missions = r.Missions
	case control.OpMissionDelete:
		c.missions = slices.DeleteFunc(c.missions, func(m control.Mission) bool {
			return m.ID == c.confirmDel
		})
		c.confirmDel = ""
		c.detail = false
		c.notice = "Mission deleted."
	case control.OpMissionAttach:
		if r.Mission != nil && c.snap != nil {
			m := *r.Mission
			c.snap.Mission = &m
			c.notice = fmt.Sprintf("This session now works on %q.", m.Title)
		}
	}
	if r.Mission != nil {
		c.upsertMission(*r.Mission, op == control.OpMissionCreate)
	}
	c.cursor = max(0, min(c.cursor, len(c.missions)-1))
}

func (c *Control) upsertMission(m control.Mission, created bool) {
	if i := slices.IndexFunc(c.missions, func(x control.Mission) bool { return x.ID == m.ID }); i >= 0 {
		c.missions[i] = m
	} else {
		c.missions = append([]control.Mission{m}, c.missions...)
	}
	if created {
		c.cursor = 0
		c.detail = true
		c.critCursor = 0
	}
	if c.snap != nil && c.snap.Mission != nil && c.snap.Mission.ID == m.ID {
		c.snap.Mission = &m
	}
}

func (c *Control) selected() *control.Mission {
	if c.cursor < 0 || c.cursor >= len(c.missions) {
		return nil
	}
	return &c.missions[c.cursor]
}

// HandleMsg implements Dialog.
func (c *Control) HandleMsg(msg tea.Msg) Action {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		if c.inputFor != controlInputNone {
			var cmd tea.Cmd
			c.input, cmd = c.input.Update(msg)
			if cmd != nil {
				return ActionCmd{cmd}
			}
		}
		return nil
	}
	if c.inputFor != controlInputNone {
		return c.handleInputKey(k)
	}
	km := &c.keyMap
	if c.page == ControlPageMissions {
		if a, handled := c.handleMissionKey(k); handled {
			return a
		}
	}
	switch {
	case key.Matches(k, km.Close):
		return ActionClose{}
	case key.Matches(k, km.TakeBack):
		return ActionTakeBack{}
	case key.Matches(k, km.Next):
		c.setPage((c.page + 1) % 4)
	case key.Matches(k, km.Prev):
		c.setPage((c.page + 3) % 4)
	case key.Matches(k, km.Page):
		c.setPage(ControlPage(k.String()[0] - '1'))
	case key.Matches(k, km.Refresh):
		c.notice = ""
		return c.refresh()
	case key.Matches(k, km.Edits):
		if c.snap == nil {
			return nil
		}
		return ActionSetEdits{On: !c.snap.State.Edits}
	case key.Matches(k, km.Up):
		if c.page == ControlPageActions {
			c.scroll = max(0, c.scroll-1)
		}
	case key.Matches(k, km.Down):
		if c.page == ControlPageActions {
			c.scroll = max(0, min(c.scroll+1, len(c.actions)-1))
		}
	}
	return nil
}

func (c *Control) setPage(p ControlPage) {
	c.page = p
	c.detail = false
	c.confirmDel = ""
}

// handleMissionKey handles the Missions page keys. It reports false for
// keys the page doesn't use so the shared keys still work.
func (c *Control) handleMissionKey(k tea.KeyPressMsg) (Action, bool) {
	km := &c.keyMap
	m := c.selected()
	if key.Matches(k, km.Delete) {
		if m == nil {
			return nil, true
		}
		if c.confirmDel != m.ID {
			c.confirmDel = m.ID
			c.notice = "Press x again to delete this mission."
			return nil, true
		}
		return ActionControl{Cmds: []control.Command{{Op: control.OpMissionDelete, MissionID: m.ID}}}, true
	}
	c.confirmDel = ""
	if !c.detail {
		switch {
		case key.Matches(k, km.Up):
			c.cursor = max(0, c.cursor-1)
		case key.Matches(k, km.Down):
			c.cursor = max(0, min(c.cursor+1, len(c.missions)-1))
		case key.Matches(k, km.Open):
			if m != nil {
				c.detail = true
				c.critCursor = 0
			}
		case key.Matches(k, km.New):
			c.startInput(controlInputNewMission, "Mission title")
		case key.Matches(k, km.Use):
			if m != nil {
				return ActionControl{Cmds: []control.Command{{Op: control.OpMissionAttach, MissionID: m.ID}}}, true
			}
		default:
			return nil, false
		}
		return nil, true
	}
	switch {
	case key.Matches(k, km.Back):
		c.detail = false
	case key.Matches(k, km.Up):
		c.critCursor = max(0, c.critCursor-1)
	case key.Matches(k, km.Down):
		c.critCursor = max(0, min(c.critCursor+1, len(m.Criteria)-1))
	case key.Matches(k, km.Check):
		if c.critCursor >= len(m.Criteria) {
			return nil, true
		}
		cr := m.Criteria[c.critCursor]
		op := control.OpMissionCheck
		if cr.Done {
			op = control.OpMissionUncheck
		}
		return ActionControl{Cmds: []control.Command{{Op: op, MissionID: m.ID, Index: cr.ID}}}, true
	case key.Matches(k, km.Add):
		c.startInput(controlInputCriterion, "What has to be true when it's done")
	case key.Matches(k, km.Note):
		c.startInput(controlInputNote, "Note for the mission journal")
	case key.Matches(k, km.Pause):
		st := control.MissionPaused
		if m.Status == control.MissionPaused {
			st = control.MissionActive
		}
		return ActionControl{Cmds: []control.Command{{Op: control.OpMissionStatus, MissionID: m.ID, Status: st}}}, true
	case key.Matches(k, km.Done):
		st := control.MissionDone
		if m.Status == control.MissionDone {
			st = control.MissionActive
		}
		return ActionControl{Cmds: []control.Command{{Op: control.OpMissionStatus, MissionID: m.ID, Status: st}}}, true
	case key.Matches(k, km.Use):
		return ActionControl{Cmds: []control.Command{{Op: control.OpMissionAttach, MissionID: m.ID}}}, true
	default:
		return nil, false
	}
	return nil, true
}

func (c *Control) startInput(kind controlInput, placeholder string) {
	c.inputFor = kind
	c.input.Reset()
	c.input.Placeholder = placeholder
	c.input.Focus()
	c.notice = ""
}

func (c *Control) handleInputKey(k tea.KeyPressMsg) Action {
	switch {
	case key.Matches(k, c.keyMap.Back):
		c.inputFor = controlInputNone
		c.input.Blur()
		return nil
	case key.Matches(k, c.keyMap.Submit):
		text := strings.TrimSpace(c.input.Value())
		kind := c.inputFor
		c.inputFor = controlInputNone
		c.input.Blur()
		if text == "" {
			return nil
		}
		switch kind {
		case controlInputNewMission:
			return ActionControl{Cmds: []control.Command{{Op: control.OpMissionCreate, Title: text}}}
		case controlInputCriterion, controlInputNote:
			m := c.selected()
			if m == nil {
				return nil
			}
			op := control.OpMissionAdd
			if kind == controlInputNote {
				op = control.OpMissionNote
			}
			return ActionControl{Cmds: []control.Command{{Op: op, MissionID: m.ID, Text: text}}}
		}
		return nil
	}
	var cmd tea.Cmd
	c.input, cmd = c.input.Update(k)
	if cmd != nil {
		return ActionCmd{cmd}
	}
	return nil
}

// Draw implements Dialog.
func (c *Control) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := c.com.Styles
	c.width = max(0, min(84, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	// Title, tabs, footer, help and the frame take about ten rows.
	c.height = max(6, min(20, area.Dy()-10))
	DrawCenter(scr, area, c.content(time.Now()))
	return nil
}

func (c *Control) innerWidth() int {
	return max(0, c.width-c.com.Styles.Dialog.View.GetHorizontalFrameSize())
}

func (c *Control) content(now time.Time) string {
	t := c.com.Styles
	inner := c.innerWidth()
	rc := NewRenderContext(t, c.width)
	rc.Title = "Control"
	rc.TitleInfo = c.whoBadge(t)
	rc.AddPart(c.tabs(t, inner))

	var body []string
	switch c.page {
	case ControlPageOverview:
		body = c.overview(t, now)
	case ControlPageCost:
		body = c.cost(t, inner-2)
	case ControlPageActions:
		body = c.actionRows(t, now, inner-2)
	case ControlPageMissions:
		body = c.missionRows(t, now, inner-2)
	}
	rc.AddPart(c.frame(body, inner))
	rc.AddPart(c.footer(t, now, inner))
	rc.Help = renderDialogHelp(t, &c.help, c, inner)
	return rc.Render()
}

// frame pads or clips body to the page height so switching pages doesn't
// make the dialog jump.
func (c *Control) frame(body []string, inner int) string {
	if len(body) > c.height {
		body = body[:c.height]
	}
	for len(body) < c.height {
		body = append(body, "")
	}
	pad := lipgloss.NewStyle().Padding(0, 1)
	out := make([]string, len(body))
	for i, l := range body {
		out[i] = pad.Render(ansi.Truncate(l, max(0, inner-2), "…"))
	}
	return strings.Join(out, "\n")
}

// whoBadge is the title's "who's in control" mark.
func (c *Control) whoBadge(t *styles.Styles) string {
	if c.snap == nil {
		return ""
	}
	st := c.snap.State
	switch {
	case st.Auto:
		return " " + t.Control.Alert.Render("AUTO")
	case st.Edits:
		return " " + t.Control.Warn.Render("EDITS")
	default:
		return " " + t.Control.On.Render("YOU")
	}
}

func (c *Control) tabs(t *styles.Styles, inner int) string {
	var full []string
	for i, name := range controlPageNames {
		label := fmt.Sprintf("%d %s", i+1, name)
		if ControlPage(i) == c.page {
			full = append(full, t.Control.TabOn.Render(label))
		} else {
			full = append(full, t.Control.Tab.Render(label))
		}
	}
	row := strings.Join(full, " ")
	if lipgloss.Width(row)+2 > inner {
		// Narrow: one page at a time with arrows.
		row = t.Control.Muted.Render("‹ ") +
			t.Control.TabOn.Render(fmt.Sprintf("%d/4 %s", c.page+1, c.page)) +
			t.Control.Muted.Render(" ›")
	}
	return lipgloss.NewStyle().Padding(0, 1).Render(row) + "\n"
}

func (c *Control) footer(t *styles.Styles, now time.Time, inner int) string {
	var line string
	switch {
	case c.inputFor != controlInputNone:
		c.input.SetWidth(max(10, inner-6))
		line = t.Dialog.InputPrompt.Render(c.input.View())
		return line
	case c.err != nil:
		line = t.Control.Alert.Render("! " + c.err.Error())
	case c.notice != "":
		line = t.Control.Muted.Render(c.notice)
	case c.updated.IsZero():
		line = t.Control.Muted.Render("Loading…")
	default:
		line = t.Control.Muted.Render("Updated " + control.Ago(c.updated, now) + " · r to refresh")
	}
	return lipgloss.NewStyle().Padding(0, 1).Render(ansi.Truncate(line, max(0, inner-2), "…"))
}

// kv renders a label column and a value.
func kv(t *styles.Styles, label, value string) string {
	return t.Control.Label.Render(fmt.Sprintf("%-10s", label)) + " " + value
}

func (c *Control) overview(t *styles.Styles, now time.Time) []string {
	s := c.snap
	if s == nil {
		return []string{t.Control.Muted.Render("Loading…")}
	}
	st := s.State
	var who string
	switch {
	case st.Auto:
		who = t.Control.Alert.Render("Auto") + t.Control.Muted.Render(" · the agent acts without asking")
	case st.Edits:
		who = t.Control.Warn.Render("Edits") + t.Control.Muted.Render(" · project edits run, everything else asks")
	default:
		who = t.Control.On.Render("You") + t.Control.Muted.Render(" · the agent asks before acting")
	}
	lines := []string{kv(t, "In control", who)}
	if !st.TakenBackAt.IsZero() {
		lines = append(lines, kv(t, "", t.Control.Muted.Render("Taken back "+control.Ago(st.TakenBackAt, now))))
	}
	if st.Waiting {
		lines = append(lines, kv(t, "", t.Control.Warn.Render("A request is waiting for your answer")))
	}
	lines = append(lines, kv(t, "Always ask", c.alwaysAsk(t, st.Policy)))
	if caps := autoBudgets(st.Policy); caps != "" {
		lines = append(lines, kv(t, "Auto caps", t.Control.Value.Render(caps)+t.Control.Muted.Render(" per session")))
	}
	lines = append(lines, kv(t, "Actions", t.Control.Value.Render(fmt.Sprintf("%d", s.SessionActions))+t.Control.Muted.Render(" this session")))
	lines = append(lines, "")

	if !s.HasLedger {
		lines = append(lines, kv(t, "Spend", t.Control.Muted.Render("n/a · usage isn't recorded")))
	} else {
		lines = append(lines,
			kv(t, "Session", spendText(t, s.Session)),
			kv(t, "Today", spendText(t, s.Today)),
			kv(t, "Month", spendText(t, s.Month)),
		)
	}
	for _, b := range s.Budgets {
		lines = append(lines, budgetLine(t, b))
	}
	lines = append(lines, "")
	if m := s.Mission; m != nil {
		done, total := m.Progress()
		lines = append(lines, kv(t, "Mission", missionLine(t, *m, done, total)))
	} else {
		lines = append(lines, kv(t, "Mission", t.Control.Muted.Render("none · 4 to pick or start one")))
	}
	return lines
}

func (c *Control) alwaysAsk(t *styles.Styles, p permission.Policy) string {
	var names []string
	for _, cl := range permission.Classes {
		if p.Asks(cl) {
			names = append(names, cl.Label())
		}
	}
	return t.Control.Value.Render(strings.Join(names, ", "))
}

func autoBudgets(p permission.Policy) string {
	var parts []string
	for _, cl := range permission.Classes {
		if n, ok := p.AutoBudget[cl]; ok {
			parts = append(parts, fmt.Sprintf("%s %d", cl.Label(), n))
		}
	}
	return strings.Join(parts, ", ")
}

// spendText shows counted spend with the honesty notes: unpriced calls
// are flagged rather than counted as $0, and flat-rate usage is shown
// apart from spend.
func spendText(t *styles.Styles, tot control.Totals) string {
	var b strings.Builder
	switch {
	case tot.Calls > 0:
		b.WriteString(t.Control.Value.Render(control.Money(tot.Cost)))
		b.WriteString(t.Control.Muted.Render(fmt.Sprintf(" · %s tok · %d calls", control.Tokens(tot.Tokens), tot.Calls)))
	case tot.Unpriced == 0 && tot.FlatCalls == 0:
		b.WriteString(t.Control.Muted.Render("$0 · no calls"))
	default:
		b.WriteString(t.Control.Muted.Render("n/a"))
	}
	if tot.Unpriced > 0 {
		b.WriteString(t.Control.Warn.Render(fmt.Sprintf(" · %d unpriced", tot.Unpriced)))
	}
	if tot.FlatCalls > 0 {
		b.WriteString(t.Control.Muted.Render(fmt.Sprintf(" · %d flat-rate (≈%s, not spend)", tot.FlatCalls, control.Money(tot.Flat))))
	}
	return b.String()
}

func budgetLine(t *styles.Styles, b control.BudgetStatus) string {
	style := t.Control.Pip
	switch {
	case b.Over:
		style = t.Control.Alert
	case b.Warn > 0:
		style = t.Control.Warn
	}
	label := b.Window.Label() + " cap"
	text := fmt.Sprintf(" %3.0f%% %s of %s", b.Pct, control.Money(b.Spent), control.Money(b.Limit))
	if b.Limit > b.Base {
		text += t.Control.Muted.Render(" (extended from " + control.Money(b.Base) + ")")
	}
	return kv(t, label, style.Render(control.Ladder(b.Pct, 12))+text)
}

func missionLine(t *styles.Styles, m control.Mission, done, total int) string {
	glyph := control.MissionGlyph(m.Status, done, total)
	s := t.Control.Value.Render(glyph+" "+m.Title) + " "
	if total > 0 {
		s += t.Control.Pip.Render(control.Pips(done, total, 10)) +
			t.Control.Muted.Render(fmt.Sprintf(" %d/%d", done, total))
	} else {
		s += t.Control.Muted.Render("no checklist yet")
	}
	if m.Status != control.MissionActive {
		s += t.Control.Muted.Render(" · " + string(m.Status))
	}
	return s
}

func (c *Control) cost(t *styles.Styles, width int) []string {
	r := c.report
	if r == nil {
		if c.snap != nil && !c.snap.HasLedger {
			return []string{t.Control.Muted.Render("Usage isn't recorded, so there's nothing to show.")}
		}
		return []string{t.Control.Muted.Render("Loading…")}
	}
	var total float64
	for _, v := range r.Daily {
		total += v
	}
	lines := []string{
		kv(t, "14 days", t.Control.Pip.Render(control.Sparkline(r.Daily))+" "+t.Control.Value.Render(control.Money(total))),
		kv(t, "", t.Control.Muted.Render("oldest → today")),
	}
	if r.HasBurn {
		lines = append(lines, kv(t, "Burn", t.Control.Value.Render(control.Money(r.Burn)+"/h")+t.Control.Muted.Render(" over the last hour")))
	} else {
		lines = append(lines, kv(t, "Burn", t.Control.Muted.Render("n/a · not enough recent calls")))
	}
	lines = append(lines, kv(t, "Last call", t.Control.Muted.Render(control.Ago(r.LastUsage, time.Now()))), "")
	lines = append(lines, t.Control.Label.Render("This month by model"))
	if len(r.Models) == 0 {
		lines = append(lines, t.Control.Muted.Render("  No calls this month."))
	}
	nameW := max(8, min(28, width-34))
	for _, m := range r.Models {
		name := ansi.Truncate(m.Model, nameW, "…")
		var cost string
		switch {
		case m.Pricing.Counted():
			cost = t.Control.Value.Render(fmt.Sprintf("%9s", control.Money(m.Cost)))
		case m.Pricing == control.PricingFlat:
			cost = t.Control.Muted.Render(fmt.Sprintf("%9s", "flat"))
		default:
			cost = t.Control.Warn.Render(fmt.Sprintf("%9s", "n/a"))
		}
		lines = append(lines, fmt.Sprintf("  %-*s %s %s", nameW, name, cost,
			t.Control.Muted.Render(fmt.Sprintf("%7s tok %4d calls", control.Tokens(m.Tokens), m.Calls))))
	}
	lines = append(lines, "",
		t.Control.Muted.Render("List-price estimates, not bills. Unpriced calls are NOT counted as $0;"),
		t.Control.Muted.Render("flat-rate (subscription) usage is shown apart and isn't spend."),
	)
	return lines
}

func (c *Control) actionRows(t *styles.Styles, now time.Time, width int) []string {
	if !c.loaded[control.OpActions] {
		return []string{t.Control.Muted.Render("Loading…")}
	}
	if len(c.actions) == 0 {
		return []string{t.Control.Muted.Render("Nothing yet. Every tool the agent asks to run shows up here, with who allowed it.")}
	}
	lines := []string{t.Control.Muted.Render(fmt.Sprintf("%d most recent in this session, newest first", len(c.actions)))}
	start := max(0, min(c.scroll, len(c.actions)-1))
	for _, a := range c.actions[start:] {
		lines = append(lines, actionLine(t, a, now, width))
	}
	return lines
}

// ActionMark is the ✓ ✕ ! glyph for an action.
func ActionMark(t *styles.Styles, a permission.Action) string {
	switch a.Decision {
	case permission.DecisionAllowed:
		return t.Control.Allowed.Render("✓")
	case permission.DecisionDenied:
		return t.Control.Denied.Render("✕")
	default:
		return t.Control.Canceled.Render("!")
	}
}

func actionLine(t *styles.Styles, a permission.Action, now time.Time, width int) string {
	when := fmt.Sprintf("%-8s", control.Ago(a.At, now))
	class := ""
	if a.Class != "" {
		class = fmt.Sprintf("%-8s", ansi.Truncate(a.Class.Label(), 8, "…"))
	}
	via := ""
	if a.Via != "" {
		via = " · " + string(a.Via)
	}
	head := ActionMark(t, a) + " " + t.Control.Muted.Render(when) + " " + t.Control.Label.Render(class) + " "
	room := max(4, width-lipgloss.Width(head)-lipgloss.Width(via))
	return head + t.Control.Value.Render(ansi.Truncate(a.Summary(), room, "…")) + t.Control.Muted.Render(via)
}

func (c *Control) missionRows(t *styles.Styles, now time.Time, width int) []string {
	if !c.loaded[control.OpMissions] && len(c.missions) == 0 {
		return []string{t.Control.Muted.Render("Loading…")}
	}
	if c.detail {
		if m := c.selected(); m != nil {
			return c.missionDetail(t, *m, now, width)
		}
	}
	if len(c.missions) == 0 {
		return []string{
			t.Control.Muted.Render("No missions yet. A mission is a goal with a checklist that"),
			t.Control.Muted.Render("survives sessions, so the agent can pick up where it left off."),
			"",
			t.Control.Muted.Render("Press n to start one."),
		}
	}
	current := ""
	if c.snap != nil && c.snap.Mission != nil {
		current = c.snap.Mission.ID
	}
	var lines []string
	// Keep the cursor in view.
	first := max(0, c.cursor-c.height+2)
	for i := first; i < len(c.missions); i++ {
		m := c.missions[i]
		done, total := m.Progress()
		mark := "  "
		if m.ID == current {
			mark = t.Control.On.Render("▸ ")
		}
		row := mark + missionLine(t, m, done, total)
		if i == c.cursor {
			plain := ansi.Strip(row)
			row = t.Dialog.SelectedItem.UnsetPadding().Render(ansi.Truncate(plain, width, "…") + strings.Repeat(" ", max(0, width-lipgloss.Width(plain))))
		}
		lines = append(lines, row)
	}
	if current != "" {
		lines = append(lines, "", t.Control.Muted.Render("▸ is this session's mission"))
	}
	return lines
}

func (c *Control) missionDetail(t *styles.Styles, m control.Mission, now time.Time, width int) []string {
	done, total := m.Progress()
	lines := []string{missionLine(t, m, done, total)}
	if m.Goal != "" {
		lines = append(lines, t.Control.Muted.Render(ansi.Truncate(m.Goal, width, "…")))
	}
	lines = append(lines, "")
	if total == 0 {
		lines = append(lines, t.Control.Muted.Render("No checklist yet. Press a to add what has to be true when it's done."))
	}
	for i, cr := range m.Criteria {
		box := t.Control.PipEmpty.Render("☐")
		if cr.Done {
			box = t.Control.Allowed.Render("☑")
		}
		text := cr.Text
		if cr.Done && cr.Note != "" {
			text += " — " + cr.Note
		}
		row := box + " " + ansi.Truncate(text, max(4, width-2), "…")
		if i == c.critCursor {
			plain := ansi.Strip(row)
			row = t.Dialog.SelectedItem.UnsetPadding().Render(plain + strings.Repeat(" ", max(0, width-lipgloss.Width(plain))))
		}
		lines = append(lines, row)
	}
	if n := len(m.Notes); n > 0 {
		last := m.Notes[n-1]
		lines = append(lines, "", t.Control.Label.Render("Latest note ")+t.Control.Muted.Render(control.Ago(last.At, now)),
			t.Control.Muted.Render(ansi.Truncate(last.Text, width, "…")))
	}
	return lines
}

// ShortHelp implements help.KeyMap.
func (c *Control) ShortHelp() []key.Binding {
	km := &c.keyMap
	if c.inputFor != controlInputNone {
		return []key.Binding{km.Submit, km.Back}
	}
	if c.page == ControlPageMissions {
		if c.detail {
			return []key.Binding{km.Check, km.Add, km.Note, km.Pause, km.Done, km.Delete, km.Back}
		}
		return []key.Binding{km.Open, km.New, km.Use, km.Delete, km.Next, km.Close}
	}
	if c.page == ControlPageActions {
		return []key.Binding{km.Up, km.Down, km.TakeBack, km.Next, km.Close}
	}
	return []key.Binding{km.TakeBack, km.Edits, km.Refresh, km.Next, km.Close}
}

// FullHelp implements help.KeyMap.
func (c *Control) FullHelp() [][]key.Binding {
	return [][]key.Binding{c.ShortHelp()}
}
