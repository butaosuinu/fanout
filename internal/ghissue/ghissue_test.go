package ghissue

import "testing"

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

func TestNormalizeCIStatus(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{in: "SUCCESS", want: "pass"},
		{in: "FAILURE", want: "fail"},
		{in: "ERROR", want: "fail"},
		{in: "PENDING", want: "pending"},
		{in: "EXPECTED", want: "pending"},
		{in: "", want: ""},
	} {
		if got := normalizeCIStatus(tc.in); got != tc.want {
			t.Fatalf("normalizeCIStatus(%q) = %q, want %q", tc.in, got, tc.want)
		}
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
