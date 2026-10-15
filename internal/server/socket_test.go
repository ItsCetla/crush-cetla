//go:build !windows

package server

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeTimeoutErr is a minimal net.Error implementation whose Timeout()
// returns true. It is used to verify that IsStaleSocketErr never
// classifies a timeout as stale.
type fakeTimeoutErr struct{}

func (fakeTimeoutErr) Error() string   { return "fake timeout" }
func (fakeTimeoutErr) Timeout() bool   { return true }
func (fakeTimeoutErr) Temporary() bool { return true }

// shortTempDir returns a temporary directory whose path leaves room for a
// socket below it. t.TempDir() embeds the test name, and on macOS (where
// $TMPDIR is already long) that pushes socket paths past the 104-byte
// sun_path limit, so bind fails with EINVAL.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "crush")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestIsStaleSocketErr(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "ECONNREFUSED", err: syscall.ECONNREFUSED, want: true},
		{
			name: "wrapped ECONNREFUSED",
			err:  fmt.Errorf("dial: %w", syscall.ECONNREFUSED),
			want: true,
		},
		{name: "fs.ErrNotExist", err: fs.ErrNotExist, want: true},
		{
			name: "wrapped fs.ErrNotExist",
			err:  fmt.Errorf("stat: %w", fs.ErrNotExist),
			want: true,
		},
		{name: "timeout net.Error", err: fakeTimeoutErr{}, want: false},
		{name: "generic error", err: errors.New("boom"), want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, IsStaleSocketErr(tc.err))
		})
	}
}

func TestDefaultHost_XDGRuntimeDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)

	host := DefaultHost()

	require.True(t, strings.HasPrefix(host, "unix://"),
		"DefaultHost should return a unix:// URL, got %q", host)
	path := strings.TrimPrefix(host, "unix://")

	// The composed path may exceed maxUnixSocketPathLen and fall back
	// to /tmp; only assert containment when it did not. Recompose the
	// path under dir (rather than checking the returned path length,
	// which is short again after a /tmp fallback) to decide whether a
	// fallback happened. The socket is named crush-<uid>.sock.
	composed := filepath.Join(dir, filepath.Base(path))
	if len(composed) <= maxUnixSocketPathLen {
		require.True(t, strings.HasPrefix(path, dir),
			"socket path %q should live under %q", path, dir)
	}
	require.True(t, strings.HasSuffix(path, ".sock"),
		"socket path %q should end in .sock", path)
	require.Contains(t, filepath.Base(path), "crush",
		"socket filename should contain 'crush'")
}

func TestDefaultHost_FallbackTemp(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")

	host := DefaultHost()

	require.True(t, strings.HasPrefix(host, "unix://"),
		"DefaultHost should return a unix:// URL, got %q", host)
	path := strings.TrimPrefix(host, "unix://")
	require.NotEmpty(t, path, "fallback socket path must be non-empty")
	require.True(t, strings.HasSuffix(path, ".sock"),
		"socket path %q should end in .sock", path)
	require.Contains(t, filepath.Base(path), "crush",
		"socket filename should contain 'crush'")
	require.Equal(t, privateSocketDirName(currentUID()), filepath.Base(filepath.Dir(path)),
		"without XDG_RUNTIME_DIR the socket must live in a private per-user directory")
}

func TestListen_SocketIsOwnerOnly(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "s.sock")
	ln, _, err := listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(socketFileMode), info.Mode().Perm())
}

