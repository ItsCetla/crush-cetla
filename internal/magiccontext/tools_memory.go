// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): tools/ctx-memory, tools/ctx-search.

// ctx_memory and ctx_search agent tools, ported from cortexkit/magic-context
// (MIT) tools/ctx-memory and tools/ctx-search.

package magiccontext

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"charm.land/fantasy"
)

// Tool names.
const (
	CtxMemoryToolName = "ctx_memory"
	CtxSearchToolName = "ctx_search"
)

const ctxMemoryDescription = "Durable facts about this project, shared with every agent working on it and kept for the months this work lasts.\n\n" +
	"Your active memories are already in <project-memory> as `#id: fact` lines. Write one when you learn something that must not have to be found again — a project rule, an architectural fact, a hard-won constraint, a config value, a naming convention — and especially when it cost you turns to find. One standalone fact per memory, phrased to make sense on its own. A pending intention with its evidence (\"do X later, here is what we know\") is ctx_note, not memory.\n\n" +
	"Actions:\n" +
	"- write: new memory (content + category).\n" +
	"- update: rewrite one memory whose fact changed (ids: [one], content; category optional to recategorize).\n" +
	"- archive: retire wrong or obsolete memories (ids: [one or more], optional reason).\n" +
	"- merge: collapse duplicates into one (ids: [two or more], content).\n" +
	"- get: fetch by id (ids: 1–20), readable in every status.\n" +
	"Examples: category=\"CONFIG_VALUES\", content=\"OpenCode source is at ~/Work/OSS/opencode\" · category=\"CONSTRAINTS\", content=\"Dashboard Tauri build needs RGBA PNGs, not grayscale\""

const ctxSearchDescription = "Search the archive — everything that ever happened in this project, not just what is on your desk.\n\n" +
	"Retrieval matches meaning and exact words and fuses them, so phrase `query` as a natural-language question that still carries the exact terms you expect in the answer (paths, symbols, config keys, error strings); a bare keyword stack finds less.\n" +
	"- \"where is the opencode source code path?\"  (a location you once knew)\n" +
	"- \"why did we choose SQLite over postgres?\"  (a decision and its reasons)\n" +
	"- \"how does the dreamer lease work?\"  (a mechanism discussed or implemented earlier)\n" +
	"- Not: \"upload client retry backoff config\"\n\n" +
	"Results only contain what you CANNOT currently see — memories already in <project-memory> and the live tail are filtered out. A query that is just memory ids (`#7234`, `12, 34`) resolves them directly.\n\n" +
	"Sources (omit for all):\n" +
	"- memory — rules, constraints, conventions; \"what's our convention for X\"\n" +
	"- message — the raw conversation behind compacted history; \"did we discuss this\"; hits carry ordinals for ctx_expand(start=N-10, end=N+5)\n" +
	"- git_commit — commit history; \"when did this change\" (pair with message for regression hunts)\n" +
	"- primer — reusable project Q&A the dreamer distils from recurring questions; \"have we answered this before\"\n" +
	"- note — parked follow-ups with their recorded text; \"did we leave a follow-up\"\n" +
	"Use from/to to restrict every source to an inclusive UTC date range."

// MemoryToolDeps wires ctx_memory to the engine.
type MemoryToolDeps struct {
	Store          *Store
	Settings       Settings
	ProjectKey     func(ctx context.Context) string
	SessionID      func(ctx context.Context) string
	Embedder       Embedder // nil = no embeddings
	EmbeddingModel string
	// AllowedActions restricts the actions (nil = write, update, archive,
	// merge, get).
	AllowedActions []string
}

// CtxMemoryParams are the ctx_memory arguments.
type CtxMemoryParams struct {
	Action   string    `json:"action" description:"write | update | archive | merge | get" enum:"write,update,archive,merge,get"`
	Content  string    `json:"content,omitempty" description:"The memory text — one standalone fact (write, update, merge)."`
	Category string    `json:"category,omitempty" description:"Kind of fact (required for write; on update/merge optional, omitted keeps the current category)." enum:"PROJECT_RULES,ARCHITECTURE,CONSTRAINTS,CONFIG_VALUES,NAMING"`
	IDs      []float64 `json:"ids,omitempty" description:"Memory ids from <project-memory>: one for update, one or more for archive, two or more for merge, 1–20 for get."`
	Reason   string    `json:"reason,omitempty" description:"Why it is being archived (optional)."`
}

