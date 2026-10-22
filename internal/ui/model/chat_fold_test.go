package model

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/chat"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

const foldTurnStart = 1_000_000

func foldUser(id string, at int64) message.Message {
	return message.Message{
		ID:        id,
		Role:      message.User,
		SessionID: "s1",
		CreatedAt: at,
		Parts:     []message.ContentPart{message.TextContent{Text: "do the thing"}},
	}
}

func foldAssistant(id, text string, reason message.FinishReason, at int64, calls ...message.ToolCall) message.Message {
	parts := []message.ContentPart{}
	if text != "" {
		parts = append(parts, message.TextContent{Text: text})
	}
	for _, c := range calls {
		parts = append(parts, c)
	}
	if reason != "" {
		parts = append(parts, message.Finish{Reason: reason, Time: at})
	}
	return message.Message{ID: id, Role: message.Assistant, SessionID: "s1", Parts: parts}
}

func foldCall(id, name string) message.ToolCall {
	return message.ToolCall{ID: id, Name: name, Input: "{}", Finished: true}
}

func foldResults(id string, callIDs ...string) message.Message {
	parts := make([]message.ContentPart, 0, len(callIDs))
	for _, c := range callIDs {
		parts = append(parts, message.ToolResult{ToolCallID: c, Content: "ok"})
	}
	return message.Message{ID: id, Role: message.Tool, SessionID: "s1", Parts: parts}
}

// finishedTurn is a prompt answered after three tool calls over 72s, with
// narration before the tools and a tool-only step in between.
func finishedTurn() []message.Message {
	return []message.Message{
		foldUser("u1", foldTurnStart),
		foldAssistant("a1", "Let me look around.", message.FinishReasonToolUse, foldTurnStart+10,
			foldCall("t1", "view"), foldCall("t2", "bash")),
		foldResults("r1", "t1", "t2"),
		foldAssistant("a2", "", message.FinishReasonToolUse, foldTurnStart+40, foldCall("t3", "bash")),
		foldResults("r2", "t3"),
		foldAssistant("a3", "All done.", message.FinishReasonEndTurn, foldTurnStart+72),
	}
}

// foldWorkspace hands the chat a config, which the answer footer needs to
// render.
type foldWorkspace struct {
	*countingWorkspace
	cfg *config.Config
}

func (w foldWorkspace) Config() *config.Config { return w.cfg }

func newFoldUI(t *testing.T) *UI {
	t.Helper()
	return newFoldUIWith(t, &config.Config{})
}

func foldConfig(cfg *config.Config) *config.Config {
	if cfg.Providers == nil {
		cfg.Providers = csync.NewMap[string, config.ProviderConfig]()
	}
	return cfg
}

func newFoldUIWith(t *testing.T, cfg *config.Config) *UI {
	t.Helper()
	m := newBusyUI(&countingWorkspace{ready: true})
	m.com.Workspace = foldWorkspace{countingWorkspace: &countingWorkspace{ready: true}, cfg: foldConfig(cfg)}
	m.chat.SetSize(100, 40)
	return m
}

// shownIDs lists the ids of the chat items that are not folded away.
func shownIDs(m *UI) []string {
	var ids []string
	for i := range m.chat.list.Len() {
		if m.chat.list.IsHidden(i) {
			continue
		}
		if item, ok := m.chat.list.ItemAt(i).(chat.MessageItem); ok {
			ids = append(ids, item.ID())
		}
	}
	return ids
}

func foldOf(t *testing.T, m *UI, userID string) *chat.TurnFoldItem {
	t.Helper()
	fold, ok := m.chat.MessageItem(chat.TurnFoldID(userID)).(*chat.TurnFoldItem)
	require.True(t, ok, "turn %s has a fold header", userID)
	return fold
}

