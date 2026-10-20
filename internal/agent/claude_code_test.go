package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/claudecode"
	"github.com/charmbracelet/crush/internal/claudecode/fakecli"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

// ccFakePerms answers permission prompts without a UI.
type ccFakePerms struct {
	permission.Service
	allow bool

	mu   sync.Mutex
	reqs []permission.CreatePermissionRequest
}

func (f *ccFakePerms) Request(_ context.Context, opts permission.CreatePermissionRequest) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, opts)
	return f.allow, nil
}

type ccTestEnv struct {
	agent    *sessionAgent
	sessions session.Service
	messages message.Service
	perms    *ccFakePerms
	dir      string
}

func newClaudeCodeTestAgent(t *testing.T, allow, planMode bool, steps ...[]string) ccTestEnv {
	t.Helper()
	bin, dir := fakecli.Write(t, steps...)
	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	q := db.New(conn)
	sessions := session.NewService(q, conn)
	messages := message.NewService(q)

	m := Model{Model: claudeCodeModel{id: "default"}, ClaudeCode: true, ClaudeBinary: bin}
	m.CatwalkCfg.ContextWindow = 200000
	m.CatwalkCfg.DefaultMaxTokens = 32000
	m.ModelCfg.Provider = claudecode.ProviderID
	m.ModelCfg.Model = "default"
	perms := &ccFakePerms{allow: allow}
	sa := NewSessionAgent(SessionAgentOptions{
		LargeModel:   m,
		SmallModel:   m,
		SystemPrompt: "system",
		Sessions:     sessions,
		Messages:     messages,
		Permissions:  perms,
		PlanMode:     planMode,
		WorkDir:      dir,
	}).(*sessionAgent)
	return ccTestEnv{agent: sa, sessions: sessions, messages: messages, perms: perms, dir: dir}
}

func ccLine(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

func ccStream(t *testing.T, ev map[string]any) string {
	return ccLine(t, map[string]any{"type": "stream_event", "session_id": "s", "event": ev})
}

// ccTextMessage streams one assistant message with text and optional
// tool uses (name, id, input JSON).
func ccTextMessage(t *testing.T, id, text, stop string, toolUses ...[3]string) []string {
	out := []string{
		ccStream(t, map[string]any{"type": "message_start", "message": map[string]any{"id": id, "usage": map[string]any{"input_tokens": 100, "output_tokens": 1}}}),
		ccStream(t, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}}),
		ccStream(t, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": text}}),
		ccStream(t, map[string]any{"type": "content_block_stop", "index": 0}),
	}
	for i, tu := range toolUses {
		idx := i + 1
		input := tu[2]
		half := len(input) / 2
		out = append(out,
			ccStream(t, map[string]any{"type": "content_block_start", "index": idx, "content_block": map[string]any{"type": "tool_use", "id": tu[1], "name": tu[0], "input": map[string]any{}}}),
			ccStream(t, map[string]any{"type": "content_block_delta", "index": idx, "delta": map[string]any{"type": "input_json_delta", "partial_json": input[:half]}}),
			ccStream(t, map[string]any{"type": "content_block_delta", "index": idx, "delta": map[string]any{"type": "input_json_delta", "partial_json": input[half:]}}),
			ccStream(t, map[string]any{"type": "content_block_stop", "index": idx}),
		)
	}
	out = append(out,
		ccStream(t, map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop}, "usage": map[string]any{"output_tokens": 40}}),
		ccStream(t, map[string]any{"type": "message_stop"}),
		// The full message repeats what was streamed and must be ignored.
		ccLine(t, map[string]any{"type": "assistant", "session_id": "s", "message": map[string]any{"id": id, "role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}}}}),
	)
	return out
}

func ccPermRequest(t *testing.T, reqID, tool, toolUseID, input string) string {
	return ccLine(t, map[string]any{
		"type":       "control_request",
		"request_id": reqID,
		"request":    map[string]any{"subtype": "can_use_tool", "tool_name": tool, "tool_use_id": toolUseID, "input": json.RawMessage(input)},
	})
}

func ccToolResult(t *testing.T, toolUseID, content string, isError bool, structured any) string {
	ev := map[string]any{
		"type":       "user",
		"session_id": "s",
		"message": map[string]any{"role": "user", "content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": toolUseID, "content": content, "is_error": isError,
		}}},
	}
	if structured != nil {
		ev["tool_use_result"] = structured
	}
	return ccLine(t, ev)
}

