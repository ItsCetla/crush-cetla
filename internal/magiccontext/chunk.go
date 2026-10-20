// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): hooks/magic-context/read-session-chunk.ts, read-session-formatting.ts, read-session-raw.ts.

package magiccontext

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/charmbracelet/crush/internal/message"
)

// RawMessage is the flattened, ordinal-addressed view of one stored message
// that every Magic Context stage reads (historian chunking, the protected
// tail, tagging). Ordinals are stable, 1-based and cover every stored message
// including tool-result messages.
type RawMessage struct {
	Ordinal     int
	ID          string
	Role        message.MessageRole
	CreatedAt   int64
	Texts       []string // visible text parts (hidden continuations excluded)
	Hidden      bool     // user message made only of hidden continuations
	ToolCalls   []message.ToolCall
	ToolResults []message.ToolResult
	Tokens      int // estimated tokens of the message as sent to the model
}

// MeaningfulUser reports whether the message is a real user turn.
func (m RawMessage) MeaningfulUser() bool {
	if m.Role != message.User {
		return false
	}
	for _, t := range m.Texts {
		if strings.TrimSpace(removeSystemReminders(t)) != "" {
			return true
		}
	}
	return false
}

// EstimateTokens is the shared heuristic token estimate (ceil(len/3.5)).
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return int(math.Ceil(float64(len(s)) / 3.5))
}

const imageTokenEstimate = 1500

// BuildRawView converts stored messages into raw messages. ordinals maps
// message id to ordinal; messages missing from it are skipped.
func BuildRawView(msgs []message.Message, ordinals map[string]int) []RawMessage {
	out := make([]RawMessage, 0, len(msgs))
	for _, m := range msgs {
		ord, ok := ordinals[m.ID]
		if !ok {
			continue
		}
		rm := RawMessage{Ordinal: ord, ID: m.ID, Role: m.Role, CreatedAt: m.CreatedAt}
		tokens := 0
		hiddenOnly := m.Role == message.User
		for _, p := range m.Parts {
			switch c := p.(type) {
			case message.TextContent:
				tokens += EstimateTokens(c.Text)
				if c.Hidden {
					continue
				}
				hiddenOnly = false
				rm.Texts = append(rm.Texts, c.Text)
			case message.ReasoningContent:
				tokens += EstimateTokens(c.Thinking)
			case message.ToolCall:
				rm.ToolCalls = append(rm.ToolCalls, c)
				tokens += EstimateTokens(c.Name) + EstimateTokens(c.Input)
			case message.ToolResult:
				rm.ToolResults = append(rm.ToolResults, c)
				tokens += EstimateTokens(c.Content)
				if c.Data != "" {
					tokens += imageTokenEstimate
				}
			case message.BinaryContent:
				hiddenOnly = false
				tokens += imageTokenEstimate
			case message.ImageURLContent:
				hiddenOnly = false
				tokens += imageTokenEstimate
			}
		}
		rm.Hidden = hiddenOnly && len(rm.Texts) == 0
		rm.Tokens = tokens + 4
		out = append(out, rm)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Ordinal < out[j].Ordinal })
	return out
}

// ---------------------------------------------------------------------------
// Token index

// TokenIndex is a prefix-sum over raw message tokens, addressed by ordinal.
type TokenIndex struct {
	prefix []int // prefix[k] = tokens of ordinals 1..k
	count  int
}

// NewTokenIndex builds the index. Ordinals are assumed dense from 1.
func NewTokenIndex(raw []RawMessage) TokenIndex {
	maxOrd := 0
	for _, m := range raw {
		maxOrd = max(maxOrd, m.Ordinal)
	}
	per := make([]int, maxOrd+1)
	for _, m := range raw {
		per[m.Ordinal] = m.Tokens
	}
	prefix := make([]int, maxOrd+1)
	for i := 1; i <= maxOrd; i++ {
		prefix[i] = prefix[i-1] + per[i]
	}
	return TokenIndex{prefix: prefix, count: maxOrd}
}

// Count is the number of raw ordinals.
func (x TokenIndex) Count() int { return x.count }

