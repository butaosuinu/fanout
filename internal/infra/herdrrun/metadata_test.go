package herdrrun

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
)

func TestMetadataArgsPinHerdr075CLI(t *testing.T) {
	tests := []struct {
		name     string
		resource string
		id       string
		tokens   []MetadataToken
		want     []string
	}{
		{
			name: "workspace sets every value", resource: "workspace", id: "w2",
			tokens: []MetadataToken{
				{Name: "fanout_issue", Value: "#494"},
				{Name: "fanout_slug", Value: "herdr-sidebar-494"},
			},
			want: []string{
				"workspace", "report-metadata", "w2", "--source", "fanout",
				"--token", "fanout_issue=#494",
				"--token", "fanout_slug=herdr-sidebar-494",
			},
		},
		{
			// An empty value is the clear rule: it must become --clear-token,
			// never a --token with an empty right-hand side.
			name: "pane clears the values it has none for", resource: "pane", id: "w2:p1",
			tokens: []MetadataToken{
				{Name: "fanout_parent", Value: "#524"},
				{Name: "fanout_pr"},
				{Name: "fanout_ci"},
			},
			want: []string{
				"pane", "report-metadata", "w2:p1", "--source", "fanout",
				"--token", "fanout_parent=#524",
				"--clear-token", "fanout_pr",
				"--clear-token", "fanout_ci",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := metadataArgs(tt.resource, tt.id, tt.tokens)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("metadataArgs(%q, %q, tokens) = %#v, want %#v", tt.resource, tt.id, got, tt.want)
			}
			for _, arg := range got {
				if arg == "--title" || arg == "--display-agent" || arg == "--state-label" {
					t.Fatalf("metadataArgs(%q, ...) writes a presentation field: %v", tt.resource, got)
				}
			}
		})
	}
}

func TestMetadataCallsSkipResourcesWithoutAPatch(t *testing.T) {
	report := MetadataReport{
		Target:     testMetadataTarget(),
		PaneTokens: []MetadataToken{{Name: "fanout_parent", Value: "#524"}},
	}
	calls := metadataCalls(report)
	if len(calls) != 1 || calls[0][0] != "pane" {
		t.Fatalf("metadataCalls(pane-only report) = %#v, want one pane call", calls)
	}
	report.WorkspaceTokens = []MetadataToken{{Name: "fanout_issue", Value: "#494"}}
	calls = metadataCalls(report)
	if len(calls) != 2 || calls[0][0] != "workspace" || calls[1][0] != "pane" {
		t.Fatalf("metadataCalls(both patches) = %#v, want workspace then pane", calls)
	}
}

func TestValidateMetadataReportFailsClosed(t *testing.T) {
	valid := []MetadataToken{{Name: "fanout_issue", Value: "#494"}}
	tests := []struct {
		name   string
		report MetadataReport
		want   string
	}{
		{
			name:   "accepts a complete report",
			report: MetadataReport{Target: testMetadataTarget(), WorkspaceTokens: valid},
		},
		{
			name:   "rejects a report with no token patch at all",
			report: MetadataReport{Target: testMetadataTarget()},
			want:   "no token patch",
		},
		{
			name: "rejects a target missing the terminal identity",
			report: MetadataReport{
				Target:     func() MetadataTarget { t := testMetadataTarget(); t.TerminalID = ""; return t }(),
				PaneTokens: valid,
			},
			want: "target is incomplete",
		},
		{
			name: "rejects a target missing worktree provenance",
			report: MetadataReport{
				Target:     func() MetadataTarget { t := testMetadataTarget(); t.CheckoutPath = ""; return t }(),
				PaneTokens: valid,
			},
			want: "worktree provenance is incomplete",
		},
		{
			name: "rejects a token name Herdr would refuse",
			report: MetadataReport{
				Target:          testMetadataTarget(),
				WorkspaceTokens: []MetadataToken{{Name: "fanout issue", Value: "#494"}},
			},
			want: "is invalid",
		},
		{
			name: "rejects the same token twice in one patch",
			report: MetadataReport{
				Target: testMetadataTarget(),
				WorkspaceTokens: []MetadataToken{
					{Name: "fanout_issue", Value: "#494"},
					{Name: "fanout_issue", Value: "#495"},
				},
			},
			want: "is repeated",
		},
		{
			// Herdr truncates past 80 characters instead of failing, so a
			// value that long would display differently from what was sent.
			name: "rejects a value Herdr would truncate",
			report: MetadataReport{
				Target:          testMetadataTarget(),
				WorkspaceTokens: []MetadataToken{{Name: "fanout_slug", Value: strings.Repeat("x", 81)}},
			},
			want: "exceeds 80 characters",
		},
		{
			name: "rejects a value Herdr would strip",
			report: MetadataReport{
				Target:          testMetadataTarget(),
				WorkspaceTokens: []MetadataToken{{Name: "fanout_slug", Value: "child\tslug"}},
			},
			want: "control-free display text",
		},
		{
			name: "rejects more tokens than one report may carry",
			report: MetadataReport{
				Target:          testMetadataTarget(),
				WorkspaceTokens: manyMetadataTokens(17),
			},
			want: "at most 16 tokens",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMetadataReport(tt.report)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("validateMetadataReport(valid) = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateMetadataReport(report) = %v, want an error containing %q", err, tt.want)
			}
		})
	}
}

