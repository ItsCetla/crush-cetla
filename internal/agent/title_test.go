package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGenerateTitle_ClaudeCodeUsesPrompt verifies that a Claude Code
// session is titled from its first prompt without asking the small
// model, so the prompt never goes to another provider for a title.
func TestGenerateTitle_ClaudeCodeUsesPrompt(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	a := NewSessionAgent(SessionAgentOptions{
		Sessions: env.sessions,
		Messages: env.messages,
	}).(*sessionAgent)
	// The small model has no LanguageModel: calling it would panic.
	a.SetModels(Model{ClaudeCode: true}, Model{})

	sess, err := env.sessions.Create(t.Context(), "session")
	require.NoError(t, err)

	a.GenerateTitle(t.Context(), sess.ID, "  fix the\nlogin   bug  ")

	got, err := env.sessions.Get(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Equal(t, "fix the login bug", got.Title)
}

func TestPromptTitle(t *testing.T) {
	t.Parallel()

	require.Empty(t, promptTitle(" \n "))
	require.Equal(t, "a b", promptTitle("a\n\tb"))
	long := promptTitle("word 0123456789 0123456789 0123456789 0123456789 0123456789")
	require.LessOrEqual(t, len([]rune(long)), 50)
	require.Contains(t, long, "…")
}
