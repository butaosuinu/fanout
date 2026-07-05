// Package blockers parses the two textual blocker sources that --unblocked-only
// consults: a child issue's `## Blocked by` section, and a (blocked by #X, #Y)
// trailer on the parent's task-list row. Mirrors fanout:270-306. It also owns
// the derived blocker-status rows and dependency-wave depths shared by the TUI
// and the web dashboard. The package stays gh-free: callers inject issue state.
package blockers

import (
	"fmt"
	"regexp"
	"slices"
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
	for line := range strings.SplitSeq(body, "\n") {
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
	for line := range strings.SplitSeq(parentBody, "\n") {
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
	slices.Sort(out)
	return out
}

// Status is one resolved blocker: the issue number and its uppercased GitHub
// state (OPEN / CLOSED / UNKNOWN). JSON tags are part of the dashboard
// snapshot wire contract.
type Status struct {
	Num   int    `json:"num"`
	State string `json:"state"`
}

// Child is the minimal child-issue shape Dependencies scans.
type Child struct {
	Num  int
	Body string
}

// Dependencies resolves each child's blockers: the child-body "## Blocked by"
// numbers unioned with the parent task-list row trailer (only when
// includeParentRows; manual and Project parents have no task-list rows).
// stateOf supplies a blocker's issue state — callers cache it because the same
// blocker can appear under several children. Empty states become UNKNOWN.
func Dependencies(children []Child, includeParentRows bool, parentBody string, stateOf func(int) string) map[int][]Status {
	deps := make(map[int][]Status, len(children))
	for _, child := range children {
		childBlockers := FromChildBody(child.Body)
		parentBlockers := []int{}
		if includeParentRows {
			parentBlockers = FromParentRow(parentBody, child.Num)
		}
		nums := Dedupe(childBlockers, parentBlockers)
		rows := make([]Status, 0, len(nums))
		for _, num := range nums {
			state := stateOf(num)
			if state == "" {
				state = "UNKNOWN"
			}
			rows = append(rows, Status{Num: num, State: strings.ToUpper(state)})
		}
		deps[child.Num] = rows
	}
	return deps
}

// Waves assigns each child a 1-based dependency depth: one more than its
// deepest in-set blocker; blockers outside childNums count as depth 1. The
// visiting map breaks blocker cycles so a cyclic graph cannot recurse forever.
func Waves(childNums []int, deps map[int][]Status) map[int]int {
	childSet := make(map[int]bool, len(childNums))
	for _, num := range childNums {
		childSet[num] = true
	}
	waves := map[int]int{}
	visiting := map[int]bool{}
	var waveFor func(int) int
	waveFor = func(num int) int {
		if wave := waves[num]; wave > 0 {
			return wave
		}
		if visiting[num] {
			return 1
		}
		visiting[num] = true
		maxBlockerWave := 0
		for _, blocker := range deps[num] {
			blockerWave := 1
			if childSet[blocker.Num] {
				blockerWave = waveFor(blocker.Num)
			}
			if blockerWave > maxBlockerWave {
				maxBlockerWave = blockerWave
			}
		}
		delete(visiting, num)
		waves[num] = maxBlockerWave + 1
		return waves[num]
	}
	for _, num := range childNums {
		waveFor(num)
	}
	return waves
}

// FormatStatuses renders blocker rows as "OPEN #12, resolved #14"; "-" when
// there are none.
func FormatStatuses(rows []Status) string {
	if len(rows) == 0 {
		return "-"
	}
	parts := make([]string, len(rows))
	for i, row := range rows {
		switch row.State {
		case "OPEN":
			parts[i] = fmt.Sprintf("OPEN #%d", row.Num)
		case "CLOSED":
			parts[i] = fmt.Sprintf("resolved #%d", row.Num)
		default:
			state := row.State
			if strings.TrimSpace(state) == "" {
				state = "-"
			}
			parts[i] = fmt.Sprintf("%s #%d", state, row.Num)
		}
	}
	return strings.Join(parts, ", ")
}

// HasOpen reports whether any blocker row is still OPEN.
func HasOpen(rows []Status) bool {
	for _, row := range rows {
		if row.State == "OPEN" {
			return true
		}
	}
	return false
}
