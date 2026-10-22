package model

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/gitreview"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/util"
)

// Limits for the review screen's background work.
const (
	reviewDiffLimit    = 12000      // bytes of diff the model sees
	reviewMaxPreview   = 512 * 1024 // bytes per side shown in the diff pane
	reviewLogCommits   = 10
	reviewGitTimeout   = 15 * time.Second
	reviewDraftTimeout = 90 * time.Second
)

// errReviewTooLarge is shown in the diff pane for files too big to preview.
var errReviewTooLarge = errors.New("file too large to show here")

// reviewStatusMsg delivers the changed files for the review screen.
type reviewStatusMsg struct {
	res     gitreview.Result
	touched map[string]bool
	err     error
}

// reviewDiffMsg delivers one file's content at HEAD and on disk.
type reviewDiffMsg struct {
	path     string
	old, new string
	err      error
}

// reviewGeneratedMsg delivers a drafted commit message.
type reviewGeneratedMsg struct {
	msg string
	err error
}

// reviewCommittedMsg delivers the outcome of a commit.
type reviewCommittedMsg struct {
	hash string
	err  error
}

// openReviewDialog opens the review and commit screen for the tab's folder
// and starts reading the changes.
func (m *UI) openReviewDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.ReviewID) {
		m.dialog.CloseDialog(dialog.ReviewID)
	}
	d, act := dialog.NewReview(m.com)
	m.dialog.OpenDialog(d)
	return m.handleReviewAction(act)
}

func (m *UI) reviewDialog() *dialog.Review {
	if !m.dialog.ContainsDialog(dialog.ReviewID) {
		return nil
	}
	d, _ := m.dialog.Dialog(dialog.ReviewID).(*dialog.Review)
	return d
}

// handleReviewMsg hands a finished background job to the review screen.
// Results that arrive after the screen closed are dropped, except a
// commit's, which is still reported.
func (m *UI) handleReviewMsg(msg tea.Msg) tea.Cmd {
	d := m.reviewDialog()
	switch msg := msg.(type) {
	case reviewStatusMsg:
		if d != nil {
			return m.reviewActionCmd(d.SetStatus(msg.res, msg.touched, msg.err))
		}
	case reviewDiffMsg:
		if d != nil {
			d.SetDiff(msg.path, msg.old, msg.new, msg.err)
		}
	case reviewGeneratedMsg:
		if d != nil {
			return m.reviewActionCmd(d.SetGenerated(msg.msg, msg.err))
		}
	case reviewCommittedMsg:
		var cmds []tea.Cmd
		if msg.err == nil {
			cmds = append(cmds, util.ReportInfo("Committed "+msg.hash))
		}
		if d != nil {
			cmds = append(cmds, m.reviewActionCmd(d.SetCommitted(msg.hash, msg.err)))
		} else if msg.err != nil {
			cmds = append(cmds, util.ReportError(msg.err))
		}
		return tea.Batch(cmds...)
	}
	return nil
}

// reviewActionCmd turns an action returned by one of the review screen's
// setters into a command.
func (m *UI) reviewActionCmd(act dialog.Action) tea.Cmd {
	switch act := act.(type) {
	case nil:
		return nil
	case dialog.ActionCmd:
		return act.Cmd
	case dialog.ReviewAction:
		return m.handleReviewAction(act)
	}
	return nil
}

// handleReviewAction starts the git or model work the review screen asked
// for. None of it runs on the UI goroutine.
func (m *UI) handleReviewAction(act dialog.ReviewAction) tea.Cmd {
	switch act := act.(type) {
	case dialog.ActionReviewStatus:
		return m.reviewStatusCmd()
	case dialog.ActionReviewDiff:
		return reviewDiffCmd(act.Root, act.File)
	case dialog.ActionReviewGenerate:
		return m.reviewGenerateCmd(act.Root, act.Files)
	case dialog.ActionReviewCommit:
		return reviewCommitCmd(act.Root, act.Paths, act.Message)
	}
	return nil
}

