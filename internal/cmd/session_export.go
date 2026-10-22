package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/export"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/spf13/cobra"
)

var (
	sessionExportFormat   string
	sessionExportOutput   string
	sessionExportNoRedact bool
)

var sessionExportCmd = &cobra.Command{
	Use:   "export [id]",
	Short: "Export a session as Markdown, HTML or JSON",
	Long: `Export a session as a file you can share.

Markdown and JSON go to stdout unless -o is given. HTML is a single
self-contained page; without -o it is written to the current folder.
Use "-o -" to send any format to stdout.

API keys, tokens, bearer credentials and the values of variables named
like *KEY*, *TOKEN* or *SECRET* are replaced with [REDACTED], and your
home folder is shown as ~. --no-redact turns all of that off.

With no id, the most recent session is exported.`,
	Example: `  tada session export
  tada session export 3f2a9c --format html
  tada session export --format md -o notes.md`,
	Args: cobra.MaximumNArgs(1),
	RunE: runSessionExport,
}

func init() {
	sessionExportCmd.Flags().StringVarP(&sessionExportFormat, "format", "f", "md", "output format: md, html or json")
	sessionExportCmd.Flags().StringVarP(&sessionExportOutput, "output", "o", "", "file to write to, or - for stdout")
	sessionExportCmd.Flags().BoolVar(&sessionExportNoRedact, "no-redact", false, "keep secrets and the home folder path as they are")
	sessionCmd.AddCommand(sessionExportCmd)
}

func runSessionExport(cmd *cobra.Command, args []string) error {
	format, err := export.ParseFormat(sessionExportFormat)
	if err != nil {
		return err
	}

	dataDir, _ := cmd.Flags().GetString("data-dir")
	ctx := cmd.Context()
	cwd, err := ResolveCwd(cmd)
	if err != nil {
		return err
	}
	cfg, err := config.Init(cwd, dataDir, false)
	if err != nil {
		return fmt.Errorf("failed to initialize config: %w", err)
	}
	if dataDir == "" {
		dataDir = cfg.Config().Options.DataDirectory
	}
	conn, err := db.Connect(ctx, dataDir)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer conn.Close()

	queries := db.New(conn)
	data, err := loadExportData(ctx,
		session.NewService(queries, conn),
		message.NewService(queries),
		history.NewService(queries, conn),
		firstArg(args),
	)
	if err != nil {
		return err
	}
	data.WorkingDir = data.Session.WorkingDir
	if data.WorkingDir == "" {
		data.WorkingDir = cfg.WorkingDir()
	}

	opts := export.Options{Redact: !sessionExportNoRedact}
	if home, err := os.UserHomeDir(); err == nil {
		opts.HomeDir = home
	}

	out := sessionExportOutput
	if out == "" && format == export.FormatHTML {
		out = export.FileName(data.Session, format)
	}
	if out == "" || out == "-" {
		return export.Render(cmd.OutOrStdout(), format, data, opts)
	}
	path, err := writeExport(out, func(w io.Writer) error {
		return export.Render(w, format, data, opts)
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), path)
	return nil
}

// loadExportData gathers a session's messages and file history. An empty
// id picks the most recently updated session.
func loadExportData(ctx context.Context, sessions session.Service, messages message.Service, files history.Service, id string) (export.Data, error) {
	var sess session.Session
	if id == "" {
		list, err := sessions.List(ctx)
		if err != nil {
			return export.Data{}, fmt.Errorf("failed to list sessions: %w", err)
		}
		if len(list) == 0 {
			return export.Data{}, fmt.Errorf("no sessions found")
		}
		sess = list[0]
	} else {
		var err error
		sess, err = resolveSessionID(ctx, sessions, id)
		if err != nil {
			return export.Data{}, err
		}
	}

	msgs, err := messages.List(ctx, sess.ID)
	if err != nil {
		return export.Data{}, fmt.Errorf("failed to list messages: %w", err)
	}
	fileHistory, err := files.ListBySession(ctx, sess.ID)
	if err != nil {
		return export.Data{}, fmt.Errorf("failed to list file history: %w", err)
	}
	return export.Data{
		Session:    sess,
		Messages:   msgs,
		Files:      fileHistory,
		ExportedAt: time.Now(),
	}, nil
}

// writeExport writes a file through a temporary sibling so a failed
// render never leaves half a file behind, and returns its absolute path.
func writeExport(path string, render func(io.Writer) error) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(abs), "."+filepath.Base(abs)+".*")
	if err != nil {
		return "", fmt.Errorf("failed to create export file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := render(tmp); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), abs); err != nil {
		return "", fmt.Errorf("failed to write export file: %w", err)
	}
	return abs, nil
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}
