//go:build !(darwin || linux || freebsd || netbsd || openbsd)

package doctor

// FreeBytes returns -1: free space isn't checked on this platform.
func FreeBytes(string) int64 { return -1 }