// A finished turn loaded from history folds its tools and narration into
// one header, keeping the prompt, the final answer and its footer.
func TestFoldCollapsesFinishedTurn(t *testing.T) {
	m := newFoldUI(t)
	m.setSessionMessages(finishedTurn())

	fold := foldOf(t, m, "u1")
	require.False(t, fold.Expanded())
	require.Equal(t, chat.FoldSummary{
		Tools:    3,
		Kinds:    []chat.ToolCount{{Name: "bash", Count: 2}, {Name: "view", Count: 1}},
		Duration: 72 * time.Second,
		Commands: 2,
	}, fold.Summary())
	require.Equal(t, []string{"u1", "u1:turn-fold", "a3", chat.AssistantInfoID("a3")}, shownIDs(m))

	line := fold.RawRender(100)
	require.Contains(t, line, "Worked for 1m 12s")
	require.Contains(t, line, "3 tool calls")
	require.Contains(t, line, "2 bash, 1 view")
}

// Toggling the header shows the whole turn, and toggling again folds it.
func TestFoldToggleShowsTurn(t *testing.T) {
	m := newFoldUI(t)
	m.setSessionMessages(finishedTurn())

	m.chat.SetSelected(m.chat.idInxMap[chat.TurnFoldID("u1")])
	m.chat.ToggleExpandedSelectedItem()
	require.True(t, foldOf(t, m, "u1").Expanded())
	require.Equal(t, []string{"u1", "u1:turn-fold", "a1", "t1", "t2", "t3", "a3", chat.AssistantInfoID("a3")}, shownIDs(m))

	m.chat.ToggleExpandedSelectedItem()
	require.Equal(t, []string{"u1", "u1:turn-fold", "a3", chat.AssistantInfoID("a3")}, shownIDs(m))
}

// A turn that is still running stays open so its tools can be watched,
// then folds once it ends.
func TestFoldLiveTurnStaysOpenUntilItEnds(t *testing.T) {
	m := newFoldUI(t)
	msgs := finishedTurn()[:3]
	m.setSessionMessages(msgs)

	fold := foldOf(t, m, "u1")
	require.True(t, fold.Summary().Live)
	require.True(t, fold.Expanded())
	require.Equal(t, []string{"u1", "u1:turn-fold", "a1", "t1", "t2"}, shownIDs(m))
	require.Contains(t, fold.RawRender(100), "Working")

	final := foldAssistant("a3", "All done.", message.FinishReasonEndTurn, foldTurnStart+20)
	m.appendSessionMessage(final)

	require.False(t, fold.Summary().Live)
	require.False(t, fold.Expanded())
	require.Equal(t, []string{"u1", "u1:turn-fold", "a3", chat.AssistantInfoID("a3")}, shownIDs(m))
}

// A fold the user opened stays open when the turn ends.
func TestFoldRespectsUserChoice(t *testing.T) {
	m := newFoldUI(t)
	m.setSessionMessages(finishedTurn()[:3])

	fold := foldOf(t, m, "u1")
	fold.ToggleExpanded() // closed by hand while live
	m.chat.Refold()
	require.False(t, fold.Expanded())

	fold.ToggleExpanded() // and opened again
	m.appendSessionMessage(foldAssistant("a3", "All done.", message.FinishReasonEndTurn, foldTurnStart+20))
	require.True(t, fold.Expanded(), "the turn ended but the user's choice wins")
}

// Errors and cancellations inside a folded turn stay in view.
func TestFoldKeepsErrorsVisible(t *testing.T) {
	m := newFoldUI(t)
	msgs := []message.Message{
		foldUser("u1", foldTurnStart),
		foldAssistant("a1", "Trying.", message.FinishReasonToolUse, foldTurnStart+5, foldCall("t1", "bash")),
		foldResults("r1", "t1"),
		foldAssistant("a2", "", message.FinishReasonError, foldTurnStart+8),
		foldAssistant("a3", "Recovered.", message.FinishReasonEndTurn, foldTurnStart+9),
	}
	m.setSessionMessages(msgs)

	require.Equal(t, []string{"u1", "u1:turn-fold", "a2", "a3", chat.AssistantInfoID("a3")}, shownIDs(m))
}

