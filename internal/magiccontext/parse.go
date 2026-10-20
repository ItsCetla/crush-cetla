// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): hooks/magic-context/compartment-parser.ts.

package magiccontext

import (
	"cmp"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// ParsedCompartment is one compartment from historian output. Tiered (v2)
// compartments carry P1-P4 with Content mirroring P1; flat ones only Content.
type ParsedCompartment struct {
	StartMessage int
	EndMessage   int
	Title        string
	Content      string
	Tiered       bool
	P1, P2, P3   string
	P4           string
	Importance   *int
	EpisodeType  string
}

// ParsedFact is one durable fact the historian extracted.
type ParsedFact struct {
	Category string
	Content  string
}

// ParsedEvent is a historian event (causal_incident, trajectory_correction...).
// AtCompartment is the 1-based compartment index it anchors to, or 0.
type ParsedEvent struct {
	Kind          string
	AtCompartment int
	Fields        map[string]string
}

// ParsedPrimerCandidate is a question worth priming future sessions with.
// OriginCompartmentIndex is 1-based, or 0 for the legacy bullet form.
type ParsedPrimerCandidate struct {
	Question               string
	OriginCompartmentIndex int
}

// ParsedCompartmentOutput is everything parsed from one historian response.
type ParsedCompartmentOutput struct {
	Compartments      []ParsedCompartment
	Facts             []ParsedFact
	DroppedFactBlocks int
	DroppedFacts      int
	Events            []ParsedEvent
	UnprocessedFrom   *int
	UserObservations  []string
	PrimerCandidates  []ParsedPrimerCandidate
}

// V2MemoryCategories are the fact categories the historian may emit.
var V2MemoryCategories = []string{"PROJECT_RULES", "ARCHITECTURE", "CONSTRAINTS", "CONFIG_VALUES", "NAMING"}

var (
	compartmentOpenRe          = regexp.MustCompile(`<compartment\s+((?:[^>"]|"[^"\n]*")*?)\s*>`)
	compartmentCloseFollowerRe = regexp.MustCompile(`^\s*(?:$|<(?:compartment\s|/compartments\s*>|/output\s*>|(?:facts|events|meta|user_observations|primer_candidates|unprocessed_from)\s*>))`)
	attrStartRe                = regexp.MustCompile(`\bstart="(\d+)"`)
	attrEndRe                  = regexp.MustCompile(`\bend="(\d+)"`)
	attrTitleRe                = regexp.MustCompile(`\btitle="([^"]*)"`)
	attrEpisodeRe              = regexp.MustCompile(`\bepisode_type="([^"]*)"`)
	attrImportanceRe           = regexp.MustCompile(`\bimportance="(\d+)"`)
	tierOpenRe                 = regexp.MustCompile(`<p(\d)\s*(/?)>`)
	tierCloseRe                = regexp.MustCompile(`</p(\d)\s*>`)
	tierCloseFollowerRe        = regexp.MustCompile(`^\s*(?:$|</?p\d\s*/?>)`)
	tierCloseAnyRe             = regexp.MustCompile(`</p\d`)
	tierOpenAnyRe              = regexp.MustCompile(`<p\d`)
	tierOpenerByDigit          = [10]*regexp.Regexp{}
	categoryOpenRe             = regexp.MustCompile(`<([A-Za-z_][A-Za-z0-9_-]*)>`)
	factItemRe                 = regexp.MustCompile(`(?m)^\s*\*\s*(.+)$`)
	unprocessedRe              = regexp.MustCompile(`<unprocessed_from>(\d+)</unprocessed_from>`)
	userObservationsRe         = regexp.MustCompile(`(?s)<user_observations>(.*?)</user_observations>`)
	primerCandidatesRe         = regexp.MustCompile(`(?s)<primer_candidates>(.*?)</primer_candidates>`)
	primerElementRe            = regexp.MustCompile(`(?s)<primer\s+at_compartment="(\d+)"\s*>(.*?)</primer>`)
	primerItemRe               = regexp.MustCompile(`(?m)^\s*(?:\*|-|\d+\.)\s*(.+)$`)
	factsBlockRe               = regexp.MustCompile(`(?s)<facts>(.*?)</facts>`)
	eventsBlockRe              = regexp.MustCompile(`(?s)<events>(.*?)</events>`)
	eventOpenRe                = regexp.MustCompile(`<([a-z_]+)\s+at_compartment="(\d+)"\s*>`)
	eventFieldOpenRe           = regexp.MustCompile(`<([a-z_]+)\s*>`)
	sideChannelOpenRe          = regexp.MustCompile(`<(meta|user_observations|primer_candidates)>`)
	outputWrapperRe            = regexp.MustCompile(`</?(?:output|compartments)>`)
)

func init() {
	for d := range tierOpenerByDigit {
		tierOpenerByDigit[d] = regexp.MustCompile(`<p` + strconv.Itoa(d) + `\s*/?>`)
	}
}

// pairedMatch is one `<name ...>body</name>` element found by findPaired.
type pairedMatch struct {
	start, end int // whole element
	groups     []string
	body       string
}

// findPaired emulates JS `/<(name)…>(.*?)<\/\1>/gs`: openRe matches the open
// tag with the element name as group 1, and the body runs to the first
// `</name>` after it. An open tag with no close is skipped one byte later,
// like a failed backreference match.
func findPaired(s string, openRe *regexp.Regexp) []pairedMatch {
	var out []pairedMatch
	for pos := 0; pos <= len(s); {
		loc := openRe.FindStringSubmatchIndex(s[pos:])
		if loc == nil {
			break
		}
		openStart, openEnd := pos+loc[0], pos+loc[1]
		groups := make([]string, 0, len(loc)/2-1)
		for g := 2; g < len(loc); g += 2 {
			if loc[g] < 0 {
				groups = append(groups, "")
				continue
			}
			groups = append(groups, s[pos+loc[g]:pos+loc[g+1]])
		}
		closeTag := "</" + groups[0] + ">"
		at := strings.Index(s[openEnd:], closeTag)
		if at < 0 {
			pos = openStart + 1
			continue
		}
		closeStart := openEnd + at
		end := closeStart + len(closeTag)
		out = append(out, pairedMatch{start: openStart, end: end, groups: groups, body: s[openEnd:closeStart]})
		pos = end
	}
	return out
}

// searchFrom returns the index of the first match of re in s at or after from.
func searchFrom(s string, re *regexp.Regexp, from int) int {
	if from > len(s) {
		return -1
	}
	loc := re.FindStringIndex(s[from:])
	if loc == nil {
		return -1
	}
	return from + loc[0]
}

func hasTierOpenerFrom(inner string, digit, from int) bool {
	if digit < 0 || digit > 9 || from > len(inner) {
		return false
	}
	return tierOpenerByDigit[digit].MatchString(inner[from:])
}

// findMatchingTierClose finds the proper close of tier digit (see upstream
// docs): the first same-digit close followed only by tier tags or the end,
// unless the body opens a tier that is not found anywhere else.
func findMatchingTierClose(inner string, bodyStart, digit int, found map[int]string) (int, int, bool) {
	for _, loc := range tierCloseRe.FindAllStringSubmatchIndex(inner[bodyStart:], -1) {
		closeStart, end := bodyStart+loc[0], bodyStart+loc[1]
		d, _ := strconv.Atoi(inner[bodyStart+loc[2] : bodyStart+loc[3]])
		if d != digit {
			continue
		}
		if !tierCloseFollowerRe.MatchString(inner[end:]) {
			continue
		}
		body := inner[bodyStart:closeStart]
		for _, open := range tierOpenRe.FindAllStringSubmatch(body, -1) {
			other, _ := strconv.Atoi(open[1])
			_, have := found[other]
			if other >= 1 && other <= 4 && other != digit && !have && !hasTierOpenerFrom(inner, other, end) {
				return 0, 0, false
			}
		}
		return closeStart, end, true
	}
	return 0, 0, false
}

// extractTiers walks tier openers in order; each digit keeps its first
// occurrence, "" for self-closed or empty tiers.
func extractTiers(inner string) map[int]string {
	tiers := map[int]string{}
	pos := 0
	for pos <= len(inner) {
		loc := tierOpenRe.FindStringSubmatchIndex(inner[pos:])
		if loc == nil {
			break
		}
		digit, _ := strconv.Atoi(inner[pos+loc[2] : pos+loc[3]])
		selfClose := inner[pos+loc[4]:pos+loc[5]] == "/"
		bodyStart := pos + loc[1]
		bodyEnd := bodyStart
		pos = bodyStart
		if !selfClose {
			if cs, ce, ok := findMatchingTierClose(inner, bodyStart, digit, tiers); ok {
				bodyEnd, pos = cs, ce
			} else {
				closeAt := searchFrom(inner, tierCloseAnyRe, bodyStart)
				if closeAt == -1 {
					bodyEnd, pos = len(inner), len(inner)
				} else {
					bodyEnd, pos = closeAt, closeAt+1
				}
				if openInside := searchFrom(inner[:bodyEnd], tierOpenAnyRe, bodyStart); openInside != -1 {
					bodyEnd, pos = openInside, openInside
				}
			}
		}
		if _, ok := tiers[digit]; !ok {
			tiers[digit] = unescapeXML(strings.TrimSpace(inner[bodyStart:bodyEnd]))
		}
		if pos >= len(inner) {
			break
		}
	}
	return tiers
}

type compartmentElement struct {
	start, end int
	attrs      string
	inner      string
}

const compartmentCloseTag = "</compartment>"

func findCompartmentElements(text string) []compartmentElement {
	var out []compartmentElement
	cursor := 0
	for cursor <= len(text) {
		loc := compartmentOpenRe.FindStringSubmatchIndex(text[cursor:])
		if loc == nil {
			break
		}
		openStart := cursor + loc[0]
		bodyStart := cursor + loc[1]
		attrs := text[cursor+loc[2] : cursor+loc[3]]
		windowEnd := len(text)
		if next := compartmentOpenRe.FindStringIndex(text[bodyStart:]); next != nil {
			windowEnd = bodyStart + next[0]
		}
		firstClose := indexFrom(text, compartmentCloseTag, bodyStart)
		if firstClose == -1 {
			break
		}
		closeAt := firstClose
		for at := firstClose; at != -1 && at < windowEnd; at = indexFrom(text, compartmentCloseTag, at+1) {
			if compartmentCloseFollowerRe.MatchString(text[at+len(compartmentCloseTag):]) {
				closeAt = at
				break
			}
		}
		end := closeAt + len(compartmentCloseTag)
		out = append(out, compartmentElement{start: openStart, end: end, attrs: attrs, inner: text[bodyStart:closeAt]})
		cursor = end
	}
	return out
}

func indexFrom(s, sub string, from int) int {
	if from > len(s) {
		return -1
	}
	i := strings.Index(s[from:], sub)
	if i < 0 {
		return -1
	}
	return from + i
}

func textOutsideCompartments(text string, elements []compartmentElement) string {
	var b strings.Builder
	last := 0
	for _, e := range elements {
		b.WriteString(text[last:e.start])
		last = e.end
	}
	b.WriteString(text[last:])
	return b.String()
}

// removePaired deletes every paired element found by findPaired.
func removePaired(s string, openRe *regexp.Regexp) string {
	matches := findPaired(s, openRe)
	if len(matches) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		b.WriteString(s[last:m.start])
		last = m.end
	}
	b.WriteString(s[last:])
	return b.String()
}

