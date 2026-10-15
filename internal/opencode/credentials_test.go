package opencode

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func credentialRow(id, integration, value, active string, created int) string {
	return fmt.Sprintf(
		"INSERT INTO credential (id, integration_id, label, value, active, time_created, time_updated) VALUES ('%s', %s, 'label', '%s', %s, %d, %d)",
		id, integration, value, active, created, created,
	)
}

func TestLoadCredentials(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	p := Paths{DataDir: dir}
	writeFile(t, filepath.Join(dir, "auth.json"), `{
		"anthropic": {"type": "api", "key": "v1-anthropic"},
		"google": {"type": "api", "key": "v1-google"},
		"groq": {"type": "api", "key": "v1-groq"},
		"zai-coding-plan": {"type": "api", "key": "v1-zai"},
		"github-copilot": {"type": "oauth", "refresh": "r", "access": "a", "expires": 1},
		"https://enterprise.example.com/": {"type": "wellknown", "key": "k", "token": "t"},
		"empty": {"type": "api", "key": ""},
		"garbage": 42
	}`)

	key := func(k string) string { return `{"type":"key","key":"` + k + `"}` }
	oauth := `{"type":"oauth","methodID":"device","refresh":"r","access":"a","expires":1}`
	db := newOpenCodeDB(t, filepath.Join(dir, "opencode.db"),
		// The active row wins over a newer inactive one.
		credentialRow("c1", "'openai'", key("v2-openai-active"), "1", 100),
		credentialRow("c2", "'openai'", key("v2-openai-newer"), "0", 200),
		// Without an active row the newest wins.
		credentialRow("c3", "'deepseek'", key("v2-deepseek-old"), "NULL", 100),
		credentialRow("c4", "'deepseek'", key("v2-deepseek-new"), "NULL", 200),
		// An active OAuth credential hides both older keys and V1's key.
		credentialRow("c5", "'anthropic'", key("v2-anthropic-key"), "0", 100),
		credentialRow("c6", "'anthropic'", oauth, "1", 50),
		// V2 beats V1 and IDs map to catwalk's.
		credentialRow("c7", "'google'", key("v2-google"), "NULL", 100),
		// Never returned.
		credentialRow("c8", "'azure'", `{"type":"external","methodID":"azure-cli"}`, "1", 100),
		credentialRow("c9", "'https://wk.example.com'", key("wellknown"), "NULL", 100),
		credentialRow("c10", "'mcp_0123456789abcdef'", oauth, "NULL", 100),
		credentialRow("c11", "NULL", key("orphan"), "NULL", 100),
		credentialRow("c12", "'broken'", `not json`, "NULL", 100),
	)
	require.NoError(t, db.Close())

	got, err := LoadCredentials(context.Background(), p, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"openai":   "v2-openai-active",
		"deepseek": "v2-deepseek-new",
		"gemini":   "v2-google",
		"groq":     "v1-groq",
		"zai":      "v1-zai",
	}, got)
}

func TestLoadCredentialsMissing(t *testing.T) {
	t.Parallel()

	got, err := LoadCredentials(context.Background(), Paths{DataDir: filepath.Join(t.TempDir(), "nope")}, nil)
	require.NoError(t, err)
	require.Empty(t, got)

	got, err = LoadCredentials(context.Background(), Paths{}, nil)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestLoadCredentialsDBWithoutTable(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "auth.json"), `{"groq": {"type": "api", "key": "k"}}`)
	db, err := openSQLite(filepath.Join(dir, "opencode.db"))
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `CREATE TABLE other (x)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	got, err := LoadCredentials(context.Background(), Paths{DataDir: dir}, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"groq": "k"}, got)
}

func TestLoadCredentialsBadAuthJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "auth.json"), `[]`)
	db := newOpenCodeDB(t, filepath.Join(dir, "opencode.db"),
		credentialRow("c1", "'openai'", `{"type":"key","key":"k"}`, "1", 1))
	require.NoError(t, db.Close())

	got, err := LoadCredentials(context.Background(), Paths{DataDir: dir}, nil)
	require.Error(t, err)
	require.Equal(t, map[string]string{"openai": "k"}, got)
}
