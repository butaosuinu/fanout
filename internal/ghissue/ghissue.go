// Package ghissue wraps the `gh` CLI calls fanout makes — sub-issue list,
// issue view (full and field-projected). It also assembles the children union
// (Sub-issues API + parent body task-list scan + --include).
package ghissue

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Label is the slim shape fanout cares about (just the name; we treat the
// presence of "blocked" as a weak signal).
type Label struct {
	Name string `json:"name"`
}

// Issue is the unified row used downstream. The Sub-issues API path leaves
// Body and Labels at zero-value; lazy hydration fills them in.
type Issue struct {
	Number int     `json:"number"`
	Title  string  `json:"title"`
	State  string  `json:"state"`
	Body   string  `json:"body"`
	Labels []Label `json:"labels"`
}

// Runner abstracts `gh` invocation so tests can swap in a fake. The Tier 2
// shim runs the real `gh` binary path through PATH, so the default execRunner
// is sufficient and tests don't actually need to swap.
type Runner struct {
	Cwd string
}

func (r Runner) gh(args ...string) ([]byte, error) {
	cmd := exec.Command("gh", args...)
	if r.Cwd != "" {
		cmd.Dir = r.Cwd
	}
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return out, fmt.Errorf("gh %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return out, err
	}
	return out, nil
}

// SubIssueList runs `gh sub-issue list <parent> --json number,title,state` and
// returns the flattened, state-uppercased issue rows.
func (r Runner) SubIssueList(parent int) ([]Issue, error) {
	out, err := r.gh("sub-issue", "list", strconv.Itoa(parent), "--json", "number,title,state")
	if err != nil {
		return nil, err
	}
	var wrap struct {
		SubIssues []struct {
			Number int    `json:"number"`
			Title  string `json:"title"`
			State  string `json:"state"`
		} `json:"subIssues"`
	}
	if err := json.Unmarshal(out, &wrap); err != nil {
		return nil, fmt.Errorf("parse gh sub-issue list output: %w", err)
	}
	issues := make([]Issue, 0, len(wrap.SubIssues))
	for _, s := range wrap.SubIssues {
		issues = append(issues, Issue{
			Number: s.Number,
			Title:  s.Title,
			State:  strings.ToUpper(s.State),
			Labels: []Label{},
		})
	}
	return issues, nil
}

// ParentBody fetches `gh issue view <parent> --json body -q .body`. Empty
// string + non-error means "couldn't read".
func (r Runner) ParentBody(parent int) (string, error) {
	out, err := r.gh("issue", "view", strconv.Itoa(parent), "--json", "body", "-q", ".body")
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// IssueDetail fetches the full issue JSON used to hydrate body/labels.
func (r Runner) IssueDetail(num int) (Issue, error) {
	out, err := r.gh("issue", "view", strconv.Itoa(num), "--json", "number,title,state,body,labels")
	if err != nil {
		return Issue{}, err
	}
	var d Issue
	if err := json.Unmarshal(out, &d); err != nil {
		return Issue{}, fmt.Errorf("parse gh issue view %d: %w", num, err)
	}
	d.State = strings.ToUpper(d.State)
	if d.Labels == nil {
		d.Labels = []Label{}
	}
	return d, nil
}

// IssueState fetches just `.state` for blocker resolution.
func (r Runner) IssueState(num int) (string, error) {
	out, err := r.gh("issue", "view", strconv.Itoa(num), "--json", "state", "-q", ".state")
	if err != nil {
		return "UNKNOWN", err
	}
	s := strings.ToUpper(strings.TrimSpace(string(out)))
	if s == "" {
		return "UNKNOWN", nil
	}
	return s, nil
}

// HydrateBodyLabels fetches body and labels for an issue and merges them into
// the existing struct (used by --unblocked-only's eager hydration pass).
func (r Runner) HydrateBodyLabels(iss *Issue) error {
	out, err := r.gh("issue", "view", strconv.Itoa(iss.Number), "--json", "body,labels")
	if err != nil {
		return err
	}
	var d struct {
		Body   string  `json:"body"`
		Labels []Label `json:"labels"`
	}
	if err := json.Unmarshal(out, &d); err != nil {
		return fmt.Errorf("parse gh issue view %d body/labels: %w", iss.Number, err)
	}
	iss.Body = d.Body
	if d.Labels != nil {
		iss.Labels = d.Labels
	}
	return nil
}

var taskListRE = regexp.MustCompile(`^\s*-\s+\[[ xX]\]\s*#([0-9]+)`)

// TaskListNumbers extracts issue numbers from each `- [ ] #N ...` row in the
// parent body. Cross-repo refs (`owner/repo#N`) are silently ignored: the
// regex only matches `#N` immediately after the checkbox.
//
// Order is preserved (first appearance wins) and duplicates collapsed, to
// match the bash jq pipeline that runs `unique` before iterating.
func TaskListNumbers(parentBody string) []int {
	seen := map[int]bool{}
	var out []int
	for _, line := range strings.Split(parentBody, "\n") {
		m := taskListRE.FindStringSubmatch(line)
		if len(m) != 2 {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// MergeExtra adds rows from extra to base, deduplicating by .number.
func MergeExtra(base, extra []Issue) []Issue {
	seen := map[int]bool{}
	for _, b := range base {
		seen[b.Number] = true
	}
	for _, e := range extra {
		if !seen[e.Number] {
			base = append(base, e)
			seen[e.Number] = true
		}
	}
	return base
}
