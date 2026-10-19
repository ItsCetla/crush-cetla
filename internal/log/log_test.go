package log

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRecoverPanicWritesToLogDir(t *testing.T) {
	dir := t.TempDir()
	logDir.Store(&dir)
	t.Cleanup(func() { logDir.Store(nil) })

	cleaned := false
	func() {
		defer RecoverPanic("test", func() { cleaned = true })
		panic("boom")
	}()
	if !cleaned {
		t.Error("Cleanup was not called")
	}

	matches, err := filepath.Glob(filepath.Join(dir, "tada-panic-test-*.log"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("Expected one panic log in %s, got %v (%v)", dir, matches, err)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Panic in test: boom") {
		t.Errorf("Unexpected panic log contents: %q", data)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(matches[0])
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("Expected panic log mode 0600, got %o", perm)
		}
	}
}

func TestCreatePanicLogFallsBackToTempDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	logDir.Store(&missing)
	t.Cleanup(func() { logDir.Store(nil) })

	file, err := createPanicLog("fallback")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(file.Name()) })
	defer file.Close()

	if got, want := filepath.Dir(file.Name()), filepath.Clean(os.TempDir()); got != want {
		t.Errorf("Expected panic log in %s, got %s", want, got)
	}
}
