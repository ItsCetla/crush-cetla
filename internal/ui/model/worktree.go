package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/commands"
	"github.com/charmbracelet/crush/internal/home"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/worktree"
)

// A worktree per tab: /worktree <name> moves the tab in view into a git
// worktree for that branch, made next to the repository when it does not
// exist yet, and "New Worktree Tab…" opens a new tab that starts in one.
// The move itself is the same as /cd: the session's working folder is
// set to the worktree. Every git call runs in a tea.Cmd; the tab bar only
// reads what the last refresh cached.

const (
	// worktreeCreateTimeout bounds creating a worktree, which checks out
	// a whole tree.
	worktreeCreateTimeout = 2 * time.Minute
	// worktreeQueryTimeout bounds listing worktrees and reading their
	// state.
	worktreeQueryTimeout = 20 * time.Second
	// tabWorktreeMax caps the branch shown on a tab, in cells.
	tabWorktreeMax = 16
)

// worktreeState is what the UI knows about the worktrees its tabs work in.
type worktreeState struct {
	// info maps a tab folder (a session's WorkingDir) to the linked
	// worktree it is in. Folders outside any linked worktree are absent.
	info map[string]worktreeTabInfo
	// inflight is set while a refresh runs, so ticks never stack them.
	inflight bool
}

// worktreeTabInfo describes the linked worktree a tab folder is in.
type worktreeTabInfo struct {
	name    string
	path    string
	branch  string
	dirty   int
	managed bool
}

type (
	// worktreeSwitchedMsg reports the tab in view moved into a worktree.
	worktreeSwitchedMsg struct {
		session session.Session
		wt      worktree.Worktree
		err     error
	}
	// worktreeTabMsg reports a new session made to work in a worktree.
	worktreeTabMsg struct {
		session session.Session
		wt      worktree.Worktree
		err     error
	}
	// worktreeListMsg fills the worktree picker.
	worktreeListMsg struct {
		entries []dialog.WorktreeEntry
		err     error
	}
	// worktreeRemovedMsg reports the result of removing a worktree. dirty
	// is set, and nothing was removed, when it has uncommitted changes.
	worktreeRemovedMsg struct {
		wt    worktree.Worktree
		dirty error
		err   error
	}
	// worktreeTabsMsg carries a refresh of the tab folders' worktrees.
	worktreeTabsMsg struct {
		info map[string]worktreeTabInfo
	}
)

// worktreeOptions returns where managed worktrees go, from the
// worktree-dir option.
func (m *UI) worktreeOptions() []worktree.Option {
	cfg := m.com.Config()
	if cfg == nil || cfg.Options == nil || cfg.Options.WorktreeDir == "" {
		return nil
	}
	return []worktree.Option{worktree.WithDir(home.Long(cfg.Options.WorktreeDir))}
}

// runWorktreeAction carries out a worktree dialog action.
func (m *UI) runWorktreeAction(action dialog.Action) tea.Cmd {
	switch a := action.(type) {
	case dialog.ActionCmd:
		// A malformed command typed in the editor: its usage warning.
		return a.Cmd
	case dialog.ActionOpenWorktrees:
		m.dialog.CloseDialog(dialog.CommandsID)
		return m.openWorktreePicker()
	case dialog.ActionWorktree:
		m.dialog.CloseDialog(dialog.CommandsID)
		return m.switchToWorktree(a.Name, a.Base)
	case dialog.ActionSwitchWorktree:
		m.dialog.CloseDialog(dialog.CommandsID)
		m.dialog.CloseDialog(dialog.WorktreesID)
		return m.runCDCommand(a.Path)
	case dialog.ActionNewWorktreeTab:
		if a.Args == nil {
			m.dialog.CloseDialog(dialog.CommandsID)
			m.dialog.CloseDialog(dialog.WorktreesID)
			m.dialog.OpenDialog(dialog.NewArguments(
				m.com,
				"New Worktree Tab",
				"Opens a tab in a git worktree of its own.",
				[]commands.Argument{
					{ID: "name", Title: "Branch", Description: "Made if it does not exist; reused if it has a worktree", Required: true},
					{ID: "base", Title: "Base", Description: "Branch or commit to start from (default: this tab's branch)"},
				},
				a,
			))
			return nil
		}
		m.dialog.CloseFrontDialog()
		return m.newWorktreeTab(strings.TrimSpace(a.Args["name"]), strings.TrimSpace(a.Args["base"]))
	case dialog.ActionRemoveWorktree:
		m.dialog.CloseDialog(dialog.CommandsID)
		m.dialog.CloseDialog(dialog.WorktreeConfirmID)
		return m.removeWorktree(a)
	}
	return nil
}

