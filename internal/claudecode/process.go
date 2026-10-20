package claudecode

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Errors that callers can retry around.
var (
	// ErrSessionNotFound means --resume pointed at a session the CLI does
	// not know. Retry with --session-id.
	ErrSessionNotFound = errors.New("claude code: no conversation found for session")
	// ErrSessionInUse means --session-id named a session that already
	// exists. Retry with --resume.
	ErrSessionInUse = errors.New("claude code: session id already in use")
)

// How long to wait for the CLI to wind down after an interrupt or after
// its final result before killing it.
var (
	interruptGrace = 3 * time.Second
	exitGrace      = 5 * time.Second
)

// TurnOptions configures one prompt → result exchange with the CLI.
type TurnOptions struct {
	// Binary is the claude executable; empty means FindBinary().
	Binary string
	// WorkDir is the project directory the CLI runs in.
	WorkDir string
	// Model is a Claude Code model alias or ID; "" or "default" lets the
	// CLI pick.
	Model string
	// PermissionMode is passed to --permission-mode when set.
	PermissionMode string
	// SessionID is the Claude Code session UUID.
	SessionID string
	// Resume continues an existing session instead of creating it.
	Resume bool
	// Prompt is the user message text.
	Prompt string
	// Env is the full child environment; nil means ChildEnv(os.Environ(), false).
	Env []string
	// Images are sent with the prompt as base64 image blocks.
	Images []Image
	// ExtraArgs are appended to the command line.
	ExtraArgs []string
}

// Image is an image attachment for the prompt.
type Image struct {
	MediaType string
	Data      []byte
}

// userContent is the message content for the prompt: a plain string, or
// text plus image blocks when there are images.
func userContent(opts TurnOptions) any {
	if len(opts.Images) == 0 {
		return opts.Prompt
	}
	blocks := []map[string]any{{"type": "text", "text": opts.Prompt}}
	for _, img := range opts.Images {
		blocks = append(blocks, map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": img.MediaType,
				"data":       base64.StdEncoding.EncodeToString(img.Data),
			},
		})
	}
	return blocks
}

// Handler receives the CLI's output.
type Handler struct {
	// OnEvent is called, in order, from a single goroutine for every
	// non-control event. Returning an error aborts the turn.
	OnEvent func(Event) error
	// CanUseTool answers a permission request. It may be called from
	// several goroutines at once; ctx is cancelled if the CLI withdraws
	// the request or the turn ends.
	CanUseTool func(ctx context.Context, req PermissionRequest) Decision
}

// TurnResult is what a finished turn reports.
type TurnResult struct {
	// Result is the final "result" event, nil if the CLI never sent one.
	Result *Event
	// SessionID is the session the CLI reported in system/init.
	SessionID string
	// Model is the model the CLI reported in system/init.
	Model string
	// Stderr holds the tail of the CLI's stderr.
	Stderr string
}

// BuildArgs returns the CLI arguments for opts.
func BuildArgs(opts TurnOptions) []string {
	args := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--verbose",
		"--permission-prompt-tool", "stdio",
	}
	if opts.PermissionMode != "" {
		args = append(args, "--permission-mode", opts.PermissionMode)
	}
	if m := strings.TrimSpace(opts.Model); m != "" && m != "default" {
		args = append(args, "--model", m)
	}
	if opts.SessionID != "" {
		if opts.Resume {
			args = append(args, "--resume", opts.SessionID)
		} else {
			args = append(args, "--session-id", opts.SessionID)
		}
	}
	return append(args, opts.ExtraArgs...)
}

// ChildEnv strips variables that would make the CLI bill an API key
// instead of the user's own login, or think it is nested inside another
// Claude Code session.
func ChildEnv(env []string, keepAPIKey bool) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_SSE_PORT":
			continue
		case "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN":
			if !keepAPIKey {
				continue
			}
		}
		out = append(out, kv)
	}
	return out
}

