// Package gitreview reads the changes in a Git working tree and commits a
// chosen set of them. It backs the review and commit screen.
//
// Every call shells out to the git binary with prompts disabled, so it
// never blocks on credentials, and paths are taken literally, so a file
// called "*.go" is just that file. Paths in and out are relative to the
// repository root, whatever folder inside the repository dir names.
package gitreview

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// emptyTree is the ID of Git's empty tree. Diffing against it stands in
// for HEAD in a repository that has no commits yet.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// maxCountSize is the largest untracked file whose lines are counted.
const maxCountSize = 4 << 20

var (
	// ErrNotRepo is returned when the folder is not inside a Git work
	// tree.
	ErrNotRepo = errors.New("not a git repository")
	// ErrNoPaths is returned by Commit when no file was chosen.
	ErrNoPaths = errors.New("no files selected")
	// ErrEmptyMessage is returned by Commit when the message is blank.
	ErrEmptyMessage = errors.New("commit message is empty")
)

// File is one changed path in the working tree.
type File struct {
	// Path is the file's path relative to the repository root.
	Path string
	// OrigPath is the path the file was renamed or copied from, if any.
	OrigPath string
	// X and Y are the index and work tree status letters from git
	// status ('.' for unchanged, '?' for untracked).
	X, Y byte
	// Staged reports whether the index differs from HEAD.
	Staged bool
	// Unstaged reports whether the work tree differs from the index.
	Unstaged bool
	// Untracked reports whether Git does not know the file yet.
	Untracked bool
	// Conflicted reports an unmerged path.
	Conflicted bool
	// Added and Removed are line counts against HEAD.
	Added, Removed int
	// Binary reports a file whose lines Git does not count.
	Binary bool
}

// Deleted reports whether the file is gone from the work tree or index.
func (f File) Deleted() bool {
	return f.X == 'D' || f.Y == 'D'
}

// Code is a one letter summary of the change: A, M, D, R, C, U or ?.
func (f File) Code() string {
	switch {
	case f.Untracked:
		return "?"
	case f.Conflicted:
		return "U"
	case f.X == 'R' || f.Y == 'R':
		return "R"
	case f.X == 'C' || f.Y == 'C':
		return "C"
	case f.Deleted():
		return "D"
	case f.X == 'A' || f.Y == 'A':
		return "A"
	default:
		return "M"
	}
}

// Paths returns the paths a commit of f must include: the file itself and,
// for a rename, the path it came from.
func (f File) Paths() []string {
	if f.OrigPath != "" && f.OrigPath != f.Path {
		return []string{f.OrigPath, f.Path}
	}
	return []string{f.Path}
}

// Counts holds the line changes for one path.
type Counts struct {
	Added, Removed int
	Binary         bool
}

// Repo describes the repository a folder belongs to.
type Repo struct {
	// Root is the absolute path of the work tree's top folder.
	Root string
	// HasHEAD is false in a repository with no commits yet.
	HasHEAD bool
	// Branch is the checked out branch, empty when detached.
	Branch string
}

// Result is what Status reports.
type Result struct {
	Repo
	Files []File
}

