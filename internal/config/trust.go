package config

// Project trust gate.
//
// Project-level config can run commands on the user's machine: tadarc is
// executed as a shell script, tada.json values are shell-expanded, and both
// can start MCP and LSP servers, install hooks, auto-approve tools or point a
// provider at another endpoint. Opening Ta-Da in a directory therefore must
// not apply that directory's config until the user has reviewed it, in the
// spirit of `direnv allow`.
//
// Project-level config is every file the project walk finds between the
// working directory and the project root (.tadarc, tadarc, .tada.json,
// tada.json), the workspace config (<data dir>/tada.json, by default
// .tada/tada.json), and anything a registered ProjectConfigFinder
// reports. System and global config (/etc/tada, ~/.config/tada, the global
// data tada.json) is never gated.
//
// A project is trusted when any of the following holds; otherwise all of its
// project-level config is skipped and the user is told to run `tada trust`:
//
//   - It has no project-level config at all (nothing to gate).
//   - TADA_TRUST_PROJECT is set to a true value (one invocation only).
//   - Its root lies under one of the options.trusted_paths entries of the
//     global or system config. Project files cannot influence this: the
//     decision is made before any of them is read.
//   - The trust store (trusted_projects.json next to the global data config)
//     holds a record for its root whose fingerprint still matches. The
//     fingerprint is a sha256 over the relative path and content of every
//     project-level file; a file that changes, appears or disappears breaks
//     the match, and the project is untrusted again.
//
// Ta-Da keeps its own writes from untrusting a project. When it writes the
// workspace config of a project that was trusted by record just before the
// write, the record is refreshed to the new content. When the project was not
// trusted by record and the workspace config did not exist before the write,
// the file holds nothing but content Ta-Da wrote itself, so it is recorded as
// a trust entry covering just that file; any other project-level file (a
// committed tada.json, or a workspace config that existed before) still
// makes the project untrusted until the user runs `tada trust`.
//
// Integration: loaders of other project-level config formats should register
// a ProjectConfigFinder so their files are fingerprinted by every trust
// decision (startup, reload, self-writes and `tada trust`), and should only
// read those files when ConfigStore.ProjectTrust reports Trusted.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/filepathext"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/home"
	"github.com/charmbracelet/crush/internal/lock"
)

// TrustProjectEnv names the environment variable that trusts the current
// project's config for a single invocation when set to a true value.
const TrustProjectEnv = "TADA_TRUST_PROJECT"

const (
	trustStoreFile        = "trusted_projects.json"
	trustStoreVersion     = 1
	trustStoreLockTimeout = 5 * time.Second
)

// TrustSource explains a trust decision.
type TrustSource string

const (
	// TrustSourceUntrusted means the project has project-level config that
	// must not be loaded.
	TrustSourceUntrusted TrustSource = "untrusted"
	// TrustSourceNoConfig means the project has no project-level config.
	TrustSourceNoConfig TrustSource = "no_config"
	// TrustSourceEnv means TADA_TRUST_PROJECT trusted the project.
	TrustSourceEnv TrustSource = "env"
	// TrustSourceAllowlist means options.trusted_paths covers the project.
	TrustSourceAllowlist TrustSource = "trusted_paths"
	// TrustSourceRecord means a matching trust record exists.
	TrustSourceRecord TrustSource = "record"
)

// ProjectTrust is the outcome of the trust gate for one project.
type ProjectTrust struct {
	// Root is the absolute, symlink-resolved project root (the git working
	// tree root, or the working directory outside of git).
	Root string
	// Files lists the existing project-level config files the decision
	// covers, as absolute paths.
	Files []string
	// Trusted reports whether project-level config may be loaded.
	Trusted bool
	// Source explains the decision.
	Source TrustSource
	// Fingerprint is the fingerprint the decision compared, if any.
	Fingerprint string

	// workspaceFile is the workspace config path the decision assumed.
	workspaceFile string
	// verified holds the files whose content a record-based decision
	// verified. Only those may be loaded.
	verified map[string]bool
}

// Allows reports whether the project-level config file at path may be
// loaded under this decision. Fingerprint-based trust only extends to the
// files whose content was verified; a file that appeared afterwards, or
// that the decision did not cover, is refused.
func (t ProjectTrust) Allows(path string) bool {
	if !t.Trusted {
		return false
	}
	switch t.Source {
	case TrustSourceEnv, TrustSourceAllowlist:
		return true
	}
	return t.verified[normalizeConfigPath(path)]
}

