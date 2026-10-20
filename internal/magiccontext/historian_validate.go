// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): hooks/magic-context/compartment-runner-validation.ts, compartment-runner-mapping.ts.

package magiccontext

import (
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"
)

// Ported from magic-context compartment-runner-validation.ts,
// historian-transient-error.ts, user-facing-codes.ts and
// agents/language-directive.ts (MIT, Copyright (c) 2025 Ufuk Altinok).

const (
	minRecompChunkTokenBudget        = 20
	historianBoundaryHealingSlack    = 2
	HistorianPersistentFailureThresh = 3
)

// ---------------------------------------------------------------------------
// User-facing failure texts

const (
	failHistorianUnavailable = "History compression could not finish this turn. It will retry automatically. (MC-H01)"
	failSavedHistoryMisalign = "History compression is paused because this session's saved summaries no longer line up with its messages. Run /ctx-recomp to rebuild them. (MC-H03)"
	failWindowTooSmall       = "History compression is paused because the history model's context window is too small for its instructions. Set options.magic_context.historian_model to a model with a larger context window. (MC-H05)"
	failRecompUnavailable    = "History compression could not be rebuilt. Run /ctx-recomp again. (MC-R01)"
)

// BuildHistorianFailureNotice frames a historian failure by how many
// consecutive failures there have been.
func BuildHistorianFailureNotice(failureCount int) string {
	heading := "## Magic Context"
	if failureCount >= HistorianPersistentFailureThresh {
		heading = "## Magic Context — History compression"
	}
	return strings.Join([]string{heading, "", failHistorianUnavailable}, "\n")
}

// BuildStoredCompartmentsInvalidNotice is shown when saved compartments no
// longer validate.
func BuildStoredCompartmentsInvalidNotice() string {
	return strings.Join([]string{"## Magic Context — History compression", "", failSavedHistoryMisalign}, "\n")
}

// BuildHistorianWindowTooSmallNotice is shown when the historian model cannot
// hold its fixed instructions plus a minimal chunk.
func BuildHistorianWindowTooSmallNotice() string {
	return strings.Join([]string{"## Magic Context — History compression", "", failWindowTooSmall}, "\n")
}

// ---------------------------------------------------------------------------
// Transient error classification

var (
	nonTransientRe = regexp.MustCompile(`(?:^|[^a-z])(?:invalid[ _]request|bad[ _]request|unauthori[sz]ed|forbidden|authentication|authenticate|authorization|o?auth)(?:[^a-z]|$)|(?:^| )400(?:\D|$)`)
	transientRe    = regexp.MustCompile(`(?:^|\D)(?:429|500|502|503)(?:\D|$)|rate[ _]limit|timeout|econnreset|etimedout|overloaded`)
)

// IsTransientHistorianError reports whether a failed historian prompt is
// worth retrying on the same model.
func IsTransientHistorianError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if nonTransientRe.MatchString(msg) {
		return false
	}
	return transientRe.MatchString(msg) || strings.Contains(msg, "deadline exceeded")
}

// ---------------------------------------------------------------------------
// Language directive

func languageName(code string) (string, bool) {
	code = strings.ToLower(strings.TrimSpace(code))
	if len(code) != 2 {
		return "", false
	}
	tag, err := language.Parse(code)
	if err != nil {
		return "", false
	}
	en := display.English.Languages().Name(tag)
	if en == "" {
		return "", false
	}
	self := display.Self.Name(tag)
	if self == "" || strings.EqualFold(self, en) {
		return en, true
	}
	return en + " (" + self + ")", true
}

// contentLanguageDirective renders the output-language block, or "" for
// unset/invalid codes.
func contentLanguageDirective(code string, preserveUserQuotes bool) string {
	name, ok := languageName(code)
	if !ok {
		return ""
	}
	lines := []string{
		"## Output language",
		"",
		"Write human-readable prose you author in: " + name + ".",
		"",
		"Do not translate or rename structural tokens. Copy required output schemas exactly:",
		"- XML tag names, XML attribute names, JSON keys, tool names, tool-call argument keys, enum values, booleans/null, and required sentinel strings stay in English exactly as shown.",
		"- Keep code identifiers, file paths, commands, config keys, CLI flags, URLs, commit hashes, model/provider IDs, stack traces, diagnostics, and transcript role markers such as U:, A:, and TC: verbatim.",
		"- Localize only free-text prose values/content: summaries, memory text, explanations, titles, observations, and answers — unless the prompt says to preserve original wording.",
		"",
		"These literal values must remain English when used:",
		"PROJECT_RULES, ARCHITECTURE, CONSTRAINTS, CONFIG_VALUES, NAMING;",
		"causal_incident, trajectory_correction;",
		"feature, design, docs, release, investigation, bug, refactor, infra;",
		"memory, observation; true, false; No relevant memories found.",
		"",
		"Preserve the required output shape. Do not add commentary outside the requested XML/JSON/tool output.",
	}
	if preserveUserQuotes {
		lines = append(lines, "", "Preserve U: lines and directly quoted user text in their original source language; write the surrounding summary prose in "+name+".")
	}
	return strings.Join(lines, "\n")
}

