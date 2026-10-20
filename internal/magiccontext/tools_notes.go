// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): tools/ctx-note.

// ctx_note agent tool, ported from cortexkit/magic-context (MIT)
// tools/ctx-note.
//
// Gaps vs upstream: no surface-condition compiler (smart notes are stored
// uncompiled and checked by the dreamer's tool-less fallback), no wake
// plane, and no unwrapImitatedReducedArgs recovery of mangled arguments.

package magiccontext

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"charm.land/fantasy"
)

// CtxNoteToolName is the tool name.
const CtxNoteToolName = "ctx_note"

const ctxNoteDescription = "Session notes are pending intentions: work you intend to return to, with its findings attached.\n\n" +
	"Use notes for:\n" +
	"- A finding to revisit when you return to the intended work\n" +
	"- A decision with its reasoning, when follow-up work remains\n" +
	"- A backlog item with evidence already found\n" +
	"- Something the user explicitly asks you to note\n\n" +
	"Don't use notes for: the next few steps; a plan you are actively executing; restart/fold insurance; or a record of how things stand (world-state, a design at a point in time) with nothing you intend to do about it — that goes stale silently; a fact worth keeping is memory, the rest is nothing. Use todos for active work. If the detail already lives in a file, record the path and what to inspect — don't copy the file into a note. Durable project facts belong in ctx_memory, not notes.\n\n" +
	"First line is the title (under 80 chars), followed by detail. Operations:\n" +
	"- write: save a new note (content required)\n" +
	"- read: one row per note — `#id · age · title` — ready smart notes first, then newest; rows untouched 30+ days are marked stale. Pass note_ids to read full bodies; limit/offset page; filter selects other statuses.\n" +
	"- update: change one note (note_ids=[N])\n" +
	"- dismiss: retire 1–50 notes (note_ids). Dismiss a note when its work lands or is abandoned; a queue you never dismiss from stops being read.\n" +
	"- surface_condition: make it a smart note — an outside checker periodically tests the condition using only externally verifiable signals (GitHub state, files, git, releases, web), never this conversation or future actions; the note is parked until the condition holds."

// Reply strings.
const (
	SmartNotesDisabledReply    = "Error: Smart notes are disabled; surface_condition is unavailable."
	sessionNoteConditionError  = "Only a note created with a condition can have one. Write a new note with surface_condition, and dismiss this one."
	noteDismissFooter          = "\n\nTo dismiss a stale note: ctx_note(action=\"dismiss\", note_ids=[N])"
	noteAnchorHint             = "\n\n↳ @msg N marks the conversation tail when a note was written. To see what led to it: ctx_expand(start=N-x, end=N) (pick x for how far back to look)."
	notesFilterDescription     = "Read filter: all, active, pending, ready, dismissed. Omitted: active session notes plus every current project note; active shows only active-status notes."
	notesFilterDescriptionFull = "Read filter: all, active, pending (unsurfaced smart notes), ready, dismissed. Omitted, it shows active session notes plus every current smart note (pending included); active shows only notes whose stored status is active."
)

// withoutSmartNoteText strips the smart-note clauses from the description.
func withoutSmartNoteText(d string) string {
	d = strings.Replace(d, "ready smart notes first, then newest", "newest first", 1)
	d, _, _ = strings.Cut(d, "\n- surface_condition:")
	d, _, _ = strings.Cut(d, " surface_condition parks")
	return d
}

// NoteToolDeps wires ctx_note to the engine.
type NoteToolDeps struct {
	Store       *Store
	Settings    Settings
	SessionID   func(ctx context.Context) string
	ProjectPath func(ctx context.Context) string // "" = unresolved
	// DescribeUnresolved explains why the project could not be resolved
	// (optional).
	DescribeUnresolved func(ctx context.Context) string
	Now                func() time.Time
}

// CtxNoteParams are the full ctx_note arguments (smart notes enabled).
type CtxNoteParams struct {
	Action           string    `json:"action,omitempty" description:"write | read | update | dismiss. Defaults to write when content is given, else read." enum:"write,read,dismiss,update"`
	Content          string    `json:"content,omitempty" description:"Note text for write/update: first line is the title (under 80 chars), then the detail."`
	SurfaceCondition string    `json:"surface_condition,omitempty" description:"Makes this a smart note: a condition an outside checker can verify on its own, periodically — repository state, releases, web pages, anything it can look up — never something only this conversation knows. The note is parked until the condition holds."`
	Filter           string    `json:"filter,omitempty" description:"Read filter: all, active, pending (unsurfaced smart notes), ready, dismissed. Omitted, it shows active session notes plus every current smart note (pending included); active shows only notes whose stored status is active." enum:"all,active,pending,ready,dismissed"`
	Limit            float64   `json:"limit,omitempty" description:"Rows per read (default 25)."`
	Offset           float64   `json:"offset,omitempty" description:"Skip this many newest rows (default 0)."`
	NoteIDs          []float64 `json:"note_ids,omitempty" description:"Note ids: one for update, 1–50 for dismiss or read (read returns full bodies). Ignored by write."`
}

