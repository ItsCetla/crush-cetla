package dialog

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/control"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
)

// SwarmConfirmID is the identifier for the dialog that confirms a swarm
// plan before anything runs.
const SwarmConfirmID = "swarm_confirm"

// MergeBoardID is the identifier for the swarm merge board.
const MergeBoardID = "merge_board"

type (
	// ActionStartSwarm asks the UI to plan a swarm for a mission and show
	// the plan for confirmation. An empty MissionID means the mission
	// attached to the session in view.
	ActionStartSwarm struct {
		MissionID string
	}
	// ActionSwarmLaunch starts the swarm plan the user confirmed.
	ActionSwarmLaunch struct {
		MissionID string
		Assign    map[string][]int
	}
	// ActionOpenMergeBoard opens the merge board.
	ActionOpenMergeBoard struct{}
)

// SwarmConfirm shows which machine a swarm would give which criteria,
// and starts it only when the user says yes.
type SwarmConfirm struct {
	com   *common.Common
	width int
	plan  control.SwarmPlan

	help   help.Model
	keyMap struct {
		Yes key.Binding
		No  key.Binding
	}
}

var _ Dialog = (*SwarmConfirm)(nil)

// NewSwarmConfirm creates the confirmation for plan.
func NewSwarmConfirm(com *common.Common, plan control.SwarmPlan) *SwarmConfirm {
	d := &SwarmConfirm{com: com, plan: plan}
	d.help = help.New()
	d.help.Styles = com.Styles.DialogHelpStyles()
	d.keyMap.Yes = key.NewBinding(
		key.WithKeys("y", "Y"),
		key.WithHelp("y", "start swarm"),
	)
	d.keyMap.No = key.NewBinding(
		key.WithKeys("n", "N", "esc", "alt+esc"),
		key.WithHelp("n/esc", "cancel"),
	)
	if len(plan.Assignments) == 0 {
		d.keyMap.Yes.SetEnabled(false)
		d.keyMap.No.SetHelp("esc", "close")
	}
	return d
}

// ID implements Dialog.
func (d *SwarmConfirm) ID() string { return SwarmConfirmID }

// HandleMsg implements Dialog.
func (d *SwarmConfirm) HandleMsg(msg tea.Msg) Action {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch {
	case key.Matches(k, d.keyMap.Yes):
		if len(d.plan.Assignments) == 0 {
			return nil
		}
		return ActionSwarmLaunch{MissionID: d.plan.MissionID, Assign: d.plan.Assign()}
	case key.Matches(k, d.keyMap.No):
		return ActionClose{}
	}
	return nil
}

