// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): hooks/magic-context/decay-curve.ts and decay-render.ts.

package magiccontext

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Decay curve hyperparameters (decay-curve.ts).
const (
	decayH50    = 24.0
	decayD      = 25.0
	decayZ1     = 0.201
	decayZ2     = 0.729
	decayZ3     = 1.322
	decayZ4     = 2.587
	decayPFloor = 0.1
)

// DefaultHistoryBudgetTokens is used when no history budget can be resolved.
const DefaultHistoryBudgetTokens = 60000

// TierCost is the measured average token cost of each tier (index 1..5).
var TierCost = [6]int{0, 322, 109, 35, 20, 5}

func clampImportance(imp int) int {
	return max(1, min(100, imp))
}

func decayZ(index, importance int, pressure float64) float64 {
	a := float64(max(index, 1) - 1)
	imp := float64(clampImportance(importance))
	p := math.Max(pressure, decayPFloor)
	f := math.Pow(2, (imp-50)/decayD)
	h := decayH50 * f / p
	return a / h
}

// DecayTier returns the paraphrase tier (1..5) for a compartment at index
// (1 = newest) with the given importance and budget pressure.
func DecayTier(index, importance int, pressure float64) int {
	z := decayZ(index, importance, pressure)
	switch {
	case z < decayZ1:
		return 1
	case z < decayZ2:
		return 2
	case z < decayZ3:
		return 3
	case z < decayZ4:
		return 4
	}
	return 5
}

// RenderedTier is DecayTier with archive protection (anchor overlap is
// reserved upstream and always 0, so it reduces to the base tier).
func RenderedTier(index, importance int, pressure float64) int {
	if decayZ(index, importance, pressure) >= decayZ4 {
		return 5
	}
	return min(DecayTier(index, importance, pressure), 4)
}

type curveInput struct{ index, importance int }

// BudgetPressure computes the single-pass pressure multiplier.
func budgetPressure(cs []curveInput, budget int) float64 {
	if budget <= 0 {
		return 1
	}
	cost := 0
	for _, c := range cs {
		t := DecayTier(c.index, c.importance, 1)
		if t < 5 {
			cost += TierCost[t]
		}
	}
	return math.Max(decayPFloor, float64(cost)/float64(budget))
}

// DecayCompartment is the renderer's view of a compartment.
type DecayCompartment struct {
	Start, End         int
	Title, Content     string
	StartDate, EndDate string
	P1, P2, P3, P4     string
	Importance         *int
}

// DecayCompartmentsFrom converts stored compartments. Dates are filled only
// when temporal is true.
func DecayCompartmentsFrom(cs []Compartment, temporal bool) []DecayCompartment {
	out := make([]DecayCompartment, 0, len(cs))
	for _, c := range cs {
		d := DecayCompartment{
			Start: c.StartOrdinal, End: c.EndOrdinal,
			Title: c.Title, Content: c.Content,
			P1: c.P1, P2: c.P2, P3: c.P3, P4: c.P4,
			Importance: c.Importance,
		}
		if temporal && c.StartTime > 0 && c.EndTime > 0 {
			d.StartDate = FormatDate(c.StartTime)
			d.EndDate = FormatDate(c.EndTime)
		}
		out = append(out, d)
	}
	return out
}

// FormatDate renders Unix seconds as a local YYYY-MM-DD date.
func FormatDate(sec int64) string {
	return time.Unix(sec, 0).Format("2006-01-02")
}

func (c DecayCompartment) noContent() bool {
	return c.Title == "" && c.Content == "" && c.P1 == "" && c.P2 == "" && c.P3 == "" && c.P4 == ""
}

func (c DecayCompartment) tiered() bool { return c.P1 != "" }

func (c DecayCompartment) importance() int {
	if c.Importance == nil {
		return 50
	}
	return clampImportance(*c.Importance)
}

func formatDateRange(start, end string) string {
	if start == "" || end == "" {
		return ""
	}
	if start == end {
		return start
	}
	if len(start) >= 7 && len(end) >= 10 && start[:7] == end[:7] {
		return start + "→" + end[8:]
	}
	return start + "→" + end
}

func sanitizeTitle(title string) string {
	var b strings.Builder
	inRun := false
	for _, r := range title {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			if !inRun {
				b.WriteByte(' ')
			}
			inRun = true
			continue
		}
		inRun = false
		b.WriteRune(r)
	}
	return escapeXMLContent(b.String())
}

func compartmentHeading(c DecayCompartment) string {
	seg := ""
	if dr := formatDateRange(c.StartDate, c.EndDate); dr != "" {
		seg = " · " + dr
	}
	return "## " + strconv.Itoa(c.Start) + "-" + strconv.Itoa(c.End) + seg + " · " + sanitizeTitle(c.Title)
}

var headingLine = regexp.MustCompile(`(?m)^## `)

func guardBody(body string) string { return headingLine.ReplaceAllString(body, " ## ") }

