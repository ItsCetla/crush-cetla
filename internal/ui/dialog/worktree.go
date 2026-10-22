package dialog

import (
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/home"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/worktree"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// WorktreesID is the identifier for the worktree picker.
const WorktreesID = "worktrees"

// WorktreeConfirmID is the identifier for the dialog that confirms
// removing a worktree with uncommitted changes.
const WorktreeConfirmID = "worktree_confirm"

type (
	// ActionOpenWorktrees opens the worktree picker.
	ActionOpenWorktrees struct{}
	// ActionWorktree moves the tab in view into the worktree for branch
	// Name, creating it from Base (or the current branch) when needed.
	ActionWorktree struct {
		Name string
		Base string
	}
	// ActionSwitchWorktree moves the tab in view into an existing
	// worktree, or the main checkout, at Path.
	ActionSwitchWorktree struct {
		Path string
	}
	// ActionNewWorktreeTab opens a new tab whose session starts in a
	// worktree. A nil Args asks for the name first; Args holds "name" and
	// an optional "base".
	ActionNewWorktreeTab struct {
		Args map[string]string
	}
	// ActionRemoveWorktree removes a worktree, found by Path or else by
	// Name. Without Force a worktree with uncommitted changes is kept and
	// the user is asked to confirm.
	ActionRemoveWorktree struct {
		Name  string
		Path  string
		Force bool
	}
)

// WorktreeCommand is a parsed /worktree command.
type WorktreeCommand struct {
	// Remove is set for "/worktree rm <name>".
	Remove bool
	// Name is the branch to create, reuse or remove. Empty (without
	// Remove) opens the picker.
	Name string
	// Base is the commit or branch a new worktree starts from.
	Base string
	// Err explains a malformed command.
	Err string
}

// WorktreeQuery reports whether value is a /worktree (or /wt) command and
// parses it. Multi-line text is never a command.
func WorktreeQuery(value string) (WorktreeCommand, bool) {
	if strings.Contains(value, "\n") {
		return WorktreeCommand{}, false
	}
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return WorktreeCommand{}, false
	}
	switch strings.ToLower(fields[0]) {
	case "/worktree", "/worktrees", "/wt":
	default:
		return WorktreeCommand{}, false
	}
	args := fields[1:]
	var c WorktreeCommand
	if len(args) > 0 && (args[0] == "rm" || args[0] == "remove") {
		c.Remove = true
		switch len(args) {
		case 1:
			c.Err = "Usage: /worktree rm <name>"
		case 2:
			c.Name = args[1]
		default:
			c.Err = "Usage: /worktree rm <name> (one worktree at a time)"
		}
		return c, true
	}
	switch len(args) {
	case 0:
	case 1:
		c.Name = args[0]
	case 2:
		c.Name, c.Base = args[0], args[1]
	default:
		c.Err = "Usage: /worktree <name> [base]"
	}
	return c, true
}

// Action returns the dialog action that carries out the command.
func (c WorktreeCommand) Action() Action {
	switch {
	case c.Err != "":
		return ActionCmd{Cmd: util.ReportWarn(c.Err)}
	case c.Remove:
		return ActionRemoveWorktree{Name: c.Name}
	case c.Name == "":
		return ActionOpenWorktrees{}
	}
	return ActionWorktree{Name: c.Name, Base: c.Base}
}

// WorktreeEntry is one row of the worktree picker.
type WorktreeEntry struct {
	worktree.Worktree
	State worktree.State
	// StateErr is set when the state could not be read.
	StateErr bool
	// Current marks the checkout the tab in view works in.
	Current bool
}

// SortWorktrees orders entries for the picker: worktrees Ta-Da made
// first, then other linked worktrees, then the main checkout, each group
// by name.
func SortWorktrees(entries []WorktreeEntry) {
	rank := func(e WorktreeEntry) int {
		switch {
		case e.Main:
			return 2
		case e.Managed:
			return 0
		}
		return 1
	}
	slices.SortStableFunc(entries, func(a, b WorktreeEntry) int {
		if ra, rb := rank(a), rank(b); ra != rb {
			return ra - rb
		}
		return strings.Compare(a.Name(), b.Name())
	})
}

