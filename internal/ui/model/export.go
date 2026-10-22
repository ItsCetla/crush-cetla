package model

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/control"
	"github.com/charmbracelet/crush/internal/export"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/util"
)

// exportSessionCmd writes the session as a self-contained HTML page in the
// data directory's exports folder and reports where it went.
func (m *UI) exportSessionCmd(sessionID string) tea.Cmd {
	ws := m.com.Workspace
	var dataDir string
	if cfg := m.com.Config(); cfg != nil && cfg.Options != nil {
		dataDir = cfg.Options.DataDirectory
	}
	return func() tea.Msg {
		path, err := exportSession(context.Background(), ws, dataDir, sessionID)
		if err != nil {
			return util.NewErrorMsg(fmt.Errorf("export session: %w", err))
		}
		return util.InfoMsg{
			Type: util.InfoTypeSuccess,
			Msg:  "Exported session to " + path,
			TTL:  10 * time.Second,
		}
	}
}

// exportWorkspace is the part of the workspace an export reads.
type exportWorkspace interface {
	GetSession(ctx context.Context, sessionID string) (session.Session, error)
	ListMessages(ctx context.Context, sessionID string) ([]message.Message, error)
	ListSessionHistory(ctx context.Context, sessionID string) ([]history.File, error)
	Control(ctx context.Context, c control.Command) (control.Reply, error)
	WorkingDir() string
}

func exportSession(ctx context.Context, ws exportWorkspace, dataDir, sessionID string) (string, error) {
	sess, err := ws.GetSession(ctx, sessionID)
	if err != nil {
		return "", err
	}
	msgs, err := ws.ListMessages(ctx, sessionID)
	if err != nil {
		return "", err
	}
	files, err := ws.ListSessionHistory(ctx, sessionID)
	if err != nil {
		return "", err
	}
	data := export.Data{
		Session:    sess,
		Messages:   msgs,
		Files:      files,
		WorkingDir: sess.WorkingDir,
		ExportedAt: time.Now(),
	}
	if data.WorkingDir == "" {
		data.WorkingDir = ws.WorkingDir()
	}
	// The network receipt is a nice extra, so a failure only leaves it out.
	if r, err := ws.Control(ctx, control.Command{Op: control.OpNetwork, SessionID: sessionID}); err == nil && r.Network != nil {
		data.Hosts = r.Network.Hosts
	}

	opts := export.Options{Redact: true}
	if home, err := os.UserHomeDir(); err == nil {
		opts.HomeDir = home
	}

	if dataDir == "" {
		dataDir = data.WorkingDir
	}
	dir := filepath.Join(dataDir, "exports")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, export.FileName(sess, export.FormatHTML))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", err
	}
	if err := export.Render(f, export.FormatHTML, data, opts); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return path, nil
}
