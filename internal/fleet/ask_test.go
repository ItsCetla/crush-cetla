package fleet

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// chatServer answers /models and echoes what /chat/completions got.
func chatServer(t *testing.T, got *chatRequest) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			_, _ = w.Write([]byte(`{"data":[{"id":"qwen"}]}`))
			return
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(got))
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"<think>hmm</think>\n Looks fine. "}}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestAsk(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))

	var got chatRequest
	url := chatServer(t, &got)
	cfg := testConfig(t, config.Machines{"m1": {URL: url + "/v1", Model: "qwen", Slots: 1}})
	pool := NewPool(t.TempDir())

	out, err := Ask(context.Background(), cfg, pool, dir, AskParams{Prompt: "Review this.", Files: []string{"main.go"}})
	require.NoError(t, err)
	require.Equal(t, "[ran on m1 (qwen)]\nLooks fine.", out)
	require.Equal(t, "qwen", got.Model)
	require.Len(t, got.Messages, 2)
	require.Contains(t, got.Messages[1].Content, "Review this.\n\n--- main.go ---\npackage main\n")
	require.Equal(t, 0, pool.Busy(Machine{Name: "m1", MachineConfig: cfg.Machines["m1"]}), "the slot is released")
}

func TestAskNoneFree(t *testing.T) {
	t.Parallel()

	var got chatRequest
	url := chatServer(t, &got)
	cfg := testConfig(t, config.Machines{"m1": {URL: url + "/v1", Model: "qwen", Slots: 1}})
	pool := NewPool(t.TempDir())
	m, _ := Lookup(cfg, "m1")
	lease, err := pool.TryAcquire(m)
	require.NoError(t, err)
	defer lease.Release()

	_, err = Ask(context.Background(), cfg, pool, t.TempDir(), AskParams{Prompt: "hi"})
	require.ErrorContains(t, err, "busy")
}

func TestWithFilesStaysInside(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("token"), 0o644))
	require.NoError(t, os.Symlink(secret, filepath.Join(root, "link.txt")))
	require.NoError(t, os.WriteFile(filepath.Join(root, "bin"), []byte{0, 1, 2}, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "big.txt"), []byte(strings.Repeat("a", 500)), 0o644))

	for _, name := range []string{secret, "../" + filepath.Base(outside) + "/secret.txt", "link.txt"} {
		_, err := withFiles("p", []string{name}, root, 10000)
		require.ErrorContains(t, err, "outside the working directory", name)
	}
	_, err := withFiles("p", []string{"bin"}, root, 10000)
	require.ErrorContains(t, err, "isn't a text file")
	_, err = withFiles("p", []string{"big.txt"}, root, 100)
	require.ErrorContains(t, err, "doesn't fit")
}
