// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): features/magic-context/dreamer/cron.ts.

// Five-field cron parsing and next-occurrence search for dreamer task
// schedules. Ported from cortexkit/magic-context (MIT):
// features/magic-context/dreamer/cron.ts.
package magiccontext

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CronSchedule is a parsed 5-field cron expression (local time).
type CronSchedule struct {
	minute, hour, dom, month, dow uint64
	domRestricted, dowRestricted  bool
}

type cronField struct {
	name     string
	min, max int
}

var cronFields = [5]cronField{
	{"minute", 0, 59}, {"hour", 0, 23}, {"day-of-month", 1, 31}, {"month", 1, 12}, {"day-of-week", 0, 7},
}

// ParseCron parses "min hour dom month dow".
func ParseCron(expr string) (*CronSchedule, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, errors.New("empty cron expression")
	}
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return nil, fmt.Errorf("expected 5 fields (minute hour day-of-month month day-of-week), got %d", len(parts))
	}
	var bits [5]uint64
	for i, p := range parts {
		b, err := parseCronField(p, cronFields[i])
		if err != nil {
			return nil, err
		}
		bits[i] = b
	}
	if bits[4]&(1<<7) != 0 {
		bits[4] = bits[4]&^(1<<7) | 1
	}
	return &CronSchedule{
		minute: bits[0], hour: bits[1], dom: bits[2], month: bits[3], dow: bits[4],
		domRestricted: !strings.HasPrefix(parts[2], "*"),
		dowRestricted: !strings.HasPrefix(parts[4], "*"),
	}, nil
}

func parseCronField(field string, f cronField) (uint64, error) {
	var bits uint64
	for tok := range strings.SplitSeq(field, ",") {
		bad := fmt.Errorf("invalid %s field %q (allowed %d-%d)", f.name, tok, f.min, f.max)
		rng, stepStr, hasStep := strings.Cut(tok, "/")
		step := 1
		if hasStep {
			if strings.Contains(stepStr, "/") || !cronDigits(stepStr) {
				return 0, bad
			}
			n, err := strconv.Atoi(stepStr)
			if err != nil || n < 1 {
				return 0, bad
			}
			step = n
		}
		var lo, hi int
		switch {
		case rng == "*":
			lo, hi = f.min, f.max
		case strings.Contains(rng, "-"):
			a, b, _ := strings.Cut(rng, "-")
			if !cronDigits(a) || !cronDigits(b) {
				return 0, bad
			}
			lo, _ = strconv.Atoi(a)
			hi, _ = strconv.Atoi(b)
			if lo > hi {
				return 0, bad
			}
		default:
			if !cronDigits(rng) {
				return 0, bad
			}
			lo, _ = strconv.Atoi(rng)
			hi = lo
			if hasStep {
				hi = f.max
			}
		}
		if lo < f.min || hi > f.max {
			return 0, bad
		}
		for v := lo; v <= hi; v += step {
			bits |= 1 << uint(v)
		}
	}
	return bits, nil
}

func cronDigits(s string) bool {
	if s == "" || len(s) > 4 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (c *CronSchedule) dayMatches(t time.Time) bool {
	domOK := c.dom&(1<<uint(t.Day())) != 0
	dowOK := c.dow&(1<<uint(t.Weekday())) != 0
	if c.domRestricted && c.dowRestricted {
		return domOK || dowOK
	}
	return domOK && dowOK
}

// dayOfMonthCanOccur reports whether some allowed dom fits some allowed
// month (Feb counted as 29 days).
func (c *CronSchedule) dayOfMonthCanOccur() bool {
	if c.domRestricted && c.dowRestricted {
		return true
	}
	days := [13]int{0, 31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	for m := 1; m <= 12; m++ {
		if c.month&(1<<uint(m)) == 0 {
			continue
		}
		for d := 1; d <= days[m]; d++ {
			if c.dom&(1<<uint(d)) != 0 {
				return true
			}
		}
	}
	return false
}

// Next returns the first matching minute strictly after after, searching at
// most min(4·366 days, maxSearch) (maxSearch <= 0 = 4·366 days).
func (c *CronSchedule) Next(after time.Time, maxSearch time.Duration) (time.Time, bool) {
	if !c.dayOfMonthCanOccur() {
		return time.Time{}, false
	}
	limit := 4 * 366 * 24 * time.Hour
	if maxSearch > 0 {
		limit = min(limit, maxSearch)
	}
	after = after.In(time.Local)
	capT := after.Add(limit)
	t := after.Truncate(time.Minute).Add(time.Minute)
	for !t.After(capT) {
		y, mo, d := t.Date()
		if c.month&(1<<uint(mo)) == 0 {
			t = time.Date(y, mo+1, 1, 0, 0, 0, 0, time.Local)
			continue
		}
		if !c.dayMatches(t) {
			t = time.Date(y, mo, d+1, 0, 0, 0, 0, time.Local)
			continue
		}
		if c.hour&(1<<uint(t.Hour())) == 0 {
			t = time.Date(y, mo, d, t.Hour()+1, 0, 0, 0, time.Local)
			continue
		}
		if c.minute&(1<<uint(t.Minute())) == 0 {
			t = t.Add(time.Minute)
			continue
		}
		return t, true
	}
	return time.Time{}, false
}

// NextDueAtMs is the next occurrence after nowMs in unix ms, or 0 for an
// empty, invalid or never-matching expression.
func NextDueAtMs(expr string, nowMs int64) int64 {
	c, err := ParseCron(expr)
	if err != nil {
		return 0
	}
	t, ok := c.Next(time.UnixMilli(nowMs), 0)
	if !ok {
		return 0
	}
	return t.UnixMilli()
}