// Draw implements Dialog.
func (d *SwarmConfirm) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := d.com.Styles
	d.width = max(0, min(76, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	inner := max(0, d.width-t.Dialog.View.GetHorizontalFrameSize())
	width := max(0, inner-2)
	rc := NewRenderContext(t, d.width)
	rc.Title = "Start Swarm?"
	if n := len(d.plan.Assignments); n > 0 {
		rc.TitleInfo = " " + t.Control.Muted.Render(countOf(n, "machine", "machines"))
	}

	// Title, help and the frame take about six rows.
	maxRows := max(3, area.Dy()-6)
	lines := d.lines(t)
	if len(lines) > maxRows {
		more := len(lines) - maxRows + 1
		lines = append(lines[:maxRows-1], t.Control.Muted.Render(fmt.Sprintf("… %d more lines", more)))
	}
	for i, l := range lines {
		lines[i] = " " + fitLine(l, width) + " "
	}
	rc.AddPart(strings.Join(lines, "\n"))
	rc.Help = renderDialogHelp(t, &d.help, d, inner)
	DrawCenter(scr, area, rc.Render())
	return nil
}

func (d *SwarmConfirm) lines(t *styles.Styles) []string {
	m := t.Control.Muted
	p := d.plan
	title := p.MissionTitle
	if title == "" {
		title = p.MissionID
	}
	lines := []string{t.Control.Label.Render("◎ " + oneLine(title))}
	if len(p.Assignments) > 0 {
		lines = append(lines, m.Render(fmt.Sprintf("%s split across %s:",
			countOf(p.Open, "open criterion", "open criteria"),
			countOf(len(p.Assignments), "machine", "machines"))), "")
	}
	for _, a := range p.Assignments {
		head := t.Control.Value.Render("● " + a.Machine)
		if a.Model != "" {
			head += "  " + m.Render(a.Model)
		}
		head += "  " + m.Render("→ "+a.Branch)
		lines = append(lines, head)
		for i, id := range a.Criteria {
			text := ""
			if i < len(a.CriteriaText) {
				text = oneLine(escapeHidden(a.CriteriaText[i]))
			}
			lines = append(lines, "  "+t.Control.Label.Render(fmt.Sprintf("#%d", id))+" "+text)
		}
	}
	if len(p.Skipped) > 0 {
		lines = append(lines, "", m.Render("Left out"))
		for _, s := range p.Skipped {
			lines = append(lines, "  "+t.Control.Label.Render(s.Machine)+"  "+m.Render(oneLine(s.Reason)))
		}
	}
	lines = append(lines, "")
	switch {
	case p.Open == 0:
		lines = append(lines, t.Control.Warn.Render("The mission has no open criteria, so there's nothing to split."))
	case len(p.Assignments) == 0:
		lines = append(lines, t.Control.Warn.Render("No machine is ready. Turn one on in Machines, then try again."))
	default:
		lines = append(lines,
			m.Render("Each machine works in its own worktree and branch. Every tool"),
			m.Render("call asks your permission, and nothing merges until you do it"),
			m.Render("on the merge board."),
		)
	}
	return lines
}

// ShortHelp implements help.KeyMap.
func (d *SwarmConfirm) ShortHelp() []key.Binding {
	return []key.Binding{d.keyMap.Yes, d.keyMap.No}
}

// FullHelp implements help.KeyMap.
func (d *SwarmConfirm) FullHelp() [][]key.Binding {
	return [][]key.Binding{d.ShortHelp()}
}

// mergeCard is one assignment on the merge board.
type mergeCard struct {
	run int
	idx int
}

// MergeBoard shows every swarm assignment as a card, with its branch,
// checks, cost and diff, and merges or discards each one.
type MergeBoard struct {
	com    *common.Common
	width  int
	height int
	now    func() time.Time

	runs   []control.SwarmRun
	cards  []mergeCard
	loaded bool
	busy   string
	notice string
	err    string

	cursor int
	offset int
	// focus is the run whose first card the cursor goes to once it loads.
	focus string
	// discard is the card armed for discarding, as "run/machine".
	discard string

	expanded   bool
	diffKey    string
	diff       []control.SwarmFileDiff
	diffLoaded bool
	diffErr    string
	diffOffset int
	diffLines  []string
	diffWidth  int

	help   help.Model
	keyMap struct {
		Up      key.Binding
		Down    key.Binding
		Diff    key.Binding
		Merge   key.Binding
		Discard key.Binding
		Yes     key.Binding
		Refresh key.Binding
		Back    key.Binding
		Close   key.Binding
	}
}

var _ Dialog = (*MergeBoard)(nil)

// NewMergeBoard creates the merge board. The caller loads it with the
// commands in the returned action.
func NewMergeBoard(com *common.Common, focus string) (*MergeBoard, ActionControl) {
	d := &MergeBoard{com: com, focus: focus, now: time.Now}
	d.help = help.New()
	d.help.Styles = com.Styles.DialogHelpStyles()
	b := func(keys []string, k, desc string) key.Binding {
		return key.NewBinding(key.WithKeys(keys...), key.WithHelp(k, desc))
	}
	d.keyMap.Up = b([]string{"up", "k", "ctrl+p"}, "↑↓", "choose")
	d.keyMap.Down = b([]string{"down", "j", "ctrl+n"}, "↓", "down")
	d.keyMap.Diff = b([]string{"enter"}, "enter", "diff")
	d.keyMap.Merge = b([]string{"m"}, "m", "merge")
	d.keyMap.Discard = b([]string{"d", "x"}, "d", "discard")
	d.keyMap.Yes = b([]string{"y", "Y"}, "y", "discard it")
	d.keyMap.Refresh = b([]string{"r"}, "r", "refresh")
	d.keyMap.Back = b([]string{"esc", "alt+esc"}, "esc", "back")
	d.keyMap.Close = CloseKey
	return d, d.refresh()
}

// ID implements Dialog.
func (d *MergeBoard) ID() string { return MergeBoardID }

func (d *MergeBoard) refresh() ActionControl {
	return ActionControl{Cmds: []control.Command{{Op: control.OpSwarmRuns}}}
}

// Running reports whether any swarm on the board is still working, so
// the caller knows to keep refreshing it.
func (d *MergeBoard) Running() bool {
	for _, r := range d.runs {
		if r.Running() {
			return true
		}
	}
	return false
}

// SetReply applies the reply to a swarm command the board sent.
func (d *MergeBoard) SetReply(op control.Op, r control.Reply, err error) {
	switch op {
	case control.OpSwarmRuns:
		d.loaded = true
		if err != nil {
			d.err = err.Error()
			return
		}
		d.setRuns(r.Swarms)
	case control.OpSwarmStart:
		if err == nil && r.Swarm != nil {
			d.focus = r.Swarm.ID
			d.putRun(*r.Swarm)
		}
	case control.OpSwarmMerge, control.OpSwarmDiscard:
		d.busy = ""
		if err != nil {
			d.err, d.notice = err.Error(), ""
			return
		}
		d.err, d.notice = "", r.Message
		if r.Swarm != nil {
			d.putRun(*r.Swarm)
		}
	case control.OpSwarmDiff:
		// A diff that comes back after the cursor moved to another card
		// belongs to the card it was asked for.
		if !d.expanded || r.RunID+"/"+r.Machine != d.diffKey {
			return
		}
		d.diffLoaded = true
		d.diffLines = nil
		if err != nil {
			d.diffErr = err.Error()
			return
		}
		d.diffErr = ""
		d.diff = r.Diff
	}
}

// putRun replaces the run with the same id, or adds it first.
func (d *MergeBoard) putRun(run control.SwarmRun) {
	runs := make([]control.SwarmRun, 0, len(d.runs)+1)
	found := false
	for _, r := range d.runs {
		if r.ID == run.ID {
			r, found = run, true
		}
		runs = append(runs, r)
	}
	if !found {
		runs = append([]control.SwarmRun{run}, runs...)
	}
	d.setRuns(runs)
}

// setRuns fills the board, keeping the cursor on the card it was on.
func (d *MergeBoard) setRuns(runs []control.SwarmRun) {
	d.loaded = true
	prev := d.selectedKey()
	d.runs = runs
	d.cards = d.cards[:0]
	for ri, r := range runs {
		for ai := range r.Assignments {
			d.cards = append(d.cards, mergeCard{run: ri, idx: ai})
		}
	}
	d.cursor = 0
	for i, c := range d.cards {
		r := d.runs[c.run]
		if d.focus != "" {
			if r.ID == d.focus {
				d.cursor = i
				break
			}
			continue
		}
		if cardKey(r, r.Assignments[c.idx]) == prev {
			d.cursor = i
			break
		}
	}
	d.focus = ""
}

func cardKey(r control.SwarmRun, a control.SwarmAssignment) string {
	return r.ID + "/" + a.Machine
}

func (d *MergeBoard) selected() (control.SwarmRun, control.SwarmAssignment, bool) {
	if d.cursor < 0 || d.cursor >= len(d.cards) {
		return control.SwarmRun{}, control.SwarmAssignment{}, false
	}
	c := d.cards[d.cursor]
	r := d.runs[c.run]
	return r, r.Assignments[c.idx], true
}

func (d *MergeBoard) selectedKey() string {
	r, a, ok := d.selected()
	if !ok {
		return ""
	}
	return cardKey(r, a)
}

// HandleMsg implements Dialog.
func (d *MergeBoard) HandleMsg(msg tea.Msg) Action {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	km := &d.keyMap
	if d.discard != "" {
		armed := d.discard
		d.discard = ""
		if key.Matches(k, km.Yes) && armed == d.selectedKey() {
			r, a, _ := d.selected()
			d.busy = "Discarding " + a.Machine + "'s branch…"
			d.err, d.notice = "", ""
			return ActionControl{Cmds: []control.Command{{Op: control.OpSwarmDiscard, RunID: r.ID, Machine: a.Machine}}}
		}
		d.notice = "Kept it."
		return nil
	}
	if d.expanded {
		switch {
		case key.Matches(k, km.Back), key.Matches(k, km.Diff):
			d.expanded = false
		case key.Matches(k, km.Up):
			d.diffOffset = max(0, d.diffOffset-1)
		case key.Matches(k, km.Down):
			d.diffOffset++
		case key.Matches(k, km.Merge), key.Matches(k, km.Discard):
			d.expanded = false
			return d.HandleMsg(msg)
		}
		return nil
	}
	switch {
	case key.Matches(k, km.Close):
		return ActionClose{}
	case key.Matches(k, km.Up):
		if len(d.cards) > 0 {
			d.cursor = (d.cursor - 1 + len(d.cards)) % len(d.cards)
		}
	case key.Matches(k, km.Down):
		if len(d.cards) > 0 {
			d.cursor = (d.cursor + 1) % len(d.cards)
		}
	case key.Matches(k, km.Refresh):
		return d.refresh()
	case key.Matches(k, km.Diff):
		r, a, ok := d.selected()
		if !ok {
			return nil
		}
		d.expanded = true
		d.diffOffset = 0
		if key := cardKey(r, a); key != d.diffKey || !d.diffLoaded || d.diffErr != "" {
			d.diffKey = key
			d.diff, d.diffLines, d.diffLoaded, d.diffErr = nil, nil, false, ""
			return ActionControl{Cmds: []control.Command{{Op: control.OpSwarmDiff, RunID: r.ID, Machine: a.Machine}}}
		}
	case key.Matches(k, km.Merge):
		r, a, ok := d.selected()
		if !ok || d.busy != "" {
			return nil
		}
		if reason := mergeBlocked(a); reason != "" {
			d.err, d.notice = reason, ""
			return nil
		}
		target := r.Target
		if target == "" {
			target = "the current branch"
		}
		d.busy = "Merging " + a.Branch + " into " + target + "…"
		d.err, d.notice = "", ""
		return ActionControl{Cmds: []control.Command{{Op: control.OpSwarmMerge, RunID: r.ID, Machine: a.Machine}}}
	case key.Matches(k, km.Discard):
		r, a, ok := d.selected()
		if !ok || d.busy != "" {
			return nil
		}
		switch {
		case a.Status.Settled():
			d.err, d.notice = a.Machine+"'s branch was already "+string(a.Status)+".", ""
			return nil
		case !a.Status.Finished():
			d.err, d.notice = a.Machine+" is still working; wait for it to finish.", ""
			return nil
		}
		d.discard = cardKey(r, a)
		d.err, d.notice = "", ""
	}
	return nil
}

// mergeBlocked says why an assignment can't be merged yet, or "".
func mergeBlocked(a control.SwarmAssignment) string {
	switch a.Status {
	case control.SwarmDone:
		return ""
	case control.SwarmPlanned, control.SwarmRunning:
		return a.Machine + " is still working; wait for it to finish."
	case control.SwarmFailed:
		return a.Machine + " didn't finish, so there's nothing to merge; discard it."
	}
	return a.Machine + "'s branch was already " + string(a.Status) + "."
}

// Draw implements Dialog.
func (d *MergeBoard) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := d.com.Styles
	d.width = max(0, min(100, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	// Title, footer, help and the frame take about eight rows.
	d.height = max(3, min(24, area.Dy()-8))
	DrawCenter(scr, area, d.content())
	return nil
}

func (d *MergeBoard) content() string {
	t := d.com.Styles
	inner := max(0, d.width-t.Dialog.View.GetHorizontalFrameSize())
	rc := NewRenderContext(t, d.width)
	rc.Title = "Merge Board"
	if len(d.cards) > 0 {
		rc.TitleInfo = " " + t.Control.Muted.Render(countOf(len(d.cards), "branch", "branches"))
	}

	width := max(0, inner-2)
	var rows []string
	var total, offset int
	if d.expanded {
		rows, total, offset = d.diffRows(t, max(0, width-1))
	} else {
		rows, total, offset = d.cardRows(t, max(0, width-1))
	}
	scroll := total > d.height
	rowW := width
	if scroll {
		rowW = max(0, width-1)
	}
	for len(rows) < d.height {
		rows = append(rows, "")
	}
	for i, l := range rows {
		rows[i] = " " + fitLine(l, rowW) + " "
	}
	list := strings.Join(rows, "\n")
	if scroll {
		list = joinScrollbar(t, list, d.height, total, d.height, offset)
	}
	rc.AddPart(list)

	var foot string
	switch {
	case d.busy != "":
		foot = t.Control.Muted.Render(d.busy)
	case d.discard != "":
		_, a, _ := d.selected()
		foot = t.Control.Warn.Render("Discard " + a.Machine + "'s worktree and branch for good? y discards it, any other key keeps it.")
	case d.err != "":
		foot = t.Control.Alert.Render("! " + oneLine(d.err))
	case d.notice != "":
		foot = t.Control.Muted.Render(oneLine(d.notice))
	case d.Running():
		foot = t.Control.Muted.Render("Agents are working; the board refreshes itself.")
	default:
		foot = t.Control.Muted.Render("Merging ticks only the criteria whose checks passed.")
	}
	rc.AddPart(" " + fitLine(foot, width) + " ")
	rc.Help = renderDialogHelp(t, &d.help, d, inner)
	return rc.Render()
}

// cardRows renders the cards around the cursor, with the total row count
// and the offset of the first row shown.
func (d *MergeBoard) cardRows(t *styles.Styles, width int) ([]string, int, int) {
	m := t.Control.Muted
	if !d.loaded {
		return []string{m.Render("Loading swarms…")}, 1, 0
	}
	if len(d.cards) == 0 {
		return []string{
			m.Render("No swarms yet."),
			"",
			m.Render("Open a mission on the cockpit's Missions page and press w to"),
			m.Render("split its open criteria across your machines."),
		}, 4, 0
	}
	var all []string
	curStart, curEnd := 0, 0
	lastRun := -1
	now := d.now()
	for i, c := range d.cards {
		r := d.runs[c.run]
		if c.run != lastRun {
			if lastRun >= 0 {
				all = append(all, "")
			}
			lastRun = c.run
			title := r.MissionTitle
			if title == "" {
				title = r.MissionID
			}
			head := t.Control.Label.Render("◎ "+oneLine(title)) + m.Render(" · run "+r.ID)
			if r.Target != "" {
				head += m.Render(" · into " + r.Target)
			}
			if cost := r.Cost(); cost > 0 {
				head += m.Render(" · " + control.Money(cost))
			}
			all = append(all, head)
		}
		if i == d.cursor {
			curStart = len(all)
		}
		all = append(all, d.card(t, r.Assignments[c.idx], i == d.cursor, now, width)...)
		if i == d.cursor {
			curEnd = len(all)
		}
	}
	h := max(1, d.height)
	if curStart < d.offset {
		d.offset = curStart
	}
	if curEnd > d.offset+h {
		d.offset = min(curStart, curEnd-h)
	}
	// Show the run header above the first card of a run when it fits.
	if d.offset > 0 && d.offset == curStart && curEnd-curStart < h {
		d.offset--
	}
	d.offset = max(0, min(d.offset, len(all)-h))
	end := min(len(all), d.offset+h)
	return all[d.offset:end], len(all), d.offset
}

// card renders one assignment.
func (d *MergeBoard) card(t *styles.Styles, a control.SwarmAssignment, sel bool, now time.Time, width int) []string {
	m := t.Control.Muted
	cur := "  "
	name := t.Control.Label.Render(a.Machine)
	if sel {
		cur = t.Control.Allowed.Render(">") + " "
		name = t.Control.Value.Render(a.Machine)
	}
	line := cur + statusIcon(t, a.Status) + " " + name
	if a.Branch != "" {
		line += "  " + m.Render("⎇ "+a.Branch)
	}
	out := []string{line}

	parts := []string{statusText(t, a.Status), checkText(t, a)}
	if a.Cost > 0 {
		parts = append(parts, t.Control.Value.Render(control.Money(a.Cost)))
	}
	if dur := a.Duration(now); dur > 0 {
		parts = append(parts, m.Render(swarmDuration(dur)))
	}
	if a.Added > 0 || a.Removed > 0 || len(a.Files) > 0 {
		parts = append(parts,
			t.Control.Allowed.Render(fmt.Sprintf("+%d", a.Added))+" "+t.Control.Alert.Render(fmt.Sprintf("−%d", a.Removed))+
				m.Render(" in "+countOf(len(a.Files), "file", "files")))
	}
	out = append(out, "    "+strings.Join(parts, m.Render(" · ")))

	crit := make([]string, 0, len(a.Criteria))
	for i, id := range a.Criteria {
		s := fmt.Sprintf("#%d", id)
		if i < len(a.CriteriaText) {
			s += " " + oneLine(escapeHidden(a.CriteriaText[i]))
		}
		crit = append(crit, s)
	}
	if len(crit) > 0 {
		out = append(out, "    "+m.Render(strings.Join(crit, "; ")))
	}
	switch {
	case a.Error != "":
		out = append(out, "    "+t.Control.Alert.Render("! "+oneLine(a.Error)))
	case a.Summary != "":
		out = append(out, "    "+m.Render(oneLine(a.Summary)))
	}
	for i, l := range out {
		out[i] = fitLine(l, width)
	}
	return out
}

func statusIcon(t *styles.Styles, s control.SwarmStatus) string {
	switch s {
	case control.SwarmRunning:
		return t.Control.Warn.Render("◐")
	case control.SwarmDone:
		return t.Control.Value.Render("●")
	case control.SwarmFailed:
		return t.Control.Alert.Render("✕")
	case control.SwarmMerged:
		return t.Control.Allowed.Render("✓")
	case control.SwarmDiscarded:
		return t.Control.Muted.Render("–")
	}
	return t.Control.Muted.Render("○")
}

func statusText(t *styles.Styles, s control.SwarmStatus) string {
	switch s {
	case control.SwarmRunning:
		return t.Control.Warn.Render("working")
	case control.SwarmDone:
		return t.Control.Value.Render("ready to merge")
	case control.SwarmFailed:
		return t.Control.Alert.Render("failed")
	case control.SwarmMerged:
		return t.Control.Allowed.Render("merged")
	case control.SwarmDiscarded:
		return t.Control.Muted.Render("discarded")
	}
	return t.Control.Muted.Render("waiting")
}

func checkText(t *styles.Styles, a control.SwarmAssignment) string {
	if !a.Status.Finished() {
		return t.Control.Muted.Render("checks pending")
	}
	switch a.CheckState() {
	case control.SwarmCheckPass:
		return t.Control.Allowed.Render("checks pass")
	case control.SwarmCheckFail:
		return t.Control.Alert.Render("checks fail")
	}
	return t.Control.Muted.Render("checks not run")
}

// diffRows renders the selected card's diff, scrolled to diffOffset.
func (d *MergeBoard) diffRows(t *styles.Styles, width int) ([]string, int, int) {
	m := t.Control.Muted
	_, a, ok := d.selected()
	if !ok {
		return nil, 0, 0
	}
	head := t.Control.Value.Render(a.Machine) + "  " + m.Render("⎇ "+a.Branch)
	var body []string
	switch {
	case !d.diffLoaded:
		body = []string{m.Render("Loading diff…")}
	case d.diffErr != "":
		body = []string{t.Control.Alert.Render("! " + oneLine(d.diffErr))}
	case len(d.diff) == 0:
		body = []string{m.Render("No changes.")}
	default:
		if d.diffLines == nil || d.diffWidth != width {
			d.diffLines = renderSwarmDiff(t, d.diff, width)
			d.diffWidth = width
		}
		body = d.diffLines
	}
	h := max(1, d.height-1)
	d.diffOffset = max(0, min(d.diffOffset, len(body)-h))
	end := min(len(body), d.diffOffset+h)
	rows := append([]string{head}, body[d.diffOffset:end]...)
	return rows, len(body) + 1, d.diffOffset
}

// renderSwarmDiff renders each changed file's diff as lines.
func renderSwarmDiff(t *styles.Styles, files []control.SwarmFileDiff, width int) []string {
	m := t.Control.Muted
	var out []string
	for i, f := range files {
		if i > 0 {
			out = append(out, "")
		}
		out = append(out, t.Control.Label.Render("── "+f.Path))
		switch {
		case f.Binary:
			out = append(out, m.Render("Binary file changed."))
			continue
		case f.Old == f.New:
			out = append(out, m.Render("No content changes."))
			continue
		}
		dv := common.DiffFormatter(t).Before(f.Path, f.Old).After(f.Path, f.New).Width(width).Wrap(true)
		out = append(out, strings.Split(strings.TrimRight(dv.String(), "\n"), "\n")...)
		if f.Truncated {
			out = append(out, m.Render("This file is too big to show in full."))
		}
	}
	return out
}

// swarmDuration formats how long an agent ran.
func swarmDuration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// countOf formats a count with the right noun.
func countOf(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// ShortHelp implements help.KeyMap.
func (d *MergeBoard) ShortHelp() []key.Binding {
	km := &d.keyMap
	switch {
	case d.discard != "":
		return []key.Binding{km.Yes, km.Back}
	case d.expanded:
		up := key.NewBinding(key.WithKeys("up"), key.WithHelp("↑↓", "scroll"))
		return []key.Binding{up, km.Merge, km.Discard, km.Back}
	}
	return []key.Binding{km.Up, km.Diff, km.Merge, km.Discard, km.Refresh, km.Close}
}

// FullHelp implements help.KeyMap.
func (d *MergeBoard) FullHelp() [][]key.Binding {
	return [][]key.Binding{d.ShortHelp()}
}
