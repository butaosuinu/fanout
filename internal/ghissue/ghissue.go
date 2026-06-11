// Package ghissue wraps the `gh` CLI calls fanout makes — sub-issue list,
// issue view (full and field-projected). It also assembles the children union
// (Sub-issues API + parent body task-list scan + --include).
package ghissue

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
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
	Wave   string  `json:"wave,omitempty"`
	Labels []Label `json:"labels"`
}

type PRRef struct {
	Number         int     `json:"number"`
	State          string  `json:"state"`
	MergedAt       *string `json:"mergedAt"`
	IsDraft        bool    `json:"isDraft,omitempty"`
	ReviewDecision string  `json:"reviewDecision,omitempty"`
	CIStatus       string  `json:"ci,omitempty"`
}

func (pr PRRef) DisplayState() string {
	state := strings.ToUpper(strings.TrimSpace(pr.State))
	if state == "MERGED" || pr.MergedAt != nil {
		return "merged"
	}
	if state == "CLOSED" {
		return "closed"
	}
	if pr.IsDraft {
		return "draft"
	}
	switch strings.ToUpper(strings.TrimSpace(pr.ReviewDecision)) {
	case "APPROVED":
		return "approved"
	case "CHANGES_REQUESTED":
		return "changes-requested"
	case "REVIEW_REQUIRED":
		return "review-required"
	}
	if state == "" {
		return ""
	}
	return strings.ToLower(state)
}

type PRDiffStat struct {
	Number       int    `json:"number"`
	Additions    int    `json:"additions"`
	Deletions    int    `json:"deletions"`
	ChangedFiles int    `json:"changedFiles"`
	Title        string `json:"title"`
	Body         string `json:"body"`
}

type IssueSnapshot struct {
	State string
	Body  string
	PRs   []PRRef
}

