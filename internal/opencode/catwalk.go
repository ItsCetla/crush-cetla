package opencode

import (
	"cmp"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"charm.land/catwalk/pkg/catwalk"
)

// wire describes how Ta-Da talks to an AI SDK package: the catwalk
// provider type and the endpoint used when models.dev has no "api".
type wire struct {
	typ      catwalk.Type
	endpoint string
}

// npmWires maps the AI SDK packages models.dev names to Ta-Da wire
// protocols. Packages that are not listed (Perplexity, GitLab, watsonx,
// SAP, Cloudflare's gateway, Vertex's Anthropic flavour, Bedrock Mantle,
// ...) are unsupported.
//
// Endpoints follow Ta-Da's client conventions rather than the AI SDK's:
// Anthropic's client appends /v1 itself and Google's appends the API
// version, so their endpoints stop at the host.
var npmWires = map[string]wire{
	"@ai-sdk/openai-compatible":   {catwalk.TypeOpenAICompat, ""},
	"@ai-sdk/anthropic":           {catwalk.TypeAnthropic, "https://api.anthropic.com"},
	"@ai-sdk/openai":              {catwalk.TypeOpenAI, "https://api.openai.com/v1"},
	"@openrouter/ai-sdk-provider": {catwalk.TypeOpenRouter, "https://openrouter.ai/api/v1"},
	"@ai-sdk/azure":               {catwalk.TypeAzure, ""},
	"@ai-sdk/google":              {catwalk.TypeGoogle, "https://generativelanguage.googleapis.com"},
	"@ai-sdk/google-vertex":       {catwalk.TypeVertexAI, ""},
	"@ai-sdk/amazon-bedrock":      {catwalk.TypeBedrock, ""},
	"@ai-sdk/gateway":             {catwalk.TypeVercel, "https://ai-gateway.vercel.sh/v1"},
	"@ai-sdk/groq":                {catwalk.TypeOpenAICompat, "https://api.groq.com/openai/v1"},
	"@ai-sdk/xai":                 {catwalk.TypeOpenAICompat, "https://api.x.ai/v1"},
	"@ai-sdk/mistral":             {catwalk.TypeOpenAICompat, "https://api.mistral.ai/v1"},
	"@ai-sdk/deepinfra":           {catwalk.TypeOpenAICompat, "https://api.deepinfra.com/v1/openai"},
	"@ai-sdk/togetherai":          {catwalk.TypeOpenAICompat, "https://api.together.xyz/v1"},
	"@ai-sdk/cerebras":            {catwalk.TypeOpenAICompat, "https://api.cerebras.ai/v1"},
	"@ai-sdk/cohere":              {catwalk.TypeOpenAICompat, "https://api.cohere.ai/compatibility/v1"},
	"venice-ai-sdk-provider":      {catwalk.TypeOpenAICompat, "https://api.venice.ai/api/v1"},
}

// keyEnvRe matches environment variables that hold a credential, as
// opposed to account IDs, regions, hosts or resource names.
var keyEnvRe = regexp.MustCompile(`(KEY|TOKEN|PAT|SECRET)$`)

// providerIDRe limits the provider IDs ToCatwalk emits.
var providerIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// resolveWire returns the catwalk type and endpoint for an AI SDK package
// served at api (models.dev's "api", possibly empty).
func resolveWire(npm, api string, env []string) (catwalk.Type, string, bool) {
	w, ok := npmWires[npm]
	if !ok {
		return "", "", false
	}
	endpoint := strings.TrimRight(api, "/")
	switch w.typ {
	case catwalk.TypeOpenRouter, catwalk.TypeVercel:
		// Ta-Da always talks to the public OpenRouter and Vercel
		// endpoints, so anything else speaking those protocols is
		// treated as OpenAI-compatible.
		if endpoint != "" && endpoint != w.endpoint {
			return catwalk.TypeOpenAICompat, endpoint, true
		}
		return w.typ, w.endpoint, true
	case catwalk.TypeAnthropic:
		endpoint = strings.TrimSuffix(endpoint, "/v1")
	case catwalk.TypeGoogle:
		endpoint = strings.TrimSuffix(strings.TrimSuffix(endpoint, "/v1beta"), "/v1")
	case catwalk.TypeAzure:
		if endpoint == "" {
			for _, name := range env {
				if strings.HasSuffix(name, "_RESOURCE_NAME") {
					endpoint = "https://${" + name + "}.openai.azure.com"
					break
				}
			}
		}
	case catwalk.TypeBedrock, catwalk.TypeVertexAI:
		// Ta-Da configures these from the cloud SDK's own settings.
		return w.typ, "", true
	}
	if endpoint == "" {
		endpoint = w.endpoint
	}
	if endpoint == "" {
		return "", "", false
	}
	return w.typ, endpoint, true
}

