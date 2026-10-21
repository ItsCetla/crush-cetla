package cmd

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Cobra sorts a command's children in place the first time Commands()
	// is called. Sort them once here so parallel tests only ever read them.
	rootCmd.Commands()
	os.Exit(m.Run())
}
