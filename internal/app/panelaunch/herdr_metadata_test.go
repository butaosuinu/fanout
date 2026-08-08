package panelaunch

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/log"
	fanoutruntime "github.com/butaosuinu/fanout/internal/infra/runtime"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

// TestHerdrSidebarMetadataFixesTokenPlacement pins which resource owns which
// token: the workspace names the child, the pane carries the fan-out and the
// work status. Moving a token between the two silently blanks a sidebar row.
func TestHerdrSidebarMetadataFixesTokenPlacement(t *testing.T) {
	tests := []struct {
		name          string
		req           Request
		wantWorkspace []herdrrun.MetadataToken
		wantPane      []herdrrun.MetadataToken
	}{
		{
			name: "issue child",
			req:  Request{ParentRef: "524", Number: 494, Slug: "herdr-sidebar-494"},
			wantWorkspace: []herdrrun.MetadataToken{
				{Name: "fanout_issue", Value: "#494"},
				{Name: "fanout_slug", Value: "herdr-sidebar-494"},
			},
			wantPane: []herdrrun.MetadataToken{
				{Name: "fanout_parent", Value: "#524"},
				{Name: "fanout_pr"},
				{Name: "fanout_ci"},
			},
		},
		{
			name: "plan task uses the task id and the plan parent",
			req:  Request{ParentRef: "plan:launch-plan", TaskID: "api-layer", Slug: "api-layer"},
			wantWorkspace: []herdrrun.MetadataToken{
				{Name: "fanout_issue", Value: "api-layer"},
				{Name: "fanout_slug", Value: "api-layer"},
			},
			wantPane: []herdrrun.MetadataToken{
				{Name: "fanout_parent", Value: "plan:launch-plan"},
				{Name: "fanout_pr"},
				{Name: "fanout_ci"},
			},
		},
		{
			name: "project child drops the github host from the parent",
			req: Request{
				ParentRef: "https://github.com/butaosuinu/fanout/projects/3",
				Number:    494, Slug: "herdr-sidebar-494",
			},
			wantWorkspace: []herdrrun.MetadataToken{
				{Name: "fanout_issue", Value: "#494"},
				{Name: "fanout_slug", Value: "herdr-sidebar-494"},
			},
			wantPane: []herdrrun.MetadataToken{
				{Name: "fanout_parent", Value: "butaosuinu/fanout/projects/3"},
				{Name: "fanout_pr"},
				{Name: "fanout_ci"},
			},
		},
		{
			// The watcher stores the synthetic @watch parent, and the rest of
			// the launch path resolves it to the issue it picked up. Reporting
			// the marker would leave the Agent row unable to name any issue.
			name: "watcher launch names the issue it picked up",
			req:  Request{ParentRef: WatchParentRef, Number: 494, Slug: "herdr-sidebar-494"},
			wantWorkspace: []herdrrun.MetadataToken{
				{Name: "fanout_issue", Value: "#494"},
				{Name: "fanout_slug", Value: "herdr-sidebar-494"},
			},
			wantPane: []herdrrun.MetadataToken{
				{Name: "fanout_parent", Value: "#494"},
				{Name: "fanout_pr"},
				{Name: "fanout_ci"},
			},
		},
		{
			// A synthetic manual row has neither an issue number nor a task
			// id, so the name clears instead of showing a negative row number.
			name: "synthetic manual row clears the child name",
			req:  Request{ParentRef: ManualParentRef, Number: -3, Slug: "shell-3"},
			wantWorkspace: []herdrrun.MetadataToken{
				{Name: "fanout_issue"},
				{Name: "fanout_slug", Value: "shell-3"},
			},
			wantPane: []herdrrun.MetadataToken{
				{Name: "fanout_parent", Value: ManualParentRef},
				{Name: "fanout_pr"},
				{Name: "fanout_ci"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := herdrSidebarMetadata(tt.req, testHerdrMetadataIntent())
			if !reflect.DeepEqual(got.WorkspaceTokens, tt.wantWorkspace) {
				t.Errorf("workspace tokens = %#v, want %#v", got.WorkspaceTokens, tt.wantWorkspace)
			}
			if !reflect.DeepEqual(got.PaneTokens, tt.wantPane) {
				t.Errorf("pane tokens = %#v, want %#v", got.PaneTokens, tt.wantPane)
			}
		})
	}
}

// TestHerdrSidebarMetadataTargetsTheRealizedChild keeps the recheck bound to
// the identity the launch verified, not to a freshly guessed one.
func TestHerdrSidebarMetadataTargetsTheRealizedChild(t *testing.T) {
	got := herdrSidebarMetadata(Request{Number: 494, ParentRef: "524"}, testHerdrMetadataIntent()).Target
	want := herdrrun.MetadataTarget{
		WorkspaceID: "w2", Label: "fanout-worktree-abc",
		RepoKey: "/repo/.git", RepoRoot: "/repo",
		CheckoutPath: "/repo/.fanout/worktrees/child",
		PaneID:       "w2:p1", TerminalID: "term-child",
	}
	if got != want {
		t.Fatalf("metadata target = %+v, want %+v", got, want)
	}
}

func TestHerdrMetadataValueMatchesHerdrStorage(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "keeps ordinary display text", raw: "herdr-sidebar-494", want: "herdr-sidebar-494"},
		{name: "trims surrounding whitespace", raw: "  slug  ", want: "slug"},
		{name: "drops control characters herdr would strip", raw: "a\tb\nc", want: "abc"},
		{
			name: "shortens to the value limit herdr truncates at",
			raw:  strings.Repeat("x", 90), want: strings.Repeat("x", 80),
		},
		{
			// Truncation counts characters, not bytes, so a multi-byte value
			// keeps 80 whole runes instead of a split one.
			name: "counts characters for multi-byte values",
			raw:  strings.Repeat("あ", 90), want: strings.Repeat("あ", 80),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := herdrMetadataValue(tt.raw); got != tt.want {
				t.Errorf("herdrMetadataValue(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// TestReportHerdrSidebarMetadataNeverFailsALaunch keeps metadata display-only:
// the report runs once after the row is recorded, is not retried, and stays out
// of stderr so cmd/fanout's bufferedLaunchNotice cannot turn a cosmetic token
// failure into the TUI's launch banner.
func TestReportHerdrSidebarMetadataNeverFailsALaunch(t *testing.T) {
	runtime := &fakeHerdrLaunchRuntime{metadataErr: errors.New("target is not live")}
	var out, errOut strings.Builder
	launcher := &Launcher{
		Cfg: &cliflags.Config{}, Log: log.NewWith(&out, &errOut, false),
		Info: &fanoutruntime.Info{ProjectRoot: "/repo"}, Herdr: runtime,
	}
	req := Request{ParentRef: "524", Number: 494, Slug: "herdr-sidebar-494"}

	launcher.reportHerdrSidebarMetadata(req, testHerdrMetadataIntent())

	if len(runtime.metadataReports) != 1 {
		t.Fatalf("metadata reports = %d, want exactly one attempt", len(runtime.metadataReports))
	}
	if !strings.Contains(out.String(), "target is not live") {
		t.Errorf("stdout = %q, want the report failure noted", out.String())
	}
	if errOut.String() != "" {
		t.Errorf("stderr = %q, want a display-only failure to stay off the launch log", errOut.String())
	}
}

func testHerdrMetadataIntent() state.HerdrIntent {
	return state.HerdrIntent{
		WorktreePath: "/repo/.fanout/worktrees/child",
		Resource: state.HerdrResource{
			WorkspaceID: "w2", Label: "fanout-worktree-abc",
			PaneID: "w2:p1", TerminalID: "term-child",
			CurrentPath: "/repo/.fanout/worktrees/child",
			RepoKey:     "/repo/.git", RepoRoot: "/repo",
		},
	}
}
