package dialog

import (
	"context"
	"fmt"
	"image"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/workspace"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/dustin/go-humanize"
	"github.com/sahilm/fuzzy"
)

// ResumeID is the identifier for the resume conversation dialog.
const ResumeID = "resume"

const (
	// resumeIndexLimit caps how many of the most recent conversations
	// get their prompts loaded for search.
	resumeIndexLimit = 300
	// resumeTextPerMessage and resumeTextPerSession cap the prompt text
	// kept for search, so a pasted log doesn't bloat the index.
	resumeTextPerMessage = 2000
	resumeTextPerSession = 8000
	// resumePreviewHeight is the number of lines the preview pane takes.
	resumePreviewHeight = 4
	// resumeIndexTimeout bounds the background prompt load.
	// resumeInfoMaxPercent is wider than the sessions dialog's budget: the
	// info column also carries the message count and the "open" marker.
	resumeInfoMaxPercent = 50
	resumeIndexTimeout   = 30 * time.Second
)

// ResumePrompts is the prompt text indexed for one conversation.
type ResumePrompts struct {
	// First is the first prompt of the conversation, on one line.
	First string
	// Last is the most recent prompt, on one line.
	Last string
	// Prompts is how many prompts were read.
	Prompts int
	// text is the lower-cased search text of every prompt read.
	text string
}

// ResumeIndexMsg carries the prompts loaded for the resume dialog.
type ResumeIndexMsg struct {
	Prompts map[string]ResumePrompts
}

// Resume is the resume conversation dialog: a searchable list of past
// conversations with a preview of the focused one.
type Resume struct {
	com       *common.Common
	help      help.Model
	list      *list.FilterableList
	input     textinput.Model
	sessions  []session.Session
	items     map[string]*resumeItem
	prompts   map[string]ResumePrompts
	indexed   bool
	currentID string

	bodyArea      image.Rectangle
	mouseScrolled bool
	lastClickTime time.Time
	lastClickID   string

	keyMap struct {
		Select   key.Binding
		Next     key.Binding
		Previous key.Binding
		UpDown   key.Binding
		Close    key.Binding
	}
}

var _ Dialog = (*Resume)(nil)

// NewResume creates the resume dialog, prefiltered with query. The
// returned command loads prompt text in the background; until it lands,
// search covers titles only.
func NewResume(com *common.Common, currentSessionID, query string) (*Resume, tea.Cmd, error) {
	all, err := com.Workspace.ListSessions(context.TODO())
	if err != nil {
		return nil, nil, err
	}
	r := &Resume{
		com:       com,
		sessions:  resumableSessions(all),
		items:     make(map[string]*resumeItem),
		prompts:   make(map[string]ResumePrompts),
		currentID: currentSessionID,
	}
	for _, s := range r.sessions {
		r.items[s.ID] = &resumeItem{
			SessionItem: &SessionItem{Versioned: list.NewVersioned(), Session: s, t: com.Styles},
			current:     s.ID == currentSessionID,
		}
	}

	r.help = help.New()
	r.help.Styles = com.Styles.DialogHelpStyles()

	r.list = list.NewFilterableList()
	r.list.Focus()

	r.input = textinput.New()
	r.input.SetVirtualCursor(false)
	r.input.Placeholder = "Search titles and prompts"
	r.input.SetStyles(com.Styles.TextInput)
	r.input.SetValue(strings.TrimSpace(query))
	r.input.CursorEnd()
	r.input.Focus()

	r.keyMap.Select = key.NewBinding(
		key.WithKeys("enter", "tab", "ctrl+y"),
		key.WithHelp("enter", "resume"),
	)
	r.keyMap.Next = key.NewBinding(key.WithKeys("down", "ctrl+n"))
	r.keyMap.Previous = key.NewBinding(key.WithKeys("up", "ctrl+p"))
	r.keyMap.UpDown = key.NewBinding(
		key.WithKeys("up", "down"),
		key.WithHelp("↑↓", "choose"),
	)
	r.keyMap.Close = CloseKey

	r.applyFilter()
	return r, resumeIndexCmd(com.Workspace, r.sessions), nil
}

// resumableSessions drops conversations with no messages; the list
// comes newest first from the workspace.
func resumableSessions(all []session.Session) []session.Session {
	out := make([]session.Session, 0, len(all))
	for _, s := range all {
		if s.MessageCount == 0 || s.ParentSessionID != "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

// resumeIndexCmd loads the prompts of the most recent conversations.
func resumeIndexCmd(ws workspace.Workspace, sessions []session.Session) tea.Cmd {
	ids := make([]string, 0, min(len(sessions), resumeIndexLimit))
	for _, s := range sessions[:min(len(sessions), resumeIndexLimit)] {
		ids = append(ids, s.ID)
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), resumeIndexTimeout)
		defer cancel()
		out := make(map[string]ResumePrompts, len(ids))
		for _, id := range ids {
			if ctx.Err() != nil {
				break
			}
			msgs, err := ws.ListUserMessages(ctx, id)
			if err != nil {
				continue
			}
			out[id] = buildResumePrompts(msgs)
		}
		return ResumeIndexMsg{Prompts: out}
	}
}