// NewCtxMemoryTool builds the ctx_memory tool.
func NewCtxMemoryTool(deps MemoryToolDeps) fantasy.AgentTool {
	return fantasy.NewAgentTool(CtxMemoryToolName, ctxMemoryDescription,
		func(ctx context.Context, p CtxMemoryParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			return fantasy.NewTextResponse(runCtxMemory(ctx, deps, p)), nil
		})
}

// integerIDs keeps the positive integral ids, deduped in order.
func integerIDs(raw []float64) []int64 {
	var out []int64
	for _, f := range raw {
		if f <= 0 || f != math.Trunc(f) || f > math.MaxInt64/2 {
			continue
		}
		if id := int64(f); !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func runCtxMemory(ctx context.Context, deps MemoryToolDeps, p CtxMemoryParams) string {
	action := strings.TrimSpace(p.Action)
	allowed := deps.AllowedActions
	if allowed == nil {
		allowed = []string{"write", "update", "archive", "merge", "get"}
	}
	if action != "" && !slices.Contains(allowed, action) {
		return fmt.Sprintf("Error: Action '%s' is not allowed in this context.", action)
	}
	if !deps.Settings.MemoryEnabled || deps.Store == nil {
		return "Cross-session memory is disabled for this project."
	}
	project := ""
	if deps.ProjectKey != nil {
		project = deps.ProjectKey(ctx)
	}
	if project == "" {
		return "Cross-session memory is disabled for this project."
	}
	content := strings.TrimSpace(p.Content)
	category := strings.TrimSpace(p.Category)
	var (
		out string
		err error
	)
	switch action {
	case "write":
		out, err = memoryWrite(ctx, deps, project, content, category)
	case "get":
		out, err = memoryGet(ctx, deps, project, p.IDs)
	case "update":
		out, err = memoryUpdate(ctx, deps, project, p.IDs, content, category)
	case "merge":
		out, err = memoryMerge(ctx, deps, project, p.IDs, content, category)
	case "archive":
		out, err = memoryArchive(ctx, deps, project, p.IDs, strings.TrimSpace(p.Reason))
	default:
		return "Error: Unknown action."
	}
	if err != nil {
		return "Error: " + err.Error()
	}
	return out
}

func (d MemoryToolDeps) embedAsync(mems ...Memory) {
	if d.Embedder == nil || d.EmbeddingModel == "" || len(mems) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_ = d.Store.EmbedMemories(ctx, d.Embedder, d.EmbeddingModel, mems)
	}()
}

func memoryWrite(ctx context.Context, deps MemoryToolDeps, project, content, category string) (string, error) {
	if content == "" {
		return "Error: 'content' is required when action is 'write'.", nil
	}
	if category == "" {
		return "Error: 'category' is required when action is 'write'.", nil
	}
	if !IsMemoryCategory(category) {
		return fmt.Sprintf("Error: Unknown memory category '%s'.", category), nil
	}
	sid := ""
	if deps.SessionID != nil {
		sid = deps.SessionID(ctx)
	}
	m, created, err := deps.Store.AddMemory(ctx, MemoryInput{ProjectKey: project, Category: category, Content: content, SourceSessionID: sid, SourceType: MemorySourceAgent})
	if err != nil {
		return "", err
	}
	if !created {
		return fmt.Sprintf("Memory already exists [ID: %d] in %s (seen count incremented).", m.ID, m.Category), nil
	}
	deps.embedAsync(m)
	return fmt.Sprintf("Saved memory [ID: %d] in %s.", m.ID, m.Category), nil
}

func memoryGet(ctx context.Context, deps MemoryToolDeps, project string, raw []float64) (string, error) {
	ids := integerIDs(raw)
	if len(ids) == 0 {
		return "Error: 'ids' must contain at least one integer memory ID when action is 'get'.", nil
	}
	if len(ids) > 20 {
		return fmt.Sprintf("Error: 'ids' must contain at most 20 memory IDs when action is 'get' (got %d).", len(ids)), nil
	}
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		m, ok, err := deps.Store.GetMemory(ctx, id)
		if err != nil {
			return "", err
		}
		if !ok || m.ProjectPath != project {
			parts = append(parts, fmt.Sprintf("id %d: not found or not visible from this project", id))
			continue
		}
		parts = append(parts, FormatMemoryList([]Memory{m}))
	}
	return strings.Join(parts, "\n\n"), nil
}

