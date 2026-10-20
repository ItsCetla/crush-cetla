// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): features/magic-context/dreamer.

// The dreamer: scheduled background maintenance (user-profile review,
// memory classification and expiry, smart-note evaluation, primer
// promotion). Ported from cortexkit/magic-context (MIT):
// features/magic-context/dreamer.
//
// Gap vs upstream: tasks that need a tool-using agent loop over the
// repository (map-memories, verify, verify-broad, curate, compress-cues,
// retrospective, maintain-docs, refresh-primers) are reported as skipped.
// Only single-shot Generator tasks and pure database tasks run.
package magiccontext

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	//go:embed prompts/review_user_memories.md
	reviewUserMemoriesTemplateRaw string
	//go:embed prompts/classify.md
	classifySystemPrompt string
)

func reviewUserMemoriesTemplate() string {
	return strings.TrimSuffix(reviewUserMemoriesTemplateRaw, "\n")
}

// Dreamer task names in run order.
const (
	DreamTaskMapMemories        = "map-memories"
	DreamTaskVerify             = "verify"
	DreamTaskVerifyBroad        = "verify-broad"
	DreamTaskCurate             = "curate"
	DreamTaskCompressCues       = "compress-cues"
	DreamTaskClassifyMemories   = "classify-memories"
	DreamTaskRetrospective      = "retrospective"
	DreamTaskMaintainDocs       = "maintain-docs"
	DreamTaskEvaluateSmartNotes = "evaluate-smart-notes"
	DreamTaskReviewUserMemories = "review-user-memories"
	DreamTaskPromotePrimers     = "promote-primers"
	DreamTaskRefreshPrimers     = "refresh-primers"
)

// DreamTaskOrder is the canonical task order.
var DreamTaskOrder = []string{
	DreamTaskMapMemories, DreamTaskVerify, DreamTaskVerifyBroad, DreamTaskCurate, DreamTaskCompressCues, DreamTaskClassifyMemories,
	DreamTaskRetrospective, DreamTaskMaintainDocs, DreamTaskEvaluateSmartNotes, DreamTaskReviewUserMemories, DreamTaskPromotePrimers, DreamTaskRefreshPrimers,
}

// DreamDefaultSchedules are the default crons ("" = disabled by default).
var DreamDefaultSchedules = map[string]string{
	DreamTaskMapMemories: "0 2 * * *", DreamTaskVerify: "0 3 * * *", DreamTaskVerifyBroad: "0 4 * * 0", DreamTaskCurate: "0 4 * * 0",
	DreamTaskCompressCues: "0 4 * * *", DreamTaskClassifyMemories: "0 6 * * *", DreamTaskRetrospective: "0 5 * * *", DreamTaskMaintainDocs: "",
	DreamTaskEvaluateSmartNotes: "0 3 * * *", DreamTaskReviewUserMemories: "0 3 * * *", DreamTaskPromotePrimers: "0 3 * * *", DreamTaskRefreshPrimers: "0 3 * * *",
}

// dreamToolLoopTasks need an agent loop and are not ported.
var dreamToolLoopTasks = map[string]bool{
	DreamTaskMapMemories: true, DreamTaskVerify: true, DreamTaskVerifyBroad: true, DreamTaskCurate: true, DreamTaskCompressCues: true,
	DreamTaskRetrospective: true, DreamTaskMaintainDocs: true, DreamTaskRefreshPrimers: true,
}

// Dreamer constants.
const (
	DreamTickInterval           = 15 * time.Minute
	DreamLeaseDuration          = 2 * time.Minute
	DefaultUserMemoryThreshold  = 3
	UserMemoryCandidateMaxAge   = 30 * 24 * time.Hour
	classifyMinPool             = 10
	classifyFullPoolCeiling     = 100
	classifyAnchorCount         = 30
	classifyChunkSize           = 100
	dreamMaxRetries             = 3
	DreamerSystemPrompt         = "You are a background maintenance agent for the magic-context system, running during a scheduled dream window. Your task and its full instructions arrive in the message below. Never read or quote secrets from .env, credentials, or key files, and never commit — the user handles git."
	ReviewUserMemoriesSysPrompt = "You are a user-profile reviewer for the magic-context system. You run during a scheduled dream window to decide which recurring behavioral observations about the human user are real, persistent patterns worth keeping in their global user profile.\n\nYou do NOT call any tools and you do NOT touch project memories — you read the candidate observations the host gives you and return a JSON verdict. Distill durable patterns; never transcribe a single moment. Phrase promoted or updated memories as concise, present-tense verb-first statements without a subject (for example, \"Prefers concise explanations\" or \"Expects changes to be tested\"). Output only the JSON the task asks for, with no surrounding prose."
)