// Worktrees lists the repository's worktrees and moves the tab in view
// into the one picked.
type Worktrees struct {
	com    *common.Common
	width  int
	height int

	entries []WorktreeEntry
	loaded  bool
	busy    bool
	notice  string
	err     string

	cursor int
	offset int

	help   help.Model
	keyMap struct {
		Up     key.Binding
		Down   key.Binding
		Select key.Binding
		New    key.Binding
		Remove key.Binding
		Close  key.Binding
	}
}

var _ Dialog = (*Worktrees)(nil)

// NewWorktrees creates the worktree picker. The caller fills it with
// SetEntries.
func NewWorktrees(com *common.Common) *Worktrees {
	d := &Worktrees{com: com}
	d.help = help.New()
	d.help.Styles = com.Styles.DialogHelpStyles()
	d.keyMap.Up = key.NewBinding(
		key.WithKeys("up", "k", "ctrl+p"),
		key.WithHelp("↑↓", "choose"),
	)
	d.keyMap.Down = key.NewBinding(key.WithKeys("down", "j", "ctrl+n"))
	d.keyMap.Select = key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "move tab here"),
	)
	d.keyMap.New = key.NewBinding(
		key.WithKeys("n"),
		key.WithHelp("n", "new worktree tab"),
	)
	d.keyMap.Remove = key.NewBinding(
		key.WithKeys("d", "x", "delete"),
		key.WithHelp("d", "remove"),
	)
	d.keyMap.Close = CloseKey
	return d
}

// ID implements Dialog.
func (d *Worktrees) ID() string { return WorktreesID }

// SetEntries fills the picker. The cursor stays on the worktree it was
// on, or starts on the one the tab works in.
func (d *Worktrees) SetEntries(entries []WorktreeEntry, err error) {
	d.loaded = true
	d.busy = false
	if err != nil {
		d.err = err.Error()
		return
	}
	d.err = ""
	prev := ""
	if d.cursor < len(d.entries) && len(d.entries) > 0 {
		prev = d.entries[d.cursor].Path
	}
	SortWorktrees(entries)
	d.entries = entries
	d.cursor = 0
	for i, e := range entries {
		if (prev != "" && e.Path == prev) || (prev == "" && e.Current) {
			d.cursor = i
			break
		}
	}
	d.clampOffset()
}

// SetNotice shows a one-line note under the list.
func (d *Worktrees) SetNotice(notice string, err error) {
	d.busy = false
	if err != nil {
		d.err, d.notice = err.Error(), ""
		return
	}
	d.err, d.notice = "", notice
}

func (d *Worktrees) selected() *WorktreeEntry {
	if d.cursor < 0 || d.cursor >= len(d.entries) {
		return nil
	}
	return &d.entries[d.cursor]
}

// HandleMsg implements Dialog.
func (d *Worktrees) HandleMsg(msg tea.Msg) Action {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch {
	case key.Matches(k, d.keyMap.Close):
		return ActionClose{}
	case key.Matches(k, d.keyMap.Up):
		if len(d.entries) > 0 {
			d.cursor = (d.cursor - 1 + len(d.entries)) % len(d.entries)
			d.clampOffset()
		}
	case key.Matches(k, d.keyMap.Down):
		if len(d.entries) > 0 {
			d.cursor = (d.cursor + 1) % len(d.entries)
			d.clampOffset()
		}
	case key.Matches(k, d.keyMap.New):
		return ActionNewWorktreeTab{}
	case key.Matches(k, d.keyMap.Select):
		e := d.selected()
		if e == nil || d.busy {
			return nil
		}
		if e.Prunable {
			d.err = "That worktree's folder is gone; remove it with d."
			return nil
		}
		return ActionSwitchWorktree{Path: e.Path}
	case key.Matches(k, d.keyMap.Remove):
		e := d.selected()
		if e == nil || d.busy {
			return nil
		}
		if e.Main {
			d.err = "The main checkout can't be removed."
			return nil
		}
		d.busy = true
		d.err, d.notice = "", ""
		return ActionRemoveWorktree{Name: e.Name(), Path: e.Path}
	}
	return nil
}

