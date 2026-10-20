package artifacts

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

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
	u := st.SignInURL("http://127.0.0.1:7480/a/abc123")
	require.True(t, strings.HasPrefix(u, "http://127.0.0.1:7480/a/abc123#t="), u)
	// The link never carries the key itself.
	require.NotContains(t, u, k)
	_, ok := checkTicket(k, strings.TrimPrefix(u, "http://127.0.0.1:7480/a/abc123#t="), time.Now())
	require.True(t, ok)
}

func TestCheckTicket(t *testing.T) {
	t.Parallel()
	key := strings.Repeat("ab", 32)
	now := time.Unix(1_800_000_000, 0)
	tk, err := signInTicket(key, now)
	require.NoError(t, err)

	_, ok := checkTicket(key, tk, now)
	require.True(t, ok)
	_, ok = checkTicket(key, tk, now.Add(signInTTL+time.Second))
	require.False(t, ok, "expired")
	_, ok = checkTicket(strings.Repeat("cd", 32), tk, now)
	require.False(t, ok, "other key")
	_, ok = checkTicket(key, tk, now.Add(-time.Hour))
	require.False(t, ok, "too far ahead")

	parts := strings.Split(tk, ".")
	later := strconv.FormatInt(now.Add(time.Minute).Unix(), 10)
	_, ok = checkTicket(key, later+"."+parts[1]+"."+parts[2], now)
	require.False(t, ok, "changed expiry")
	for _, bad := range []string{"", "x", "1.2.3", tk + ".x", parts[0] + "." + parts[1] + "."} {
		_, ok = checkTicket(key, bad, now)
		require.False(t, ok, bad)
	}
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
