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
		tokens   []corebackend.MetadataToken
		want     []string
	}{
		{
			name: "workspace sets every value", resource: "workspace", id: "w2",
			tokens: []corebackend.MetadataToken{
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
			tokens: []corebackend.MetadataToken{
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
	report := corebackend.MetadataReport{
		Target:     testMetadataTarget(),
		PaneTokens: []corebackend.MetadataToken{{Name: "fanout_parent", Value: "#524"}},
	}
	calls := metadataCalls(report)
	if len(calls) != 1 || calls[0][0] != "pane" {
		t.Fatalf("metadataCalls(pane-only report) = %#v, want one pane call", calls)
	}
	report.WorkspaceTokens = []corebackend.MetadataToken{{Name: "fanout_issue", Value: "#494"}}
	calls = metadataCalls(report)
	if len(calls) != 2 || calls[0][0] != "workspace" || calls[1][0] != "pane" {
		t.Fatalf("metadataCalls(both patches) = %#v, want workspace then pane", calls)
	}
}

func TestValidateMetadataReportFailsClosed(t *testing.T) {
	valid := []corebackend.MetadataToken{{Name: "fanout_issue", Value: "#494"}}
	tests := []struct {
		name   string
		report corebackend.MetadataReport
		want   string
	}{
		{
			name:   "accepts a complete report",
			report: corebackend.MetadataReport{Target: testMetadataTarget(), WorkspaceTokens: valid},
		},
		{
			name:   "rejects a report with no token patch at all",
			report: corebackend.MetadataReport{Target: testMetadataTarget()},
			want:   "no token patch",
		},
		{
			name: "rejects a target missing the terminal identity",
			report: corebackend.MetadataReport{
				Target:     func() corebackend.MetadataTarget { t := testMetadataTarget(); t.TerminalID = ""; return t }(),
				PaneTokens: valid,
			},
			want: "target is incomplete",
		},
		{
			name: "rejects a target missing worktree provenance",
			report: corebackend.MetadataReport{
				Target:     func() corebackend.MetadataTarget { t := testMetadataTarget(); t.CheckoutPath = ""; return t }(),
				PaneTokens: valid,
			},
			want: "worktree provenance is incomplete",
		},
		{
			name: "rejects a token name Herdr would refuse",
			report: corebackend.MetadataReport{
				Target:          testMetadataTarget(),
				WorkspaceTokens: []corebackend.MetadataToken{{Name: "fanout issue", Value: "#494"}},
			},
			want: "is invalid",
		},
		{
			name: "rejects the same token twice in one patch",
			report: corebackend.MetadataReport{
				Target: testMetadataTarget(),
				WorkspaceTokens: []corebackend.MetadataToken{
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
			report: corebackend.MetadataReport{
				Target:          testMetadataTarget(),
				WorkspaceTokens: []corebackend.MetadataToken{{Name: "fanout_slug", Value: strings.Repeat("x", 81)}},
			},
			want: "exceeds 80 characters",
		},
		{
			name: "rejects a value Herdr would strip",
			report: corebackend.MetadataReport{
				Target:          testMetadataTarget(),
				WorkspaceTokens: []corebackend.MetadataToken{{Name: "fanout_slug", Value: "child\tslug"}},
			},
			want: "control-free display text",
		},
		{
			name: "rejects more tokens than one report may carry",
			report: corebackend.MetadataReport{
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
		mutate  func(*snapshotJSON)
		wantHit bool
	}{
		{name: "matches the recorded workspace and pane", mutate: func(*snapshotJSON) {}, wantHit: true},
		{
			// The launch postcondition compares checkout paths through
			// filepath.Clean; the report must not be stricter than the launch
			// it follows.
			name: "accepts a checkout path the launch would also accept",
			mutate: func(s *snapshotJSON) {
				(*s.Workspaces)[0].Worktree.CheckoutPath = "/repo/.fanout/worktrees/child/"
			},
			wantHit: true,
		},
		{
			// Herdr drops a pane record when its agent exits, so a sibling
			// child going away must not fail this target's recheck.
			name: "ignores an unrelated workspace that lost its pane",
			mutate: func(s *snapshotJSON) {
				*s.Workspaces = append(*s.Workspaces, workspaceJSON{WorkspaceID: "w3", Label: "sibling"})
			},
			wantHit: true,
		},
		{
			name:   "rejects a relabeled workspace",
			mutate: func(s *snapshotJSON) { (*s.Workspaces)[0].Label = "fanout-worktree-other" },
		},
		{
			name:   "rejects a foreign repository",
			mutate: func(s *snapshotJSON) { (*s.Workspaces)[0].Worktree.RepoKey = "/other/.git" },
		},
		{
			name: "rejects a moved checkout",
			mutate: func(s *snapshotJSON) {
				(*s.Workspaces)[0].Worktree.CheckoutPath = "/repo/.fanout/worktrees/other"
			},
		},
		{
			name:   "rejects a coordinator workspace with no worktree provenance",
			mutate: func(s *snapshotJSON) { (*s.Workspaces)[0].Worktree = nil },
		},
		{name: "rejects a replaced terminal", mutate: func(s *snapshotJSON) { (*s.Panes)[0].TerminalID = "term-2" }},
		{name: "rejects a pane that moved workspace", mutate: func(s *snapshotJSON) { (*s.Panes)[0].WorkspaceID = "w9" }},
		{name: "rejects a session without the pane", mutate: func(s *snapshotJSON) { *s.Panes = nil }},
		{name: "rejects a session without the workspace", mutate: func(s *snapshotJSON) { *s.Workspaces = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := testMetadataSnapshot()
			tt.mutate(&snapshot)
			if got := metadataTargetLive(testMetadataTarget(), snapshot); got != tt.wantHit {
				t.Fatalf("metadataTargetLive(target, snapshot) = %t, want %t", got, tt.wantHit)
			}
		})
	}
}

// TestReportBracketedMetadataRechecksTheTargetAroundEveryReport pins the
// per-mutation bracket: each report is preceded by its own identity snapshot,
// and one more closes the patch. A single bracket around both reports would let
// the pane be replaced between them.
func TestReportBracketedMetadataRechecksTheTargetAroundEveryReport(t *testing.T) {
	fake, b := newMetadataTestBackend(t)
	fake.respond = func([]string) ([]byte, error) { return nil, nil }
	probed, err := b.probe()
	if err != nil {
		t.Fatal(err)
	}
	if err := b.reportBracketedMetadata(t.Context(), probed, snapshotMetadataReport()); err != nil {
		t.Fatalf("reportBracketedMetadata() error = %v", err)
	}
	want := []string{
		"snapshot",
		"workspace report-metadata w2 --source fanout --token fanout_issue=#494",
		"snapshot",
		"pane report-metadata w2:p1 --source fanout --clear-token fanout_pr",
		"snapshot",
	}
	if got := metadataCallLog(fake); !reflect.DeepEqual(got, want) {
		t.Fatalf("call sequence = %#v, want %#v", got, want)
	}
}

// TestReportBracketedMetadataRechecksTheTargetAfterAFailedReport keeps a lost
// response classified: the mutation may have applied, so the closing recheck
// still runs and the error says how much of the patch was issued.
func TestReportBracketedMetadataRechecksTheTargetAfterAFailedReport(t *testing.T) {
	fake, b := newMetadataTestBackend(t)
	// A successful report-metadata prints nothing at all, so any output is an
	// outcome fanout cannot classify.
	fake.respond = func(args []string) ([]byte, error) {
		if args[0] != "pane" {
			return nil, nil
		}
		return []byte(`{"error":{"code":"pane_not_found","message":"gone"},"id":"cli:request"}`), nil
	}
	probed, err := b.probe()
	if err != nil {
		t.Fatal(err)
	}
	err = b.reportBracketedMetadata(t.Context(), probed, snapshotMetadataReport())
	if err == nil || !strings.Contains(err.Error(), "unexpected response") {
		t.Fatalf("reportBracketedMetadata() = %v, want an unexpected-response error", err)
	}
	if got := metadataCallLog(fake); got[len(got)-1] != "snapshot" {
		t.Fatalf("call sequence = %#v, want a closing recheck after the failed report", got)
	}
}

func newMetadataTestBackend(t *testing.T) (*fakeHerdr, *Backend) {
	t.Helper()
	const (
		session = "fanout-test"
		socket  = "/private/tmp/fanout-test/herdr.sock"
	)
	fake := newFakeHerdr(session, socket)
	return fake, newTestBackend(t, session, socket, fake)
}

// metadataCallLog renders the calls made after the probe's version and status.
func metadataCallLog(fake *fakeHerdr) []string {
	log := make([]string, 0, len(fake.commands))
	for _, call := range fake.commands[2:] {
		if commandKey(call.args) == "snapshot" {
			log = append(log, "snapshot")
			continue
		}
		log = append(log, strings.Join(call.args, " "))
	}
	return log
}

func TestReportMetadataRejectsANilSessionAndAnEmptyTarget(t *testing.T) {
	var session *OwnedSession
	if err := session.ReportMetadata(t.Context(), snapshotMetadataReport()); err == nil {
		t.Fatal("ReportMetadata() on a nil session unexpectedly succeeded")
	}
	err := validateMetadataReport(corebackend.MetadataReport{Target: corebackend.MetadataTarget{}})
	if !errors.Is(err, corebackend.ErrOwnedIdentityMismatch) {
		t.Fatalf("validateMetadataReport(empty target) = %v, want an identity mismatch", err)
	}
}

// snapshotMetadataReport targets the child workspace of validSnapshot().
func snapshotMetadataReport() corebackend.MetadataReport {
	return corebackend.MetadataReport{
		Target: corebackend.MetadataTarget{
			WorkspaceID: "w2", Label: "child",
			RepoKey: "/repo/.git", RepoRoot: "/repo",
			CheckoutPath: "/repo/.fanout/worktrees/child",
			PaneID:       "w2:p1", TerminalID: "term-child",
		},
		WorkspaceTokens: []corebackend.MetadataToken{{Name: "fanout_issue", Value: "#494"}},
		PaneTokens:      []corebackend.MetadataToken{{Name: "fanout_pr"}},
	}
}

func testMetadataTarget() corebackend.MetadataTarget {
	return corebackend.MetadataTarget{
		WorkspaceID: "w2", Label: "fanout-worktree-abc",
		RepoKey: "/repo/.git", RepoRoot: "/repo",
		CheckoutPath: "/repo/.fanout/worktrees/child",
		PaneID:       "w2:p1", TerminalID: "term-1",
	}
}

func testMetadataSnapshot() snapshotJSON {
	workspaces := []workspaceJSON{{
		WorkspaceID: "w2", Label: "fanout-worktree-abc",
		Worktree: &worktreeInfoJSON{
			RepoKey: "/repo/.git", RepoRoot: "/repo",
			CheckoutPath: "/repo/.fanout/worktrees/child",
		},
	}}
	panes := []paneJSON{{PaneID: "w2:p1", WorkspaceID: "w2", TerminalID: "term-1"}}
	return snapshotJSON{Workspaces: &workspaces, Panes: &panes}
}

func manyMetadataTokens(count int) []corebackend.MetadataToken {
	tokens := make([]corebackend.MetadataToken, 0, count)
	for i := range count {
		tokens = append(tokens, corebackend.MetadataToken{Name: "fanout_" + strings.Repeat("x", i+1), Value: "v"})
	}
	return tokens
}