// buildResumePrompts indexes a conversation's prompts, which arrive
// newest first.
func buildResumePrompts(msgs []message.Message) ResumePrompts {
	var p ResumePrompts
	var b strings.Builder
	for i := len(msgs) - 1; i >= 0; i-- {
		content := msgs[i].Content()
		text := oneLine(content.Text)
		// Hidden messages are generated continuations, not prompts.
		if text == "" || content.Hidden {
			continue
		}
		if p.First == "" {
			p.First = text
		}
		p.Last = text
		p.Prompts++
		if b.Len() < resumeTextPerSession {
			b.WriteString(strings.ToLower(truncateBytes(text, resumeTextPerMessage)))
			b.WriteByte('\n')
		}
	}
	p.text = truncateBytes(b.String(), resumeTextPerSession)
	return p
}

// oneLine collapses runs of whitespace, newlines included, to one space.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// truncateBytes cuts s to at most n bytes without splitting a rune.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// resumeHit is one conversation that matches a search.
type resumeHit struct {
	session session.Session
	// titleIdx are the byte offsets of matched title text.
	titleIdx []int
}

// matchResume returns the conversations that contain every word of
// query in their title or prompts. Title matches come first; each group
// keeps the newest-first order.
func matchResume(sessions []session.Session, prompts map[string]ResumePrompts, query string) []resumeHit {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		hits := make([]resumeHit, len(sessions))
		for i, s := range sessions {
			hits[i] = resumeHit{session: s}
		}
		return hits
	}
	var inTitle, inPrompts []resumeHit
	for _, s := range sessions {
		title := strings.ToLower(s.Title)
		if !allWordsIn(words, title, prompts[s.ID].text, s.ID, session.HashID(s.ID)) {
			continue
		}
		var idx []int
		allTitle := true
		// Highlight only when lower-casing kept the byte offsets.
		mark := len(title) == len(s.Title)
		for _, w := range words {
			at := strings.Index(title, w)
			if at < 0 {
				allTitle = false
				continue
			}
			for i := range len(w) {
				if mark {
					idx = append(idx, at+i)
				}
			}
		}
		slices.Sort(idx)
		hit := resumeHit{session: s, titleIdx: slices.Compact(idx)}
		if allTitle {
			inTitle = append(inTitle, hit)
		} else {
			inPrompts = append(inPrompts, hit)
		}
	}
	return append(inTitle, inPrompts...)
}

// allWordsIn reports whether every word is in the title, the prompt
// text, or starts the session ID or its short hash (the one the exit
// banner prints).
func allWordsIn(words []string, title, text, id, hash string) bool {
	id = strings.ToLower(id)
	for _, w := range words {
		if !strings.Contains(title, w) && !strings.Contains(text, w) &&
			!strings.HasPrefix(id, w) && !strings.HasPrefix(hash, w) {
			return false
		}
	}
	return true
}

// applyFilter rebuilds the list from the current query, keeping the
// focused conversation when it still matches.
func (r *Resume) applyFilter() {
	prev := ""
	if it := r.selectedItem(); it != nil {
		prev = it.Session.ID
	}
	hits := matchResume(r.sessions, r.prompts, r.input.Value())
	items := make([]list.FilterableItem, 0, len(hits))
	selected := 0
	for i, h := range hits {
		it := r.items[h.session.ID]
		it.SetMatch(fuzzy.Match{Str: it.Title, MatchedIndexes: h.titleIdx})
		items = append(items, it)
		if h.session.ID == prev {
			selected = i
		}
	}
	r.list.SetItems(items...)
	r.list.ScrollToTop()
	r.list.SetSelected(selected)
	r.list.ScrollToSelected()
}

// SetIndex hands the dialog the loaded prompts and refreshes the list.
func (r *Resume) SetIndex(prompts map[string]ResumePrompts) {
	if prompts != nil {
		r.prompts = prompts
	}
	r.indexed = true
	r.applyFilter()
}

// ID implements Dialog.
func (r *Resume) ID() string {
	return ResumeID
}