// withContentLanguageDirective appends the language directive to prompt.
func withContentLanguageDirective(prompt, code string, preserveUserQuotes bool) string {
	d := contentLanguageDirective(code, preserveUserQuotes)
	if d == "" {
		return prompt
	}
	return prompt + "\n\n" + d
}

// ---------------------------------------------------------------------------
// Validation

// validatedPass is a historian response that passed validation.
type validatedPass struct {
	Compartments     []Compartment
	Facts            []ParsedFact
	Events           []ParsedEvent
	UserObservations []string
	PrimerCandidates []ParsedPrimerCandidate
}

func healCompartmentGaps(cs []ParsedCompartment, toolOnly []OrdRange) {
	for i := 1; i < len(cs); i++ {
		gapStart := cs[i-1].EndMessage + 1
		gapEnd := cs[i].StartMessage - 1
		if gapEnd-gapStart+1 <= 0 {
			continue
		}
		for _, r := range toolOnly {
			if r.Start <= gapStart && r.End >= gapEnd {
				cs[i-1].EndMessage = gapEnd
				break
			}
		}
	}
}

func boundarySplitsCompletedArc(boundary int, arcs []ToolArc) bool {
	for _, a := range arcs {
		if a.Res != 0 && arcCrosses(a.Inv, a.Res, boundary) {
			return true
		}
	}
	return false
}

func healTerminalCompletedToolArc(cs []ParsedCompartment, unprocessedFrom *int, arcs []ToolArc, chunkEnd int) *int {
	if len(cs) == 0 {
		return unprocessedFrom
	}
	last := &cs[len(cs)-1]
	orig := last.EndMessage
	for pass := 0; pass <= len(arcs); pass++ {
		boundary := last.EndMessage + 1
		next := last.EndMessage
		for _, a := range arcs {
			if a.Res != 0 && a.Res <= chunkEnd && arcCrosses(a.Inv, a.Res, boundary) {
				next = max(next, a.Res)
			}
		}
		if next == last.EndMessage {
			break
		}
		last.EndMessage = next
	}
	if last.EndMessage != orig && unprocessedFrom != nil {
		v := last.EndMessage + 1
		return &v
	}
	return unprocessedFrom
}

// shouldDiscardLastCompartment reports whether the trailing compartment sits
// so close to the chunk end that it is likely incomplete.
func shouldDiscardLastCompartment(cs []Compartment, chunk Chunk) bool {
	if len(cs) < 2 {
		return false
	}
	last, prev := cs[len(cs)-1], cs[len(cs)-2]
	return chunk.EndIndex-last.EndOrdinal <= historianBoundaryHealingSlack &&
		!boundarySplitsCompletedArc(prev.EndOrdinal+1, chunk.CompletedArcs)
}

func validateParsedCompartments(cs []ParsedCompartment, chunkStart, chunkEnd int, unprocessedFrom *int) string {
	expected := chunkStart
	for i, c := range cs {
		if strings.TrimSpace(c.P1) == "" {
			return fmt.Sprintf("compartment %d is missing the tiered paraphrase structure (p1..p4); re-emit with all four tiers", i+1)
		}
		if c.EndMessage < c.StartMessage {
			return fmt.Sprintf("invalid range %d-%d", c.StartMessage, c.EndMessage)
		}
		if c.StartMessage < chunkStart || c.EndMessage > chunkEnd {
			return fmt.Sprintf("range %d-%d is outside chunk %d-%d", c.StartMessage, c.EndMessage, chunkStart, chunkEnd)
		}
		if c.StartMessage != expected {
			if c.StartMessage < expected {
				return fmt.Sprintf("overlap before message %d (saw %d-%d)", expected, c.StartMessage, c.EndMessage)
			}
			return fmt.Sprintf("gap before message %d (expected %d)", c.StartMessage, expected)
		}
		expected = c.EndMessage + 1
	}
	if unprocessedFrom != nil {
		u := *unprocessedFrom
		if u == chunkEnd+1 {
			return ""
		}
		if u < chunkStart || u > chunkEnd {
			return fmt.Sprintf("<unprocessed_from> %d is outside chunk %d-%d", u, chunkStart, chunkEnd)
		}
		if u != expected {
			return fmt.Sprintf("<unprocessed_from> %d does not match next uncovered message %d", u, expected)
		}
		return ""
	}
	if expected <= chunkEnd {
		return fmt.Sprintf("output left uncovered messages %d-%d without <unprocessed_from>", expected, chunkEnd)
	}
	return ""
}

