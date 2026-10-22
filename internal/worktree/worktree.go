// Package worktree creates, lists and removes the Git worktrees Ta-Da
// manages: one extra checkout of a repository per branch, so several tabs
// (or agents) can work on separate branches of the same project at once
// without stepping on each other's files.
//
// Managed worktrees live outside the repository so they never show up in
// its status or its file tree. By default a worktree for branch "feat/x" of
// the repository at /src/app is created at
//
//	/src/.tada-worktrees/app/feat-x
//
// [WithDir] moves the shared parent (/src/.tada-worktrees above) elsewhere;
// the repository name and branch are still appended to it.
//
// A worktree is "managed" when Ta-Da created it. That is recorded in a small
// marker file inside the worktree's administrative directory in the main
// repository (.git/worktrees/<id>/tada-worktree), which also remembers the
// branch it was created from. git deletes that directory, marker included,
// when the worktree is removed.
//
// Every git command runs with GIT_TERMINAL_PROMPT=0, so nothing ever blocks
// waiting for credentials.
package worktree

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// DirName is the directory, next to the repository, that holds the managed
// worktrees of every repository in that parent directory.
const DirName = ".tada-worktrees"

// markerFile is the file in a worktree's administrative directory that marks
// it as created by Ta-Da.
const markerFile = "tada-worktree"

var (
	// ErrNotRepo is returned when a directory is not inside a Git
	// repository.
	ErrNotRepo = errors.New("not a git repository")
	// ErrInvalidName is returned when a name has nothing left that can be
	// used as a branch name once sanitized.
	ErrInvalidName = errors.New("invalid worktree name")
	// ErrDirty is returned by [Remove] when the worktree has uncommitted
	// changes and force is false.
	ErrDirty = errors.New("worktree has uncommitted changes")
	// ErrMain is returned by [Remove] for the main checkout, which is never
	// removed.
	ErrMain = errors.New("cannot remove the main checkout")
)

// Worktree is one checkout of a repository.
type Worktree struct {
	// Path is the absolute path of the checkout.
	Path string `json:"path"`
	// Branch is the short name of the checked-out branch; empty when the
	// HEAD is detached.
	Branch string `json:"branch,omitempty"`
	// Head is the checked-out commit.
	Head string `json:"head,omitempty"`
	// Base is the ref a managed worktree's branch was created from. Ahead
	// and behind counts are measured against it. Empty when unknown.
	Base string `json:"base,omitempty"`
	// Repo is the root of the main checkout.
	Repo string `json:"repo,omitempty"`
	// Managed reports whether Ta-Da created this worktree.
	Managed bool `json:"managed,omitempty"`
	// Main reports whether this is the main checkout rather than a linked
	// worktree.
	Main bool `json:"main,omitempty"`
	// Detached reports whether HEAD is detached.
	Detached bool `json:"detached,omitempty"`
	// Locked reports whether the worktree is locked against pruning.
	Locked bool `json:"locked,omitempty"`
	// Prunable reports whether git considers the worktree stale, usually
	// because its directory was deleted by hand.
	Prunable bool `json:"prunable,omitempty"`
}

// Name is the short name shown to users: the branch, or the directory name
// when HEAD is detached.
func (w Worktree) Name() string {
	if w.Branch != "" {
		return w.Branch
	}
	return filepath.Base(w.Path)
}

// State is a summary of a worktree's state, as [Status] reports it.
type State struct {
	// Dirty is the number of changed, staged or untracked paths.
	Dirty int `json:"dirty"`
	// Ahead is the number of commits on HEAD that are not on Base.
	Ahead int `json:"ahead"`
	// Behind is the number of commits on Base that are not on HEAD.
	Behind int `json:"behind"`
	// Base is the ref Ahead and Behind are counted against; empty when the
	// worktree has no known base.
	Base string `json:"base,omitempty"`
}

// Option configures where managed worktrees are created and found.
type Option func(*options)

type options struct {
	dir string
}

// WithDir sets the directory that holds managed worktrees in place of the
// default "<parent of repo>/.tada-worktrees". The repository name and the
// branch are appended to it. A relative dir is resolved against the main
// checkout; an empty dir keeps the default.
func WithDir(dir string) Option {
	return func(o *options) { o.dir = dir }
}

func buildOptions(opts []Option) options {
	var o options
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	return o
}

// Location returns the directory that holds the managed worktrees of the
// repository whose main checkout is at repo.
func Location(repo string, opts ...Option) string {
	o := buildOptions(opts)
	parent := filepath.Join(filepath.Dir(repo), DirName)
	if o.dir != "" {
		parent = o.dir
		if !filepath.IsAbs(parent) {
			parent = filepath.Join(repo, parent)
		}
	}
	return filepath.Join(filepath.Clean(parent), filepath.Base(repo))
}