// HandleMsg implements Dialog.
func (r *Resume) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case ResumeIndexMsg:
		r.SetIndex(msg.Prompts)
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, r.keyMap.Close):
			return ActionClose{}
		case key.Matches(msg, r.keyMap.Previous):
			if r.list.IsSelectedFirst() {
				r.list.SelectLast()
			} else {
				r.list.SelectPrev()
			}
			r.list.ScrollToSelected()
			r.mouseScrolled = false
		case key.Matches(msg, r.keyMap.Next):
			if r.list.IsSelectedLast() {
				r.list.SelectFirst()
			} else {
				r.list.SelectNext()
			}
			r.list.ScrollToSelected()
			r.mouseScrolled = false
		case key.Matches(msg, r.keyMap.Select):
			if it := r.selectedItem(); it != nil {
				return ActionSelectSession{it.Session}
			}
		default:
			prev := r.input.Value()
			var cmd tea.Cmd
			r.input, cmd = r.input.Update(msg)
			if r.input.Value() != prev {
				r.applyFilter()
			}
			return ActionCmd{cmd}
		}
	case common.CoalescedWheelMsg:
		if image.Pt(msg.Mouse.X, msg.Mouse.Y).In(r.bodyArea) {
			r.list.ScrollBy(int(msg.DeltaY))
			r.mouseScrolled = true
		}
	case tea.MouseClickMsg:
		return r.handleMouseClick(msg)
	}
	return nil
}

func (r *Resume) handleMouseClick(msg tea.MouseClickMsg) Action {
	area := r.bodyArea
	area.Max.X = min(area.Max.X, area.Min.X+r.list.Width())
	point := image.Pt(msg.X, msg.Y)
	if msg.Button != tea.MouseLeft || !point.In(area) {
		r.lastClickTime, r.lastClickID = time.Time{}, ""
		return nil
	}
	index, _ := r.list.ItemIndexAtPosition(point.X-area.Min.X, point.Y-area.Min.Y)
	if index < 0 {
		r.lastClickTime, r.lastClickID = time.Time{}, ""
		return nil
	}
	it := r.list.ItemAt(index).(*resumeItem)
	now := time.Now()
	if r.lastClickID == it.ID() && now.Sub(r.lastClickTime) <= sessionDoubleClickThreshold {
		r.lastClickTime, r.lastClickID = time.Time{}, ""
		return ActionSelectSession{it.Session}
	}
	r.lastClickTime, r.lastClickID = now, it.ID()
	r.list.SetSelected(index)
	return nil
}

func (r *Resume) selectedItem() *resumeItem {
	if it := r.list.SelectedItem(); it != nil {
		return it.(*resumeItem)
	}
	return nil
}

