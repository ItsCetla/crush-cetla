package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSummarizeThresholdReservesOutput(t *testing.T) {
	t.Parallel()

	// 256k window, 32k max_tokens: the reported overflow case. At 229k
	// prompt tokens the old 20k buffer said there was room; the request
	// still needed 32k for the reply.
	cw, out := int64(262_144), int64(32_768)
	th := summarizeThreshold(cw, out)
	require.Equal(t, int64(largeContextWindowBuffer)+out, th)
	require.LessOrEqual(t, cw-229_377, th, "should summarize before the overflow")

	// Small window keeps the ratio and adds the reply.
	require.Equal(t, int64(6553+8192), summarizeThreshold(32_768, 8192))

	// No max_tokens keeps the old behavior.
	require.Equal(t, int64(largeContextWindowBuffer), summarizeThreshold(cw, 0))

	// A huge max_tokens is capped so it can't summarize every step.
	require.Equal(t, int64(16_384), summarizeThreshold(32_768, 30_000))
}

func TestMagicContextLimitReservesOutput(t *testing.T) {
	t.Parallel()

	require.Equal(t, 262_144-32_768, magicContextLimit(262_144, 32_768))
	require.Equal(t, 262_144, magicContextLimit(262_144, 0))
	require.Equal(t, 0, magicContextLimit(0, 32_768))
	// A reply over half the window is a config problem; don't shrink.
	require.Equal(t, 32_768, magicContextLimit(32_768, 20_000))
}
