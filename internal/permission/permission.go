package permission

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/google/uuid"
)

// hookApprovalKey is the unexported context key used to mark a tool call as
// pre-approved by a PreToolUse hook. The value is the tool call ID so an
// approval can't be reused across calls that happen to share a context.
type hookApprovalKey struct{}

// WithHookApproval returns a context that marks the given tool call ID as
// pre-approved by a hook. When the permission service sees a matching
// request it short-circuits the normal prompt and grants immediately.
func WithHookApproval(ctx context.Context, toolCallID string) context.Context {
	return context.WithValue(ctx, hookApprovalKey{}, toolCallID)
}

// hookApproved reports whether the context carries a hook approval for the
// given tool call ID.
func hookApproved(ctx context.Context, toolCallID string) bool {
	if toolCallID == "" {
		return false
	}
	v, _ := ctx.Value(hookApprovalKey{}).(string)
	return v == toolCallID
}

type CreatePermissionRequest struct {
	SessionID   string `json:"session_id"`
	ToolCallID  string `json:"tool_call_id"`
	ToolName    string `json:"tool_name"`
	Description string `json:"description"`
	Action      string `json:"action"`
	Params      any    `json:"params"`
	Path        string `json:"path"`
}

type PermissionNotification struct {
	SessionID  string `json:"session_id"`
	ToolCallID string `json:"tool_call_id"`
	Granted    bool   `json:"granted"`
	Denied     bool   `json:"denied"`
}

type PermissionRequest struct {
	ID          string `json:"id"`
	SessionID   string `json:"session_id"`
	ToolCallID  string `json:"tool_call_id"`
	ToolName    string `json:"tool_name"`
	Description string `json:"description"`
	Action      string `json:"action"`
	Params      any    `json:"params"`
	Path        string `json:"path"`
	// Class grades what the action can change.
	Class Class `json:"class,omitempty"`
	// Reason says why a request asks although a mode or an earlier
	// approval would usually let it run.
	Reason string `json:"reason,omitempty"`
	// AlwaysAsk is set when the request asks every time, so approving it
	// for the session would do nothing.
	AlwaysAsk bool `json:"always_ask,omitempty"`
}

type Service interface {
	pubsub.Subscriber[PermissionRequest]
	// GrantPersistent grants a permission request and remembers the grant
	// for the session. It returns true if this call actually resolved the
	// pending request; false if the request had already been resolved
	// (e.g., by another concurrent caller) or is unknown.
	GrantPersistent(permission PermissionRequest) bool
	// Grant grants a permission request. It returns true if this call
	// actually resolved the pending request; false if the request had
	// already been resolved or is unknown.
	Grant(permission PermissionRequest) bool
	// Deny denies a permission request. It returns true if this call
	// actually resolved the pending request; false if the request had
	// already been resolved or is unknown.
	Deny(permission PermissionRequest) bool
	Request(ctx context.Context, opts CreatePermissionRequest) (bool, error)
	// ActiveRequest returns the request that is currently awaiting a
	// decision, or false when no request is pending. A request is only
	// reported while it can still be resolved; a request whose waiter
	// was cancelled is not returned.
	ActiveRequest() (PermissionRequest, bool)
	AutoApproveSession(sessionID string)
	// SessionAutoApproved reports whether every request from the session
	// is approved without asking.
	SessionAutoApproved(sessionID string) bool
	SetSkipRequests(skip bool)
	SkipRequests() bool
	SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[PermissionNotification]
}

// PermissionKey is a composite key for session permission lookups.
type PermissionKey struct {
	SessionID string
	ToolName  string
	Action    string
	Path      string
}

type permissionService struct {
	*pubsub.Broker[PermissionRequest]

	notificationBroker    *pubsub.Broker[PermissionNotification]
	workingDir            string
	sessionPermissions    *csync.Map[PermissionKey, bool]
	pendingRequests       *csync.Map[string, chan bool]
	autoApproveSessions   map[string]bool
	autoApproveSessionsMu sync.RWMutex
	skip                  atomic.Bool
	allowedTools          []string
	projectAllowedTools   []string

	// used to make sure we only process one request at a time
	requestMu       sync.Mutex
	activeRequest   *PermissionRequest
	activeRequestMu sync.Mutex

	edits atomic.Bool

	controlMu   sync.Mutex
	policy      Policy
	takenBackAt time.Time
	// autoUsed counts, per session and class, the actions Auto or Edits
	// mode approved since the person last answered for that class.
	autoUsed map[string]map[Class]int
	log      []Action
	recorder func(Action)
}