// reviewStatusCmd lists the changes in the tab's folder and the files the
// session's agent edited.
func (m *UI) reviewStatusCmd() tea.Cmd {
	if m.com == nil || m.com.Workspace == nil {
		return nil
	}
	ws := m.com.Workspace
	dir := m.tabWorkingDir()
	sessionID := ""
	if m.hasSession() {
		sessionID = m.session.ID
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), reviewGitTimeout)
		defer cancel()
		res, err := gitreview.Status(ctx, dir)
		if err != nil {
			return reviewStatusMsg{err: err}
		}
		var touched map[string]bool
		if sessionID != "" {
			// The marker is a hint, so a failed lookup just leaves it off.
			if files, herr := ws.ListSessionHistory(ctx, sessionID); herr == nil {
				paths := make([]string, 0, len(files))
				for _, f := range files {
					paths = append(paths, f.Path)
				}
				touched = reviewTouched(res.Root, ws.WorkingDir(), paths)
			}
		}
		return reviewStatusMsg{res: res, touched: touched}
	}
}

// reviewTouched maps the paths the agent edited, absolute or relative to
// base, to slash separated paths relative to the repository root. Paths
// outside the repository are left out.
func reviewTouched(root, base string, paths []string) map[string]bool {
	if root == "" || len(paths) == 0 {
		return nil
	}
	roots := []string{root}
	if r, err := filepath.EvalSymlinks(root); err == nil && r != root {
		roots = append(roots, r)
	}
	out := make(map[string]bool, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(base, p)
		}
		candidates := []string{filepath.Clean(p)}
		if r, err := filepath.EvalSymlinks(p); err == nil {
			candidates = append(candidates, r)
		}
		for _, c := range candidates {
			if rel, ok := relInside(roots, c); ok {
				out[rel] = true
				break
			}
		}
	}
	return out
}

func relInside(roots []string, p string) (string, bool) {
	for _, root := range roots {
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == "." || rel == ".." ||
			strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return filepath.ToSlash(rel), true
	}
	return "", false
}

// reviewDiffCmd reads a file at HEAD and on disk for the diff pane.
func reviewDiffCmd(root string, f gitreview.File) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), reviewGitTimeout)
		defer cancel()
		before := f.Path
		if f.OrigPath != "" {
			before = f.OrigPath
		}
		msg := reviewDiffMsg{path: f.Path}
		old, err := gitreview.FileAtHEAD(ctx, root, before)
		if err != nil {
			msg.err = err
			return msg
		}
		cur, err := gitreview.FileInWorkTree(root, f.Path)
		if err != nil {
			msg.err = err
			return msg
		}
		if len(old) > reviewMaxPreview || len(cur) > reviewMaxPreview {
			msg.err = errReviewTooLarge
			return msg
		}
		msg.old, msg.new = old, cur
		return msg
	}
}

// reviewGenerateCmd drafts a commit message for files with the small
// model, showing it the diff and the recent history.
func (m *UI) reviewGenerateCmd(root string, files []gitreview.File) tea.Cmd {
	if m.com == nil || m.com.Workspace == nil {
		return nil
	}
	ws := m.com.Workspace
	sessionID := ""
	if m.hasSession() {
		sessionID = m.session.ID
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), reviewDraftTimeout)
		defer cancel()
		diff, err := gitreview.Diff(ctx, root, files)
		if err != nil {
			return reviewGeneratedMsg{err: err}
		}
		if strings.TrimSpace(diff) == "" {
			return reviewGeneratedMsg{err: reviewNote("The picked files have no changes to describe.")}
		}
		diff = gitreview.TruncateDiff(diff, reviewDiffLimit)
		// History only helps the model match the style; go on without it.
		log, _ := gitreview.Log(ctx, root, reviewLogCommits)
		msg, err := ws.GenerateCommitMessage(ctx, sessionID, diff, log)
		if err != nil {
			return reviewGeneratedMsg{err: reviewDraftError(err)}
		}
		return reviewGeneratedMsg{msg: msg}
	}
}

// reviewDraftError turns a failed draft into something the user can act
// on. Over HTTP the error arrives as text, so it is matched by message too.
func reviewDraftError(err error) error {
	switch {
	case errors.Is(err, agent.ErrNoCommitModel),
		strings.Contains(err.Error(), agent.ErrNoCommitModel.Error()):
		return reviewNote("No model is set up to write commit messages; type one below.")
	case errors.Is(err, context.DeadlineExceeded):
		return reviewNote("The model took too long to answer; press esc then c to retry, or type a message.")
	}
	return reviewNote("Couldn't draft a message: " + err.Error())
}

// reviewNote is an error worded as a sentence for the review screen.
type reviewNote string

func (n reviewNote) Error() string { return string(n) }

// reviewCommitCmd commits paths with msg. It never pushes.
func reviewCommitCmd(root string, paths []string, msg string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		hash, err := gitreview.Commit(ctx, root, paths, msg)
		return reviewCommittedMsg{hash: hash, err: err}
	}
}