// DisplayFiles returns Files relative to Root where possible, for messages.
func (t ProjectTrust) DisplayFiles() []string {
	out := make([]string, 0, len(t.Files))
	for _, f := range t.Files {
		out = append(out, t.displayPath(f))
	}
	return out
}

func (t ProjectTrust) displayPath(path string) string {
	if rel, ok := relWithin(t.Root, path); ok {
		return rel
	}
	return home.Short(path)
}

// Notice returns the user-facing warning for an untrusted project, or the
// empty string when nothing was skipped.
func (t ProjectTrust) Notice() string {
	if t.Trusted || len(t.Files) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"Ignoring untrusted project config (%s) in %s. Review it, then run `tada trust`.",
		strings.Join(t.DisplayFiles(), ", "),
		home.Short(t.Root),
	)
}

// ProjectConfigFinder reports additional project-level config files for a
// working directory, given the project root. Returned paths may point at
// files that do not exist yet; they are fingerprinted once they appear.
type ProjectConfigFinder func(workingDir, projectRoot string) []string

var (
	projectConfigFindersMu sync.RWMutex
	projectConfigFinders   []ProjectConfigFinder
)

// RegisterProjectConfigFinder adds f to the finders consulted by every trust
// decision. Use it for project-level config formats that are loaded outside
// the tada.json/tadarc pipeline so their files are gated by the same
// fingerprint.
func RegisterProjectConfigFinder(f ProjectConfigFinder) {
	projectConfigFindersMu.Lock()
	defer projectConfigFindersMu.Unlock()
	projectConfigFinders = append(projectConfigFinders, f)
}

func registeredProjectConfigFiles(workingDir, root string) []string {
	projectConfigFindersMu.RLock()
	finders := slices.Clone(projectConfigFinders)
	projectConfigFindersMu.RUnlock()
	var files []string
	for _, f := range finders {
		files = append(files, f(workingDir, root)...)
	}
	return files
}

// ProjectRoot returns the absolute, symlink-resolved root of the project
// containing workingDir: the git working tree root, or workingDir itself
// outside of git. Trust records are keyed by it.
func ProjectRoot(workingDir string) string {
	return resolvePath(projectBoundary(workingDir))
}

// ProjectFingerprint returns the sha256 fingerprint of the given project
// config files. Each existing file contributes its path relative to root
// (or its absolute path when it lies outside root) and a digest of its
// content; missing files are skipped, so a file appearing or disappearing
// changes the fingerprint just like a content change does.
func ProjectFingerprint(root string, files ...string) string {
	root = resolvePath(root)
	entries := make(map[string]string, len(files))
	for _, f := range files {
		if f == "" {
			continue
		}
		p := normalizeConfigPath(f)
		digest, ok := fileDigest(p)
		if !ok {
			continue
		}
		entries[trustRecordKey(root, p)] = digest
	}
	h := sha256.New()
	for _, k := range slices.Sorted(maps.Keys(entries)) {
		fmt.Fprintf(h, "%s\x00%s\n", k, entries[k])
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// fileDigest hashes a config file's content. ok is false when the file does
// not exist. Anything but a regular file is never opened (opening a FIFO
// blocks) and hashes to a marker, as do read errors, so it can never match
// the fingerprint of real content.
func fileDigest(path string) (digest string, ok bool) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false
	}
	if err != nil {
		return "unreadable", true
	}
	if !info.Mode().IsRegular() {
		return "irregular:" + info.Mode().Type().String(), true
	}
	f, err := os.Open(path)
	if err != nil {
		return "unreadable", true
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "unreadable", true
	}
	return hex.EncodeToString(h.Sum(nil)), true
}

func configFileExists(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, fs.ErrNotExist)
}

// trustInput holds everything a trust decision depends on. All of it is
// derived from the working directory and global config, never from project
// config.
type trustInput struct {
	workingDir    string
	projectFiles  []string
	workspaceFile string
	trustedPaths  []string
	extraFiles    []string
}

// candidates returns the normalized, deduplicated paths a decision covers.
// Missing files are kept so the record can notice them appearing.
func (in trustInput) candidates(root string) []string {
	paths := slices.Concat(
		in.projectFiles,
		[]string{in.workspaceFile},
		registeredProjectConfigFiles(in.workingDir, root),
		in.extraFiles,
	)
	return normalizePaths(paths)
}

