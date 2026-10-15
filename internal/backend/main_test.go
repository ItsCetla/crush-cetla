package backend

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Keep the developer's real OpenCode install out of loaded config.
	os.Setenv("CRUSH_DISABLE_OPENCODE", "1")
	os.Exit(m.Run())
}
