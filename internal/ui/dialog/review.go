package dialog

import (
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/gitreview"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// ReviewID is the identifier for the review and commit dialog.
const ReviewID = "review"

// Review layout limits.
const (
	reviewMaxWidth    = 200
	reviewMaxHeight   = 60
	reviewCompactW    = 72
	reviewWideAt      = 100 // inner width from which the list sits beside the diff
	reviewSplitAt     = 120 // diff pane width from which the diff is split
	reviewMinListW    = 30
	reviewMaxListW    = 56
	reviewStatusLines = 3
)

// ReviewQuery reports whether value is the review command typed into the
// editor ("/review" or "/commit").
func ReviewQuery(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "/review", "/commit":
		return true
	}
	return false
}

// ReviewAction is implemented by every action the review dialog hands the
// UI, so the UI can route them with a single case.
type ReviewAction interface {
	reviewAction()
}

// ActionReviewStatus asks the UI to read the changed files again and hand
// them back through SetStatus.
type ActionReviewStatus struct{}

// ActionReviewDiff asks the UI to read File at HEAD and on disk and hand
// both back through SetDiff.
type ActionReviewDiff struct {
	Root string
	File gitreview.File
}

// ActionReviewGenerate asks the UI to have the small model draft a commit
// message for Files and hand it back through SetGenerated.
type ActionReviewGenerate struct {
	Root  string
	Files []gitreview.File
}

// ActionReviewCommit asks the UI to commit Paths with Message and hand the
// outcome back through SetCommitted. It never pushes.
type ActionReviewCommit struct {
	Root    string
	Paths   []string
	Message string
}

func (ActionReviewStatus) reviewAction()   {}
func (ActionReviewDiff) reviewAction()     {}
func (ActionReviewGenerate) reviewAction() {}
func (ActionReviewCommit) reviewAction()   {}

type reviewState int

const (
	reviewLoading reviewState = iota
	reviewNotRepo
	reviewFailed
	reviewReady
)

type reviewFocus int

const (
	reviewFocusFiles reviewFocus = iota
	reviewFocusDiff
	reviewFocusMessage
	reviewFocusCount
)

type reviewFile struct {
	gitreview.File
	selected bool
	touched  bool
}

type reviewDiff struct {
	old, new string
	err      error
}

// reviewRender caches the rendered diff of one file at one size.
type reviewRender struct {
	path  string
	width int
	split bool
	lines []string
}

type reviewKeyMap struct {
	Up, Down          key.Binding
	Toggle, ToggleAll key.Binding
	Generate, Edit    key.Binding
	Commit            key.Binding
	Next, Prev        key.Binding
	Refresh           key.Binding
	PageUp, PageDown  key.Binding
	Top, Bottom       key.Binding
	Back, Close       key.Binding
}

// Review lists the changed files in the tab's repository, shows each
// file's diff, drafts a commit message with the small model and commits
// the chosen files.
type Review struct {
	com *common.Common

	state   reviewState
	loadErr string
	repo    gitreview.Repo
	files   []reviewFile

	cursor     int
	listOffset int
	focus      reviewFocus

	diffs      map[string]reviewDiff
	render     reviewRender
	diffOffset int
	diffRows   int // diff rows last drawn, for paging

	ta textarea.Model

	refreshing bool
	generating bool
	committing bool
	notice     string
	err        string

	help   help.Model
	keyMap reviewKeyMap
}

var _ Dialog = (*Review)(nil)