// Draw implements [Dialog].
func (r *Resume) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := r.com.Styles
	width := max(0, min(defaultDialogMaxWidth+10, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	height := max(0, min(defaultDialogHeight+resumePreviewHeight+1, area.Dy()-t.Dialog.View.GetVerticalBorderSize()))
	innerWidth := width - t.Dialog.View.GetHorizontalFrameSize()
	r.input.SetWidth(dialogInputTextWidth(t, r.input, innerWidth))
	preview := r.renderPreview(t, innerWidth-t.Dialog.List.GetHorizontalFrameSize())
	listHeight, listTotalHeight, listWidth := sizeDialogList(t, r.list, innerWidth, height-lipgloss.Height(preview))
	applyInfoColumnVisibility(r.list.FilteredItems(), listWidth, resumeInfoMaxPercent)

	rc := NewRenderContext(t, width)
	rc.Title = "Resume Conversation"
	rc.AddPart(t.Dialog.InputPrompt.Render(r.input.View()))
	bodyView := t.Dialog.List.Height(r.list.Height()).Render(r.list.Render())
	bodyView = joinScrollbar(t, bodyView, listHeight, listTotalHeight, listHeight, r.list.Offset())
	rc.AddPart(bodyView)
	rc.AddPart(t.Dialog.List.Render(preview))
	rc.Help = renderDialogHelp(t, &r.help, r, innerWidth)

	view := rc.Render()
	r.updateBodyArea(area, view, bodyView, preview, rc.Help, t, innerWidth, listHeight)
	cur := InputCursor(t, r.input.Cursor())
	DrawCenterCursor(scr, area, view, cur)
	return cur
}

// updateBodyArea records where the list sits on screen, for mouse
// handling. The list is followed by the preview and the help line.
func (r *Resume) updateBodyArea(
	area uv.Rectangle,
	view, bodyView, preview, helpView string,
	t *styles.Styles,
	bodyWidth, bodyHeight int,
) {
	viewStyle, bodyStyle := t.Dialog.View, t.Dialog.List
	viewWidth, viewHeight := lipgloss.Size(view)
	dialogArea := common.CenterRect(area, min(viewWidth, area.Dx()), min(viewHeight, area.Dy()))
	bodyTop := dialogArea.Max.Y -
		viewStyle.GetMarginBottom() -
		viewStyle.GetBorderBottomSize() -
		viewStyle.GetPaddingBottom() -
		lipgloss.Height(helpView) -
		lipgloss.Height(bodyStyle.Render(preview)) -
		lipgloss.Height(bodyView)
	minPt := image.Pt(
		dialogArea.Min.X+viewStyle.GetMarginLeft()+viewStyle.GetBorderLeftSize()+
			viewStyle.GetPaddingLeft()+bodyStyle.GetMarginLeft()+
			bodyStyle.GetBorderLeftSize()+bodyStyle.GetPaddingLeft(),
		bodyTop+bodyStyle.GetMarginTop()+bodyStyle.GetBorderTopSize()+bodyStyle.GetPaddingTop(),
	)
	r.bodyArea = image.Rect(minPt.X, minPt.Y, minPt.X+bodyWidth, minPt.Y+bodyHeight).
		Intersect(dialogArea).Intersect(area)
}

// renderPreview draws the focused conversation's first and last prompt
// and its stats, always resumePreviewHeight lines tall.
func (r *Resume) renderPreview(t *styles.Styles, width int) string {
	width = max(0, width)
	label := func(s string) string { return t.Control.Label.Render(fmt.Sprintf("%-6s", s)) }
	line := func(s string) string { return ansi.Truncate(s, width, "…") }
	lines := []string{t.Control.Muted.Render(strings.Repeat("─", width))}

	it := r.selectedItem()
	switch {
	case it == nil && len(r.sessions) == 0:
		lines = append(lines, t.Control.Muted.Render("No saved conversations in this project yet."))
	case it == nil:
		lines = append(lines, t.Control.Muted.Render("No conversations match."))
		if !r.indexed {
			lines = append(lines, t.Control.Muted.Render("Still loading prompts to search…"))
		}
	default:
		s := it.Session
		p, ok := r.prompts[s.ID]
		switch {
		case ok && p.First != "":
			lines = append(lines, line(label("first")+" "+t.Control.Value.Render(p.First)))
			if p.Prompts > 1 {
				lines = append(lines, line(label("last")+" "+t.Control.Value.Render(p.Last)))
			}
		case !r.indexed:
			lines = append(lines, t.Control.Muted.Render("Loading prompts…"))
		}
		stats := []string{fmt.Sprintf("%d messages", s.MessageCount)}
		if s.Cost > 0 {
			stats = append(stats, fmt.Sprintf("$%.2f", s.Cost))
		}
		stats = append(stats,
			"started "+time.Unix(s.CreatedAt, 0).Format("Jan 2 15:04"),
			"updated "+humanize.Time(time.Unix(s.UpdatedAt, 0)),
		)
		lines = append(lines, line(t.Control.Muted.Render(strings.Join(stats, " · "))))
	}
	for len(lines) < resumePreviewHeight {
		lines = append(lines, "")
	}
	return strings.Join(lines[:resumePreviewHeight], "\n")
}

// ShortHelp implements [help.KeyMap].
func (r *Resume) ShortHelp() []key.Binding {
	return []key.Binding{r.keyMap.UpDown, r.keyMap.Select, r.keyMap.Close}
}

// FullHelp implements [help.KeyMap].
func (r *Resume) FullHelp() [][]key.Binding {
	return [][]key.Binding{r.ShortHelp()}
}

// resumeItem is a conversation row: its title, how long ago it was
// last used, and its size.
type resumeItem struct {
	*SessionItem
	current bool
}

// InfoText returns the secondary text shown on the right of the row.
func (r *resumeItem) InfoText() string {
	info := humanize.Time(time.Unix(r.UpdatedAt, 0)) + fmt.Sprintf(" · %d msgs", r.MessageCount)
	if r.current {
		info = "open · " + info
	}
	return info
}

// Render returns the row.
func (r *resumeItem) Render(width int) string {
	info := r.InfoText()
	if r.hideInfo {
		info = ""
	}
	if r.cache == nil {
		r.cache = make(map[int]string)
	}
	return renderItem(ListItemStyles{
		ItemBlurred:     r.t.Dialog.NormalItem,
		ItemFocused:     r.t.Dialog.SelectedItem,
		InfoTextBlurred: r.t.Dialog.Sessions.InfoBlurred,
		InfoTextFocused: r.t.Dialog.Sessions.InfoFocused,
	}, r.Title, info, r.focused, width, r.cache, &r.m)
}

// ResumeQuery reports whether value is a resume command ("/resume" or
// "/continue", with an optional search) and returns the search text.
func ResumeQuery(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "/") {
		return "", false
	}
	name, rest, _ := strings.Cut(value, " ")
	switch strings.ToLower(strings.TrimPrefix(name, "/")) {
	case "resume", "continue":
	default:
		return "", false
	}
	if strings.Contains(name, "\n") {
		return "", false
	}
	return strings.TrimSpace(rest), true
}
