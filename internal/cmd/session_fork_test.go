package cmd

import (
	"testing"

	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

func TestResolveMessageID(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	conn, err := db.Connect(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	q := db.New(conn)
	sessions := session.NewService(q, conn)
	messages := message.NewService(q)

	s, err := sessions.Create(ctx, "Work")
	require.NoError(t, err)
	m, err := messages.Create(ctx, s.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "hi"}},
	})
	require.NoError(t, err)

	got, err := resolveMessageID(ctx, messages, s.ID, "")
	require.NoError(t, err)
	require.Empty(t, got)

	got, err = resolveMessageID(ctx, messages, s.ID, m.ID)
	require.NoError(t, err)
	require.Equal(t, m.ID, got)

	got, err = resolveMessageID(ctx, messages, s.ID, m.ID[:8])
	require.NoError(t, err)
	require.Equal(t, m.ID, got)

	_, err = resolveMessageID(ctx, messages, s.ID, "zzz")
	require.ErrorContains(t, err, "no message found")
}