func ccResult(t *testing.T, result string, isError bool) string {
	return ccLine(t, map[string]any{"type": "result", "subtype": "success", "session_id": "s", "result": result, "is_error": isError, "total_cost_usd": 0.25})
}

func ccMessages(t *testing.T, env ccTestEnv, sessionID string) []message.Message {
	t.Helper()
	msgs, err := env.messages.List(t.Context(), sessionID)
	require.NoError(t, err)
	return msgs
}

func TestClaudeCodeRunStreamsToolCallsAndPermissions(t *testing.T) {
	t.Parallel()
	init := ccLine(t, map[string]any{"type": "system", "subtype": "init", "session_id": "s", "model": "claude-fake"})
	step1 := append([]string{init}, ccTextMessage(t, "m1", "Let me look.", "tool_use", [3]string{"Bash", "tu-1", `{"command":"ls","description":"List files"}`})...)
	step1 = append(step1, ccPermRequest(t, "perm-1", "Bash", "tu-1", `{"command":"ls","description":"List files"}`))
	step2 := []string{ccToolResult(t, "tu-1", "file.txt", false, map[string]any{"stdout": "file.txt", "stderr": "", "interrupted": false})}
	step2 = append(step2, ccTextMessage(t, "m2", "All done.", "end_turn")...)
	step2 = append(step2, ccResult(t, "All done.", false))

	env := newClaudeCodeTestAgent(t, true, false, step1, step2)
	sess, err := env.sessions.Create(t.Context(), "cc")
	require.NoError(t, err)

	res, err := env.agent.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "what is here?"})
	require.NoError(t, err)
	require.Equal(t, "All done.", res.Response.Content.Text())

	// The CLI ran in the user's own permission mode with a stable session id.
	args := fakecli.Args(t, env.dir)
	require.Contains(t, args, "--session-id "+claudecode.SessionUUID(sess.ID))
	require.NotContains(t, args, "--permission-mode")
	require.NotContains(t, args, "--model")
	require.Equal(t, "what is here?", fakecli.UserText(t, env.dir), "a new session has no earlier history to send")

	// The permission prompt went through Ta-Da's permission service.
	require.Len(t, env.perms.reqs, 1)
	require.Equal(t, tools.BashToolName, env.perms.reqs[0].ToolName)
	require.Equal(t, "tu-1", env.perms.reqs[0].ToolCallID)
	require.Equal(t, sess.ID, env.perms.reqs[0].SessionID)
	in := fakecli.Input(t, env.dir)
	resp := in[len(in)-1]["response"].(map[string]any)["response"].(map[string]any)
	require.Equal(t, "allow", resp["behavior"])

	msgs := ccMessages(t, env, sess.ID)
	require.Len(t, msgs, 4)
	require.Equal(t, message.User, msgs[0].Role)

	require.Equal(t, message.Assistant, msgs[1].Role)
	require.Equal(t, "Let me look.", msgs[1].Content().String())
	calls := msgs[1].ToolCalls()
	require.Len(t, calls, 1)
	require.Equal(t, tools.BashToolName, calls[0].Name)
	require.True(t, calls[0].Finished)
	require.JSONEq(t, `{"command":"ls","description":"List files"}`, calls[0].Input)
	require.Equal(t, message.FinishReasonToolUse, msgs[1].FinishReason())
	require.Equal(t, claudecode.ProviderID, msgs[1].Provider)

	require.Equal(t, message.Tool, msgs[2].Role)
	results := msgs[2].ToolResults()
	require.Len(t, results, 1)
	require.Equal(t, "tu-1", results[0].ToolCallID)
	require.Equal(t, tools.BashToolName, results[0].Name)
	require.Equal(t, "file.txt", results[0].Content)
	require.False(t, results[0].IsError)

	require.Equal(t, "All done.", msgs[3].Content().String())
	require.Equal(t, message.FinishReasonEndTurn, msgs[3].FinishReason())

	// Usage is tracked; cost is not, because the subscription pays.
	s, err := env.sessions.Get(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Positive(t, s.PromptTokens+s.CompletionTokens)
	require.Zero(t, s.Cost)
	require.Equal(t, "what is here?", s.Title)
}