func TestMetadataTargetLiveRequiresTheExactWorkspaceAndPane(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*WorkspaceObservation)
		wantHit bool
	}{
		{name: "matches the recorded workspace and pane", mutate: func(*WorkspaceObservation) {}, wantHit: true},
		{name: "rejects a relabeled workspace", mutate: func(w *WorkspaceObservation) { w.Label = "fanout-worktree-other" }},
		{name: "rejects a foreign repository", mutate: func(w *WorkspaceObservation) { w.RepoKey = "/other/.git" }},
		{name: "rejects a moved checkout", mutate: func(w *WorkspaceObservation) { w.Path = "/repo/.fanout/worktrees/other" }},
		{name: "rejects a replaced terminal", mutate: func(w *WorkspaceObservation) { w.Panes[0].TerminalID = "term-2" }},
		{name: "rejects a different pane", mutate: func(w *WorkspaceObservation) { w.Panes[0].Pane.Pane = "w2:p9" }},
		{name: "rejects a workspace with no panes", mutate: func(w *WorkspaceObservation) { w.Panes = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observed := testMetadataObservation()
			tt.mutate(&observed)
			if got := metadataTargetLive(testMetadataTarget(), []WorkspaceObservation{observed}); got != tt.wantHit {
				t.Fatalf("metadataTargetLive(target, %+v) = %t, want %t", observed, got, tt.wantHit)
			}
		})
	}
}

// TestReportBracketedMetadataRechecksTheTargetOnBothSides pins the order a
// report runs in: identity snapshot, workspace report, pane report, identity
// snapshot again.
func TestReportBracketedMetadataRechecksTheTargetOnBothSides(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	fake := newFakeHerdr(session, socket)
	fake.respond = func([]string) ([]byte, error) { return nil, nil }
	b := newTestBackend(t, session, socket, fake)
	probed, err := b.probe()
	if err != nil {
		t.Fatal(err)
	}
	if err := b.reportBracketedMetadata(t.Context(), probed, snapshotMetadataReport()); err != nil {
		t.Fatalf("reportBracketedMetadata() error = %v", err)
	}
	var reports [][]string
	snapshots := 0
	for _, call := range fake.commands[2:] {
		if commandKey(call.args) == "snapshot" {
			snapshots++
			continue
		}
		reports = append(reports, call.args)
	}
	if snapshots != 2 {
		t.Fatalf("identity snapshots = %d, want one before and one after the report", snapshots)
	}
	want := [][]string{
		{"workspace", "report-metadata", "w2", "--source", "fanout", "--token", "fanout_issue=#494"},
		{"pane", "report-metadata", "w2:p1", "--source", "fanout", "--clear-token", "fanout_pr"},
	}
	if !reflect.DeepEqual(reports, want) {
		t.Fatalf("report commands = %#v, want %#v", reports, want)
	}
}

func TestReportBracketedMetadataFailsClosedOnAnUnexpectedResponse(t *testing.T) {
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	fake := newFakeHerdr(session, socket)
	// A successful report-metadata prints nothing at all, so any output is an
	// outcome fanout cannot classify.
	fake.respond = func([]string) ([]byte, error) {
		return []byte(`{"error":{"code":"workspace_not_found","message":"gone"},"id":"cli:request"}`), nil
	}
	b := newTestBackend(t, session, socket, fake)
	probed, err := b.probe()
	if err != nil {
		t.Fatal(err)
	}
	err = b.reportBracketedMetadata(t.Context(), probed, snapshotMetadataReport())
	if err == nil || !strings.Contains(err.Error(), "unexpected response") {
		t.Fatalf("reportBracketedMetadata() = %v, want an unexpected-response error", err)
	}
}

func TestReportMetadataRejectsANilSessionAndAnEmptyTarget(t *testing.T) {
	var session *OwnedSession
	if err := session.ReportMetadata(t.Context(), snapshotMetadataReport()); err == nil {
		t.Fatal("ReportMetadata() on a nil session unexpectedly succeeded")
	}
	err := validateMetadataReport(MetadataReport{Target: MetadataTarget{}})
	if !errors.Is(err, ErrOwnedIdentityMismatch) {
		t.Fatalf("validateMetadataReport(empty target) = %v, want an identity mismatch", err)
	}
}

// snapshotMetadataReport targets the child workspace of validSnapshot().
func snapshotMetadataReport() MetadataReport {
	return MetadataReport{
		Target: MetadataTarget{
			WorkspaceID: "w2", Label: "child",
			RepoKey: "/repo/.git", RepoRoot: "/repo",
			CheckoutPath: "/repo/.fanout/worktrees/child",
			PaneID:       "w2:p1", TerminalID: "term-child",
		},
		WorkspaceTokens: []MetadataToken{{Name: "fanout_issue", Value: "#494"}},
		PaneTokens:      []MetadataToken{{Name: "fanout_pr"}},
	}
}

func testMetadataTarget() MetadataTarget {
	return MetadataTarget{
		WorkspaceID: "w2", Label: "fanout-worktree-abc",
		RepoKey: "/repo/.git", RepoRoot: "/repo",
		CheckoutPath: "/repo/.fanout/worktrees/child",
		PaneID:       "w2:p1", TerminalID: "term-1",
	}
}

func testMetadataObservation() WorkspaceObservation {
	pane := corebackend.PaneRef{Backend: corebackend.Herdr, Workspace: "w2", Pane: "w2:p1"}
	return WorkspaceObservation{
		WorkspaceID: "w2", Label: "fanout-worktree-abc",
		Path: "/repo/.fanout/worktrees/child", RepoKey: "/repo/.git", RepoRoot: "/repo",
		Pane: pane, TerminalID: "term-1", CWD: "/repo/.fanout/worktrees/child",
		Panes: []WorkspacePaneObservation{
			{Pane: pane, TerminalID: "term-1", CWD: "/repo/.fanout/worktrees/child"},
		},
	}
}

func manyMetadataTokens(count int) []MetadataToken {
	tokens := make([]MetadataToken, 0, count)
	for i := range count {
		tokens = append(tokens, MetadataToken{Name: "fanout_" + strings.Repeat("x", i+1), Value: "v"})
	}
	return tokens
}
