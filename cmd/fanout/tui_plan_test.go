package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/state"
	fanouttui "github.com/butaosuinu/fanout/internal/tui"
)

const tuiPlanTestSpec = `{
  "version": 1,
  "plan": {
    "slug": "launch-plan",
    "title": "Launch plan",
    "base_branch": "main"
  },
  "tasks": [
    {
      "id": "base-types",
      "title": "Define base types",
      "briefing": "## Goal\nDefine base types"
    },
    {
      "id": "api-client",
      "title": "Extract API client",
      "briefing": "## Goal\nExtract API client",
      "blocked_by": ["base-types"],
      "wave": "2"
    }
  ]
}`

func writeTUIPlanTestSpec(t *testing.T, repo string) {
	t.Helper()
	plansDir := filepath.Join(repo, ".fanout", "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plansDir, "launch-plan.json"), []byte(tuiPlanTestSpec), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNewTUIListPlanTasksFuncReadsSpec(t *testing.T) {
	repo := t.TempDir()
	writeTUIPlanTestSpec(t, repo)

	got, err := newTUIListPlanTasksFunc(repo)("launch-plan")
	if err != nil {
		t.Fatal(err)
	}

	want := []fanouttui.PlanTaskItem{
		{ID: "base-types", Title: "Define base types"},
		{ID: "api-client", Title: "Extract API client", Wave: "2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("newTUIListPlanTasksFunc() = %#v, want %#v", got, want)
	}
}

func TestNewTUIListPlanTasksFuncReportsMissingSpec(t *testing.T) {
	if _, err := newTUIListPlanTasksFunc(t.TempDir())("no-such-plan"); err == nil {
		t.Fatal("newTUIListPlanTasksFunc() succeeded for a missing spec")
	}
}

// TestLaunchPlanFromTUINothingToDo drives runPlanWithRuntime end to end with
// every task already recorded, pinning the notice the TUI shows instead of a
// silent no-op.
func TestLaunchPlanFromTUINothingToDo(t *testing.T) {
	repo := t.TempDir()
	initTUITestGitRepo(t, repo)
	writeTUIPlanTestSpec(t, repo)
	locked, err := state.LockProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, taskID := range []string{"base-types", "api-client"} {
		if err = locked.RecordPane(state.Pane{Parent: "plan:launch-plan", TaskID: taskID, Slug: "launch-plan-" + taskID, PaneID: "%1"}); err != nil {
			t.Fatal(err)
		}
	}
	if err = locked.Unlock(); err != nil {
		t.Fatal(err)
	}
	installFakeExecutable(t, "claude")

	notice, err := launchPlanFromTUI(repo, "fanout-test", "fanout", "launch-plan", "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notice, "plan launch-plan: nothing to do") {
		t.Fatalf("launchPlanFromTUI() notice = %q, want nothing-to-do", notice)
	}
}

func TestLaunchPlanFromTUIReportsSpecLoadError(t *testing.T) {
	repo := t.TempDir()
	initTUITestGitRepo(t, repo)
	installFakeExecutable(t, "claude")

	_, err := launchPlanFromTUI(repo, "fanout-test", "fanout", "no-such-plan", "claude", nil)
	if err == nil {
		t.Fatal("launchPlanFromTUI() succeeded for a missing spec")
	}
}

func TestLaunchPlanFromTUIRejectsEmptySlug(t *testing.T) {
	if _, err := launchPlanFromTUI(t.TempDir(), "fanout-test", "fanout", "  ", "claude", nil); err == nil {
		t.Fatal("launchPlanFromTUI() succeeded for an empty slug")
	}
}