// switchToWorktree moves the tab in view into the worktree for branch
// name, making it from base first when needed.
func (m *UI) switchToWorktree(name, base string) tea.Cmd {
	repoDir := m.tabWorkingDir()
	launch := m.com.Workspace.WorkingDir()
	opts := m.worktreeOptions()
	loadCmd, err := m.ensureSession()
	if err != nil {
		return util.ReportError(err)
	}
	sessionID := m.session.ID
	ws := m.com.Workspace
	setCmd := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), worktreeCreateTimeout)
		defer cancel()
		wt, err := worktree.Create(ctx, repoDir, name, base, opts...)
		if err != nil {
			return worktreeSwitchedMsg{err: worktreeErr(err)}
		}
		dir := wt.Path
		if sameDir(dir, launch) {
			dir = ""
		}
		sess, err := ws.SetSessionWorkingDir(ctx, sessionID, dir)
		return worktreeSwitchedMsg{session: sess, wt: wt, err: err}
	}
	if loadCmd == nil {
		return tea.Batch(util.ReportInfo("Preparing worktree "+worktree.Sanitize(name)+"…"), setCmd)
	}
	return tea.Sequence(loadCmd, setCmd)
}

// handleWorktreeSwitched applies a move into a worktree.
func (m *UI) handleWorktreeSwitched(msg worktreeSwitchedMsg) tea.Cmd {
	if msg.err != nil {
		return util.ReportError(msg.err)
	}
	if m.session != nil && m.session.ID == msg.session.ID {
		m.session.WorkingDir = msg.session.WorkingDir
	}
	m.setTabDir(msg.session.ID, msg.session.WorkingDir)
	m.noteWorktree(msg.session.WorkingDir, msg.wt)
	text := "This tab now works in worktree ⎇ " + msg.wt.Name() + " (" + home.Short(msg.wt.Path) + ")"
	if msg.wt.Main {
		text = "This tab now works in the main checkout (" + home.Short(msg.wt.Path) + ")"
	}
	if m.isAgentBusy() {
		text += ", from the next turn"
	}
	return util.ReportInfo(text)
}

// newWorktreeTab opens a new tab whose session works in the worktree for
// branch name.
func (m *UI) newWorktreeTab(name, base string) tea.Cmd {
	if name == "" {
		return util.ReportWarn("A worktree tab needs a branch name.")
	}
	repoDir := m.tabWorkingDir()
	launch := m.com.Workspace.WorkingDir()
	opts := m.worktreeOptions()
	ws := m.com.Workspace
	create := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), worktreeCreateTimeout)
		defer cancel()
		wt, err := worktree.Create(ctx, repoDir, name, base, opts...)
		if err != nil {
			return worktreeTabMsg{err: worktreeErr(err)}
		}
		sess, err := ws.CreateSession(ctx, "New Session")
		if err != nil {
			return worktreeTabMsg{err: err}
		}
		dir := wt.Path
		if sameDir(dir, launch) {
			dir = ""
		}
		sess, err = ws.SetSessionWorkingDir(ctx, sess.ID, dir)
		return worktreeTabMsg{session: sess, wt: wt, err: err}
	}
	return tea.Batch(util.ReportInfo("Preparing worktree "+worktree.Sanitize(name)+"…"), create)
}

// handleWorktreeTab shows the new worktree tab.
func (m *UI) handleWorktreeTab(msg worktreeTabMsg) tea.Cmd {
	if msg.err != nil {
		return util.ReportError(msg.err)
	}
	m.noteWorktree(msg.session.WorkingDir, msg.wt)
	return tea.Batch(
		m.loadSession(msg.session.ID),
		util.ReportInfo("New tab in worktree ⎇ "+msg.wt.Name()+" ("+home.Short(msg.wt.Path)+")"),
	)
}

// openWorktreePicker opens the worktree picker and starts listing.
func (m *UI) openWorktreePicker() tea.Cmd {
	if !m.dialog.ContainsDialog(dialog.WorktreesID) {
		m.dialog.OpenDialog(dialog.NewWorktrees(m.com))
	}
	return m.listWorktrees()
}

