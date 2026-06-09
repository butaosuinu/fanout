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