// maxActions bounds the in-memory action log.
const maxActions = 200

// resolve atomically removes the pending request entry for the given
// permission and, if it was still pending, publishes exactly one
// PermissionNotification and forwards the outcome to the waiter on
// respCh. It returns true if this call resolved the request, false if
// it had already been resolved (e.g., by another concurrent caller) or
// the request ID is unknown.
//
// If onResolve is non-nil it runs after the pending entry has been
// taken but before the notification is published or the waiter is
// unblocked. This lets GrantPersistent record the session permission
// only when it actually wins the race, so a losing GrantPersistent
// that lost to a Deny does not leak an auto-approve entry.
//
// All three public resolution methods (Grant, GrantPersistent, Deny)
// route through this helper so multi-subscriber UIs can race safely:
// the first caller wins, the rest become no-ops.
func (s *permissionService) resolve(permission PermissionRequest, granted, denied bool, onResolve func()) bool {
	respCh, ok := s.pendingRequests.Take(permission.ID)
	if !ok {
		return false
	}

	if onResolve != nil {
		onResolve()
	}

	s.notificationBroker.Publish(pubsub.CreatedEvent, PermissionNotification{
		SessionID:  permission.SessionID,
		ToolCallID: permission.ToolCallID,
		Granted:    granted,
		Denied:     denied,
	})

	// respCh is buffered (cap 1) and only ever has at most one sender
	// per request because Take removes the entry under the map lock,
	// so this send never blocks.
	respCh <- granted

	s.activeRequestMu.Lock()
	if s.activeRequest != nil && s.activeRequest.ID == permission.ID {
		s.activeRequest = nil
	}
	s.activeRequestMu.Unlock()
	return true
}

func (s *permissionService) GrantPersistent(permission PermissionRequest) bool {
	// Record the persistent grant only if this call wins the
	// pending-request race. Otherwise a losing GrantPersistent that
	// lost to a Deny would still leave an auto-approve entry behind,
	// silently flipping later denied calls to allowed.
	return s.resolve(permission, true, false, func() {
		s.sessionPermissions.Set(PermissionKey{
			SessionID: permission.SessionID,
			ToolName:  permission.ToolName,
			Action:    permission.Action,
			Path:      permission.Path,
		}, true)
	})
}

func (s *permissionService) Grant(permission PermissionRequest) bool {
	return s.resolve(permission, true, false, nil)
}

func (s *permissionService) Deny(permission PermissionRequest) bool {
	return s.resolve(permission, false, true, nil)
}