// listWorktrees lists the worktrees of the tab's repository, with each
// one's state, for the picker.
func (m *UI) listWorktrees() tea.Cmd {
	dir := m.tabWorkingDir()
	opts := m.worktreeOptions()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), worktreeQueryTimeout)
		defer cancel()
		list, err := worktree.List(ctx, dir, opts...)
		if errors.Is(err, worktree.ErrNotRepo) {
			return worktreeListMsg{}
		}
		if err != nil {
			return worktreeListMsg{err: err}
		}
		entries := make([]dialog.WorktreeEntry, 0, len(list))
		current, best := -1, 0
		for i, wt := range list {
			e := dialog.WorktreeEntry{Worktree: wt}
			if !wt.Prunable {
				st, err := worktree.Status(ctx, wt)
				e.State, e.StateErr = st, err != nil
			}
			// The longest checkout holding the folder is the one it is
			// in; a worktree may sit inside the main checkout.
			if worktree.Contains(wt.Path, dir) && len(wt.Path) > best {
				current, best = i, len(wt.Path)
			}
			entries = append(entries, e)
		}
		if current >= 0 {
			entries[current].Current = true
		}
		return worktreeListMsg{entries: entries}
	}
}

// handleWorktreeList fills the picker, if it is still open.
func (m *UI) handleWorktreeList(msg worktreeListMsg) {
	if d, ok := m.dialog.Dialog(dialog.WorktreesID).(*dialog.Worktrees); ok {
		d.SetEntries(msg.entries, msg.err)
	}
}

// removeWorktree removes a worktree found by path or name. A worktree an
// open tab works in is kept, and one with uncommitted changes needs
// a.Force, which the confirm dialog sets.
func (m *UI) removeWorktree(a dialog.ActionRemoveWorktree) tea.Cmd {
	repoDir := m.tabWorkingDir()
	opts := m.worktreeOptions()
	inUse := m.openTabDirs()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), worktreeQueryTimeout)
		defer cancel()
		list, err := worktree.List(ctx, repoDir, opts...)
		if err != nil {
			return worktreeRemovedMsg{err: worktreeErr(err)}
		}
		var wt worktree.Worktree
		found := false
		if a.Path != "" {
			for _, w := range list {
				if sameDir(w.Path, a.Path) {
					wt, found = w, true
					break
				}
			}
		}
		if !found && a.Name != "" {
			wt, found = worktree.Find(list, a.Name)
		}
		if !found {
			return worktreeRemovedMsg{err: fmt.Errorf("no worktree named %q in this repository", cmpDir(a.Name, a.Path))}
		}
		if wt.Main {
			return worktreeRemovedMsg{wt: wt, err: worktree.ErrMain}
		}
		for _, dir := range inUse {
			if worktree.Contains(wt.Path, dir) {
				return worktreeRemovedMsg{wt: wt, err: fmt.Errorf("an open tab works in %s; close it or move it with /cd first", wt.Name())}
			}
		}
		err = worktree.Remove(ctx, wt, a.Force)
		if errors.Is(err, worktree.ErrDirty) {
			return worktreeRemovedMsg{wt: wt, dirty: err}
		}
		return worktreeRemovedMsg{wt: wt, err: err}
	}
}

// handleWorktreeRemoved reports a removal, asks to confirm one that would
// lose changes, and refreshes the picker.
func (m *UI) handleWorktreeRemoved(msg worktreeRemovedMsg) tea.Cmd {
	picker, _ := m.dialog.Dialog(dialog.WorktreesID).(*dialog.Worktrees)
	if msg.dirty != nil {
		if picker != nil {
			picker.SetNotice("", nil)
		}
		reason := strings.TrimPrefix(msg.dirty.Error(), worktree.ErrDirty.Error()+": ")
		m.dialog.OpenDialog(dialog.NewWorktreeConfirm(m.com, msg.wt.Name(), msg.wt.Path, reason))
		return nil
	}
	if msg.err != nil {
		if picker != nil {
			picker.SetNotice("", msg.err)
			return nil
		}
		return util.ReportError(msg.err)
	}
	for dir, info := range m.wt.info {
		if sameDir(info.path, msg.wt.Path) {
			delete(m.wt.info, dir)
		}
	}
	text := "Removed worktree " + msg.wt.Name()
	if msg.wt.Branch != "" {
		text += "; branch " + msg.wt.Branch + " is kept"
	}
	if picker != nil {
		picker.SetNotice(text+".", nil)
		return m.listWorktrees()
	}
	return util.ReportInfo(text + ".")
}