// RunTurn starts the CLI, sends one prompt, streams events to h until the
// final result, and waits for the process to exit. Cancelling ctx sends an
// interrupt and then kills the process if it does not stop.
func RunTurn(ctx context.Context, opts TurnOptions, h Handler) (*TurnResult, error) {
	bin := opts.Binary
	if bin == "" {
		bin = FindBinary()
	}
	if bin == "" {
		return nil, errors.New("claude code: the `claude` CLI was not found on PATH")
	}
	cmd := exec.Command(bin, BuildArgs(opts)...)
	cmd.Dir = opts.WorkDir
	cmd.Env = opts.Env
	if cmd.Env == nil {
		cmd.Env = ChildEnv(os.Environ(), false)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &cappedBuffer{max: 64 << 10}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("claude code: start %s: %w", bin, err)
	}

	permCtx, cancelPerms := context.WithCancel(ctx)
	defer cancelPerms()
	t := &turn{stdin: stdin, permCtx: permCtx, pending: map[string]*pendingPerm{}, resultCh: make(chan struct{})}

	kill := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}

	if err := t.send(map[string]any{
		"type":       "control_request",
		"request_id": t.nextID(),
		"request":    map[string]any{"subtype": "initialize"},
	}); err == nil {
		// A failed write means the CLI already exited; the read loop
		// below picks up its output and stderr either way.
		_ = t.send(map[string]any{
			"type":               "user",
			"message":            map[string]any{"role": "user", "content": userContent(opts)},
			"parent_tool_use_id": nil,
			"session_id":         opts.SessionID,
		})
	}

	readDone := make(chan error, 1)
	go func() { readDone <- t.readLoop(stdout, h) }()

	var readErr error
	select {
	case readErr = <-readDone:
	case <-t.resultCh:
		select {
		case readErr = <-readDone:
		case <-time.After(exitGrace):
			kill()
			readErr = <-readDone
		}
	case <-ctx.Done():
		_ = t.send(map[string]any{
			"type":       "control_request",
			"request_id": t.nextID(),
			"request":    map[string]any{"subtype": "interrupt"},
		})
		select {
		case readErr = <-readDone:
		case <-t.resultCh:
			select {
			case readErr = <-readDone:
			case <-time.After(interruptGrace):
				kill()
				readErr = <-readDone
			}
		case <-time.After(interruptGrace):
			kill()
			readErr = <-readDone
		}
	}
	if readErr != nil {
		// The handler aborted (or stdout broke): make sure the child goes away.
		kill()
	}
	// Withdraw any open permission prompts and wait for their handlers so
	// none outlives the turn.
	cancelPerms()
	t.wg.Wait()
	t.closeStdin()
	waitErr := cmd.Wait()

	res := &TurnResult{Result: t.result, SessionID: t.sessionID, Model: t.model, Stderr: stderr.String()}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return res, readErr
	}
	if t.result != nil {
		if err := classify(t.result.Result + "\n" + strings.Join(t.result.Errors, "\n")); err != nil && t.result.IsError && !t.sawAssistant {
			return res, err
		}
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		return res, nil
	}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	if err := classify(res.Stderr); err != nil {
		return res, err
	}
	msg := strings.TrimSpace(tail(res.Stderr, 2000))
	if waitErr != nil {
		if msg == "" {
			return res, fmt.Errorf("claude code exited: %w", waitErr)
		}
		return res, fmt.Errorf("claude code exited: %w: %s", waitErr, msg)
	}
	if msg != "" {
		return res, fmt.Errorf("claude code ended without a result: %s", msg)
	}
	return res, errors.New("claude code ended without a result")
}

func classify(text string) error {
	switch {
	case strings.Contains(text, "No conversation found"):
		return ErrSessionNotFound
	case strings.Contains(text, "is already in use"):
		return ErrSessionInUse
	}
	return nil
}

type pendingPerm struct {
	cancel    context.CancelFunc
	withdrawn bool
}

