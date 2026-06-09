package main

import "testing"

func TestBuildDashboardBody(t *testing.T) {
	got := buildDashboardBody(200, statusSummary{
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

Total: 2 | Merged: 1 | Pending: 1 | All merged: false

| Sub-issue # | PR | PR state | CI | +/- | Type | TL;DR | Score |
| --- | --- | --- | --- | --- | --- | --- | --- |
| #201 | [#601](https://github.com/butaosuinu/fanout/pull/601) | merged | pass | +120 / -8 (5 files) | feat | Adds the dashboard \| status rollup. | 2 |
| #202 | - | - | - | - | - | No PR yet | - |
`
	if got != want {
		t.Fatalf("buildDashboardBody() =\n%s\nwant\n%s", got, want)
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