func tierBody(c DecayCompartment, tier int) string {
	tiers := [4]string{c.P1, c.P2, c.P3, c.P4}
	// Upstream treats a present-but-empty tier string as requested (valid v2
	// row with a title-only P4); stored columns can't distinguish null, so an
	// empty requested tier falls back to denser tiers only below P4.
	if req := tiers[tier-1]; req != "" || tier == 4 {
		return strings.TrimSpace(req)
	}
	for i := tier - 2; i >= 0; i-- {
		if tiers[i] != "" {
			return strings.TrimSpace(tiers[i])
		}
	}
	return strings.TrimSpace(c.Content)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimRightFunc(string(r[:n]), unicode.IsSpace) + "…"
}

func legacyBody(content string, tier int) string {
	switch {
	case tier <= 1:
		return content
	case tier == 2:
		return truncateRunes(content, 1200)
	}
	return truncateRunes(content, 420)
}

var legacyUserLine = regexp.MustCompile(`(?m)^U:`)

func legacyTier(c DecayCompartment) int {
	if legacyUserLine.MatchString(c.Content) {
		return 3
	}
	return 4
}

// RenderCompartmentAtTier renders one compartment at an explicit tier; the
// m1 new-compartments block uses tier 1.
func RenderCompartmentAtTier(c DecayCompartment, tier int) string {
	if c.noContent() || tier >= 5 {
		return ""
	}
	h := compartmentHeading(c)
	if !c.tiered() {
		flat := strings.TrimSpace(c.Content)
		if tier >= 4 || flat == "" {
			return h
		}
		return h + "\n" + guardBody(escapeXMLContent(legacyBody(flat, tier)))
	}
	body := tierBody(c, tier)
	if body == "" {
		return h
	}
	return h + "\n" + guardBody(escapeXMLContent(body))
}

func computeTiers(cs []DecayCompartment, budget int) []int {
	var inputs []curveInput
	idx := make(map[int]int)
	total := 0
	for _, c := range cs {
		if c.tiered() {
			total++
		}
	}
	n := 0
	for i, c := range cs {
		if !c.tiered() {
			continue
		}
		ci := total - n
		n++
		idx[i] = ci
		inputs = append(inputs, curveInput{ci, c.importance()})
	}
	p := 1.0
	if budget > 0 {
		p = budgetPressure(inputs, budget)
	}
	tiers := make([]int, len(cs))
	for i, c := range cs {
		if !c.tiered() {
			tiers[i] = legacyTier(c)
			continue
		}
		ci, ok := idx[i]
		if !ok {
			ci = 1
		}
		tiers[i] = RenderedTier(ci, c.importance(), p)
	}
	return tiers
}

// RenderDecayedCompartments renders the decayed history body (without the
// <session-history> wrapper), demoting oldest compartments first while over
// budget.
func RenderDecayedCompartments(all []DecayCompartment, budget int) string {
	cs := make([]DecayCompartment, 0, len(all))
	for _, c := range all {
		if !c.noContent() {
			cs = append(cs, c)
		}
	}
	if len(cs) == 0 {
		return ""
	}
	tiers := computeTiers(cs, budget)
	rendered := make([][6]*string, len(cs))
	tokens := make([][6]int, len(cs))
	for i := range tokens {
		for t := range tokens[i] {
			tokens[i][t] = -1
		}
	}
	renderedAt := func(i, t int) string {
		if p := rendered[i][t]; p != nil {
			return *p
		}
		s := RenderCompartmentAtTier(cs[i], t)
		rendered[i][t] = &s
		return s
	}
	tokensAt := func(i, t int) int {
		if v := tokens[i][t]; v >= 0 {
			return v
		}
		v := EstimateTokens(renderedAt(i, t))
		tokens[i][t] = v
		return v
	}
	render := func() string {
		parts := make([]string, 0, len(cs))
		for i := range cs {
			if s := renderedAt(i, tiers[i]); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n\n")
	}
	if budget <= 0 {
		return render()
	}
	running := 0
	for i := range tiers {
		running += tokensAt(i, tiers[i])
	}
	guard := len(cs) * 5
	oldest := 0
	demote := func() bool {
		for oldest < len(tiers) && tiers[oldest] >= 5 {
			oldest++
		}
		if oldest >= len(tiers) {
			return false
		}
		prev := tiers[oldest]
		running += tokensAt(oldest, prev+1) - tokensAt(oldest, prev)
		tiers[oldest] = prev + 1
		return true
	}
	for running > budget && guard > 0 {
		if !demote() {
			break
		}
		guard--
	}
	body := render()
	for EstimateTokens(body) > budget && guard > 0 {
		if !demote() {
			break
		}
		guard--
		body = render()
	}
	return body
}

// RenderSessionHistory renders the full <session-history> block with the
// materialize tightening loop: while the block exceeds budget×1.05, raise
// pressure by 1.15× (at most 3 times). Returns "" when there is nothing.
func RenderSessionHistory(cs []DecayCompartment, budget int) string {
	pressure := 1.0
	var block string
	for attempt := 0; ; attempt++ {
		eff := budget
		if budget > 0 {
			eff = int(math.Floor(float64(budget) / math.Max(1, pressure)))
		}
		body := RenderDecayedCompartments(cs, eff)
		if body == "" {
			return ""
		}
		block = "<session-history>\n" + body + "\n</session-history>"
		if budget <= 0 || attempt >= 3 || float64(EstimateTokens(block)) <= float64(budget)*1.05 {
			return block
		}
		pressure *= 1.15
	}
}
