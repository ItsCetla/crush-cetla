// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): hooks/magic-context/protected-tail-boundary.ts.

package magiccontext

import "math"

// Protected-tail constants (hooks/magic-context/protected-tail-boundary.ts).
const (
	tailAlpha          = 0.3
	tailFloorRatio     = 0.08
	tailFloorMin       = 2000
	tailFloorMax       = 12000
	tailAbsCap         = 96000
	tailMaxUsableRatio = 0.4
	tailHysteresis     = 256
	hugeMessageMin     = 64000
	snapCostCap        = 48000
)

// ProtectedTailTarget derives N, the token size of the protected tail.
func ProtectedTailTarget(w Window, usagePct float64) int {
	u := max(1, int(math.Round(float64(w.ContextLimit)*w.Exec/100)))
	uf := float64(u)
	reserve := max(1000, int(math.Round(uf*0.02)))
	rawN := int(math.Round(uf * tailAlpha * (1 - usagePct/100)))
	floorN := min(tailFloorMax, max(tailFloorMin, int(math.Round(uf*tailFloorRatio))))
	headroom := min(w.TriggerBudget()+reserve, int(math.Floor(uf*0.5)))
	ceilN := max(1, min(tailAbsCap, int(math.Floor(uf*tailMaxUsableRatio)), u-headroom))
	return min(ceilN, max(min(floorN, ceilN), rawN))
}

// PerRunCap bounds how many raw tokens one historian run may consume.
func PerRunCap(w Window, usagePct float64, n int) int {
	u := float64(max(1, w.Usable))
	switch {
	case usagePct >= EmergencyPercentage:
		return min(750000, max(4*n, min(int(math.Round(0.5*u)), 250000)))
	case usagePct >= 80:
		return min(500000, max(3*n, min(int(math.Round(0.35*u)), 150000)))
	default:
		return min(250000, max(2*n, min(int(math.Round(0.25*u)), 100000)))
	}
}

// MinForceEligibleTokens is the smallest eligible prefix worth forcing.
func MinForceEligibleTokens(scaledN int) int { return min(1000, max(1, scaledN/8)) }

// TailBoundary is the resolved split between historian-eligible history
// and the protected recent tail.
type TailBoundary struct {
	Offset             int // first unsummarized ordinal
	ProtectedTailStart int // first protected ordinal
	EligibleEnd        int // exclusive end of what this run may consume
	TargetTokens       int
	Oversize           bool
}

// TailRequest carries the inputs of ResolveProtectedTail.
type TailRequest struct {
	Raw         []RawMessage
	LastEnd     int // last compartment end ordinal, 0 if none
	Window      Window
	UsagePct    float64
	TailScale   float64 // emergency scale (0 = none)
	FullRecomp  bool
	KeepLastRaw int // wrapup: keep this many raw messages (0 = off)
}

// ResolveProtectedTail computes the protected-tail boundary and the
// eligible head for the next historian run.
func ResolveProtectedTail(req TailRequest) TailBoundary {
	idx := NewTokenIndex(req.Raw)
	arcs := BuildToolArcs(req.Raw)
	count := idx.Count()
	offset := max(1, req.LastEnd+1)
	n := ProtectedTailTarget(req.Window, req.UsagePct)
	scaledN := n
	if req.TailScale > 0 {
		scaledN = max(1, int(math.Round(float64(n)*req.TailScale)))
	}
	byOrd := make(map[int]RawMessage, len(req.Raw))
	for _, m := range req.Raw {
		byOrd[m.Ordinal] = m
	}

	if req.FullRecomp {
		start := count + 1
		for _, a := range arcs {
			if a.Res == 0 && a.Inv >= offset {
				start = min(start, a.Inv)
			}
		}
		return TailBoundary{Offset: offset, ProtectedTailStart: start, EligibleEnd: start, TargetTokens: 0}
	}

	var boundary int
	if req.KeepLastRaw > 0 {
		boundary = max(1, count-req.KeepLastRaw+1)
	} else {
		boundary = idx.SuffixStartFor(scaledN)
	}
	recentCutoff := boundary
	huge := max(2*scaledN, hugeMessageMin)
	if idx.TokenFor(boundary) > huge && boundary < count {
		boundary++
	}
	boundary = FenceArcs(boundary, arcs, req.LastEnd, recentCutoff)

	// Semantic snap back to the start of a user turn or tool step.
	snapCap := min(int(math.Round(1.5*float64(scaledN))), snapCostCap)
	if req.KeepLastRaw > 0 {
		snapCap = min(max(req.Window.TriggerBudget(), 2000), snapCostCap)
	}
	for o := min(boundary, count); o >= offset; o-- {
		m, ok := byOrd[o]
		if !ok {
			continue
		}
		if !m.MeaningfulUser() && len(m.ToolCalls) == 0 && len(m.ToolResults) == 0 {
			continue
		}
		extra := idx.Range(o, boundary)
		if extra > snapCap {
			break
		}
		if m.MeaningfulUser() && m.Tokens > huge {
			break
		}
		boundary = o
		break
	}
	boundary = FenceArcs(boundary, arcs, req.LastEnd, recentCutoff)

	start := max(boundary, offset)
	if req.TailScale == 0 && req.UsagePct < req.Window.ForceMaterializationPct() {
		lastUser := 0
		for i := len(req.Raw) - 1; i >= 0; i-- {
			if req.Raw[i].MeaningfulUser() {
				lastUser = req.Raw[i].Ordinal
				break
			}
		}
		if lastUser >= offset {
			start = min(start, lastUser)
		}
	}
	if start > offset && idx.Range(offset, start) <= tailHysteresis {
		start = offset
	}
	start = min(count+1, max(1, start))
	start = max(offset, FenceCompleted(start, arcs, 1))

	capTokens := PerRunCap(req.Window, req.UsagePct, scaledN)
	end, oversize := applyHeadCap(idx, start, offset, arcs, req.LastEnd, capTokens, recentCutoff)
	end = max(offset, FenceCompleted(end, arcs, 1))
	end = min(end, start)
	return TailBoundary{Offset: offset, ProtectedTailStart: start, EligibleEnd: end, TargetTokens: scaledN, Oversize: oversize}
}

func applyHeadCap(idx TokenIndex, start, offset int, arcs []ToolArc, lastEnd, capTokens, recentCutoff int) (int, bool) {
	if start <= offset {
		return offset, false
	}
	end := idx.HeadEndForCap(offset, start, capTokens)
	oversize := end == offset+1 && idx.TokenFor(offset) > capTokens
	fence := FenceCompleted(end, arcs, lastEnd+1)
	if fence <= offset && end > offset {
		if alt := FenceCompleted(end, arcs, offset+1); alt <= start {
			fence = alt
		}
	}
	if fence > end {
		end = min(start, fence)
		if idx.Range(offset, end) > capTokens {
			oversize = true
		}
	} else {
		end = fence
	}
	for _, a := range arcs {
		if a.Res == 0 && a.Inv >= recentCutoff && a.Inv >= offset && a.Inv < end {
			end = a.Inv
			break
		}
	}
	end = FenceCompleted(end, arcs, lastEnd+1)
	if end > start {
		end = offset
	}
	if end <= offset {
		return offset, oversize
	}
	return end, oversize
}
