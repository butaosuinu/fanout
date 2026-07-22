package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/codexapp"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
)

func TestLoadTUIRestoreSnapshotFailsClosedWhenIdentityListingIsIncomplete(t *testing.T) {
	argsPath := installTUIIdentityTitleFailureTmuxShim(t)

	_, err := loadTUIRestoreSnapshot("fanout")
	if err == nil || !strings.Contains(err.Error(), "titles") {
		t.Fatalf("loadTUIRestoreSnapshot() error = %v, want strict title-listing error", err)
	}
	args, readErr := os.ReadFile(argsPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(args), "split-window") {
		t.Fatalf("tmux calls contain split-window after incomplete identity sweep:\n%s", args)
	}
}

func installTUIIdentityTitleFailureTmuxShim(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args.txt")
	tmuxPath := filepath.Join(dir, "tmux")
	script := `#!/bin/sh
printf '%s\n' "$@" >> "$TMUX_SHIM_ARGS"
printf '%s\n' '---' >> "$TMUX_SHIM_ARGS"
case "$4" in
*pane_current_path*) printf '%%9\t/wt/nine\n' ;;
*pane_title*) exit 7 ;;
*) exit 99 ;;
esac
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_SHIM_ARGS", argsPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsPath
}

func TestRestoreRecordedPanesRebindsLivePaneByTitle(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, ".fanout", "worktrees", "restore-api-101")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRestoreState(t, root, []state.Pane{{
		Parent:       "81",
		IssueNum:     101,
		Slug:         "restore-api-101",
		DisplayName:  "Restore API",
		PaneID:       "%old",
		Agent:        "claude",
		WorktreePath: wt,
	}})
	livePane := tmuxrun.LivePane{ID: "%new", CurrentPath: filepath.Join(wt, "internal"), Title: "Restore API"}

	report, err := restoreRecordedPanesForRootWithSnapshot(root, "fanout", "fanout", func(string) (tuiRestoreSnapshot, error) {
		return tuiRestoreSnapshot{
			Live: map[string]tmuxrun.LivePane{
				livePane.ID: livePane,
			},
			PanesByTitle: map[string][]tmuxrun.LivePane{
				"Restore API": {livePane},
			},
		}, nil
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	got := readRestoreState(t, root)
	if len(got.Panes) != 1 || got.Panes[0].PaneID != "%new" {
		t.Fatalf("state panes = %+v, want rebound pane id %%new", got.Panes)
	}
	if report.Rebound != 1 || report.Restored != 0 || report.RemovedShells != 0 {
		t.Fatalf("report = %+v, want one rebound", report)
	}
}

func TestRestoreRecordedPanesRemovesStaleShellRows(t *testing.T) {
	root := t.TempDir()
	writeRestoreState(t, root, []state.Pane{{
		Parent:       "@shell",
		IssueNum:     -1,
		Kind:         state.PaneKindShell,
		PaneID:       "%9",
		ShellKey:     "shell-1",
		DisplayName:  "terminal",
		WorktreePath: root,
	}})

	report, err := restoreRecordedPanesForRootWithSnapshot(root, "fanout", "fanout", func(string) (tuiRestoreSnapshot, error) {
		return tuiRestoreSnapshot{}, nil
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	got := readRestoreState(t, root)
	if len(got.Panes) != 0 {
		t.Fatalf("state panes = %+v, want stale shell removed", got.Panes)
	}
	if report.RemovedShells != 1 {
		t.Fatalf("RemovedShells = %d, want 1", report.RemovedShells)
	}
}

func TestRestoreRecordedPanesKeepsLiveShellWhenShellKeyMissing(t *testing.T) {
	root := t.TempDir()
	writeRestoreState(t, root, []state.Pane{{
		Parent:       "@shell",
		IssueNum:     -1,
		Kind:         state.PaneKindShell,
		PaneID:       "%9",
		ShellKey:     "shell-1",
		DisplayName:  "terminal",
		WorktreePath: root,
	}})

	report, err := restoreRecordedPanesForRootWithSnapshot(root, "fanout", "fanout", func(string) (tuiRestoreSnapshot, error) {
		return tuiRestoreSnapshot{
			Live: map[string]tmuxrun.LivePane{
				"%9": {ID: "%9", CurrentPath: root, Title: "terminal"},
			},
		}, nil
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	got := readRestoreState(t, root)
	if len(got.Panes) != 1 || got.Panes[0].PaneID != "%9" {
		t.Fatalf("state panes = %+v, want live shell row preserved", got.Panes)
	}
	if report.Skipped != 1 || report.RemovedShells != 0 {
		t.Fatalf("report = %+v, want one skipped shell and no removal", report)
	}
}

func TestRestoreRecordedPanesKeepsKeyedAgentWhenLiveKeyIsUnavailable(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, ".fanout", "worktrees", "restore-api-101")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := installRestoreTmuxAndAgentScripts(t, "claude")
	pane := state.Pane{
		Parent:       "81",
		IssueNum:     101,
		Slug:         "restore-api-101",
		DisplayName:  "Restore API",
		PaneID:       "%live",
		ShellKey:     "key-live",
		Agent:        "claude",
		WorktreePath: wt,
	}
	writeRestoreState(t, root, []state.Pane{pane})
	livePane := tmuxrun.LivePane{ID: pane.PaneID, CurrentPath: wt, Title: pane.DisplayName}

	report, err := restoreRecordedPanesForRootWithSnapshot(root, "fanout", "fanout", func(string) (tuiRestoreSnapshot, error) {
		return tuiRestoreSnapshot{
			Live:         map[string]tmuxrun.LivePane{livePane.ID: livePane},
			PanesByTitle: map[string][]tmuxrun.LivePane{livePane.Title: {livePane}},
		}, nil
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	got := readRestoreState(t, root)
	if len(got.Panes) != 1 || got.Panes[0].PaneID != pane.PaneID || got.Panes[0].ShellKey != pane.ShellKey {
		t.Fatalf("state panes = %+v, want keyed live pane preserved", got.Panes)
	}
	if report.Skipped != 1 || report.Restored != 0 || report.Rebound != 0 {
		t.Fatalf("report = %+v, want unknown identity skipped without restore", report)
	}
	logBody, readErr := os.ReadFile(logPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	if strings.Contains(string(logBody), "split-window") {
		t.Fatalf("tmux log = %q, must not split while the live key is unavailable", logBody)
	}
}

func TestRestoreRecordedPanesRemovesShellWhenLiveShellKeyDiffers(t *testing.T) {
	root := t.TempDir()
	writeRestoreState(t, root, []state.Pane{{
		Parent:       "@shell",
		IssueNum:     -1,
		Kind:         state.PaneKindShell,
		PaneID:       "%9",
		ShellKey:     "shell-1",
		DisplayName:  "terminal",
		WorktreePath: root,
	}})

	report, err := restoreRecordedPanesForRootWithSnapshot(root, "fanout", "fanout", func(string) (tuiRestoreSnapshot, error) {
		return tuiRestoreSnapshot{
			Live: map[string]tmuxrun.LivePane{
				"%9": {ID: "%9", CurrentPath: root, Title: "terminal", ShellKey: "shell-2"},
			},
		}, nil
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	got := readRestoreState(t, root)
	if len(got.Panes) != 0 {
		t.Fatalf("state panes = %+v, want reused shell row removed", got.Panes)
	}
	if report.RemovedShells != 1 {
		t.Fatalf("RemovedShells = %d, want 1", report.RemovedShells)
	}
}

func TestRestoreRecordedPanesSkipsRootWithoutStateFile(t *testing.T) {
	root := t.TempDir()
	snapshotCalled := false

	report, err := restoreRecordedPanesForRootWithSnapshot(root, "fanout", "fanout", func(string) (tuiRestoreSnapshot, error) {
		snapshotCalled = true
		return tuiRestoreSnapshot{}, nil
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if snapshotCalled {
		t.Fatal("snapshot loader was called for a root without state")
	}
	if report.Changed() {
		t.Fatalf("report = %+v, want no changes", report)
	}
	if _, err := os.Stat(filepath.Join(root, ".fanout")); !os.IsNotExist(err) {
		t.Fatalf(".fanout stat error = %v, want missing directory", err)
	}
}

func TestRestoreRecordedPanesDoesNotRecreateLivePaneWithPathMismatch(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, ".fanout", "worktrees", "restore-api-101")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRestoreState(t, root, []state.Pane{{
		Parent:       "81",
		IssueNum:     101,
		Slug:         "restore-api-101",
		DisplayName:  "Restore API",
		PaneID:       "%old",
		Agent:        "claude",
		WorktreePath: wt,
	}})

	report, err := restoreRecordedPanesForRootWithSnapshot(root, "fanout", "fanout", func(string) (tuiRestoreSnapshot, error) {
		return tuiRestoreSnapshot{
			Live: map[string]tmuxrun.LivePane{
				"%old": {ID: "%old", CurrentPath: "/tmp", Title: "Restore API"},
			},
		}, nil
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	got := readRestoreState(t, root)
	if len(got.Panes) != 1 || got.Panes[0].PaneID != "%old" {
		t.Fatalf("state panes = %+v, want original live pane id preserved", got.Panes)
	}
	if report.Skipped != 1 || report.Restored != 0 || report.Rebound != 0 {
		t.Fatalf("report = %+v, want one skipped pane and no restore", report)
	}
}

// Fixed instants for the adoption tests: the tmux server started at the
// epoch below, rows created after it hold the incarnation proof that their
// recorded pane id was never reused, and the default pane start (30s before
// the row) satisfies the per-pane provenance window.
var (
	restoreAdoptServerStart   = time.Unix(1_700_000_000, 0)
	restoreAdoptCreatedAfter  = time.Unix(1_700_000_060, 0).UTC().Format(time.RFC3339)
	restoreAdoptCreatedBefore = time.Unix(1_699_999_940, 0).UTC().Format(time.RFC3339)
	restoreAdoptPaneStart     = time.Unix(1_700_000_030, 0)
)

func stubRestorePaneStartTime(t *testing.T, fn func(string) (time.Time, error)) {
	t.Helper()
	original := restorePaneStartTime
	restorePaneStartTime = fn
	t.Cleanup(func() { restorePaneStartTime = original })
}

func stubDefaultRestorePaneStartTime(t *testing.T) {
	t.Helper()
	stubRestorePaneStartTime(t, func(string) (time.Time, error) { return restoreAdoptPaneStart, nil })
}

// TestRestoreRecordedPanesAdoptsLegacyLivePaneKey pins the migration for rows
// recorded before per-pane liveness keys existed: a live keyless agent pane
// created within this tmux server's lifetime and carrying the launch-time
// fanout markers is stamped once and the key is persisted so lifecycle close
// can verify it.
func TestRestoreRecordedPanesAdoptsLegacyLivePaneKey(t *testing.T) {
	tests := []struct {
		name        string
		pane        func(root, wt string) state.Pane
		live        func(root, wt string) tmuxrun.LivePane
		paneStart   func() (time.Time, error)
		wantAdopted bool
		wantStamps  int
		wantKey     string
		wantSkipped int
	}{
		{
			name: "legacy issue agent row alive by path gets a key",
			pane: func(_, wt string) state.Pane {
				return state.Pane{Parent: "81", IssueNum: 101, Slug: "restore-api-101", DisplayName: "Restore API", PaneID: "%live", Agent: "claude", WorktreePath: wt, CreatedAt: restoreAdoptCreatedAfter}
			},
			live: func(_, wt string) tmuxrun.LivePane {
				return tmuxrun.LivePane{ID: "%live", CurrentPath: filepath.Join(wt, "internal"), Title: "Restore API", WorktreePath: wt, Label: "#81 · Restore API"}
			},
			wantAdopted: true,
			wantStamps:  1,
		},
		{
			name: "legacy manual worktree row gets a key",
			pane: func(_, wt string) state.Pane {
				return state.Pane{Parent: "@manual", IssueNum: -8, Slug: "manual-8-main-pane", DisplayName: "manual-8-main-pane", PaneID: "%live", Agent: "codex", PlanMode: true, WorktreePath: wt, CreatedAt: restoreAdoptCreatedAfter}
			},
			live: func(_, wt string) tmuxrun.LivePane {
				return tmuxrun.LivePane{ID: "%live", CurrentPath: wt, Title: "manual-8-main-pane", WorktreePath: wt, Label: "@manual · manual-8-main-pane"}
			},
			wantAdopted: true,
			wantStamps:  1,
		},
		{
			// A live key on a keyless row is an earlier adoption whose state
			// save failed or crashed: re-associate it instead of restamping.
			name: "orphaned live key is re-associated without a restamp",
			pane: func(_, wt string) state.Pane {
				return state.Pane{Parent: "81", IssueNum: 101, DisplayName: "Restore API", PaneID: "%live", Agent: "claude", WorktreePath: wt, CreatedAt: restoreAdoptCreatedAfter}
			},
			live: func(_, wt string) tmuxrun.LivePane {
				return tmuxrun.LivePane{ID: "%live", CurrentPath: wt, Title: "Restore API", ShellKey: "key-orphan", WorktreePath: wt, Label: "#81 · Restore API"}
			},
			wantAdopted: true,
			wantKey:     "key-orphan",
		},
		{
			// The recorded pane id predates this tmux server, so it may have
			// been reused by an unrelated pane: the incarnation proof fails.
			name: "row created before this tmux server stays legacy",
			pane: func(_, wt string) state.Pane {
				return state.Pane{Parent: "81", IssueNum: 101, DisplayName: "Restore API", PaneID: "%live", Agent: "claude", WorktreePath: wt, CreatedAt: restoreAdoptCreatedBefore}
			},
			live: func(_, wt string) tmuxrun.LivePane {
				return tmuxrun.LivePane{ID: "%live", CurrentPath: wt, Title: "Restore API", WorktreePath: wt, Label: "#81 · Restore API"}
			},
		},
		{
			name: "row without a created timestamp stays legacy",
			pane: func(_, wt string) state.Pane {
				return state.Pane{Parent: "81", IssueNum: 101, DisplayName: "Restore API", PaneID: "%live", Agent: "claude", WorktreePath: wt}
			},
			live: func(_, wt string) tmuxrun.LivePane {
				return tmuxrun.LivePane{ID: "%live", CurrentPath: wt, Title: "Restore API", WorktreePath: wt, Label: "#81 · Restore API"}
			},
		},
		{
			// A pane whose root process started an hour before the row was
			// recorded is not the pane this row launched (id coincidence
			// from another server generation or socket).
			name: "live pane whose process predates the row stays legacy",
			pane: func(_, wt string) state.Pane {
				return state.Pane{Parent: "81", IssueNum: 101, DisplayName: "Restore API", PaneID: "%live", Agent: "claude", WorktreePath: wt, CreatedAt: restoreAdoptCreatedAfter}
			},
			live: func(_, wt string) tmuxrun.LivePane {
				return tmuxrun.LivePane{ID: "%live", CurrentPath: wt, Title: "Restore API", WorktreePath: wt, Label: "#81 · Restore API"}
			},
			paneStart: func() (time.Time, error) { return restoreAdoptPaneStart.Add(-time.Hour), nil },
		},
		{
			name: "pane start lookup failure stays legacy",
			pane: func(_, wt string) state.Pane {
				return state.Pane{Parent: "81", IssueNum: 101, DisplayName: "Restore API", PaneID: "%live", Agent: "claude", WorktreePath: wt, CreatedAt: restoreAdoptCreatedAfter}
			},
			live: func(_, wt string) tmuxrun.LivePane {
				return tmuxrun.LivePane{ID: "%live", CurrentPath: wt, Title: "Restore API", WorktreePath: wt, Label: "#81 · Restore API"}
			},
			paneStart: func() (time.Time, error) { return time.Time{}, errors.New("pane gone") },
		},
		{
			name: "live pane without the fanout worktree marker stays legacy",
			pane: func(_, wt string) state.Pane {
				return state.Pane{Parent: "81", IssueNum: 101, DisplayName: "Restore API", PaneID: "%live", Agent: "claude", WorktreePath: wt, CreatedAt: restoreAdoptCreatedAfter}
			},
			live: func(_, wt string) tmuxrun.LivePane {
				return tmuxrun.LivePane{ID: "%live", CurrentPath: wt, Title: "Restore API", Label: "#81 · Restore API"}
			},
		},
		{
			// An attached agent shares the worktree but never this row's label.
			name: "live pane with another session's label stays legacy",
			pane: func(_, wt string) state.Pane {
				return state.Pane{Parent: "81", IssueNum: 101, DisplayName: "Restore API", PaneID: "%live", Agent: "claude", WorktreePath: wt, CreatedAt: restoreAdoptCreatedAfter}
			},
			live: func(_, wt string) tmuxrun.LivePane {
				return tmuxrun.LivePane{ID: "%live", CurrentPath: wt, Title: "reviewer", WorktreePath: wt, Label: "@manual · attached-reviewer"}
			},
		},
		{
			name: "keyed row is not restamped",
			pane: func(_, wt string) state.Pane {
				return state.Pane{Parent: "81", IssueNum: 101, DisplayName: "Restore API", PaneID: "%live", ShellKey: "key-1", Agent: "claude", WorktreePath: wt, CreatedAt: restoreAdoptCreatedAfter}
			},
			live: func(_, wt string) tmuxrun.LivePane {
				return tmuxrun.LivePane{ID: "%live", CurrentPath: wt, Title: "Restore API", ShellKey: "key-1", WorktreePath: wt}
			},
		},
		{
			name: "keyless shell row is skipped, not adopted",
			pane: func(root, _ string) state.Pane {
				return state.Pane{Parent: "@shell", IssueNum: -1, Kind: state.PaneKindShell, PaneID: "%live", DisplayName: "terminal", WorktreePath: root, CreatedAt: restoreAdoptCreatedAfter}
			},
			live: func(root, _ string) tmuxrun.LivePane {
				return tmuxrun.LivePane{ID: "%live", CurrentPath: root, Title: "terminal", WorktreePath: root}
			},
			wantSkipped: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			wt := filepath.Join(root, ".fanout", "worktrees", "restore-api-101")
			if err := os.MkdirAll(wt, 0o755); err != nil {
				t.Fatal(err)
			}
			var stamped []string
			stubRestorePaneOps(t, func(paneID, key string) error {
				stamped = append(stamped, paneID+"\x00"+key)
				return nil
			}, nil, nil, nil)
			startFn := tt.paneStart
			if startFn == nil {
				startFn = func() (time.Time, error) { return restoreAdoptPaneStart, nil }
			}
			stubRestorePaneStartTime(t, func(string) (time.Time, error) { return startFn() })
			original := tt.pane(root, wt)
			writeRestoreState(t, root, []state.Pane{original})
			livePane := tt.live(root, wt)

			report, err := restoreRecordedPanesForRootWithSnapshot(root, "fanout", "fanout", func(string) (tuiRestoreSnapshot, error) {
				return tuiRestoreSnapshot{Live: map[string]tmuxrun.LivePane{livePane.ID: livePane}, ServerStart: restoreAdoptServerStart}, nil
			}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}

			got := readRestoreState(t, root)
			if len(got.Panes) != 1 || got.Panes[0].PaneID != original.PaneID {
				t.Fatalf("state panes = %+v, want the original live row preserved", got.Panes)
			}
			if report.Changed() {
				t.Fatalf("report = %+v, adoption must not trigger a layout re-apply", report)
			}
			if report.Skipped != tt.wantSkipped || report.Restored != 0 || report.Rebound != 0 {
				t.Fatalf("report = %+v, want skipped=%d and no restore", report, tt.wantSkipped)
			}
			if !tt.wantAdopted {
				if report.Adopted != 0 || len(stamped) != 0 || got.Panes[0].ShellKey != original.ShellKey {
					t.Fatalf("restoreRecordedPanesForRoot() = (adopted=%d, stamps=%v, key=%q), want untouched row", report.Adopted, stamped, got.Panes[0].ShellKey)
				}
				return
			}
			if report.Adopted != 1 {
				t.Fatalf("Adopted = %d, want 1", report.Adopted)
			}
			key := got.Panes[0].ShellKey
			if tt.wantKey != "" {
				if key != tt.wantKey {
					t.Fatalf("adopted shellKey = %q, want re-associated key %q", key, tt.wantKey)
				}
			} else if !strings.HasPrefix(key, "shell-") {
				t.Fatalf("adopted shellKey = %q, want generated liveness key", key)
			}
			if len(stamped) != tt.wantStamps {
				t.Fatalf("liveness stamps = %v, want %d stamp(s)", stamped, tt.wantStamps)
			}
			if tt.wantStamps == 1 && stamped[0] != original.PaneID+"\x00"+key {
				t.Fatalf("liveness stamps = %v, want one stamp of %q on %s", stamped, key, original.PaneID)
			}
		})
	}
}

// Repeated attaches of one agent to one worktree share a DisplayName, so two
// keyless rows can claim the same pane id with identical markers. Neither may
// be adopted: stamping the first would let its close kill the other session.
func TestRestoreRecordedPanesDoesNotAdoptAmbiguousTwinRows(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, ".fanout", "worktrees", "restore-api-101")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	stamps := 0
	stubRestorePaneOps(t, func(string, string) error {
		stamps++
		return nil
	}, nil, nil, nil)
	stubDefaultRestorePaneStartTime(t)
	twin := state.Pane{
		Parent:       "@manual",
		Kind:         state.PaneKindAttachedAgent,
		DisplayName:  "claude for restore-api-101",
		PaneID:       "%live",
		Agent:        "claude",
		WorktreePath: wt,
		CreatedAt:    restoreAdoptCreatedAfter,
	}
	first, second := twin, twin
	first.IssueNum = -4
	second.IssueNum = -6
	writeRestoreState(t, root, []state.Pane{first, second})

	report, err := restoreRecordedPanesForRootWithSnapshot(root, "fanout", "fanout", func(string) (tuiRestoreSnapshot, error) {
		return tuiRestoreSnapshot{Live: map[string]tmuxrun.LivePane{
			"%live": {ID: "%live", CurrentPath: wt, Title: "claude for restore-api-101", WorktreePath: wt, Label: "@manual · claude for restore-api-101"},
		}, ServerStart: restoreAdoptServerStart}, nil
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	got := readRestoreState(t, root)
	if len(got.Panes) != 2 || got.Panes[0].ShellKey != "" || got.Panes[1].ShellKey != "" {
		t.Fatalf("state panes = %+v, want both ambiguous twin rows left keyless", got.Panes)
	}
	if report.Adopted != 0 || stamps != 0 {
		t.Fatalf("report = %+v (stamps=%d), want ambiguous adoption declined", report, stamps)
	}
}

// Claimants live in every restore root's store, not just the one being
// restored: a sibling root's row recording the same pane id or the same
// liveness key must block adoption there too.
func TestRestoreRecordedPanesAdoptionChecksClaimantsAcrossRoots(t *testing.T) {
	tests := []struct {
		name    string
		sibling state.Pane
		liveKey string
	}{
		{
			name:    "pane id claimed by a sibling root's row",
			sibling: state.Pane{Parent: "90", IssueNum: 201, DisplayName: "Sibling API", PaneID: "%live", ShellKey: "key-sibling", Agent: "claude"},
		},
		{
			name:    "orphan key recorded by a sibling root's row",
			sibling: state.Pane{Parent: "90", IssueNum: 201, DisplayName: "Sibling API", PaneID: "%elsewhere", ShellKey: "key-orphan", Agent: "claude"},
			liveKey: "key-orphan",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			sibling := t.TempDir()
			wt := filepath.Join(root, ".fanout", "worktrees", "restore-api-101")
			if err := os.MkdirAll(wt, 0o755); err != nil {
				t.Fatal(err)
			}
			stamps := 0
			stubRestorePaneOps(t, func(string, string) error {
				stamps++
				return nil
			}, nil, nil, nil)
			stubDefaultRestorePaneStartTime(t)
			writeRestoreState(t, root, []state.Pane{{
				Parent:       "81",
				IssueNum:     101,
				DisplayName:  "Restore API",
				PaneID:       "%live",
				Agent:        "claude",
				WorktreePath: wt,
				CreatedAt:    restoreAdoptCreatedAfter,
			}})
			siblingPane := tt.sibling
			siblingPane.WorktreePath = sibling
			writeRestoreState(t, sibling, []state.Pane{siblingPane})
			claims := collectRestoreClaimants([]string{root, sibling})

			report, err := restoreRecordedPanesForRootWithSnapshot(root, "fanout", "fanout", func(string) (tuiRestoreSnapshot, error) {
				return tuiRestoreSnapshot{Live: map[string]tmuxrun.LivePane{
					"%live": {ID: "%live", CurrentPath: wt, Title: "Restore API", ShellKey: tt.liveKey, WorktreePath: wt, Label: "#81 · Restore API"},
				}, ServerStart: restoreAdoptServerStart}, nil
			}, nil, claims)
			if err != nil {
				t.Fatal(err)
			}

			got := readRestoreState(t, root)
			if len(got.Panes) != 1 || got.Panes[0].ShellKey != "" {
				t.Fatalf("state panes = %+v, want cross-root claimed row left keyless", got.Panes)
			}
			if report.Adopted != 0 || stamps != 0 {
				t.Fatalf("report = %+v (stamps=%d), want cross-root adoption declined", report, stamps)
			}
		})
	}
}

// A live key already recorded by another row proves the pane belongs to that
// row; absorbing it would let one close kill the other row's pane.
func TestRestoreRecordedPanesDoesNotAbsorbKeyClaimedByAnotherRow(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, ".fanout", "worktrees", "restore-api-101")
	otherWt := filepath.Join(root, ".fanout", "worktrees", "restore-api-102")
	for _, dir := range []string{wt, otherWt} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stamps := 0
	stubRestorePaneOps(t, func(string, string) error {
		stamps++
		return nil
	}, nil, nil, nil)
	stubDefaultRestorePaneStartTime(t)
	writeRestoreState(t, root, []state.Pane{
		{Parent: "81", IssueNum: 101, DisplayName: "Restore API", PaneID: "%live", Agent: "claude", WorktreePath: wt, CreatedAt: restoreAdoptCreatedAfter},
		{Parent: "81", IssueNum: 102, DisplayName: "Other API", PaneID: "%other", ShellKey: "key-claimed", Agent: "claude", WorktreePath: otherWt, CreatedAt: restoreAdoptCreatedAfter},
	})

	report, err := restoreRecordedPanesForRootWithSnapshot(root, "fanout", "fanout", func(string) (tuiRestoreSnapshot, error) {
		return tuiRestoreSnapshot{Live: map[string]tmuxrun.LivePane{
			"%live":  {ID: "%live", CurrentPath: wt, Title: "Restore API", ShellKey: "key-claimed", WorktreePath: wt, Label: "#81 · Restore API"},
			"%other": {ID: "%other", CurrentPath: otherWt, Title: "Other API", ShellKey: "key-claimed", WorktreePath: otherWt, Label: "#81 · Other API"},
		}, ServerStart: restoreAdoptServerStart}, nil
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	got := readRestoreState(t, root)
	if len(got.Panes) != 2 || got.Panes[0].ShellKey != "" || got.Panes[1].ShellKey != "key-claimed" {
		t.Fatalf("state panes = %+v, want claimed key left with its own row", got.Panes)
	}
	if report.Adopted != 0 || stamps != 0 {
		t.Fatalf("report = %+v (stamps=%d), want no adoption of a claimed key", report, stamps)
	}
}

func TestRestoreRecordedPanesAdoptionStampFailureKeepsRowLegacy(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, ".fanout", "worktrees", "restore-api-101")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	stubRestorePaneOps(t, func(string, string) error { return errors.New("stamp failed") }, nil, nil, nil)
	stubDefaultRestorePaneStartTime(t)
	writeRestoreState(t, root, []state.Pane{{
		Parent:       "81",
		IssueNum:     101,
		DisplayName:  "Restore API",
		PaneID:       "%live",
		Agent:        "claude",
		WorktreePath: wt,
		CreatedAt:    restoreAdoptCreatedAfter,
	}})
	livePane := tmuxrun.LivePane{ID: "%live", CurrentPath: wt, Title: "Restore API", WorktreePath: wt, Label: "#81 · Restore API"}

	report, err := restoreRecordedPanesForRootWithSnapshot(root, "fanout", "fanout", func(string) (tuiRestoreSnapshot, error) {
		return tuiRestoreSnapshot{Live: map[string]tmuxrun.LivePane{livePane.ID: livePane}, ServerStart: restoreAdoptServerStart}, nil
	}, nil, nil)

	if err == nil || !strings.Contains(err.Error(), "adopt legacy pane") {
		t.Fatalf("restore error = %v, want adopt legacy pane failure", err)
	}
	got := readRestoreState(t, root)
	if len(got.Panes) != 1 || got.Panes[0].PaneID != "%live" || got.Panes[0].ShellKey != "" {
		t.Fatalf("state panes = %+v, want legacy row unchanged", got.Panes)
	}
	if report.Adopted != 0 || report.Restored != 0 || report.Changed() {
		t.Fatalf("report = %+v, want no adoption after stamp failure", report)
	}
}

// TestRestoreRecordedPanesAdoptsLegacyPaneClaimedByPreclaim pins the adoption
// point before the dedupe skip: preclaimed live issue rows never reach the
// alive branch, so adopting later would silently strand them keyless.
func TestRestoreRecordedPanesAdoptsLegacyPaneClaimedByPreclaim(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, ".fanout", "worktrees", "restore-api-101")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	stamps := 0
	stubRestorePaneOps(t, func(string, string) error {
		stamps++
		return nil
	}, nil, nil, nil)
	stubDefaultRestorePaneStartTime(t)
	writeRestoreState(t, root, []state.Pane{{
		Parent:       "81",
		IssueNum:     101,
		DisplayName:  "Restore API",
		PaneID:       "%live",
		Agent:        "claude",
		WorktreePath: wt,
		CreatedAt:    restoreAdoptCreatedAfter,
	}})
	claimed := map[string]bool{"issue\x0081\x00101": true}
	livePane := tmuxrun.LivePane{ID: "%live", CurrentPath: wt, Title: "Restore API", WorktreePath: wt, Label: "#81 · Restore API"}

	report, err := restoreRecordedPanesForRootWithSnapshot(root, "fanout", "fanout", func(string) (tuiRestoreSnapshot, error) {
		return tuiRestoreSnapshot{Live: map[string]tmuxrun.LivePane{livePane.ID: livePane}, ServerStart: restoreAdoptServerStart}, nil
	}, claimed, nil)
	if err != nil {
		t.Fatal(err)
	}

	got := readRestoreState(t, root)
	if len(got.Panes) != 1 || !strings.HasPrefix(got.Panes[0].ShellKey, "shell-") {
		t.Fatalf("state panes = %+v, want preclaimed legacy row adopted", got.Panes)
	}
	if report.Adopted != 1 || report.Skipped != 1 || stamps != 1 {
		t.Fatalf("report = %+v (stamps=%d), want adoption before the dedupe skip", report, stamps)
	}
}

func TestRestoreRecordedPanesAdoptionIsIdempotent(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, ".fanout", "worktrees", "restore-api-101")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	stamps := 0
	stubRestorePaneOps(t, func(string, string) error {
		stamps++
		return nil
	}, nil, nil, nil)
	stubDefaultRestorePaneStartTime(t)
	writeRestoreState(t, root, []state.Pane{{
		Parent:       "81",
		IssueNum:     101,
		DisplayName:  "Restore API",
		PaneID:       "%live",
		Agent:        "claude",
		WorktreePath: wt,
		CreatedAt:    restoreAdoptCreatedAfter,
	}})

	if _, err := restoreRecordedPanesForRootWithSnapshot(root, "fanout", "fanout", func(string) (tuiRestoreSnapshot, error) {
		return tuiRestoreSnapshot{Live: map[string]tmuxrun.LivePane{"%live": {ID: "%live", CurrentPath: wt, Title: "Restore API", WorktreePath: wt, Label: "#81 · Restore API"}}, ServerStart: restoreAdoptServerStart}, nil
	}, nil, nil); err != nil {
		t.Fatal(err)
	}
	key := readRestoreState(t, root).Panes[0].ShellKey

	report, err := restoreRecordedPanesForRootWithSnapshot(root, "fanout", "fanout", func(string) (tuiRestoreSnapshot, error) {
		return tuiRestoreSnapshot{Live: map[string]tmuxrun.LivePane{"%live": {ID: "%live", CurrentPath: wt, Title: "Restore API", ShellKey: key, WorktreePath: wt, Label: "#81 · Restore API"}}, ServerStart: restoreAdoptServerStart}, nil
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if got := readRestoreState(t, root).Panes[0].ShellKey; got != key {
		t.Fatalf("second restore shellKey = %q, want first adopted key %q kept", got, key)
	}
	if report.Adopted != 0 || stamps != 1 {
		t.Fatalf("second restore report = %+v (stamps=%d), want no re-adoption", report, stamps)
	}
}

func TestRestoreRecordedPanesSkipsHerdrRowWithoutMutation(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, ".fanout", "worktrees", "herdr-child")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := installRestoreTmuxAndAgentScripts(t, "codex")
	writeRestoreState(t, root, []state.Pane{{
		Parent:           "423",
		IssueNum:         425,
		Backend:          backend.Herdr,
		PaneID:           "w1:p1",
		HerdrWorkspaceID: "w1",
		HerdrAgentID:     "fanout-child",
		HerdrSession:     "fanout-test",
		HerdrSocketPath:  "/private/tmp/fanout-test/herdr.sock",
		Agent:            "codex",
		WorktreePath:     wt,
	}})
	statePath := state.Path(root)
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}

	report, err := restoreRecordedPanesForRootWithSnapshot(root, "fanout", "fanout", func(string) (tuiRestoreSnapshot, error) {
		return tuiRestoreSnapshot{}, nil
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Skipped != 1 || report.Changed() {
		t.Fatalf("report = %+v, want one unchanged skipped row", report)
	}
	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("herdr restore changed state bytes:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
	if logBody, err := os.ReadFile(logPath); err == nil {
		if len(logBody) != 0 {
			t.Fatalf("herdr restore invoked tmux: %q", logBody)
		}
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestRestoreRecordedPanesSkipsDuplicateIssueClaimedByAnotherRoot(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, ".fanout", "worktrees", "restore-api-101")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRestoreState(t, root, []state.Pane{{
		Parent:       "081",
		IssueNum:     101,
		Slug:         "restore-api-101",
		DisplayName:  "Restore API",
		PaneID:       "%old",
		Agent:        "claude",
		WorktreePath: wt,
	}})
	claimed := map[string]bool{"issue\x0081\x00101": true}

	report, err := restoreRecordedPanesForRootWithSnapshot(root, "fanout", "fanout", func(string) (tuiRestoreSnapshot, error) {
		return tuiRestoreSnapshot{}, nil
	}, claimed, nil)
	if err != nil {
		t.Fatal(err)
	}

	got := readRestoreState(t, root)
	if len(got.Panes) != 1 || got.Panes[0].PaneID != "%old" {
		t.Fatalf("state panes = %+v, want duplicate row left unchanged", got.Panes)
	}
	if report.Skipped != 1 || report.Restored != 0 {
		t.Fatalf("report = %+v, want duplicate skipped without restore", report)
	}
}

func TestPreclaimLiveRestoreIdentitiesClaimsLiveSibling(t *testing.T) {
	root := t.TempDir()
	sibling := t.TempDir()
	wt := filepath.Join(sibling, ".fanout", "worktrees", "restore-api-101")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRestoreState(t, sibling, []state.Pane{{
		Parent:       "081",
		IssueNum:     101,
		Slug:         "restore-api-101",
		DisplayName:  "Restore API",
		PaneID:       "%live",
		Agent:        "claude",
		WorktreePath: wt,
	}})

	claimed := preclaimLiveRestoreIdentities([]string{root, sibling}, tuiRestoreSnapshot{
		Live: map[string]tmuxrun.LivePane{
			"%live": {ID: "%live", CurrentPath: filepath.Join(wt, "subdir"), Title: "Restore API"},
		},
	})

	if !claimed["issue\x0081\x00101"] {
		t.Fatalf("claimed = %#v, want live sibling issue claimed", claimed)
	}
}

func TestPreclaimLiveRestoreIdentitiesClaimsUnknownKeyedSibling(t *testing.T) {
	sibling := t.TempDir()
	wt := filepath.Join(sibling, ".fanout", "worktrees", "restore-api-102")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRestoreState(t, sibling, []state.Pane{{
		Parent:       "82",
		IssueNum:     102,
		PaneID:       "%live",
		ShellKey:     "key-live",
		Agent:        "codex",
		WorktreePath: wt,
	}})

	claimed := preclaimLiveRestoreIdentities([]string{sibling}, tuiRestoreSnapshot{
		Live: map[string]tmuxrun.LivePane{
			"%live": {ID: "%live", CurrentPath: wt},
		},
	})

	if !claimed["issue\x0082\x00102"] {
		t.Fatalf("claimed = %#v, want unknown keyed sibling identity claimed", claimed)
	}
}

func TestRestoreRecordedPanesRecreatesMissingAgentPaneWithResumeCommand(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, ".fanout", "worktrees", "restore-api-101")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := installRestoreTmuxAndAgentScripts(t, "claude")
	writeRestoreState(t, root, []state.Pane{{
		Parent:       "81",
		IssueNum:     101,
		Slug:         "restore-api-101",
		DisplayName:  "Restore API",
		PaneID:       "%old",
		Agent:        "claude",
		WorktreePath: wt,
	}})

	report, err := restoreRecordedPanesForRootWithSnapshot(root, "fanout", "fanout", func(string) (tuiRestoreSnapshot, error) {
		return tuiRestoreSnapshot{}, nil
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	got := readRestoreState(t, root)
	if len(got.Panes) != 1 || got.Panes[0].PaneID != "%restored" || got.Panes[0].AgentStatus != "running" {
		t.Fatalf("state panes = %+v, want restored running pane %%restored", got.Panes)
	}
	if !strings.HasPrefix(got.Panes[0].ShellKey, "shell-") {
		t.Fatalf("restored legacy pane shellKey = %q, want generated liveness key", got.Panes[0].ShellKey)
	}
	if report.Restored != 1 {
		t.Fatalf("Restored = %d, want 1", report.Restored)
	}
	logBody, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logBody), "split-window") || !strings.Contains(string(logBody), "--continue") {
		t.Fatalf("tmux log = %q, want split-window with claude --continue", string(logBody))
	}
	if !strings.Contains(string(logBody), "--settings") {
		t.Fatalf("tmux log = %q, want restored claude command to carry the agent-state hook --settings", string(logBody))
	}
	if !strings.Contains(string(logBody), "@fanout_worktree_path "+wt) {
		t.Fatalf("tmux log = %q, want restored pane worktree option %q", string(logBody), wt)
	}
	if !strings.Contains(string(logBody), "@fanout_shell_key "+got.Panes[0].ShellKey) {
		t.Fatalf("tmux log = %q, want generated liveness key stamp", string(logBody))
	}
}

func TestRestoreTracksPaneWhenLivenessStampAndFreshCloseFail(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, ".fanout", "worktrees", "restore-api-103")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	installRestoreTmuxAndAgentScripts(t, "claude")
	stubRestorePaneOps(t,
		func(string, string) error { return errors.New("stamp failed") },
		func(string) error { return errors.New("pane still live") },
		nil,
		nil,
	)
	writeRestoreState(t, root, []state.Pane{{
		Parent:       "81",
		IssueNum:     103,
		DisplayName:  "Restore API",
		PaneID:       "%old",
		ShellKey:     "key-old",
		Agent:        "claude",
		WorktreePath: wt,
	}})

	report, err := restoreRecordedPanesForRootWithSnapshot(root, "fanout", "fanout", func(string) (tuiRestoreSnapshot, error) {
		return tuiRestoreSnapshot{}, nil
	}, nil, nil)

	if err == nil || !strings.Contains(err.Error(), "pane still live") {
		t.Fatalf("restore error = %v, want failed fresh close", err)
	}
	store := readRestoreState(t, root)
	if len(store.Panes) != 1 || store.Panes[0].PaneID != "%restored" || store.Panes[0].ShellKey != "key-old" {
		t.Fatalf("state panes = %+v, want failed restored pane tracked as %%restored", store.Panes)
	}
	if report.Skipped != 1 || report.Restored != 0 || report.Tracked != 1 {
		t.Fatalf("report = %+v, want failed restore skipped with recovery row", report)
	}
}

func TestRestoreTracksPaneWhenCodexStartupAndOwnedCloseFail(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, ".fanout", "worktrees", "restore-plan-104")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	installRestoreTmuxAndAgentScripts(t, "codex")
	stubRestorePaneOps(t,
		nil,
		nil,
		func(string, time.Duration) (codexapp.Status, error) {
			return codexapp.Status{}, errors.New("startup failed")
		},
		func(string, string, string) (tmuxrun.ClosePaneResult, error) {
			return tmuxrun.ClosePaneResult{Status: tmuxrun.ClosePaneFailed}, errors.New("pane still live")
		},
	)
	writeRestoreState(t, root, []state.Pane{{
		Parent:        "81",
		IssueNum:      104,
		DisplayName:   "Restore Plan",
		PaneID:        "%old",
		ShellKey:      "key-plan",
		Agent:         "codex",
		WorktreePath:  wt,
		PlanMode:      true,
		CodexThreadID: "thread-104",
	}})

	report, err := restoreRecordedPanesForRootWithSnapshot(root, "fanout", "fanout", func(string) (tuiRestoreSnapshot, error) {
		return tuiRestoreSnapshot{}, nil
	}, nil, nil)

	if err == nil || !strings.Contains(err.Error(), "pane still live") {
		t.Fatalf("restore error = %v, want failed owned close", err)
	}
	store := readRestoreState(t, root)
	if len(store.Panes) != 1 || store.Panes[0].PaneID != "%restored" || store.Panes[0].ShellKey != "key-plan" {
		t.Fatalf("state panes = %+v, want failed Codex pane tracked as %%restored", store.Panes)
	}
	if report.Skipped != 1 || report.Restored != 0 || report.Tracked != 1 {
		t.Fatalf("report = %+v, want failed Codex restore skipped with recovery row", report)
	}
}

func TestRestoreAgentCommandUsesSavedCodexCoordinatorPlanThread(t *testing.T) {
	root := t.TempDir()
	installRestoreAgentScript(t, "codex")

	command, statusPath, err := restoreAgentCommand(state.Pane{
		Parent:         panelaunch.ManualParentRef,
		IssueNum:       -7,
		Slug:           "plan-prompt-7",
		Agent:          "codex",
		PlanMode:       true,
		CodexThreadID:  "thread-7",
		CodexSessionID: "session-7",
	}, root, "fanout")
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"__codex-plan-tui", "--resume-thread-id", "thread-7", "--resume-session-id", "session-7", "--status-file"} {
		if !strings.Contains(command, want) {
			t.Fatalf("command = %q, missing %q", command, want)
		}
	}
	if statusPath == "" || !strings.Contains(statusPath, "fanout-codex-plan-") {
		t.Fatalf("statusPath = %q, want generated codex plan status path", statusPath)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if wantPrefix := "FANOUT_BIN=" + agent.ShellQuote(executable) + " "; !strings.HasPrefix(command, wantPrefix) {
		t.Fatalf("command = %q, want prefix %q", command, wantPrefix)
	}
}

func TestRestoreAgentCommandUsesGenericResumeForNonCodexPlanPane(t *testing.T) {
	for _, tc := range []struct {
		agent string
		want  string
	}{
		{agent: "claude", want: "--continue"},
		{agent: "opencode", want: "--continue"},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			installRestoreAgentScript(t, tc.agent)
			command, statusPath, err := restoreAgentCommand(state.Pane{
				Agent:    tc.agent,
				PlanMode: true,
			}, t.TempDir(), "fanout")
			if err != nil {
				t.Fatal(err)
			}
			if statusPath != "" {
				t.Fatalf("statusPath = %q, want empty generic resume path", statusPath)
			}
			if !strings.Contains(command, tc.want) {
				t.Fatalf("command = %q, want %q", command, tc.want)
			}
			for _, unwanted := range []string{"__codex-plan-tui", "--permission-mode", "--agent plan"} {
				if strings.Contains(command, unwanted) {
					t.Fatalf("command = %q, unexpectedly contains %q", command, unwanted)
				}
			}
		})
	}
}

func TestRestoreAgentCommandPinsFanoutBinaryForNormalAgent(t *testing.T) {
	installRestoreAgentScript(t, "claude")
	command, statusPath, err := restoreAgentCommand(state.Pane{Agent: "claude"}, t.TempDir(), "fanout")
	if err != nil {
		t.Fatal(err)
	}
	if statusPath != "" {
		t.Fatalf("statusPath = %q, want empty", statusPath)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if wantPrefix := "FANOUT_BIN=" + agent.ShellQuote(executable) + " "; !strings.HasPrefix(command, wantPrefix) {
		t.Fatalf("command = %q, want prefix %q", command, wantPrefix)
	}
}

func TestRestoreAgentCommandRejectsCodexPlanWithoutThread(t *testing.T) {
	_, _, err := restoreAgentCommand(state.Pane{
		Agent:       "codex",
		PlanMode:    true,
		DisplayName: "Plan pane",
	}, t.TempDir(), "fanout")

	if err == nil || !strings.Contains(err.Error(), "missing codex thread id") {
		t.Fatalf("restoreAgentCommand() error = %v, want missing thread id", err)
	}
}

func writeRestoreState(t *testing.T, root string, panes []state.Pane) {
	t.Helper()
	locked, err := state.LockProject(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locked.Unlock() }()
	locked.SchemaVersion = state.SchemaVersion
	locked.Panes = panes
	if err := locked.Save(); err != nil {
		t.Fatal(err)
	}
}

func readRestoreState(t *testing.T, root string) state.Store {
	t.Helper()
	store, err := state.LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func installRestoreTmuxAndAgentScripts(t *testing.T, agentName string) string {
	t.Helper()
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "tmux.log")
	tmuxScript := `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$TMUX_RESTORE_LOG"
case "${1:-}" in
  split-window)
    printf '%%restored\n'
    ;;
  select-pane|set-option|kill-pane)
    ;;
  *)
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "tmux"), []byte(tmuxScript), 0o755); err != nil {
		t.Fatal(err)
	}
	agentScript := "#!/usr/bin/env bash\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, agentName), []byte(agentScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_RESTORE_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func installRestoreAgentScript(t *testing.T, agentName string) {
	t.Helper()
	binDir := t.TempDir()
	agentScript := "#!/usr/bin/env bash\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, agentName), []byte(agentScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func stubRestorePaneOps(
	t *testing.T,
	setKey func(string, string) error,
	closeFresh func(string) error,
	waitReady func(string, time.Duration) (codexapp.Status, error),
	closeOwned func(string, string, string) (tmuxrun.ClosePaneResult, error),
) {
	t.Helper()
	originalSetKey := setRestoredPaneLivenessKey
	originalCloseFresh := closeFreshRestoredPane
	originalWaitReady := waitRestoredPaneReady
	originalCloseOwned := closeRestoredPaneIfOwned
	if setKey != nil {
		setRestoredPaneLivenessKey = setKey
	}
	if closeFresh != nil {
		closeFreshRestoredPane = closeFresh
	}
	if waitReady != nil {
		waitRestoredPaneReady = waitReady
	}
	if closeOwned != nil {
		closeRestoredPaneIfOwned = closeOwned
	}
	t.Cleanup(func() {
		setRestoredPaneLivenessKey = originalSetKey
		closeFreshRestoredPane = originalCloseFresh
		waitRestoredPaneReady = originalWaitReady
		closeRestoredPaneIfOwned = originalCloseOwned
	})
}

