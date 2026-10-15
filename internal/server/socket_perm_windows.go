//go:build windows

package server

// VerifySocketOwner is a no-op on Windows, where the server listens on a
// named pipe rather than a Unix socket file.
func VerifySocketOwner(string) error { return nil }