// NewReview creates the review dialog. The caller loads the files by
// handling the returned action.
func NewReview(com *common.Common) (*Review, ActionReviewStatus) {
	t := com.Styles
	d := &Review{com: com, diffs: map[string]reviewDiff{}}
	d.help = help.New()
	d.help.Styles = t.DialogHelpStyles()

	ta := textarea.New()
	ta.SetStyles(t.Editor.Textarea)
	ta.Placeholder = "Type a commit message, or press c in the file list to draft one"
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetVirtualCursor(false)
	ta.SetPromptFunc(0, func(textarea.PromptInfo) string { return "" })
	ta.Blur()
	d.ta = ta

	b := func(keys []string, k, desc string) key.Binding {
		return key.NewBinding(key.WithKeys(keys...), key.WithHelp(k, desc))
	}
	km := &d.keyMap
	km.Up = b([]string{"up", "k"}, "↑/k", "up")
	km.Down = b([]string{"down", "j"}, "↓/j", "down")
	km.Toggle = b([]string{"space", " "}, "space", "pick")
	km.ToggleAll = b([]string{"a"}, "a", "pick all")
	km.Generate = b([]string{"c"}, "c", "draft message")
	km.Edit = b([]string{"e", "enter"}, "e", "edit message")
	km.Commit = b([]string{"ctrl+s"}, "ctrl+s", "commit")
	km.Next = b([]string{"tab"}, "tab", "next pane")
	km.Prev = b([]string{"shift+tab"}, "shift+tab", "prev pane")
	km.Refresh = b([]string{"r"}, "r", "reload")
	km.PageUp = b([]string{"pgup", "ctrl+u"}, "pgup", "page up")
	km.PageDown = b([]string{"pgdown", "ctrl+d"}, "pgdn", "page down")
	km.Top = b([]string{"g", "home"}, "g", "top")
	km.Bottom = b([]string{"G", "end"}, "G", "bottom")
	km.Back = b([]string{"esc"}, "esc", "back")
	km.Close = CloseKey
	return d, ActionReviewStatus{}
}

// ID implements Dialog.
func (d *Review) ID() string { return ReviewID }

// Root is the repository's top folder, empty until the files are loaded.
func (d *Review) Root() string { return d.repo.Root }

// SetStatus shows the changed files. touched holds the root relative paths
// the session's agent edited. Picks survive a reload; new files start
// picked. It returns the action that loads the current file's diff.
func (d *Review) SetStatus(res gitreview.Result, touched map[string]bool, err error) Action {
	d.refreshing = false
	switch {
	case errors.Is(err, gitreview.ErrNotRepo):
		d.state = reviewNotRepo
		return nil
	case err != nil:
		d.state = reviewFailed
		d.loadErr = oneLine(err.Error())
		return nil
	}

	var prev map[string]bool
	current := ""
	if d.state == reviewReady {
		prev = make(map[string]bool, len(d.files))
		for _, f := range d.files {
			prev[f.Path] = f.selected
		}
		if f := d.current(); f != nil {
			current = f.Path
		}
	}

	d.state = reviewReady
	d.repo = res.Repo
	d.files = make([]reviewFile, len(res.Files))
	d.cursor = 0
	for i, f := range res.Files {
		sel, ok := prev[f.Path]
		if !ok {
			sel = true
		}
		d.files[i] = reviewFile{File: f, selected: sel, touched: touched[f.Path]}
		if f.Path == current {
			d.cursor = i
		}
	}
	d.diffs = map[string]reviewDiff{}
	d.render = reviewRender{}
	d.diffOffset = 0
	if len(d.files) == 0 && d.focus != reviewFocusMessage {
		d.focus = reviewFocusFiles
	}
	return d.loadDiff()
}

// SetDiff hands over a file's content at HEAD and on disk.
func (d *Review) SetDiff(path, old, new string, err error) {
	d.diffs[path] = reviewDiff{old: old, new: new, err: err}
	if d.render.path == path {
		d.render = reviewRender{}
	}
}

// SetGenerated shows a drafted commit message. On error the message box is
// focused so the user can write one by hand.
func (d *Review) SetGenerated(msg string, err error) Action {
	d.generating = false
	if err != nil {
		d.err = oneLine(err.Error())
		d.notice = ""
		return d.setFocus(reviewFocusMessage)
	}
	d.ta.SetValue(strings.TrimSpace(msg))
	d.err = ""
	d.notice = "Drafted a message. Edit it if you like, then ctrl+s commits."
	return d.setFocus(reviewFocusMessage)
}

// SetCommitted shows the outcome of a commit. On success the message is
// cleared and the files are reloaded; on failure the error stays on screen
// and nothing else changes.
func (d *Review) SetCommitted(hash string, err error) Action {
	d.committing = false
	if err != nil {
		d.err = "Commit failed: " + oneLine(err.Error())
		d.notice = ""
		return nil
	}
	d.err = ""
	d.notice = "Committed " + hash
	d.ta.Reset()
	d.setFocus(reviewFocusFiles)
	d.refreshing = true
	return ActionReviewStatus{}
}