// TokenFor returns one ordinal's tokens.
func (x TokenIndex) TokenFor(ord int) int {
	if ord < 1 || ord > x.count {
		return 0
	}
	return x.prefix[ord] - x.prefix[ord-1]
}

// Suffix returns the tokens from ord to the end.
func (x TokenIndex) Suffix(ord int) int {
	if ord <= 1 {
		return x.prefix[x.count]
	}
	if ord > x.count {
		return 0
	}
	return x.prefix[x.count] - x.prefix[ord-1]
}

// Range returns tokens of [start, end).
func (x TokenIndex) Range(start, end int) int {
	start = max(1, start)
	end = max(start, min(x.count+1, end))
	return x.prefix[end-1] - x.prefix[start-1]
}

// Total returns all tokens.
func (x TokenIndex) Total() int { return x.prefix[x.count] }

// SuffixStartFor returns the first ordinal of the smallest suffix whose
// tokens cover need.
func (x TokenIndex) SuffixStartFor(need int) int {
	if need <= 0 {
		return x.count + 1
	}
	total := x.prefix[x.count]
	if total < need {
		return 1
	}
	cut := total - need
	lo, hi, best := 0, x.count, 0
	for lo <= hi {
		mid := (lo + hi) >> 1
		if x.prefix[mid] <= cut {
			best = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return best + 1
}

// HeadEndForCap returns the exclusive end of the longest head starting at
// start (bounded by end) whose tokens fit capTokens; at least one message.
func (x TokenIndex) HeadEndForCap(start, end, capTokens int) int {
	start = max(1, min(x.count+1, start))
	end = max(start, min(x.count+1, end))
	if capTokens <= 0 {
		return start
	}
	cut := x.prefix[start-1] + capTokens
	result := start
	for o := start; o < end; o++ {
		if x.prefix[o] <= cut {
			result = o + 1
		} else {
			break
		}
	}
	if result == start && start < end {
		return start + 1
	}
	return result
}

// ---------------------------------------------------------------------------
// Tool arcs

// ToolArc pairs a tool invocation with its result (Res == 0 means open).
type ToolArc struct {
	CallID string
	Inv    int
	Res    int
}

// BuildToolArcs pairs calls and results FIFO by call id.
func BuildToolArcs(raw []RawMessage) []ToolArc {
	open := map[string][]int{}
	var arcs []ToolArc
	for _, m := range raw {
		for _, c := range m.ToolCalls {
			if c.ID == "" {
				continue
			}
			open[c.ID] = append(open[c.ID], m.Ordinal)
		}
		for _, r := range m.ToolResults {
			q := open[r.ToolCallID]
			if len(q) == 0 {
				continue
			}
			arcs = append(arcs, ToolArc{CallID: r.ToolCallID, Inv: q[0], Res: m.Ordinal})
			if len(q) == 1 {
				delete(open, r.ToolCallID)
			} else {
				open[r.ToolCallID] = q[1:]
			}
		}
	}
	for id, q := range open {
		for _, inv := range q {
			arcs = append(arcs, ToolArc{CallID: id, Inv: inv})
		}
	}
	sort.SliceStable(arcs, func(i, j int) bool {
		if arcs[i].Inv != arcs[j].Inv {
			return arcs[i].Inv < arcs[j].Inv
		}
		ri, rj := arcs[i].Res, arcs[j].Res
		if ri == 0 {
			ri = math.MaxInt
		}
		if rj == 0 {
			rj = math.MaxInt
		}
		return ri < rj
	})
	return arcs
}

func arcCrosses(inv, res, boundary int) bool { return inv < boundary && boundary <= res }

// FenceCompleted moves candidate so no completed arc component is split.
// Components starting before floor are kept whole on the tail side's
// opposite (boundary moves past them); others move the boundary before them.
func FenceCompleted(candidate int, arcs []ToolArc, floor int) int {
	type pair struct{ inv, res int }
	var completed []pair
	for _, a := range arcs {
		if a.Res != 0 {
			completed = append(completed, pair{a.Inv, a.Res})
		}
	}
	in := make([]bool, len(completed))
	any := false
	minInv, maxRes := math.MaxInt, 0
	for i, p := range completed {
		if arcCrosses(p.inv, p.res, candidate) {
			in[i] = true
			any = true
			minInv = min(minInv, p.inv)
			maxRes = max(maxRes, p.res)
		}
	}
	if !any {
		return candidate
	}
	for {
		grew := false
		for i, p := range completed {
			if !in[i] && p.inv <= maxRes && p.res >= minInv {
				in[i] = true
				grew = true
				minInv = min(minInv, p.inv)
				maxRes = max(maxRes, p.res)
			}
		}
		if !grew {
			break
		}
	}
	if minInv < floor {
		return maxRes + 1
	}
	return minInv
}

// FenceArcs applies the completed-arc fence plus recent open-arc protection.
func FenceArcs(candidate int, arcs []ToolArc, lastEnd, recentOpenCutoff int) int {
	boundary := FenceCompleted(candidate, arcs, lastEnd+1)
	for _, a := range arcs {
		if a.Res != 0 || a.Inv < recentOpenCutoff {
			continue
		}
		if a.Inv >= lastEnd+1 && a.Inv < boundary {
			boundary = a.Inv
			break
		}
		if a.Inv >= boundary {
			boundary = a.Inv
			break
		}
	}
	return FenceCompleted(boundary, arcs, 1)
}

// ---------------------------------------------------------------------------
// Chunk formatting

var (
	commitVerbRe     = regexp.MustCompile(`(?i)\b(?:commit(?:ted|ting|s)?|cherry-?pick(?:ed|ing|s)?|merge[ds]?|merging|rebas(?:e|ed|es|ing))\b`)
	commitHashRe     = regexp.MustCompile("(?i)`?\\b([0-9a-f]{7,12})\\b`?")
	commitHashTest   = regexp.MustCompile(`(?i)\b[0-9a-f]{7,12}\b`)
	whitespaceRe     = regexp.MustCompile(`\s+`)
	emptyParensRe    = regexp.MustCompile(`\(\s*\)`)
	spaceCommaRe     = regexp.MustCompile(`\s+,`)
	multiCommaRe     = regexp.MustCompile(`,\s*,+`)
	multiSpaceRe     = regexp.MustCompile(`\s{2,}`)
	spacePunctRe     = regexp.MustCompile(`\s+([,.;:])`)
	systemReminderRe = regexp.MustCompile(`(?s)<system[-_]reminder>.*?</system[-_]reminder>`)
)

// MaxCommitsPerBlock bounds commit hashes listed per block.
const MaxCommitsPerBlock = 5

func removeSystemReminders(s string) string { return systemReminderRe.ReplaceAllString(s, "") }

func normalizeText(s string) string { return strings.TrimSpace(whitespaceRe.ReplaceAllString(s, " ")) }

// TextMentionsCommit reports a hash and a commit verb in the same text.
func TextMentionsCommit(s string) bool {
	return commitHashTest.MatchString(s) && commitVerbRe.MatchString(s)
}

func extractCommitHashes(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range commitHashRe.FindAllStringSubmatch(s, -1) {
		h := strings.ToLower(m[1])
		if seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
		if len(out) >= MaxCommitsPerBlock {
			break
		}
	}
	return out
}

func compactTextForSummary(text string, assistant bool) (string, []string) {
	var hashes []string
	if assistant {
		hashes = extractCommitHashes(text)
	}
	if len(hashes) == 0 || !commitVerbRe.MatchString(text) {
		return text, hashes
	}
	w := commitHashRe.ReplaceAllString(text, "")
	w = emptyParensRe.ReplaceAllString(w, "")
	w = spaceCommaRe.ReplaceAllString(w, ",")
	w = multiCommaRe.ReplaceAllString(w, ", ")
	w = multiSpaceRe.ReplaceAllString(w, " ")
	w = spacePunctRe.ReplaceAllString(w, "$1")
	w = strings.TrimSpace(w)
	if w == "" {
		return text, hashes
	}
	return w, hashes
}

func mergeCommitHashes(existing, next []string) []string {
	for _, h := range next {
		if len(existing) >= MaxCommitsPerBlock {
			break
		}
		dup := false
		for _, e := range existing {
			if e == h {
				dup = true
				break
			}
		}
		if !dup {
			existing = append(existing, h)
		}
	}
	return existing
}

func truncateArg(v string) string {
	r := []rune(v)
	if len(r) <= 60 {
		return v
	}
	return string(r[:60]) + "…"
}

// ToolCallSummary renders one call as "TC: …".
func ToolCallSummary(c message.ToolCall) string {
	var input map[string]any
	_ = json.Unmarshal([]byte(c.Input), &input)
	if d, ok := input["description"].(string); ok && d != "" {
		return "TC: " + d
	}
	if k := extractKeyArg(input); k != "" {
		return fmt.Sprintf("TC: %s(%s)", c.Name, k)
	}
	return "TC: " + c.Name
}

func extractKeyArg(input map[string]any) string {
	if input == nil {
		return ""
	}
	for _, k := range []string{"filePath", "file_path", "path", "pattern", "query", "url", "command"} {
		if v, ok := input[k].(string); ok {
			return truncateArg(v)
		}
	}
	for _, k := range []string{"symbol", "module", "action"} {
		if v, ok := input[k].(string); ok {
			return v
		}
	}
	return ""
}

func compactRole(r message.MessageRole) string {
	switch r {
	case message.Assistant, message.Tool:
		return "A"
	case message.User:
		return "U"
	}
	s := string(r)
	if s == "" {
		return "M"
	}
	return strings.ToUpper(s[:1])
}

type chunkBlock struct {
	role       string
	start, end int
	parts      []string
	commits    []string
	toolOnly   bool
	lastID     string
	firstID    string
	count      int
}

func formatBlock(b *chunkBlock) string {
	var sb strings.Builder
	if b.start == b.end {
		fmt.Fprintf(&sb, "[%d]", b.start)
	} else {
		fmt.Fprintf(&sb, "[%d-%d]", b.start, b.end)
	}
	sb.WriteString(" " + b.role + ":")
	if len(b.commits) > 0 {
		sb.WriteString(" commits: " + strings.Join(b.commits, ", "))
	}
	sb.WriteString(" " + strings.Join(b.parts, " / "))
	return sb.String()
}

// OrdRange is an inclusive ordinal range.
type OrdRange struct{ Start, End int }

// Chunk is one serialized slice of raw history for the historian.
type Chunk struct {
	StartIndex, EndIndex       int
	StartMessageID, EndMessage string
	MessageCount               int
	TokenEstimate              int
	HasMore                    bool
	Text                       string
	CommitClusters             int
	ToolOnlyRanges             []OrdRange
	CompletedArcs              []ToolArc
	OversizeAtomicUnit         bool
	IDs                        map[int]string // ordinal → message id within chunk
}

// ReadChunk serializes raw messages from startOrd up to (not including)
// eligibleEnd, within tokenBudget.
func ReadChunk(raw []RawMessage, startOrd, eligibleEnd, tokenBudget int) Chunk {
	arcs := BuildToolArcs(raw)
	var completed []ToolArc
	for _, a := range arcs {
		if a.Res != 0 && a.Res >= startOrd && a.Inv < eligibleEnd {
			completed = append(completed, a)
		}
	}
	totalCount := 0
	for _, m := range raw {
		totalCount = max(totalCount, m.Ordinal)
	}
	ch := Chunk{StartIndex: startOrd, CompletedArcs: completed, IDs: map[int]string{}}
	var lines []string
	var toolOnly []OrdRange
	var cur *chunkBlock
	var pendingNoise []RawMessage
	lastOrd := startOrd - 1
	highest := startOrd - 1
	lastFlushedRole := ""
	totalTokens := 0

	flush := func() bool {
		if cur == nil {
			return true
		}
		text := formatBlock(cur)
		bt := EstimateTokens(text)
		if totalTokens+bt > tokenBudget && totalTokens > 0 {
			splits := false
			for _, a := range completed {
				if a.Inv <= lastOrd && a.Res > lastOrd {
					splits = true
					break
				}
			}
			if !splits {
				return false
			}
		}
		if cur.role == "A" && len(cur.commits) > 0 && lastFlushedRole != "A" {
			ch.CommitClusters++
		}
		lastFlushedRole = cur.role
		if ch.StartMessageID == "" {
			ch.StartMessageID = cur.firstID
		}
		lastOrd = cur.end
		highest = max(highest, lastOrd)
		ch.EndMessage = cur.lastID
		ch.MessageCount += cur.count
		lines = append(lines, text)
		totalTokens += bt
		if cur.toolOnly {
			toolOnly = append(toolOnly, OrdRange{cur.start, cur.end})
		}
		cur = nil
		return true
	}
	newBlock := func(m RawMessage, role, text string, commits []string, tonly bool) {
		start := m.Ordinal
		first := m.ID
		if len(pendingNoise) > 0 {
			start = pendingNoise[0].Ordinal
			first = pendingNoise[0].ID
		}
		cur = &chunkBlock{role: role, start: start, end: m.Ordinal, parts: []string{text}, commits: commits, toolOnly: tonly, firstID: first, lastID: m.ID, count: len(pendingNoise) + 1}
		pendingNoise = nil
	}
	extend := func(m RawMessage, text string, commits []string, narrative bool) {
		cur.end = m.Ordinal
		cur.lastID = m.ID
		cur.count += len(pendingNoise) + 1
		pendingNoise = nil
		if text != "" {
			cur.parts = append(cur.parts, text)
		}
		cur.commits = mergeCommitHashes(cur.commits, commits)
		if narrative {
			cur.toolOnly = false
		}
	}

	for _, m := range raw {
		if m.Ordinal >= eligibleEnd {
			break
		}
		if m.Ordinal < startOrd {
			continue
		}
		ch.IDs[m.Ordinal] = m.ID

		// Tool results carry no narrative; they belong to the A block that
		// issued the calls.
		if m.Role == message.Tool {
			if cur != nil && cur.role == "A" {
				extend(m, "", nil, false)
			} else {
				pendingNoise = append(pendingNoise, m)
				if cur == nil {
					highest = max(highest, m.Ordinal)
				}
			}
			continue
		}
		if m.Role == message.User && !m.MeaningfulUser() {
			pendingNoise = append(pendingNoise, m)
			if cur == nil {
				highest = max(highest, m.Ordinal)
			}
			continue
		}
		role := compactRole(m.Role)
		var texts []string
		for _, t := range m.Texts {
			if m.Role == message.User {
				t = removeSystemReminders(t)
			}
			if t = normalizeText(t); t != "" {
				texts = append(texts, t)
			}
		}
		parts := texts
		if len(texts) == 0 {
			for _, c := range m.ToolCalls {
				parts = append(parts, ToolCallSummary(c))
			}
		}
		text, commits := compactTextForSummary(strings.Join(parts, " / "), m.Role == message.Assistant)
		if text == "" {
			pendingNoise = append(pendingNoise, m)
			if cur == nil {
				highest = max(highest, m.Ordinal)
			}
			continue
		}
		narrative := len(texts) > 0
		if cur != nil && cur.role == role {
			extend(m, text, commits, narrative)
			continue
		}
		if !flush() {
			break
		}
		newBlock(m, role, text, commits, !narrative)
	}
	if flush() && len(pendingNoise) > 0 {
		last := pendingNoise[len(pendingNoise)-1]
		if ch.MessageCount > 0 {
			ch.MessageCount += len(pendingNoise)
			lastOrd = last.Ordinal
			ch.EndMessage = last.ID
		}
		highest = max(highest, last.Ordinal)
	}
	var merged []OrdRange
	for _, r := range toolOnly {
		if n := len(merged); n > 0 && r.Start == merged[n-1].End+1 {
			merged[n-1].End = r.End
		} else {
			merged = append(merged, r)
		}
	}
	ch.ToolOnlyRanges = merged
	ch.Text = strings.Join(lines, "\n")
	ch.EndIndex = lastOrd
	ch.TokenEstimate = totalTokens
	limit := min(eligibleEnd-1, totalCount)
	ch.HasMore = max(lastOrd, highest) < limit
	if EstimateTokens(ch.Text) > tokenBudget {
		for _, a := range completed {
			if a.Inv <= lastOrd && a.Res >= startOrd {
				ch.OversizeAtomicUnit = true
				break
			}
		}
	}
	return ch
}
