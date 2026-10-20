// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): hooks/magic-context/emergency-drop.ts.

package magiccontext

import (
	"context"
	"math"
	"strings"
)

const (
	emergencyTargetFraction  = 0.3
	emergencyRecencyReserve  = 0.2
	emergencyMinReclaimToken = 2000
	emergencyTokensPerByte   = 0.25
)

var (
	emergencyTier1 = map[string]bool{
		"read": true, "view": true, "todowrite": true, "todos": true, "task": true,
		"agent": true, "aft_outline": true, "aft_zoom": true,
	}
	emergencyTier2 = map[string]bool{
		"edit": true, "multiedit": true, "write": true, "apply_patch": true,
		"grep": true, "glob": true, "aft_search": true,
	}
)

// emergencyTier is 1 (keep longest), 2 or 3 (drop first).
func emergencyTier(tool string) int {
	n := strings.TrimPrefix(strings.ToLower(tool), "mcp_")
	switch {
	case emergencyTier1[n]:
		return 1
	case emergencyTier2[n]:
		return 2
	}
	return 3
}

// EmergencyPlan is the outcome of planEmergencyDrop.
type EmergencyPlan struct {
	Reason  string
	Reclaim int
	Drops   []Tag
}

// planEmergencyDrop picks tool outputs to drop right away at the force band.
func (e *Engine) planEmergencyDrop(ps *passState, latched bool) EmergencyPlan {
	limit := ps.window.ContextLimit
	ceiling := float64(limit) * ps.window.Exec / 100
	if ceiling <= 0 || ps.input <= 0 {
		return EmergencyPlan{Reason: "unknown-usage"}
	}
	if latched && ps.pct < EmergencyPercentage {
		return EmergencyPlan{Reason: "pressure-episode-latched"}
	}
	tail := 0
	for _, t := range ps.tags {
		if t.Status == TagActive && t.Type == TagTool {
			tail += tagReclaimTokens(t)
		}
	}
	current := float64(ps.input)
	floor := math.Max(current-float64(tail), 0)
	span := math.Max(ceiling-floor, 0)
	target := floor + emergencyTargetFraction*span
	need := int(math.Round(current - target))
	if need <= emergencyMinReclaimToken {
		return EmergencyPlan{Reason: "below-min-reclaim", Reclaim: need}
	}

	tiers := map[int][]Tag{}
	for _, t := range ps.tags { // oldest first
		if t.Type != TagTool || t.Status != TagActive || ps.protected[t.ID] || ps.tagOrd[t.ID] == 0 {
			continue
		}
		tier := emergencyTier(t.ToolName)
		tiers[tier] = append(tiers[tier], t)
	}
	if ps.pct < EmergencyPercentage {
		for _, tier := range []int{1, 2} {
			list := tiers[tier]
			reserve := int(math.Ceil(float64(len(list)) * emergencyRecencyReserve))
			list = list[:len(list)-reserve]
			kept := list[:0]
			for _, t := range list {
				if ps.tagOrd[t.ID] < ps.protCutoff {
					kept = append(kept, t)
				}
			}
			tiers[tier] = kept
		}
		kept := tiers[3][:0]
		for _, t := range tiers[3] {
			if ps.tagOrd[t.ID] < ps.protCutoff {
				kept = append(kept, t)
			}
		}
		tiers[3] = kept
	}
	plan := EmergencyPlan{}
	for _, tier := range []int{3, 2, 1} {
		for _, t := range tiers[tier] {
			if plan.Reclaim >= need {
				break
			}
			plan.Drops = append(plan.Drops, t)
			plan.Reclaim += tagReclaimTokens(t)
		}
	}
	if len(plan.Drops) == 0 {
		plan.Reason = "no-candidates"
	}
	return plan
}

// emergencyDrop applies the emergency plan immediately (bypassing the queue)
// and latches the pressure episode.
func (e *Engine) emergencyDrop(ctx context.Context, ps *passState, meta SessionMeta) (int, error) {
	plan := e.planEmergencyDrop(ps, meta.EmergencyLatched)
	if len(plan.Drops) == 0 {
		return 0, nil
	}
	drops := make(map[int64]string, len(plan.Drops))
	for _, t := range plan.Drops {
		drops[t.ID] = ps.dropMode(t)
	}
	if err := e.markDropped(ctx, ps, drops); err != nil {
		return 0, err
	}
	if err := e.Store.SetMeta(ctx, ps.in.SessionID, map[string]any{"emergency_latched": true}); err != nil {
		return 0, err
	}
	e.logf("magic-context: emergency drop session=%s tags=%d reclaim=%d pct=%.1f", ps.in.SessionID, len(drops), plan.Reclaim, ps.pct)
	return len(drops), nil
}