// ParseCompartmentOutput parses a historian response. It is a faithful port
// of upstream compartment-parser.ts.
func ParseCompartmentOutput(text string) ParsedCompartmentOutput {
	var out ParsedCompartmentOutput
	elements := findCompartmentElements(text)
	for _, el := range elements {
		sm := attrStartRe.FindStringSubmatch(el.attrs)
		em := attrEndRe.FindStringSubmatch(el.attrs)
		tm := attrTitleRe.FindStringSubmatch(el.attrs)
		if sm == nil || em == nil || tm == nil {
			continue
		}
		start, err1 := strconv.Atoi(sm[1])
		end, err2 := strconv.Atoi(em[1])
		title := unescapeXML(tm[1])
		if err1 != nil || err2 != nil || title == "" {
			continue
		}
		var episode string
		if m := attrEpisodeRe.FindStringSubmatch(el.attrs); m != nil {
			episode = unescapeXML(m[1])
		}
		var importance *int
		if m := attrImportanceRe.FindStringSubmatch(el.attrs); m != nil {
			if v, err := strconv.Atoi(m[1]); err == nil {
				importance = &v
			}
		}
		tiers := extractTiers(el.inner)
		if p1 := tiers[1]; p1 != "" {
			c := ParsedCompartment{
				StartMessage: start, EndMessage: end, Title: title, Content: p1, Tiered: true,
				P1: p1, Importance: importance, EpisodeType: episode,
			}
			p2, has2 := tiers[2]
			p3, has3 := tiers[3]
			switch {
			case has2:
				c.P2 = p2
			default:
				c.P2 = p1
			}
			switch {
			case has3:
				c.P3 = p3
			case has2:
				c.P3 = p2
			default:
				c.P3 = p1
			}
			c.P4 = tiers[4]
			out.Compartments = append(out.Compartments, c)
			continue
		}
		if content := unescapeXML(strings.TrimSpace(el.inner)); content != "" {
			out.Compartments = append(out.Compartments, ParsedCompartment{
				StartMessage: start, EndMessage: end, Title: title, Content: content,
				Importance: importance, EpisodeType: episode,
			})
		}
	}

	outside := textOutsideCompartments(text, elements)
	var factsScope string
	if m := factsBlockRe.FindStringSubmatch(outside); m != nil {
		factsScope = m[1]
	} else {
		factsScope = replaceFirst(outside, eventsBlockRe, "")
		factsScope = removePaired(factsScope, sideChannelOpenRe)
		factsScope = outputWrapperRe.ReplaceAllString(factsScope, "")
	}
	for _, m := range findPaired(factsScope, categoryOpenRe) {
		category := m.groups[0]
		var items []string
		for _, im := range factItemRe.FindAllStringSubmatch(m.body, -1) {
			if v := unescapeXML(strings.TrimSpace(im[1])); v != "" {
				items = append(items, v)
			}
		}
		if !slices.Contains(V2MemoryCategories, category) {
			if len(items) == 0 {
				continue
			}
			out.DroppedFactBlocks++
			out.DroppedFacts += len(items)
			continue
		}
		for _, v := range items {
			out.Facts = append(out.Facts, ParsedFact{Category: category, Content: v})
		}
	}

	if m := unprocessedRe.FindStringSubmatch(outside); m != nil {
		if v, err := strconv.Atoi(m[1]); err == nil {
			out.UnprocessedFrom = &v
		}
	}
	if m := userObservationsRe.FindStringSubmatch(outside); m != nil {
		for _, im := range factItemRe.FindAllStringSubmatch(m[1], -1) {
			if v := unescapeXML(strings.TrimSpace(im[1])); v != "" {
				out.UserObservations = append(out.UserObservations, v)
			}
		}
	}
	if m := primerCandidatesRe.FindStringSubmatch(outside); m != nil {
		block := m[1]
		els := primerElementRe.FindAllStringSubmatch(block, -1)
		for _, el := range els {
			if q := unescapeXML(strings.TrimSpace(el[2])); q != "" {
				idx, _ := strconv.Atoi(el[1])
				out.PrimerCandidates = append(out.PrimerCandidates, ParsedPrimerCandidate{Question: q, OriginCompartmentIndex: idx})
			}
		}
		if len(els) == 0 {
			for _, im := range primerItemRe.FindAllStringSubmatch(block, -1) {
				if q := unescapeXML(strings.TrimSpace(im[1])); q != "" {
					out.PrimerCandidates = append(out.PrimerCandidates, ParsedPrimerCandidate{Question: q})
				}
			}
		}
	}
	out.Events = parseEvents(outside)

	// Sort compartments by start and re-point 1-based anchors at the sorted
	// positions; out-of-range anchors are left for validation to discard.
	type indexed struct {
		c       ParsedCompartment
		emitted int
	}
	sorted := make([]indexed, len(out.Compartments))
	for i, c := range out.Compartments {
		sorted[i] = indexed{c, i}
	}
	slices.SortStableFunc(sorted, func(a, b indexed) int { return cmp.Compare(a.c.StartMessage, b.c.StartMessage) })
	position := make([]int, len(sorted))
	for s, ix := range sorted {
		position[ix.emitted] = s + 1
		out.Compartments[s] = ix.c
	}
	remap := func(anchor int) int {
		if anchor >= 1 && anchor <= len(position) {
			return position[anchor-1]
		}
		return anchor
	}
	for i := range out.Events {
		if out.Events[i].AtCompartment != 0 {
			out.Events[i].AtCompartment = remap(out.Events[i].AtCompartment)
		}
	}
	for i := range out.PrimerCandidates {
		if out.PrimerCandidates[i].OriginCompartmentIndex != 0 {
			out.PrimerCandidates[i].OriginCompartmentIndex = remap(out.PrimerCandidates[i].OriginCompartmentIndex)
		}
	}
	return out
}

func replaceFirst(s string, re *regexp.Regexp, repl string) string {
	loc := re.FindStringIndex(s)
	if loc == nil {
		return s
	}
	return s[:loc[0]] + repl + s[loc[1]:]
}

func parseEvents(text string) []ParsedEvent {
	m := eventsBlockRe.FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	var events []ParsedEvent
	for _, el := range findPaired(m[1], eventOpenRe) {
		at, _ := strconv.Atoi(el.groups[1])
		fields := map[string]string{}
		for _, f := range findPaired(el.body, eventFieldOpenRe) {
			if v := unescapeXML(strings.TrimSpace(f.body)); v != "" {
				fields[f.groups[0]] = v
			}
		}
		events = append(events, ParsedEvent{Kind: el.groups[0], AtCompartment: at, Fields: fields})
	}
	return events
}