// CtxNotePlainParams are the arguments when smart notes are unavailable.
type CtxNotePlainParams struct {
	Action  string    `json:"action,omitempty" description:"write | read | update | dismiss. Defaults to write when content is given, else read." enum:"write,read,dismiss,update"`
	Content string    `json:"content,omitempty" description:"Note text for write/update: first line is the title (under 80 chars), then the detail."`
	Filter  string    `json:"filter,omitempty" description:"Read filter: all, active, pending, ready, dismissed. Omitted: active session notes plus every current project note; active shows only active-status notes." enum:"all,active,pending,ready,dismissed"`
	Limit   float64   `json:"limit,omitempty" description:"Rows per read (default 25)."`
	Offset  float64   `json:"offset,omitempty" description:"Skip this many newest rows (default 0)."`
	NoteIDs []float64 `json:"note_ids,omitempty" description:"Note ids: one for update, 1–50 for dismiss or read (read returns full bodies). Ignored by write."`
}

// NewCtxNoteTool builds ctx_note. The smart-note surface (description text
// and surface_condition) is shown only when both smart notes and the
// dreamer are enabled.
func NewCtxNoteTool(deps NoteToolDeps) fantasy.AgentTool {
	if deps.Settings.SmartNotes && deps.Settings.Dreamer {
		return fantasy.NewAgentTool(CtxNoteToolName, ctxNoteDescription,
			func(ctx context.Context, p CtxNoteParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
				return fantasy.NewTextResponse(RunCtxNote(ctx, deps, p)), nil
			})
	}
	return fantasy.NewAgentTool(CtxNoteToolName, withoutSmartNoteText(ctxNoteDescription),
		func(ctx context.Context, p CtxNotePlainParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			return fantasy.NewTextResponse(RunCtxNote(ctx, deps, CtxNoteParams{
				Action: p.Action, Content: p.Content, Filter: p.Filter, Limit: p.Limit, Offset: p.Offset, NoteIDs: p.NoteIDs,
			})), nil
		})
}

func parseNoteIDs(action string, raw []float64) ([]int64, string) {
	maxN := MaxNoteIDs
	if action == "update" {
		maxN = 1
	}
	bad := len(raw) < 1 || len(raw) > maxN
	ids := make([]int64, 0, len(raw))
	for _, f := range raw {
		if f <= 0 || f != math.Trunc(f) || f > math.MaxInt64/2 {
			bad = true
			break
		}
		ids = append(ids, int64(f))
	}
	if bad {
		if action == "update" {
			return nil, "Error: 'note_ids' must contain exactly one positive integer id when action is 'update'."
		}
		return nil, fmt.Sprintf("Error: 'note_ids' must contain 1 to 50 positive integer ids when action is '%s'.", action)
	}
	return ids, ""
}

