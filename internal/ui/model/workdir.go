package model

import (
	"context"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/home"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/workdir"
)

// sessionWorkingDirMsg reports the result of a /cd command.
type sessionWorkingDirMsg struct {
	session session.Session
	err     error
}

// tabFolder returns the folder the current tab was moved to with /cd, or
// "" when it works in the folder Ta-Da was launched in.
func (m *UI) tabFolder() string {
	if m.session == nil {
		return ""
	}
	return m.session.WorkingDir
}

// tabWorkingDir returns the folder the current tab works in.
func (m *UI) tabWorkingDir() string {
	if dir := m.tabFolder(); dir != "" {
		return dir
	}
	return m.com.Workspace.WorkingDir()
}

// tabGitBranch returns the git branch to show next to the tab's folder.
// The branch is polled for the launch folder only, so a tab moved with
// /cd shows none, unless it moved into a worktree.
func (m *UI) tabGitBranch() string {
	if dir := m.tabFolder(); dir != "" {
		return m.wt.info[dir].branch
	}
	return m.gitBranch
}

// parseCDCommand reports whether value is a /cd (or /directory) command
// and returns its argument. Bang mode is never a command.
func parseCDCommand(value string, bangMode bool) (arg string, ok bool) {
	if bangMode {
		return "", false
	}
	return dialog.CDQuery(value)
}

// runCDCommand switches the current tab to another folder. No argument
// shows the tab's folder; "-" goes back to the launch folder. A tab
// without a session gets one first, so the folder has somewhere to live.
func (m *UI) runCDCommand(arg string) tea.Cmd {
	launch := m.com.Workspace.WorkingDir()
	if arg == "" {
		msg := "This tab works in " + home.Short(m.tabWorkingDir())
		if m.tabFolder() == "" {
			msg += " (the launch folder)"
		}
		return util.ReportInfo(msg + ". Type /cd <path> to move it.")
	}

	dir := ""
	if arg != "-" {
		resolved, err := workdir.Resolve(m.tabWorkingDir(), arg)
		if err != nil {
			return util.ReportError(err)
		}
		if !sameDir(resolved, launch) {
			dir = resolved
		}
	}
	if dir == m.tabFolder() {
		return util.ReportInfo("This tab already works in " + home.Short(cmpDir(dir, launch)))
	}

	loadCmd, err := m.ensureSession()
	if err != nil {
		return util.ReportError(err)
	}
	sessionID := m.session.ID
	ws := m.com.Workspace
	setCmd := func() tea.Msg {
		sess, err := ws.SetSessionWorkingDir(context.Background(), sessionID, dir)
		return sessionWorkingDirMsg{session: sess, err: err}
	}
	if loadCmd == nil {
		return setCmd
	}
	return tea.Sequence(loadCmd, setCmd)
}

// handleSessionWorkingDir applies the result of a /cd command.
func (m *UI) handleSessionWorkingDir(msg sessionWorkingDirMsg) tea.Cmd {
	if msg.err != nil {
		return util.ReportError(msg.err)
	}
	if m.session != nil && m.session.ID == msg.session.ID {
		m.session.WorkingDir = msg.session.WorkingDir
	}
	m.setTabDir(msg.session.ID, msg.session.WorkingDir)
	dir := cmpDir(msg.session.WorkingDir, m.com.Workspace.WorkingDir())
	text := "This tab now works in " + home.Short(dir)
	if msg.session.WorkingDir == "" {
		text += " (the launch folder)"
	}
	if m.isAgentBusy() {
		text += ", from the next turn"
	}
	return util.ReportInfo(text)
}

func cmpDir(dir, fallback string) string {
	if dir != "" {
		return dir
	}
	return fallback
}

func sameDir(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}
