package cmd

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/export"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

func TestSessionExportLoadsLatestSession(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	conn, err := db.Connect(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	q := db.New(conn)
	sessions := session.NewService(q, conn)
	messages := message.NewService(q)
	files := history.NewService(q, conn)

	sess, err := sessions.Create(ctx, "Newest")
	require.NoError(t, err)
	_, err = messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "use sk-proj-abcdefghijklmnopqrstuvwx please"}},
	})
	require.NoError(t, err)
	_, err = files.Create(ctx, sess.ID, "/tmp/demo/a.txt", "one\n")
	require.NoError(t, err)
	_, err = files.CreateVersion(ctx, sess.ID, "/tmp/demo/a.txt", "one\ntwo\n")
	require.NoError(t, err)

	data, err := loadExportData(ctx, sessions, messages, files, "")
	require.NoError(t, err)
	require.Equal(t, sess.ID, data.Session.ID)
	require.Len(t, data.Messages, 1)
	require.Len(t, data.Files, 2)

	// Timestamps have one-second precision, so the hash lookup is checked
	// once a second session exists rather than relying on list order.
	_, err = sessions.Create(ctx, "Other")
	require.NoError(t, err)
	data, err = loadExportData(ctx, sessions, messages, files, session.HashID(sess.ID)[:10])
	require.NoError(t, err)
	require.Equal(t, sess.ID, data.Session.ID)

	var buf bytes.Buffer
	require.NoError(t, export.Render(&buf, export.FormatMarkdown, data, export.Options{Redact: true}))
	require.Contains(t, buf.String(), "# Newest")
	require.Contains(t, buf.String(), "use [REDACTED] please")
	require.Contains(t, buf.String(), "a.txt` (+1 −0)")
}

func TestSessionExportNoSessions(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	conn, err := db.Connect(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	q := db.New(conn)

	_, err = loadExportData(ctx, session.NewService(q, conn), message.NewService(q), history.NewService(q, conn), "")
	require.EqualError(t, err, "no sessions found")
}

func TestWriteExportLeavesNothingOnFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "out.html")

	path, err := writeExport(target, func(w io.Writer) error {
		_, err := io.WriteString(w, "<p>hi</p>")
		return err
	})
	require.NoError(t, err)
	require.Equal(t, target, path)
	got, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "<p>hi</p>", string(got))

	_, err = writeExport(filepath.Join(dir, "bad.html"), func(io.Writer) error {
		return errors.New("boom")
	})
	require.Error(t, err)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "out.html", entries[0].Name())
}