func TestClaudeCodeRunDeniedPermissionEndsTurn(t *testing.T) {
	t.Parallel()
	step1 := ccTextMessage(t, "m1", "I'll delete it.", "tool_use", [3]string{"Bash", "tu-1", `{"command":"rm -rf build"}`})
	step1 = append(step1, ccPermRequest(t, "perm-1", "Bash", "tu-1", `{"command":"rm -rf build"}`))
	step2 := []string{
		ccToolResult(t, "tu-1", "The user denied permission for this tool call.", true, nil),
		ccLine(t, map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "session_id": "s"}),
	}
	env := newClaudeCodeTestAgent(t, false, false, step1, step2)
	sess, err := env.sessions.Create(t.Context(), "cc")
	require.NoError(t, err)

	_, err = env.agent.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "clean up"})
	require.NoError(t, err, "a denied tool call is not an error")

	in := fakecli.Input(t, env.dir)
	resp := in[len(in)-1]["response"].(map[string]any)["response"].(map[string]any)
	require.Equal(t, "deny", resp["behavior"])
	require.Equal(t, true, resp["interrupt"])

	msgs := ccMessages(t, env, sess.ID)
	require.Len(t, msgs, 3)
	require.Equal(t, message.FinishReasonEndTurn, msgs[1].FinishReason())
	require.True(t, msgs[2].ToolResults()[0].IsError)
}

func TestClaudeCodeRunErrorResultFails(t *testing.T) {
	t.Parallel()
	env := newClaudeCodeTestAgent(t, true, false, []string{
		ccLine(t, map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "errors": []string{"Claude Code is not logged in"}}),
	})
	sess, err := env.sessions.Create(t.Context(), "cc")
	require.NoError(t, err)
	_, err = env.agent.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "hi"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not logged in")
}

func TestClaudeCodeRunTodoWriteUpdatesSessionTodos(t *testing.T) {
	t.Parallel()
	todos := `{"todos":[{"content":"Write tests","status":"in_progress","activeForm":"Writing tests"},{"content":"Ship","status":"pending","activeForm":"Shipping"}]}`
	step := ccTextMessage(t, "m1", "Planning.", "tool_use", [3]string{"TodoWrite", "tu-1", todos})
	step = append(step, ccToolResult(t, "tu-1", "Todos have been modified successfully.", false, nil))
	step = append(step, ccTextMessage(t, "m2", "Started.", "end_turn")...)
	step = append(step, ccResult(t, "Started.", false))
	env := newClaudeCodeTestAgent(t, true, false, step)
	sess, err := env.sessions.Create(t.Context(), "cc")
	require.NoError(t, err)

	_, err = env.agent.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "go"})
	require.NoError(t, err)
	require.Empty(t, env.perms.reqs, "TodoWrite needs no permission")

	s, err := env.sessions.Get(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Equal(t, []session.Todo{
		{Content: "Write tests", Status: session.TodoStatusInProgress, ActiveForm: "Writing tests"},
		{Content: "Ship", Status: session.TodoStatusPending, ActiveForm: "Shipping"},
	}, s.Todos)

	msgs := ccMessages(t, env, sess.ID)
	call := msgs[1].ToolCalls()[0]
	require.Equal(t, tools.TodosToolName, call.Name)
	require.Contains(t, call.Input, `"active_form":"Writing tests"`)
	tr := msgs[2].ToolResults()[0]
	require.Equal(t, tools.TodosToolName, tr.Name)
	require.NotEmpty(t, tr.Metadata)
}

