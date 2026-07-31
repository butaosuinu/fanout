// Package ghissue wraps the `gh` CLI calls fanout makes — Sub-issues listing,
// issue view (full and field-projected). It also assembles the children union
// (Sub-issues API + parent body task-list scan + --include).
package ghissue

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/infra/execx"
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
	// ParentNumber and OpenSubIssueCount classify an issue's place in the
	// GitHub Sub-issues graph for the picker. Only the GraphQL ListOpenIssues
	// path sets them; the Sub-issues REST path (SubIssueList) and issue view
	// (IssueDetail) leave them at zero value.
	ParentNumber      int // 0 = no parent
	OpenSubIssueCount int // max(0, subIssuesSummary.total - completed): OPEN children, matching launch's countOpenChildTargets > 0 fan-out test
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

// PrimaryPR picks the ref that best represents an issue's closing PRs: the
// first MERGED ref wins, otherwise the first ref. ok is false when prs is
// empty.
func PrimaryPR(prs []PRRef) (PRRef, bool) {
	if len(prs) == 0 {
		return PRRef{}, false
	}
	for _, pr := range prs {
		if pr.State == "MERGED" {
			return pr, true
		}
	}
	return prs[0], true
}

// SummarizeCI reports the primary PR's normalized CI status ("pass" / "fail" /
// "pending"); "-" when there is no PR or no recorded rollup.
func SummarizeCI(prs []PRRef) string {
	pr, ok := PrimaryPR(prs)
	if !ok || strings.TrimSpace(pr.CIStatus) == "" {
		return "-"
	}
	return pr.CIStatus
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
	Number int
	Title  string
	State  string
	Body   string
	Labels []Label
	PRs    []PRRef
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
	return execx.Output(r.Cwd, nil, "gh", args...)
}

func (r Runner) ghWithInput(input string, args ...string) error {
	_, err := execx.OutputStdin(r.Cwd, input, "gh", args...)
	return err
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

type subIssueItem struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
}

