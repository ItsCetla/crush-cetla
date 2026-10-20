// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): config/schema/magic-context.ts.

package config

// MagicContextOptions configures Magic Context, a Go port of
// cortexkit/magic-context (MIT, Copyright (c) 2025 Ufuk Altinok). When
// enabled it replaces auto-summarize for the main agent: a background
// historian compresses old turns into compartments, the agent can drop
// tagged tool output with ctx_reduce, and project memories, notes and
// search carry knowledge across sessions. Everything is local except the
// model calls the historian and dreamer make through your configured
// providers, and the optional embedding endpoint.
type MagicContextOptions struct {
	Enabled bool `json:"enabled,omitempty" jsonschema:"description=Turn on Magic Context for the main agent. Replaces auto-summarize,default=false"`
	// ExecuteThresholdPercentage is the context usage at which queued drops
	// apply and the historian starts (20-90).
	ExecuteThresholdPercentage int  `json:"execute_threshold_percentage,omitempty" jsonschema:"description=Context usage percentage at which queued drops apply and the historian runs (20-90),default=65,minimum=20,maximum=90"`
	ExecuteThresholdTokens     int  `json:"execute_threshold_tokens,omitempty" jsonschema:"description=Absolute token threshold that overrides execute_threshold_percentage (min 5000),minimum=0"`
	ProtectedTokens            int  `json:"protected_tokens,omitempty" jsonschema:"description=Newest tokens that are never dropped. Default is derived from the context window (min 4000),minimum=0"`
	KeepReasoningTokens        *int `json:"keep_reasoning_tokens,omitempty" jsonschema:"description=Reasoning tokens kept in the live tail,default=10000"`
	// HistoryBudgetPercentage is the share of usable context the rendered
	// compartment history may take.
	HistoryBudgetPercentage float64 `json:"history_budget_percentage,omitempty" jsonschema:"description=Fraction of usable context the session history may use (0.05-0.5),default=0.15"`
	CacheTTL                string  `json:"cache_ttl,omitempty" jsonschema:"description=Provider prompt-cache lifetime. Queued drops wait for it to expire unless usage passes the threshold. Like 5m\\, 1h or never,default=5m"`
	HistorianTimeoutMS      int     `json:"historian_timeout_ms,omitempty" jsonschema:"description=Historian run timeout in milliseconds (min 60000),default=600000"`
	TemporalAwareness       *bool   `json:"temporal_awareness,omitempty" jsonschema:"description=Add time-gap markers and dates to history,default=true"`
	// ProtectedTools keeps the newest N results of each named tool.
	ProtectedTools       map[string]int          `json:"protected_tools,omitempty" jsonschema:"description=Keep the newest N results of these tools out of drops (merged with todos:1 and ctx_reduce:3)"`
	CommitClusterTrigger *MCCommitClusterOptions `json:"commit_cluster_trigger,omitempty"`
	Historian            *MCHistorianOptions     `json:"historian,omitempty"`
	Memory               *MCMemoryOptions        `json:"memory,omitempty"`
	Embedding            *MCEmbeddingOptions     `json:"embedding,omitempty"`
	SmartNotes           *MCToggle               `json:"smart_notes,omitempty"`
	Dreamer              *MCDreamerOptions       `json:"dreamer,omitempty"`
	SystemPromptInject   *MCToggle               `json:"system_prompt_injection,omitempty"`
	Language             string                  `json:"language,omitempty" jsonschema:"description=ISO 639-1 language for historian prose,example=en"`
}

// MCToggle is a nested {enabled} switch.
type MCToggle struct {
	Enabled *bool `json:"enabled,omitempty"`
}

// MCCommitClusterOptions triggers the historian after enough commit clusters.
type MCCommitClusterOptions struct {
	Enabled     *bool `json:"enabled,omitempty" jsonschema:"default=true"`
	MinClusters int   `json:"min_clusters,omitempty" jsonschema:"default=3"`
}