func (s *permissionService) Request(ctx context.Context, opts CreatePermissionRequest) (bool, error) {
	class := Classify(opts, s.workingDir)
	policy := s.currentPolicy()
	alwaysAsk := policy.Asks(class)

	// Explicit allow rules from the config and hook approvals are the
	// person's own decisions made ahead of time, so they hold even for
	// classes that always ask.
	commandKey := opts.ToolName + ":" + opts.Action
	if slices.Contains(s.allowedTools, commandKey) || slices.Contains(s.allowedTools, opts.ToolName) {
		s.record(opts, class, DecisionAllowed, ViaAllowlist)
		return true, nil
	}

	// Tools allowed inside the project only run without asking when the
	// file they write resolves, symlinks included, inside the working
	// directory. Anything else falls through to the normal prompt.
	if slices.Contains(s.projectAllowedTools, opts.ToolName) {
		if target, ok := opts.Params.(FileTarget); ok && editableInProject(s.workingDir, target.TargetFile()) {
			s.record(opts, class, DecisionAllowed, ViaProject)
			return true, nil
		}
	}

	// A PreToolUse hook that returned decision=allow stamps the context
	// with the tool call ID. Treat that as a pre-approval and skip the
	// prompt entirely. We still publish a granted notification so the UI
	// and audit subscribers see the outcome.
	if hookApproved(ctx, opts.ToolCallID) {
		s.notificationBroker.Publish(pubsub.CreatedEvent, PermissionNotification{
			SessionID:  opts.SessionID,
			ToolCallID: opts.ToolCallID,
			Granted:    true,
		})
		s.record(opts, class, DecisionAllowed, ViaHook)
		return true, nil
	}

	// Auto mode approves everything, Edits mode approves edits inside
	// the project, each up to its per-session budget.
	var budgetUsed int
	if !alwaysAsk {
		via := Via("")
		switch {
		case s.skip.Load():
			via = ViaAuto
		case s.edits.Load() && class == ClassWrite && opts.Params != nil && isFileEdit(opts):
			via = ViaEdits
		}
		if via != "" {
			ok, limit := s.takeAuto(opts.SessionID, class, policy)
			if ok {
				s.record(opts, class, DecisionAllowed, via)
				return true, nil
			}
			budgetUsed = limit
		}
	}

	s.requestMu.Lock()
	defer s.requestMu.Unlock()

	// tell the UI that a permission was requested
	s.notificationBroker.Publish(pubsub.CreatedEvent, PermissionNotification{
		SessionID:  opts.SessionID,
		ToolCallID: opts.ToolCallID,
	})

	s.autoApproveSessionsMu.RLock()
	autoApprove := s.autoApproveSessions[opts.SessionID]
	s.autoApproveSessionsMu.RUnlock()

	if autoApprove {
		// Nobody is there to answer, so a non-interactive run is
		// approved, except for spending past a budget.
		granted := class != ClassSpend
		s.notificationBroker.Publish(pubsub.CreatedEvent, PermissionNotification{
			SessionID:  opts.SessionID,
			ToolCallID: opts.ToolCallID,
			Granted:    granted,
			Denied:     !granted,
		})
		if granted {
			s.record(opts, class, DecisionAllowed, ViaHeadless)
		} else {
			s.record(opts, class, DecisionDenied, ViaHeadless)
		}
		return granted, nil
	}

	fileInfo, err := os.Stat(opts.Path)
	dir := opts.Path
	if err == nil {
		if fileInfo.IsDir() {
			dir = opts.Path
		} else {
			dir = filepath.Dir(opts.Path)
		}
	}

	if dir == "." {
		dir = s.workingDir
	}
	permission := PermissionRequest{
		ID:          uuid.New().String(),
		Path:        dir,
		SessionID:   opts.SessionID,
		ToolCallID:  opts.ToolCallID,
		ToolName:    opts.ToolName,
		Description: opts.Description,
		Action:      opts.Action,
		Params:      opts.Params,
		Class:       class,
		Reason:      reasonFor(class, alwaysAsk, budgetUsed),
		AlwaysAsk:   alwaysAsk,
	}

	if !alwaysAsk {
		if _, ok := s.sessionPermissions.Get(PermissionKey{
			SessionID: permission.SessionID,
			ToolName:  permission.ToolName,
			Action:    permission.Action,
			Path:      permission.Path,
		}); ok {
			s.notificationBroker.Publish(pubsub.CreatedEvent, PermissionNotification{
				SessionID:  opts.SessionID,
				ToolCallID: opts.ToolCallID,
				Granted:    true,
			})
			s.record(opts, class, DecisionAllowed, ViaSession)
			return true, nil
		}
	}

	s.activeRequestMu.Lock()
	s.activeRequest = &permission
	s.activeRequestMu.Unlock()

	respCh := make(chan bool, 1)
	s.pendingRequests.Set(permission.ID, respCh)
	defer s.pendingRequests.Del(permission.ID)

	// Publish the request
	s.Publish(pubsub.CreatedEvent, permission)

	select {
	case <-ctx.Done():
		s.record(opts, class, DecisionCanceled, "")
		return false, ctx.Err()
	case granted := <-respCh:
		if granted {
			s.resetAuto(opts.SessionID, class)
			s.record(opts, class, DecisionAllowed, ViaUser)
		} else {
			s.record(opts, class, DecisionDenied, ViaUser)
		}
		return granted, nil
	}
}

// isFileEdit reports whether the request edits a single file, the only
// kind of action Edits mode approves.
func isFileEdit(opts CreatePermissionRequest) bool {
	t, ok := opts.Params.(FileTarget)
	return ok && t.TargetFile() != ""
}

// ActiveRequest returns the request that is currently awaiting a
// decision. The active pointer can outlive the waiter that published
// it (Request leaves it behind when its context is cancelled), so a
// request is only reported while its ID is still registered in
// pendingRequests, i.e. while Grant/Deny can actually resolve it.
func (s *permissionService) ActiveRequest() (PermissionRequest, bool) {
	s.activeRequestMu.Lock()
	active := s.activeRequest
	s.activeRequestMu.Unlock()
	if active == nil {
		return PermissionRequest{}, false
	}
	if _, ok := s.pendingRequests.Get(active.ID); !ok {
		return PermissionRequest{}, false
	}
	return *active, true
}