type IssueComment struct {
	ID   string
	Body string
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

func (r Runner) ghWithInput(input string, args ...string) ([]byte, error) {
	cmd := exec.Command("gh", args...)
	if r.Cwd != "" {
		cmd.Dir = r.Cwd
	}
	cmd.Stdin = strings.NewReader(input)
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

// LatestReleaseTag returns the latest fanout release tag.
func (r Runner) LatestReleaseTag() (string, error) {
	out, err := r.gh("release", "view", "-R", "butaosuinu/fanout", "--json", "tagName", "-q", ".tagName")
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

// IssueSnapshotWithPRs returns an issue's state/body plus all closed-by
// PR refs, following closedByPullRequestsReferences pagination.
func (r Runner) IssueSnapshotWithPRs(owner, repo string, num int) (IssueSnapshot, error) {
	const query = `
    query($owner: String!, $repo: String!, $num: Int!, $after: String) {
      repository(owner: $owner, name: $repo) {
        issue(number: $num) {
          state
          body
          closedByPullRequestsReferences(first: 100, after: $after) {
            pageInfo { hasNextPage endCursor }
            nodes {
              number
              state
              mergedAt
              isDraft
              reviewDecision
              commits(last: 1) {
                nodes {
                  commit {
                    statusCheckRollup {
                      state
                    }
                  }
                }
              }
            }
          }
        }
      }
    }
  `
	snapshot := IssueSnapshot{PRs: []PRRef{}}
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
			return IssueSnapshot{}, err
		}
		if len(strings.TrimSpace(string(out))) == 0 {
			return IssueSnapshot{}, fmt.Errorf("empty issue response")
		}
		var page struct {
			State string `json:"state"`
			Body  string `json:"body"`
			Refs  struct {
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []prRefGraphQL `json:"nodes"`
			} `json:"closedByPullRequestsReferences"`
		}
		if err := json.Unmarshal(out, &page); err != nil {
			return IssueSnapshot{}, fmt.Errorf("parse gh api graphql issue %d: %w", num, err)
		}
		snapshot.State = page.State
		snapshot.Body = page.Body
		for _, pr := range page.Refs.Nodes {
			snapshot.PRs = append(snapshot.PRs, pr.ref())
		}
		if !page.Refs.PageInfo.HasNextPage {
			break
		}
		next := page.Refs.PageInfo.EndCursor
		if next == "" || next == cursor {
			break
		}
		cursor = next
	}
	return snapshot, nil
}

// IssueWithPRs returns an issue's state plus all closed-by PR refs, following
// closedByPullRequestsReferences pagination.
func (r Runner) IssueWithPRs(owner, repo string, num int) (state string, prs []PRRef, err error) {
	snapshot, err := r.IssueSnapshotWithPRs(owner, repo, num)
	if err != nil {
		return "", nil, err
	}
	return snapshot.State, snapshot.PRs, nil
}

type prRefGraphQL struct {
	Number         int     `json:"number"`
	State          string  `json:"state"`
	MergedAt       *string `json:"mergedAt"`
	IsDraft        bool    `json:"isDraft"`
	ReviewDecision string  `json:"reviewDecision"`
	Commits        struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					State string `json:"state"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

func (pr prRefGraphQL) ref() PRRef {
	return PRRef{
		Number:         pr.Number,
		State:          pr.State,
		MergedAt:       pr.MergedAt,
		IsDraft:        pr.IsDraft,
		ReviewDecision: pr.ReviewDecision,
		CIStatus:       normalizeCIStatus(pr.statusCheckRollupState()),
	}
}

func (pr prRefGraphQL) statusCheckRollupState() string {
	if len(pr.Commits.Nodes) == 0 || pr.Commits.Nodes[0].Commit.StatusCheckRollup == nil {
		return ""
	}
	return pr.Commits.Nodes[0].Commit.StatusCheckRollup.State
}

func normalizeCIStatus(state string) string {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "":
		return ""
	case "SUCCESS":
		return "pass"
	case "ERROR", "FAILURE":
		return "fail"
	case "EXPECTED", "PENDING":
		return "pending"
	default:
		return strings.ToLower(strings.TrimSpace(state))
	}
}

func (r Runner) PRDiffStat(num int) (PRDiffStat, error) {
	out, err := r.gh("pr", "view", strconv.Itoa(num), "--json", "number,additions,deletions,changedFiles,title,body")
	if err != nil {
		return PRDiffStat{}, err
	}
	var stat PRDiffStat
	if err := json.Unmarshal(out, &stat); err != nil {
		return PRDiffStat{}, fmt.Errorf("parse gh pr view %d: %w", num, err)
	}
	return stat, nil
}

func (r Runner) FindDashboardComment(parent int, marker string) (IssueComment, bool, error) {
	endpoint := fmt.Sprintf("repos/{owner}/{repo}/issues/%d/comments?per_page=100", parent)
	out, err := r.gh("api", "--paginate", "--slurp", endpoint)
	if err != nil {
		return IssueComment{}, false, err
	}

	pages, err := parseIssueCommentPages(out)
	if err != nil {
		return IssueComment{}, false, fmt.Errorf("parse gh issue %d comments: %w", parent, err)
	}
	for _, page := range pages {
		for _, comment := range page {
			if !strings.HasPrefix(comment.Body, marker) {
				continue
			}
			id := commentID(comment.ID, comment.DatabaseID, comment.URL)
			if id == "" {
				return IssueComment{}, false, fmt.Errorf("dashboard comment for #%d has no REST comment id", parent)
			}
			return IssueComment{ID: id, Body: comment.Body}, true, nil
		}
	}
	return IssueComment{}, false, nil
}

type issueCommentPageItem struct {
	ID         json.RawMessage `json:"id"`
	DatabaseID int             `json:"databaseId"`
	URL        string          `json:"url"`
	Body       string          `json:"body"`
}

func parseIssueCommentPages(out []byte) ([][]issueCommentPageItem, error) {
	var pages [][]issueCommentPageItem
	if err := json.Unmarshal(out, &pages); err == nil {
		return pages, nil
	}

	var single []issueCommentPageItem
	if err := json.Unmarshal(out, &single); err != nil {
		return nil, err
	}
	return [][]issueCommentPageItem{single}, nil
}

func (r Runner) PostIssueComment(parent int, body string) error {
	_, err := r.ghWithInput(body, "issue", "comment", strconv.Itoa(parent), "--body-file", "-")
	return err
}

func (r Runner) EditIssueComment(owner, repo, id, body string) error {
	path := fmt.Sprintf("/repos/%s/%s/issues/comments/%s", owner, repo, id)
	_, err := r.ghWithInput(body, "api", "-X", "PATCH", path, "-F", "body=@-")
	return err
}

func commentID(raw json.RawMessage, databaseID int, url string) string {
	if databaseID > 0 {
		return strconv.Itoa(databaseID)
	}
	var numeric int64
	if err := json.Unmarshal(raw, &numeric); err == nil && numeric > 0 {
		return strconv.FormatInt(numeric, 10)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if rePositiveCommentID.MatchString(s) {
			return s
		}
	}
	if i := strings.LastIndex(url, "issuecomment-"); i >= 0 {
		candidate := url[i+len("issuecomment-"):]
		if rePositiveCommentID.MatchString(candidate) {
			return candidate
		}
	}
	return ""
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
			if errors.Is(err, errProjectNotFound) {
				// Mirror the shell's actionable message, including the
				// canonical project URL so the user can check it.
				projectURL := fmt.Sprintf("https://github.com/%s/%s/projects/%d", ownerType, owner, number)
				return result, fmt.Errorf("%s: %s (check the URL, token scopes, and project visibility)", err, projectURL)
			}
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

// errProjectNotFound is returned when GraphQL yields projectV2: null for a
// syntactically valid URL whose project number is wrong or whose token can't
// see it. ProjectItems wraps it with the project URL + actionable guidance to
// match the shell's `die "Project not found or not accessible: $parent (...)"`.
var errProjectNotFound = errors.New("Project not found or not accessible")

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
		return projectPage{}, errProjectNotFound
	}
	return projectPage{
		Title:          entry.ProjectV2.Title,
		HasStatusField: entry.ProjectV2.Field != nil,
		Items:          entry.ProjectV2.Items.Nodes,
		PageInfo:       entry.ProjectV2.Items.PageInfo,
	}, nil
}

var (
	rePositiveCommentID = regexp.MustCompile(`^[1-9][0-9]*$`)
	taskListRE          = regexp.MustCompile(`^\s*-\s+\[[ xX]\]\s*#([0-9]+)`)
	waveHeadingRE       = regexp.MustCompile(`(?i)^wave\s*([0-9]+)`)
)

// TaskListNumbers extracts issue numbers from each `- [ ] #N ...` row in the
// parent body. Cross-repo refs (`owner/repo#N`) are silently ignored: the
// regex only matches `#N` immediately after the checkbox.
//
// Order is preserved (first appearance wins) and duplicates collapsed, to
// match the bash jq pipeline that runs `unique` before iterating.
func TaskListNumbers(parentBody string) []int {
	seen := map[int]bool{}
	var out []int
	for line := range strings.SplitSeq(parentBody, "\n") {
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
	slices.Sort(out)
	return out
}

// TaskListWaves extracts wave labels from parent-body sections such as
// `**wave5（...）**` and assigns them to following `- [ ] #N ...` rows.
func TaskListWaves(parentBody string) map[int]string {
	out := map[int]string{}
	currentWave := ""
	for line := range strings.SplitSeq(parentBody, "\n") {
		if m := taskListRE.FindStringSubmatch(line); len(m) == 2 {
			if currentWave == "" {
				continue
			}
			n, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			if _, exists := out[n]; !exists {
				out[n] = currentWave
			}
			continue
		}
		if wave := parseWaveHeading(line); wave != "" {
			currentWave = wave
		}
	}
	return out
}

func parseWaveHeading(line string) string {
	cleaned := strings.TrimSpace(line)
	cleaned = strings.TrimLeft(cleaned, "#* _")
	cleaned = strings.TrimSpace(cleaned)
	m := waveHeadingRE.FindStringSubmatch(cleaned)
	if len(m) != 2 {
		return ""
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return ""
	}
	return fmt.Sprintf("wave%d", n)
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
