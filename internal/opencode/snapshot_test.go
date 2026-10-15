package opencode

import (
	"os"
	"slices"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/catwalk/pkg/embedded"
	"github.com/stretchr/testify/require"
)

// TestSnapshotSanity converts a real models.dev api.json and compares the
// result with catwalk's embedded data. It only runs when
// OPENCODE_MODELS_SNAPSHOT points at the file.
func TestSnapshotSanity(t *testing.T) {
	path := os.Getenv("OPENCODE_MODELS_SNAPSHOT")
	if path == "" {
		t.Skip("set OPENCODE_MODELS_SNAPSHOT to a models.dev api.json to run")
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	md, err := ParseCatalog(raw)
	require.NoError(t, err)

	inModels := 0
	for _, p := range md {
		inModels += len(p.Models)
	}
	out := ToCatwalk(md)
	outModels := 0
	types := map[catwalk.Type]int{}
	for _, p := range out {
		outModels += len(p.Models)
		types[p.Type]++
		require.True(t, SafeTemplate(p.APIKey), p.ID)
		require.True(t, SafeTemplate(p.APIEndpoint), p.ID)
		require.NotEmpty(t, p.DefaultLargeModelID, p.ID)
		require.NotEmpty(t, p.DefaultSmallModelID, p.ID)
	}
	t.Logf("models.dev: %d providers, %d models", len(md), inModels)
	t.Logf("catwalk:    %d providers, %d models, types %v", len(out), outModels, types)

	ids := make([]string, 0, len(out))
	for _, p := range out {
		ids = append(ids, string(p.ID))
	}
	require.True(t, slices.IsSorted(ids))
	require.Len(t, slices.Compact(slices.Clone(ids)), len(ids), "duplicate provider IDs")

	byID := map[catwalk.InferenceProvider]catwalk.Provider{}
	for _, p := range out {
		byID[p.ID] = p
	}
	var skipped []string
	for _, id := range providerIDs(md) {
		if _, ok := byID[catwalk.InferenceProvider(CatwalkID(id))]; !ok {
			skipped = append(skipped, id+" ("+md[id].NPM+")")
		}
	}
	t.Logf("skipped providers: %v", skipped)
	for _, ref := range embedded.GetAll() {
		got, ok := byID[ref.ID]
		if !ok {
			t.Logf("%-18s missing from models.dev conversion", ref.ID)
			continue
		}
		shared, exactTokens := 0, 0
		refModels := map[string]catwalk.Model{}
		for _, m := range ref.Models {
			refModels[m.ID] = m
		}
		for _, m := range got.Models {
			r, ok := refModels[m.ID]
			if !ok {
				continue
			}
			shared++
			if r.DefaultMaxTokens == m.DefaultMaxTokens {
				exactTokens++
			}
		}
		t.Logf("%-18s type %-13s vs %-13s endpoint %-45q key %-30q models %4d (catwalk %3d, shared %3d, max-tokens exact %3d) large=%s small=%s",
			ref.ID, got.Type, ref.Type, got.APIEndpoint, got.APIKey, len(got.Models), len(ref.Models), shared, exactTokens,
			got.DefaultLargeModelID, got.DefaultSmallModelID)
	}

	if os.Getenv("OPENCODE_MODELS_SNAPSHOT_SPOT") != "" {
		for _, id := range []catwalk.InferenceProvider{"anthropic", "openai", "openrouter", "deepseek", "groq"} {
			p := byID[id]
			for _, m := range p.Models {
				t.Logf("%s/%s ctx=%d max=%d in=%.4g out=%.4g inC=%.4g outC=%.4g reason=%v levels=%v def=%q img=%v",
					id, m.ID, m.ContextWindow, m.DefaultMaxTokens, m.CostPer1MIn, m.CostPer1MOut,
					m.CostPer1MInCached, m.CostPer1MOutCached, m.CanReason, m.ReasoningLevels,
					m.DefaultReasoningEffort, m.SupportsImages)
			}
		}
	}
}