func evaluateProjectTrust(in trustInput) ProjectTrust {
	root := ProjectRoot(in.workingDir)
	candidates := in.candidates(root)
	t := ProjectTrust{
		Root:          root,
		Files:         existingPaths(candidates),
		Source:        TrustSourceUntrusted,
		workspaceFile: in.workspaceFile,
	}

	switch {
	case len(t.Files) == 0:
		t.Trusted, t.Source = true, TrustSourceNoConfig
		return t
	case trustedByEnv():
		t.Trusted, t.Source = true, TrustSourceEnv
	case trustedByAllowlist(root, in.trustedPaths):
		t.Trusted, t.Source = true, TrustSourceAllowlist
	}
	if t.Trusted {
		t.Fingerprint = ProjectFingerprint(root, t.Files...)
		return t
	}

	store, err := readTrustStore()
	if err != nil {
		slog.Warn("Failed to read the project trust store; treating the project as untrusted", "error", err)
		return t
	}
	rec, ok := store.Projects[root]
	if !ok {
		return t
	}
	// Include the files the record covers, even when this working directory
	// would not discover them, so trusting from one subdirectory of a
	// project does not untrust it from another.
	covered := normalizePaths(append(candidates, rec.paths(root)...))
	t.Fingerprint = ProjectFingerprint(root, covered...)
	if t.Fingerprint != rec.Fingerprint {
		return t
	}
	t.Trusted, t.Source = true, TrustSourceRecord
	t.verified = make(map[string]bool)
	for _, p := range existingPaths(covered) {
		t.verified[p] = true
	}
	return t
}

func trustedByEnv() bool {
	v, err := strconv.ParseBool(os.Getenv(TrustProjectEnv))
	return err == nil && v
}

// trustedByAllowlist reports whether root lies at or below one of the
// options.trusted_paths entries. Entries are ~-expanded and symlink
// resolved; relative entries are ignored because they have no stable
// meaning. Matching is on whole path components, so /a/b does not cover
// /a/bc.
func trustedByAllowlist(root string, allowlist []string) bool {
	for _, entry := range allowlist {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		p := home.Long(entry)
		if !filepath.IsAbs(p) {
			slog.Warn("Ignoring relative entry in options.trusted_paths", "path", entry)
			continue
		}
		if pathWithin(root, resolvePath(p)) {
			return true
		}
	}
	return false
}

// TrustedProject describes a recorded trust entry.
type TrustedProject struct {
	Root        string
	Files       []string
	Fingerprint string
	TrustedAt   time.Time
}

// trustRecord is the persisted form of a trust decision.
type trustRecord struct {
	Fingerprint string `json:"fingerprint"`
	// Files lists the covered files that existed when the record was made,
	// relative to the root (slash separated), or absolute when outside it.
	Files     []string  `json:"files"`
	TrustedAt time.Time `json:"trusted_at"`
}

func (r trustRecord) paths(root string) []string {
	out := make([]string, 0, len(r.Files))
	for _, f := range r.Files {
		if filepath.IsAbs(f) {
			out = append(out, f)
			continue
		}
		out = append(out, filepath.Join(root, filepath.FromSlash(f)))
	}
	return out
}

func newTrustRecord(root string, files []string, trustedAt time.Time) trustRecord {
	keys := make([]string, 0, len(files))
	for _, f := range files {
		keys = append(keys, trustRecordKey(root, f))
	}
	slices.Sort(keys)
	return trustRecord{
		Fingerprint: ProjectFingerprint(root, files...),
		Files:       slices.Compact(keys),
		TrustedAt:   trustedAt.UTC(),
	}
}

func trustRecordKey(root, path string) string {
	if rel, ok := relWithin(root, path); ok {
		return filepath.ToSlash(rel)
	}
	return path
}

type trustStore struct {
	Version  int                    `json:"version"`
	Projects map[string]trustRecord `json:"projects"`
}

// TrustStorePath returns the path of the file recording trusted projects.
// It lives next to the global data config.
func TrustStorePath() string {
	return filepath.Join(filepath.Dir(GlobalConfigData()), trustStoreFile)
}