type turn struct {
	writeMu sync.Mutex
	stdin   io.WriteCloser
	closed  bool
	seq     int

	permCtx context.Context
	wg      sync.WaitGroup
	mu      sync.Mutex
	pending map[string]*pendingPerm

	resultCh     chan struct{}
	result       *Event
	sessionID    string
	model        string
	sawAssistant bool
}

func (t *turn) nextID() string {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	t.seq++
	return fmt.Sprintf("tada-%d", t.seq)
}

func (t *turn) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if t.closed {
		return io.ErrClosedPipe
	}
	_, err = t.stdin.Write(append(b, '\n'))
	return err
}

func (t *turn) closeStdin() {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if !t.closed {
		t.closed = true
		_ = t.stdin.Close()
	}
}

func (t *turn) readLoop(r io.Reader, h Handler) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 256<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			// Not JSON (a stray log line); ignore it.
			continue
		}
		ev.Raw = append(json.RawMessage(nil), line...)
		switch ev.Type {
		case "keep_alive", "control_response":
			continue
		case "control_cancel_request":
			t.withdraw(ev.RequestID)
			continue
		case "control_request":
			t.handleControl(ev, h)
			continue
		case "system":
			if ev.Subtype == "init" {
				t.sessionID = ev.SessionID
				t.model = ev.Model
			}
		case "assistant", "stream_event":
			if !ev.IsSubagent() {
				t.sawAssistant = true
			}
		}
		if h.OnEvent != nil {
			if err := h.OnEvent(ev); err != nil {
				return err
			}
		}
		if ev.Type == "result" && !ev.IsSubagent() {
			e := ev
			t.result = &e
			t.closeStdin()
			close(t.resultCh)
			// Keep draining stdout until the process exits.
			for sc.Scan() {
			}
			return nil
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return io.EOF
}

func (t *turn) handleControl(ev Event, h Handler) {
	var req PermissionRequest
	_ = json.Unmarshal(ev.Request, &req)
	if req.Subtype != "can_use_tool" {
		_ = t.send(map[string]any{
			"type": "control_response",
			"response": map[string]any{
				"subtype":    "error",
				"request_id": ev.RequestID,
				"error":      "Ta-Da does not support control request " + req.Subtype,
			},
		})
		return
	}
	ctx, cancel := context.WithCancel(t.permCtx)
	p := &pendingPerm{cancel: cancel}
	t.mu.Lock()
	t.pending[ev.RequestID] = p
	t.mu.Unlock()
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		defer cancel()
		d := Decision{Message: "Ta-Da has no permission handler for Claude Code."}
		if h.CanUseTool != nil {
			d = h.CanUseTool(ctx, req)
		}
		t.mu.Lock()
		delete(t.pending, ev.RequestID)
		withdrawn := p.withdrawn
		t.mu.Unlock()
		if withdrawn {
			return
		}
		_ = t.send(permissionResponse(ev.RequestID, req, d))
	}()
}

func permissionResponse(requestID string, req PermissionRequest, d Decision) map[string]any {
	var body map[string]any
	if d.Allow {
		input := d.UpdatedInput
		if len(input) == 0 {
			input = req.Input
		}
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		body = map[string]any{"behavior": "allow", "updatedInput": input}
	} else {
		msg := d.Message
		if msg == "" {
			msg = "The user denied this tool call."
		}
		body = map[string]any{"behavior": "deny", "message": msg}
		if d.Interrupt {
			body["interrupt"] = true
		}
	}
	if req.ToolUseID != "" {
		body["toolUseID"] = req.ToolUseID
	}
	return map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   body,
		},
	}
}

func (t *turn) withdraw(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if p, ok := t.pending[id]; ok {
		p.withdrawn = true
		p.cancel()
	}
}

type cappedBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buf = append(c.buf, p...)
	if over := len(c.buf) - c.max; over > 0 {
		c.buf = append([]byte(nil), c.buf[over:]...)
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.buf)
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