// MCHistorianOptions selects the historian model.
type MCHistorianOptions struct {
	// Model is "large" (the session model), "small", or "provider/model".
	Model   string `json:"model,omitempty" jsonschema:"description=Historian model: large (session model)\\, small\\, or provider/model,default=large,example=small"`
	TwoPass bool   `json:"two_pass,omitempty" jsonschema:"description=Run a second editor pass over each historian draft,default=false"`
}

// MCMemoryOptions configures cross-session project memory.
type MCMemoryOptions struct {
	Enabled               *bool                `json:"enabled,omitempty" jsonschema:"default=true"`
	InjectionBudgetTokens int                  `json:"injection_budget_tokens,omitempty" jsonschema:"default=4000,minimum=0"`
	AutoPromote           *bool                `json:"auto_promote,omitempty" jsonschema:"description=Promote historian facts to project memory,default=true"`
	AutoSearch            *MCAutoSearchOptions `json:"auto_search,omitempty"`
	GitCommitIndexing     *MCGitIndexOptions   `json:"git_commit_indexing,omitempty"`
}

// MCAutoSearchOptions configures the per-prompt related-context hint.
type MCAutoSearchOptions struct {
	Enabled        *bool   `json:"enabled,omitempty" jsonschema:"default=true"`
	ScoreThreshold float64 `json:"score_threshold,omitempty" jsonschema:"default=0.6"`
	MinPromptChars int     `json:"min_prompt_chars,omitempty" jsonschema:"default=20"`
}

// MCGitIndexOptions indexes local git commits for ctx_search.
type MCGitIndexOptions struct {
	Enabled    bool `json:"enabled,omitempty" jsonschema:"default=false"`
	SinceDays  int  `json:"since_days,omitempty" jsonschema:"default=365"`
	MaxCommits int  `json:"max_commits,omitempty" jsonschema:"default=2000"`
}

// MCEmbeddingOptions enables semantic search through an OpenAI-compatible
// embeddings endpoint. Off by default: no text leaves the machine for
// embeddings unless an endpoint is configured.
type MCEmbeddingOptions struct {
	Provider string `json:"provider,omitempty" jsonschema:"description=off or openai-compatible,enum=off,enum=openai-compatible,default=off"`
	Endpoint string `json:"endpoint,omitempty" jsonschema:"description=Base URL of an OpenAI-compatible API,example=http://localhost:1234/v1"`
	Model    string `json:"model,omitempty" jsonschema:"example=text-embedding-nomic-embed-text-v1.5"`
	APIKey   string `json:"api_key,omitempty"`
}

// MCDreamerOptions configures the background dreamer.
type MCDreamerOptions struct {
	Disable bool `json:"disable,omitempty" jsonschema:"description=Turn off the dreamer (also turns off smart notes),default=false"`
	// Model is "small" (default), "large", or "provider/model".
	Model string                       `json:"model,omitempty" jsonschema:"default=small"`
	Tasks map[string]MCDreamerTaskOpts `json:"tasks,omitempty"`
	// InjectDocs adds ARCHITECTURE.md and STRUCTURE.md to the cached history block.
	InjectDocs *bool `json:"inject_docs,omitempty" jsonschema:"description=Inject ARCHITECTURE.md and STRUCTURE.md into the cached history block,default=true"`
}

// MCDreamerTaskOpts overrides one dreamer task.
type MCDreamerTaskOpts struct {
	Schedule           *string `json:"schedule,omitempty" jsonschema:"description=5-field cron or empty to disable"`
	PromotionThreshold int     `json:"promotion_threshold,omitempty"`
	RecencyDays        int     `json:"recency_days,omitempty"`
}

// MagicContextEnabled reports whether Magic Context is on.
func (o *Options) MagicContextEnabled() bool {
	return o != nil && o.MagicContext != nil && o.MagicContext.Enabled
}