// Message returns the commit message as typed.
func (d *Review) Message() string { return d.ta.Value() }

func (d *Review) current() *reviewFile {
	if d.cursor < 0 || d.cursor >= len(d.files) {
		return nil
	}
	return &d.files[d.cursor]
}

func (d *Review) picked() []gitreview.File {
	var out []gitreview.File
	for _, f := range d.files {
		if f.selected {
			out = append(out, f.File)
		}
	}
	return out
}

// loadDiff asks for the current file's diff unless it is already here.
func (d *Review) loadDiff() Action {
	f := d.current()
	if f == nil || f.Binary {
		return nil
	}
	if _, ok := d.diffs[f.Path]; ok {
		return nil
	}
	return ActionReviewDiff{Root: d.repo.Root, File: f.File}
}

func (d *Review) setFocus(f reviewFocus) Action {
	d.focus = f
	if f == reviewFocusMessage {
		return ActionCmd{d.ta.Focus()}
	}
	d.ta.Blur()
	return nil
}

// HandleMsg implements Dialog.
func (d *Review) HandleMsg(msg tea.Msg) Action {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		if d.focus == reviewFocusMessage {
			var cmd tea.Cmd
			d.ta, cmd = d.ta.Update(msg)
			if cmd != nil {
				return ActionCmd{cmd}
			}
		}
		return nil
	}

	km := &d.keyMap
	if key.Matches(k, km.Commit) {
		return d.commit()
	}
	if d.focus == reviewFocusMessage {
		switch {
		case key.Matches(k, km.Back), key.Matches(k, km.Next):
			return d.setFocus(reviewFocusFiles)
		case key.Matches(k, km.Prev):
			return d.setFocus(reviewFocusDiff)
		case key.Matches(k, km.Close):
			return ActionClose{}
		}
		d.err = ""
		var cmd tea.Cmd
		d.ta, cmd = d.ta.Update(k)
		if cmd != nil {
			return ActionCmd{cmd}
		}
		return nil
	}

	switch {
	case key.Matches(k, km.Close):
		return ActionClose{}
	case key.Matches(k, km.Refresh):
		if d.refreshing || d.state == reviewLoading {
			return nil
		}
		d.refreshing = true
		d.err, d.notice = "", ""
		return ActionReviewStatus{}
	}
	if d.state != reviewReady || len(d.files) == 0 {
		return nil
	}

	page := max(1, d.diffRows-1)
	switch {
	case key.Matches(k, km.Next):
		return d.setFocus((d.focus + 1) % reviewFocusCount)
	case key.Matches(k, km.Prev):
		return d.setFocus((d.focus + reviewFocusCount - 1) % reviewFocusCount)
	case key.Matches(k, km.Up):
		if d.focus == reviewFocusDiff {
			d.scroll(-1)
			return nil
		}
		return d.move(-1)
	case key.Matches(k, km.Down):
		if d.focus == reviewFocusDiff {
			d.scroll(1)
			return nil
		}
		return d.move(1)
	case key.Matches(k, km.PageUp):
		d.scroll(-page)
	case key.Matches(k, km.PageDown):
		d.scroll(page)
	case key.Matches(k, km.Top):
		d.diffOffset = 0
	case key.Matches(k, km.Bottom):
		d.diffOffset = len(d.render.lines)
	case key.Matches(k, km.Toggle):
		if f := d.current(); f != nil {
			f.selected = !f.selected
		}
		d.err = ""
	case key.Matches(k, km.ToggleAll):
		all := true
		for _, f := range d.files {
			all = all && f.selected
		}
		for i := range d.files {
			d.files[i].selected = !all
		}
		d.err = ""
	case key.Matches(k, km.Generate):
		return d.generate()
	case key.Matches(k, km.Edit):
		return d.setFocus(reviewFocusMessage)
	}
	return nil
}

func (d *Review) move(delta int) Action {
	next := max(0, min(d.cursor+delta, len(d.files)-1))
	if next == d.cursor {
		return nil
	}
	d.cursor = next
	d.diffOffset = 0
	return d.loadDiff()
}

func (d *Review) scroll(delta int) {
	d.diffOffset = max(0, d.diffOffset+delta)
}

