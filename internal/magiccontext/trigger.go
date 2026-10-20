// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): hooks/magic-context/compartment-trigger.ts, context-limit-resolution.ts.

package magiccontext

import "math"

// Trigger constants (hooks/magic-context/compartment-trigger.ts).
const (
	proactiveTriggerOffset     = 2
	postDropTargetRatio        = 0.75
	minProactiveTailTokens     = 6000
	minProactiveTailMessages   = 12
	defaultMinCommitClusters   = 3
	tailSizeTriggerMultiplier  = 3
	blockUntilDonePercentage   = 95
	defaultTriggerContextLimit = 128000
)

// Trigger reasons.
const (
	ReasonForceBand         = "force_band"
	ReasonCommitClusters    = "commit_clusters"
	ReasonTailSize          = "tail_size"
	ReasonProjectedHeadroom = "projected_headroom"
)

// TriggerInput is everything the historian trigger looks at.
type TriggerInput struct {
	Settings Settings
	Window   Window
	UsagePct float64
	Raw      []RawMessage
	LastEnd  int
	// ProjectedPostDropPct is the usage after queued drops land, or < 0 when
	// unknown.
	ProjectedPostDropPct float64
	// DropsCanLand reports whether queued drops would be applied this pass.
	DropsCanLand bool
}

// TriggerDecision is the trigger's verdict.
type TriggerDecision struct {
	Fire     bool
	Reason   string
	Block    bool // usage ≥ 95%: wait for the historian before sending
	Boundary TailBoundary
}

// ResolveContextLimit picks the context limit: the model's, else derived
// from usage, else 128k.
func ResolveContextLimit(modelLimit int, inputTokens int64, pct float64) int {
	if modelLimit > 0 {
		return modelLimit
	}
	if pct > 0 && inputTokens > 0 {
		return int(math.Round(float64(inputTokens) / (pct / 100)))
	}
	return defaultTriggerContextLimit
}

func runnableWindow(b TailBoundary) bool {
	return b.Offset < min(b.ProtectedTailStart, b.EligibleEnd)
}

// EvaluateTrigger decides whether the historian should run now.
func EvaluateTrigger(in TriggerInput) TriggerDecision {
	count := 0
	for _, m := range in.Raw {
		count = max(count, m.Ordinal)
	}
	if count <= in.LastEnd {
		return TriggerDecision{}
	}
	tb := in.Window.TriggerBudget()
	boundary := ResolveProtectedTail(TailRequest{Raw: in.Raw, LastEnd: in.LastEnd, Window: in.Window, UsagePct: in.UsagePct})
	hasHead := runnableWindow(boundary)

	var tokenEstimate, commitClusters int
	var chunkHasMore, meaningful bool
	if hasHead {
		scan := max(minProactiveTailTokens, tb*tailSizeTriggerMultiplier)
		ch := ReadChunk(in.Raw, boundary.Offset, boundary.EligibleEnd, scan)
		idx := NewTokenIndex(in.Raw)
		trueRaw := idx.Range(boundary.Offset, boundary.EligibleEnd)
		tokenEstimate = ch.TokenEstimate
		chunkHasMore = ch.HasMore
		commitClusters = ch.CommitClusters
		meaningful = ch.HasMore || trueRaw >= minProactiveTailTokens ||
			ch.TokenEstimate >= minProactiveTailTokens || ch.MessageCount >= minProactiveTailMessages
	}

	target := in.Window.Exec * postDropTargetRatio
	redundant := in.DropsCanLand && in.ProjectedPostDropPct >= 0 && in.ProjectedPostDropPct <= target
	block := in.UsagePct >= blockUntilDonePercentage

	if in.UsagePct >= in.Window.ForceMaterializationPct() {
		if redundant {
			return TriggerDecision{}
		}
		if hasHead {
			return TriggerDecision{Fire: true, Reason: ReasonForceBand, Block: block, Boundary: boundary}
		}
		scale := 0.5
		if in.UsagePct >= blockUntilDonePercentage {
			scale = 0.25
		}
		scaled := ResolveProtectedTail(TailRequest{Raw: in.Raw, LastEnd: in.LastEnd, Window: in.Window, UsagePct: in.UsagePct, TailScale: scale})
		if runnableWindow(scaled) {
			return TriggerDecision{Fire: true, Reason: ReasonForceBand, Block: block, Boundary: scaled}
		}
		return TriggerDecision{}
	}
	if !hasHead {
		return TriggerDecision{}
	}

	minClusters := in.Settings.MinCommitClusters
	if minClusters <= 0 {
		minClusters = defaultMinCommitClusters
	}
	if in.Settings.CommitClusters && commitClusters >= minClusters && tokenEstimate >= tb {
		return TriggerDecision{Fire: true, Reason: ReasonCommitClusters, Boundary: boundary}
	}
	if tokenEstimate >= tb*tailSizeTriggerMultiplier || (chunkHasMore && tokenEstimate > 0) {
		return TriggerDecision{Fire: true, Reason: ReasonTailSize, Boundary: boundary}
	}
	if in.UsagePct < in.Window.Exec-proactiveTriggerOffset {
		return TriggerDecision{}
	}
	if redundant || !meaningful {
		return TriggerDecision{}
	}
	return TriggerDecision{Fire: true, Reason: ReasonProjectedHeadroom, Boundary: boundary}
}

// ProjectedPostDropPct estimates usage after queued drops land: usage scaled
// by the share of active tag bytes that stays.
func ProjectedPostDropPct(pct float64, active []Tag, pending map[int64]bool) float64 {
	total, drop := 0, 0
	for _, t := range active {
		sz := t.ByteSize + t.InputByteSize
		total += sz
		if pending[t.ID] {
			drop += sz
		}
	}
	if total == 0 {
		return -1
	}
	return pct * (1 - float64(drop)/float64(total))
}