func inactiveError(id int64, verb string) string {
	return fmt.Sprintf("Error: Memory with ID %d is archived or superseded; restore it before %s.", id, verb)
}

func loadEditable(ctx context.Context, q querier, project string, id int64, verb string) (Memory, string, error) {
	m, ok, err := memoryByID(ctx, q, id)
	if err != nil {
		return Memory{}, "", err
	}
	if !ok || m.ProjectPath != project {
		return Memory{}, fmt.Sprintf("Error: Memory with ID %d was not found.", id), nil
	}
	if !m.Active() {
		return Memory{}, inactiveError(id, verb), nil
	}
	return m, "", nil
}

func memoryUpdate(ctx context.Context, deps MemoryToolDeps, project string, raw []float64, content, category string) (string, error) {
	ids := integerIDs(raw)
	if len(ids) != 1 || len(raw) != 1 {
		return "Error: 'ids' must contain exactly one integer memory ID when action is 'update'.", nil
	}
	if content == "" {
		return "Error: 'content' is required when action is 'update'.", nil
	}
	var reply string
	var updated Memory
	err := deps.Store.Immediate(ctx, func(q querier) error {
		m, msg, err := loadEditable(ctx, q, project, ids[0], "updating")
		if err != nil || msg != "" {
			reply = msg
			return err
		}
		cat := m.Category
		if isV2Category(category) {
			cat = category
		}
		if dup, ok, err := memoryByHash(ctx, q, project, cat, MemoryHash(content)); err != nil {
			return err
		} else if ok && dup.ID != m.ID {
			reply = fmt.Sprintf("Error: Memory content already exists as ID %d; merge or archive duplicates instead.", dup.ID)
			return nil
		}
		if err := rewriteMemory(ctx, q, m, content, cat); err != nil {
			return err
		}
		updated, _, err = memoryByID(ctx, q, m.ID)
		reply = fmt.Sprintf("Updated memory [ID: %d] in %s.", m.ID, cat)
		return err
	})
	if err != nil {
		return "", err
	}
	if updated.ID != 0 {
		deps.embedAsync(updated)
	}
	return reply, nil
}