// Turns without tool calls get no header at all, and earlier turns are
// over once a newer prompt follows.
func TestFoldHeaderOnlyForTurnsWithTools(t *testing.T) {
	m := newFoldUI(t)
	msgs := append(finishedTurn(),
		foldUser("u2", foldTurnStart+100),
		foldAssistant("a4", "Sure.", message.FinishReasonEndTurn, foldTurnStart+102),
	)
	m.setSessionMessages(msgs)

	require.Equal(t, []string{
		"u1", "u1:turn-fold", "a3", chat.AssistantInfoID("a3"),
		"u2", "a4", chat.AssistantInfoID("a4"),
	}, shownIDs(m))
}

// Removing a prompt takes its fold header with it.
func TestFoldRemovedWithPrompt(t *testing.T) {
	m := newFoldUI(t)
	m.setSessionMessages(finishedTurn())

	m.chat.RemoveMessage("u1")
	require.Nil(t, m.chat.MessageItem(chat.TurnFoldID("u1")))
	require.Contains(t, shownIDs(m), "t1", "with no header, nothing stays folded")
}

// options.tui.fold_tools: false keeps every turn fully shown.
func TestFoldOptionOff(t *testing.T) {
	off := false
	m := newFoldUIWith(t, &config.Config{Options: &config.Options{TUI: &config.TUIOptions{FoldTools: &off}}})
	m.setSessionMessages(finishedTurn())

	require.Nil(t, m.chat.MessageItem(chat.TurnFoldID("u1")))
	require.Equal(t, []string{"u1", "a1", "t1", "t2", "t3", "a3", chat.AssistantInfoID("a3")}, shownIDs(m))
}

func TestLooksLikeTestCommand(t *testing.T) {
	t.Parallel()

	for cmd, want := range map[string]bool{
		"go test ./...":                    true,
		"go test -race ./internal/ui/...":  true,
		"cd web && npm test":               true,
		"npm run test -- --watch=false":    true,
		"yarn test:unit":                   true,
		"pnpm --filter app test":           false,
		"python -m pytest -q":              true,
		"uv run pytest tests/":             true,
		"cargo test --workspace":           true,
		"CGO_ENABLED=0 go test ./... 2>&1": true,
		"./node_modules/.bin/vitest run":   true,
		"make test":                        true,
		"bundle exec rspec":                true,
		"go build ./...":                   false,
		"go vet ./...":                     false,
		"git commit -m 'add test'":         false,
		"test -f go.mod && echo yes":       false,
		"ls internal/ui/chat/fold_test.go": false,
	} {
		require.Equal(t, want, looksLikeTestCommand(cmd), cmd)
	}
}

func receiptCall(id, name, input string) message.ToolCall {
	return message.ToolCall{ID: id, Name: name, Input: input, Finished: true}
}

func receiptResult(callID, metadata string, isError bool) message.ToolResult {
	return message.ToolResult{ToolCallID: callID, Content: "ok", Metadata: metadata, IsError: isError}
}