// Root returns the top level of the checkout that contains dir. In a linked
// worktree that is the worktree itself, not the main checkout.
func Root(ctx context.Context, dir string) (string, error) {
	out, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", notRepo(err)
	}
	if out == "" {
		return "", ErrNotRepo
	}
	return filepath.Clean(out), nil
}

// MainRoot returns the root of the main checkout of the repository that
// contains dir, which may be a linked worktree.
func MainRoot(ctx context.Context, dir string) (string, error) {
	list, err := List(ctx, dir)
	if err != nil {
		return "", err
	}
	if len(list) == 0 || !list[0].Main {
		return "", fmt.Errorf("%w: no main checkout", ErrNotRepo)
	}
	return list[0].Path, nil
}

// Sanitize turns name into a valid branch name: whitespace becomes "-",
// characters git does not allow in ref names are dropped, and leading or
// trailing separators and dots are trimmed from each path component. It
// returns "" when nothing usable is left.
func Sanitize(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		switch {
		case unicode.IsSpace(r):
			b.WriteByte('-')
		case r == '-' || r == '_' || r == '.' || r == '/':
			b.WriteRune(r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}
	var parts []string
	for part := range strings.SplitSeq(b.String(), "/") {
		for strings.Contains(part, "..") {
			part = strings.ReplaceAll(part, "..", ".")
		}
		for strings.Contains(part, "--") {
			part = strings.ReplaceAll(part, "--", "-")
		}
		part = strings.Trim(part, "-.")
		for strings.HasSuffix(part, ".lock") {
			part = strings.Trim(strings.TrimSuffix(part, ".lock"), "-.")
		}
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "/")
}

// dirFor is the directory name of a managed worktree for branch.
func dirFor(branch string) string {
	return strings.ReplaceAll(branch, "/", "-")
}

// Create makes a managed worktree for the branch name in the repository
// that contains repoDir and returns it.
//
// The name is sanitized first. When the branch does not exist it is created
// from base, or from the current branch of repoDir when base is empty. When
// the branch exists it is checked out as is and base only records what to
// compare it with. When the branch is already checked out in a worktree,
// that worktree is returned unchanged.
func Create(ctx context.Context, repoDir, name, base string, opts ...Option) (Worktree, error) {
	branch := Sanitize(name)
	if branch == "" {
		return Worktree{}, fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	if _, err := git(ctx, repoDir, "check-ref-format", "--branch", branch); err != nil {
		return Worktree{}, fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	list, err := List(ctx, repoDir, opts...)
	if err != nil {
		return Worktree{}, err
	}
	if len(list) == 0 || !list[0].Main {
		return Worktree{}, fmt.Errorf("%w: no main checkout", ErrNotRepo)
	}
	repo := list[0].Path
	for _, wt := range list {
		if wt.Branch == branch {
			return wt, nil
		}
	}

	base = strings.TrimSpace(base)
	if base == "" {
		base = currentRef(ctx, repoDir)
	}
	exists := refExists(ctx, repo, "refs/heads/"+branch)
	if !exists {
		if base == "" {
			return Worktree{}, errors.New("repository has no commits to branch from")
		}
		if !refExists(ctx, repo, base+"^{commit}") {
			return Worktree{}, fmt.Errorf("unknown base %q", base)
		}
	}

	path := filepath.Join(Location(repo, opts...), dirFor(branch))
	if entries, err := os.ReadDir(path); err == nil && len(entries) > 0 {
		return Worktree{}, fmt.Errorf("%s already exists and is not a worktree of %s", path, branch)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Worktree{}, fmt.Errorf("creating worktree directory: %w", err)
	}

	args := []string{"worktree", "add"}
	if exists {
		args = append(args, path, branch)
	} else {
		args = append(args, "--no-track", "-b", branch, path, base)
	}
	if _, err := git(ctx, repo, args...); err != nil {
		return Worktree{}, err
	}

	gitDir, err := git(ctx, path, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return Worktree{}, err
	}
	if err := writeMarker(gitDir, marker{Base: base, Created: time.Now().UTC()}); err != nil {
		return Worktree{}, err
	}

	list, err = List(ctx, repo, opts...)
	if err != nil {
		return Worktree{}, err
	}
	for _, wt := range list {
		if wt.Branch == branch {
			return wt, nil
		}
	}
	return Worktree{}, fmt.Errorf("created worktree for %s is missing from git worktree list", branch)
}

// List returns every checkout of the repository that contains repoDir, the
// main checkout first, as git worktree list reports them, with Managed and
// Base filled in for the worktrees Ta-Da created.
func List(ctx context.Context, repoDir string, opts ...Option) ([]Worktree, error) {
	out, err := git(ctx, repoDir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, notRepo(err)
	}
	list := parsePorcelain(out)
	if len(list) == 0 {
		return nil, nil
	}
	repo := ""
	if list[0].Main {
		repo = list[0].Path
	}
	managedDir := ""
	if repo != "" {
		managedDir = Location(repo, opts...)
	}
	for i := range list {
		list[i].Repo = repo
		if list[i].Main {
			continue
		}
		if m, ok := readMarker(gitDirOf(list[i].Path)); ok {
			list[i].Managed = true
			list[i].Base = m.Base
		} else if managedDir != "" && within(managedDir, list[i].Path) {
			list[i].Managed = true
		}
	}
	return list, nil
}

// parsePorcelain parses the output of git worktree list --porcelain. The
// first record is the main checkout unless the repository is bare, in
// which case no record is marked Main.
func parsePorcelain(out string) []Worktree {
	var (
		list  []Worktree
		cur   *Worktree
		bare  bool
		first = true
	)
	flush := func() {
		if cur == nil {
			return
		}
		if !bare {
			cur.Main = first
			list = append(list, *cur)
		}
		first = false
		cur, bare = nil, false
	}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			flush()
			continue
		}
		key, val, _ := strings.Cut(line, " ")
		if key == "worktree" {
			flush()
			cur = &Worktree{Path: filepath.Clean(val)}
			continue
		}
		if cur == nil {
			continue
		}
		switch key {
		case "HEAD":
			cur.Head = val
		case "branch":
			cur.Branch = strings.TrimPrefix(val, "refs/heads/")
		case "detached":
			cur.Detached = true
		case "bare":
			bare = true
		case "locked":
			cur.Locked = true
		case "prunable":
			cur.Prunable = true
		}
	}
	flush()
	return list
}

// Find returns the worktree in list whose branch or directory name is name.
func Find(list []Worktree, name string) (Worktree, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Worktree{}, false
	}
	clean := Sanitize(name)
	for _, wt := range list {
		if wt.Branch != "" && (wt.Branch == name || wt.Branch == clean) {
			return wt, true
		}
	}
	for _, wt := range list {
		if wt.Main {
			continue
		}
		if base := filepath.Base(wt.Path); base == name || (clean != "" && base == dirFor(clean)) {
			return wt, true
		}
	}
	return Worktree{}, false
}