func memoryMerge(ctx context.Context, deps MemoryToolDeps, project string, raw []float64, content, category string) (string, error) {
	if len(raw) < 2 {
		return "Error: 'ids' must include at least two integer memory IDs when action is 'merge'.", nil
	}
	ids := integerIDs(raw)
	if len(ids) < 2 {
		return "Error: 'ids' must include at least two distinct memory IDs when action is 'merge'.", nil
	}
	if content == "" {
		return "Error: 'content' is required when action is 'merge'.", nil
	}
	var reply string
	var embedMe Memory
	err := deps.Store.Immediate(ctx, func(q querier) error {
		var sources []Memory
		for _, id := range ids {
			m, ok, err := memoryByID(ctx, q, id)
			if err != nil {
				return err
			}
			if !ok || m.ProjectPath != project {
				reply = "Error: One or more source memories were not found."
				return nil
			}
			if !m.Active() {
				reply = inactiveError(id, "merging")
				return nil
			}
			sources = append(sources, m)
		}
		cat := sources[0].Category
		for _, m := range sources[1:] {
			if m.Category != cat {
				reply = fmt.Sprintf("Error: Cannot merge memories from different categories (%s, %s). If they are genuine duplicates, one is miscategorized — archive the redundant one instead of merging across categories.", cat, m.Category)
				return nil
			}
		}
		if isV2Category(category) {
			cat = category
		}
		hash := MemoryHash(content)
		var canonical Memory
		existed := false
		if dup, ok, err := memoryByHash(ctx, q, project, cat, hash); err != nil {
			return err
		} else if ok {
			if !slices.Contains(ids, dup.ID) {
				reply = fmt.Sprintf("Error: Memory content already exists as ID %d; update or archive existing duplicates instead.", dup.ID)
				return nil
			}
			canonical, existed = dup, true
		}
		if !existed {
			sid := ""
			if deps.SessionID != nil {
				sid = deps.SessionID(ctx)
			}
			m, _, err := addMemory(ctx, q, MemoryInput{ProjectKey: project, Category: cat, Content: content, SourceSessionID: sid, SourceType: MemorySourceAgent})
			if err != nil {
				return err
			}
			canonical = m
		}
		if err := mergeMemoryStats(ctx, q, canonical.ID, sources); err != nil {
			return err
		}
		var superseded []int64
		for _, m := range sources {
			if m.ID == canonical.ID {
				continue
			}
			if err := supersedeMemory(ctx, q, m, canonical.ID); err != nil {
				return err
			}
			superseded = append(superseded, m.ID)
		}
		if existed && (canonical.Content != content || canonical.Category != cat) {
			if err := rewriteMemory(ctx, q, canonical, content, cat); err != nil {
				return err
			}
		}
		m, _, err := memoryByID(ctx, q, canonical.ID)
		if err != nil {
			return err
		}
		embedMe = m
		reply = fmt.Sprintf("Merged memories [%s] into canonical memory [ID: %d] in %s; superseded [%s].", joinIDs(ids), canonical.ID, cat, joinIDs(superseded))
		return nil
	})
	if err != nil {
		return "", err
	}
	if embedMe.ID != 0 {
		deps.embedAsync(embedMe)
	}
	return reply, nil
}

func memoryArchive(ctx context.Context, deps MemoryToolDeps, project string, raw []float64, reason string) (string, error) {
	ids := integerIDs(raw)
	if len(ids) == 0 {
		return "Error: 'ids' must contain at least one integer memory ID when action is 'archive'.", nil
	}
	var reply string
	err := deps.Store.Immediate(ctx, func(q querier) error {
		var mems []Memory
		for _, id := range ids {
			m, msg, err := loadEditable(ctx, q, project, id, "archiving")
			if err != nil || msg != "" {
				reply = msg
				return err
			}
			mems = append(mems, m)
		}
		for _, m := range mems {
			if err := archiveMemory(ctx, q, m); err != nil {
				return err
			}
		}
		noun := "memories"
		if len(ids) == 1 {
			noun = "memory"
		}
		reply = fmt.Sprintf("Archived %s [ID: %s].", noun, joinIDs(ids))
		if reason != "" {
			reply = fmt.Sprintf("Archived %s [ID: %s] (%s).", noun, joinIDs(ids), reason)
		}
		return nil
	})
	return reply, err
}

// ---------------------------------------------------------------------------
// ctx_search

// SearchToolDeps wires ctx_search to the engine.
type SearchToolDeps struct {
	Store            *Store
	Settings         Settings
	ProjectKey       func(ctx context.Context) string
	SessionID        func(ctx context.Context) string
	VisibleMemoryIDs func(ctx context.Context) []int64
	Embedder         Embedder
	EmbeddingModel   string
	ExtraSources     map[string]SearchSourceFunc // primer / note providers
}

// CtxSearchParams are the ctx_search arguments.
type CtxSearchParams struct {
	Query   string   `json:"query" description:"A natural-language question carrying the exact terms you expect in the answer."`
	Limit   float64  `json:"limit,omitempty" description:"Maximum results (default 10)."`
	From    string   `json:"from,omitempty" description:"Earliest date, YYYY-MM-DD (inclusive)."`
	To      string   `json:"to,omitempty" description:"Latest date, YYYY-MM-DD (inclusive; default open)."`
	Sources []string `json:"sources,omitempty" description:"Restrict to these sources (memory, message, git_commit, primer, note); omitting it or passing [] searches every source."`
}