// The fold's receipt counts changed files and lines from edit, multiedit
// and write metadata, shell commands, and how a closing test run went.
func TestFoldReceiptFromToolMetadata(t *testing.T) {
	m := newFoldUI(t)
	m.setSessionMessages([]message.Message{
		foldUser("u1", foldTurnStart),
		foldAssistant("a1", "", message.FinishReasonToolUse, foldTurnStart+10,
			receiptCall("e1", "edit", `{"file_path":"/w/a.go"}`),
			receiptCall("e2", "multiedit", `{"file_path":"/w/a.go"}`),
			receiptCall("w1", "write", `{"file_path":"/w/b.go"}`),
			receiptCall("d1", "edit", `{"file_path":"/w/c.go"}`),
			receiptCall("b1", "bash", `{"command":"go build ./..."}`),
			receiptCall("b2", "bash", `{"command":"go test ./..."}`),
		),
		{ID: "r1", Role: message.Tool, SessionID: "s1", Parts: []message.ContentPart{
			receiptResult("e1", `{"additions":10,"removals":2,"old_content":"x","new_content":"y"}`, false),
			receiptResult("e2", `{"additions":5,"removals":1}`, false),
			receiptResult("w1", `{"diff":"...","additions":20,"removals":0}`, false),
			// Denied edits carry a diff but changed nothing.
			receiptResult("d1", `{"additions":7,"removals":7}`, true),
			receiptResult("b1", `{"output":"","exit_code":0}`, false),
			receiptResult("b2", `{"output":"FAIL","exit_code":1}`, false),
		}},
		foldAssistant("a2", "Tests fail.", message.FinishReasonEndTurn, foldTurnStart+134),
	})

	s := foldOf(t, m, "u1").Summary()
	require.Equal(t, 2, s.Files)
	require.Equal(t, 35, s.Additions)
	require.Equal(t, 3, s.Removals)
	require.Equal(t, 2, s.Commands)
	require.Equal(t, chat.TestsFailed, s.Tests)
	require.Contains(t, ansi.Strip(foldOf(t, m, "u1").RawRender(200)),
		"Worked for 2m 14s · 2 files changed (+35 −3) · 2 commands · tests failed")
}

// Without an exit status — older history, or a command still running in
// the background — the receipt says nothing about tests.
func TestFoldReceiptSkipsTestsWithoutExitCode(t *testing.T) {
	m := newFoldUI(t)
	m.setSessionMessages([]message.Message{
		foldUser("u1", foldTurnStart),
		foldAssistant("a1", "", message.FinishReasonToolUse, foldTurnStart+10,
			receiptCall("b1", "bash", `{"command":"go test ./..."}`)),
		{ID: "r1", Role: message.Tool, SessionID: "s1", Parts: []message.ContentPart{
			receiptResult("b1", `{"output":"ok"}`, false),
		}},
		foldAssistant("a2", "Done.", message.FinishReasonEndTurn, foldTurnStart+20),
	})
	s := foldOf(t, m, "u1").Summary()
	require.Equal(t, 1, s.Commands)
	require.Equal(t, chat.TestsUnknown, s.Tests)
}

// A turn's cost is what the session spent after its prompt arrived.
func TestFoldTurnCost(t *testing.T) {
	m := newFoldUI(t)
	m.setSessionMessages(finishedTurn())
	m.chat.SetTurnCostBase("u1", 1.25)
	m.chat.UpdateTurnCost(1.67)
	require.InDelta(t, 0.42, foldOf(t, m, "u1").Cost(), 1e-9)
	require.Contains(t, ansi.Strip(foldOf(t, m, "u1").RawRender(200)), "$0.42")
}

// options.tui.diff_mode reaches the tool items that draw diffs, both when
// a session loads and when items arrive later.
func TestChatAppliesDiffMode(t *testing.T) {
	m := newFoldUIWith(t, &config.Config{Options: &config.Options{TUI: &config.TUIOptions{DiffMode: config.DiffModeSplit}}})

	edit := func(id string) chat.ToolMessageItem {
		input := `{"file_path":"/tmp/main.go","old_string":"oldValue","new_string":"newValue"}`
		meta := `{"additions":1,"removals":1,"old_content":"var x = oldValue\n","new_content":"var x = newValue\n"}`
		return chat.NewEditToolMessageItem(m.com.Styles,
			message.ToolCall{ID: id, Name: "edit", Input: input, Finished: true},
			&message.ToolResult{ToolCallID: id, Name: "edit", Content: "ok", Metadata: meta}, false)
	}
	sideBySide := func(item chat.ToolMessageItem) bool {
		for _, line := range strings.Split(ansi.Strip(item.RawRender(100)), "\n") {
			if strings.Contains(line, "oldValue") && strings.Contains(line, "newValue") {
				return true
			}
		}
		return false
	}

	first, second := edit("e1"), edit("e2")
	m.chat.SetMessages(first)
	m.chat.AppendMessages(second)
	require.True(t, sideBySide(first), "split mode draws a narrow diff side by side")
	require.True(t, sideBySide(second))
}
