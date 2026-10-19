package update

import (
	"regexp"
	"strconv"
	"strings"
)

type parsedVersion struct {
	nums []int
	pre  string
}

var versionRe = regexp.MustCompile(`^v?(\d+(?:\.\d+)*)(?:-([0-9A-Za-z.-]+))?`)

// parseVersion turns "4.1.0" or "v4.1.0-beta.2" into its numbers and
// prerelease part. It reports false when s holds no version number.
func parseVersion(s string) (parsedVersion, bool) {
	m := versionRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return parsedVersion{}, false
	}
	parts := strings.Split(m[1], ".")
	nums := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return parsedVersion{}, false
		}
		nums[i] = n
	}
	return parsedVersion{nums: nums, pre: m[2]}, true
}

// ValidVersion reports whether s starts with a version number.
func ValidVersion(s string) bool {
	_, ok := parseVersion(s)
	return ok
}

// CompareVersions returns > 0 when a is newer than b, < 0 when it is older
// and 0 when they are the same (or either is not a version). A full release
// beats a prerelease of the same number, and a leading "v" is optional.
func CompareVersions(a, b string) int {
	x, okX := parseVersion(a)
	y, okY := parseVersion(b)
	if !okX || !okY {
		return 0
	}
	for i := range max(len(x.nums), len(y.nums)) {
		var xn, yn int
		if i < len(x.nums) {
			xn = x.nums[i]
		}
		if i < len(y.nums) {
			yn = y.nums[i]
		}
		if xn != yn {
			if xn > yn {
				return 1
			}
			return -1
		}
	}
	switch {
	case x.pre == y.pre:
		return 0
	case x.pre == "":
		return 1
	case y.pre == "":
		return -1
	}
	return comparePrerelease(x.pre, y.pre)
}

// comparePrerelease compares dot-separated prerelease identifiers, numeric
// ones numerically (so beta.10 > beta.2).
func comparePrerelease(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := range min(len(as), len(bs)) {
		an, aErr := strconv.Atoi(as[i])
		bn, bErr := strconv.Atoi(bs[i])
		switch {
		case aErr == nil && bErr == nil:
			if an != bn {
				if an > bn {
					return 1
				}
				return -1
			}
		case aErr == nil:
			return -1
		case bErr == nil:
			return 1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	switch {
	case len(as) > len(bs):
		return 1
	case len(as) < len(bs):
		return -1
	}
	return 0
}

var pseudoVersionRe = regexp.MustCompile(`\d{14}-[0-9a-f]{12}`)

// IsDevVersion reports whether v belongs to a development build: no version
// stamped in by ldflags ("devel"), a dev or snapshot version
// ("0.1.0-dev.<sha>", "0.0.0-<timestamp>"), a Go pseudo-version, a dirty
// build, or anything that is not a version number at all.
func IsDevVersion(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || v == "devel" || v == "(devel)" || v == "unknown" {
		return true
	}
	p, ok := parseVersion(v)
	if !ok {
		return true
	}
	if strings.Contains(v, "dirty") || pseudoVersionRe.MatchString(v) {
		return true
	}
	if p.pre != "" {
		for id := range strings.SplitSeq(p.pre, ".") {
			if strings.HasPrefix(id, "dev") {
				return true
			}
		}
	}
	for _, n := range p.nums {
		if n != 0 {
			return false
		}
	}
	return true // 0.0.0-<timestamp> snapshots
}

// trimV strips a leading "v" from a tag name.
func trimV(tag string) string {
	return strings.TrimPrefix(strings.TrimSpace(tag), "v")
}