// NewCtxSearchTool builds the ctx_search tool.
func NewCtxSearchTool(deps SearchToolDeps) fantasy.AgentTool {
	return fantasy.NewAgentTool(CtxSearchToolName, ctxSearchDescription,
		func(ctx context.Context, p CtxSearchParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			out, err := runCtxSearch(ctx, deps, p)
			if err != nil {
				return fantasy.NewTextErrorResponse("Error: " + err.Error()), nil
			}
			return fantasy.NewTextResponse(out), nil
		})
}

// parseSearchDate parses YYYY-MM-DD (UTC day; end of day when isTo) or a
// full ISO datetime into ms.
func parseSearchDate(s string, isTo bool) (int64, bool) {
	s = strings.TrimSpace(s)
	if t, err := time.ParseInLocation("2006-01-02", s, time.UTC); err == nil {
		ms := t.UnixMilli()
		if isTo {
			ms += 86400000 - 1
		}
		return ms, true
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t.UnixMilli(), true
		}
	}
	return 0, false
}

func runCtxSearch(ctx context.Context, deps SearchToolDeps, p CtxSearchParams) (string, error) {
	query := strings.TrimSpace(p.Query)
	if query == "" {
		return "Error: 'query' is required.", nil
	}
	var from, to int64
	if strings.TrimSpace(p.From) != "" {
		v, ok := parseSearchDate(p.From, false)
		if !ok {
			return "Error: Invalid 'from' date; use YYYY-MM-DD or a full ISO datetime.", nil
		}
		from = v
	}
	if strings.TrimSpace(p.To) != "" {
		v, ok := parseSearchDate(p.To, true)
		if !ok {
			return "Error: Invalid 'to' date; use YYYY-MM-DD or a full ISO datetime.", nil
		}
		to = v
	}
	if to != 0 && from > to {
		return "Error: Invalid date range; 'from' must be on or before 'to'.", nil
	}
	limit := searchDefaultLimit
	if p.Limit > 0 {
		limit = max(1, int(math.Floor(p.Limit)))
	}
	limit = min(limit, searchMaxLimit)
	var project, session string
	if deps.ProjectKey != nil {
		project = deps.ProjectKey(ctx)
	}
	if deps.SessionID != nil {
		session = deps.SessionID(ctx)
	}
	if deps.Store == nil {
		return "", fmt.Errorf("search store unavailable")
	}

	if ids := parseIDQuery(query); len(ids) > 0 && deps.Settings.MemoryEnabled && project != "" {
		mems, err := deps.Store.MemoriesByIDs(ctx, project, ids)
		if err != nil {
			return "", err
		}
		if len(mems) > 0 {
			var res SearchResult
			for i, m := range mems {
				res.Hits = append(res.Hits, SearchHit{Source: SourceMemory, Score: 1 - float64(i)*0.01, MatchType: "fts", Content: m.Content, MemoryID: m.ID, Category: m.Category, CreatedAt: m.CreatedAt})
			}
			return FormatSearchResults(query, res), nil
		}
	}

	var sources []string
	for _, s := range p.Sources {
		if s = strings.TrimSpace(s); slices.Contains(AllSearchSources, s) && !slices.Contains(sources, s) {
			sources = append(sources, s)
		}
	}
	var visible []int64
	if deps.VisibleMemoryIDs != nil {
		visible = deps.VisibleMemoryIDs(ctx)
	}
	model := deps.EmbeddingModel
	if model == "" {
		model = deps.Settings.EmbeddingModel
	}
	extra := deps.ExtraSources
	if extra == nil {
		extra = deps.Store.DefaultSearchSources()
	}
	res, err := deps.Store.Search(ctx, SearchRequest{
		ProjectKey: project, SessionID: session, Query: query, Limit: limit, Sources: sources,
		From: from, To: to, Explicit: true, VisibleMemoryIDs: visible,
		MemoryEnabled: deps.Settings.MemoryEnabled, Embedder: deps.Embedder, EmbeddingModel: model,
		ExtraSources: extra,
	})
	if err != nil {
		return "", err
	}
	return FormatSearchResults(query, res), nil
}