// SubIssueList lists the parent's sub-issues through the official GitHub
// Sub-issues REST API and returns the flattened, state-uppercased issue rows.
// The REST API reports state in lowercase (`open`/`closed`).
func (r Runner) SubIssueList(parent int) ([]Issue, error) {
	endpoint := fmt.Sprintf("repos/{owner}/{repo}/issues/%d/sub_issues?per_page=100", parent)
	out, err := r.gh("api", "--paginate", "--slurp", endpoint)
	if err != nil {
		return nil, err
	}
	pages, err := parsePages[subIssueItem](out)
	if err != nil {
		return nil, fmt.Errorf("parse gh issue %d sub_issues: %w", parent, err)
	}
	total := 0
	for _, page := range pages {
		total += len(page)
	}
	issues := make([]Issue, 0, total)
	for _, page := range pages {
		for _, s := range page {
			issues = append(issues, Issue{
				Number: s.Number,
				Title:  s.Title,
				State:  strings.ToUpper(s.State),
				Labels: []Label{},
			})
		}
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
	normalizeIssue(&d)
	return d, nil
}

// issueDetailsBatchSize bounds the number of aliased issue fields in one
// GraphQL request.
const issueDetailsBatchSize = 50

// IssueDetails fetches full issue rows in aliased GraphQL batches. It omits
// closing PRs so body hydration does not pay for or depend on unused PR data.
func (r Runner) IssueDetails(nums []int) (map[int]Issue, error) {
	snapshots, err := r.issueSnapshots("{owner}", "{repo}", nums, false)
	issues := make(map[int]Issue, len(snapshots))
	for num, snapshot := range snapshots {
		issues[num] = Issue{
			Number: num,
			Title:  snapshot.Title,
			State:  snapshot.State,
			Body:   snapshot.Body,
			Labels: snapshot.Labels,
		}
	}
	return issues, err
}

// ListOpenIssuesWithLabel returns up to 100 OPEN issues that carry the
// requested label. `gh issue list` lists issues, not pull requests; watcher
// label scans rely on that command boundary so same-label PRs do not enter the
// issue work queue.
func (r Runner) ListOpenIssuesWithLabel(label string) ([]Issue, error) {
	out, err := r.gh(
		"issue", "list",
		"--state", "open",
		"--label", label,
		"--limit", "100",
		"--json", "number,title,state,body,labels",
	)
	if err != nil {
		return nil, err
	}
	issues, err := parseIssueList(out)
	if err != nil {
		return nil, fmt.Errorf("parse gh issue list --label %q: %w", label, err)
	}
	return issues, nil
}

// openIssuesPageSize is the per-page node count for the ListOpenIssues cursor
// walk.
const openIssuesPageSize = 100

// ListOpenIssues returns every OPEN issue for interactive pickers, walking
// GraphQL cursor pages so repositories with more than one page of issues stay
// fully reachable. Bodies are omitted: pickers render number/title/labels
// only, and launch paths re-fetch details (IssueDetail) at launch time so a
// stale list entry cannot carry a stale briefing body.
func (r Runner) ListOpenIssues() ([]Issue, error) {
	// orderBy CREATED_AT DESC keeps gh's newest-first ordering, which the
	// picker ranker (rankPickerItems) relies on for stable source order.
	const query = `
query($owner: String!, $name: String!, $first: Int!, $after: String) {
  repository(owner: $owner, name: $name) {
    issues(states: OPEN, first: $first, after: $after, orderBy: {field: CREATED_AT, direction: DESC}) {
      nodes {
        number
        title
        labels(first: 100) { nodes { name } }
        parent { number }
        subIssuesSummary { total completed }
      }
      pageInfo { hasNextPage endCursor }
    }
  }
}
`
	var issues []Issue
	cursor := ""
	for {
		args := []string{
			"api", "graphql",
			"-f", "query=" + query,
			"-F", "owner={owner}",
			"-F", "name={repo}",
			"-F", "first=" + strconv.Itoa(openIssuesPageSize),
		}
		if cursor != "" {
			args = append(args, "-F", "after="+cursor)
		}
		out, err := r.gh(args...)
		if err != nil {
			return nil, fmt.Errorf("gh api graphql (open issues) failed: %w", err)
		}
		page, err := parseOpenIssuesPage(out)
		if err != nil {
			return nil, err
		}
		issues = append(issues, page.Issues...)
		if !page.PageInfo.HasNextPage {
			break
		}
		next := page.PageInfo.EndCursor
		if next == "" || next == cursor {
			break
		}
		cursor = next
	}
	if issues == nil {
		return []Issue{}, nil
	}
	return issues, nil
}

// openIssuesPage is one GraphQL page of the ListOpenIssues cursor walk. It is a
// dedicated local type (not reusing the Project path's shapes) so later PRs can
// extend the issue node fields here without touching ProjectItems.
type openIssuesPage struct {
	Issues   []Issue
	PageInfo pageInfo
}

func parseOpenIssuesPage(out []byte) (openIssuesPage, error) {
	var root struct {
		Data struct {
			Repository *struct {
				Issues struct {
					Nodes []struct {
						Number int    `json:"number"`
						Title  string `json:"title"`
						Labels struct {
							Nodes []Label `json:"nodes"`
						} `json:"labels"`
						Parent *struct {
							Number int `json:"number"`
						} `json:"parent"`
						SubIssuesSummary struct {
							Total     int `json:"total"`
							Completed int `json:"completed"`
						} `json:"subIssuesSummary"`
					} `json:"nodes"`
					PageInfo pageInfo `json:"pageInfo"`
				} `json:"issues"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &root); err != nil {
		return openIssuesPage{}, fmt.Errorf("parse gh api graphql (open issues): %w", err)
	}
	if root.Data.Repository == nil {
		return openIssuesPage{}, fmt.Errorf("gh api graphql (open issues): repository not found in response")
	}
	nodes := root.Data.Repository.Issues.Nodes
	issues := make([]Issue, 0, len(nodes))
	for _, n := range nodes {
		labels := make([]Label, 0, len(n.Labels.Nodes))
		for _, l := range n.Labels.Nodes {
			labels = append(labels, Label{Name: l.Name})
		}
		parentNumber := 0
		if n.Parent != nil {
			parentNumber = n.Parent.Number
		}
		issues = append(issues, Issue{
			Number:            n.Number,
			Title:             n.Title,
			State:             "OPEN",
			Labels:            labels,
			ParentNumber:      parentNumber,
			OpenSubIssueCount: max(0, n.SubIssuesSummary.Total-n.SubIssuesSummary.Completed),
		})
	}
	return openIssuesPage{Issues: issues, PageInfo: root.Data.Repository.Issues.PageInfo}, nil
}

// SwapIssueLabels moves one issue from remove to add with one `gh issue edit`
// call so GitHub observes a single label mutation round.
func (r Runner) SwapIssueLabels(num int, remove, add string) error {
	_, err := r.gh("issue", "edit", strconv.Itoa(num), "--remove-label", remove, "--add-label", add)
	return err
}

// RemoveIssueLabel removes label from one issue.
func (r Runner) RemoveIssueLabel(num int, label string) error {
	_, err := r.gh("issue", "edit", strconv.Itoa(num), "--remove-label", label)
	return err
}

// EnsureLabel creates name when it is absent from the repository labels.
func (r Runner) EnsureLabel(name string) error {
	out, err := r.gh("label", "list", "--search", name, "--limit", "100", "--json", "name")
	if err != nil {
		return err
	}
	var labels []Label
	if strings.TrimSpace(string(out)) != "" {
		if unmarshalErr := json.Unmarshal(out, &labels); unmarshalErr != nil {
			return fmt.Errorf("parse gh label list --search %q: %w", name, unmarshalErr)
		}
	}
	for _, label := range labels {
		if strings.EqualFold(strings.TrimSpace(label.Name), strings.TrimSpace(name)) {
			return nil
		}
	}
	_, err = r.gh("label", "create", name)
	return err
}

func parseIssueList(out []byte) ([]Issue, error) {
	var issues []Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, err
	}
	if issues == nil {
		return []Issue{}, nil
	}
	for i := range issues {
		normalizeIssue(&issues[i])
	}
	return issues, nil
}

func normalizeIssue(issue *Issue) {
	issue.State = strings.ToUpper(issue.State)
	if issue.Labels == nil {
		issue.Labels = []Label{}
	}
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

// IssuesSnapshotWithPRs fetches issue details and the first 100 closed-by PR
// refs in aliased GraphQL batches. Only issues whose PR connection has another
// page fall back to IssueSnapshotWithPRs so the complete PR list is preserved.
// Per-issue and per-chunk failures are joined while successful sibling results
// remain available.
func (r Runner) IssuesSnapshotWithPRs(owner, repo string, nums []int) (map[int]IssueSnapshot, error) {
	return r.issueSnapshots(owner, repo, nums, true)
}

func (r Runner) issueSnapshots(owner, repo string, nums []int, withPRs bool) (map[int]IssueSnapshot, error) {
	unique, inputErr := uniquePositiveIssueNumbers(nums)
	snapshots := make(map[int]IssueSnapshot, len(unique))
	loadErr := inputErr
	for start := 0; start < len(unique); start += issueDetailsBatchSize {
		end := min(start+issueDetailsBatchSize, len(unique))
		chunk := unique[start:end]
		out, err := r.gh(
			"api", "graphql",
			"-F", "owner="+owner,
			"-F", "repo="+repo,
			"-f", "query="+issueDetailsQuery(chunk, withPRs),
		)
		if err != nil {
			for _, num := range chunk {
				loadErr = errors.Join(loadErr, fmt.Errorf("#%d: gh api graphql issue batch: %w", num, err))
			}
			continue
		}

		parsed, fallback, err := parseIssueDetailsBatch(out, chunk)
		loadErr = errors.Join(loadErr, err)
		maps.Copy(snapshots, parsed)
		for _, num := range fallback {
			snapshot, err := r.IssueSnapshotWithPRs(owner, repo, num)
			if err != nil {
				loadErr = errors.Join(loadErr, fmt.Errorf("#%d: page closed-by PRs: %w", num, err))
				continue
			}
			// IssueSnapshotWithPRs owns the complete PR list but its legacy
			// query omits title and labels. Preserve those fields from the
			// successful alias response.
			snapshot.Number = parsed[num].Number
			snapshot.Title = parsed[num].Title
			snapshot.Labels = parsed[num].Labels
			snapshots[num] = snapshot
		}
	}
	return snapshots, loadErr
}

func uniquePositiveIssueNumbers(nums []int) ([]int, error) {
	unique := make([]int, 0, len(nums))
	seen := make(map[int]bool, len(nums))
	var inputErr error
	for _, num := range nums {
		if num <= 0 {
			inputErr = errors.Join(inputErr, fmt.Errorf("#%d: issue number must be positive", num))
			continue
		}
		if seen[num] {
			continue
		}
		seen[num] = true
		unique = append(unique, num)
	}
	return unique, inputErr
}

func issueDetailsQuery(nums []int, withPRs bool) string {
	issueFields := ` {
      number
      title
      state
      body
      labels(first: 100) { nodes { name } }`
	if withPRs {
		issueFields += `
      closedByPullRequestsReferences(first: 100) {
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
                statusCheckRollup { state }
              }
            }
          }
        }
      }`
	}
	issueFields += `
    }`
	fields := make([]string, 0, len(nums))
	for _, num := range nums {
		number := strconv.Itoa(num)
		fields = append(fields, "    issue_"+number+": issue(number: "+number+")"+issueFields)
	}
	return `query($owner: String!, $repo: String!) {
  repository(owner: $owner, name: $repo) {
` + strings.Join(fields, "\n") + `
  }
}`
}

type issueDetailsBatchNode struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Body   string `json:"body"`
	Labels struct {
		Nodes []Label `json:"nodes"`
	} `json:"labels"`
	Refs struct {
		PageInfo struct {
			HasNextPage bool `json:"hasNextPage"`
		} `json:"pageInfo"`
		Nodes []prRefGraphQL `json:"nodes"`
	} `json:"closedByPullRequestsReferences"`
}

type issueDetailsGraphQLError struct {
	Message string            `json:"message"`
	Path    []json.RawMessage `json:"path"`
}

func parseIssueDetailsBatch(out []byte, nums []int) (map[int]IssueSnapshot, []int, error) {
	var root struct {
		Data struct {
			Repository map[string]*issueDetailsBatchNode `json:"repository"`
		} `json:"data"`
		Errors []issueDetailsGraphQLError `json:"errors"`
	}
	if err := json.Unmarshal(out, &root); err != nil {
		return nil, nil, fmt.Errorf("parse gh api graphql issue batch: %w", err)
	}

	aliasErrors := map[string][]string{}
	var loadErr error
	for _, graphErr := range root.Errors {
		alias := issueAliasFromPath(graphErr.Path)
		if alias == "" {
			loadErr = errors.Join(loadErr, fmt.Errorf("gh api graphql issue batch: %s", graphErr.Message))
			continue
		}
		aliasErrors[alias] = append(aliasErrors[alias], graphErr.Message)
	}

	snapshots := make(map[int]IssueSnapshot, len(nums))
	fallback := []int{}
	for _, num := range nums {
		alias := "issue_" + strconv.Itoa(num)
		node := root.Data.Repository[alias]
		if node == nil {
			messages := aliasErrors[alias]
			if len(messages) == 0 {
				loadErr = errors.Join(loadErr, fmt.Errorf("#%d: issue not found in batch response", num))
			}
			for _, message := range messages {
				loadErr = errors.Join(loadErr, fmt.Errorf("#%d: graphql: %s", num, message))
			}
			continue
		}

		labels := append([]Label(nil), node.Labels.Nodes...)
		if labels == nil {
			labels = []Label{}
		}
		prs := make([]PRRef, 0, len(node.Refs.Nodes))
		for _, pr := range node.Refs.Nodes {
			prs = append(prs, pr.ref())
		}
		snapshots[num] = IssueSnapshot{
			Number: num,
			Title:  node.Title,
			State:  strings.ToUpper(node.State),
			Body:   node.Body,
			Labels: labels,
			PRs:    prs,
		}
		if node.Refs.PageInfo.HasNextPage {
			fallback = append(fallback, num)
		}
		for _, message := range aliasErrors[alias] {
			loadErr = errors.Join(loadErr, fmt.Errorf("#%d: graphql: %s", num, message))
		}
	}
	return snapshots, fallback, loadErr
}

func issueAliasFromPath(path []json.RawMessage) string {
	for _, raw := range path {
		var field string
		if err := json.Unmarshal(raw, &field); err == nil && strings.HasPrefix(field, "issue_") {
			return field
		}
	}
	return ""
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
	snapshot := IssueSnapshot{Number: num, PRs: []PRRef{}}
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

func (r Runner) PRsForBranch(branch string) ([]PRRef, error) {
	out, err := r.gh(
		"pr", "list",
		"--head", branch,
		"--state", "all",
		"--json", "number,state,mergedAt,isDraft,reviewDecision,statusCheckRollup",
	)
	if err != nil {
		return nil, err
	}
	var refs []prListItem
	if err := json.Unmarshal(out, &refs); err != nil {
		return nil, fmt.Errorf("parse gh pr list --head %q: %w", branch, err)
	}
	prs := make([]PRRef, 0, len(refs))
	for _, pr := range refs {
		prs = append(prs, pr.ref())
	}
	return prs, nil
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

type prListItem struct {
	Number            int             `json:"number"`
	State             string          `json:"state"`
	MergedAt          *string         `json:"mergedAt"`
	IsDraft           bool            `json:"isDraft"`
	ReviewDecision    string          `json:"reviewDecision"`
	StatusCheckRollup json.RawMessage `json:"statusCheckRollup"`
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

func (pr prListItem) ref() PRRef {
	return PRRef{
		Number:         pr.Number,
		State:          pr.State,
		MergedAt:       pr.MergedAt,
		IsDraft:        pr.IsDraft,
		ReviewDecision: pr.ReviewDecision,
		CIStatus:       ciStatusFromStatusCheckRollup(pr.StatusCheckRollup),
	}
}

func (pr prRefGraphQL) statusCheckRollupState() string {
	if len(pr.Commits.Nodes) == 0 || pr.Commits.Nodes[0].Commit.StatusCheckRollup == nil {
		return ""
	}
	return pr.Commits.Nodes[0].Commit.StatusCheckRollup.State
}

func ciStatusFromStatusCheckRollup(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}

	var rollup struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(raw, &rollup); err == nil && strings.TrimSpace(rollup.State) != "" {
		return normalizeCIStatus(rollup.State)
	}

	var checks []struct {
		State      string `json:"state"`
		Conclusion string `json:"conclusion"`
		Status     string `json:"status"`
	}
	if err := json.Unmarshal(raw, &checks); err != nil {
		return ""
	}

	sawPass := false
	sawPending := false
	for _, check := range checks {
		status := strings.ToUpper(strings.TrimSpace(check.Status))
		if status != "" && status != "COMPLETED" {
			sawPending = true
			continue
		}
		state := check.State
		if strings.TrimSpace(state) == "" {
			state = check.Conclusion
		}
		switch normalizeCIStatus(state) {
		case "fail":
			return "fail"
		case "pending":
			sawPending = true
		case "pass":
			sawPass = true
		}
	}
	if sawPending {
		return "pending"
	}
	if sawPass {
		return "pass"
	}
	return ""
}

func normalizeCIStatus(state string) string {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "":
		return ""
	case "SUCCESS":
		return "pass"
	case "ACTION_REQUIRED", "CANCELLED", "CANCELED", "ERROR", "FAILURE", "STARTUP_FAILURE", "TIMED_OUT": //nolint:misspell // GitHub's CheckConclusionState enum spells this CANCELLED.
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

	pages, err := parsePages[issueCommentPageItem](out)
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

// parsePages decodes `gh api --paginate --slurp` output: an array of pages,
// each page an array of items. Plain single-array output (no slurp wrapper)
// is accepted as one page.
func parsePages[T any](out []byte) ([][]T, error) {
	var pages [][]T
	if err := json.Unmarshal(out, &pages); err == nil {
		return pages, nil
	}

	var single []T
	if err := json.Unmarshal(out, &single); err != nil {
		return nil, err
	}
	return [][]T{single}, nil
}

func (r Runner) PostIssueComment(parent int, body string) error {
	return r.ghWithInput(body, "issue", "comment", strconv.Itoa(parent), "--body-file", "-")
}

func (r Runner) EditIssueComment(owner, repo, id, body string) error {
	path := fmt.Sprintf("/repos/%s/%s/issues/comments/%s", owner, repo, id)
	return r.ghWithInput(body, "api", "-X", "PATCH", path, "-F", "body=@-")
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
				return result, fmt.Errorf("%w: %s (check the URL, token scopes, and project visibility)", err, projectURL)
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
var errProjectNotFound = errors.New("Project not found or not accessible") //nolint:staticcheck // ST1005: "Project" (GitHub Projects) is a proper noun, and the text must match the shell's die message

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