// TestRestorePaneAliveKeyedPaneRequiresKeyMatch pins liveness for rows recorded
// with a ShellKey: pane ids and worktree paths can be reused, so only a
// matching @fanout_shell_key counts as alive.
func TestRestorePaneAliveKeyedPaneRequiresKeyMatch(t *testing.T) {
	tests := []struct {
		name string
		pane state.Pane
		live tmuxrun.LivePane
		want bool
	}{
		{
			name: "keyed coordinator with matching key is alive",
			pane: state.Pane{PaneID: "%1", Kind: state.PaneKindAttachedAgent, ShellKey: "shell-a", WorktreePath: "/repo"},
			live: tmuxrun.LivePane{ID: "%1", CurrentPath: "/repo", ShellKey: "shell-a"},
			want: true,
		},
		{
			name: "reused pane id under the repo root is dead without the key",
			pane: state.Pane{PaneID: "%1", Kind: state.PaneKindAttachedAgent, ShellKey: "shell-a", WorktreePath: "/repo"},
			live: tmuxrun.LivePane{ID: "%1", CurrentPath: "/repo/.fanout/worktrees/other"},
			want: false,
		},
		{
			name: "unkeyed agent pane keeps the path containment check",
			pane: state.Pane{PaneID: "%1", WorktreePath: "/repo/.fanout/worktrees/child"},
			live: tmuxrun.LivePane{ID: "%1", CurrentPath: "/repo/.fanout/worktrees/child"},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := restorePaneAlive(map[string]tmuxrun.LivePane{tt.live.ID: tt.live}, tt.pane); got != tt.want {
				t.Fatalf("restorePaneAlive(%+v) = %v, want %v", tt.pane, got, tt.want)
			}
		})
	}
}