func readTrustStore() (*trustStore, error) {
	path := TrustStorePath()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &trustStore{Version: trustStoreVersion, Projects: map[string]trustRecord{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var store trustStore
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if store.Projects == nil {
		store.Projects = map[string]trustRecord{}
	}
	return &store, nil
}

// updateTrustStore runs fn on the trust records under a cross-process lock
// and writes them back atomically with 0600 permissions when fn reports a
// change.
func updateTrustStore(fn func(projects map[string]trustRecord) bool) error {
	path := TrustStorePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create trust store directory: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), trustStoreLockTimeout)
	defer cancel()
	release, err := lock.File(ctx, path+".lock")
	if err != nil {
		return fmt.Errorf("lock trust store: %w", err)
	}
	defer release()

	store, err := readTrustStore()
	if err != nil {
		return err
	}
	if !fn(store.Projects) {
		return nil
	}
	store.Version = trustStoreVersion
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(path, append(data, '\n'), 0o600)
}

// trustInputFor gathers the trust inputs for workingDir, reading only system
// and global config (for options.trusted_paths and options.data_directory).
func trustInputFor(ctx context.Context, workingDir, dataDir string) (trustInput, error) {
	global, _, err := loadFromConfigPaths(ctx, globalConfigPaths())
	if err != nil {
		return trustInput{}, fmt.Errorf("load global config: %w", err)
	}
	return newTrustInput(workingDir, dataDir, global.Options), nil
}

func newTrustInput(workingDir, dataDir string, globalOpts *Options) trustInput {
	var configuredDataDir string
	var trustedPaths []string
	if globalOpts != nil {
		configuredDataDir = globalOpts.DataDirectory
		trustedPaths = globalOpts.TrustedPaths
	}
	return trustInput{
		workingDir:    workingDir,
		projectFiles:  projectConfigPaths(workingDir),
		workspaceFile: workspaceConfigPath(resolveDataDirectory(workingDir, dataDir, configuredDataDir)),
		trustedPaths:  trustedPaths,
	}
}

// EvaluateProjectTrust reports the trust state of the project containing
// workingDir without loading any project-level config. dataDir is the
// --data-dir override, if any. extraFiles are fingerprinted in addition to
// the standard and registered project-level config files.
func EvaluateProjectTrust(ctx context.Context, workingDir, dataDir string, extraFiles ...string) (ProjectTrust, error) {
	in, err := trustInputFor(ctx, workingDir, dataDir)
	if err != nil {
		return ProjectTrust{}, err
	}
	in.extraFiles = extraFiles
	return evaluateProjectTrust(in), nil
}

// TrustProject records the current fingerprint of the project-level config
// of the project containing workingDir, so later loads apply it. Files the
// previous record covered stay covered (with their current content). When
// the project has no project-level config nothing is recorded.
func TrustProject(ctx context.Context, workingDir, dataDir string, extraFiles ...string) (ProjectTrust, error) {
	in, err := trustInputFor(ctx, workingDir, dataDir)
	if err != nil {
		return ProjectTrust{}, err
	}
	in.extraFiles = extraFiles
	root := ProjectRoot(workingDir)
	candidates := in.candidates(root)

	result := ProjectTrust{Root: root, Source: TrustSourceNoConfig, Trusted: true, workspaceFile: in.workspaceFile}
	err = updateTrustStore(func(projects map[string]trustRecord) bool {
		files := candidates
		if rec, ok := projects[root]; ok {
			files = normalizePaths(append(files, rec.paths(root)...))
		}
		existing := existingPaths(files)
		if len(existing) == 0 {
			return false
		}
		rec := newTrustRecord(root, existing, time.Now())
		projects[root] = rec
		result.Files = existing
		result.Source = TrustSourceRecord
		result.Fingerprint = rec.Fingerprint
		result.verified = make(map[string]bool, len(existing))
		for _, p := range existing {
			result.verified[p] = true
		}
		return true
	})
	if err != nil {
		return ProjectTrust{}, err
	}
	return result, nil
}

// RevokeProjectTrust removes the trust record of the project containing
// workingDir. It reports the project root and whether a record existed.
func RevokeProjectTrust(workingDir string) (root string, removed bool, err error) {
	root = ProjectRoot(workingDir)
	err = updateTrustStore(func(projects map[string]trustRecord) bool {
		if _, ok := projects[root]; !ok {
			return false
		}
		delete(projects, root)
		removed = true
		return true
	})
	return root, removed, err
}

// TrustedProjects lists the recorded trust entries, sorted by root.
func TrustedProjects() ([]TrustedProject, error) {
	store, err := readTrustStore()
	if err != nil {
		return nil, err
	}
	out := make([]TrustedProject, 0, len(store.Projects))
	for _, root := range slices.Sorted(maps.Keys(store.Projects)) {
		rec := store.Projects[root]
		out = append(out, TrustedProject{
			Root:        root,
			Files:       slices.Clone(rec.Files),
			Fingerprint: rec.Fingerprint,
			TrustedAt:   rec.TrustedAt,
		})
	}
	return out, nil
}

