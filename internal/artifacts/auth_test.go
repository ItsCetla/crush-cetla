package artifacts

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestViewerKeyIsSharedAndPrivate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Several processes may create the key at once; they must agree.
	keys := make([]string, 8)
	var wg sync.WaitGroup
	for i := range keys {
		wg.Go(func() {
			st, err := NewStore(dir)
			require.NoError(t, err)
			k, err := st.ViewerKey()
			require.NoError(t, err)
			keys[i] = k
		})
	}
	wg.Wait()
	for _, k := range keys {
		require.True(t, validKey(k), k)
		require.Equal(t, keys[0], k)
	}

	fi, err := os.Stat(filepath.Join(dir, keyFile))
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	}

	// The key file isn't an artifact.
	st, err := NewStore(dir)
	require.NoError(t, err)
	list, err := st.List(ListOptions{})
	require.NoError(t, err)
	require.Empty(t, list)

	// A damaged key file is replaced.
	bad := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bad, keyFile), []byte("short"), 0o600))
	st, err = NewStore(bad)
	require.NoError(t, err)
	k, err := st.ViewerKey()
	require.NoError(t, err)
	require.True(t, validKey(k))
	raw, err := os.ReadFile(filepath.Join(bad, keyFile))
	require.NoError(t, err)
	require.Equal(t, k, string(raw))
}

func TestSignInURL(t *testing.T) {
	t.Parallel()
	st, err := NewStore(t.TempDir())
	require.NoError(t, err)
	k, err := st.ViewerKey()
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:7480/a/abc123#k="+k, st.SignInURL("http://127.0.0.1:7480/a/abc123"))
}

func TestPingNeedsProof(t *testing.T) {
	t.Parallel()
	st, err := NewStore(t.TempDir())
	require.NoError(t, err)

	serve := func(store *Store, claim func(addr string) string) string {
		ts := httptest.NewUnstartedServer(nil)
		addr := ts.Listener.Addr().String()
		h, err := Handler(store, ServerOptions{}, claim(addr))
		require.NoError(t, err)
		ts.Config.Handler = h
		ts.Start()
		t.Cleanup(ts.Close)
		return addr
	}
	self := func(addr string) string { return "http://" + addr }

	// The store's own viewer answers with a valid proof.
	require.True(t, pingOK(serve(st, self), st))

	// A viewer for another store (another key) doesn't pass, even though
	// it is a real Ta-Da viewer.
	other, err := NewStore(t.TempDir())
	require.NoError(t, err)
	require.False(t, pingOK(serve(other, self), st))

	// A proof made for another address is refused, so a process on one
	// port can't relay the answers of a real viewer on another.
	relayed := serve(st, func(string) string { return "http://127.0.0.1:1" })
	require.False(t, pingOK(relayed, st))

	// Nothing listening.
	require.False(t, pingOK("127.0.0.1:1", st))

	// The ping itself never reveals the key.
	addr := serve(st, self)
	k, err := st.ViewerKey()
	require.NoError(t, err)
	for _, nonce := range []string{"", "abc", k} {
		req := httptest.NewRequestWithContext(t.Context(), "GET", "/api/ping?nonce="+nonce, nil)
		req.Host = addr
		w := httptest.NewRecorder()
		h, err := Handler(st, ServerOptions{}, "http://"+addr)
		require.NoError(t, err)
		h.ServeHTTP(w, req)
		require.False(t, strings.Contains(w.Body.String(), k))
	}
}