// A keyed row never stays bound to a reused pane id on a title coincidence.
func TestRestorePaneIDStillBelongsToRecordChecksLivenessKey(t *testing.T) {
	pane := state.Pane{PaneID: "%1", Kind: state.PaneKindAttachedAgent, ShellKey: "shell-a", DisplayName: "plan: build search", WorktreePath: "/repo"}
	title := restorePaneTitleCandidates(pane)[0]
	tests := []struct {
		name string
		live tmuxrun.LivePane
		want bool
	}{
		{name: "matching title with the matching key stays bound", live: tmuxrun.LivePane{ID: "%1", Title: title, ShellKey: "shell-a"}, want: true},
		{name: "matching title without the key is released", live: tmuxrun.LivePane{ID: "%1", Title: title}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := restorePaneIDStillBelongsToRecord(tt.live, pane); got != tt.want {
				t.Fatalf("restorePaneIDStillBelongsToRecord(%+v) = %v, want %v", tt.live, got, tt.want)
			}
		})
	}
}

// TestRestoreRecordedPanesRecreatesCoordinatorWithLivenessKey pins that the
// plan fan-out coordinator's @fanout_shell_key survives an automatic restore:
// without it the recreated pane would read as stale forever.
func TestRestoreRecordedPanesRecreatesCoordinatorWithLivenessKey(t *testing.T) {
	root := t.TempDir()
	logPath := installRestoreTmuxAndAgentScripts(t, "claude")
	writeRestoreState(t, root, []state.Pane{{
		Parent:       "@manual",
		IssueNum:     -1,
		Kind:         state.PaneKindAttachedAgent,
		Slug:         "plan-prompt-1",
		DisplayName:  "plan: build search",
		PaneID:       "%old",
		ShellKey:     "shell-coordinator",
		Agent:        "claude",
		WorktreePath: root,
	}})

	report, err := restoreRecordedPanesForRootWithSnapshot(root, "fanout", "fanout", func(string) (tuiRestoreSnapshot, error) {
		return tuiRestoreSnapshot{}, nil
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Restored != 1 {
		t.Fatalf("Restored = %d, want 1", report.Restored)
	}
	got := readRestoreState(t, root)
	if len(got.Panes) != 1 || got.Panes[0].PaneID != "%restored" || got.Panes[0].ShellKey != "shell-coordinator" {
		t.Fatalf("state panes = %+v, want restored pane keeping its liveness key", got.Panes)
	}
	logBody, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logBody), "@fanout_shell_key shell-coordinator") {
		t.Fatalf("tmux log = %q, want @fanout_shell_key set on the restored pane", string(logBody))
	}
}
