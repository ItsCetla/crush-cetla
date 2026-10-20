package claudecode_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/claudecode"
	"github.com/charmbracelet/crush/internal/claudecode/fakecli"
	"github.com/stretchr/testify/require"
)

func TestBuildArgs(t *testing.T) {
	t.Parallel()
	base := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--verbose",
		"--permission-prompt-tool", "stdio",
	}
	require.Equal(t, base, claudecode.BuildArgs(claudecode.TurnOptions{Model: "default"}))
	require.Equal(t,
		append(append([]string{}, base...), "--permission-mode", "plan", "--model", "opus", "--session-id", "abc"),
		claudecode.BuildArgs(claudecode.TurnOptions{PermissionMode: "plan", Model: "opus", SessionID: "abc"}),
	)
	require.Equal(t,
		append(append([]string{}, base...), "--resume", "abc", "--x"),
		claudecode.BuildArgs(claudecode.TurnOptions{SessionID: "abc", Resume: true, ExtraArgs: []string{"--x"}}),
	)
}

func TestChildEnv(t *testing.T) {
	t.Parallel()
	env := []string{"PATH=/bin", "CLAUDECODE=1", "CLAUDE_CODE_ENTRYPOINT=cli", "CLAUDE_CODE_SSE_PORT=1", "ANTHROPIC_API_KEY=k", "ANTHROPIC_AUTH_TOKEN=t", "HOME=/h"}
	require.Equal(t, []string{"PATH=/bin", "HOME=/h"}, claudecode.ChildEnv(env, false))
	require.Equal(t, []string{"PATH=/bin", "ANTHROPIC_API_KEY=k", "ANTHROPIC_AUTH_TOKEN=t", "HOME=/h"}, claudecode.ChildEnv(env, true))
}

func TestSessionUUIDStable(t *testing.T) {
	t.Parallel()
	a := claudecode.SessionUUID("session-1")
	require.Equal(t, a, claudecode.SessionUUID("session-1"))
	require.NotEqual(t, a, claudecode.SessionUUID("session-2"))
	require.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, a)
}

func TestSessionExists(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	id := claudecode.SessionUUID("x")
	require.False(t, claudecode.SessionExists(id))
	proj := filepath.Join(dir, "projects", "-some-project")
	require.NoError(t, os.MkdirAll(proj, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(proj, id+".jsonl"), []byte("{}\n"), 0o644))
	require.True(t, claudecode.SessionExists(id))
}

func TestRunTurnStreamsAndAnswersPermission(t *testing.T) {
	t.Parallel()
	bin, dir := fakecli.Write(t, []string{
		`{"type":"system","subtype":"init","session_id":"s-1","model":"claude-fake"}`,
		`{"type":"control_request","request_id":"perm-1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"},"tool_use_id":"tu-1"}}`,
	}, []string{
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}}`,
		`{"type":"result","subtype":"success","result":"done","session_id":"s-1"}`,
	})

	var seen []string
	var asked claudecode.PermissionRequest
	res, err := claudecode.RunTurn(t.Context(), claudecode.TurnOptions{Binary: bin, WorkDir: dir, Prompt: "hi", SessionID: "s-1", Env: os.Environ()}, claudecode.Handler{
		OnEvent: func(ev claudecode.Event) error {
			seen = append(seen, ev.Type)
			return nil
		},
		CanUseTool: func(_ context.Context, req claudecode.PermissionRequest) claudecode.Decision {
			asked = req
			return claudecode.Decision{Allow: true}
		},
	})
	require.NoError(t, err)
	require.Equal(t, "done", res.Result.Result)
	require.Equal(t, "s-1", res.SessionID)
	require.Equal(t, "claude-fake", res.Model)
	require.Equal(t, []string{"system", "assistant", "result"}, seen)
	require.Equal(t, "Bash", asked.ToolName)
	require.Equal(t, "tu-1", asked.ToolUseID)

	in := fakecli.Input(t, dir)
	require.Len(t, in, 3)
	require.Equal(t, "control_request", in[0]["type"])
	require.Equal(t, "user", in[1]["type"])
	require.Equal(t, "control_response", in[2]["type"])
	resp := in[2]["response"].(map[string]any)
	require.Equal(t, "perm-1", resp["request_id"])
	body := resp["response"].(map[string]any)
	require.Equal(t, "allow", body["behavior"])
	require.Equal(t, map[string]any{"command": "ls"}, body["updatedInput"])

	args := fakecli.Args(t, dir)
	require.Contains(t, args, "--session-id s-1")
}

func TestRunTurnSessionNotFound(t *testing.T) {
	t.Parallel()
	bin, dir := fakecli.Write(t, []string{
		`{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["No conversation found with session ID: abc"]}`,
	})
	_, err := claudecode.RunTurn(t.Context(), claudecode.TurnOptions{Binary: bin, WorkDir: dir, Prompt: "hi", SessionID: "abc", Resume: true, Env: os.Environ()}, claudecode.Handler{})
	require.ErrorIs(t, err, claudecode.ErrSessionNotFound)
}

func TestRunTurnNoResult(t *testing.T) {
	t.Parallel()
	bin, dir := fakecli.Write(t, nil)
	_, err := claudecode.RunTurn(t.Context(), claudecode.TurnOptions{Binary: bin, WorkDir: dir, Prompt: "hi", Env: os.Environ()}, claudecode.Handler{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "without a result")
}

func TestRunTurnCancelSendsInterrupt(t *testing.T) {
	t.Parallel()
	bin, dir := fakecli.Write(t, []string{
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"working"}]}}`,
	}, []string{
		`{"type":"result","subtype":"error_during_execution","is_error":true}`,
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err := claudecode.RunTurn(ctx, claudecode.TurnOptions{Binary: bin, WorkDir: dir, Prompt: "hi", Env: os.Environ()}, claudecode.Handler{
		OnEvent: func(ev claudecode.Event) error {
			if ev.Type == "assistant" {
				cancel()
			}
			return nil
		},
	})
	require.ErrorIs(t, err, context.Canceled)
	in := fakecli.Input(t, dir)
	require.Len(t, in, 3)
	require.Equal(t, "interrupt", in[2]["request"].(map[string]any)["subtype"])
}