func (s *permissionService) AutoApproveSession(sessionID string) {
	s.autoApproveSessionsMu.Lock()
	s.autoApproveSessions[sessionID] = true
	s.autoApproveSessionsMu.Unlock()
}

func (s *permissionService) SessionAutoApproved(sessionID string) bool {
	s.autoApproveSessionsMu.RLock()
	defer s.autoApproveSessionsMu.RUnlock()
	return s.autoApproveSessions[sessionID]
}

func (s *permissionService) SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[PermissionNotification] {
	return s.notificationBroker.Subscribe(ctx)
}

func (s *permissionService) SetSkipRequests(skip bool) {
	s.skip.Store(skip)
	if skip {
		s.controlMu.Lock()
		s.takenBackAt = time.Time{}
		s.controlMu.Unlock()
	}
}

func (s *permissionService) SkipRequests() bool {
	return s.skip.Load()
}

// FileTarget is implemented by the params of tools that write a single
// file, so a project-scoped allow rule can check where the write lands.
type FileTarget interface {
	TargetFile() string
}

// withinDir reports whether path resolves to a location inside root.
// Symlinks are followed on both sides, so a link inside the project can't
// point the check somewhere else. A relative path resolves against the
// process working directory, as the file system calls that write it do.
func withinDir(root, path string) bool {
	_, ok := projectRel(root, path)
	return ok
}

// projectRel returns path relative to the real project root, and false when
// it resolves outside root.
func projectRel(root, path string) (string, bool) {
	if root == "" || path == "" {
		return "", false
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false
	}
	realPath, err := resolveExisting(path)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(realRoot, realPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// protectedDirs and protectedNames are project paths that decide what runs
// later or with more trust: git hooks and config, CI workflows, editor tasks,
// agent settings and Ta-Da's own project config. A write there always asks,
// even in Edits mode or for a tool allowed inside the project.
var (
	protectedDirs  = []string{".git", ".tada", ".claude", ".github", ".vscode", ".idea", ".husky"}
	protectedNames = []string{".tadarc", "tadarc", ".tada.json", "tada.json", ".mcp.json", ".envrc"}
)

// editableInProject reports whether path resolves inside root and isn't a
// protected path.
func editableInProject(root, path string) bool {
	rel, ok := projectRel(root, path)
	return ok && !protectedRel(rel)
}

// protectedRel reports whether a project-relative path is protected. Names
// are compared without case and without trailing dots, spaces or a ":stream"
// suffix, since case-insensitive and Windows file systems treat those
// spellings as the same file.
func protectedRel(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i, part := range parts {
		part, _, _ = strings.Cut(part, ":")
		part = strings.ToLower(strings.TrimRight(part, ". "))
		if part == "" {
			continue
		}
		if slices.Contains(protectedDirs, part) {
			return true
		}
		if i == len(parts)-1 && slices.Contains(protectedNames, part) {
			return true
		}
	}
	return false
}

// resolveExisting resolves symlinks in the longest existing prefix of an
// absolute, clean path and re-attaches the rest, so a file that doesn't
// exist yet resolves through its real parent. A dangling symlink is an
// error: writing through it would create its target, wherever that is.
func resolveExisting(path string) (string, error) {
	var rest []string
	for {
		real, err := filepath.EvalSymlinks(path)
		if err == nil {
			return filepath.Join(append([]string{real}, rest...)...), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		if _, lerr := os.Lstat(path); lerr == nil {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", err
		}
		rest = append([]string{filepath.Base(path)}, rest...)
		path = parent
	}
}

// NewPermissionService returns the permission service. allowedTools run
// without asking anywhere; projectAllowedTools only when the file they
// write is inside workingDir.
func NewPermissionService(workingDir string, skip bool, allowedTools, projectAllowedTools []string) Service {
	svc := &permissionService{
		Broker:              pubsub.NewBroker[PermissionRequest](),
		notificationBroker:  pubsub.NewBroker[PermissionNotification](),
		workingDir:          workingDir,
		sessionPermissions:  csync.NewMap[PermissionKey, bool](),
		autoApproveSessions: make(map[string]bool),
		allowedTools:        allowedTools,
		projectAllowedTools: projectAllowedTools,
		pendingRequests:     csync.NewMap[string, chan bool](),
		policy:              DefaultPolicy(),
		autoUsed:            make(map[string]map[Class]int),
	}
	svc.skip.Store(skip)
	return svc
}