func TestClaudeCodePlanModeShowsPlan(t *testing.T) {
	t.Parallel()
	step := ccTextMessage(t, "m1", "Here is my plan.", "tool_use", [3]string{"ExitPlanMode", "tu-1", `{"plan":"1. Do it\n2. Test it"}`})
	step = append(step, ccPermRequest(t, "perm-1", "ExitPlanMode", "tu-1", `{"plan":"1. Do it\n2. Test it"}`))
	env := newClaudeCodeTestAgent(t, true, true, step, []string{
		ccToolResult(t, "tu-1", "Ta-Da has shown your plan to the user.", true, nil),
		ccResult(t, "", false),
	})
	sess, err := env.sessions.Create(t.Context(), "cc")
	require.NoError(t, err)

	_, err = env.agent.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "plan it"})
	require.NoError(t, err)
	require.Contains(t, fakecli.Args(t, env.dir), "--permission-mode plan")
	require.Empty(t, env.perms.reqs, "ExitPlanMode is answered without asking the user")

	msgs := ccMessages(t, env, sess.ID)
	require.Len(t, msgs, 2, "the hidden tool gets no card and no result message")
	require.Equal(t, "Here is my plan.\n\n1. Do it\n2. Test it", msgs[1].Content().String())
	require.Empty(t, msgs[1].ToolCalls())
	require.Equal(t, message.FinishReasonEndTurn, msgs[1].FinishReason())
}

func TestClaudeCodeSecondTurnResumes(t *testing.T) {
	t.Parallel()
	turn := func() []string {
		s := ccTextMessage(t, "m1", "Hi.", "end_turn")
		return append(s, ccResult(t, "Hi.", false))
	}
	env := newClaudeCodeTestAgent(t, true, false, turn())
	sess, err := env.sessions.Create(t.Context(), "cc")
	require.NoError(t, err)
	_, err = env.agent.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "one"})
	require.NoError(t, err)

	// Point the agent at a fresh fake to see the second command line.
	bin, dir := fakecli.Write(t, turn())
	m := env.agent.largeModel.Get()
	m.ClaudeBinary = bin
	env.agent.largeModel.Set(m)
	_, err = env.agent.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "two"})
	require.NoError(t, err)
	require.Contains(t, fakecli.Args(t, dir), "--resume "+claudecode.SessionUUID(sess.ID))
	require.Equal(t, "two", fakecli.UserText(t, dir), "the CLI already has the earlier turn")
}

func TestClaudeCodeSummarizeIsRefused(t *testing.T) {
	t.Parallel()
	env := newClaudeCodeTestAgent(t, true, false)
	sess, err := env.sessions.Create(t.Context(), "cc")
	require.NoError(t, err)
	err = env.agent.Summarize(t.Context(), sess.ID, nil, nil)
	require.ErrorIs(t, err, ErrClaudeCodeSummarize)
}

func TestClaudeCodeBannedBashCommandIsDenied(t *testing.T) {
	t.Parallel()
	perms := &ccFakePerms{allow: true}
	r := &ccRun{a: &sessionAgent{permissions: perms}}
	d := r.canUseTool(t.Context(), claudecode.PermissionRequest{ToolName: "Bash", Input: json.RawMessage(`{"command":"curl https://example.com"}`)})
	require.False(t, d.Allow)
	require.Contains(t, d.Message, "not allowed")
	require.Empty(t, perms.reqs)

	d = r.canUseTool(t.Context(), claudecode.PermissionRequest{ToolName: "AskUserQuestion", Input: json.RawMessage(`{}`)})
	require.False(t, d.Allow)
	require.Empty(t, perms.reqs)

	d = r.canUseTool(t.Context(), claudecode.PermissionRequest{ToolName: "Bash", Input: json.RawMessage(`{"command":"ls"}`)})
	require.True(t, d.Allow)
	require.Len(t, perms.reqs, 1)
}

