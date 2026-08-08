package statusreport

import (
	"bytes"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/planspec"
	"github.com/butaosuinu/fanout/internal/infra/log"
)

func TestBuildDashboardBody(t *testing.T) {
	got := buildDashboardBody(200, Summary{
		Total:     2,
		Merged:    1,
		Pending:   1,
		AllMerged: false,
	}, []dashboardRow{
		{
			Issue:   "#201",
			PR:      "[#601](https://github.com/butaosuinu/fanout/pull/601)",
			PRState: "merged",
			CI:      "pass",
			Diff:    "+120 / -8 (5 files)",
			Type:    "feat",
			TLDR:    "Adds the dashboard | status rollup.",
			Score:   "2",
		},
		{
			Issue:   "#202",
			PR:      "-",
			PRState: "-",
			CI:      "-",
			Diff:    "-",
			Type:    "-",
			TLDR:    "No PR yet",
			Score:   "-",
		},
	})
	want := `<!-- fanout:dashboard parent=200 -->
## fanout dashboard #200

Total: 2 | Merged: 1 | Pending: 1 | Blocked: 0 | All merged: false

| Sub-issue # | PR | PR state | CI | +/- | Type | TL;DR | Score |
| --- | --- | --- | --- | --- | --- | --- | --- |
| #201 | [#601](https://github.com/butaosuinu/fanout/pull/601) | merged | pass | +120 / -8 (5 files) | feat | Adds the dashboard \| status rollup. | 2 |
| #202 | - | - | - | - | - | No PR yet | - |
`
	if got != want {
		t.Fatalf("buildDashboardBody() =\n%s\nwant\n%s", got, want)
	}
}

func TestNewStatusReportCountsBlockedChildren(t *testing.T) {
	report := NewReport(200, []Child{
		{Num: 201, HasMergedPR: true},
		{Num: 202, Blocked: true},
		{Num: 203},
	})

	if report.Summary.Total != 3 || report.Summary.Merged != 1 || report.Summary.Pending != 2 || report.Summary.Blocked != 1 {
		t.Fatalf("summary = %#v, want total=3 merged=1 pending=2 blocked=1", report.Summary)
	}
}

func TestExtractDashboardPRBody(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      string
		wantTLDR  string
		wantScore string
	}{
		{
			name:      "inline label",
			body:      "TL;DR: Adds the parent dashboard.\nReview effort: 3\n\n## Test plan\n- make test",
			wantTLDR:  "Adds the parent dashboard.",
			wantScore: "3",
		},
		{
			name:      "heading label",
			body:      "\n## TL;DR\nPosts one rollup comment.\n\nReview effort: 4\n",
			wantTLDR:  "Posts one rollup comment.",
			wantScore: "4",
		},
		{
			name:      "prefers later explicit tldr",
			body:      "## Summary\nImplementation details.\n\n## TL;DR\nDashboard rows stay concise.\n\nReview effort: 2\n",
			wantTLDR:  "Dashboard rows stay concise.",
			wantScore: "2",
		},
		{
			name:      "score only",
			body:      "Review effort: 2\n",
			wantTLDR:  "-",
			wantScore: "2",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotTLDR, gotScore := extractDashboardPRBody(tc.body)
			if gotTLDR != tc.wantTLDR || gotScore != tc.wantScore {
				t.Fatalf("extractDashboardPRBody() = (%q, %q), want (%q, %q)", gotTLDR, gotScore, tc.wantTLDR, tc.wantScore)
			}
		})
	}
}

func TestSummarize(t *testing.T) {
	tests := []struct {
		name    string
		merged  []bool // one item per entry; true = merged
		blocked []bool
		want    Summary
	}{
		{
			name:    "empty input is never all merged",
			merged:  nil,
			blocked: nil,
			want:    Summary{},
		},
		{
			name:    "mixed items count pending as non-merged",
			merged:  []bool{true, false, false},
			blocked: []bool{false, true, false},
			want:    Summary{Total: 3, Merged: 1, Pending: 2, Blocked: 1},
		},
		{
			name:    "all merged flips the flag",
			merged:  []bool{true, true},
			blocked: []bool{false, false},
			want:    Summary{Total: 2, Merged: 2, Pending: 0, Blocked: 0, AllMerged: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items := make([]int, len(tt.merged))
			for i := range items {
				items[i] = i
			}
			got := Summarize(items,
				func(i int) bool { return tt.merged[i] },
				func(i int) bool { return tt.blocked[i] })
			if got != tt.want {
				t.Fatalf("Summarize() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestPlanTaskStatusBlocked(t *testing.T) {
	// planTaskStatusBlocked treats a merged task as never blocked and an
	// unmerged dependency as blocking.
	tests := []struct {
		name      string
		taskID    string
		blockedBy []string
		merged    map[string]bool
		want      bool
	}{
		{
			name:   "merged task is never blocked",
			taskID: "a", blockedBy: []string{"b"},
			merged: map[string]bool{"a": true},
			want:   false,
		},
		{
			name:   "unmerged dependency blocks",
			taskID: "a", blockedBy: []string{"b"},
			merged: map[string]bool{"a": false, "b": false},
			want:   true,
		},
		{
			name:   "all dependencies merged unblocks",
			taskID: "a", blockedBy: []string{"b"},
			merged: map[string]bool{"a": false, "b": true},
			want:   false,
		},
		{
			name:   "no dependencies never blocks",
			taskID: "a",
			merged: map[string]bool{"a": false},
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := planspec.Task{ID: tt.taskID, BlockedBy: tt.blockedBy}
			if got := planTaskStatusBlocked(task, tt.merged); got != tt.want {
				t.Fatalf("planTaskStatusBlocked(%s, %v) = %t, want %t", tt.taskID, tt.merged, got, tt.want)
			}
		})
	}
}

func TestWriteTableIncludesBackendAndPaneColumns(t *testing.T) {
	var out bytes.Buffer
	WriteTable(&out, log.Palette{}, TableSpec{
		Heading:        "fanout status #100",
		EmptyText:      "empty",
		FirstColHeader: "ISSUE",
	}, []TableRow{{
		Label: "#101", Backend: "herdr", PaneID: "workspace-1:terminal-2",
		ReportedState: "working",
		State:         "OPEN", PR: "-", PRState: "-", CI: "-", Type: "-", Files: "-", Link: "-",
	}}, 0, len("+0"), len("-0"))

	got := out.String()
	if !strings.Contains(got, "ISSUE  BACKEND  PANE                    REPORTED_STATE") {
		t.Fatalf("table header does not include backend metadata:\n%s", got)
	}
	if !strings.Contains(got, "#101   herdr    workspace-1:terminal-2  working") {
		t.Fatalf("table row does not include backend metadata:\n%s", got)
	}
}