// WireFor returns the catwalk provider type and endpoint Ta-Da uses for an
// AI SDK package served at baseURL (which may be empty). ok is false when
// Ta-Da cannot speak the package's protocol or no endpoint is known.
func WireFor(npm, baseURL string) (typ catwalk.Type, endpoint string, ok bool) {
	return resolveWire(npm, baseURL, nil)
}

// ToCatwalk converts a models.dev catalog into catwalk providers Ta-Da can
// use directly. Provider IDs go through [CatwalkID]; providers and models
// are sorted by ID.
//
// A provider is skipped when its package has no Ta-Da equivalent, its
// "api" is not a [SafeTemplate], any of its env names is not a plain
// upper-case variable name, or no model survives the model filters.
//
// A model is skipped when it is deprecated or alpha, is not a chat model
// (embedding, reranking, video), cannot call tools, does not output text,
// or needs a different protocol or endpoint than its provider (per-model
// package or api overrides, or a Responses/Chat Completions shape the
// provider's protocol cannot serve).
//
// Field mapping: cost input/output map directly; cache_write maps to
// CostPer1MInCached and cache_read to CostPer1MOutCached, as in catwalk's
// own Anthropic data. ContextWindow is limit.context; DefaultMaxTokens is
// [defaultMaxTokens]; ReasoningLevels are the "effort" reasoning option
// values. The API key is a reference to the provider's credential env
// variables (see [providerAPIKey]).
func ToCatwalk(md map[string]ModelsDevProvider) []catwalk.Provider {
	out := make([]catwalk.Provider, 0, len(md))
	seen := map[catwalk.InferenceProvider]bool{}
	for _, key := range providerIDs(md) {
		p := md[key]
		if p.ID == "" {
			p.ID = key
		}
		cp, ok := convertProvider(p)
		if !ok || seen[cp.ID] {
			continue
		}
		seen[cp.ID] = true
		out = append(out, cp)
	}
	slices.SortFunc(out, func(a, b catwalk.Provider) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

func convertProvider(p ModelsDevProvider) (catwalk.Provider, bool) {
	if !providerIDRe.MatchString(p.ID) {
		return catwalk.Provider{}, false
	}
	for _, name := range p.Env {
		if !envNameRe.MatchString(name) {
			return catwalk.Provider{}, false
		}
	}
	if !SafeTemplate(p.API) {
		return catwalk.Provider{}, false
	}
	typ, endpoint, ok := resolveWire(p.NPM, p.API, p.Env)
	if !ok || !SafeTemplate(endpoint) {
		return catwalk.Provider{}, false
	}
	apiKey := providerAPIKey(typ, endpoint, p.Env)
	if !SafeTemplate(apiKey) {
		return catwalk.Provider{}, false
	}

	type candidate struct {
		model   catwalk.Model
		source  ModelsDevModel
		release time.Time
	}
	var models []candidate
	seen := map[string]bool{}
	for _, key := range slices.Sorted(maps.Keys(p.Models)) {
		m := p.Models[key]
		if seen[m.ID] || !keepModel(m) || !sameWire(p, m, typ, endpoint) {
			continue
		}
		seen[m.ID] = true
		models = append(models, candidate{
			model:   convertModel(m, typ),
			source:  m,
			release: parseReleaseDate(m.ReleaseDate),
		})
	}
	if len(models) == 0 {
		return catwalk.Provider{}, false
	}
	slices.SortFunc(models, func(a, b candidate) int { return cmp.Compare(a.model.ID, b.model.ID) })

	cp := catwalk.Provider{
		Name:        cmp.Or(cleanText(p.Name), p.ID),
		ID:          catwalk.InferenceProvider(CatwalkID(p.ID)),
		APIKey:      apiKey,
		APIEndpoint: endpoint,
		Type:        typ,
		Models:      make([]catwalk.Model, 0, len(models)),
	}
	pick := make([]pickable, 0, len(models))
	for _, c := range models {
		cp.Models = append(cp.Models, c.model)
		pick = append(pick, pickable{
			id:      c.model.ID,
			release: c.release,
			beta:    c.source.Status == "beta",
			output:  c.source.Limit.Output,
			context: c.source.Limit.Context,
			hasCost: c.source.Cost != nil,
			costIn:  c.model.CostPer1MIn,
			costOut: c.model.CostPer1MOut,
		})
	}
	cp.DefaultLargeModelID, cp.DefaultSmallModelID = pickDefaults(pick)
	return cp, true
}

// providerAPIKey builds the API key reference: the provider's credential
// variables (names ending in KEY, TOKEN, PAT or SECRET that the endpoint
// does not already use), as "$A" or "${A:-${B}}" for several. Bedrock and
// Vertex authenticate through their cloud SDKs and get no key.
func providerAPIKey(typ catwalk.Type, endpoint string, env []string) string {
	if typ == catwalk.TypeBedrock || typ == catwalk.TypeVertexAI {
		return ""
	}
	used := templateRefs(endpoint)
	var names []string
	for _, name := range env {
		if !used[name] && keyEnvRe.MatchString(name) && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return envTemplate(names)
}

func keepModel(m ModelsDevModel) bool {
	switch {
	case m.ID == "" || cleanText(m.ID) != m.ID:
		return false
	case m.Status == "deprecated" || m.Status == "alpha":
		return false
	case m.Type != "":
		return false
	case !m.ToolCall:
		return false
	case !slices.Contains(m.Modalities.Output, "text"):
		return false
	}
	return true
}

// sameWire reports whether the model can be served through its provider's
// protocol and endpoint.
func sameWire(p ModelsDevProvider, m ModelsDevModel, typ catwalk.Type, endpoint string) bool {
	shape := ""
	if m.Provider != nil {
		shape = m.Provider.Shape
		if m.Provider.NPM != "" || m.Provider.API != "" {
			npm := cmp.Or(m.Provider.NPM, p.NPM)
			api := cmp.Or(m.Provider.API, p.API)
			if !SafeTemplate(api) {
				return false
			}
			mt, me, ok := resolveWire(npm, api, p.Env)
			if !ok || mt != typ || me != endpoint {
				return false
			}
		}
	}
	switch shape {
	case "completions":
		return typ != catwalk.TypeOpenAI && typ != catwalk.TypeAzure
	case "responses":
		return typ != catwalk.TypeOpenAICompat
	}
	return true
}

func convertModel(m ModelsDevModel, typ catwalk.Type) catwalk.Model {
	cm := catwalk.Model{
		ID:               m.ID,
		Name:             cmp.Or(cleanText(m.Name), m.ID),
		ContextWindow:    m.Limit.Context,
		DefaultMaxTokens: defaultMaxTokens(m.Limit.Context, m.Limit.Output),
		CanReason:        m.Reasoning,
		SupportsImages:   slices.Contains(m.Modalities.Input, "image"),
	}
	if m.Cost != nil {
		cm.CostPer1MIn = m.Cost.Input
		cm.CostPer1MOut = m.Cost.Output
		cm.CostPer1MInCached = m.Cost.CacheWrite
		cm.CostPer1MOutCached = m.Cost.CacheRead
	}
	if m.Reasoning {
		for _, opt := range m.ReasoningOptions {
			if opt.Type != "effort" {
				continue
			}
			for _, v := range opt.Values {
				if v != "" && cleanText(v) == v && !slices.Contains(cm.ReasoningLevels, v) {
					cm.ReasoningLevels = append(cm.ReasoningLevels, v)
				}
			}
		}
		cm.DefaultReasoningEffort = defaultEffort(cm.ReasoningLevels, typ)
	}
	return cm
}

// defaultMaxTokens picks the max output tokens Ta-Da requests by default.
//
// Catwalk's hand-maintained providers (Anthropic, OpenAI, and the
// generated Venice, Vercel and OpenCode data) use the model's full output
// limit, and its generators fall back to a tenth of the context window
// when no usable output limit exists. models.dev sometimes reports an
// output limit equal to (or above) the context window, which would leave
// no room for input, so that case also falls back to context/10. Against
// the 458 models catwalk and models.dev share outside OpenRouter (whose
// generator halves the limit), this reproduces catwalk exactly for 79%
// and within 2x for 88%.
func defaultMaxTokens(context, output int64) int64 {
	switch {
	case output > 0 && (context <= 0 || output < context):
		return output
	case context > 0:
		return context / 10
	default:
		return 0
	}
}

// defaultEffort picks the default reasoning effort: "high" for Anthropic
// (the API's own default), otherwise "medium", then "high", then
// "default", then the first level that does not turn reasoning off.
func defaultEffort(levels []string, typ catwalk.Type) string {
	prefs := []string{"medium", "high", "default"}
	if typ == catwalk.TypeAnthropic {
		prefs = []string{"high", "medium", "default"}
	}
	for _, p := range prefs {
		if slices.Contains(levels, p) {
			return p
		}
	}
	for _, l := range levels {
		if l != "none" {
			return l
		}
	}
	return ""
}

// pickable is the subset of a model that default selection looks at.
type pickable struct {
	id              string
	release         time.Time
	beta            bool
	output, context int64
	hasCost         bool
	costIn, costOut float64
}

// recentWindow is how far behind a provider's newest model a model may be
// released and still count as recent for the small-model pick.
const recentWindow = 365 * 24 * time.Hour

// pickDefaults chooses the default large and small models. Beta models are
// only considered when nothing else is left.
//
// Small: among models released within a year of the provider's newest
// model, the cheapest input price (models with known pricing first), then
// the cheapest output price, then the newest, then the lowest ID.
//
// Large: the newest release among models priced above the small pick (so
// a provider's newest budget model does not become its large default;
// all models when none is), then the highest output limit, then the
// largest context window, then the lowest ID.
func pickDefaults(models []pickable) (large, small string) {
	if len(models) == 0 {
		return "", ""
	}
	pool := slices.DeleteFunc(slices.Clone(models), func(m pickable) bool { return m.beta })
	if len(pool) == 0 {
		pool = slices.Clone(models)
	}

	newest := slices.MaxFunc(pool, func(a, b pickable) int { return a.release.Compare(b.release) }).release
	recent := pool
	if !newest.IsZero() {
		cutoff := newest.Add(-recentWindow)
		recent = slices.DeleteFunc(slices.Clone(pool), func(m pickable) bool { return m.release.Before(cutoff) })
	}
	smallest := slices.MinFunc(recent, func(a, b pickable) int {
		return cmp.Or(
			boolOrder(b.hasCost, a.hasCost),
			cmp.Compare(a.costIn, b.costIn),
			cmp.Compare(a.costOut, b.costOut),
			b.release.Compare(a.release),
			cmp.Compare(a.id, b.id),
		)
	})

	candidates := pool
	if smallest.hasCost {
		pricier := slices.DeleteFunc(slices.Clone(pool), func(m pickable) bool {
			return !m.hasCost || m.costIn <= smallest.costIn
		})
		if len(pricier) > 0 {
			candidates = pricier
		}
	}
	largest := slices.MinFunc(candidates, func(a, b pickable) int {
		return cmp.Or(
			b.release.Compare(a.release),
			cmp.Compare(b.output, a.output),
			cmp.Compare(b.context, a.context),
			cmp.Compare(a.id, b.id),
		)
	})
	return largest.id, smallest.id
}

// boolOrder orders false before true.
func boolOrder(a, b bool) int {
	switch {
	case a == b:
		return 0
	case !a:
		return -1
	default:
		return 1
	}
}

// cleanText drops control characters (including terminal escape
// introducers) from catalog text that ends up on screen.
func cleanText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, s)
}

// parseReleaseDate parses models.dev's "YYYY-MM-DD" or "YYYY-MM" dates;
// anything else is the zero time.
func parseReleaseDate(s string) time.Time {
	for _, layout := range []string{"2006-01-02", "2006-01", "2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