func TestListen_CreatesPrivateSocketDir(t *testing.T) {
	dir := filepath.Join(shortTempDir(t), privateSocketDirName(currentUID()))
	ln, _, err := listen("unix", filepath.Join(dir, "s.sock"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.True(t, info.IsDir())
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

func TestListen_RefusesUnsafeSocketDir(t *testing.T) {
	name := privateSocketDirName(currentUID())

	t.Run("writable by others", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), name)
		require.NoError(t, os.Mkdir(dir, 0o700))
		require.NoError(t, os.Chmod(dir, 0o777))
		_, _, err := listen("unix", filepath.Join(dir, "s.sock"))
		require.ErrorContains(t, err, "writable by other users")
	})

	t.Run("symlink", func(t *testing.T) {
		base := t.TempDir()
		target := filepath.Join(base, "elsewhere")
		require.NoError(t, os.Mkdir(target, 0o700))
		dir := filepath.Join(base, name)
		require.NoError(t, os.Symlink(target, dir))
		_, _, err := listen("unix", filepath.Join(dir, "s.sock"))
		require.ErrorContains(t, err, "not a directory")
	})

	t.Run("owned by another user", func(t *testing.T) {
		if os.Getuid() != 0 {
			t.Skip("needs root to hand a directory to another user")
		}
		dir := filepath.Join(t.TempDir(), name)
		require.NoError(t, os.Mkdir(dir, 0o700))
		require.NoError(t, os.Chown(dir, 4242, 4242))
		_, _, err := listen("unix", filepath.Join(dir, "s.sock"))
		require.ErrorContains(t, err, "owned by uid 4242")
	})
}

func TestVerifySocketOwner(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "s.sock")
	require.NoError(t, VerifySocketOwner(path), "a missing socket is left to the dial")

	ln, _, err := listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	require.NoError(t, VerifySocketOwner(path))

	if os.Getuid() != 0 {
		t.Skip("needs root to hand the socket to another user")
	}
	require.NoError(t, os.Chown(path, 4242, 4242))
	require.ErrorContains(t, VerifySocketOwner(path), "owned by uid 4242")
}

// staleSocketPath creates a deterministic stale unix socket file on
// disk: the socket node exists but no goroutine is accepting on it.
// It does so by binding a listener, disabling unlink-on-close, then
// closing the listener. The path is returned so the caller can probe
// it. A leftover file is best-effort removed via t.Cleanup.
func staleSocketPath(t *testing.T, path string) {
	t.Helper()
	ln, err := net.Listen("unix", path) //nolint:noctx
	require.NoError(t, err)
	ul, ok := ln.(*net.UnixListener)
	require.True(t, ok, "expected *net.UnixListener, got %T", ln)
	ul.SetUnlinkOnClose(false)
	require.NoError(t, ul.Close())

	// Verify it is actually stale: dialing should fail.
	conn, dialErr := net.DialTimeout("unix", path, 200*time.Millisecond) //nolint:noctx
	if dialErr == nil {
		conn.Close()
		t.Fatalf("expected stale socket at %q to refuse connections", path)
	}
	require.True(t, IsStaleSocketErr(dialErr),
		"expected stale-socket dial error, got %v", dialErr)

	t.Cleanup(func() {
		_ = os.Remove(path)
	})
}

func TestListen_RemovesStaleSocket(t *testing.T) {
	// t.TempDir() yields a path that may already be near the macOS
	// sun_path limit; use a short filename to stay well under it.
	dir := t.TempDir()
	path := filepath.Join(dir, "s.sock")

	staleSocketPath(t, path)

	// Confirm the stale node is present before we call listen.
	_, statErr := os.Stat(path)
	require.NoError(t, statErr, "stale socket file should exist on disk")

	ln, removedStale, err := listen("unix", path)
	require.NoError(t, err)
	require.NotNil(t, ln)
	require.True(t, removedStale, "listen should report removedStale=true")
	t.Cleanup(func() {
		_ = ln.Close()
	})
}

func TestListen_LiveSocketNotRemoved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.sock")

	ln1, err := net.Listen("unix", path) //nolint:noctx
	require.NoError(t, err)

	// Drain accepts so the listener stays alive and responsive without
	// blocking the test on a stray connection.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln1.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	t.Cleanup(func() {
		_ = ln1.Close()
		wg.Wait()
	})

	ln2, removedStale, err := listen("unix", path)
	if ln2 != nil {
		_ = ln2.Close()
	}
	require.Error(t, err, "listen on a live socket must fail")
	require.False(t, removedStale,
		"a live socket must never be removed (got removedStale=true)")

	// The live socket file must still be on disk and dialable.
	_, statErr := os.Stat(path)
	require.NoError(t, statErr, "live socket file should still exist")
	conn, dialErr := net.DialTimeout("unix", path, 200*time.Millisecond) //nolint:noctx
	require.NoError(t, dialErr, "live socket should still accept dials")
	_ = conn.Close()
}
