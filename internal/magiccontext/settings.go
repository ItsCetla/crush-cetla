// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): config/schema/magic-context.ts, hooks/magic-context/derive-budgets.ts.

package magiccontext

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/crush/internal/config"
)

// Defaults (config/schema/magic-context.ts).
const (
	DefaultExecuteThreshold    = 65
	MaxExecuteThreshold        = 90
	MinExecuteThreshold        = 20
	ProtectedTokensMin         = 4000
	DefaultKeepReasoningTokens = 10000
	DefaultHistoryBudget       = 0.15
	DefaultHistorianTimeout    = 10 * time.Minute
	DefaultCacheTTL            = "5m"
	DefaultMemoryBudget        = 4000
	FallbackHistoryBudget      = 60000
	DefaultHistorianContext    = 128000
	ForceBandPercentage        = 85
	EmergencyPercentage        = 95
)

// Settings are the resolved Magic Context options.
type Settings struct {
	Enabled                bool
	ExecuteThreshold       int
	ExecuteThresholdTokens int
	ProtectedTokens        int // 0 = derived
	KeepReasoningTokens    int
	HistoryBudget          float64
	CacheTTL               time.Duration // <0 = never
	HistorianTimeout       time.Duration
	TemporalAwareness      bool
	ProtectedTools         map[string]int
	CommitClusters         bool
	MinCommitClusters      int
	HistorianModel         string
	TwoPass                bool
	Language               string

	MemoryEnabled       bool
	MemoryBudget        int
	AutoPromote         bool
	AutoSearch          bool
	AutoSearchThreshold float64
	AutoSearchMinChars  int
	GitIndex            bool
	GitSinceDays        int
	GitMaxCommits       int

	EmbeddingEndpoint string // "" = embeddings off
	EmbeddingModel    string
	EmbeddingAPIKey   string

	SmartNotes      bool
	Dreamer         bool
	DreamerModel    string
	DreamerTasks    map[string]config.MCDreamerTaskOpts
	InjectGuidance  bool
	InjectDocs      bool
	InjectSkipMarks []string
}

func boolOr(p *bool, d bool) bool {
	if p == nil {
		return d
	}
	return *p
}

// SettingsFromConfig resolves options with defaults.
func SettingsFromConfig(o *config.MagicContextOptions) Settings {
	if o == nil {
		o = &config.MagicContextOptions{}
	}
	s := Settings{
		Enabled:                o.Enabled,
		ExecuteThreshold:       DefaultExecuteThreshold,
		ExecuteThresholdTokens: o.ExecuteThresholdTokens,
		KeepReasoningTokens:    DefaultKeepReasoningTokens,
		HistoryBudget:          DefaultHistoryBudget,
		CacheTTL:               ParseCacheTTL(o.CacheTTL),
		HistorianTimeout:       DefaultHistorianTimeout,
		TemporalAwareness:      boolOr(o.TemporalAwareness, true),
		ProtectedTools:         map[string]int{"todos": 1, "todowrite": 1, "ctx_reduce": 3},
		CommitClusters:         true,
		MinCommitClusters:      3,
		HistorianModel:         "large",
		Language:               o.Language,
		MemoryEnabled:          true,
		MemoryBudget:           DefaultMemoryBudget,
		AutoPromote:            true,
		AutoSearch:             true,
		AutoSearchThreshold:    0.6,
		AutoSearchMinChars:     20,
		GitSinceDays:           365,
		GitMaxCommits:          2000,
		SmartNotes:             true,
		Dreamer:                true,
		DreamerModel:           "small",
		InjectGuidance:         boolOr(nilToggle(o.SystemPromptInject), true),
		InjectDocs:             o.Dreamer == nil || boolOr(o.Dreamer.InjectDocs, true),
		InjectSkipMarks:        []string{"<!-- magic-context: skip -->"},
	}
	if v := o.ExecuteThresholdPercentage; v > 0 {
		s.ExecuteThreshold = min(MaxExecuteThreshold, max(MinExecuteThreshold, v))
	}
	if s.ExecuteThresholdTokens > 0 {
		s.ExecuteThresholdTokens = max(5000, s.ExecuteThresholdTokens)
	}
	if o.ProtectedTokens > 0 {
		s.ProtectedTokens = max(ProtectedTokensMin, o.ProtectedTokens)
	}
	if o.KeepReasoningTokens != nil && *o.KeepReasoningTokens >= 0 {
		s.KeepReasoningTokens = *o.KeepReasoningTokens
	}
	if v := o.HistoryBudgetPercentage; v > 0 {
		s.HistoryBudget = math.Min(0.5, math.Max(0.05, v))
	}
	if o.HistorianTimeoutMS > 0 {
		s.HistorianTimeout = time.Duration(max(60000, o.HistorianTimeoutMS)) * time.Millisecond
	}
	for k, v := range o.ProtectedTools {
		s.ProtectedTools[normalizeToolName(k)] = max(0, v)
	}
	if c := o.CommitClusterTrigger; c != nil {
		s.CommitClusters = boolOr(c.Enabled, true)
		if c.MinClusters > 0 {
			s.MinCommitClusters = c.MinClusters
		}
	}
	if h := o.Historian; h != nil {
		if strings.TrimSpace(h.Model) != "" {
			s.HistorianModel = strings.TrimSpace(h.Model)
		}
		s.TwoPass = h.TwoPass
	}
	if m := o.Memory; m != nil {
		s.MemoryEnabled = boolOr(m.Enabled, true)
		if m.InjectionBudgetTokens > 0 {
			s.MemoryBudget = min(20000, max(500, m.InjectionBudgetTokens))
		}
		s.AutoPromote = boolOr(m.AutoPromote, true)
		if a := m.AutoSearch; a != nil {
			s.AutoSearch = boolOr(a.Enabled, true)
			if a.ScoreThreshold > 0 {
				s.AutoSearchThreshold = math.Min(0.95, math.Max(0.3, a.ScoreThreshold))
			}
			if a.MinPromptChars > 0 {
				s.AutoSearchMinChars = a.MinPromptChars
			}
		}
		if g := m.GitCommitIndexing; g != nil {
			s.GitIndex = g.Enabled
			if g.SinceDays > 0 {
				s.GitSinceDays = g.SinceDays
			}
			if g.MaxCommits > 0 {
				s.GitMaxCommits = g.MaxCommits
			}
		}
	}
	if e := o.Embedding; e != nil && e.Provider == "openai-compatible" && strings.TrimSpace(e.Endpoint) != "" {
		s.EmbeddingEndpoint = strings.TrimRight(strings.TrimSpace(e.Endpoint), "/")
		s.EmbeddingModel = e.Model
		s.EmbeddingAPIKey = e.APIKey
	}
	if d := o.Dreamer; d != nil {
		s.Dreamer = !d.Disable
		if strings.TrimSpace(d.Model) != "" {
			s.DreamerModel = strings.TrimSpace(d.Model)
		}
		s.DreamerTasks = d.Tasks
	}
	s.SmartNotes = s.Dreamer && boolOr(nilToggle(o.SmartNotes), true)
	return s
}

