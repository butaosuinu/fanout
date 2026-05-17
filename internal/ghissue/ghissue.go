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

type PRRef struct {
	Number   int     `json:"number"`
	State    string  `json:"state"`
	MergedAt *string `json:"mergedAt"`
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

// RepoNameWithOwner runs `gh repo view --json nameWithOwner -q .nameWithOwner`.
func (r Runner) RepoNameWithOwner() (string, error) {
	out, err := r.gh("repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
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

// IssueWithPRs returns an issue's state plus all closed-by PR refs, following
// closedByPullRequestsReferences pagination.
func (r Runner) IssueWithPRs(owner, repo string, num int) (state string, prs []PRRef, err error) {
	const query = `
    query($owner: String!, $repo: String!, $num: Int!, $after: String) {
      repository(owner: $owner, name: $repo) {
        issue(number: $num) {
          state
          closedByPullRequestsReferences(first: 100, after: $after) {
            pageInfo { hasNextPage endCursor }
            nodes { number state mergedAt }
          }
        }
      }
    }
  `
	prs = []PRRef{}
	cursor := ""
	for {
		args := []string{
			"api", "graphql",
			"-F", "owner=" + owner,
			"-F", "repo=" + repo,
			"-F", "num=" + strconv.Itoa(num),
			"-f", "query=" + query,
			"--jq", ".data.repository.issue // empty",
		}
		if cursor != "" {
			args = append(args, "-F", "after="+cursor)
		}
		out, err := r.gh(args...)
		if err != nil {
			return "", nil, err
		}
		if len(strings.TrimSpace(string(out))) == 0 {
			return "", nil, fmt.Errorf("empty issue response")
		}
		var page struct {
			State string `json:"state"`
			Refs  struct {
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []PRRef `json:"nodes"`
			} `json:"closedByPullRequestsReferences"`
		}
		if err := json.Unmarshal(out, &page); err != nil {
			return "", nil, fmt.Errorf("parse gh api graphql issue %d: %w", num, err)
		}
		state = page.State
		prs = append(prs, page.Refs.Nodes...)
		if !page.Refs.PageInfo.HasNextPage {
			break
		}
		next := page.Refs.PageInfo.EndCursor
		if next == "" || next == cursor {
			break
		}
		cursor = next
	}
	return state, prs, nil
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

type ProjectItemsResult struct {
	Issues            []Issue
	CrossRepoWarnings []string
	ProjectTitle      string
	MissingStatus     bool
}

// ProjectItems lists issue items from a GitHub Projects v2 board, filters to
// the current repo, and applies the single-select Status filter unless status
// is "all".
func (r Runner) ProjectItems(ownerType, owner string, number int, repo, status string) (ProjectItemsResult, error) {
	entryField := "user"
	if ownerType == "orgs" {
		entryField = "organization"
	}
	const query = `
query($owner: String!, $number: Int!, $first: Int!, $after: String) {
  ENTRY(login: $owner) {
    projectV2(number: $number) {
      title
      url
      field(name: "Status") { __typename }
      items(first: $first, after: $after) {
        nodes {
          content {
            __typename
            ... on Issue {
              number
              title
              state
              body
              repository { nameWithOwner }
              labels(first: 20) { nodes { name } }
            }
          }
          status: fieldValueByName(name: "Status") {
            ... on ProjectV2ItemFieldSingleSelectValue { name }
          }
        }
        pageInfo { hasNextPage endCursor }
      }
    }
  }
}
`
	gql := strings.ReplaceAll(query, "ENTRY", entryField)
	var result ProjectItemsResult
	cursor := ""
	for {
		args := []string{
			"api", "graphql",
			"-f", "query=" + gql,
			"-F", "owner=" + owner,
			"-F", "number=" + strconv.Itoa(number),
			"-F", "first=100",
		}
		if cursor != "" {
			args = append(args, "-F", "after="+cursor)
		}
		out, err := r.gh(args...)
		if err != nil {
			return result, fmt.Errorf("gh api graphql (projectV2 items) failed: %w", err)
		}
		page, err := parseProjectPage(out, entryField)
		if err != nil {
			return result, err
		}
		if result.ProjectTitle == "" {
			result.ProjectTitle = page.Title
		}
		effectiveStatus := status
		if !page.HasStatusField {
			result.MissingStatus = true
			effectiveStatus = "all"
		}
		for _, item := range page.Items {
			if item.Content.TypeName != "Issue" {
				continue
			}
			itemRepo := item.Content.Repository.NameWithOwner
			if itemRepo != repo {
				result.CrossRepoWarnings = append(result.CrossRepoWarnings, fmt.Sprintf("%s#%d", itemRepo, item.Content.Number))
				continue
			}
			if effectiveStatus != "all" && item.Status.Name != effectiveStatus {
				continue
			}
			labels := make([]Label, 0, len(item.Content.Labels.Nodes))
			for _, l := range item.Content.Labels.Nodes {
				labels = append(labels, Label{Name: l.Name})
			}
			result.Issues = append(result.Issues, Issue{
				Number: item.Content.Number,
				Title:  item.Content.Title,
				State:  strings.ToUpper(item.Content.State),
				Body:   item.Content.Body,
				Labels: labels,
			})
		}
		if !page.PageInfo.HasNextPage {
			break
		}
		next := page.PageInfo.EndCursor
		if next == "" || next == cursor {
			break
		}
		cursor = next
	}
	return result, nil
}

type projectPage struct {
	Title          string
	HasStatusField bool
	Items          []projectItem
	PageInfo       pageInfo
}

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type projectItem struct {
	Content struct {
		TypeName   string `json:"__typename"`
		Number     int    `json:"number"`
		Title      string `json:"title"`
		State      string `json:"state"`
		Body       string `json:"body"`
		Repository struct {
			NameWithOwner string `json:"nameWithOwner"`
		} `json:"repository"`
		Labels struct {
			Nodes []Label `json:"nodes"`
		} `json:"labels"`
	} `json:"content"`
	Status struct {
		Name string `json:"name"`
	} `json:"status"`
}

func parseProjectPage(raw []byte, entryField string) (projectPage, error) {
	var root struct {
		Data map[string]struct {
			ProjectV2 *struct {
				Title string           `json:"title"`
				Field *json.RawMessage `json:"field"`
				Items struct {
					Nodes    []projectItem `json:"nodes"`
					PageInfo pageInfo      `json:"pageInfo"`
				} `json:"items"`
			} `json:"projectV2"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return projectPage{}, fmt.Errorf("parse gh api graphql project items: %w", err)
	}
	entry, ok := root.Data[entryField]
	if !ok || entry.ProjectV2 == nil {
		return projectPage{}, fmt.Errorf("Project not found or not accessible")
	}
	return projectPage{
		Title:          entry.ProjectV2.Title,
		HasStatusField: entry.ProjectV2.Field != nil,
		Items:          entry.ProjectV2.Items.Nodes,
		PageInfo:       entry.ProjectV2.Items.PageInfo,
	}, nil
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