// openTabDirs returns the folders the open tabs work in, the launch
// folder standing in for tabs that were never moved.
func (m *UI) openTabDirs() []string {
	launch := m.com.Workspace.WorkingDir()
	seen := map[string]bool{}
	var dirs []string
	add := func(dir string) {
		dir = cmpDir(dir, launch)
		if !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	for _, t := range m.tabs {
		add(t.dir)
	}
	if m.session != nil {
		add(m.session.WorkingDir)
	}
	return dirs
}

// refreshWorktreeTabs reads which worktree each moved tab works in, and
// how many changes each managed one has, for the tab bar. It runs on the
// git branch tick and never overlaps itself.
func (m *UI) refreshWorktreeTabs() tea.Cmd {
	if m.wt.inflight {
		return nil
	}
	seen := map[string]bool{}
	var dirs []string
	for _, t := range m.tabs {
		if t.dir != "" && !seen[t.dir] {
			seen[t.dir] = true
			dirs = append(dirs, t.dir)
		}
	}
	if m.session != nil && m.session.WorkingDir != "" && !seen[m.session.WorkingDir] {
		dirs = append(dirs, m.session.WorkingDir)
	}
	if len(dirs) == 0 {
		m.wt.info = nil
		return nil
	}
	m.wt.inflight = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), worktreeQueryTimeout)
		defer cancel()
		info := make(map[string]worktreeTabInfo, len(dirs))
		for _, dir := range dirs {
			wt, ok := worktree.Lookup(dir)
			if !ok {
				continue
			}
			ti := worktreeInfo(wt)
			if wt.Managed {
				if st, err := worktree.Status(ctx, wt); err == nil {
					ti.dirty = st.Dirty
				}
			}
			info[dir] = ti
		}
		return worktreeTabsMsg{info: info}
	}
}

// handleWorktreeTabs stores a refresh.
func (m *UI) handleWorktreeTabs(msg worktreeTabsMsg) {
	m.wt.inflight = false
	m.wt.info = msg.info
}

// noteWorktree records at once that dir is in wt, so the tab shows it
// before the next refresh.
func (m *UI) noteWorktree(dir string, wt worktree.Worktree) {
	if dir == "" || wt.Main {
		return
	}
	if m.wt.info == nil {
		m.wt.info = map[string]worktreeTabInfo{}
	}
	m.wt.info[dir] = worktreeInfo(wt)
}

func worktreeInfo(wt worktree.Worktree) worktreeTabInfo {
	return worktreeTabInfo{
		name:    wt.Name(),
		path:    wt.Path,
		branch:  wt.Branch,
		managed: wt.Managed,
	}
}

// setTabDir records the folder the tab for sessionID works in.
func (m *UI) setTabDir(sessionID, dir string) {
	if i := m.tabIndex(sessionID); i >= 0 {
		m.tabs[i].dir = dir
	}
}

// tabWorktree returns the managed worktree the tab works in, if any.
func (m *UI) tabWorktree(t sessionTab) (worktreeTabInfo, bool) {
	if t.dir == "" {
		return worktreeTabInfo{}, false
	}
	info, ok := m.wt.info[t.dir]
	if !ok || !info.managed {
		return worktreeTabInfo{}, false
	}
	return info, true
}

// worktreeCloseHint tells the user a closed tab's worktree is still on
// disk, unless another open tab works in it too.
func (m *UI) worktreeCloseHint(closed sessionTab) tea.Cmd {
	info, ok := m.tabWorktree(closed)
	if !ok {
		return nil
	}
	for _, t := range m.tabs {
		if t.id != closed.id && t.dir == closed.dir {
			return nil
		}
	}
	text := "Worktree ⎇ " + info.name + " stays on disk"
	if info.dirty > 0 {
		text += fmt.Sprintf(" with %d uncommitted change(s)", info.dirty)
	}
	return util.ReportInfo(text + ". /worktree rm " + info.name + " removes it.")
}

// worktreeErr makes git's "not a repository" error say what to do.
func worktreeErr(err error) error {
	if errors.Is(err, worktree.ErrNotRepo) {
		return errors.New("this tab's folder is not in a git repository with commits; /worktree needs one")
	}
	return err
}
