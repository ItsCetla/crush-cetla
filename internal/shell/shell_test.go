package shell

import (
	"context"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// Benchmark to measure CPU efficiency
func BenchmarkShellQuickCommands(b *testing.B) {
	shell := NewShell(&Options{WorkingDir: b.TempDir()})

	b.ReportAllocs()

	for b.Loop() {
		_, _, err := shell.Exec(b.Context(), "echo test")
		exitCode := ExitCode(err)
		if err != nil || exitCode != 0 {
			b.Fatalf("Command failed: %v, exit code: %d", err, exitCode)
		}
	}
}

func TestTestTimeout(t *testing.T) {
	// XXX(@andreynering): This fails on Windows. Address once possible.
	if runtime.GOOS == "windows" {
		t.Skip("Skipping test on Windows")
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	t.Cleanup(cancel)

	shell := NewShell(&Options{WorkingDir: t.TempDir()})
	_, _, err := shell.Exec(ctx, "sleep 10")
	if status := ExitCode(err); status == 0 {
		t.Fatalf("Expected non-zero exit status, got %d", status)
	}
	if !IsInterrupt(err) {
		t.Fatalf("Expected command to be interrupted, but it was not")
	}
	if err == nil {
		t.Fatalf("Expected an error due to timeout, but got none")
	}
}

func TestTestCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // immediately cancel the context

	shell := NewShell(&Options{WorkingDir: t.TempDir()})
	_, _, err := shell.Exec(ctx, "sleep 10")
	if status := ExitCode(err); status == 0 {
		t.Fatalf("Expected non-zero exit status, got %d", status)
	}
	if !IsInterrupt(err) {
		t.Fatalf("Expected command to be interrupted, but it was not")
	}
	if err == nil {
		t.Fatalf("Expected an error due to cancel, but got none")
	}
}

func TestRunCommandError(t *testing.T) {
	shell := NewShell(&Options{WorkingDir: t.TempDir()})
	_, _, err := shell.Exec(t.Context(), "nopenopenope")
	if status := ExitCode(err); status == 0 {
		t.Fatalf("Expected non-zero exit status, got %d", status)
	}
	if IsInterrupt(err) {
		t.Fatalf("Expected command to not be interrupted, but it was")
	}
	if err == nil {
		t.Fatalf("Expected an error, got nil")
	}
}

func TestRunContinuity(t *testing.T) {
	tempDir1 := t.TempDir()
	tempDir2 := t.TempDir()

	shell := NewShell(&Options{WorkingDir: tempDir1})
	if _, _, err := shell.Exec(t.Context(), "export FOO=bar"); err != nil {
		t.Fatalf("failed to set env: %v", err)
	}
	if _, _, err := shell.Exec(t.Context(), "cd "+filepath.ToSlash(tempDir2)); err != nil {
		t.Fatalf("failed to change directory: %v", err)
	}
	out, _, err := shell.Exec(t.Context(), "echo $FOO ; pwd")
	if err != nil {
		t.Fatalf("failed to echo: %v", err)
	}
	expect := "bar\n" + tempDir2 + "\n"
	if out != expect {
		t.Fatalf("expected output %q, got %q", expect, out)
	}
}

func TestCrossPlatformExecution(t *testing.T) {
	shell := NewShell(&Options{WorkingDir: "."})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// Test a simple command that should work on all platforms
	stdout, stderr, err := shell.Exec(ctx, "echo hello")
	if err != nil {
		t.Fatalf("Echo command failed: %v, stderr: %s", err, stderr)
	}

	if stdout == "" {
		t.Error("Echo command produced no output")
	}

	// The output should contain "hello" regardless of platform
	if !strings.Contains(strings.ToLower(stdout), "hello") {
		t.Errorf("Echo output should contain 'hello', got: %q", stdout)
	}
}

func TestNewShellScrubsProviderKeys(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "dummy")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "dummy")
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "dummy")
	t.Setenv("CUSTOM_PROVIDER_KEY", "dummy")
	t.Setenv("GH_TOKEN", "kept")
	t.Setenv("GITHUB_TOKEN", "kept")
	t.Setenv("AWS_ACCESS_KEY_ID", "kept")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "kept")
	t.Setenv("SSH_AUTH_SOCK", "kept")
	SetScrubbedEnvVars([]string{"CUSTOM_PROVIDER_KEY"})
	t.Cleanup(func() { SetScrubbedEnvVars(nil) })

	sh := NewShell(&Options{WorkingDir: t.TempDir()})
	env := sh.GetEnv()
	for _, name := range []string{"OPENAI_API_KEY", "ANTHROPIC_AUTH_TOKEN", "AWS_BEARER_TOKEN_BEDROCK", "CUSTOM_PROVIDER_KEY"} {
		if slices.ContainsFunc(env, func(e string) bool { return strings.HasPrefix(e, name+"=") }) {
			t.Errorf("%s was not removed from the shell environment", name)
		}
	}
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "SSH_AUTH_SOCK"} {
		if !slices.Contains(env, name+"=kept") {
			t.Errorf("%s was removed from the shell environment", name)
		}
	}

	stdout, _, err := sh.Exec(t.Context(), "echo \"${OPENAI_API_KEY:-unset} ${CUSTOM_PROVIDER_KEY:-unset} $GH_TOKEN\"")
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}
	if got := strings.TrimSpace(stdout); got != "unset unset kept" {
		t.Errorf("unexpected output %q", got)
	}

	SetScrubbedEnvVars(nil)
	if !slices.Contains(NewShell(nil).GetEnv(), "CUSTOM_PROVIDER_KEY=dummy") {
		t.Error("CUSTOM_PROVIDER_KEY was removed after the extra names were cleared")
	}
}