// Remove deletes the linked worktree wt from disk and from the repository.
// The branch is kept. Unless force is true, a worktree with uncommitted or
// untracked changes is left alone and an error wrapping [ErrDirty] is
// returned.
func Remove(ctx context.Context, wt Worktree, force bool) error {
	if wt.Main {
		return ErrMain
	}
	repo := wt.Repo
	if repo == "" {
		root, err := MainRoot(ctx, wt.Path)
		if err != nil {
			return err
		}
		repo = root
	}
	if sameDir(repo, wt.Path) {
		return ErrMain
	}
	if !force {
		if _, err := os.Stat(wt.Path); err == nil {
			st, err := Status(ctx, wt)
			if err != nil {
				return err
			}
			if st.Dirty > 0 {
				return fmt.Errorf("%w: %d changed %s", ErrDirty, st.Dirty, plural(st.Dirty, "path", "paths"))
			}
		}
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, wt.Path)
	_, err := git(ctx, repo, args...)
	return err
}

// Status reports how many paths in wt have changed and, when its base is
// known, how far its HEAD is ahead of and behind that base.
func Status(ctx context.Context, wt Worktree) (State, error) {
	out, err := git(ctx, wt.Path, "status", "--porcelain")
	if err != nil {
		return State{}, err
	}
	st := State{Base: wt.Base}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.TrimSpace(line) != "" {
			st.Dirty++
		}
	}
	if st.Base == "" {
		if m, ok := readMarker(gitDirOf(wt.Path)); ok {
			st.Base = m.Base
		}
	}
	if st.Base == "" {
		return st, nil
	}
	counts, err := git(ctx, wt.Path, "rev-list", "--left-right", "--count", st.Base+"...HEAD")
	if err != nil {
		// The base may have been deleted or renamed since; the dirty count
		// is still worth reporting.
		return st, nil //nolint:nilerr
	}
	if f := strings.Fields(counts); len(f) == 2 {
		st.Behind, _ = strconv.Atoi(f[0])
		st.Ahead, _ = strconv.Atoi(f[1])
	}
	return st, nil
}

// Lookup reports whether dir is inside a linked worktree and describes it,
// reading only a few small files: it never runs git, so it is cheap enough
// to call on every prompt build. Locked and Prunable are not filled in. It
// returns false for main checkouts, submodules and directories outside any
// repository.
func Lookup(dir string) (Worktree, bool) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return Worktree{}, false
	}
	for {
		info, err := os.Stat(filepath.Join(dir, ".git"))
		if err == nil {
			if info.IsDir() {
				return Worktree{}, false
			}
			return lookupAt(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return Worktree{}, false
		}
		dir = parent
	}
}