// DreamDeps wires a dream run.
type DreamDeps struct {
	Store       *Store
	Settings    Settings
	ProjectPath string // ProjectKey of the working directory
	// Generator is the dreamer side model (nil = model tasks fail with
	// ErrNoGenerator; database-only tasks still run).
	Generator Generator
	// Embedder/EmbeddingModelID optionally embed primer candidates.
	Embedder         Embedder
	EmbeddingModelID string
	HolderID         string
	Now              func() time.Time
}

func (d DreamDeps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// Dream task statuses.
const (
	DreamStatusOK      = "ok"
	DreamStatusSkipped = "skipped"
	DreamStatusFailed  = "failed"
)

// DreamTaskResult is one task's outcome.
type DreamTaskResult struct {
	Task   string
	Status string
	Detail string
	Err    string
}

// DreamReport summarizes RunDream.
type DreamReport struct {
	RunID              int64
	ProjectPath        string
	StartedAt          int64
	FinishedAt         int64
	Tasks              []DreamTaskResult
	Succeeded          int
	Failed             int
	SmartNotesSurfaced int
	SmartNotesPending  int
	MemoryChanges      map[string]int
	UserMemory         *UserMemoryReviewResult
	Primers            *PrimerPromotionResult
}

// DreamTaskSchedule resolves a task's cron (settings override, else the
// default; "" = disabled).
func DreamTaskSchedule(s Settings, task string) string {
	if o, ok := s.DreamerTasks[task]; ok && o.Schedule != nil {
		return strings.TrimSpace(*o.Schedule)
	}
	return DreamDefaultSchedules[task]
}

// dreamTaskGate reports why a task cannot run ("" = allowed).
func dreamTaskGate(s Settings, task string) string {
	if !s.Dreamer {
		return "dreamer disabled"
	}
	switch task {
	case DreamTaskEvaluateSmartNotes:
		if !s.SmartNotes {
			return "smart notes disabled"
		}
	case DreamTaskReviewUserMemories:
	default:
		if !s.MemoryEnabled {
			return "memory disabled"
		}
	}
	if dreamToolLoopTasks[task] {
		return "requires a tool-loop agent (not ported)"
	}
	return ""
}

func dreamLeaseKey(task, project string) string {
	switch task {
	case DreamTaskReviewUserMemories:
		return "lease:user-memories"
	case DreamTaskEvaluateSmartNotes:
		return "lease:evaluate-smart-notes:" + project
	case DreamTaskMaintainDocs:
		return "lease:maintain-docs:" + project
	}
	return "lease:memory:" + project
}

type dreamLease struct {
	Holder string `json:"holder"`
	Until  int64  `json:"until"`
}

func (s *Store) dreamAcquireLease(ctx context.Context, key, holder string, now time.Time) (bool, error) {
	ok := false
	err := s.Immediate(ctx, func(q querier) error {
		var v string
		err := q.QueryRowContext(ctx, `SELECT value FROM mc_dream_state WHERE key = ?`, key).Scan(&v)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var l dreamLease
		_ = json.Unmarshal([]byte(v), &l)
		if l.Holder != "" && l.Holder != holder && l.Until > now.UnixMilli() {
			return nil
		}
		b, _ := json.Marshal(dreamLease{Holder: holder, Until: now.Add(DreamLeaseDuration).UnixMilli()})
		_, err = q.ExecContext(ctx, `INSERT INTO mc_dream_state (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, string(b))
		ok = err == nil
		return err
	})
	return ok, err
}

func (s *Store) dreamReleaseLease(ctx context.Context, key, holder string) error {
	return s.Immediate(ctx, func(q querier) error {
		var v string
		if err := q.QueryRowContext(ctx, `SELECT value FROM mc_dream_state WHERE key = ?`, key).Scan(&v); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		var l dreamLease
		if json.Unmarshal([]byte(v), &l) == nil && l.Holder != holder {
			return nil
		}
		_, err := q.ExecContext(ctx, `DELETE FROM mc_dream_state WHERE key = ?`, key)
		return err
	})
}

// dreamScheduleRow is one mc_task_schedule_state row.
type dreamScheduleRow struct {
	nextDueAt  int64
	schedule   string
	retryCount int
	found      bool
}

func dreamLoadSchedule(ctx context.Context, q querier, project, task string) (dreamScheduleRow, error) {
	var r dreamScheduleRow
	err := q.QueryRowContext(ctx, `SELECT next_due_at, schedule, retry_count FROM mc_task_schedule_state WHERE project_path = ? AND task = ?`, project, task).
		Scan(&r.nextDueAt, &r.schedule, &r.retryCount)
	if errors.Is(err, sql.ErrNoRows) {
		return r, nil
	}
	r.found = err == nil
	return r, err
}

// DreamDue returns the enabled tasks whose next_due_at has passed, in task
// order. A task seen for the first time (or whose schedule changed) is
// scheduled for its next occurrence instead of running immediately.
func DreamDue(ctx context.Context, deps DreamDeps) ([]string, error) {
	if !deps.Settings.Dreamer || deps.Store == nil {
		return nil, nil
	}
	now := deps.now().UnixMilli()
	var due []string
	for _, task := range DreamTaskOrder {
		sched := DreamTaskSchedule(deps.Settings, task)
		if sched == "" || dreamTaskGate(deps.Settings, task) != "" {
			continue
		}
		row, err := dreamLoadSchedule(ctx, deps.Store.db, deps.ProjectPath, task)
		if err != nil {
			return nil, err
		}
		if !row.found || row.schedule != sched || row.nextDueAt == 0 {
			next := NextDueAtMs(sched, now)
			if _, err := deps.Store.db.ExecContext(ctx, `INSERT INTO mc_task_schedule_state (project_path, task, next_due_at, schedule) VALUES (?,?,?,?)
ON CONFLICT(project_path, task) DO UPDATE SET next_due_at = excluded.next_due_at, schedule = excluded.schedule`, deps.ProjectPath, task, next, sched); err != nil {
				return nil, err
			}
			continue
		}
		if row.nextDueAt <= now {
			due = append(due, task)
		}
	}
	return due, nil
}

// RunDueDreams runs the due tasks (one scheduler tick). Hosts call it every
// DreamTickInterval while a session for the project is open.
func RunDueDreams(ctx context.Context, deps DreamDeps) (DreamReport, error) {
	tasks, err := DreamDue(ctx, deps)
	if err != nil || len(tasks) == 0 {
		return DreamReport{ProjectPath: deps.ProjectPath}, err
	}
	return RunDream(ctx, deps, tasks)
}

func dreamAdvanceSchedule(ctx context.Context, s *Store, project, task, sched, status, errText string, now time.Time) error {
	row, err := dreamLoadSchedule(ctx, s.db, project, task)
	if err != nil {
		return err
	}
	nowMs := now.UnixMilli()
	next := NextDueAtMs(sched, nowMs)
	retry := 0
	if status == DreamStatusFailed {
		retry = row.retryCount + 1
		if retry > dreamMaxRetries {
			retry = 0 // give up until the next cron occurrence
		} else if r := nowMs + (DreamTickInterval * time.Duration(retry)).Milliseconds(); next == 0 || r < next {
			next = r
		}
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO mc_task_schedule_state (project_path, task, last_run_at, next_due_at, schedule, last_status, last_error, retry_count) VALUES (?,?,?,?,?,?,?,?)
ON CONFLICT(project_path, task) DO UPDATE SET last_run_at = excluded.last_run_at, next_due_at = excluded.next_due_at, schedule = excluded.schedule,
 last_status = excluded.last_status, last_error = excluded.last_error, retry_count = excluded.retry_count`,
		project, task, nowMs, next, sched, status, errText, retry)
	return err
}

// RunDream runs the named tasks (in canonical order) under per-kind leases,
// records an mc_dream_runs row and advances each task's schedule. Memory
// expiry runs first whenever memory is enabled.
func RunDream(ctx context.Context, deps DreamDeps, tasks []string) (DreamReport, error) {
	rep := DreamReport{ProjectPath: deps.ProjectPath, MemoryChanges: map[string]int{}}
	if deps.Store == nil {
		return rep, errors.New("magiccontext: dream needs a store")
	}
	if !deps.Settings.Dreamer {
		return rep, nil
	}
	s := deps.Store
	holder := deps.HolderID
	if holder == "" {
		holder = fmt.Sprintf("dream-%d", deps.now().UnixNano())
	}
	want := map[string]bool{}
	for _, t := range tasks {
		want[t] = true
	}
	rep.StartedAt = deps.now().UnixMilli()
	if deps.Settings.MemoryEnabled && deps.ProjectPath != "" {
		n, err := dreamExpireMemories(ctx, s, deps.ProjectPath, deps.now())
		if err != nil {
			return rep, err
		}
		if n > 0 {
			rep.MemoryChanges["expired"] = n
		}
	}
	for _, task := range DreamTaskOrder {
		if !want[task] {
			continue
		}
		r := DreamTaskResult{Task: task}
		if why := dreamTaskGate(deps.Settings, task); why != "" {
			r.Status, r.Detail = DreamStatusSkipped, why
			rep.Tasks = append(rep.Tasks, r)
			if dreamToolLoopTasks[task] {
				if err := dreamAdvanceSchedule(ctx, s, deps.ProjectPath, task, DreamTaskSchedule(deps.Settings, task), DreamStatusSkipped, why, deps.now()); err != nil {
					return rep, err
				}
			}
			continue
		}
		key := dreamLeaseKey(task, deps.ProjectPath)
		ok, err := s.dreamAcquireLease(ctx, key, holder, deps.now())
		if err != nil {
			return rep, err
		}
		if !ok {
			r.Status, r.Detail = DreamStatusSkipped, "lease held by another runner"
			rep.Tasks = append(rep.Tasks, r)
			continue
		}
		detail, terr := runDreamTask(ctx, deps, task, &rep)
		_ = s.dreamReleaseLease(context.WithoutCancel(ctx), key, holder)
		if terr != nil {
			r.Status, r.Err = DreamStatusFailed, terr.Error()
			rep.Failed++
		} else {
			r.Status, r.Detail = DreamStatusOK, detail
			rep.Succeeded++
		}
		rep.Tasks = append(rep.Tasks, r)
		if err := dreamAdvanceSchedule(ctx, s, deps.ProjectPath, task, DreamTaskSchedule(deps.Settings, task), r.Status, r.Err, deps.now()); err != nil {
			return rep, err
		}
	}
	rep.FinishedAt = deps.now().UnixMilli()
	names := make([]string, 0, len(rep.Tasks))
	for _, t := range rep.Tasks {
		names = append(names, t.Task)
	}
	tj, _ := json.Marshal(names)
	mj, _ := json.Marshal(rep.MemoryChanges)
	res, err := s.db.ExecContext(ctx, `INSERT INTO mc_dream_runs (project_path, started_at, finished_at, holder_id, tasks_json, tasks_succeeded, tasks_failed, smart_notes_surfaced, smart_notes_pending, memory_changes_json) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		deps.ProjectPath, rep.StartedAt, rep.FinishedAt, holder, string(tj), rep.Succeeded, rep.Failed, rep.SmartNotesSurfaced, rep.SmartNotesPending, string(mj))
	if err != nil {
		return rep, err
	}
	rep.RunID, _ = res.LastInsertId()
	return rep, nil
}

func runDreamTask(ctx context.Context, deps DreamDeps, task string, rep *DreamReport) (string, error) {
	s := deps.Store
	switch task {
	case DreamTaskReviewUserMemories:
		threshold := 0
		if o, ok := deps.Settings.DreamerTasks[task]; ok {
			threshold = o.PromotionThreshold
		}
		res, err := ReviewUserMemories(ctx, s, deps.Generator, threshold, deps.now())
		if err != nil {
			return "", err
		}
		rep.UserMemory = &res
		return fmt.Sprintf("promoted %d, merged %d, dismissed %d, consumed %d", res.Promoted, res.Merged, res.Dismissed, res.CandidatesConsumed), nil
	case DreamTaskClassifyMemories:
		n, err := ClassifyMemories(ctx, s, deps.Generator, deps.ProjectPath)
		if err != nil {
			return "", err
		}
		if n > 0 {
			rep.MemoryChanges["classified"] += n
		}
		return fmt.Sprintf("classified %d", n), nil
	case DreamTaskEvaluateSmartNotes:
		res, err := EvaluateSmartNotes(ctx, s, deps.Generator, deps.ProjectPath, DreamTaskSchedule(deps.Settings, task), deps.now())
		rep.SmartNotesSurfaced += res.Surfaced
		rep.SmartNotesPending = res.Pending
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("evaluated %d, surfaced %d, pending %d", res.Evaluated, res.Surfaced, res.Pending), nil
	case DreamTaskPromotePrimers:
		res, err := PromotePrimers(ctx, s, deps.ProjectPath, deps.Embedder, deps.EmbeddingModelID, deps.now())
		if err != nil {
			return "", err
		}
		rep.Primers = &res
		return fmt.Sprintf("promoted %d, updated %d, candidates %d, pruned %d", res.Promoted, res.Updated, res.Candidates, res.Pruned), nil
	}
	return "", fmt.Errorf("unknown dream task %q", task)
}

// dreamExpireMemories archives memories past expires_at.
func dreamExpireMemories(ctx context.Context, s *Store, project string, now time.Time) (int, error) {
	n := 0
	err := s.Immediate(ctx, func(q querier) error {
		mems, err := queryMemories(ctx, q, `SELECT `+memoryCols+` FROM mc_memories WHERE project_path = ? AND status IN ('active','permanent') AND expires_at > 0 AND expires_at < ?`,
			project, now.UnixMilli())
		if err != nil {
			return err
		}
		for _, m := range mems {
			if err := archiveMemory(ctx, q, m); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// ---------------------------------------------------------------------------
// review-user-memories

// UserMemoryReviewResult summarizes a review pass.
type UserMemoryReviewResult struct {
	Promoted           int
	Merged             int
	Dismissed          int
	CandidatesConsumed int
	Pruned             int
}

type userMemoryVerdict struct {
	Promote []struct {
		Content      string  `json:"content"`
		CandidateIDs []int64 `json:"candidate_ids"`
	} `json:"promote"`
	UpdateExisting []struct {
		MemoryID     int64   `json:"memory_id"`
		Content      string  `json:"content"`
		CandidateIDs []int64 `json:"candidate_ids"`
	} `json:"update_existing"`
	DismissExisting []struct {
		MemoryID int64  `json:"memory_id"`
		Reason   string `json:"reason"`
	} `json:"dismiss_existing"`
	ConsumeCandidateIDs []int64 `json:"consume_candidate_ids"`
}

var fencedJSONRe = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)```")

func parseUserMemoryVerdict(out string) (userMemoryVerdict, error) {
	var v userMemoryVerdict
	out = strings.TrimSpace(out)
	if out == "" {
		return v, errors.New("User memory review returned no output.")
	}
	body := ""
	if m := fencedJSONRe.FindStringSubmatch(out); m != nil {
		body = strings.TrimSpace(m[1])
	} else if i, j := strings.Index(out, "{"), strings.LastIndex(out, "}"); i >= 0 && j > i {
		body = out[i : j+1]
	}
	if body == "" {
		return v, errors.New("User memory review returned no JSON.")
	}
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		return v, errors.New("User memory review returned invalid JSON.")
	}
	return v, nil
}

// BuildUserMemoryReviewPrompt fills the review template.
func BuildUserMemoryReviewPrompt(stable []UserMemory, cands []UserMemoryCandidate, threshold int) string {
	var sl []string
	for _, m := range stable {
		sl = append(sl, fmt.Sprintf("- Memory #%d: %q", m.ID, m.Content))
	}
	stableList := "(none)"
	if len(sl) > 0 {
		stableList = strings.Join(sl, "\n")
	}
	var cl []string
	for _, c := range cands {
		sid := c.SessionID
		if r := []rune(sid); len(r) > 12 {
			sid = string(r[:12])
		}
		cl = append(cl, fmt.Sprintf("- Candidate #%d [session %s]: %q", c.ID, sid, c.Content))
	}
	return strings.NewReplacer("{{STABLE_LIST}}", stableList, "{{CANDIDATE_LIST}}", strings.Join(cl, "\n"),
		"{{PROMOTION_THRESHOLD}}", strconv.Itoa(threshold)).Replace(reviewUserMemoriesTemplate())
}

// ReviewUserMemories prunes stale candidates and, once enough have
// accumulated, asks the model which recurring observations to promote into
// the global user profile. threshold <= 0 uses the default (clamped 2–20).
func ReviewUserMemories(ctx context.Context, s *Store, gen Generator, threshold int, now time.Time) (UserMemoryReviewResult, error) {
	var res UserMemoryReviewResult
	if threshold <= 0 {
		threshold = DefaultUserMemoryThreshold
	}
	threshold = min(max(threshold, 2), 20)
	r, err := s.db.ExecContext(ctx, `DELETE FROM mc_user_memory_candidates WHERE created_at < ?`, now.Add(-UserMemoryCandidateMaxAge).UnixMilli())
	if err != nil {
		return res, err
	}
	pruned, _ := r.RowsAffected()
	res.Pruned = int(pruned)
	cands, err := dreamUserCandidates(ctx, s.db)
	if err != nil || len(cands) < threshold {
		return res, err
	}
	if gen == nil {
		return res, ErrNoGenerator
	}
	stable, err := dreamActiveUserMemories(ctx, s.db)
	if err != nil {
		return res, err
	}
	out, err := gen(ctx, ReviewUserMemoriesSysPrompt, BuildUserMemoryReviewPrompt(stable, cands, threshold), 4096)
	if err != nil {
		return res, err
	}
	v, err := parseUserMemoryVerdict(out)
	if err != nil {
		return res, err
	}
	candSet := map[int64]bool{}
	for _, c := range cands {
		candSet[c.ID] = true
	}
	stableSet := map[int64]bool{}
	for _, m := range stable {
		stableSet[m.ID] = true
	}
	consumed := map[int64]bool{}
	addIDs := func(ids []int64) []int64 {
		var keep []int64
		for _, id := range ids {
			if candSet[id] {
				consumed[id] = true
				keep = append(keep, id)
			}
		}
		return keep
	}
	err = s.Immediate(ctx, func(q querier) error {
		nowMs := now.UnixMilli()
		changed := false
		for _, p := range v.Promote {
			c := strings.TrimSpace(p.Content)
			ids := addIDs(p.CandidateIDs)
			if c == "" {
				continue
			}
			src, _ := json.Marshal(nonNilIDs(ids))
			if _, err := q.ExecContext(ctx, `INSERT INTO mc_user_memories (content, status, promoted_at, source_candidate_ids, created_at, updated_at) VALUES (?, 'active', ?, ?, ?, ?)`,
				c, nowMs, string(src), nowMs, nowMs); err != nil {
				return err
			}
			res.Promoted++
			changed = true
		}
		for _, u := range v.UpdateExisting {
			c := strings.TrimSpace(u.Content)
			addIDs(u.CandidateIDs)
			if c == "" || !stableSet[u.MemoryID] {
				continue
			}
			if _, err := q.ExecContext(ctx, `UPDATE mc_user_memories SET content = ?, updated_at = ? WHERE id = ? AND status = 'active'`, c, nowMs, u.MemoryID); err != nil {
				return err
			}
			res.Merged++
			changed = true
		}
		for _, d := range v.DismissExisting {
			if !stableSet[d.MemoryID] {
				continue
			}
			if _, err := q.ExecContext(ctx, `UPDATE mc_user_memories SET status = 'dismissed', updated_at = ? WHERE id = ? AND status = 'active'`, nowMs, d.MemoryID); err != nil {
				return err
			}
			res.Dismissed++
			changed = true
		}
		addIDs(v.ConsumeCandidateIDs)
		for id := range consumed {
			if _, err := q.ExecContext(ctx, `DELETE FROM mc_user_memory_candidates WHERE id = ?`, id); err != nil {
				return err
			}
		}
		res.CandidatesConsumed = len(consumed)
		if changed {
			return dreamBumpProfileVersion(ctx, q)
		}
		return nil
	})
	return res, err
}

func nonNilIDs(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}

// ---------------------------------------------------------------------------
// classify-memories

type classifyVerdict struct {
	importance int
	scope      string
	shareable  *bool
}

var (
	classifyBlockRe = regexp.MustCompile(`(?s)<classify>(.*?)(</classify>|$)`)
	classifyEntryRe = regexp.MustCompile(`<memory\b([^>]*)/?>`)
	classifyIDRe    = regexp.MustCompile(`\bid\s*=\s*"(\d+)"`)
	classifyImpRe   = regexp.MustCompile(`\bimportance\s*=\s*"(\d+)"`)
	classifyScopeRe = regexp.MustCompile(`\bscope\s*=\s*"([^"]*)"`)
	classifyShareRe = regexp.MustCompile(`\bshareable\s*=\s*"([^"]*)"`)
)

func parseClassifyManifest(out string) (map[int64]classifyVerdict, error) {
	m := classifyBlockRe.FindStringSubmatch(out)
	if m == nil {
		return nil, errors.New("classify manifest missing <classify> block")
	}
	if m[2] == "" {
		return nil, errors.New("classify manifest missing </classify> close tag")
	}
	res := map[int64]classifyVerdict{}
	for _, e := range classifyEntryRe.FindAllStringSubmatch(m[1], -1) {
		attrs := e[1]
		idm := classifyIDRe.FindStringSubmatch(attrs)
		if idm == nil {
			return nil, errors.New("classify manifest entry missing numeric id")
		}
		id, _ := strconv.ParseInt(idm[1], 10, 64)
		var v classifyVerdict
		has := false
		if im := classifyImpRe.FindStringSubmatch(attrs); im != nil {
			n, _ := strconv.Atoi(im[1])
			v.importance = min(max(n, 1), 100)
			has = true
		}
		if sm := classifyScopeRe.FindStringSubmatch(attrs); sm != nil {
			switch sm[1] {
			case "project", "ecosystem", "universe":
				v.scope = sm[1]
				has = true
			default:
				return nil, fmt.Errorf("classify manifest invalid scope %s", sm[1])
			}
		}
		if sh := classifyShareRe.FindStringSubmatch(attrs); sh != nil {
			switch sh[1] {
			case "true", "1":
				b := true
				v.shareable, has = &b, true
			case "false", "0":
				b := false
				v.shareable, has = &b, true
			}
		}
		if !has {
			return nil, fmt.Errorf("classify manifest entry %d missing classification fields", id)
		}
		if _, dup := res[id]; dup {
			return nil, fmt.Errorf("classify manifest duplicate id %d", id)
		}
		res[id] = v
	}
	return res, nil
}

func classifyMemoryEntry(m Memory) string {
	imp := 50
	if m.Importance > 0 {
		imp = m.Importance
	}
	scope := m.Scope
	if scope == "" {
		scope = "project"
	}
	return fmt.Sprintf("[%d] %s (current: importance=%d scope=%s shareable=false)\n%s", m.ID, m.Category, imp, scope, m.Content)
}

// BuildClassifyPrompt renders one classification chunk.
func BuildClassifyPrompt(project string, anchors, pool []Memory) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Task: Classify Project Memories\n\n**Project:** %s\n\nScore EVERY memory in the pool below. Emit one <classify> manifest covering every id.\n\n", project)
	if len(anchors) > 0 {
		b.WriteString("### Already-classified reference memories (calibrate against these — do NOT re-score them, they are NOT in your output)\n")
		parts := make([]string, len(anchors))
		for i, a := range anchors {
			parts[i] = classifyMemoryEntry(a)
		}
		b.WriteString(strings.Join(parts, "\n\n") + "\n\n")
	}
	b.WriteString("### Memory pool to classify\n")
	parts := make([]string, len(pool))
	for i, m := range pool {
		parts[i] = classifyMemoryEntry(m)
	}
	b.WriteString(strings.Join(parts, "\n\n"))
	return b.String()
}

// ClassifyMemories scores unclassified project memories (importance and
// scope) in chunks. Pools under 10 are left alone; pools over 100 get 30
// stratified calibration anchors. It returns how many were classified.
func ClassifyMemories(ctx context.Context, s *Store, gen Generator, project string) (int, error) {
	all, err := s.ProjectMemories(ctx, project)
	if err != nil {
		return 0, err
	}
	var pool, classified []Memory
	for _, m := range all {
		if m.ClassifiedAt == 0 {
			pool = append(pool, m)
		} else {
			classified = append(classified, m)
		}
	}
	if len(pool) < classifyMinPool {
		return 0, nil
	}
	if gen == nil {
		return 0, ErrNoGenerator
	}
	sort.Slice(pool, func(i, j int) bool { return pool[i].ID < pool[j].ID })
	var anchors []Memory
	if len(pool) > classifyFullPoolCeiling && len(classified) > 0 {
		imp := func(m Memory) int {
			if m.Importance > 0 {
				return m.Importance
			}
			return 50
		}
		sort.SliceStable(classified, func(i, j int) bool {
			if imp(classified[i]) != imp(classified[j]) {
				return imp(classified[i]) > imp(classified[j])
			}
			return classified[i].ID < classified[j].ID
		})
		count := min(classifyAnchorCount, len(classified))
		step := float64(len(classified)) / float64(count)
		for i := range count {
			anchors = append(anchors, classified[int(float64(i)*step)])
		}
	}
	done := 0
	for start := 0; start < len(pool); start += classifyChunkSize {
		chunk := pool[start:min(len(pool), start+classifyChunkSize)]
		out, err := gen(ctx, classifySystemPrompt, BuildClassifyPrompt(project, anchors, chunk), 8192)
		if err != nil {
			return done, err
		}
		verdicts, err := parseClassifyManifest(out)
		if err != nil {
			return done, err
		}
		if len(verdicts) != len(chunk) {
			return done, fmt.Errorf("classify manifest covered %d of %d memories", len(verdicts), len(chunk))
		}
		for _, m := range chunk {
			if _, ok := verdicts[m.ID]; !ok {
				return done, fmt.Errorf("classify manifest missing memory %d", m.ID)
			}
		}
		err = s.Immediate(ctx, func(q querier) error {
			now := nowMS()
			for _, m := range chunk {
				v := verdicts[m.ID]
				sets := []string{"classified_at = ?"}
				args := []any{now}
				if v.importance > 0 {
					sets = append(sets, "importance = ?")
					args = append(args, v.importance)
				}
				if v.scope != "" {
					sets = append(sets, "scope = ?")
					args = append(args, v.scope)
				}
				args = append(args, m.ID)
				if _, err := q.ExecContext(ctx, `UPDATE mc_memories SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return done, err
		}
		done += len(chunk)
	}
	return done, nil
}

// userObservationCollection reports whether historian user observations are
// stored as review candidates: the dreamer is on and review-user-memories is
// scheduled (upstream userObservationCollectionEnabled).
func userObservationCollection(set Settings) bool {
	return set.Dreamer && DreamTaskSchedule(set, DreamTaskReviewUserMemories) != ""
}