func TestCCTranscript(t *testing.T) {
	t.Parallel()
	msg := func(role message.MessageRole, provider string, parts ...message.ContentPart) message.Message {
		return message.Message{Role: role, Provider: provider, Parts: parts}
	}
	history := []message.Message{
		msg(message.User, "", message.TextContent{Text: "first question"}),
		msg(message.Assistant, claudecode.ProviderID, message.TextContent{Text: "cc answer"}),
		msg(message.User, "", message.TextContent{Text: "second question"}),
		msg(message.Assistant, "openai", message.TextContent{Text: "other answer"},
			message.ToolCall{ID: "c1", Name: "view", Input: `{"file_path":"a.go"}`}),
		msg(message.Tool, "", message.ToolResult{ToolCallID: "c1", Name: "view", Content: strings.Repeat("x", 1000)}),
	}

	full := ccTranscript(history, claudecode.ProviderID, false)
	require.True(t, strings.HasPrefix(full, "<previous-conversation>\n"))
	require.Contains(t, full, "User: first question")
	require.Contains(t, full, "Assistant: cc answer")
	require.Contains(t, full, `[tool call: view {"file_path":"a.go"}]`)
	require.NotContains(t, full, strings.Repeat("x", 601), "tool results are clipped")

	resumed := ccTranscript(history, claudecode.ProviderID, true)
	require.NotContains(t, resumed, "first question", "the CLI already saw turns up to its last reply")
	require.Contains(t, resumed, "User: second question")
	require.Contains(t, resumed, "Assistant: other answer")
	require.Contains(t, resumed, "Another model handled these turns")

	require.Empty(t, ccTranscript(history[:2], claudecode.ProviderID, true))
	require.Empty(t, ccTranscript(nil, claudecode.ProviderID, false))

	long := []message.Message{msg(message.User, "", message.TextContent{Text: strings.Repeat("é", ccTranscriptLimit)})}
	cut := ccTranscript(long, claudecode.ProviderID, false)
	require.Contains(t, cut, "[earlier messages cut]")
	require.True(t, strings.Contains(cut, "é"))
}

func TestCCToolNameAndInput(t *testing.T) {
	t.Parallel()
	require.Equal(t, tools.BashToolName, ccToolName("Bash"))
	require.Equal(t, tools.ViewToolName, ccToolName("Read"))
	require.Equal(t, AgentToolName, ccToolName("Task"))
	require.Equal(t, "mcp_github_create_issue", ccToolName("mcp__github__create_issue"))
	require.Equal(t, "SomethingNew", ccToolName("SomethingNew"))

	require.JSONEq(t, `{"file_path":"a.go","offset":9,"limit":5}`, ccToolInput("Read", json.RawMessage(`{"file_path":"a.go","offset":10,"limit":5}`)))
	require.JSONEq(t, `{"pattern":"x","glob":"*.go","include":"*.go"}`, ccToolInput("Grep", json.RawMessage(`{"pattern":"x","glob":"*.go"}`)))
	require.JSONEq(t, `{"url":"https://a","format":"markdown"}`, ccToolInput("WebFetch", json.RawMessage(`{"url":"https://a"}`)))
	require.Equal(t, "{}", ccToolInput("Bash", nil))
	require.Equal(t, "not json", ccToolInput("Bash", json.RawMessage("not json")))
}

func TestClaudeCodePassesEffortAndKeepsModelList(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("TADA_GLOBAL_DATA", dataDir)
	env := newClaudeCodeTestAgent(t, true, false, []string{
		ccLine(t, map[string]any{"type": "control_response", "response": map[string]any{
			"subtype": "success", "request_id": "tada-1",
			"response": map[string]any{"models": []map[string]any{
				{"value": "default", "displayName": "Default", "supportsEffort": true, "supportedEffortLevels": []string{"low", "high"}},
			}},
		}}),
		ccLine(t, map[string]any{"type": "result", "subtype": "success", "result": "ok"}),
	})
	m := env.agent.largeModel.Get()
	m.CatwalkCfg.ReasoningLevels = []string{"default", "low", "high"}
	m.ModelCfg.ReasoningEffort = "high"
	env.agent.largeModel.Set(m)
	sess, err := env.sessions.Create(t.Context(), "cc")
	require.NoError(t, err)
	_, err = env.agent.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "hi"})
	require.NoError(t, err)
	require.Contains(t, fakecli.Args(t, env.dir), "--effort high")

	saved := claudecode.LoadModels(config.ClaudeCodeModelsPath())
	require.Len(t, saved, 1)
	require.Equal(t, "default", saved[0].Value)
	require.Equal(t, []string{"low", "high"}, saved[0].Efforts())
}
