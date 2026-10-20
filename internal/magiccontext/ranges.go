// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): features/magic-context/range-parser.ts.

package magiccontext

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	rangeDroppedRe = regexp.MustCompile(`\[dropped\s+§(\d+)§\]`)
	rangeTagWordRe = regexp.MustCompile(`tag\s+(\d+)`)
	rangeTagRe     = regexp.MustCompile(`§(\d+)§?`)
	digitsRe       = regexp.MustCompile(`^\d+$`)
)

const maxRangeElements = 1000

// ParseRangeString parses "3-5", "1,2,9" or "1-5,8,12-15" into sorted unique
// ints. Copied §N§ handles, "tag N" and dropped placeholders are accepted.
func ParseRangeString(input string) ([]int, error) {
	s := rangeDroppedRe.ReplaceAllString(input, "$1")
	s = rangeTagWordRe.ReplaceAllString(s, "$1")
	s = rangeTagRe.ReplaceAllString(s, "$1")
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("Range string must not be empty")
	}
	set := map[int]struct{}{}
	for seg := range strings.SplitSeq(s, ",") {
		part := strings.TrimSpace(seg)
		if before, after, ok := strings.Cut(part, "-"); ok {
			start, err := parseRangeInt(strings.TrimSpace(before))
			if err != nil {
				return nil, err
			}
			end, err := parseRangeInt(strings.TrimSpace(after))
			if err != nil {
				return nil, err
			}
			if start > end {
				return nil, fmt.Errorf("Invalid range %q: start (%d) must be <= end (%d)", part, start, end)
			}
			if size := end - start + 1; size > maxRangeElements {
				return nil, fmt.Errorf("Range %q exceeds maximum size of %d elements (got %d)", part, maxRangeElements, size)
			}
			for i := start; i <= end; i++ {
				set[i] = struct{}{}
			}
			continue
		}
		n, err := parseRangeInt(part)
		if err != nil {
			return nil, err
		}
		set[n] = struct{}{}
	}
	if len(set) > maxRangeElements {
		return nil, fmt.Errorf("Total range size exceeds maximum of %d elements (got %d)", maxRangeElements, len(set))
	}
	out := make([]int, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	slices.Sort(out)
	return out, nil
}

func parseRangeInt(s string) (int, error) {
	if !digitsRe.MatchString(s) {
		return 0, fmt.Errorf("Invalid integer: %q", s)
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("Invalid integer: %q", s)
	}
	return n, nil
}
