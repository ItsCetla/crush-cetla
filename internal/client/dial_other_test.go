//go:build !windows

package client

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestClientRefusesForeignSocket checks that the client never talks to a
// Unix socket owned by another user, since every request carries the
// client's environment.
func TestClientRefusesForeignSocket(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("needs root to hand the socket to another user")
	}
	path := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", path) //nolint:noctx
	require.NoError(t, err)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	c, err := NewClient(t.TempDir(), "unix", path)
	require.NoError(t, err)
	require.NoError(t, c.Health(context.Background()), "a socket we own is fine")

	require.NoError(t, os.Chown(path, 4242, 4242))
	c, err = NewClient(t.TempDir(), "unix", path)
	require.NoError(t, err)
	require.ErrorContains(t, c.Health(context.Background()), "refusing to connect")
}