// Draw implements Dialog.
func (d *Worktrees) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := d.com.Styles
	d.width = max(0, min(88, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	// Title, footer, help and the frame take about eight rows.
	d.height = max(1, min(12, area.Dy()-8))
	d.clampOffset()
	DrawCenter(scr, area, d.content())
	return nil
}

func (d *Worktrees) clampOffset() {
	h := max(1, d.height)
	if d.cursor < d.offset {
		d.offset = d.cursor
	}
	if d.cursor >= d.offset+h {
		d.offset = d.cursor - h + 1
	}
	d.offset = max(0, min(d.offset, len(d.entries)-h))
}

func (d *Worktrees) content() string {
	t := d.com.Styles
	inner := max(0, d.width-t.Dialog.View.GetHorizontalFrameSize())
	rc := NewRenderContext(t, d.width)
	rc.Title = "Worktrees"
	if d.loaded && len(d.entries) > 0 {
		rc.TitleInfo = " " + t.Control.Muted.Render(fmt.Sprintf("%d", len(d.entries)))
	}

	pad := lipgloss.NewStyle().Padding(0, 1)
	scroll := len(d.entries) > d.height
	width := max(0, inner-2)
	if scroll {
		width = max(0, width-1)
	}
	rows := d.rows(t, width)
	for len(rows) < d.height {
		rows = append(rows, "")
	}
	for i, l := range rows {
		rows[i] = pad.Render(ansi.Truncate(l, width, "…"))
	}
	list := strings.Join(rows, "\n")
	if scroll {
		list = joinScrollbar(t, list, d.height, len(d.entries), d.height, d.offset)
	}
	rc.AddPart(list)

	var foot string
	switch {
	case d.busy:
		foot = t.Control.Muted.Render("Working…")
	case d.err != "":
		foot = t.Control.Alert.Render("! " + d.err)
	case d.notice != "":
		foot = t.Control.Muted.Render(d.notice)
	default:
		foot = t.Control.Muted.Render("Type /worktree <name> to make one for this tab.")
	}
	rc.AddPart(pad.Render(ansi.Truncate(foot, max(0, inner-2), "…")))
	rc.Help = renderDialogHelp(t, &d.help, d, inner)
	return rc.Render()
}

func (d *Worktrees) rows(t *styles.Styles, width int) []string {
	if !d.loaded {
		return []string{t.Control.Muted.Render("Listing worktrees…")}
	}
	if len(d.entries) == 0 {
		return []string{t.Control.Muted.Render("This folder is not in a git repository.")}
	}
	nameW := 4
	for _, e := range d.entries {
		nameW = max(nameW, ansi.StringWidth(e.Name()))
	}
	nameW = min(nameW, 28)
	var out []string
	for i := d.offset; i < len(d.entries) && i < d.offset+d.height; i++ {
		out = append(out, d.row(t, d.entries[i], i == d.cursor, nameW, width))
	}
	return out
}

func (d *Worktrees) row(t *styles.Styles, e WorktreeEntry, sel bool, nameW, width int) string {
	cur := "  "
	if sel {
		cur = t.Control.Allowed.Render(">") + " "
	}
	here := "  "
	if e.Current {
		here = t.Control.Allowed.Render("●") + " "
	}
	icon := "⎇ "
	if e.Main {
		icon = "⌂ "
	}
	name := fmt.Sprintf("%-*s", nameW, ansi.Truncate(e.Name(), nameW, "…"))
	if sel {
		name = t.Control.Value.Render(icon + name)
	} else {
		name = t.Control.Label.Render(icon + name)
	}
	line := cur + here + name + "  " + d.state(t, e)
	path := home.Short(e.Path)
	if room := width - ansi.StringWidth(line) - 2; room > 8 {
		line += "  " + t.Control.Muted.Render(ansi.TruncateLeft(path, max(0, ansi.StringWidth(path)-room), "…"))
	}
	return line
}

// state is the short note after a worktree's name: what kind it is and
// whether it has changes.
func (d *Worktrees) state(t *styles.Styles, e WorktreeEntry) string {
	var parts []string
	switch {
	case e.Prunable:
		return t.Control.Alert.Render("folder missing")
	case e.Main:
		parts = append(parts, t.Control.Muted.Render("main checkout"))
	case !e.Managed:
		parts = append(parts, t.Control.Muted.Render("not made by Ta-Da"))
	}
	if e.Detached {
		parts = append(parts, t.Control.Muted.Render("detached"))
	}
	if e.Locked {
		parts = append(parts, t.Control.Warn.Render("locked"))
	}
	switch {
	case e.StateErr:
	case e.State.Dirty > 0:
		parts = append(parts, t.Control.Warn.Render(fmt.Sprintf("%d changed", e.State.Dirty)))
	default:
		parts = append(parts, t.Control.Allowed.Render("clean"))
	}
	if e.State.Ahead > 0 {
		parts = append(parts, t.Control.Value.Render(fmt.Sprintf("%d ahead", e.State.Ahead)))
	}
	if e.State.Behind > 0 {
		parts = append(parts, t.Control.Muted.Render(fmt.Sprintf("%d behind", e.State.Behind)))
	}
	return strings.Join(parts, t.Control.Muted.Render(" · "))
}

// ShortHelp implements help.KeyMap.
func (d *Worktrees) ShortHelp() []key.Binding {
	return []key.Binding{d.keyMap.Up, d.keyMap.Select, d.keyMap.New, d.keyMap.Remove, d.keyMap.Close}
}

// FullHelp implements help.KeyMap.
func (d *Worktrees) FullHelp() [][]key.Binding {
	return [][]key.Binding{d.ShortHelp()}
}

// WorktreeConfirm asks before removing a worktree that has uncommitted
// changes.
type WorktreeConfirm struct {
	com    *common.Common
	width  int
	name   string
	path   string
	reason string

	help   help.Model
	keyMap struct {
		Yes key.Binding
		No  key.Binding
	}
}

var _ Dialog = (*WorktreeConfirm)(nil)

// NewWorktreeConfirm creates the confirmation for removing the worktree
// name at path; reason says what would be lost.
func NewWorktreeConfirm(com *common.Common, name, path, reason string) *WorktreeConfirm {
	d := &WorktreeConfirm{com: com, name: name, path: path, reason: reason}
	d.help = help.New()
	d.help.Styles = com.Styles.DialogHelpStyles()
	d.keyMap.Yes = key.NewBinding(
		key.WithKeys("y", "Y"),
		key.WithHelp("y", "remove anyway"),
	)
	d.keyMap.No = key.NewBinding(
		key.WithKeys("n", "N", "esc", "alt+esc", "enter"),
		key.WithHelp("n/esc", "keep it"),
	)
	return d
}

// ID implements Dialog.
func (d *WorktreeConfirm) ID() string { return WorktreeConfirmID }

// HandleMsg implements Dialog.
func (d *WorktreeConfirm) HandleMsg(msg tea.Msg) Action {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch {
	case key.Matches(k, d.keyMap.Yes):
		return ActionRemoveWorktree{Name: d.name, Path: d.path, Force: true}
	case key.Matches(k, d.keyMap.No):
		return ActionClose{}
	}
	return nil
}

// Draw implements Dialog.
func (d *WorktreeConfirm) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := d.com.Styles
	d.width = max(0, min(64, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	inner := max(0, d.width-t.Dialog.View.GetHorizontalFrameSize())
	width := max(0, inner-2)
	rc := NewRenderContext(t, d.width)
	rc.Title = "Remove Worktree?"

	m := t.Control.Muted
	lines := []string{
		t.Control.Label.Render("⎇ "+d.name) + "  " + t.Control.Warn.Render(d.reason),
		m.Render(home.Short(d.path)),
		"",
		m.Render("Removing it deletes those changes for good."),
		m.Render("The branch itself is kept."),
	}
	pad := lipgloss.NewStyle().Padding(0, 1)
	for i, l := range lines {
		lines[i] = pad.Render(ansi.Truncate(l, width, "…"))
	}
	rc.AddPart(strings.Join(lines, "\n"))
	rc.Help = renderDialogHelp(t, &d.help, d, inner)
	DrawCenter(scr, area, rc.Render())
	return nil
}

// ShortHelp implements help.KeyMap.
func (d *WorktreeConfirm) ShortHelp() []key.Binding {
	return []key.Binding{d.keyMap.Yes, d.keyMap.No}
}

// FullHelp implements help.KeyMap.
func (d *WorktreeConfirm) FullHelp() [][]key.Binding {
	return [][]key.Binding{d.ShortHelp()}
}
