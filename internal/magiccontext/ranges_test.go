// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): tests for features/magic-context/range-parser.ts.

package magiccontext

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseRangeString(t *testing.T) {
	t.Parallel()
	got, err := ParseRangeString("1-3, 8,§12§,[dropped §5§], tag 20, 2")
	require.NoError(t, err)
	require.Equal(t, []int{1, 2, 3, 5, 8, 12, 20}, got)

	_, err = ParseRangeString("  ")
	require.EqualError(t, err, "Range string must not be empty")
	_, err = ParseRangeString("5-3")
	require.EqualError(t, err, `Invalid range "5-3": start (5) must be <= end (3)`)
	_, err = ParseRangeString("1-1001")
	require.ErrorContains(t, err, "exceeds maximum size of 1000 elements (got 1001)")
	_, err = ParseRangeString("x")
	require.EqualError(t, err, `Invalid integer: "x"`)
}

func TestTagStrip(t *testing.T) {
	t.Parallel()
	require.Equal(t, "hello", StripTagPrefix("§3§ §4§ hello"))
	require.Equal(t, "§1 hello", PrependTag(7, "§1 hello")[len("§7§ "):][0:0]+"§1 hello")
	require.Equal(t, "hi there", StripTagPrefix(`§5">§ hi there`))
	require.Equal(t, "version §1.5 ok", StripTagPrefix("version §1.5 ok"))
	require.Equal(t, "done see", StripPersistedText("§12§ done §3§see"))
	require.Equal(t, "§9§ body", PrependTag(9, "§2§ body"))
	require.Equal(t, "[dropped §4§]", TagPlaceholder(4))
}

func TestSettings(t *testing.T) {
	t.Parallel()
	s := SettingsFromConfig(nil)
	require.Equal(t, 65, s.ExecuteThreshold)
	require.Equal(t, 5*time.Minute, s.CacheTTL)
	w := s.WindowFor(200000)
	require.Equal(t, 130000, w.Usable)
	require.Equal(t, 10400, s.ProtectedTokensFor(w)) // min(16000, 10400)=10400 > 6500
	require.Equal(t, 6500, w.TriggerBudget())
	require.InDelta(t, 85.0, w.ForceMaterializationPct(), 0.001)
	require.Equal(t, 32000, HistorianChunkTokens(128000))
	require.Equal(t, time.Duration(-1), ParseCacheTTL("never"))
	require.Equal(t, 90*time.Second, ParseCacheTTL("90s"))
	require.Equal(t, 1500*time.Millisecond, ParseCacheTTL("1500"))
	require.Equal(t, 5*time.Minute, ParseCacheTTL("bogus"))
	require.Equal(t, 3, s.ProtectedToolCount("CTX_REDUCE"))

	now := int64(10_000_000)
	require.False(t, s.ShouldExecute(w, 0, 0, now))
	require.True(t, s.ShouldExecute(w, 70, now-1000, now))
	require.False(t, s.ShouldExecute(w, 40, now-1000, now))
	require.True(t, s.ShouldExecute(w, 40, now-6*60*1000, now))
}
