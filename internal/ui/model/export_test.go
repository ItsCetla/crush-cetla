package model

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/control"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/netlog"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

type fakeExportWorkspace struct {
	dir        string
	controlErr error
}

func (f fakeExportWorkspace) GetSession(_ context.Context, id string) (session.Session, error) {
	return session.Session{ID: id, Title: "Fix the build", CreatedAt: 1760000000}, nil
}

func (f fakeExportWorkspace) ListMessages(context.Context, string) ([]message.Message, error) {
	return []message.Message{{
		ID:    "m1",
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "my key is sk-proj-abcdefghijklmnopqrstuvwx"}},
	}}, nil
}

func (f fakeExportWorkspace) ListSessionHistory(context.Context, string) ([]history.File, error) {
	return nil, nil
}

func (f fakeExportWorkspace) Control(context.Context, control.Command) (control.Reply, error) {
	if f.controlErr != nil {
		return control.Reply{}, f.controlErr
	}
	return control.Reply{Network: &netlog.Report{Hosts: []netlog.Host{{Host: "api.example.com", Requests: 3}}}}, nil
}

func (f fakeExportWorkspace) WorkingDir() string { return f.dir }

func TestExportSessionWritesRedactedHTML(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()

	path, err := exportSession(t.Context(), fakeExportWorkspace{dir: t.TempDir()}, dataDir, "s1")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dataDir, "exports"), filepath.Dir(path))
	require.True(t, strings.HasSuffix(path, ".html"))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	page := string(got)
	require.Contains(t, page, "Fix the build")
	require.Contains(t, page, "[REDACTED]")
	require.NotContains(t, page, "sk-proj-abcdefghijklmnopqrstuvwx")
	require.Contains(t, page, "api.example.com")
}

func TestExportSessionWithoutNetworkReceipt(t *testing.T) {
	t.Parallel()
	wd := t.TempDir()

	ws := fakeExportWorkspace{dir: wd, controlErr: errors.New("no hub")}
	path, err := exportSession(t.Context(), ws, "", "s1")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(wd, "exports"), filepath.Dir(path))
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(got), "api.example.com")
}
