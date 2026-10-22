package swarm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// safeConfig keeps git from running programs it would otherwise start on
// its own: hook scripts and the file system monitor. The agent may have
// written to the repository's hooks through an approved command;
// committing or merging its work must not run them.
var safeConfig = []string{
	"-c", "core.quotepath=off",
	"-c", "core.fsmonitor=false",
	"-c", "core.hooksPath=" + os.DevNull,
}

// noExternalDiff keeps git diff from running the diff and textconv
// programs the config names.
var noExternalDiff = []string{"--no-ext-diff", "--no-textconv"}

// errConfigChanged means the git config or attributes changed since the
// swarm fingerprinted them.
var errConfigChanged = errors.New("the git config changed since the swarm started")

// configHash fingerprints what decides which programs git runs in dir:
// every config entry it reads, with where it came from, and the
// repository's info/attributes. Filter, diff and merge drivers live
// there, and an agent's approved command could add one, so the swarm
// compares this with the fingerprint from before the agent ran before
// running git on its work. Branch settings are left out because merging
// and deleting branches changes them.
func configHash(ctx context.Context, dir string) (string, error) {
	out, err := gitRaw(ctx, dir, "config", "--list", "--show-origin", "-z")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	// Each entry is the origin, then the key and value, each ending in a
	// NUL.
	parts := strings.Split(out, "\x00")
	for i := 0; i+1 < len(parts); i += 2 {
		origin, entry := parts[i], parts[i+1]
		key, _, _ := strings.Cut(entry, "\n")
		if strings.HasPrefix(strings.ToLower(key), "branch.") {
			continue
		}
		fmt.Fprintf(h, "%q %q\n", origin, entry)
	}

	common, err := git(ctx, dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	attrs, err := os.ReadFile(filepath.Join(common, "info", "attributes"))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		h.Write([]byte("no attributes"))
	case err != nil:
		return "", err
	default:
		fmt.Fprintf(h, "attributes %q", attrs)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// verifyConfig returns errConfigChanged unless dir's config fingerprint
// is still want. An empty want, from a swarm that never fingerprinted
// its config, can't be trusted either.
func verifyConfig(ctx context.Context, dir, want string) error {
	if want == "" {
		return errConfigChanged
	}
	got, err := configHash(ctx, dir)
	if err != nil {
		return fmt.Errorf("fingerprint the git config: %w", err)
	}
	if got != want {
		return errConfigChanged
	}
	return nil
}

// git runs git in dir and returns its trimmed standard output. A failure
// carries git's standard error.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := gitRaw(ctx, dir, args...)
	return strings.TrimSpace(out), err
}

// gitRaw is git without trimming, for file contents.
func gitRaw(ctx context.Context, dir string, args ...string) (string, error) {
	full := append(append([]string{}, safeConfig...), args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_EDITOR=true",
		"GIT_MERGE_AUTOEDIT=no",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		msg = strings.TrimPrefix(msg, "fatal: ")
		sub := "git"
		if len(args) > 0 {
			sub = "git " + args[0]
		}
		return "", fmt.Errorf("%s: %s", sub, msg)
	}
	return stdout.String(), nil
}

// headCommit returns the commit HEAD points at in dir.
func headCommit(ctx context.Context, dir string) (string, error) {
	return git(ctx, dir, "rev-parse", "--verify", "HEAD^{commit}")
}

// currentBranch returns the branch checked out in dir, or an error when
// HEAD is detached.
func currentBranch(ctx context.Context, dir string) (string, error) {
	out, err := git(ctx, dir, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil || out == "" {
		return "", errors.New("HEAD is detached; check out a branch first")
	}
	return out, nil
}

// branchExists reports whether the local branch exists in dir's
// repository.
func branchExists(ctx context.Context, dir, branch string) bool {
	_, err := git(ctx, dir, "rev-parse", "--verify", "-q", "refs/heads/"+branch)
	return err == nil
}

// dirtyPaths returns the tracked paths in dir with uncommitted changes.
func dirtyPaths(ctx context.Context, dir string) ([]string, error) {
	out, err := git(ctx, dir, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// hasIdentity reports whether git knows who commits in dir.
func hasIdentity(ctx context.Context, dir string) bool {
	name, _ := git(ctx, dir, "config", "user.name")
	email, _ := git(ctx, dir, "config", "user.email")
	return name != "" && email != ""
}

// commitAll commits every change in the worktree dir and returns the new
// HEAD. With nothing to commit it returns HEAD unchanged and false.
func commitAll(ctx context.Context, dir, message string) (string, bool, error) {
	if _, err := git(ctx, dir, "add", "-A"); err != nil {
		return "", false, err
	}
	staged, err := git(ctx, dir, "diff", "--cached", "--name-only", "--no-ext-diff", "--no-textconv")
	if err != nil {
		return "", false, err
	}
	if staged == "" {
		head, err := headCommit(ctx, dir)
		return head, false, err
	}
	var args []string
	// The swarm commits on a scratch branch with no one at the keyboard
	// to unlock a signing key.
	args = append(args, "-c", "commit.gpgsign=false")
	if !hasIdentity(ctx, dir) {
		args = append(args, "-c", "user.name=Ta-Da swarm", "-c", "user.email=swarm@tada.invalid")
	}
	args = append(args, "commit", "--no-verify", "-q", "-m", message)
	if _, err := git(ctx, dir, args...); err != nil {
		return "", false, err
	}
	head, err := headCommit(ctx, dir)
	return head, true, err
}

// numstat sums the lines added and removed between two commits and
// lists the files that changed.
func numstat(ctx context.Context, dir, from, to string) (added, removed int, files []string, err error) {
	out, err := git(ctx, dir, append(append([]string{"diff", "--numstat", "--no-renames"}, noExternalDiff...), from, to, "--")...)
	if err != nil {
		return 0, 0, nil, err
	}
	for line := range strings.SplitSeq(out, "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		a, _ := strconv.Atoi(parts[0])
		r, _ := strconv.Atoi(parts[1])
		added += a
		removed += r
		files = append(files, parts[2])
	}
	return added, removed, files, nil
}

// mergeInProgress reports whether dir has a merge waiting to be
// concluded or aborted.
func mergeInProgress(ctx context.Context, dir string) bool {
	_, err := git(ctx, dir, "rev-parse", "-q", "--verify", "MERGE_HEAD")
	return err == nil
}

// conflictedPaths lists the paths a stopped merge left unmerged.
func conflictedPaths(ctx context.Context, dir string) []string {
	out, err := git(ctx, dir, "diff", "--name-only", "--diff-filter=U", "--no-ext-diff", "--no-textconv")
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}
