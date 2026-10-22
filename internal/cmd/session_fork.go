package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/rewind"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/spf13/cobra"
)

var (
	sessionForkAt   string
	sessionForkJSON bool
)

var sessionForkCmd = &cobra.Command{
	Use:   "fork <id>",
	Short: "Copy a session into a new one",
	Long: `Copy a session's chat, file history and read records into a new session.

With --at, a user message cuts the copy just before it and any other message
copies through the end of its turn. Files on disk are not copied. ID can be a
UUID, full hash, or hash prefix; --at takes a message ID or a unique prefix.
Use --json for machine-readable output.`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionFork,
}

func init() {
	sessionForkCmd.Flags().StringVar(&sessionForkAt, "at", "", "message ID to fork at (default: copy the whole session)")
	sessionForkCmd.Flags().BoolVar(&sessionForkJSON, "json", false, "output in JSON format")
	sessionCmd.AddCommand(sessionForkCmd)
}

type sessionForkResult struct {
	ID             string `json:"id"`
	UUID           string `json:"uuid"`
	Title          string `json:"title"`
	MessageCount   int64  `json:"message_count"`
	ForkedFrom     string `json:"forked_from"`
	ForkedFromUUID string `json:"forked_from_uuid"`
	Forked         bool   `json:"forked"`
}

func runSessionFork(cmd *cobra.Command, args []string) error {
	ctx, svc, cleanup, err := sessionSetup(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	src, err := resolveSessionID(ctx, svc.sessions, args[0])
	if err != nil {
		return err
	}
	at, err := resolveMessageID(ctx, svc.messages, src.ID, sessionForkAt)
	if err != nil {
		return err
	}

	// No agent runs in this process, so the session is never seen as busy.
	rw := rewind.New(rewind.Options{
		DB:       svc.conn,
		Messages: svc.messages,
		Sessions: svc.sessions,
		History:  history.NewService(svc.queries, svc.conn),
	})
	fork, err := rw.Fork(ctx, src.ID, at)
	if err != nil {
		return fmt.Errorf("failed to fork session: %w", err)
	}

	out := cmd.OutOrStdout()
	if sessionForkJSON {
		enc := json.NewEncoder(out)
		enc.SetEscapeHTML(false)
		return enc.Encode(sessionForkResult{
			ID:             session.HashID(fork.ID),
			UUID:           fork.ID,
			Title:          fork.Title,
			MessageCount:   fork.MessageCount,
			ForkedFrom:     session.HashID(src.ID),
			ForkedFromUUID: src.ID,
			Forked:         true,
		})
	}

	fmt.Fprintf(out, "Forked session %s as %s (%q, %d messages)\n",
		session.HashID(src.ID)[:12], session.HashID(fork.ID)[:12], fork.Title, fork.MessageCount)
	return nil
}

// resolveMessageID finds a message of a session by its full ID or a unique
// prefix. An empty id stays empty.
func resolveMessageID(ctx context.Context, svc message.Service, sessionID, id string) (string, error) {
	if id == "" {
		return "", nil
	}
	msgs, err := svc.List(ctx, sessionID)
	if err != nil {
		return "", fmt.Errorf("failed to list messages: %w", err)
	}
	var matches []string
	for _, m := range msgs {
		if m.ID == id {
			return id, nil
		}
		if strings.HasPrefix(m.ID, id) {
			matches = append(matches, m.ID)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no message found with ID %q in session %s", id, session.HashID(sessionID)[:12])
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("message ID prefix %q matches %d messages; use more characters", id, len(matches))
	}
}