func (d *Review) generate() Action {
	if d.generating || d.committing {
		return nil
	}
	files := d.picked()
	if len(files) == 0 {
		d.err = "Pick at least one file with space first."
		return nil
	}
	d.generating = true
	d.err, d.notice = "", ""
	return ActionReviewGenerate{Root: d.repo.Root, Files: files}
}

func (d *Review) commit() Action {
	if d.committing || d.state != reviewReady || len(d.files) == 0 {
		return nil
	}
	var paths []string
	for _, f := range d.picked() {
		paths = append(paths, f.Paths()...)
	}
	if len(paths) == 0 {
		d.err = "Pick at least one file with space first."
		return nil
	}
	msg := strings.TrimSpace(d.ta.Value())
	if msg == "" {
		d.err = "Write a commit message first: c drafts one, e lets you type it."
		return nil
	}
	d.committing = true
	d.err, d.notice = "", ""
	return ActionReviewCommit{Root: d.repo.Root, Paths: paths, Message: msg}
}

// ShortHelp implements help.KeyMap.
func (d *Review) ShortHelp() []key.Binding {
	km := &d.keyMap
	if d.state != reviewReady || len(d.files) == 0 {
		return []key.Binding{km.Refresh, km.Close}
	}
	switch d.focus {
	case reviewFocusMessage:
		back := km.Back
		back.SetHelp("esc", "files")
		return []key.Binding{km.Commit, back, km.Next}
	case reviewFocusDiff:
		return []key.Binding{km.Up, km.Down, km.PageDown, km.PageUp, km.Top, km.Bottom, km.Next, km.Commit, km.Close}
	}
	return []key.Binding{km.Up, km.Down, km.Toggle, km.ToggleAll, km.Generate, km.Edit, km.Commit, km.Next, km.Refresh, km.Close}
}

// FullHelp implements help.KeyMap.
func (d *Review) FullHelp() [][]key.Binding {
	return [][]key.Binding{d.ShortHelp()}
}

// reviewLayout holds the sizes of one frame.
type reviewLayout struct {
	inner        int // content width
	panes        int // rows shared by the file list and the diff
	wide         bool
	listW, listH int
	diffW, diffH int
	msgH         int
	status       []string
}

// Draw implements Dialog.
func (d *Review) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	view, cur := d.view(area.Dx(), area.Dy())
	DrawCenterCursor(scr, area, view, cur)
	return cur
}

// view renders the dialog for an area of w by h cells and returns the
// cursor relative to the dialog's top left corner.
func (d *Review) view(w, h int) (string, *tea.Cursor) {
	t := d.com.Styles
	maxW := reviewMaxWidth
	if d.state != reviewReady || len(d.files) == 0 {
		maxW = reviewCompactW
	}
	width := max(0, min(maxW, w-t.Dialog.View.GetHorizontalBorderSize()))
	inner := max(0, width-t.Dialog.View.GetHorizontalFrameSize())

	rc := NewRenderContext(t, width)
	rc.Title = "Review Changes"
	rc.TitleInfo = d.titleInfo(t)
	rc.Help = renderDialogHelp(t, &d.help, d, inner)

	if d.state != reviewReady || len(d.files) == 0 {
		rc.AddPart(strings.Join(d.compactBody(t, inner), "\n"))
		return rc.Render(), nil
	}

	titleRows := 1 + t.Dialog.Title.GetVerticalFrameSize()
	chrome := t.Dialog.View.GetVerticalFrameSize() + titleRows + 1 +
		t.Dialog.HelpView.GetVerticalFrameSize()
	body := max(8, min(reviewMaxHeight, h)-chrome)
	lay := d.layout(t, inner, body)

	var rows []string
	rows = append(rows, d.panes(t, lay)...)
	rows = append(rows, d.messageRows(t, lay)...)
	rows = append(rows, lay.status...)
	rc.AddPart(strings.Join(rows, "\n"))

	var cur *tea.Cursor
	if d.focus == reviewFocusMessage && d.ta.Focused() {
		if c := d.ta.Cursor(); c != nil {
			c.X += t.Dialog.View.GetBorderLeftSize() + t.Dialog.View.GetPaddingLeft() + 1
			c.Y += t.Dialog.View.GetBorderTopSize() + t.Dialog.View.GetPaddingTop() +
				titleRows + lay.panes + 1
			cur = c
		}
	}
	return rc.Render(), cur
}

