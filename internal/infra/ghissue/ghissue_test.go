package ghissue

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestPRRefDisplayState(t *testing.T) {
	mergedAt := "2026-06-09T00:00:00Z"
	for _, tc := range []struct {
		name string
		pr   PRRef
		want string
	}{
		{name: "merged state wins", pr: PRRef{State: "MERGED", IsDraft: true, ReviewDecision: "CHANGES_REQUESTED"}, want: "merged"},
		{name: "merged timestamp wins", pr: PRRef{State: "CLOSED", MergedAt: &mergedAt}, want: "merged"},
		{name: "closed", pr: PRRef{State: "CLOSED"}, want: "closed"},
		{name: "draft", pr: PRRef{State: "OPEN", IsDraft: true, ReviewDecision: "APPROVED"}, want: "draft"},
		{name: "approved", pr: PRRef{State: "OPEN", ReviewDecision: "APPROVED"}, want: "approved"},
		{name: "changes requested", pr: PRRef{State: "OPEN", ReviewDecision: "CHANGES_REQUESTED"}, want: "changes-requested"},
		{name: "review required", pr: PRRef{State: "OPEN", ReviewDecision: "REVIEW_REQUIRED"}, want: "review-required"},
		{name: "open", pr: PRRef{State: "OPEN"}, want: "open"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.pr.DisplayState(); got != tc.want {
				t.Fatalf("DisplayState() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPrimaryPR(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prs     []PRRef
		wantNum int
		wantOK  bool
	}{
		{name: "empty", prs: nil, wantNum: 0, wantOK: false},
		{name: "first when none merged", prs: []PRRef{{Number: 1, State: "OPEN"}, {Number: 2, State: "CLOSED"}}, wantNum: 1, wantOK: true},
		{name: "merged wins over earlier refs", prs: []PRRef{{Number: 1, State: "OPEN"}, {Number: 2, State: "MERGED"}}, wantNum: 2, wantOK: true},
		{name: "first merged wins", prs: []PRRef{{Number: 1, State: "MERGED"}, {Number: 2, State: "MERGED"}}, wantNum: 1, wantOK: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pr, ok := PrimaryPR(tc.prs)
			if pr.Number != tc.wantNum || ok != tc.wantOK {
				t.Fatalf("PrimaryPR() = #%d, %v, want #%d, %v", pr.Number, ok, tc.wantNum, tc.wantOK)
			}
		})
	}
}

func TestSummarizeCI(t *testing.T) {
	for _, tc := range []struct {
		name string
		prs  []PRRef
		want string
	}{
		{name: "no prs", prs: nil, want: "-"},
		{name: "no rollup", prs: []PRRef{{Number: 1, State: "OPEN"}}, want: "-"},
		{name: "primary status", prs: []PRRef{{Number: 1, State: "OPEN", CIStatus: "fail"}}, want: "fail"},
		{
			name: "merged pr selected over first",
			prs:  []PRRef{{Number: 1, State: "OPEN", CIStatus: "fail"}, {Number: 2, State: "MERGED", CIStatus: "pass"}},
			want: "pass",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SummarizeCI(tc.prs); got != tc.want {
				t.Fatalf("SummarizeCI(%#v) = %q, want %q", tc.prs, got, tc.want)
			}
		})
	}
}

func TestParseIssueListFixture(t *testing.T) {
	fixture := `[
  {
    "number": 221,
    "title": "label watcher API",
    "state": "open",
    "body": "body",
    "labels": [{"name": "fanout:queued"}]
  },
  {
    "number": 222,
    "title": "missing labels normalizes empty",
    "state": "OPEN",
    "body": "",
    "labels": null
  }
]`

	got, err := parseIssueList([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}

	want := []Issue{
		{Number: 221, Title: "label watcher API", State: "OPEN", Body: "body", Labels: []Label{{Name: "fanout:queued"}}},
		{Number: 222, Title: "missing labels normalizes empty", State: "OPEN", Body: "", Labels: []Label{}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseIssueList() = %#v, want %#v", got, want)
	}
}

func TestParseIssueListRejectsInvalidJSON(t *testing.T) {
	got, err := parseIssueList([]byte(`{"number":221}`))
	if err == nil {
		t.Fatalf("parseIssueList() = %#v, want error", got)
	}
}

func TestListOpenIssuesWithLabelRunsGHIssueList(t *testing.T) {
	argsPath := installFakeGH(t, `[{"number":221,"title":"queued","state":"OPEN","body":"body","labels":[{"name":"fanout:queued"}]}]`)

	got, err := (Runner{}).ListOpenIssuesWithLabel("fanout:queued")
	if err != nil {
		t.Fatal(err)
	}

	want := []Issue{{Number: 221, Title: "queued", State: "OPEN", Body: "body", Labels: []Label{{Name: "fanout:queued"}}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListOpenIssuesWithLabel() = %#v, want %#v", got, want)
	}
	assertFakeGHArgs(t, argsPath, []string{
		"issue", "list",
		"--state", "open",
		"--label", "fanout:queued",
		"--limit", "100",
		"--json", "number,title,state,body,labels",
	})
}

func TestListOpenIssuesWithLabelReturnsParseError(t *testing.T) {
	installFakeGH(t, `not-json`)

	got, err := (Runner{}).ListOpenIssuesWithLabel("fanout:queued")
	if err == nil {
		t.Fatalf("ListOpenIssuesWithLabel() = %#v, want error", got)
	}
	if !strings.Contains(err.Error(), `parse gh issue list --label "fanout:queued"`) {
		t.Fatalf("ListOpenIssuesWithLabel() error = %v", err)
	}
}

// TestListOpenIssuesPaginatesGraphQL guarantees ListOpenIssues walks every
// cursor page: page 1 reports hasNextPage with endCursor "C1", page 2 closes
// the walk, and the two pages concatenate in source order with State/Labels
// normalized. The second request must carry after=C1.
func TestListOpenIssuesPaginatesGraphQL(t *testing.T) {
	argsPath := installFakeGHScript(t, `
args="$*"
printf '%s\n' "$args" >> "$GH_FAKE_ARGS"
case "$args" in
*"-F after=C1"*)
  printf '{"data":{"repository":{"issues":{"nodes":[{"number":9,"title":"third","labels":{"nodes":[{"name":"bug"}]}}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}'
  ;;
*"api graphql"*)
  printf '{"data":{"repository":{"issues":{"nodes":[{"number":12,"title":"tui picker","labels":{"nodes":[{"name":"enhancement"}]}},{"number":11,"title":"second","labels":{"nodes":[]}}],"pageInfo":{"hasNextPage":true,"endCursor":"C1"}}}}}'
  ;;
*)
  printf 'unexpected gh args: %s\n' "$args" >&2
  exit 64
  ;;
esac
`)

	got, err := (Runner{}).ListOpenIssues()
	if err != nil {
		t.Fatal(err)
	}

	want := []Issue{
		{Number: 12, Title: "tui picker", State: "OPEN", Labels: []Label{{Name: "enhancement"}}},
		{Number: 11, Title: "second", State: "OPEN", Labels: []Label{}},
		{Number: 9, Title: "third", State: "OPEN", Labels: []Label{{Name: "bug"}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListOpenIssues() = %#v, want %#v", got, want)
	}

	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	// Each gh invocation records exactly one "-F owner={owner}" (the query text
	// uses $owner), so the count pins the number of requests.
	if calls := strings.Count(string(data), "-F owner={owner}"); calls != 2 {
		t.Fatalf("ListOpenIssues() made %d gh calls, want 2", calls)
	}
	if !strings.Contains(string(data), "-F after=C1") {
		t.Fatalf("ListOpenIssues() second page did not send after=C1:\n%s", data)
	}
}

// TestListOpenIssuesSinglePage guarantees a hasNextPage:false first page ends
// the walk after one request.
func TestListOpenIssuesSinglePage(t *testing.T) {
	argsPath := installFakeGHScript(t, `
args="$*"
printf '%s\n' "$args" >> "$GH_FAKE_ARGS"
case "$args" in
*"api graphql"*)
  printf '{"data":{"repository":{"issues":{"nodes":[{"number":12,"title":"tui picker","labels":{"nodes":[{"name":"enhancement"}]}}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}'
  ;;
*)
  printf 'unexpected gh args: %s\n' "$args" >&2
  exit 64
  ;;
esac
`)

	got, err := (Runner{}).ListOpenIssues()
	if err != nil {
		t.Fatal(err)
	}

	want := []Issue{{Number: 12, Title: "tui picker", State: "OPEN", Labels: []Label{{Name: "enhancement"}}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListOpenIssues() = %#v, want %#v", got, want)
	}

	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if calls := strings.Count(string(data), "-F owner={owner}"); calls != 1 {
		t.Fatalf("ListOpenIssues() made %d gh calls, want 1", calls)
	}
}

// TestListOpenIssuesReportsMissingRepository guarantees a null repository (bad
// token scope or repo) becomes an error rather than an empty list.
func TestListOpenIssuesReportsMissingRepository(t *testing.T) {
	installFakeGH(t, `{"data":{"repository":null}}`)

	got, err := (Runner{}).ListOpenIssues()
	if err == nil {
		t.Fatalf("ListOpenIssues() = %#v, want error", got)
	}
	if !strings.Contains(err.Error(), "repository not found") {
		t.Fatalf("ListOpenIssues() error = %v", err)
	}
}

func TestListOpenIssuesReturnsParseError(t *testing.T) {
	installFakeGH(t, `not-json`)

	got, err := (Runner{}).ListOpenIssues()
	if err == nil {
		t.Fatalf("ListOpenIssues() = %#v, want error", got)
	}
	if !strings.Contains(err.Error(), "parse gh api graphql (open issues)") {
		t.Fatalf("ListOpenIssues() error = %v", err)
	}
}

func TestIssueDetailsChunksAliasedGraphQLRequests(t *testing.T) {
	nums := make([]int, 51)
	for i := range nums {
		nums[i] = i + 1
	}
	firstPage := issueDetailsBatchFixture(nums[:50], nil)
	secondPage := issueDetailsBatchFixture(nums[50:], nil)
	argsPath := installFakeGHScript(t, `
args="$*"
printf '%s\n' "$args" >> "$GH_FAKE_ARGS"
case "$args" in
*"issue_51: issue(number: 51)"*)
  printf '%s' '`+secondPage+`'
  ;;
*"issue_1: issue(number: 1)"*)
  printf '%s' '`+firstPage+`'
  ;;
*)
  printf 'unexpected gh args: %s\n' "$args" >&2
  exit 64
  ;;
esac
`)

	got, err := (Runner{}).IssueDetails(nums)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(nums) {
		t.Fatalf("IssueDetails() returned %d issues, want %d", len(got), len(nums))
	}
	if got[51].Title != "issue 51" || got[51].State != "OPEN" || got[51].Body != "body 51" {
		t.Fatalf("IssueDetails()[51] = %#v", got[51])
	}

	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if calls := strings.Count(string(data), "api graphql"); calls != 2 {
		t.Fatalf("IssueDetails() made %d gh calls, want 2:\n%s", calls, data)
	}
	for _, field := range []string{"title", "body", "labels(first: 100)"} {
		if !strings.Contains(string(data), field) {
			t.Fatalf("IssueDetails() query missing %q:\n%s", field, data)
		}
	}
	if strings.Contains(string(data), "closedByPullRequestsReferences") {
		t.Fatalf("IssueDetails() fetched unused closing PRs:\n%s", data)
	}
}

func TestIssuesSnapshotWithPRsKeepsPartialResults(t *testing.T) {
	installFakeGH(t, `{
  "data": {
    "repository": {
      "issue_1": {
        "number": 1,
        "title": "one",
        "state": "OPEN",
        "body": "body",
        "labels": {"nodes": [{"name": "bug"}]},
        "closedByPullRequestsReferences": {"pageInfo": {"hasNextPage": false}, "nodes": []}
      },
      "issue_2": null
    }
  },
  "errors": [{"message": "Could not resolve to an Issue with the number of 2.", "path": ["repository", "issue_2"]}]
}`)

	got, err := (Runner{}).IssuesSnapshotWithPRs("owner", "repo", []int{1, 2})
	if err == nil || !strings.Contains(err.Error(), "#2: graphql: Could not resolve") {
		t.Fatalf("IssuesSnapshotWithPRs() error = %v, want per-issue #2 error", err)
	}
	if len(got) != 1 || got[1].Title != "one" || !reflect.DeepEqual(got[1].Labels, []Label{{Name: "bug"}}) {
		t.Fatalf("IssuesSnapshotWithPRs() = %#v, want successful #1 result", got)
	}
}

func TestIssuesSnapshotWithPRsFallsBackForPaginatedPRs(t *testing.T) {
	argsPath := installFakeGHScript(t, `
args="$*"
printf '%s\n' "$args" >> "$GH_FAKE_ARGS"
case "$args" in
*"-F after=C1"*)
  printf '%s' '{"state":"CLOSED","body":"body","closedByPullRequestsReferences":{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[{"number":102,"state":"MERGED","mergedAt":"2026-07-31T00:00:00Z","commits":{"nodes":[]}}]}}'
  ;;
*"-F num=7"*)
  printf '%s' '{"state":"CLOSED","body":"body","closedByPullRequestsReferences":{"pageInfo":{"hasNextPage":true,"endCursor":"C1"},"nodes":[{"number":101,"state":"CLOSED","mergedAt":null,"commits":{"nodes":[]}}]}}'
  ;;
*)
  printf '%s' '{"data":{"repository":{"issue_7":{"number":7,"title":"seven","state":"CLOSED","body":"body","labels":{"nodes":[{"name":"done"}]},"closedByPullRequestsReferences":{"pageInfo":{"hasNextPage":true,"endCursor":"ignored"},"nodes":[{"number":100,"state":"CLOSED","mergedAt":null,"commits":{"nodes":[]}}]}}}}}'
  ;;
esac
`)

	got, err := (Runner{}).IssuesSnapshotWithPRs("owner", "repo", []int{7})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := got[7]
	if snapshot.Title != "seven" || !reflect.DeepEqual(snapshot.Labels, []Label{{Name: "done"}}) {
		t.Fatalf("IssuesSnapshotWithPRs()[7] details = %#v", snapshot)
	}
	if len(snapshot.PRs) != 2 {
		t.Fatalf("IssuesSnapshotWithPRs()[7].PRs = %#v, want two paged PRs", snapshot.PRs)
	}
	if got, want := []int{snapshot.PRs[0].Number, snapshot.PRs[1].Number}, []int{101, 102}; !reflect.DeepEqual(got, want) {
		t.Fatalf("IssuesSnapshotWithPRs()[7].PRs = %#v, want PR numbers %#v", snapshot.PRs, want)
	}

	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if calls := strings.Count(string(data), "api graphql"); calls != 3 {
		t.Fatalf("IssuesSnapshotWithPRs() made %d gh calls, want batch plus two fallback pages:\n%s", calls, data)
	}
	if !strings.Contains(string(data), "-F after=C1") {
		t.Fatalf("IssuesSnapshotWithPRs() did not page the fallback:\n%s", data)
	}
	if !strings.Contains(string(data), "closedByPullRequestsReferences(first: 100)") {
		t.Fatalf("IssuesSnapshotWithPRs() batch query omitted closing PRs:\n%s", data)
	}
}

func issueDetailsBatchFixture(nums []int, hasNext map[int]bool) string {
	fields := make([]string, 0, len(nums))
	for _, num := range nums {
		number := strconv.Itoa(num)
		next := "false"
		if hasNext[num] {
			next = "true"
		}
		fields = append(fields, `"issue_`+number+`":{"number":`+number+`,"title":"issue `+number+`","state":"open","body":"body `+number+`","labels":{"nodes":[]},"closedByPullRequestsReferences":{"pageInfo":{"hasNextPage":`+next+`},"nodes":[]}}`)
	}
	return `{"data":{"repository":{` + strings.Join(fields, ",") + `}}}`
}

// TestListOpenIssuesClassifiesSubIssueGraph pins the Sub-issues classification
// the picker markers rely on: a parent surfaces its OPEN child count, a child
// carries its parent number, a standalone issue has neither link, and a parent
// whose children are all CLOSED reports zero OPEN children. It also confirms the
// query asks for the two new fields.
func TestListOpenIssuesClassifiesSubIssueGraph(t *testing.T) {
	tests := []struct {
		name              string
		node              string
		wantParent        int
		wantOpenSubIssues int
	}{
		{
			name:              "parent surfaces open child count",
			node:              `{"number":10,"title":"parent","labels":{"nodes":[]},"parent":null,"subIssuesSummary":{"total":3,"completed":1}}`,
			wantOpenSubIssues: 2,
		},
		{
			name:              "child carries parent number",
			node:              `{"number":11,"title":"child","labels":{"nodes":[]},"parent":{"number":10},"subIssuesSummary":{"total":0,"completed":0}}`,
			wantParent:        10,
			wantOpenSubIssues: 0,
		},
		{
			name:              "standalone has neither link",
			node:              `{"number":12,"title":"standalone","labels":{"nodes":[]},"parent":null,"subIssuesSummary":{"total":0,"completed":0}}`,
			wantOpenSubIssues: 0,
		},
		{
			// completed == total means every child is CLOSED: no OPEN fan-out target.
			name:              "all children closed reports zero open",
			node:              `{"number":13,"title":"done parent","labels":{"nodes":[]},"parent":null,"subIssuesSummary":{"total":2,"completed":2}}`,
			wantOpenSubIssues: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argsPath := installFakeGHScript(t, `
args="$*"
printf '%s\n' "$args" >> "$GH_FAKE_ARGS"
case "$args" in
*"api graphql"*)
  printf '{"data":{"repository":{"issues":{"nodes":[`+tt.node+`],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}'
  ;;
*)
  printf 'unexpected gh args: %s\n' "$args" >&2
  exit 64
  ;;
esac
`)

			got, err := (Runner{}).ListOpenIssues()
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 {
				t.Fatalf("ListOpenIssues() = %#v, want one issue", got)
			}
			if got[0].ParentNumber != tt.wantParent || got[0].OpenSubIssueCount != tt.wantOpenSubIssues {
				t.Fatalf("ListOpenIssues() parent/openSubIssues = %d/%d, want %d/%d",
					got[0].ParentNumber, got[0].OpenSubIssueCount, tt.wantParent, tt.wantOpenSubIssues)
			}

			data, err := os.ReadFile(argsPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"parent", "subIssuesSummary"} {
				if !strings.Contains(string(data), field) {
					t.Fatalf("ListOpenIssues() query missing %q:\n%s", field, data)
				}
			}
		})
	}
}

func TestSwapIssueLabelsRunsSingleEdit(t *testing.T) {
	argsPath := installFakeGH(t, ``)

	if err := (Runner{}).SwapIssueLabels(221, "fanout:queued", "fanout:running"); err != nil {
		t.Fatal(err)
	}

	assertFakeGHArgs(t, argsPath, []string{
		"issue", "edit", "221",
		"--remove-label", "fanout:queued",
		"--add-label", "fanout:running",
	})
}

func TestSwapIssueLabelsReturnsGHError(t *testing.T) {
	installFakeGHWithResult(t, ``, `missing label`, 1)

	err := (Runner{}).SwapIssueLabels(221, "fanout:queued", "fanout:running")
	if err == nil {
		t.Fatal("SwapIssueLabels() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "missing label") {
		t.Fatalf("SwapIssueLabels() error = %v", err)
	}
}

func TestRemoveIssueLabelRunsSingleEdit(t *testing.T) {
	argsPath := installFakeGH(t, ``)

	if err := (Runner{}).RemoveIssueLabel(226, "fanout:running"); err != nil {
		t.Fatal(err)
	}

	assertFakeGHArgs(t, argsPath, []string{
		"issue", "edit", "226",
		"--remove-label", "fanout:running",
	})
}

func TestRemoveIssueLabelReturnsGHError(t *testing.T) {
	installFakeGHWithResult(t, ``, `missing label`, 1)

	err := (Runner{}).RemoveIssueLabel(226, "fanout:running")
	if err == nil {
		t.Fatal("RemoveIssueLabel() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "missing label") {
		t.Fatalf("RemoveIssueLabel() error = %v", err)
	}
}

func TestEnsureLabelSkipsCreateWhenPresent(t *testing.T) {
	argsPath := installFakeGH(t, `[{"name":"fanout:running"}]`)

	if err := (Runner{}).EnsureLabel("fanout:running"); err != nil {
		t.Fatal(err)
	}

	assertFakeGHArgs(t, argsPath, []string{
		"label", "list",
		"--search", "fanout:running",
		"--limit", "100",
		"--json", "name",
	})
}

func TestEnsureLabelCreatesMissingLabel(t *testing.T) {
	argsPath := installFakeGHScript(t, `
args="$*"
printf '%s\n' "$args" >> "$GH_FAKE_ARGS"
case "$args" in
"label list --search fanout:running --limit 100 --json name")
  printf '[{"name":"fanout:queued"}]'
  ;;
"label create fanout:running")
  ;;
*)
  printf 'unexpected gh args: %s\n' "$args" >&2
  exit 64
  ;;
esac
`)

	if err := (Runner{}).EnsureLabel("fanout:running"); err != nil {
		t.Fatal(err)
	}

	assertFakeGHCommandLines(t, argsPath, []string{
		"label list --search fanout:running --limit 100 --json name",
		"label create fanout:running",
	})
}

func TestEnsureLabelCreatesWhenListOutputEmpty(t *testing.T) {
	argsPath := installFakeGHScript(t, `
args="$*"
printf '%s\n' "$args" >> "$GH_FAKE_ARGS"
case "$args" in
"label list --search fanout:running --limit 100 --json name")
  ;;
"label create fanout:running")
  ;;
*)
  printf 'unexpected gh args: %s\n' "$args" >&2
  exit 64
  ;;
esac
`)

	if err := (Runner{}).EnsureLabel("fanout:running"); err != nil {
		t.Fatal(err)
	}

	assertFakeGHCommandLines(t, argsPath, []string{
		"label list --search fanout:running --limit 100 --json name",
		"label create fanout:running",
	})
}

func TestEnsureLabelReturnsParseError(t *testing.T) {
	installFakeGH(t, `not-json`)

	err := (Runner{}).EnsureLabel("fanout:running")
	if err == nil {
		t.Fatal("EnsureLabel() error = nil, want error")
	}
	if !strings.Contains(err.Error(), `parse gh label list --search "fanout:running"`) {
		t.Fatalf("EnsureLabel() error = %v", err)
	}
}

func TestEnsureLabelReturnsCreateError(t *testing.T) {
	installFakeGHScript(t, `
args="$*"
case "$args" in
"label list --search fanout:running --limit 100 --json name")
  printf '[]'
  ;;
"label create fanout:running")
  printf 'create failed' >&2
  exit 1
  ;;
*)
  printf 'unexpected gh args: %s\n' "$args" >&2
  exit 64
  ;;
esac
`)

	err := (Runner{}).EnsureLabel("fanout:running")
	if err == nil {
		t.Fatal("EnsureLabel() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "create failed") {
		t.Fatalf("EnsureLabel() error = %v", err)
	}
}

func TestPRsForBranchMapsListOutput(t *testing.T) {
	mergedAt := "2026-06-13T01:02:03Z"
	argsPath := installFakeGH(t, `[
  {
    "number": 10,
    "state": "MERGED",
    "mergedAt": "2026-06-13T01:02:03Z",
    "isDraft": false,
    "reviewDecision": "APPROVED",
    "statusCheckRollup": {"state": "SUCCESS"}
  },
  {
    "number": 11,
    "state": "OPEN",
    "mergedAt": null,
    "isDraft": true,
    "reviewDecision": "REVIEW_REQUIRED",
    "statusCheckRollup": [{"status": "IN_PROGRESS"}]
  },
  {
    "number": 12,
    "state": "OPEN",
    "mergedAt": null,
    "isDraft": false,
    "reviewDecision": "",
    "statusCheckRollup": [
      {"status": "COMPLETED", "conclusion": "SUCCESS"},
      {"status": "COMPLETED", "conclusion": "TIMED_OUT"}
    ]
  }
]`)

	got, err := (Runner{}).PRsForBranch("fanout/taskid-state-pr-213")
	if err != nil {
		t.Fatal(err)
	}

	want := []PRRef{
		{
			Number:         10,
			State:          "MERGED",
			MergedAt:       &mergedAt,
			ReviewDecision: "APPROVED",
			CIStatus:       "pass",
		},
		{
			Number:         11,
			State:          "OPEN",
			IsDraft:        true,
			ReviewDecision: "REVIEW_REQUIRED",
			CIStatus:       "pending",
		},
		{
			Number:   12,
			State:    "OPEN",
			CIStatus: "fail",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PRsForBranch() = %#v, want %#v", got, want)
	}
	assertFakeGHArgs(t, argsPath, []string{
		"pr", "list",
		"--head", "fanout/taskid-state-pr-213",
		"--state", "all",
		"--json", "number,state,mergedAt,isDraft,reviewDecision,statusCheckRollup",
	})
}

func TestPRsForBranchReturnsEmptyList(t *testing.T) {
	installFakeGH(t, `[]`)

	got, err := (Runner{}).PRsForBranch("fanout/no-prs")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("PRsForBranch() = %#v, want empty", got)
	}
}

func TestNormalizeCIStatus(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{in: "SUCCESS", want: "pass"},
		{in: "FAILURE", want: "fail"},
		{in: "ERROR", want: "fail"},
		{in: "CANCELLED", want: "fail"}, //nolint:misspell // GitHub's CheckConclusionState enum spells this CANCELLED.
		{in: "TIMED_OUT", want: "fail"},
		{in: "ACTION_REQUIRED", want: "fail"},
		{in: "STARTUP_FAILURE", want: "fail"},
		{in: "PENDING", want: "pending"},
		{in: "EXPECTED", want: "pending"},
		{in: "", want: ""},
	} {
		if got := normalizeCIStatus(tc.in); got != tc.want {
			t.Fatalf("normalizeCIStatus(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func installFakeGH(t *testing.T, output string) string {
	t.Helper()
	return installFakeGHWithResult(t, output, "", 0)
}

func installFakeGHWithResult(t *testing.T, output, stderr string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	script := filepath.Join(dir, "gh")
	body := `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$@" > "$GH_FAKE_ARGS"
if [[ "$GH_FAKE_EXIT" != "0" ]]; then
  printf '%s' "$GH_FAKE_STDERR" >&2
  exit "$GH_FAKE_EXIT"
fi
printf '%s' "$GH_FAKE_OUTPUT"
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_FAKE_ARGS", argsPath)
	t.Setenv("GH_FAKE_OUTPUT", output)
	t.Setenv("GH_FAKE_STDERR", stderr)
	t.Setenv("GH_FAKE_EXIT", strconv.Itoa(exitCode))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsPath
}

func installFakeGHScript(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	script := filepath.Join(dir, "gh")
	fullBody := "#!/usr/bin/env bash\nset -euo pipefail\n" + body
	if err := os.WriteFile(script, []byte(fullBody), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_FAKE_ARGS", argsPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsPath
}

func assertFakeGHArgs(t *testing.T, argsPath string, want []string) {
	t.Helper()
	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("gh args = %#v, want %#v", got, want)
	}
}

func assertFakeGHCommandLines(t *testing.T, argsPath string, want []string) {
	t.Helper()
	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("gh command lines = %#v, want %#v", got, want)
	}
}

func TestTaskListWavesAssignsWaveHeadingsToIssueRows(t *testing.T) {
	body := `
overview

**wave5（監視レイヤ）**
- [ ] #115 dashboard filter
- [x] #116 another dashboard item

Some notes about the same wave.
- [ ] owner/repo#999 ignored cross repo

## Wave 6
- [ ] #120 next work
`

	got := TaskListWaves(body)

	for issue, want := range map[int]string{
		115: "wave5",
		116: "wave5",
		120: "wave6",
	} {
		if got[issue] != want {
			t.Fatalf("TaskListWaves()[%d] = %q, want %q (all: %#v)", issue, got[issue], want, got)
		}
	}
	if _, ok := got[999]; ok {
		t.Fatalf("TaskListWaves assigned cross-repo issue 999: %#v", got)
	}
}

func TestParsePages(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      string
		want    [][]subIssueItem
		wantErr bool
	}{
		{
			name: "slurp pages",
			in:   `[[{"number":1,"title":"a","state":"open"}],[{"number":2,"title":"b","state":"closed"}]]`,
			want: [][]subIssueItem{{{Number: 1, Title: "a", State: "open"}}, {{Number: 2, Title: "b", State: "closed"}}},
		},
		{
			name: "single array fallback",
			in:   `[{"number":1,"title":"a","state":"open"}]`,
			want: [][]subIssueItem{{{Number: 1, Title: "a", State: "open"}}},
		},
		{
			name: "empty page",
			in:   `[[]]`,
			want: [][]subIssueItem{{}},
		},
		{
			name:    "invalid JSON",
			in:      `{"subIssues":[]}`,
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePages[subIssueItem]([]byte(tc.in))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parsePages(%q) = %#v, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePages(%q) error: %v", tc.in, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("parsePages(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
			for i := range got {
				if len(got[i]) != len(tc.want[i]) {
					t.Fatalf("parsePages(%q) page %d = %#v, want %#v", tc.in, i, got[i], tc.want[i])
				}
				for j := range got[i] {
					if got[i][j] != tc.want[i][j] {
						t.Fatalf("parsePages(%q)[%d][%d] = %#v, want %#v", tc.in, i, j, got[i][j], tc.want[i][j])
					}
				}
			}
		})
	}
}
