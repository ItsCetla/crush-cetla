package config

// BudgetOptions cap counted model spend. Spend is counted from list
// prices of the models you configure; flat-rate plans such as a Claude
// subscription are never counted. A zero limit means no cap.
type BudgetOptions struct {
	Daily   float64 `json:"daily,omitempty" jsonschema:"description=Daily budget in US dollars. When today's counted spend reaches it\\, Ta-Da asks before the next model call,example=5"`
	Monthly float64 `json:"monthly,omitempty" jsonschema:"description=Monthly budget in US dollars,example=50"`
	Session float64 `json:"session,omitempty" jsonschema:"description=Budget for one session in US dollars,example=2"`
	WarnAt  []int   `json:"warn_at,omitempty" jsonschema:"description=Percentages of a budget that show a warning. Defaults to 50\\, 75 and 90,example=80"`
}

// AlwaysAskClasses returns the action classes that always ask, with the
// default (delete) applied when the list wasn't set. An explicit empty
// list turns it off.
func (p *Permissions) AlwaysAskClasses() []string {
	if p == nil || p.AlwaysAsk == nil {
		return []string{"delete"}
	}
	return p.AlwaysAsk
}
