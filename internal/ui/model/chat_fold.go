package model

import (
	"time"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/chat"
	"github.com/charmbracelet/crush/internal/ui/list"
)

// foldEnabled reports whether finished turns fold their tool calls into a
// "Worked for …" header.
func (m *Chat) foldEnabled() bool {
	if m.com == nil || m.com.Styles == nil {
		return false
	}
	if m.com.Workspace == nil {
		return true
	}
	cfg := m.com.Config()
	if cfg == nil || cfg.Options == nil {
		return true
	}
	return cfg.Options.TUI.FoldToolsEnabled()
}

// withFolds returns msgs with a fold header after each prompt that does
// not have one yet. The prompt echo shown before the stored message
// arrives gets none; the stored message that replaces it does.
func (m *Chat) withFolds(msgs []chat.MessageItem) []chat.MessageItem {
	if !m.foldEnabled() {
		return msgs
	}
	out := make([]chat.MessageItem, 0, len(msgs)+1)
	for i, msg := range msgs {
		out = append(out, msg)
		user, ok := msg.(*chat.UserMessageItem)
		if !ok || user.ID() == chat.PendingUserID {
			continue
		}
		foldID := chat.TurnFoldID(user.ID())
		if _, exists := m.idInxMap[foldID]; exists {
			continue
		}
		if i+1 < len(msgs) && msgs[i+1].ID() == foldID {
			continue
		}
		out = append(out, chat.NewTurnFoldItem(m.com.Styles, user.ID(), time.Unix(user.Message().CreatedAt, 0)))
	}
	return out
}

// Refold re-applies folding to the last turn. Call it after changing an
// item of the last turn in place, such as a tool result arriving.
func (m *Chat) Refold() {
	m.refoldFrom(m.list.Len())
}

// refoldFrom re-applies folding to every turn from the one holding index
// start to the end of the chat.
func (m *Chat) refoldFrom(start int) {
	n := m.list.Len()
	if n == 0 {
		return
	}
	f := -1
	for i := min(start, n-1); i >= 0; i-- {
		if _, ok := m.list.ItemAt(i).(*chat.TurnFoldItem); ok {
			f = i
			break
		}
	}
	if f < 0 {
		// Nothing before the first header belongs to a turn, so nothing
		// there stays folded (its prompt may just have been removed).
		f = m.nextFold(0)
		stop := f
		if stop < 0 {
			stop = n
		}
		for i := range stop {
			m.list.SetHidden(m.list.ItemAt(i), false)
		}
	}
	for f >= 0 && f < n {
		end := m.nextFold(f + 1)
		if end < 0 {
			end = n
		}
		m.applyFold(f, end, end < n)
		f = end
	}
}

// nextFold returns the index of the first fold header at or after from,
// or -1.
func (m *Chat) nextFold(from int) int {
	for i := from; i < m.list.Len(); i++ {
		if _, ok := m.list.ItemAt(i).(*chat.TurnFoldItem); ok {
			return i
		}
	}
	return -1
}

// applyFold updates the fold header at index f, whose turn runs up to end,
// and shows or hides the turn's items to match it. later is true when a
// newer turn follows, which means this one is over.
func (m *Chat) applyFold(f, end int, later bool) {
	fold, ok := m.list.ItemAt(f).(*chat.TurnFoldItem)
	if !ok {
		return
	}

	var (
		final   *chat.AssistantMessageItem
		pending bool
		running bool
		counts  = map[string]int{}
		total   int
	)
	for i := f + 1; i < end; i++ {
		switch item := m.list.ItemAt(i).(type) {
		case *chat.AssistantMessageItem:
			final = item
		case *chat.PendingAssistantItem:
			pending = true
		case chat.ToolMessageItem:
			switch chat.EffectiveToolStatus(item) {
			case chat.ToolStatusRunning, chat.ToolStatusAwaitingPermission:
				running = true
			}
			if _, isQuestion := item.(*chat.QuestionToolMessageItem); !isQuestion {
				counts[item.ToolCall().Name]++
				total++
			}
		}
	}

	live := !later
	var duration time.Duration
	if final != nil {
		msg := final.Message()
		if !later && !pending && !running && msg.IsFinished() && msg.FinishReason() != message.FinishReasonToolUse {
			live = false
		}
		if fp := msg.FinishPart(); fp != nil && fp.Time > 0 && !live {
			duration = time.Unix(fp.Time, 0).Sub(fold.Start())
		}
	}

	kinds := make([]chat.ToolCount, 0, len(counts))
	for name, c := range counts {
		kinds = append(kinds, chat.ToolCount{Name: name, Count: c})
	}
	chat.SortToolCounts(kinds)
	fold.SetSummary(chat.FoldSummary{Tools: total, Kinds: kinds, Live: live, Duration: max(duration, 0)})
	if !fold.UserSet() {
		fold.SetExpanded(live)
	}

	m.list.SetHidden(fold, total == 0)
	collapse := total > 0 && !fold.Expanded()
	finalInfo := ""
	if final != nil {
		finalInfo = chat.AssistantInfoID(final.ID())
	}
	for i := f + 1; i < end; i++ {
		item := m.list.ItemAt(i)
		m.list.SetHidden(item, collapse && foldHides(item, final, finalInfo))
	}
}

// foldHides reports whether a collapsed turn hides item. It keeps the
// final answer and its footer, errors, cancellations, questions and
// pending approvals in view, and hides the rest of the tool calls and
// intermediate notes.
func foldHides(item list.Item, final *chat.AssistantMessageItem, finalInfo string) bool {
	switch item := item.(type) {
	case *chat.AssistantMessageItem:
		if item == final {
			return false
		}
		msg := item.Message()
		return !msg.IsErrorLike() && msg.FinishReason() != message.FinishReasonCanceled
	case *chat.AssistantInfoItem:
		return item.ID() != finalInfo
	case *chat.QuestionToolMessageItem:
		return false
	case chat.ToolMessageItem:
		return chat.EffectiveToolStatus(item) != chat.ToolStatusAwaitingPermission
	}
	return false
}
