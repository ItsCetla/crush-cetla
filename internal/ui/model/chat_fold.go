package model

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/crush/internal/agent/tools"
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
		stats   turnStats
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
			stats.add(item)
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
	fold.SetSummary(chat.FoldSummary{
		Tools:     total,
		Kinds:     kinds,
		Live:      live,
		Duration:  max(duration, 0),
		Files:     len(stats.files),
		Additions: stats.additions,
		Removals:  stats.removals,
		Commands:  stats.commands,
		Tests:     stats.tests(),
	})
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

// turnStats gathers a turn's receipt from its tool calls' inputs and
// result metadata. It does no IO.
type turnStats struct {
	files     map[string]struct{}
	additions int
	removals  int
	commands  int
	// lastBash is the turn's most recent shell command so far.
	lastBash chat.ToolMessageItem
}

// lineCounts is the part of the edit, multiedit and write metadata the
// receipt reads.
type lineCounts struct {
	Additions int `json:"additions"`
	Removals  int `json:"removals"`
}

// add counts one tool call.
func (s *turnStats) add(item chat.ToolMessageItem) {
	call := item.ToolCall()
	switch call.Name {
	case tools.BashToolName:
		s.commands++
		s.lastBash = item
	case tools.EditToolName, tools.MultiEditToolName, tools.WriteToolName:
		res := item.Result()
		if res == nil || res.IsError || res.Metadata == "" {
			return
		}
		var meta lineCounts
		if err := json.Unmarshal([]byte(res.Metadata), &meta); err != nil {
			return
		}
		if meta.Additions == 0 && meta.Removals == 0 {
			return
		}
		var params struct {
			FilePath string `json:"file_path"`
		}
		_ = json.Unmarshal([]byte(call.Input), &params)
		if s.files == nil {
			s.files = map[string]struct{}{}
		}
		s.files[filepath.Clean(params.FilePath)] = struct{}{}
		s.additions += meta.Additions
		s.removals += meta.Removals
	}
}

// tests reports how the turn's last shell command went when it was a
// test run that finished with a known exit status.
func (s *turnStats) tests() chat.TestOutcome {
	if s.lastBash == nil {
		return chat.TestsUnknown
	}
	res := s.lastBash.Result()
	if res == nil || res.Metadata == "" {
		return chat.TestsUnknown
	}
	var params tools.BashParams
	if err := json.Unmarshal([]byte(s.lastBash.ToolCall().Input), &params); err != nil {
		return chat.TestsUnknown
	}
	if !looksLikeTestCommand(params.Command) {
		return chat.TestsUnknown
	}
	var meta tools.BashResponseMetadata
	if err := json.Unmarshal([]byte(res.Metadata), &meta); err != nil || meta.ExitCode == nil {
		return chat.TestsUnknown
	}
	if *meta.ExitCode == 0 {
		return chat.TestsPassed
	}
	return chat.TestsFailed
}

// testRunners are commands whose "test" subcommand runs a test suite, as
// in "go test" or "npm run test".
var testRunners = map[string]bool{
	"go": true, "npm": true, "yarn": true, "pnpm": true, "bun": true,
	"deno": true, "cargo": true, "make": true, "task": true, "just": true,
	"mvn": true, "gradle": true, "gradlew": true, "mix": true,
	"dotnet": true, "swift": true, "zig": true, "flutter": true,
	"dart": true, "poetry": true, "uv": true, "hatch": true,
}

// testTools are commands that run tests on their own.
var testTools = map[string]bool{
	"pytest": true, "py.test": true, "jest": true, "vitest": true,
	"mocha": true, "rspec": true, "phpunit": true, "ctest": true,
	"tox": true, "nox": true, "gotestsum": true, "ginkgo": true,
	"nextest": true, "playwright": true, "cypress": true,
}

// looksLikeTestCommand reports whether a shell command runs a test
// suite: "go test ./...", "npm test", "cd web && pnpm run test",
// "python -m pytest -q" and the like.
func looksLikeTestCommand(command string) bool {
	fields := strings.FieldsFunc(command, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', ';', '&', '|', '(', ')':
			return true
		}
		return false
	})
	for i, f := range fields {
		name := filepath.Base(f)
		if testTools[name] {
			return true
		}
		if !testRunners[name] {
			continue
		}
		// Allow "npm run test", "yarn run test:unit", "uv run pytest".
		for j := i + 1; j < len(fields) && j <= i+2; j++ {
			next := fields[j]
			if next == "run" || strings.HasPrefix(next, "-") {
				continue
			}
			if next == "test" || strings.HasPrefix(next, "test:") || testTools[next] {
				return true
			}
			break
		}
	}
	return false
}

// SetTurnCostBase records the session's spend at the start of the turn
// begun by the given user message, so the turn's receipt can show what it
// cost. It's a no-op when the turn has no fold header.
func (m *Chat) SetTurnCostBase(userMessageID string, sessionCost float64) {
	if fold, ok := m.MessageItem(chat.TurnFoldID(userMessageID)).(*chat.TurnFoldItem); ok {
		fold.SetCostBase(sessionCost)
	}
}

// UpdateTurnCost charges the session's new running spend to the latest
// turn. It's a no-op when that turn's starting spend isn't known.
func (m *Chat) UpdateTurnCost(sessionCost float64) {
	for i := m.list.Len() - 1; i >= 0; i-- {
		fold, ok := m.list.ItemAt(i).(*chat.TurnFoldItem)
		if !ok {
			continue
		}
		fold.SetSessionCost(sessionCost)
		return
	}
}
