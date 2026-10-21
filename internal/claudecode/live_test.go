package claudecode

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLiveSessions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("HOME", t.TempDir())
	sessions := filepath.Join(dir, "sessions")
	require.NoError(t, os.MkdirAll(sessions, 0o700))

	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(sessions, name), []byte(body), 0o600))
	}
	pid := os.Getpid()
	write("live.json", fmt.Sprintf(`{"pid":%d,"sessionId":"abc","cwd":"/w","name":"fix bug","kind":"interactive","status":"waiting","updatedAt":1760000000000}`, pid))
	// Same pid twice: listed once.
	write("dup.json", fmt.Sprintf(`{"pid":%d,"sessionId":"dup","kind":"interactive","updatedAt":1}`, pid))
	write("print.json", fmt.Sprintf(`{"pid":%d,"sessionId":"p","kind":"print"}`, pid))
	write("bad.json", `{not json`)
	write("nopid.json", `{"sessionId":"z","kind":"interactive"}`)
	// Not a session file and never read.
	write(fmt.Sprintf("%d.key", pid), fmt.Sprintf(`{"pid":%d,"sessionId":"secret","kind":"interactive"}`, pid))

	got := LiveSessions()
	require.Len(t, got, 1)
	s := got[0]
	require.Equal(t, pid, s.PID)
	require.Contains(t, []string{"abc", "dup"}, s.SessionID)
	require.NotEqual(t, "secret", s.SessionID)
}

func TestLiveSessionsFields(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("HOME", t.TempDir())
	sessions := filepath.Join(dir, "sessions")
	require.NoError(t, os.MkdirAll(sessions, 0o700))
	body := fmt.Sprintf(`{"pid":%d,"sessionId":"abc","cwd":"/w","name":"fix bug","kind":"interactive","status":"waiting","updatedAt":1760000000000,"statusUpdatedAt":1760000001000}`, os.Getpid())
	require.NoError(t, os.WriteFile(filepath.Join(sessions, "a.json"), []byte(body), 0o600))

	got := LiveSessions()
	require.Len(t, got, 1)
	s := got[0]
	require.Equal(t, "abc", s.SessionID)
	require.Equal(t, "/w", s.Cwd)
	require.Equal(t, "fix bug", s.Name)
	require.Equal(t, "waiting", s.Status)
	require.Equal(t, int64(1760000000000), s.UpdatedAt.UnixMilli())
	require.Equal(t, int64(1760000001000), s.StatusUpdated.UnixMilli())
}

func TestProcessAlive(t *testing.T) {
	t.Parallel()
	require.True(t, processAlive(os.Getpid()))
}
