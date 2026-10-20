// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): features/magic-context/memory/project-identity.ts.

// Project identity, ported from cortexkit/magic-context (MIT)
// memory/project-identity.ts.

package magiccontext

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	projectKeyMu    sync.Mutex
	projectKeyCache = map[string]string{}
	rootCommitRe    = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
)

// ProjectKey returns the stable memory identity of workingDir:
// "git:<root commit sha>" (the lexicographically smallest root commit, so
// clones and linked worktrees share memories) or, outside git or on an unborn
// HEAD, "dir:<first 12 hex of md5(abs path)>". The unborn case is not cached
// so the key flips to git: after the first commit. The home directory itself
// is refused and yields "" (memory unavailable), like upstream without
// allow_home_project.
func ProjectKey(workingDir string) string {
	abs, err := filepath.Abs(workingDir)
	if err != nil || workingDir == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	if home, err := os.UserHomeDir(); err == nil {
		if h, err := filepath.EvalSymlinks(home); err == nil {
			home = h
		}
		if filepath.Clean(home) == filepath.Clean(abs) {
			return ""
		}
	}
	projectKeyMu.Lock()
	if k, ok := projectKeyCache[abs]; ok {
		projectKeyMu.Unlock()
		return k
	}
	projectKeyMu.Unlock()

	if sha := gitRootCommit(abs); sha != "" {
		key := "git:" + sha
		projectKeyMu.Lock()
		projectKeyCache[abs] = key
		projectKeyMu.Unlock()
		return key
	}
	sum := md5.Sum([]byte(abs))
	key := "dir:" + hex.EncodeToString(sum[:])[:12]
	if !isGitWorkTree(abs) { // unborn HEAD stays uncached
		projectKeyMu.Lock()
		projectKeyCache[abs] = key
		projectKeyMu.Unlock()
	}
	return key
}

func gitRootCommit(dir string) string {
	out, err := runGit(dir, 5*time.Second, "rev-list", "--max-parents=0", "HEAD")
	if err != nil {
		return ""
	}
	var shas []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if len(line) > 64 {
			line = line[:64]
		}
		if rootCommitRe.MatchString(line) {
			shas = append(shas, line)
		}
	}
	if len(shas) == 0 {
		return ""
	}
	sort.Strings(shas)
	return shas[0]
}

func isGitWorkTree(dir string) bool {
	out, err := runGit(dir, 5*time.Second, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

// runGit runs a local git command (never a network operation).
func runGit(dir string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LC_ALL=C", "GIT_TERMINAL_PROMPT=0")
	b, err := cmd.Output()
	return string(b), err
}