// Open finds the repository dir belongs to. It returns ErrNotRepo when dir
// is outside any work tree.
func Open(ctx context.Context, dir string) (Repo, error) {
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return Repo{}, ErrNotRepo
	}
	out, err := run(ctx, dir, nil, "rev-parse", "--is-inside-work-tree", "--show-toplevel")
	if err != nil {
		var ge *gitError
		if errors.As(err, &ge) {
			// Outside a repository, or inside a .git folder or a bare
			// repository, which have no work tree to review.
			return Repo{}, ErrNotRepo
		}
		return Repo{}, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "true" {
		return Repo{}, ErrNotRepo
	}
	repo := Repo{Root: strings.TrimSpace(lines[1])}
	if _, err := run(ctx, repo.Root, nil, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err == nil {
		repo.HasHEAD = true
	}
	if b, err := run(ctx, repo.Root, nil, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		repo.Branch = strings.TrimSpace(b)
	}
	return repo, nil
}

// Status lists every changed, staged and untracked file in the repository
// dir belongs to, with line counts against HEAD.
func Status(ctx context.Context, dir string) (Result, error) {
	repo, err := Open(ctx, dir)
	if err != nil {
		return Result{}, err
	}
	out, err := run(ctx, repo.Root, nil, "status", "--porcelain=v2", "-z", "--untracked-files=all", "--no-renames")
	if err != nil {
		return Result{}, err
	}
	files := parseStatus(out)

	counts, err := numstat(ctx, repo)
	if err != nil {
		return Result{}, err
	}
	for i := range files {
		f := &files[i]
		if f.Untracked {
			c := countFile(filepath.Join(repo.Root, filepath.FromSlash(f.Path)))
			f.Added, f.Binary = c.Added, c.Binary
			continue
		}
		if c, ok := counts[f.Path]; ok {
			f.Added, f.Removed, f.Binary = c.Added, c.Removed, c.Binary
		}
	}
	return Result{Repo: repo, Files: files}, nil
}

// Numstat returns the line changes of every tracked file against HEAD,
// staged and unstaged together, keyed by root relative path.
func Numstat(ctx context.Context, dir string) (map[string]Counts, error) {
	repo, err := Open(ctx, dir)
	if err != nil {
		return nil, err
	}
	return numstat(ctx, repo)
}

func numstat(ctx context.Context, repo Repo) (map[string]Counts, error) {
	out, err := run(ctx, repo.Root, nil, "diff", "--numstat", "-z", "--no-renames", "--no-ext-diff", "--no-textconv", base(repo), "--")
	if err != nil {
		return nil, err
	}
	return parseNumstat(out), nil
}

// FileAtHEAD returns the content of the root relative path at HEAD. A path
// that does not exist there, or a repository without commits, yields an
// empty string and no error.
func FileAtHEAD(ctx context.Context, dir, path string) (string, error) {
	repo, err := Open(ctx, dir)
	if err != nil {
		return "", err
	}
	if !repo.HasHEAD {
		return "", nil
	}
	if _, err := run(ctx, repo.Root, nil, "cat-file", "-e", "HEAD:"+path); err != nil {
		return "", nil //nolint:nilerr // A path missing at HEAD is a new file.
	}
	return run(ctx, repo.Root, nil, "show", "--no-textconv", "HEAD:"+path)
}

// FileInWorkTree returns the content of the root relative path on disk. A
// missing file yields an empty string and no error.
func FileInWorkTree(root, path string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Diff returns a unified diff of files against HEAD, the way they would be
// committed: tracked files with staged and unstaged edits together, and
// untracked files as new files.
func Diff(ctx context.Context, dir string, files []File) (string, error) {
	repo, err := Open(ctx, dir)
	if err != nil {
		return "", err
	}
	var tracked, untracked []string
	for _, f := range files {
		if f.Untracked {
			untracked = append(untracked, f.Path)
			continue
		}
		tracked = append(tracked, f.Paths()...)
	}

	var b strings.Builder
	if len(tracked) > 0 {
		args := append([]string{"diff", "--no-color", "--no-ext-diff", "--no-textconv", base(repo), "--"}, tracked...)
		out, err := run(ctx, repo.Root, nil, args...)
		if err != nil {
			return "", err
		}
		b.WriteString(out)
	}
	for _, p := range untracked {
		// With --no-index, exit status 1 just means the files differ.
		out, err := run(ctx, repo.Root, nil, "diff", "--no-index", "--no-color", "--no-ext-diff", "--", os.DevNull, p)
		var ge *gitError
		if err != nil && (!errors.As(err, &ge) || ge.code != 1) {
			return "", err
		}
		b.WriteString(out)
	}
	return b.String(), nil
}

// Log returns the last n commits, one line each. A repository with no
// commits yields an empty string.
func Log(ctx context.Context, dir string, n int) (string, error) {
	repo, err := Open(ctx, dir)
	if err != nil {
		return "", err
	}
	if !repo.HasHEAD {
		return "", nil
	}
	return run(ctx, repo.Root, nil, "log", "--oneline", "--no-decorate", "--no-color", "-n", strconv.Itoa(n))
}

// Commit stages paths (root relative, deletions and new files included)
// and commits exactly those paths with msg, leaving anything else that was
// staged where it was. It returns the new commit's short hash. Git hooks
// run as usual; when one fails, the error carries its output. Commit never
// pushes.
func Commit(ctx context.Context, dir string, paths []string, msg string) (string, error) {
	if len(paths) == 0 {
		return "", ErrNoPaths
	}
	if strings.TrimSpace(msg) == "" {
		return "", ErrEmptyMessage
	}
	repo, err := Open(ctx, dir)
	if err != nil {
		return "", err
	}
	add := append([]string{"add", "-A", "--"}, paths...)
	if _, err := run(ctx, repo.Root, nil, add...); err != nil {
		return "", err
	}
	commit := append([]string{"commit", "--cleanup=strip", "-F", "-", "--"}, paths...)
	if _, err := run(ctx, repo.Root, strings.NewReader(msg), commit...); err != nil {
		return "", err
	}
	out, err := run(ctx, repo.Root, nil, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// TruncateDiff cuts diff to at most limit bytes on a line boundary and
// notes how much was left out.
func TruncateDiff(diff string, limit int) string {
	if limit <= 0 || len(diff) <= limit {
		return diff
	}
	cut := diff[:limit]
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		cut = cut[:i+1]
	}
	for cut != "" && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return fmt.Sprintf("%s\n[diff truncated: showing %d of %d bytes]\n", cut, len(cut), len(diff))
}

func base(repo Repo) string {
	if repo.HasHEAD {
		return "HEAD"
	}
	return emptyTree
}

// statusFields is how many space separated fields each porcelain v2 record
// type has, the path being the last.
var statusFields = map[byte]int{'1': 9, '2': 10, 'u': 11}

// parseStatus parses `git status --porcelain=v2 -z` output.
//
// The records look like this, with the rename's original path in the NUL
// separated field that follows it:
//
//	1 XY sub mH mI mW hH hI path
//	2 XY sub mH mI mW hH hI Xscore path
//	u XY sub m1 m2 m3 mW h1 h2 h3 path
//	? path
func parseStatus(out string) []File {
	var files []File
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		rec := fields[i]
		if rec == "" {
			continue
		}
		switch rec[0] {
		case '1', '2', 'u':
			n := statusFields[rec[0]]
			parts := strings.SplitN(rec, " ", n)
			if len(parts) < n || len(parts[1]) != 2 {
				continue
			}
			f := File{Path: parts[n-1], X: parts[1][0], Y: parts[1][1]}
			if rec[0] == '2' && i+1 < len(fields) {
				i++
				f.OrigPath = fields[i]
			}
			f.Conflicted = rec[0] == 'u'
			f.Staged = f.X != '.' && !f.Conflicted
			f.Unstaged = f.Y != '.' && !f.Conflicted
			files = append(files, f)
		case '?':
			if len(rec) > 2 {
				files = append(files, File{Path: rec[2:], X: '?', Y: '?', Untracked: true, Unstaged: true})
			}
		}
	}
	return files
}

// parseNumstat parses `git diff --numstat -z` output.
func parseNumstat(out string) map[string]Counts {
	counts := map[string]Counts{}
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		parts := strings.SplitN(fields[i], "\t", 3)
		if len(parts) != 3 {
			continue
		}
		path := parts[2]
		if path == "" && i+2 < len(fields) {
			// A rename: the old and new paths follow as their own fields.
			path = fields[i+2]
			i += 2
		}
		var c Counts
		if parts[0] == "-" || parts[1] == "-" {
			c.Binary = true
		} else {
			c.Added, _ = strconv.Atoi(parts[0])
			c.Removed, _ = strconv.Atoi(parts[1])
		}
		counts[path] = c
	}
	return counts
}

// countFile counts the lines of an untracked file.
func countFile(path string) Counts {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxCountSize {
		return Counts{}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Counts{}
	}
	if bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0 {
		return Counts{Binary: true}
	}
	n := bytes.Count(b, []byte{'\n'})
	if len(b) > 0 && b[len(b)-1] != '\n' {
		n++
	}
	return Counts{Added: n}
}

// gitError is a failed git command with what it printed.
type gitError struct {
	args   []string
	code   int
	stderr string
	stdout string
}

func (e *gitError) Error() string {
	msg := strings.TrimSpace(e.stderr)
	if msg == "" {
		msg = strings.TrimSpace(e.stdout)
	}
	if msg == "" {
		msg = fmt.Sprintf("exit status %d", e.code)
	}
	return fmt.Sprintf("git %s: %s", e.args[0], msg)
}

// run runs git in dir and returns its standard output.
func run(ctx context.Context, dir string, stdin io.Reader, args ...string) (string, error) {
	full := append([]string{"-c", "core.quotepath=off", "--literal-pathspecs"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		// Status would otherwise refresh the index and could race the
		// agent's own git commands for the lock.
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_EDITOR=true",
	)
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return stdout.String(), &gitError{args: args, code: ee.ExitCode(), stderr: stderr.String(), stdout: stdout.String()}
		}
		return "", fmt.Errorf("run git %s: %w", args[0], err)
	}
	return stdout.String(), nil
}