// validateHistorianOutput parses and validates one historian response for
// chunk; sequenceOffset numbers the resulting compartments.
func validateHistorianOutput(text string, chunk Chunk, sequenceOffset int) (validatedPass, error) {
	parsed := ParseCompartmentOutput(text)
	if len(parsed.Compartments) == 0 {
		return validatedPass{}, fmt.Errorf("Historian returned no usable compartments.")
	}
	healCompartmentGaps(parsed.Compartments, chunk.ToolOnlyRanges)
	parsed.UnprocessedFrom = healTerminalCompletedToolArc(parsed.Compartments, parsed.UnprocessedFrom, chunk.CompletedArcs, chunk.EndIndex)

	idFor := func(ord int) (string, bool) {
		if ord < chunk.StartIndex || ord > chunk.EndIndex {
			return "", false
		}
		id, ok := chunk.IDs[ord]
		return id, ok
	}
	mapped := make([]Compartment, 0, len(parsed.Compartments))
	for i, c := range parsed.Compartments {
		sid, ok1 := idFor(c.StartMessage)
		eid, ok2 := idFor(c.EndMessage)
		if !ok1 || !ok2 {
			return validatedPass{}, fmt.Errorf("Historian returned invalid compartment output: Compartment range %d-%d does not map to raw session lines %d-%d", c.StartMessage, c.EndMessage, chunk.StartIndex, chunk.EndIndex)
		}
		mapped = append(mapped, Compartment{
			Sequence: sequenceOffset + i, StartOrdinal: c.StartMessage, EndOrdinal: c.EndMessage,
			StartMessageID: sid, EndMessageID: eid, Title: c.Title, Content: c.Content,
			P1: c.P1, P2: c.P2, P3: c.P3, P4: c.P4, Importance: c.Importance, EpisodeType: c.EpisodeType,
		})
	}
	if msg := validateParsedCompartments(parsed.Compartments, chunk.StartIndex, chunk.EndIndex, parsed.UnprocessedFrom); msg != "" {
		return validatedPass{}, fmt.Errorf("Historian returned invalid compartment output: %s", msg)
	}
	last := parsed.Compartments[len(parsed.Compartments)-1]
	if boundarySplitsCompletedArc(last.EndMessage+1, chunk.CompletedArcs) {
		return validatedPass{}, fmt.Errorf("Historian terminal boundary splits a completed tool invocation/result arc")
	}
	out := validatedPass{Compartments: mapped, Facts: parsed.Facts, Events: parsed.Events, UserObservations: parsed.UserObservations}
	if len(parsed.PrimerCandidates) > 0 {
		out.PrimerCandidates = parsed.PrimerCandidates[:1]
	}
	return out, nil
}

// buildHistorianRepairPrompt asks the historian to fix an invalid response.
func buildHistorianRepairPrompt(originalPrompt, previousOutput, validationError, lang string) string {
	prompt := strings.Join([]string{
		originalPrompt,
		"",
		"Your previous XML response was invalid and cannot be persisted.",
		"Validation error: " + validationError,
		"Return a corrected full XML response for the same existing state and new messages.",
		"Do not skip any displayed raw ordinal or displayed raw range, even if the message looks trivial.",
		"Every displayed message range must belong to exactly one compartment unless it is intentionally left in one trailing suffix marked by <unprocessed_from>.",
		"",
		"Previous invalid XML:",
		previousOutput,
	}, "\n")
	return withContentLanguageDirective(prompt, lang, true)
}

// ValidateStoredCompartments checks that saved compartments are contiguous
// from message 1, allowing gaps inside nonNarrative ranges.
func ValidateStoredCompartments(cs []Compartment, nonNarrative []OrdRange) string {
	expected := 1
	for _, c := range cs {
		if c.StartOrdinal != expected {
			if c.StartOrdinal < expected {
				return fmt.Sprintf("overlap before message %d (saw %d-%d)", expected, c.StartOrdinal, c.EndOrdinal)
			}
			gapEnd := c.StartOrdinal - 1
			safe := false
			for _, r := range nonNarrative {
				if r.Start <= expected && r.End >= gapEnd {
					safe = true
					break
				}
			}
			if !safe {
				return fmt.Sprintf("gap before message %d (expected %d)", c.StartOrdinal, expected)
			}
		}
		if c.EndOrdinal < c.StartOrdinal {
			return fmt.Sprintf("invalid range %d-%d", c.StartOrdinal, c.EndOrdinal)
		}
		expected = c.EndOrdinal + 1
	}
	return ""
}

// validateChunkCoverage checks that the chunk covers every ordinal it claims.
func validateChunkCoverage(chunk Chunk) string {
	if chunk.MessageCount == 0 {
		return ""
	}
	expected := chunk.StartIndex
	for expected <= chunk.EndIndex {
		if _, ok := chunk.IDs[expected]; !ok {
			return fmt.Sprintf("chunk omits raw message %d while still claiming coverage through %d", expected, chunk.EndIndex)
		}
		expected++
	}
	return ""
}

// reducedRecompTokenBudget halves budget (min 20), or returns 0 when that
// would not shrink it.
func reducedRecompTokenBudget(budget int) int {
	r := max(minRecompChunkTokenBudget, budget/2)
	if r < budget {
		return r
	}
	return 0
}
