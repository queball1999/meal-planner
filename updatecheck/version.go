package updatecheck

import (
	"strconv"
	"strings"
)

// Version is a parsed release version: MAJOR.MINOR.PATCH plus an optional
// prerelease suffix ("dev" in v0.0.2-dev).
type Version struct {
	Major, Minor, Patch int
	Pre                 string
}

// Parse reads "v1.2.3", "1.2.3" or "v1.2.3-dev". ok is false for anything
// else, including the "dev" and "dev-abc1234" a non-release build carries.
func Parse(s string) (v Version, ok bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	core, pre, _ := strings.Cut(s, "-")
	core, _, _ = strings.Cut(core, "+") // build metadata never affects order

	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, false
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Version{}, false
		}
		nums[i] = n
	}
	return Version{Major: nums[0], Minor: nums[1], Patch: nums[2], Pre: pre}, true
}

// Compare returns -1, 0 or 1. A prerelease sorts before its release
// (v0.0.2-dev < v0.0.2); two prereleases of the same version compare by
// their suffix text.
func Compare(a, b Version) int {
	for _, d := range [][2]int{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		switch {
		case d[0] < d[1]:
			return -1
		case d[0] > d[1]:
			return 1
		}
	}
	switch {
	case a.Pre == b.Pre:
		return 0
	case a.Pre == "":
		return 1
	case b.Pre == "":
		return -1
	}
	return strings.Compare(a.Pre, b.Pre)
}

// Newer reports whether latest is a newer version than current. Anything
// that doesn't parse is never newer.
func Newer(latest, current string) bool {
	l, ok1 := Parse(latest)
	c, ok2 := Parse(current)
	return ok1 && ok2 && Compare(l, c) > 0
}