// ProjectTrust returns the trust decision made by the last config load.
func (s *ConfigStore) ProjectTrust() ProjectTrust {
	s.trustMu.RLock()
	defer s.trustMu.RUnlock()
	return s.trust
}

func (s *ConfigStore) setProjectTrust(t ProjectTrust) {
	s.trustMu.Lock()
	defer s.trustMu.Unlock()
	s.trust = t
}

// writeWorkspaceConfig performs write, which replaces the workspace config at
// path, and keeps the project's trust record in step with it (see the
// package comment on the trust gate). The caller holds the workspace config
// lock. A trust store that cannot be updated never blocks the write itself.
func (s *ConfigStore) writeWorkspaceConfig(path string, write func() error) error {
	if s.workingDir == "" {
		return write()
	}
	root := ProjectRoot(s.workingDir)
	wrote := false
	var writeErr error
	err := updateTrustStore(func(projects map[string]trustRecord) bool {
		candidates := normalizePaths(slices.Concat(
			projectConfigPaths(s.workingDir),
			[]string{s.ProjectTrust().workspaceFile, path},
			registeredProjectConfigFiles(s.workingDir, root),
		))
		existed := configFileExists(path)
		rec, hadRecord := projects[root]
		recordTrusted := false
		if hadRecord {
			candidates = normalizePaths(append(candidates, rec.paths(root)...))
			recordTrusted = ProjectFingerprint(root, candidates...) == rec.Fingerprint
		}

		wrote = true
		if writeErr = write(); writeErr != nil {
			return false
		}

		switch {
		case recordTrusted:
			projects[root] = newTrustRecord(root, existingPaths(candidates), rec.TrustedAt)
		case !existed:
			projects[root] = newTrustRecord(root, []string{normalizeConfigPath(path)}, time.Now())
		default:
			return false
		}
		return true
	})
	if !wrote {
		slog.Warn("Writing workspace config without updating project trust", "path", path, "error", err)
		return write()
	}
	if writeErr != nil {
		return writeErr
	}
	if err != nil {
		slog.Warn("Failed to update project trust after writing workspace config", "path", path, "error", err)
	}
	return nil
}

// workspaceConfigPath returns the workspace config file inside dataDir.
func workspaceConfigPath(dataDir string) string {
	return filepath.Join(dataDir, fmt.Sprintf("%s.json", appName))
}

// resolveDataDirectory returns the absolute data directory: the --data-dir
// override when set, else the configured options.data_directory, else the
// closest existing .tada directory within the project, else .tada in the
// working directory.
func resolveDataDirectory(workingDir, override, configured string) string {
	dir := override
	if dir == "" {
		dir = configured
	}
	if dir == "" {
		if path, ok := fsext.LookupClosestBounded(workingDir, projectBoundary(workingDir), defaultDataDirectory); ok {
			dir = path
		} else {
			dir = filepath.Join(workingDir, defaultDataDirectory)
		}
	}
	return filepath.Clean(filepathext.SmartJoin(workingDir, dir))
}

// resolvePath returns the absolute, symlink-resolved form of p. When p does
// not exist, its longest existing ancestor is resolved and the remaining
// components are appended unchanged.
func resolvePath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Clean(p)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	parent := filepath.Dir(abs)
	if parent == abs {
		return abs
	}
	return filepath.Join(resolvePath(parent), filepath.Base(abs))
}

// normalizeConfigPath resolves symlinks in the directories leading to p but
// keeps its final element, so a config file that is itself a symlink keeps
// its name (its content is still what gets hashed).
func normalizeConfigPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Clean(p)
	}
	return filepath.Join(resolvePath(filepath.Dir(abs)), filepath.Base(abs))
}

func normalizePaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		out = append(out, normalizeConfigPath(p))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func existingPaths(paths []string) []string {
	var out []string
	for _, p := range paths {
		if configFileExists(p) {
			out = append(out, p)
		}
	}
	return out
}

// relWithin returns path relative to root when path lies at or below root.
func relWithin(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(rel) || rel == ".." ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// pathWithin reports whether path is dir or lies below it.
func pathWithin(path, dir string) bool {
	_, ok := relWithin(dir, path)
	return ok
}
