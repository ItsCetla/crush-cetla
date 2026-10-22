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

	// Files is how many distinct files the turn's edits and writes
	// changed, with the lines they added and removed.
	Files     int
	Additions int
	Removals  int
	// Commands is the number of shell commands the turn ran.
	Commands int
	// Tests is how the turn's last command went when it was a test run.
	Tests TestOutcome
}

// TestOutcome is the result of a turn's closing test run.
type TestOutcome int

const (
	// TestsUnknown means the turn didn't end on a finished test run.
	TestsUnknown TestOutcome = iota
	TestsPassed
	TestsFailed
)

// equal reports whether two summaries draw the same line.
func (s FoldSummary) equal(o FoldSummary) bool {
	return s.Tools == o.Tools && s.Live == o.Live && s.Duration == o.Duration &&
		s.Files == o.Files && s.Additions == o.Additions && s.Removals == o.Removals &&
		s.Commands == o.Commands && s.Tests == o.Tests && slices.Equal(s.Kinds, o.Kinds)
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

	// costBase is the session's spend when the turn started, and cost
	// what the session has spent since. Both are only known for turns
	// started while the UI was watching; reloaded history has no base.
	costBase    float64
	hasCostBase bool
	cost        float64
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
	if f.summary.equal(s) {
		return
	}
	f.summary = s
	f.Bump()
}

// SetCostBase records the session's spend when the turn started. Only the
// first call counts.
func (f *TurnFoldItem) SetCostBase(sessionCost float64) {
	if f.hasCostBase {
		return
	}
	f.costBase = sessionCost
	f.hasCostBase = true
}

// HasCostBase reports whether the turn's starting spend is known.
func (f *TurnFoldItem) HasCostBase() bool { return f.hasCostBase }

// SetSessionCost updates the turn's spend from the session's running
// total. It's a no-op for turns without a starting spend.
func (f *TurnFoldItem) SetSessionCost(sessionCost float64) {
	if !f.hasCostBase {
		return
	}
	cost := max(0, sessionCost-f.costBase)
	if cost == f.cost {
		return
	}
	f.cost = cost
	f.Bump()
}

// Cost returns what the turn has spent so far, or zero when unknown.
func (f *TurnFoldItem) Cost() float64 { return f.cost }

// RawRender implements list.RawRenderable.
func (f *TurnFoldItem) RawRender(width int) string {
	avail := max(0, width-MessageLeftPaddingTotal)
	return ansi.Truncate(f.line(avail), avail, "…")
}

// Render implements list.Item.
func (f *TurnFoldItem) Render(width int) string {
	prefix := f.sty.Messages.ToolCallBlurred.Render()
	if f.focused {
		prefix = f.sty.Messages.ToolCallFocused.Render()
	}
	return prefix + f.RawRender(width)
}

// foldSep separates the receipt's segments.
const foldSep = " · "

// line draws the header, keeping as many receipt segments as fit in width.
func (f *TurnFoldItem) line(width int) string {
	subtle := f.sty.Messages.AssistantInfoProvider
	muted := f.sty.Messages.AssistantInfoModel

	arrow := "▸"
	if f.expanded {
		arrow = "▾"
	}
	segs := FitReceipt(arrow+" ", ReceiptSegments(f.summary, f.cost), width)
	parts := make([]string, len(segs))
	for i, seg := range segs {
		if i == 0 {
			parts[i] = subtle.Render(arrow) + " " + muted.Render(seg)
			continue
		}
		parts[i] = subtle.Render(seg)
	}
	return strings.Join(parts, subtle.Render(foldSep))
}

// ReceiptSegments returns the fold line's segments, most important first:
// the head ("Worked for 2m 14s"), then files changed, commands, the
// closing test run, spend, tool calls and tool kinds. Segments without
// data are left out. cost is the turn's spend, zero when unknown.
func ReceiptSegments(s FoldSummary, cost float64) []string {
	head := "Worked"
	if s.Live {
		head = "Working"
	} else if s.Duration > 0 {
		head = "Worked for " + FormatTurnDuration(s.Duration)
	}
	segs := []string{head}
	if s.Files > 0 {
		files := plural(s.Files, "file") + " changed"
		var lines []string
		if s.Additions > 0 {
			lines = append(lines, fmt.Sprintf("+%d", s.Additions))
		}
		if s.Removals > 0 {
			lines = append(lines, fmt.Sprintf("−%d", s.Removals))
		}
		if len(lines) > 0 {
			files += " (" + strings.Join(lines, " ") + ")"
		}
		segs = append(segs, files)
	}
	if s.Commands > 0 {
		segs = append(segs, plural(s.Commands, "command"))
	}
	switch s.Tests {
	case TestsPassed:
		segs = append(segs, "tests passed")
	case TestsFailed:
		segs = append(segs, "tests failed")
	}
	if c := formatTurnCost(cost); c != "" {
		segs = append(segs, c)
	}
	segs = append(segs, plural(s.Tools, "tool call"))
	if kinds := formatKinds(s.Kinds); kinds != "" {
		segs = append(segs, kinds)
	}
	return segs
}

// FitReceipt drops segments from the right until the line, with prefix
// before the first segment, fits in width. The first segment is always
// kept; the caller truncates it when even that doesn't fit.
func FitReceipt(prefix string, segs []string, width int) []string {
	if len(segs) == 0 {
		return segs
	}
	used := ansi.StringWidth(prefix) + ansi.StringWidth(segs[0])
	sepWidth := ansi.StringWidth(foldSep)
	n := 1
	for ; n < len(segs); n++ {
		used += sepWidth + ansi.StringWidth(segs[n])
		if used > width {
			break
		}
	}
	return segs[:n]
}

// plural renders "1 file" or "3 files".
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// formatTurnCost renders a turn's spend as "$0.42", or "<$0.01" when it's
// less than a cent. It's empty when nothing was spent.
func formatTurnCost(cost float64) string {
	switch {
	case cost <= 0:
		return ""
	case cost < 0.005:
		return "<$0.01"
	default:
		return fmt.Sprintf("$%.2f", cost)
	}
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
