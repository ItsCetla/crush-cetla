package control

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Ladder draws a progress bar of the given width, for example
// "█████·····" at 50%. It never shows full until pct reaches 100.
func Ladder(pct float64, width int) string {
	if width <= 0 {
		return ""
	}
	if math.IsNaN(pct) || pct < 0 {
		pct = 0
	}
	filled := int(math.Floor(pct / 100 * float64(width)))
	if pct < 100 && filled >= width {
		filled = width - 1
	}
	if pct >= 100 {
		filled = width
	}
	if pct > 0 && filled == 0 {
		filled = 1
	}
	return strings.Repeat("█", filled) + strings.Repeat("·", width-filled)
}

// Pips draws mission progress as filled and empty blocks, for example
// "▮▮▮▯▯".
func Pips(done, total, max int) string {
	if total <= 0 {
		return ""
	}
	if total <= max {
		return strings.Repeat("▮", done) + strings.Repeat("▯", total-done)
	}
	filled := done * max / total
	if done > 0 && filled == 0 {
		filled = 1
	}
	return strings.Repeat("▮", filled) + strings.Repeat("▯", max-filled)
}

var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// Sparkline draws one rune per value, scaled to the largest. Zero values
// draw as a space-like low bar so gaps stay visible.
func Sparkline(values []float64) string {
	maxV := 0.0
	for _, v := range values {
		maxV = math.Max(maxV, v)
	}
	var b strings.Builder
	for _, v := range values {
		if maxV <= 0 || v <= 0 {
			b.WriteRune('·')
			continue
		}
		i := int(math.Round(v / maxV * float64(len(sparkRunes)-1)))
		b.WriteRune(sparkRunes[max(0, min(i, len(sparkRunes)-1))])
	}
	return b.String()
}

// DailyCost returns counted spend per day for the last n days, oldest
// first, ending today.
func DailyCost(rows []Usage, n int, now time.Time) []float64 {
	out := make([]float64, n)
	today := startOfDay(now)
	for _, r := range rows {
		if !r.Pricing.Counted() {
			continue
		}
		d := startOfDay(r.At)
		days := int(math.Round(today.Sub(d).Hours() / 24))
		if days >= 0 && days < n {
			out[n-1-days] += r.Cost
		}
	}
	return out
}

// BurnRate returns counted dollars per hour over the rows, or false when
// there's too little data (fewer than 3 priced rows, or under a minute).
func BurnRate(rows []Usage) (float64, bool) {
	var first, last time.Time
	n := 0
	cost := 0.0
	for _, r := range rows {
		if !r.Pricing.Counted() {
			continue
		}
		if n == 0 || r.At.Before(first) {
			first = r.At
		}
		if r.At.After(last) {
			last = r.At
		}
		cost += r.Cost
		n++
	}
	span := last.Sub(first)
	if n < 3 || span < time.Minute {
		return 0, false
	}
	return cost / span.Hours(), true
}

// ModelCost is one model's share of spend.
type ModelCost struct {
	Model   string  `json:"model"`
	Cost    float64 `json:"cost"`
	Tokens  int64   `json:"tokens"`
	Calls   int     `json:"calls"`
	Pricing Pricing `json:"pricing"`
}

// TopModels groups rows by model and returns the n with the most counted
// spend (then most tokens).
func TopModels(rows []Usage, n int) []ModelCost {
	by := map[string]*ModelCost{}
	for _, r := range rows {
		name := ShortModel(r.Model)
		m := by[name]
		if m == nil {
			m = &ModelCost{Model: name, Pricing: r.Pricing}
			by[name] = m
		}
		if r.Pricing.Counted() {
			m.Cost += r.Cost
			m.Pricing = r.Pricing
		}
		m.Tokens += r.Tokens()
		m.Calls++
	}
	out := make([]ModelCost, 0, len(by))
	for _, m := range by {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Cost != out[j].Cost {
			return out[i].Cost > out[j].Cost
		}
		if out[i].Tokens != out[j].Tokens {
			return out[i].Tokens > out[j].Tokens
		}
		return out[i].Model < out[j].Model
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// ShortModel trims a model id to something that fits a narrow column:
// no vendor prefix and no date suffix.
func ShortModel(id string) string {
	if id == "" {
		return "unknown"
	}
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	parts := strings.Split(id, "-")
	if n := len(parts); n > 1 && len(parts[n-1]) == 8 && isDigits(parts[n-1]) {
		id = strings.Join(parts[:n-1], "-")
	}
	return id
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// Tokens formats a token count compactly: 950, 12.3k, 4.1M.
func Tokens(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
}

// Ago formats how long ago t was: "now", "45s ago", "3m ago", "2h ago",
// "4d ago".
func Ago(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case t.IsZero():
		return "never"
	case d < 5*time.Second:
		return "now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// MissionGlyph is the status mark for a mission.
func MissionGlyph(s MissionStatus, done, total int) string {
	switch s {
	case MissionDone:
		return "●"
	case MissionAbandoned:
		return "⊘"
	case MissionPaused:
		return "·"
	}
	if total > 0 && done > 0 {
		return "◐"
	}
	return "○"
}
