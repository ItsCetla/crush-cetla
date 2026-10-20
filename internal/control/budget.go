package control

import (
	"fmt"
	"slices"
	"time"
)

// Budget caps counted spend. A zero limit means no cap for that window.
type Budget struct {
	Daily   float64 `json:"daily,omitempty"`
	Monthly float64 `json:"monthly,omitempty"`
	Session float64 `json:"session,omitempty"`
	// WarnAt lists the percentages that show a warning (default 50, 75,
	// 90).
	WarnAt []int `json:"warn_at,omitempty"`
}

// Enabled reports whether any window has a limit.
func (b Budget) Enabled() bool { return b.Daily > 0 || b.Monthly > 0 || b.Session > 0 }

// Warnings returns the warning thresholds, sorted.
func (b Budget) Warnings() []int {
	if len(b.WarnAt) == 0 {
		return []int{50, 75, 90}
	}
	out := slices.Clone(b.WarnAt)
	slices.Sort(out)
	return out
}

// Window is a budget period.
type Window string

const (
	WindowSession Window = "session"
	WindowToday   Window = "today"
	WindowMonth   Window = "month"
)

// Label is how the window reads in a sentence ("the daily budget").
func (w Window) Label() string {
	switch w {
	case WindowSession:
		return "session"
	case WindowToday:
		return "daily"
	case WindowMonth:
		return "monthly"
	}
	return string(w)
}

// BudgetStatus is one window's spend against its limit.
type BudgetStatus struct {
	Window Window  `json:"window"`
	Limit  float64 `json:"limit"`
	// Base is the configured limit before any extensions.
	Base  float64 `json:"base"`
	Spent float64 `json:"spent"`
	Pct   float64 `json:"pct"`
	Over  bool    `json:"over"`
	// Warn is the highest warning threshold reached, or 0.
	Warn int `json:"warn,omitempty"`
}

// Totals sums counted spend.
type Totals struct {
	Cost     float64 `json:"cost"`
	Tokens   int64   `json:"tokens"`
	Calls    int     `json:"calls"`
	Unpriced int     `json:"unpriced"`
	// Flat is the list-price value of flat-rate usage, which isn't
	// counted.
	Flat      float64 `json:"flat"`
	FlatCalls int     `json:"flat_calls"`
}

// Sum totals rows that pass keep.
func Sum(rows []Usage, keep func(Usage) bool) Totals {
	var t Totals
	for _, r := range rows {
		if keep != nil && !keep(r) {
			continue
		}
		t.Tokens += r.Tokens()
		switch {
		case r.Pricing.Counted():
			t.Cost += r.Cost
			t.Calls++
		case r.Pricing == PricingFlat:
			t.Flat += r.Cost
			t.FlatCalls++
		default:
			t.Unpriced++
		}
	}
	return t
}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func startOfMonth(t time.Time) time.Time {
	y, m, _ := t.Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, t.Location())
}

// windowKey names the current instance of a window, so extensions reset
// when a new day or month starts.
func windowKey(w Window, sessionID string, now time.Time) string {
	switch w {
	case WindowSession:
		return "session:" + sessionID
	case WindowToday:
		return "today:" + now.Format("2006-01-02")
	default:
		return "month:" + now.Format("2006-01")
	}
}

// Evaluate returns the status of every window that has a limit. rows must
// cover at least the current month. extra holds the extensions granted so
// far, keyed by windowKey.
func Evaluate(b Budget, rows []Usage, sessionID string, now time.Time, extra map[string]float64) []BudgetStatus {
	day, month := startOfDay(now), startOfMonth(now)
	type win struct {
		w     Window
		limit float64
		keep  func(Usage) bool
	}
	wins := []win{
		{WindowSession, b.Session, func(u Usage) bool { return sessionID != "" && u.SessionID == sessionID }},
		{WindowToday, b.Daily, func(u Usage) bool { return !u.At.Before(day) }},
		{WindowMonth, b.Monthly, func(u Usage) bool { return !u.At.Before(month) }},
	}
	warns := b.Warnings()
	var out []BudgetStatus
	for _, w := range wins {
		if w.limit <= 0 || (w.w == WindowSession && sessionID == "") {
			continue
		}
		limit := w.limit + extra[windowKey(w.w, sessionID, now)]
		spent := Sum(rows, w.keep).Cost
		st := BudgetStatus{Window: w.w, Base: w.limit, Limit: limit, Spent: spent}
		st.Pct = spent / limit * 100
		st.Over = spent >= limit
		for _, p := range warns {
			if st.Pct >= float64(p) {
				st.Warn = p
			}
		}
		out = append(out, st)
	}
	return out
}

// Money formats a dollar amount: cents above a dollar, more precision
// below it so small sessions don't all read $0.00.
func Money(v float64) string {
	switch {
	case v == 0:
		return "$0"
	case v < 0.01:
		return fmt.Sprintf("$%.4f", v)
	case v < 1:
		return fmt.Sprintf("$%.3f", v)
	case v < 1000:
		return fmt.Sprintf("$%.2f", v)
	default:
		return fmt.Sprintf("$%.0f", v)
	}
}

// OverMessage is the permission prompt for continuing past a budget.
func OverMessage(st BudgetStatus) string {
	who := map[Window]string{
		WindowSession: "This session's spend",
		WindowToday:   "Today's spend",
		WindowMonth:   "This month's spend",
	}[st.Window]
	return fmt.Sprintf("%s %s reached the %s %s budget. Allow continuing up to %s?",
		who, Money(st.Spent), Money(st.Limit), st.Window.Label(), Money(NextLimit(st)))
}

// Extension is how much a grant raises a window: a quarter of its
// configured limit.
func Extension(st BudgetStatus) float64 { return st.Base * 0.25 }

// NextLimit is the limit after a grant: a quarter of the configured limit
// past whichever is higher, the current limit or what's been spent, so a
// big overrun doesn't prompt again right away.
func NextLimit(st BudgetStatus) float64 { return max(st.Limit, st.Spent) + Extension(st) }