// RunCtxNote executes one ctx_note call and returns the reply text.
func RunCtxNote(ctx context.Context, deps NoteToolDeps, p CtxNoteParams) string {
	if deps.Store == nil || deps.SessionID == nil {
		return "Error: notes are unavailable."
	}
	now := time.Now()
	if deps.Now != nil {
		now = deps.Now()
	}
	condition := strings.TrimSpace(p.SurfaceCondition)
	if !deps.Settings.SmartNotes && condition != "" {
		return SmartNotesDisabledReply
	}
	sid := deps.SessionID(ctx)
	content := strings.TrimSpace(p.Content)
	action := strings.TrimSpace(p.Action)
	if action == "" {
		action = "read"
		if content != "" {
			action = "write"
		}
	}
	var ids []int64
	if action == "dismiss" || action == "update" || (action == "read" && len(p.NoteIDs) > 0) {
		var msg string
		if ids, msg = parseNoteIDs(action, p.NoteIDs); msg != "" {
			return msg
		}
	}
	project := ""
	if deps.ProjectPath != nil {
		project = deps.ProjectPath(ctx)
	}
	unresolved := func(what string) string {
		why := "no project directory"
		if deps.DescribeUnresolved != nil {
			if d := deps.DescribeUnresolved(ctx); d != "" {
				why = d
			}
		}
		return fmt.Sprintf("Error: Could not resolve project identity for %s: %s", what, why)
	}
	s := deps.Store
	fail := func(err error) string { return "Error: " + err.Error() }

	switch action {
	case "write":
		if content == "" {
			return "Error: 'content' is required when action is 'write'."
		}
		if condition != "" {
			if !deps.Settings.Dreamer {
				return "Error: Smart notes require dreamer to be enabled. Enable dreamer in magic-context.jsonc to use surface_condition."
			}
			if project == "" {
				return unresolved("smart note")
			}
			n, err := s.AddSmartNote(ctx, sid, project, content, condition)
			if err != nil {
				return fail(err)
			}
			return fmt.Sprintf("Created smart note #%d. Dreamer will evaluate the condition during nightly runs:\n- Content: %s\n- Condition: %s", n.ID, content, condition)
		}
		n, err := s.AddSessionNote(ctx, sid, content)
		if err != nil {
			return fail(err)
		}
		tray, err := s.SessionNoteTray(ctx, sid, now)
		if err != nil {
			return fail(err)
		}
		return fmt.Sprintf("Saved session note #%d.%s", n.ID, tray)

	case "dismiss":
		if project == "" {
			return unresolved("note dismiss")
		}
		res, err := s.DismissNotes(ctx, ids, sid, project)
		if err != nil {
			return fail(err)
		}
		if len(ids) == 1 {
			if len(res) == 1 && res[0].Outcome == NoteDismissed {
				return fmt.Sprintf("Note #%d dismissed.", ids[0])
			}
			return fmt.Sprintf("Error: Note #%d not found in your session/project or already dismissed.", ids[0])
		}
		done := 0
		lines := make([]string, len(res))
		for i, r := range res {
			if r.Outcome == NoteDismissed {
				done++
			}
			o := r.Outcome
			if o == NoteNotOwned {
				o = NoteNotFound
			}
			lines[i] = fmt.Sprintf("- Note #%d: %s", r.ID, o)
		}
		return fmt.Sprintf("Dismissed %d of %d notes.\n%s", done, len(res), strings.Join(lines, "\n"))

	case "update":
		id := ids[0]
		if condition != "" && project != "" {
			if n, ok, err := s.NoteInScope(ctx, id, sid, project); err != nil {
				return fail(err)
			} else if ok && n.Type == NoteTypeSession {
				return "Error: " + sessionNoteConditionError
			}
		}
		if content == "" && condition == "" {
			return "Error: Provide 'content' and/or 'surface_condition' to update."
		}
		if project == "" {
			return unresolved("note update")
		}
		var cp, condp *string
		var parts []string
		if content != "" {
			cp = &content
			parts = append(parts, "Content: "+content)
		}
		if condition != "" {
			condp = &condition
			parts = append(parts, "Condition: "+condition)
		}
		_, ok, err := s.UpdateNote(ctx, id, sid, project, cp, condp)
		if err != nil {
			return fail(err)
		}
		if !ok {
			return fmt.Sprintf("Error: Note #%d not found in your session/project or has no compatible fields to update.", id)
		}
		return fmt.Sprintf("Updated note #%d:\n%s", id, strings.Join(parts, "\n"))

	case "read":
	default:
		return fmt.Sprintf("Error: unknown action %q.", action)
	}

	limit := DefaultNoteReadLimit
	if p.Limit > 0 {
		limit = int(math.Floor(p.Limit))
	}
	offset := 0
	if p.Offset > 0 {
		offset = int(math.Floor(p.Offset))
	}
	var body string
	if len(ids) > 0 {
		if project == "" {
			return unresolved("note read")
		}
		found := map[int64]Note{}
		for _, id := range ids {
			n, ok, err := s.NoteInScope(ctx, id, sid, project)
			if err != nil {
				return fail(err)
			}
			if ok {
				found[id] = n
			}
		}
		body = RenderNotesByID(ids, found, now)
	} else {
		notes, err := readGlanceNotes(ctx, s, sid, project, strings.TrimSpace(p.Filter))
		if err != nil {
			return fail(err)
		}
		body = RenderNoteGlance(notes, now, limit, offset)
	}
	// Best-effort read watermark for the note nudger.
	_ = s.SetMeta(ctx, sid, map[string]any{"last_note_read_at": now.UnixMilli()})
	if body == EmptyNoteReadReply {
		return body
	}
	hint := ""
	if strings.Contains(body, "↳ @msg ") {
		hint = noteAnchorHint
	}
	return body + hint + noteDismissFooter
}

func readGlanceNotes(ctx context.Context, s *Store, sid, project, filter string) ([]Note, error) {
	var statuses []string
	switch filter {
	case "":
		return s.DefaultNoteGlance(ctx, sid, project)
	case "all":
		statuses = AllNoteStatuses
	case NoteStatusActive, NoteStatusPending, NoteStatusReady, NoteStatusDismissed:
		statuses = []string{filter}
	default:
		return s.DefaultNoteGlance(ctx, sid, project)
	}
	sess, err := s.GetNotes(ctx, NoteQuery{SessionID: sid, Type: NoteTypeSession, Statuses: statuses})
	if err != nil {
		return nil, err
	}
	smart, err := s.GetNotes(ctx, NoteQuery{ProjectPath: project, Type: NoteTypeSmart, Statuses: statuses})
	if err != nil {
		return nil, err
	}
	return append(sess, smart...), nil
}