func (d *Review) layout(t *styles.Styles, inner, body int) reviewLayout {
	lay := reviewLayout{inner: inner}
	lay.status = d.statusRows(t, inner)
	lay.msgH = max(3, min(8, body/6))
	lay.panes = body - len(lay.status) - lay.msgH - 1
	if lay.panes < 4 {
		lay.msgH = max(1, lay.msgH-(4-lay.panes))
		lay.panes = max(2, body-len(lay.status)-lay.msgH-1)
	}
	lay.wide = inner >= reviewWideAt
	if lay.wide {
		lay.listW = max(reviewMinListW, min(reviewMaxListW, inner/3))
		lay.diffW = max(1, inner-lay.listW-1)
		lay.listH, lay.diffH = lay.panes, lay.panes
		return lay
	}
	lay.listW, lay.diffW = inner, inner
	lay.listH = max(2, min(len(d.files)+1, lay.panes/3))
	lay.diffH = max(1, lay.panes-lay.listH)
	return lay
}

func (d *Review) titleInfo(t *styles.Styles) string {
	if d.state != reviewReady {
		return ""
	}
	var parts []string
	if d.repo.Branch != "" {
		parts = append(parts, d.repo.Branch)
	}
	if n := len(d.files); n > 0 {
		parts = append(parts, fmt.Sprintf("%d file%s", n, plural(n)))
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + t.Control.Muted.Render(strings.Join(parts, " · "))
}

// compactBody is the small dialog shown while loading, outside a
// repository, on a load error and when there is nothing to commit.
func (d *Review) compactBody(t *styles.Styles, inner int) []string {
	w := max(1, inner-2)
	var lines []string
	add := func(st func(...string) string, s string) {
		for l := range strings.SplitSeq(ansi.Wrap(s, w, ""), "\n") {
			lines = append(lines, fitLine(" "+st(l), inner))
		}
	}
	muted := t.Control.Muted.Render
	plain := t.Dialog.NormalItem.UnsetPadding().Render
	lines = append(lines, "")
	switch d.state {
	case reviewLoading:
		add(muted, "Reading changes…")
	case reviewNotRepo:
		add(plain, "This folder isn't a Git repository, so there is nothing to review.")
		lines = append(lines, "")
		add(muted, "Run git init here, or point the tab at a repository with /cd.")
	case reviewFailed:
		add(t.Dialog.OAuth.ErrorText.Render, "Couldn't read the changes: "+d.loadErr)
	default:
		if d.notice != "" {
			add(t.Dialog.OAuth.Success.Render, d.notice)
			lines = append(lines, "")
		}
		add(plain, "Nothing to commit: the working tree is clean.")
	}
	lines = append(lines, "")
	return lines
}

// panes renders the file list and the diff, side by side or stacked.
func (d *Review) panes(t *styles.Styles, lay reviewLayout) []string {
	list := d.listRows(t, lay.listW, lay.listH)
	diff := d.diffPane(t, lay.diffW, lay.diffH)
	if !lay.wide {
		return append(list, diff...)
	}
	out := make([]string, lay.panes)
	for i := range out {
		out[i] = list[i] + " " + diff[i]
	}
	return out
}

func (d *Review) paneHeader(t *styles.Styles, focused bool, text string, w int) string {
	st := t.Dialog.SecondaryText
	if focused {
		st = t.Dialog.PrimaryText
	}
	return fitLine(st.Render(ansi.Truncate(text, max(0, w-st.GetHorizontalFrameSize()), "…")), w)
}

func (d *Review) listRows(t *styles.Styles, w, h int) []string {
	picked := 0
	touched := false
	for _, f := range d.files {
		if f.selected {
			picked++
		}
		touched = touched || f.touched
	}
	header := fmt.Sprintf("Files · %d of %d picked", picked, len(d.files))
	if touched {
		header += " · • agent"
	}
	rows := []string{d.paneHeader(t, d.focus == reviewFocusFiles, header, w)}

	visible := max(0, h-1)
	if d.cursor < d.listOffset {
		d.listOffset = d.cursor
	}
	if visible > 0 && d.cursor >= d.listOffset+visible {
		d.listOffset = d.cursor - visible + 1
	}
	d.listOffset = max(0, min(d.listOffset, len(d.files)-visible))

	overflow := len(d.files) > visible
	rowW := w
	if overflow {
		rowW = max(0, w-1)
	}
	var body []string
	for i := d.listOffset; i < len(d.files) && len(body) < visible; i++ {
		body = append(body, d.fileRow(t, d.files[i], i == d.cursor, rowW))
	}
	for len(body) < visible {
		body = append(body, fitLine("", rowW))
	}
	if overflow && visible > 0 {
		joined := joinScrollbar(t, strings.Join(body, "\n"), visible, len(d.files), visible, d.listOffset)
		body = strings.Split(joined, "\n")
	}
	for _, l := range body {
		rows = append(rows, fitLine(l, w))
	}
	return rows
}

// fileRow renders "[x] M  • path  +3 -1".
func (d *Review) fileRow(t *styles.Styles, f reviewFile, current bool, w int) string {
	check := "[ ]"
	if f.selected {
		check = "[x]"
	}
	dot := " "
	if f.touched {
		dot = "•"
	}
	counts := reviewCounts(f.File)
	prefix := check + " " + reviewXY(f.File) + " " + dot + " "

	st := t.Dialog.NormalItem
	if current {
		st = t.Dialog.SelectedItem
	}
	text := max(0, w-st.GetHorizontalFrameSize())
	pathW := max(1, text-ansi.StringWidth(prefix)-ansi.StringWidth(counts)-1)
	path := f.Path
	if over := ansi.StringWidth(path) - pathW; over > 0 {
		path = ansi.TruncateLeft(path, over+1, "…")
	}
	gap := strings.Repeat(" ", max(1, text-ansi.StringWidth(prefix)-ansi.StringWidth(path)-ansi.StringWidth(counts)))

	if current {
		return fitLine(st.Render(ansi.Truncate(prefix+path+gap+counts, text, "…")), w)
	}
	styled := prefix + path + gap + d.styledCounts(t, f.File)
	if f.touched {
		styled = check + " " + reviewXY(f.File) + " " + t.Control.On.Render(dot) + " " + path + gap + d.styledCounts(t, f.File)
	}
	return fitLine(st.Render(ansi.Truncate(styled, text, "…")), w)
}

// reviewXY is the file's two letter git status, staged then unstaged,
// with "??" for untracked files.
func reviewXY(f gitreview.File) string {
	if f.Untracked {
		return "??"
	}
	c := func(b byte) string {
		if b == 0 || b == '.' {
			return " "
		}
		return string(b)
	}
	return c(f.X) + c(f.Y)
}

func reviewCounts(f gitreview.File) string {
	if f.Binary {
		return "bin"
	}
	return fmt.Sprintf("+%d -%d", f.Added, f.Removed)
}

func (d *Review) styledCounts(t *styles.Styles, f gitreview.File) string {
	if f.Binary {
		return t.Control.Muted.Render("bin")
	}
	return t.Files.Additions.Render(fmt.Sprintf("+%d", f.Added)) + " " +
		t.Files.Deletions.Render(fmt.Sprintf("-%d", f.Removed))
}

func (d *Review) diffPane(t *styles.Styles, w, h int) []string {
	f := d.current()
	if f == nil || h <= 0 {
		return padRows(nil, w, h)
	}
	split := w >= reviewSplitAt
	codeW := max(1, w-1)
	lines, ok := d.diffLines(t, *f, codeW, split)

	header := f.Path
	if f.OrigPath != "" && f.OrigPath != f.Path {
		header = f.OrigPath + " → " + f.Path
	}
	visible := max(0, h-1)
	d.diffRows = visible
	if ok {
		d.diffOffset = max(0, min(d.diffOffset, len(lines)-visible))
		mode := "unified"
		if split {
			mode = "split"
		}
		header += fmt.Sprintf(" · %s · %d lines", mode, len(lines))
	} else {
		d.diffOffset = 0
	}
	rows := []string{d.paneHeader(t, d.focus == reviewFocusDiff, header, w)}
	if visible == 0 {
		return rows
	}

	end := min(len(lines), d.diffOffset+visible)
	body := make([]string, 0, visible)
	for _, l := range lines[min(d.diffOffset, end):end] {
		body = append(body, fitLine(l, codeW))
	}
	for len(body) < visible {
		body = append(body, fitLine("", codeW))
	}
	joined := joinScrollbar(t, strings.Join(body, "\n"), visible, len(lines), visible, d.diffOffset)
	for l := range strings.SplitSeq(joined, "\n") {
		rows = append(rows, fitLine(l, w))
	}
	return padRows(rows, w, h)
}

// diffLines returns the rendered diff of f, or a short note when there is
// none to show. ok is false for notes.
func (d *Review) diffLines(t *styles.Styles, f reviewFile, w int, split bool) ([]string, bool) {
	note := func(st func(...string) string, s string) []string {
		return []string{" " + st(s)}
	}
	if f.Binary {
		return note(t.Control.Muted.Render, "Binary file; no preview."), false
	}
	df, loaded := d.diffs[f.Path]
	switch {
	case !loaded:
		return note(t.Control.Muted.Render, "Loading diff…"), false
	case df.err != nil:
		return note(t.Dialog.OAuth.ErrorText.Render, oneLine(df.err.Error())), false
	case df.old == df.new:
		return note(t.Control.Muted.Render, "No content changes (only the file mode changed)."), false
	}
	r := d.render
	if r.lines != nil && r.path == f.Path && r.width == w && r.split == split {
		return r.lines, true
	}
	before := f.Path
	if f.OrigPath != "" {
		before = f.OrigPath
	}
	dv := common.DiffFormatter(t).Before(before, df.old).After(f.Path, df.new).Width(w).Wrap(true)
	if split {
		dv = dv.Split()
	}
	lines := strings.Split(strings.TrimRight(dv.String(), "\n"), "\n")
	d.render = reviewRender{path: f.Path, width: w, split: split, lines: lines}
	return lines, true
}

func (d *Review) messageRows(t *styles.Styles, lay reviewLayout) []string {
	label := "Message"
	if d.ta.Value() == "" && d.focus != reviewFocusMessage {
		label += " · c drafts one with the small model, e to type"
	}
	rows := []string{d.paneHeader(t, d.focus == reviewFocusMessage, label, lay.inner)}
	d.ta.SetWidth(max(1, lay.inner-2))
	d.ta.SetHeight(lay.msgH)
	view := strings.Split(d.ta.View(), "\n")
	if len(view) > lay.msgH {
		view = view[:lay.msgH]
	}
	for _, l := range view {
		rows = append(rows, fitLine(" "+l, lay.inner))
	}
	return padRows(rows, lay.inner, lay.msgH+1)
}

// statusRows is the error, notice or progress line under the message.
func (d *Review) statusRows(t *styles.Styles, inner int) []string {
	w := max(1, inner-2)
	var st func(...string) string
	var text string
	switch {
	case d.err != "":
		st, text = t.Dialog.OAuth.ErrorText.Render, d.err
	case d.committing:
		st, text = t.Control.Muted.Render, "Committing…"
	case d.generating:
		st, text = t.Control.Muted.Render, "Drafting a message with the small model…"
	case d.refreshing:
		st, text = t.Control.Muted.Render, "Reading changes…"
	case d.notice != "":
		st, text = t.Dialog.OAuth.Success.Render, d.notice
	default:
		st, text = t.Control.Muted.Render, "Commits only the picked files. Nothing is pushed."
	}
	lines := strings.Split(ansi.Wrap(text, w, ""), "\n")
	if len(lines) > reviewStatusLines {
		lines = lines[:reviewStatusLines]
		lines[reviewStatusLines-1] = ansi.Truncate(lines[reviewStatusLines-1], w-1, "") + "…"
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = fitLine(" "+st(l), inner)
	}
	return out
}

// fitLine truncates s to w cells and pads it with spaces to exactly w.
func fitLine(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	if pad := w - ansi.StringWidth(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// padRows clips or pads rows to h rows of w cells.
func padRows(rows []string, w, h int) []string {
	if len(rows) > h {
		rows = rows[:h]
	}
	for len(rows) < h {
		rows = append(rows, fitLine("", w))
	}
	return rows
}
