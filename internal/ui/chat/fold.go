package chat

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
)

// foldMaxKinds is how many tool kinds the summary names before it folds
// the rest into "+N more".
const foldMaxKinds = 4

// TurnFoldID returns the ID of the fold header for the turn started by the
// given user message.
func TurnFoldID(userMessageID string) string {
	return userMessageID + ":turn-fold"
}

// ToolCount is how many calls of one tool a turn made.
type ToolCount struct {
	Name  string
	Count int
}

// FoldSummary is what a fold header shows about its turn.
type FoldSummary struct {
	// Tools is the number of tool calls in the turn.
	Tools int
	// Kinds counts calls per tool name, most used first.
	Kinds []ToolCount
	// Live is true while the turn is still running.
	Live bool
	// Duration is how long the turn took; zero when unknown.
	Duration time.Duration
}

// TurnFoldItem is the one-line header ("Worked for 1m 12s · 8 tools")
// that folds a turn's tool calls and intermediate narration. The chat
// hides and shows the turn's items; this item only holds the state and
// draws the line.
type TurnFoldItem struct {
	*list.Versioned
	*focusableMessageItem

	id    string
	sty   *styles.Styles
	start time.Time

	summary FoldSummary

	expanded bool
	// userSet is true once the user toggled the fold. From then on the
	// chat no longer opens or closes it on its own.
	userSet bool
}

// NewTurnFoldItem returns a fold header for the turn started by the user
// message with the given ID and creation time.
func NewTurnFoldItem(sty *styles.Styles, userMessageID string, start time.Time) *TurnFoldItem {
	v := list.NewVersioned()
	return &TurnFoldItem{
		Versioned:            v,
		focusableMessageItem: newFocusableMessageItem(v),
		id:                   TurnFoldID(userMessageID),
		sty:                  sty,
		start:                start,
	}
}

// ID implements MessageItem.
func (f *TurnFoldItem) ID() string { return f.id }

// Start returns when the turn started.
func (f *TurnFoldItem) Start() time.Time { return f.start }

// Finished implements list.Item. Every change bumps the version.
func (f *TurnFoldItem) Finished() bool { return true }

// Expanded reports whether the turn's items are shown.
func (f *TurnFoldItem) Expanded() bool { return f.expanded }

// UserSet reports whether the user has toggled this fold.
func (f *TurnFoldItem) UserSet() bool { return f.userSet }

// SetExpanded opens or closes the fold without marking it as user-set.
func (f *TurnFoldItem) SetExpanded(expanded bool) {
	if f.expanded == expanded {
		return
	}
	f.expanded = expanded
	f.Bump()
}

// ToggleExpanded implements Expandable.
func (f *TurnFoldItem) ToggleExpanded() bool {
	f.expanded = !f.expanded
	f.userSet = true
	f.Bump()
	return f.expanded
}

// HandleMouseClick implements list.MouseClickable: a click anywhere on
// the line toggles the fold.
func (f *TurnFoldItem) HandleMouseClick(btn ansi.MouseButton, _, _ int) bool {
	return btn == ansi.MouseButton1
}

// HandleKeyEvent implements KeyEventHandler. The fold has no keys of its
// own; the chat's expand key toggles it.
func (f *TurnFoldItem) HandleKeyEvent(tea.KeyMsg) (bool, tea.Cmd) {
	return false, nil
}

// Summary returns what the header currently shows.
func (f *TurnFoldItem) Summary() FoldSummary { return f.summary }

// SetSummary updates the header line.
func (f *TurnFoldItem) SetSummary(s FoldSummary) {
	if f.summary.Tools == s.Tools && f.summary.Live == s.Live &&
		f.summary.Duration == s.Duration && slices.Equal(f.summary.Kinds, s.Kinds) {
		return
	}
	f.summary = s
	f.Bump()
}

// RawRender implements list.RawRenderable.
func (f *TurnFoldItem) RawRender(width int) string {
	return ansi.Truncate(f.line(), max(0, width-MessageLeftPaddingTotal), "…")
}

// Render implements list.Item.
func (f *TurnFoldItem) Render(width int) string {
	prefix := f.sty.Messages.ToolCallBlurred.Render()
	if f.focused {
		prefix = f.sty.Messages.ToolCallFocused.Render()
	}
	return prefix + f.RawRender(width)
}

func (f *TurnFoldItem) line() string {
	subtle := f.sty.Messages.AssistantInfoProvider
	muted := f.sty.Messages.AssistantInfoModel

	arrow := "▸"
	if f.expanded {
		arrow = "▾"
	}
	head := "Worked"
	if f.summary.Live {
		head = "Working"
	} else if f.summary.Duration > 0 {
		head = "Worked for " + FormatTurnDuration(f.summary.Duration)
	}
	tools := fmt.Sprintf("%d tool call", f.summary.Tools)
	if f.summary.Tools != 1 {
		tools += "s"
	}
	parts := []string{subtle.Render(arrow) + " " + muted.Render(head), subtle.Render(tools)}
	if kinds := formatKinds(f.summary.Kinds); kinds != "" {
		parts = append(parts, subtle.Render(kinds))
	}
	return strings.Join(parts, subtle.Render(" · "))
}

// formatKinds renders "3 bash, 4 view, 1 edit", naming at most
// foldMaxKinds tools.
func formatKinds(kinds []ToolCount) string {
	if len(kinds) == 0 {
		return ""
	}
	shown := kinds[:min(len(kinds), foldMaxKinds)]
	out := make([]string, 0, len(shown)+1)
	for _, k := range shown {
		out = append(out, fmt.Sprintf("%d %s", k.Count, k.Name))
	}
	if rest := len(kinds) - len(shown); rest > 0 {
		out = append(out, fmt.Sprintf("+%d more", rest))
	}
	return strings.Join(out, ", ")
}

// SortToolCounts orders counts most used first, then by name.
func SortToolCounts(kinds []ToolCount) {
	slices.SortFunc(kinds, func(a, b ToolCount) int {
		if c := cmp.Compare(b.Count, a.Count); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
}

// FormatTurnDuration renders a duration as "45s", "1m 12s" or "1h 3m".
func FormatTurnDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Second {
		return "<1s"
	}
	h := int(d / time.Hour)
	m := int(d % time.Hour / time.Minute)
	s := int(d % time.Minute / time.Second)
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}