func lookupAt(root string) (Worktree, bool) {
	gitDir := gitDirOf(root)
	if gitDir == "" || filepath.Base(filepath.Dir(gitDir)) != "worktrees" {
		return Worktree{}, false
	}
	wt := Worktree{Path: root}
	if head, err := os.ReadFile(filepath.Join(gitDir, "HEAD")); err == nil {
		ref := strings.TrimSpace(string(head))
		if name, ok := strings.CutPrefix(ref, "ref: refs/heads/"); ok {
			wt.Branch = name
		} else {
			wt.Detached = true
			wt.Head = ref
		}
	}
	common := filepath.Dir(filepath.Dir(gitDir))
	if b, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		c := strings.TrimSpace(string(b))
		if !filepath.IsAbs(c) {
			c = filepath.Join(gitDir, c)
		}
		common = filepath.Clean(c)
	}
	if filepath.Base(common) == ".git" {
		wt.Repo = filepath.Dir(common)
	}
	if m, ok := readMarker(gitDir); ok {
		wt.Managed = true
		wt.Base = m.Base
	}
	return wt, true
}

// gitDirOf resolves the "gitdir:" pointer in root/.git, which names the
// administrative directory of a linked worktree. It returns "" when root
// has no such file.
func gitDirOf(root string) string {
	b, err := os.ReadFile(filepath.Join(root, ".git"))
	if err != nil {
		return ""
	}
	dir, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
	if !ok {
		return ""
	}
	dir = strings.TrimSpace(dir)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	return filepath.Clean(dir)
}

// marker is the content of the file that marks a managed worktree.
type marker struct {
	Base    string    `json:"base,omitempty"`
	Created time.Time `json:"created"`
}

func writeMarker(gitDir string, m marker) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(gitDir, markerFile), append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("marking worktree as managed: %w", err)
	}
	return nil
}

func readMarker(gitDir string) (marker, bool) {
	if gitDir == "" {
		return marker{}, false
	}
	b, err := os.ReadFile(filepath.Join(gitDir, markerFile))
	if err != nil {
		return marker{}, false
	}
	var m marker
	// An unreadable marker still marks the worktree as managed.
	_ = json.Unmarshal(b, &m)
	return m, true
}

// currentRef returns the branch checked out in dir, or the commit when HEAD
// is detached, or "" when the repository has no commits.
func currentRef(ctx context.Context, dir string) string {
	if b, err := git(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil && b != "" {
		if refExists(ctx, dir, "refs/heads/"+b) {
			return b
		}
		return ""
	}
	if h, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", "HEAD"); err == nil {
		return h
	}
	return ""
}

func refExists(ctx context.Context, dir, ref string) bool {
	_, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", ref)
	return err == nil
}

// git runs git in dir and returns its trimmed standard output. A failure
// carries git's standard error.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		// Status polling must not take the index lock away from the user's
		// own git commands.
		"GIT_OPTIONAL_LOCKS=0",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		msg = strings.TrimPrefix(msg, "fatal: ")
		return "", &gitError{args: args, msg: msg, err: err}
	}
	return strings.TrimSpace(stdout.String()), nil
}

// gitError is a failed git command with its standard error.
type gitError struct {
	args []string
	msg  string
	err  error
}

func (e *gitError) Error() string {
	sub := "git"
	if len(e.args) > 0 {
		sub = "git " + e.args[0]
	}
	return sub + ": " + e.msg
}

func (e *gitError) Unwrap() error { return e.err }

// notRepo turns git's "not a git repository" failure into [ErrNotRepo].
func notRepo(err error) error {
	var ge *gitError
	if errors.As(err, &ge) && strings.Contains(strings.ToLower(ge.msg), "not a git repository") {
		return ErrNotRepo
	}
	return err
}

// Contains reports whether path is dir or inside it, after resolving
// symlinks.
func Contains(dir, path string) bool {
	return within(dir, path)
}

// within reports whether path is dir or inside it.
func within(dir, path string) bool {
	rel, err := filepath.Rel(canonical(dir), canonical(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func sameDir(a, b string) bool {
	return canonical(a) == canonical(b)
}

// canonical resolves symlinks where it can, so /tmp and /private/tmp on
// macOS compare equal.
func canonical(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	// The path may not exist yet; resolve its closest existing parent.
	parent, base := filepath.Dir(p), filepath.Base(p)
	if parent == p {
		return filepath.Clean(p)
	}
	return filepath.Join(canonical(parent), base)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
