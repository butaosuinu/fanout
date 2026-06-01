// Package blockers parses the two textual blocker sources that --unblocked-only
// consults: a child issue's `## Blocked by` section, and a (blocked by #X, #Y)
// trailer on the parent's task-list row. Mirrors fanout:270-306.
package blockers

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	blockedBySectionStartRE = regexp.MustCompile(`^##\s+blocked by`)
	headingRE               = regexp.MustCompile(`^##`)
	hashNumRE               = regexp.MustCompile(`#([0-9]+)`)
)

// FromChildBody extracts blocker numbers from the first "## Blocked by"
// section in body. The section ends at a blank line or the next heading.
// Heading detection is case-insensitive (matches the awk tolower() in
// fanout:277).
func FromChildBody(body string) []int {
	out := []int{}
	inSection := false
	for _, line := range strings.Split(body, "\n") {
		lower := strings.ToLower(line)
		if blockedBySectionStartRE.MatchString(lower) {
			inSection = true
			continue
		}
		if !inSection {
			continue
		}
		if strings.TrimSpace(line) == "" {
			inSection = false
			continue
		}
		if headingRE.MatchString(line) {
			inSection = false
			continue
		}
		for _, m := range hashNumRE.FindAllStringSubmatch(line, -1) {
			n, err := strconv.Atoi(m[1])
			if err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}

// FromParentRow extracts blocker numbers from the task-list row in parentBody
// that begins with `#child`. Looks for a `(blocked by ...)` trailer (case-
// insensitive) on that row and returns every `#N` inside.
func FromParentRow(parentBody string, child int) []int {
	rowPrefix := regexp.MustCompile(`^\s*-\s+\[[ xX]\]\s*#` + strconv.Itoa(child) + `(?:[^0-9]|$)`)
	blockedByRE := regexp.MustCompile(`(?i)\(blocked by\s+([^)]+)\)`)
	out := []int{}
	for _, line := range strings.Split(parentBody, "\n") {
		if !rowPrefix.MatchString(line) {
			continue
		}
		bm := blockedByRE.FindStringSubmatch(line)
		if len(bm) != 2 {
			continue
		}
		for _, m := range hashNumRE.FindAllStringSubmatch(bm[1], -1) {
			n, err := strconv.Atoi(m[1])
			if err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}

// Dedupe combines two slices into a sorted, unique list.
func Dedupe(a, b []int) []int {
	seen := map[int]bool{}
	for _, n := range append(a, b...) {
		seen[n] = true
	}
	out := make([]int, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}
