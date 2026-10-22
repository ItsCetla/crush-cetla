//go:build darwin || linux || freebsd || netbsd || openbsd

package doctor

import "golang.org/x/sys/unix"

// FreeBytes returns the space available to this user on the disk that
// holds path, or -1 when it can't be read.
func FreeBytes(path string) int64 {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return -1
	}
	return int64(uint64(st.Bavail) * uint64(st.Bsize)) //nolint:gosec
}
