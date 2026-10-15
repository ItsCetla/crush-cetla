package opencode

// modelsDevToCatwalk maps models.dev provider IDs to the catwalk provider
// IDs of the same service: same company, same account and credentials,
// and the same (or an equivalent) endpoint. IDs that are equal in both
// catalogs (anthropic, openai, azure, xai, deepseek, groq, openrouter,
// cerebras, venice, vercel, minimax, opencode-go, ...) are not listed.
//
// models.dev's "zai" (the pay-as-you-go endpoint) is renamed because
// catwalk's "zai" is the coding-plan endpoint, which models.dev calls
// "zai-coding-plan"; this keeps the mapping one-to-one.
var modelsDevToCatwalk = map[string]string{
	"alibaba":             "alibaba-singapore", // dashscope-intl.aliyuncs.com
	"amazon-bedrock":      "bedrock",
	"fireworks-ai":        "fireworks",
	"github-copilot":      "copilot",
	"google":              "gemini",
	"google-vertex":       "vertexai",
	"io-net":              "ionet",
	"kimi-code-plan-cn":   "kimi-coding", // api.kimi.com/coding
	"minimax-cn":          "minimax-china",
	"moonshotai":          "moonshot",
	"opencode":            "opencode-zen",
	"qiniu-ai":            "qiniucloud",
	"zai":                 "zai-paas",
	"zai-coding-plan":     "zai", // api.z.ai/api/coding/paas/v4
	"zhipuai":             "zhipu",
	"zhipuai-coding-plan": "zhipu-coding",
}

var catwalkToModelsDev = func() map[string]string {
	inv := make(map[string]string, len(modelsDevToCatwalk))
	for md, cw := range modelsDevToCatwalk {
		inv[cw] = md
	}
	return inv
}()

// CatwalkID returns the catwalk provider ID for a models.dev (and
// OpenCode) provider ID. Unmapped IDs are returned unchanged.
func CatwalkID(modelsDevID string) string {
	if id, ok := modelsDevToCatwalk[modelsDevID]; ok {
		return id
	}
	return modelsDevID
}

// ModelsDevID is the inverse of [CatwalkID].
func ModelsDevID(catwalkID string) string {
	if id, ok := catwalkToModelsDev[catwalkID]; ok {
		return id
	}
	return catwalkID
}
