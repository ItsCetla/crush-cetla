// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): tests for compartment-runner-incremental.ts publish hand-offs.

package magiccontext

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// partialHistorian covers the first four turns of the chunk, defers the rest
// with <unprocessed_from>, and reports one observation and one primer.
func partialHistorian(_ context.Context, _, user string, _ int) (string, error) {
	m := fakeRangeRe.FindAllStringSubmatch(user, -1)
	if m == nil {
		return "", fmt.Errorf("no range in prompt")
	}
	var s int
	fmt.Sscan(m[len(m)-1][1], &s)
	var b strings.Builder
	b.WriteString("<output>\n<compartments>\n")
	for st := s; st < s+12; st += 3 {
		fmt.Fprintf(&b, "<compartment start=\"%d\" end=\"%d\" title=\"turn %d\">\n<p1>\nRead file %d.\n</p1>\n<p2>\nRead.\n</p2>\n<p3>\nRead.\n</p3>\n<p4/>\n</compartment>\n", st, st+2, st, st)
	}
	fmt.Fprintf(&b, "</compartments>\n<unprocessed_from>%d</unprocessed_from>\n", s+12)
	b.WriteString("<user_observations>\n* Prefers terse answers\n</user_observations>\n")
	b.WriteString("<primer_candidates>\n<primer at_compartment=\"2\">How is file 4 structured?</primer>\n</primer_candidates>\n</output>")
	return b.String(), nil
}

func TestHistorianPublishesCandidates(t *testing.T) {
	ctx := context.Background()
	for _, dreamer := range []bool{true, false} {
		t.Run(fmt.Sprintf("dreamer=%v", dreamer), func(t *testing.T) {
			e, _ := cmdEngine(t, "s1", false)
			e.Settings.Dreamer = dreamer
			e.Historian = &Historian{
				Store: e.Store, Settings: e.Settings, Generate: partialHistorian, ContextWindow: 200000,
				ProjectKey: "git:p", Logf: t.Logf, Sleep: func(context.Context, time.Duration) error { return nil },
			}
			msgs := convo("s1", 10, uniquePath)
			v, err := e.loadView(ctx, "s1", msgs, 100000)
			require.NoError(t, err)
			res, err := e.Historian.Run(ctx, HistorianRun{
				SessionID: "s1", Raw: v.raw,
				Boundary: TailBoundary{Offset: 1, ProtectedTailStart: 25, EligibleEnd: 25},
			})
			require.NoError(t, err)
			require.Len(t, res.Published, 4)

			cands, err := e.Store.UserMemoryCandidates(ctx)
			require.NoError(t, err)
			if dreamer {
				require.Len(t, cands, 1)
				require.Equal(t, "Prefers terse answers", cands[0].Content)
				require.Equal(t, 1, cands[0].SourceCompartmentStart)
				require.Equal(t, 12, cands[0].SourceCompartmentEnd)
			} else {
				require.Empty(t, cands)
			}

			prims, err := dreamPrimerCandidates(ctx, e.Store.db, "git:p")
			require.NoError(t, err)
			require.Len(t, prims, 1)
			require.Equal(t, "How is file 4 structured?", prims[0].Question)
			require.Equal(t, 4, prims[0].SourceCompartmentStart)
			require.Equal(t, 6, prims[0].SourceCompartmentEnd)
			require.Equal(t, int64(1_700_000_020)*1000, prims[0].SourceMessageTime)
		})
	}
}

func TestPrimerCandidateFallsBackToChunkSpan(t *testing.T) {
	cs := []Compartment{{StartOrdinal: 1, EndOrdinal: 3}, {StartOrdinal: 4, EndOrdinal: 9}}
	now := time.UnixMilli(5000)
	for _, idx := range []int{0, 7} {
		pc, ok := PrimerCandidateFromHistorian("p", "s", []ParsedPrimerCandidate{{Question: " Q? ", OriginCompartmentIndex: idx}}, cs, now)
		require.True(t, ok)
		require.Equal(t, "Q?", pc.Question)
		require.Equal(t, 1, pc.SourceCompartmentStart)
		require.Equal(t, 9, pc.SourceCompartmentEnd)
		require.Equal(t, int64(5000), pc.SourceMessageTime)
	}
	_, ok := PrimerCandidateFromHistorian("", "s", []ParsedPrimerCandidate{{Question: "Q"}}, cs, now)
	require.False(t, ok)
}
