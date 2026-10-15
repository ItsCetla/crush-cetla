//go:build !windows

package server

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// socketFileMode is applied to the Unix socket right after binding. Only
// the owner may connect: clients send their whole environment, API keys
// included, to whoever answers on the socket.
const socketFileMode = 0o600

// prepareSocketDir readies the directory of a Unix socket before binding.
// When the socket lives in Crush's private per-user directory, that
// directory is created with mode 0700 and verified (see
// [ensurePrivateSocketDir]). Sockets elsewhere, e.g. in $XDG_RUNTIME_DIR
// or at a path given with --host, are left to their directory's own
// permissions.
func prepareSocketDir(address string) error {
	dir := filepath.Dir(address)
	if filepath.Base(dir) != privateSocketDirName(currentUID()) {
		return nil
	}
	return ensurePrivateSocketDir(dir)
}

// ensurePrivateSocketDir creates dir with mode 0700, or checks that an
// existing dir is a real directory owned by the current user that neither
// group nor others can write to. Anything else could let another user
// replace the socket, so it is refused.
func ensurePrivateSocketDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("create socket directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect socket directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("refusing to use socket directory %s: not a directory", dir)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("refusing to use socket directory %s: owned by uid %d, not the current user (uid %d)", dir, st.Uid, os.Getuid())
	}
	if perm := info.Mode().Perm(); perm&0o022 != 0 {
		return fmt.Errorf("refusing to use socket directory %s: writable by other users (mode %#o); remove it or run chmod 700 on it", dir, perm)
	}
	return nil
}

// VerifySocketOwner checks that the Unix socket at path belongs to the
// current user before a client connects to it and sends its environment.
// A missing socket is not an error; dialing reports it.
func VerifySocketOwner(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect server socket: %w", err)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("refusing to connect to Crush server socket %s: owned by uid %d, not the current user (uid %d)", path, st.Uid, os.Getuid())
	}
	return nil
}
