package dialog

import (
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

func TestResumeQuery(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in    string
		query string
		ok    bool
	}{
		{"/resume", "", true},
		{"  /resume  ", "", true},
		{"/resume auth bug", "auth bug", true},
		{"/RESUME Auth", "Auth", true},
		{"/continue  login flow ", "login flow", true},
		{"resume", "", false},
		{"/resumex", "", false},
		{"/resume\nfoo", "", false},
		{"please /resume", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		query, ok := ResumeQuery(c.in)
		require.Equal(t, c.ok, ok, "input %q", c.in)
		require.Equal(t, c.query, query, "input %q", c.in)
	}
}

func userMsg(text string, hidden bool) message.Message {
	return message.Message{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: text, Hidden: hidden}},
	}
}

func TestBuildResumePrompts(t *testing.T) {
	t.Parallel()

	// Messages arrive newest first.
	p := buildResumePrompts([]message.Message{
		userMsg("Third\n  prompt", false),
		userMsg("keep going", true),
		userMsg("", false),
		userMsg("Second", false),
		userMsg("First prompt", false),
	})
	require.Equal(t, "First prompt", p.First)
	require.Equal(t, "Third prompt", p.Last)
	require.Equal(t, 3, p.Prompts)
	require.Equal(t, "first prompt\nsecond\nthird prompt\n", p.text)
	require.NotContains(t, p.text, "keep going")

	require.Equal(t, ResumePrompts{}, buildResumePrompts(nil))
}

func TestBuildResumePromptsCaps(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("é", resumeTextPerMessage)
	msgs := make([]message.Message, 20)
	for i := range msgs {
		msgs[i] = userMsg(long, false)
	}
	p := buildResumePrompts(msgs)
	require.Equal(t, 20, p.Prompts)
	require.LessOrEqual(t, len(p.text), resumeTextPerSession)
	require.True(t, strings.HasSuffix(p.text, "é") || strings.HasSuffix(p.text, "\n"))
}

func TestTruncateBytes(t *testing.T) {
	t.Parallel()

	require.Equal(t, "abc", truncateBytes("abc", 5))
	require.Equal(t, "ab", truncateBytes("abc", 2))
	// "é" is two bytes, so cutting at three keeps one whole rune.
	require.Equal(t, "é", truncateBytes("éé", 3))
	require.Empty(t, truncateBytes("é", 1))
}

func TestResumableSessions(t *testing.T) {
	t.Parallel()

	got := resumableSessions([]session.Session{
		{ID: "a", MessageCount: 3},
		{ID: "empty"},
		{ID: "child", MessageCount: 2, ParentSessionID: "a"},
		{ID: "b", MessageCount: 1},
	})
	ids := make([]string, len(got))
	for i, s := range got {
		ids[i] = s.ID
	}
	require.Equal(t, []string{"a", "b"}, ids)
}

func hitIDs(hits []resumeHit) []string {
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.session.ID
	}
	return ids
}

func TestMatchResume(t *testing.T) {
	t.Parallel()

	sessions := []session.Session{
		{ID: "s1", Title: "Fix login redirect"},
		{ID: "s2", Title: "Refactor auth middleware"},
		{ID: "s3", Title: "Release notes"},
	}
	prompts := map[string]ResumePrompts{
		"s1": {text: "the auth cookie is dropped after login\n"},
		"s3": {text: "write notes for v0.5.5\n"},
	}

	t.Run("empty query keeps every session in order", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, []string{"s1", "s2", "s3"}, hitIDs(matchResume(sessions, prompts, "  ")))
	})

	t.Run("title matches come before prompt matches", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, []string{"s2", "s1"}, hitIDs(matchResume(sessions, prompts, "AUTH")))
	})

	t.Run("every word must match", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, []string{"s1"}, hitIDs(matchResume(sessions, prompts, "auth cookie")))
		require.Empty(t, matchResume(sessions, prompts, "auth nothing"))
	})

	t.Run("words can be split between title and prompts", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, []string{"s3"}, hitIDs(matchResume(sessions, prompts, "release v0.5.5")))
	})

	t.Run("session ID and short hash prefixes match", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, []string{"s2"}, hitIDs(matchResume(sessions, prompts, "S2")))
		hash := session.HashID("s3")[:7]
		require.Equal(t, []string{"s3"}, hitIDs(matchResume(sessions, prompts, hash)))
	})

	t.Run("title highlights cover the matched words", func(t *testing.T) {
		t.Parallel()
		hits := matchResume(sessions, prompts, "fix login")
		require.Len(t, hits, 1)
		require.Equal(t, []int{0, 1, 2, 4, 5, 6, 7, 8}, hits[0].titleIdx)
	})
}
