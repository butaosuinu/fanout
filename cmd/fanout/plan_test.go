package main

import (
	"reflect"
	"testing"

	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/ghissue"
)

func TestBuildPlanOnlyIdempotencyAndLimit(t *testing.T) {
	children := []ghissue.Issue{
		{Number: 101, Title: "first", State: "OPEN"},
		{Number: 102, Title: "second", State: "OPEN"},
		{Number: 103, Title: "closed", State: "CLOSED"},
		{Number: 104, Title: "already", State: "OPEN"},
	}
	cfg := &cliflags.Config{
		Only:  []int{101, 102, 104, 999},
		Limit: 1,
	}

	plan := buildPlan(cfg, children, map[int]bool{104: true}, "", nil, nil)

	assertEqual(t, "total", plan.TotalChildren, 4)
	assertEqual(t, "open", plan.OpenCount, 3)
	assertEqual(t, "open after filter", plan.OpenAfterFilter, 3)
	assertEqual(t, "unfanned", plan.UnfannedCount, 2)
	assertInts(t, "missing only", plan.MissingOnly, []int{999})
	assertInts(t, "targets", issueNums(plan.Targets), []int{101})
	assertInts(t, "limit deferred", issueNums(plan.LimitDeferred), []int{102})
	assertInts(t, "already fanned", plan.AlreadyFanned, []int{104})
}

func TestBuildPlanSkipFilter(t *testing.T) {
	children := []ghissue.Issue{
		{Number: 201, Title: "keep one", State: "OPEN"},
		{Number: 202, Title: "skip me", State: "OPEN"},
		{Number: 203, Title: "keep two", State: "OPEN"},
	}
	cfg := &cliflags.Config{Skip: []int{202}}

	plan := buildPlan(cfg, children, nil, "", nil, nil)

	assertInts(t, "targets", issueNums(plan.Targets), []int{201, 203})
	assertInts(t, "filtered skip", issueNums(plan.FilteredSkip), []int{202})
	if len(plan.FilteredOnly) != 0 {
		t.Fatalf("filtered only = %#v, want empty", plan.FilteredOnly)
	}
}

func TestBuildPlanUnblockedOnly(t *testing.T) {
	children := []ghissue.Issue{
		{Number: 301, Title: "blocked by child body", State: "OPEN", Body: "## Blocked by\n- #401\n"},
		{Number: 302, Title: "blocked label only", State: "OPEN", Labels: []ghissue.Label{{Name: "blocked"}}},
		{Number: 303, Title: "blocked by parent row", State: "OPEN", Body: "## Blocked by\n- #402\n"},
	}
	parentBody := "## Fanout children\n- [ ] #303 blocked by parent (blocked by #403)\n"
	states := map[int]string{
		401: "OPEN",
		402: "CLOSED",
		403: "OPEN",
	}
	cfg := &cliflags.Config{UnblockedOnly: true}

	plan := buildPlan(cfg, children, nil, parentBody, nil, func(num int) string {
		return states[num]
	})

	assertInts(t, "targets", issueNums(plan.Targets), []int{302})
	assertInts(t, "blocked label without refs", plan.BlockedLabelWithoutRefs, []int{302})
	if len(plan.BlockedRows) != 2 {
		t.Fatalf("blocked rows len = %d, want 2", len(plan.BlockedRows))
	}
	assertEqual(t, "first blocked issue", plan.BlockedRows[0].Issue.Number, 301)
	assertEqual(t, "first blocked refs", plan.BlockedRows[0].Refs, "OPEN #401")
	assertEqual(t, "second blocked issue", plan.BlockedRows[1].Issue.Number, 303)
	assertEqual(t, "second blocked refs", plan.BlockedRows[1].Refs, "OPEN #403")
}

func TestBuildPlanHydratesBeforeBlockerCheck(t *testing.T) {
	children := []ghissue.Issue{
		{Number: 501, Title: "needs hydration", State: "OPEN"},
	}
	cfg := &cliflags.Config{UnblockedOnly: true}
	hydrated := false

	plan := buildPlan(cfg, children, nil, "", func(issue *ghissue.Issue) {
		hydrated = true
		issue.Body = "## Blocked by\n- #601\n"
	}, func(num int) string {
		if num == 601 {
			return "OPEN"
		}
		return "UNKNOWN"
	})

	if !hydrated {
		t.Fatal("hydrateIssue was not called")
	}
	if len(plan.Targets) != 0 {
		t.Fatalf("targets = %#v, want empty", plan.Targets)
	}
	if len(plan.BlockedRows) != 1 {
		t.Fatalf("blocked rows len = %d, want 1", len(plan.BlockedRows))
	}
	assertEqual(t, "blocked issue", plan.BlockedRows[0].Issue.Number, 501)
	assertEqual(t, "blocked refs", plan.BlockedRows[0].Refs, "OPEN #601")
}

func issueNums(issues []ghissue.Issue) []int {
	nums := make([]int, len(issues))
	for i, issue := range issues {
		nums[i] = issue.Number
	}
	return nums
}

func assertInts(t *testing.T, name string, got, want []int) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %#v, want %#v", name, got, want)
	}
}

func assertEqual[T comparable](t *testing.T, name string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %#v, want %#v", name, got, want)
	}
}
