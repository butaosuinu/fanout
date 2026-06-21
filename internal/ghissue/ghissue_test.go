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