func nilToggle(t *config.MCToggle) *bool {
	if t == nil {
		return nil
	}
	return t.Enabled
}

func normalizeToolName(n string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(n)), "mcp_")
}

var ttlPattern = regexp.MustCompile(`^(\d+)([smh])$`)

// ParseCacheTTL parses "5m", "1h", "30s", a bare millisecond count, or
// "never" (returned as -1). Invalid input falls back to 5m.
func ParseCacheTTL(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		s = DefaultCacheTTL
	}
	if strings.EqualFold(s, "never") {
		return -1
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n >= 0 {
		return time.Duration(n) * time.Millisecond
	}
	m := ttlPattern.FindStringSubmatch(s)
	if m == nil {
		return 5 * time.Minute
	}
	n, _ := strconv.Atoi(m[1])
	switch m[2] {
	case "s":
		return time.Duration(n) * time.Second
	case "h":
		return time.Duration(n) * time.Hour
	}
	return time.Duration(n) * time.Minute
}

// Window holds the per-pass derived budgets for a context limit.
type Window struct {
	ContextLimit int
	// Exec is the effective execute threshold percentage.
	Exec float64
	// Usable is ContextLimit × Exec/100.
	Usable int
}

// WindowFor resolves the execute threshold for a context limit.
func (s Settings) WindowFor(contextLimit int) Window {
	w := Window{ContextLimit: contextLimit, Exec: float64(s.ExecuteThreshold)}
	if s.ExecuteThresholdTokens > 0 && contextLimit > 0 {
		pct := float64(s.ExecuteThresholdTokens) / float64(contextLimit) * 100
		w.Exec = math.Min(MaxExecuteThreshold, pct)
	}
	w.Usable = int(math.Round(float64(contextLimit) * w.Exec / 100))
	return w
}

// ProtectedTokensFor returns the protected window size.
func (s Settings) ProtectedTokensFor(w Window) int {
	if s.ProtectedTokens > 0 {
		return s.ProtectedTokens
	}
	u := float64(w.Usable)
	lo := math.Min(16000, math.Round(0.08*u))
	v := math.Max(lo, math.Min(math.Round(0.05*u), 64000))
	return max(ProtectedTokensMin, int(v))
}

// HistoryBudgetFor returns the token budget for rendered history.
func (s Settings) HistoryBudgetFor(w Window) int {
	if w.Usable <= 0 {
		return FallbackHistoryBudget
	}
	return int(math.Round(float64(w.Usable) * s.HistoryBudget))
}

// ForceMaterializationPct is max(85, min(exec,90)+2).
func (w Window) ForceMaterializationPct() float64 {
	return math.Max(ForceBandPercentage, math.Min(w.Exec, 90)+2)
}

// TriggerBudget is clamp(round(ctx·exec/100·0.05), 5000, 50000).
func (w Window) TriggerBudget() int {
	v := int(math.Round(float64(w.ContextLimit) * w.Exec / 100 * 0.05))
	return min(50000, max(5000, v))
}

// HistorianChunkTokens is clamp(round(hctx·0.25), 8000, 50000).
func HistorianChunkTokens(historianContext int) int {
	if historianContext <= 0 {
		historianContext = DefaultHistorianContext
	}
	return min(50000, max(8000, int(math.Round(float64(historianContext)*0.25))))
}

// ProtectedToolCount returns how many newest results of tool are protected.
func (s Settings) ProtectedToolCount(tool string) int {
	return s.ProtectedTools[normalizeToolName(tool)]
}

// ShouldExecute is the scheduler: apply queued drops when usage passed the
// threshold or the provider cache has already gone cold.
func (s Settings) ShouldExecute(w Window, pct float64, lastResponse, now int64) bool {
	if pct == 0 && lastResponse == 0 {
		return false
	}
	if pct >= w.Exec {
		return true
	}
	if s.CacheTTL < 0 || lastResponse == 0 {
		return false
	}
	return now-lastResponse > s.CacheTTL.Milliseconds()
}
