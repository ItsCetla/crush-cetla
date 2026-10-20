// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): hooks/magic-context/reference-seeds.generated.ts.

package magiccontext

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Reference retrieval constants, from upstream reference-retrieval.ts.
const (
	seedFloor              = 3
	sessionRefWindow       = 4
	sessionRefDiverse      = 3
	sessionReferencesLimit = 7
)

var seedBands = [5][2]int{{85, 100}, {60, 84}, {30, 59}, {10, 29}, {1, 9}}

func seedBandIndex(importance int) int {
	for i, b := range seedBands {
		if importance >= b[0] && importance <= b[1] {
			return i
		}
	}
	if importance > 100 {
		return 0
	}
	return len(seedBands) - 1
}

// fnv1a hashes the UTF-16 code units of s, matching upstream's JS version.
func fnv1a(s string) uint32 {
	h := uint32(0x811c9dc5)
	for _, c := range utf16.Encode([]rune(s)) {
		h ^= uint32(c)
		h *= 16777619
	}
	return h
}

// selectSeeds picks count calibration seeds spread across importance bands,
// deterministically for a given session.
func selectSeeds(sessionID string, chunkStart, count int) []referenceSeed {
	corpus := referenceSeeds()
	if count <= 0 || len(corpus) == 0 {
		return nil
	}
	seed := int(fnv1a(sessionID + ":" + strconv.Itoa(chunkStart)))
	var bands [5][]referenceSeed
	for _, s := range corpus {
		bi := seedBandIndex(s.importance)
		bands[bi] = append(bands[bi], s)
	}
	var bandOrder [5]int
	for i := range bandOrder {
		bandOrder[i] = (i + seed%5) % 5
	}
	var picks []referenceSeed
	picked := map[string]bool{}
	for bi, guard := 0, 0; len(picks) < count && guard < 20; guard++ {
		band := bands[bandOrder[bi%5]]
		bi++
		if len(band) == 0 {
			continue
		}
		s := band[(seed+len(picks))%len(band)]
		if !picked[s.block] {
			picked[s.block] = true
			picks = append(picks, s)
		}
	}
	for i := 0; len(picks) < count && i < len(corpus); i++ {
		s := corpus[(seed+i)%len(corpus)]
		if !picked[s.block] {
			picked[s.block] = true
			picks = append(picks, s)
		}
	}
	return picks
}

func renderSeedExamplesBlock(picks []referenceSeed) string {
	if len(picks) == 0 {
		return ""
	}
	blocks := make([]string, len(picks))
	for i, p := range picks {
		blocks[i] = p.block
	}
	return "<compartment_examples_from_other_projects>\n" + strings.Join(blocks, "\n\n") + "\n</compartment_examples_from_other_projects>"
}

func isNoContentCompartment(c Compartment) bool {
	return c.Title == "" && c.Content == "" && c.P1 == "" && c.P2 == "" && c.P3 == "" && c.P4 == ""
}

func compartmentImportance(c Compartment) int {
	if c.Importance == nil {
		return 50
	}
	return *c.Importance
}

func renderSessionRefCompartment(c Compartment, showImportance bool) string {
	attrs := `start="` + strconv.Itoa(c.StartOrdinal) + `" end="` + strconv.Itoa(c.EndOrdinal) + `" title="` + escapeXMLAttr(c.Title) + `"`
	if c.EpisodeType != "" {
		attrs += ` episode_type="` + escapeXMLAttr(c.EpisodeType) + `"`
	}
	if showImportance {
		attrs += ` importance="` + strconv.Itoa(compartmentImportance(c)) + `"`
	}
	if c.P1 != "" {
		p4 := "<p4/>"
		if c.P4 != "" {
			p4 = "<p4>\n" + escapeXMLContent(c.P4) + "\n</p4>"
		}
		return strings.Join([]string{
			"<compartment " + attrs + ">",
			"<p1>\n" + escapeXMLContent(c.P1) + "\n</p1>",
			"<p2>\n" + escapeXMLContent(c.P2) + "\n</p2>",
			"<p3>\n" + escapeXMLContent(c.P3) + "\n</p3>",
			p4,
			"</compartment>",
		}, "\n")
	}
	return "<compartment " + attrs + ">\n" + escapeXMLContent(c.Content) + "\n</compartment>"
}

// selectSessionReferences returns three importance-diverse older compartments
// (in session order) followed by the four most recent ones.
func selectSessionReferences(rows []Compartment, seedPicks []referenceSeed, sessionID string, chunkStart int) []Compartment {
	var eligible []Compartment
	for _, r := range rows {
		if !isNoContentCompartment(r) {
			eligible = append(eligible, r)
		}
	}
	olderCount := max(0, len(eligible)-sessionRefWindow)
	recent := eligible[olderCount:]
	hash := int(fnv1a(sessionID + ":" + strconv.Itoa(chunkStart)))

	var counts [5]int
	for _, s := range seedPicks {
		counts[seedBandIndex(s.importance)]++
	}
	var bands [5][]int
	for i := range olderCount {
		bi := seedBandIndex(compartmentImportance(eligible[i]))
		bands[bi] = append(bands[bi], i)
	}
	var bandOrder [5]int
	for i := range bandOrder {
		bandOrder[i] = (i + hash%5) % 5
	}
	var picks []int
	for len(picks) < sessionRefDiverse {
		best := -1
		for _, b := range bandOrder {
			if len(bands[b]) == 0 {
				continue
			}
			if best == -1 || counts[b] < counts[best] {
				best = b
			}
		}
		if best == -1 {
			break
		}
		at := (hash + len(picks)) % len(bands[best])
		picks = append(picks, bands[best][at])
		bands[best] = slices.Delete(bands[best], at, at+1)
		counts[best]++
	}
	slices.Sort(picks)
	out := make([]Compartment, 0, len(picks)+len(recent))
	for _, i := range picks {
		out = append(out, eligible[i])
	}
	return append(out, recent...)
}

// renderSessionReferencesBlock renders the selected references; only the
// diverse (older) ones carry their importance score.
func renderSessionReferencesBlock(selected []Compartment, window float64) string {
	var rows []Compartment
	for _, r := range selected {
		if !isNoContentCompartment(r) {
			rows = append(rows, r)
		}
	}
	count := int(math.Max(0, math.Min(sessionReferencesLimit, math.Floor(window))))
	if len(rows) == 0 || count == 0 {
		return ""
	}
	recentStart := max(0, len(rows)-sessionRefWindow)
	trimStart := max(0, len(rows)-count)
	rows = rows[trimStart:]
	parts := make([]string, len(rows))
	for i, r := range rows {
		parts[i] = renderSessionRefCompartment(r, i+trimStart < recentStart)
	}
	return "<session_references>\n" + strings.Join(parts, "\n\n") + "\n</session_references>"
}

// buildReferenceBlocks builds the seed and session-reference blocks for one
// historian run; deterministic for a given session, chunk start and history.
func buildReferenceBlocks(sessionID string, chunkStart int, history []Compartment) (seedExamples, sessionReferences string) {
	picks := selectSeeds(sessionID, chunkStart, seedFloor)
	return renderSeedExamplesBlock(picks),
		renderSessionReferencesBlock(selectSessionReferences(history, picks, sessionID, chunkStart), sessionReferencesLimit)
}
