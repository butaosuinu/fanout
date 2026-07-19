package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/settings"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func TestResolveBackendSelectionCarriesParentStickiness(t *testing.T) {
	got, err := resolveBackendSelection("0425", runtimeBackendInputs{
		cli:         backend.Tmux,
		environment: "tmux",
		rows: []backend.Binding{
			{Parent: "425", Backend: ""},
			{Parent: "999", Backend: backend.Herdr},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != backend.Tmux || got.Reason != backend.ReasonExistingParent {
		t.Fatalf("selection = %+v, want sticky tmux", got)
	}
}

func TestResolveBackendSelectionNestedHerdrWinsTmuxContext(t *testing.T) {
	got, err := resolveBackendSelection("425", runtimeBackendInputs{
		herdrEnvironment: true,
		tmuxEnvironment:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != backend.Herdr || got.Reason != backend.ReasonHerdrContext {
		t.Fatalf("selection = %+v, want herdr context", got)
	}
}

func TestResolveBackendSelectionCarriesProvisionalIntents(t *testing.T) {
	got, err := resolveBackendSelection("425", runtimeBackendInputs{
		tmuxEnvironment: true,
		provisionalIntents: []backend.Binding{{
			Parent:  "0425",
			Backend: backend.Herdr,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != backend.Herdr || got.Reason != backend.ReasonExistingParent {
		t.Fatalf("selection = %+v, want sticky provisional herdr", got)
	}

	_, err = resolveBackendSelection("425", runtimeBackendInputs{
		rows:               []backend.Binding{{Parent: "425"}},
		provisionalIntents: []backend.Binding{{Parent: "425", Backend: backend.Herdr}},
	})
	if err == nil || !strings.Contains(err.Error(), "mixed state") {
		t.Fatalf("mixed row/intent error = %v, want mixed state", err)
	}

	_, err = resolveBackendSelection("425", runtimeBackendInputs{
		provisionalIntents: []backend.Binding{{Parent: "425"}},
	})
	if err == nil || !strings.Contains(err.Error(), "provisional intent for parent 425 has no backend") {
		t.Fatalf("empty intent error = %v, want fail closed", err)
	}
}

func TestBackendSelectionVerifierRejectsRowCreatedAfterPreflight(t *testing.T) {
	repo := t.TempDir()
	gitCmdTest(t, repo, "init", "-b", "main")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TMUX", "/private/tmp/tmux.sock,1,0")
	t.Setenv("HERDR_ENV", "")
	t.Setenv("FANOUT_BACKEND", "")
	cfg := &cliflags.Config{ParentRef: "425"}
	store, err := state.LoadProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveLaunchBackend(cfg, repo, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.selection.Name != backend.Tmux {
		t.Fatalf("preflight selection = %+v, want tmux", resolved.selection)
	}

	locked, err := state.LockProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = locked.Unlock() })
	locked.Panes = append(locked.Panes, state.Pane{
		Parent:  "425",
		Backend: backend.Herdr,
		PaneID:  "w1:p1",
	})
	err = resolved.verify("425", locked.Store)
	if err == nil || !strings.Contains(err.Error(), "selection changed from tmux to herdr while acquiring the launch lock") {
		t.Fatalf("locked recheck error = %v, want racing row rejection", err)
	}
}

func TestResolveLaunchBackendIncludesLinkedWorktreeRows(t *testing.T) {
	repo := initLifecycleRepo(t)
	sibling := filepath.Join(t.TempDir(), "sibling")
	gitCmdTest(t, repo, "worktree", "add", "-b", "backend-sibling", sibling, "HEAD")
	writeLifecycleState(t, sibling, state.Pane{
		Parent:  "425",
		Backend: backend.Herdr,
		PaneID:  "w1:p1",
	})
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TMUX", "/private/tmp/tmux.sock,1,0")
	t.Setenv("HERDR_ENV", "")
	t.Setenv("FANOUT_BACKEND", "")

	store, err := state.LoadProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolveLaunchBackend(&cliflags.Config{ParentRef: "425", Backend: backend.Tmux}, repo, store, nil)
	if err == nil || !strings.Contains(err.Error(), "runtime backend for parent 425 is herdr; --backend requests tmux") {
		t.Fatalf("resolveLaunchBackend() error = %v, want linked-worktree stickiness conflict", err)
	}

	writeLifecycleState(t, repo, state.Pane{Parent: "425", PaneID: "%9"})
	store, err = state.LoadProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolveLaunchBackend(&cliflags.Config{ParentRef: "425"}, repo, store, nil)
	if err == nil || !strings.Contains(err.Error(), "mixed state: herdr, tmux") {
		t.Fatalf("resolveLaunchBackend() mixed error = %v, want linked-worktree mixed-state rejection", err)
	}
}

func TestRuntimeBackendBindingsKeepNonIssuePlansWorktreeLocal(t *testing.T) {
	repo := initLifecycleRepo(t)
	sibling := filepath.Join(t.TempDir(), "sibling")
	gitCmdTest(t, repo, "worktree", "add", "-b", "plan-backend-sibling", sibling, "HEAD")
	writeRawLifecycleState(t, repo, state.Pane{
		Parent:  "plan:shared",
		TaskID:  "local-task",
		Backend: backend.Tmux,
		PaneID:  "%9",
	})
	writeRawLifecycleState(t, sibling, state.Pane{
		Parent:  "plan:shared",
		TaskID:  "sibling-task",
		Backend: backend.Herdr,
		PaneID:  "w1:p1",
	})

	for _, tt := range []struct {
		name string
		root string
		want backend.Name
	}{
		{name: "home worktree", root: repo, want: backend.Tmux},
		{name: "sibling worktree", root: sibling, want: backend.Herdr},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store, err := state.LoadProject(tt.root)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := runtimeBackendBindings(tt.root, store)
			if err != nil {
				t.Fatal(err)
			}
			got, err := resolveBackendSelection("plan:shared", runtimeBackendInputs{rows: rows})
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != tt.want || got.Reason != backend.ReasonExistingParent {
				t.Fatalf("selection = %+v, want sticky %s in this worktree", got, tt.want)
			}
		})
	}
}

func TestRuntimeBackendBindingsKeepIssueSourcedPlansRepositoryWide(t *testing.T) {
	repo := initLifecycleRepo(t)
	sibling := filepath.Join(t.TempDir(), "sibling")
	gitCmdTest(t, repo, "worktree", "add", "-b", "issue-plan-backend-sibling", sibling, "HEAD")
	planDir := filepath.Join(sibling, ".fanout", "plans")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(planDir, "shared.json"),
		[]byte(`{"version":1,"plan":{"slug":"shared","title":"Shared","source":"issue #425"},"tasks":[]}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	writeRawLifecycleState(t, repo, state.Pane{Parent: "425", Backend: backend.Tmux, PaneID: "%9"})
	writeRawLifecycleState(t, sibling, state.Pane{
		Parent:  "plan:shared",
		TaskID:  "sibling-task",
		Backend: backend.Herdr,
		PaneID:  "w1:p1",
	})

	store, err := state.LoadProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := runtimeBackendBindings(repo, store)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolveBackendSelection("425", runtimeBackendInputs{rows: rows})
	if err == nil || !strings.Contains(err.Error(), "mixed state: herdr, tmux") {
		t.Fatalf("issue-sourced sibling plan error = %v, want repository-wide mixed-state rejection", err)
	}
}

func TestBackendSelectionVerifierRechecksLinkedWorktreeRows(t *testing.T) {
	repo := initLifecycleRepo(t)
	sibling := filepath.Join(t.TempDir(), "sibling")
	gitCmdTest(t, repo, "worktree", "add", "-b", "backend-race-sibling", sibling, "HEAD")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TMUX", "/private/tmp/tmux.sock,1,0")
	t.Setenv("HERDR_ENV", "")
	t.Setenv("FANOUT_BACKEND", "")

	store, err := state.LoadProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveLaunchBackend(&cliflags.Config{ParentRef: "425"}, repo, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.selection.Name != backend.Tmux {
		t.Fatalf("preflight selection = %+v, want tmux", resolved.selection)
	}

	writeLifecycleState(t, sibling, state.Pane{
		Parent:  "425",
		Backend: backend.Herdr,
		PaneID:  "w1:p1",
	})
	if err := resolved.verify("425", store); err == nil || !strings.Contains(err.Error(), "selection changed from tmux to herdr while acquiring the launch lock") {
		t.Fatalf("locked recheck error = %v, want linked-worktree racing row rejection", err)
	}
}

func TestBackendSelectionVerifierKeepsProvisionalIntents(t *testing.T) {
	inputs := runtimeBackendInputs{
		provisionalIntents: []backend.Binding{{Parent: "425", Backend: backend.Herdr}},
	}
	selection, err := resolveBackendSelection("425", inputs)
	if err != nil {
		t.Fatal(err)
	}
	err = backendSelectionVerifier(selection, inputs)("425", state.Store{Panes: []state.Pane{{Parent: "425"}}})
	if err == nil || !strings.Contains(err.Error(), "mixed state") {
		t.Fatalf("locked recheck error = %v, want mixed final row/provisional intent rejection", err)
	}
}

func TestValidateLaunchBackendRejectsHerdrV1(t *testing.T) {
	err := validateLaunchBackend(backend.Selection{Name: backend.Herdr})
	if !errors.Is(err, backend.ErrUnsupported) {
		t.Fatalf("error = %v, want ErrUnsupported", err)
	}
	if err := validateLaunchBackend(backend.Selection{Name: backend.Tmux}); err != nil {
		t.Fatalf("tmux error = %v", err)
	}
}

func TestResolveLaunchRuntimeRejectsHerdrBeforeFanoutMutation(t *testing.T) {
	repo := t.TempDir()
	gitCmdTest(t, repo, "init", "-b", "main")
	t.Chdir(repo)
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("TMUX", "")
	t.Setenv("FANOUT_BACKEND", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var stderr bytes.Buffer
	rt, code := resolveLaunchRuntime(
		&cliflags.Config{ParentRef: "425", Agent: "claude"},
		nil,
		log.NewWith(io.Discard, &stderr, false),
	)
	if code != exitcode.Env || rt != nil {
		t.Fatalf("resolveLaunchRuntime() = (%+v, %d), want (nil, %d)", rt, code, exitcode.Env)
	}
	if !strings.Contains(stderr.String(), "runtime backend herdr does not support") {
		t.Fatalf("stderr = %q, want herdr unsupported error", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(repo, ".fanout")); !os.IsNotExist(err) {
		t.Fatalf("herdr launch preflight mutated .fanout: %v", err)
	}
}

func TestResolveLaunchRuntimeUsesCallerProvisionalIntent(t *testing.T) {
	repo := t.TempDir()
	gitCmdTest(t, repo, "init", "-b", "main")
	t.Chdir(repo)
	t.Setenv("HERDR_ENV", "")
	t.Setenv("TMUX", "/private/tmp/tmux.sock,1,0")
	t.Setenv("FANOUT_BACKEND", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var stderr bytes.Buffer
	rt, code := resolveLaunchRuntime(
		&cliflags.Config{ParentRef: "425", Agent: "claude"},
		[]backend.Binding{{Parent: "425", Backend: backend.Herdr}},
		log.NewWith(io.Discard, &stderr, false),
	)
	if code != exitcode.Env || rt != nil {
		t.Fatalf("resolveLaunchRuntime(intent) = (%+v, %d), want (nil, %d)", rt, code, exitcode.Env)
	}
	if !strings.Contains(stderr.String(), "runtime backend herdr does not support") {
		t.Fatalf("stderr = %q, want provisional herdr unsupported error", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(repo, ".fanout")); !os.IsNotExist(err) {
		t.Fatalf("provisional herdr preflight mutated .fanout: %v", err)
	}
}

func TestResolveLaunchRuntimeRejectsProjectHerdrBeforeMutation(t *testing.T) {
	repo := t.TempDir()
	gitCmdTest(t, repo, "init", "-b", "main")
	t.Chdir(repo)
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("TMUX", "")
	t.Setenv("FANOUT_BACKEND", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	projectURL := "https://github.com/orgs/example/projects/7"
	var stderr bytes.Buffer
	rt, code := resolveLaunchRuntime(
		&cliflags.Config{ParentRef: projectURL, ParentMode: cliflags.ModeProject, Agent: "claude"},
		nil,
		log.NewWith(io.Discard, &stderr, false),
	)
	if code != exitcode.Env || rt != nil {
		t.Fatalf("resolveLaunchRuntime(Project) = (%+v, %d), want (nil, %d)", rt, code, exitcode.Env)
	}
	if !strings.Contains(stderr.String(), "runtime backend herdr does not support") {
		t.Fatalf("stderr = %q, want herdr unsupported error", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(repo, ".fanout")); !os.IsNotExist(err) {
		t.Fatalf("Project herdr preflight mutated .fanout: %v", err)
	}
}

func TestLoadRuntimeBackendInputsDefersSettingsWarnings(t *testing.T) {
	repo := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	repoConfig := settings.RepoConfigPath(repo)
	if err := os.MkdirAll(filepath.Dir(repoConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repoConfig, []byte(`{"runtimeBackend":"herdr"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &cliflags.Config{ParentRef: "425"}
	_ = loadRuntimeBackendInputs(cfg, repo, state.Store{}, nil)
	var warnings []string
	settings.Resolve(repo, settings.CLIOverrides{}, func(format string, a ...any) {
		warnings = append(warnings, format)
	})
	count := 0
	for _, warning := range warnings {
		if strings.Contains(warning, "runtimeBackend is ignored in repo config") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("runtimeBackend warning count = %d, want 1; warnings=%v", count, warnings)
	}
}

func TestBackendBindingsPreserveLegacyAndExplicitRows(t *testing.T) {
	got := backendBindings("", state.Store{Panes: []state.Pane{
		{Parent: "425"},
		{Parent: "426", Backend: backend.Herdr},
	}})
	if len(got) != 2 || got[0].Backend != "" || got[1].Backend != backend.Herdr {
		t.Fatalf("bindings = %+v", got)
	}
}

func TestBackendBindingsActualIssueAliasKeepsLegacyTmuxSticky(t *testing.T) {
	tests := []struct {
		name string
		pane state.Pane
	}{
		{
			name: "watch row",
			pane: state.Pane{Parent: panelaunch.WatchParentRef, IssueNum: 425},
		},
		{
			name: "plan coordinator",
			pane: state.Pane{Parent: panelaunch.ManualParentRef, IssueNum: -1, Slug: panelaunch.PlanIssueSlug(425, -1)},
		},
		{
			name: "issue orchestrator",
			pane: state.Pane{Parent: panelaunch.ManualParentRef, IssueNum: -1, Slug: panelaunch.OrchestratorIssueSlug(425, -1)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveBackendSelection("425", runtimeBackendInputs{
				userDefault: backend.Herdr,
				rows:        backendBindings("", state.Store{Panes: []state.Pane{tt.pane}}),
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != backend.Tmux || got.Reason != backend.ReasonExistingParent {
				t.Fatalf("selection = %+v, want legacy alias sticky tmux", got)
			}
		})
	}
}

func TestBackendBindingsWatchUsesOnlyActualIssueParent(t *testing.T) {
	got := backendBindings("", state.Store{Panes: []state.Pane{{
		Parent:   panelaunch.WatchParentRef,
		IssueNum: 425,
		Backend:  backend.Herdr,
	}}})
	if len(got) != 1 {
		t.Fatalf("bindings = %+v, want one actual-issue binding", got)
	}
	if got[0].Parent != "425" || got[0].Backend != backend.Herdr {
		t.Fatalf("binding = %+v, want parent 425/herdr", got[0])
	}
}

func TestBackendBindingsIssueSourcedPlanTaskUsesActualParent(t *testing.T) {
	repo := t.TempDir()
	planDir := filepath.Join(repo, ".fanout", "plans")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "launch-plan.json"), []byte(`{"version":1,"plan":{"slug":"launch-plan","title":"Launch","source":"issue #425"},"tasks":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	got := backendBindings(repo, state.Store{Panes: []state.Pane{{
		Parent:  "plan:launch-plan",
		TaskID:  "base",
		Backend: backend.Herdr,
	}}})
	if len(got) != 1 || got[0] != (backend.Binding{Parent: "425", Backend: backend.Herdr}) {
		t.Fatalf("bindings = %+v, want only issue 425/herdr", got)
	}
}

func TestBackendBindingsAttachedRowsKeepSourceIssueBinding(t *testing.T) {
	for _, sourceParent := range []string{panelaunch.WatchParentRef, panelaunch.ManualParentRef} {
		t.Run(sourceParent, func(t *testing.T) {
			got := backendBindings("", state.Store{Panes: []state.Pane{{
				Parent:         sourceParent,
				IssueNum:       -1,
				Kind:           state.PaneKindAttachedAgent,
				SourceParent:   sourceParent,
				SourceIssueNum: 425,
				Backend:        backend.Herdr,
			}}})
			if len(got) != 1 || got[0] != (backend.Binding{Parent: "425", Backend: backend.Herdr}) {
				t.Fatalf("bindings = %+v, want only source issue 425/herdr", got)
			}
		})
	}
}

func TestBackendBindingsActualIssueAliasesRejectMixedBackends(t *testing.T) {
	rows := backendBindings("", state.Store{Panes: []state.Pane{
		{Parent: panelaunch.WatchParentRef, IssueNum: 425},
		{
			Parent:   panelaunch.ManualParentRef,
			IssueNum: -1,
			Slug:     panelaunch.OrchestratorIssueSlug(425, -1),
			Backend:  backend.Herdr,
		},
	}})
	_, err := resolveBackendSelection("425", runtimeBackendInputs{rows: rows})
	if err == nil || !strings.Contains(err.Error(), "mixed state: herdr, tmux") {
		t.Fatalf("mixed issue aliases error = %v, want fail closed", err)
	}
}

func TestBackendBindingsIgnoreUnrelatedManualRows(t *testing.T) {
	rows := backendBindings("", state.Store{Panes: []state.Pane{{
		Parent:   panelaunch.ManualParentRef,
		IssueNum: -1,
		Slug:     "scratch-terminal",
		Backend:  backend.Tmux,
	}}})
	if len(rows) != 0 {
		t.Fatalf("bindings = %+v, want unrelated @manual row omitted", rows)
	}
	got, err := resolveBackendSelection(panelaunch.ManualParentRef, runtimeBackendInputs{
		userDefault: backend.Herdr,
		rows:        rows,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != backend.Herdr || got.Reason != backend.ReasonUserConfig {
		t.Fatalf("selection = %+v, want new synthetic launch to use user herdr", got)
	}
}

func TestRuntimeReadRoutesCombineTmuxAndDistinctSavedHerdrSessions(t *testing.T) {
	repo := initLifecycleRepo(t)
	sibling := filepath.Join(t.TempDir(), "sibling")
	gitCmdTest(t, repo, "worktree", "add", "-b", "read-route-sibling", sibling, "HEAD")
	writeRawLifecycleState(t, repo,
		state.Pane{Parent: "425", PaneID: "%1"},
		state.Pane{
			Parent:          "426",
			Backend:         backend.Herdr,
			PaneID:          "w1:p1",
			HerdrSession:    "saved-a",
			HerdrSocketPath: "/tmp/saved-a.sock",
		},
	)
	writeRawLifecycleState(t, sibling,
		state.Pane{
			Parent:          "427",
			Backend:         backend.Herdr,
			PaneID:          "w1:p2",
			HerdrSession:    "saved-a",
			HerdrSocketPath: "/tmp/saved-a.sock",
		},
		state.Pane{
			Parent:          "428",
			Backend:         backend.Herdr,
			PaneID:          "w1:p3",
			HerdrSession:    "saved-b",
			HerdrSocketPath: "/tmp/saved-b.sock",
		},
	)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("FANOUT_BACKEND", "herdr")
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_SESSION", "ambient")
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/ambient.sock")
	t.Setenv("TMUX", "")

	routes, err := runtimeReadRoutes(repo, false)
	if err != nil {
		t.Fatal(err)
	}
	want := map[runtimeReadRoute]bool{
		{name: backend.Tmux}: true,
		{name: backend.Herdr, herdrSession: "saved-a", herdrSocketPath: "/tmp/saved-a.sock"}: true,
		{name: backend.Herdr, herdrSession: "saved-b", herdrSocketPath: "/tmp/saved-b.sock"}: true,
	}
	if len(routes) != len(want) {
		t.Fatalf("routes = %+v, want %d distinct persisted routes", routes, len(want))
	}
	for _, route := range routes {
		if !want[route] {
			t.Fatalf("unexpected route %+v; all routes=%+v", route, routes)
		}
	}
}

func TestRuntimeReadRoutesUseAmbientHerdrWithoutSavedRoute(t *testing.T) {
	repo := initLifecycleRepo(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("FANOUT_BACKEND", "")
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_SESSION", "ambient")
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/ambient.sock")
	t.Setenv("TMUX", "")

	routes, err := runtimeReadRoutes(repo, false)
	if err != nil {
		t.Fatal(err)
	}
	want := runtimeReadRoute{name: backend.Herdr, herdrSession: "ambient", herdrSocketPath: "/tmp/ambient.sock"}
	if len(routes) != 1 || routes[0] != want {
		t.Fatalf("routes = %+v, want [%+v]", routes, want)
	}

	routes, err = runtimeReadRoutes(repo, true)
	if err != nil {
		t.Fatal(err)
	}
	wantWithTUIHost := map[runtimeReadRoute]bool{
		{name: backend.Tmux}: true,
		want:                 true,
	}
	if len(routes) != len(wantWithTUIHost) {
		t.Fatalf("TUI routes = %+v, want tmux host plus ambient herdr", routes)
	}
	for _, route := range routes {
		if !wantWithTUIHost[route] {
			t.Fatalf("unexpected TUI route %+v; all routes=%+v", route, routes)
		}
	}
}

func TestRuntimeReadRoutesUseUserDefaultHerdrWithoutSavedRoute(t *testing.T) {
	repo := initLifecycleRepo(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("FANOUT_BACKEND", "")
	t.Setenv("HERDR_ENV", "")
	t.Setenv("HERDR_SESSION", "user-default")
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/user-default.sock")
	t.Setenv("TMUX", "")
	userConfig := settings.UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(userConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userConfig, []byte(`{"runtimeBackend":"herdr"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	routes, err := runtimeReadRoutes(repo, false)
	if err != nil {
		t.Fatal(err)
	}
	want := runtimeReadRoute{name: backend.Herdr, herdrSession: "user-default", herdrSocketPath: "/tmp/user-default.sock"}
	if len(routes) != 1 || routes[0] != want {
		t.Fatalf("routes = %+v, want [%+v]", routes, want)
	}
}

func TestRuntimeReadRoutesDoNotAmbientFallbackForIncompleteSavedHerdrRoute(t *testing.T) {
	repo := initLifecycleRepo(t)
	writeRawLifecycleState(t, repo, state.Pane{
		Parent:       "425",
		Backend:      backend.Herdr,
		PaneID:       "w1:p1",
		HerdrSession: "saved",
	})
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("FANOUT_BACKEND", "herdr")
	t.Setenv("HERDR_SESSION", "ambient")
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/ambient.sock")
	t.Setenv("HERDR_ENV", "")
	t.Setenv("TMUX", "")

	routes, err := runtimeReadRoutes(repo, false)
	if err == nil || !strings.Contains(err.Error(), "requires herdrSession and herdrSocketPath") {
		t.Fatalf("runtimeReadRoutes() error = %v, want incomplete saved-route rejection", err)
	}
	if len(routes) != 0 {
		t.Fatalf("routes = %+v, want no ambient fallback", routes)
	}
}

func TestCollectRuntimeLiveCombinesSuccessesAndReportsRouteFailures(t *testing.T) {
	routes := []runtimeReadRoute{
		{name: backend.Tmux},
		{name: backend.Herdr, herdrSession: "saved-a", herdrSocketPath: "/tmp/a.sock"},
		{name: backend.Herdr, herdrSession: "saved-b", herdrSocketPath: "/tmp/b.sock"},
	}
	calls := map[runtimeReadRoute]int{}
	panes, err := collectRuntimeLive(routes, errors.New("route discovery degraded"), func(route runtimeReadRoute) ([]backend.LivePane, error) {
		calls[route]++
		if route.herdrSession == "saved-b" {
			return nil, errors.New("offline")
		}
		return []backend.LivePane{{Ref: backend.PaneRef{Backend: route.name, Pane: runtimeReadRouteLabel(route)}}}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "route discovery degraded") || !strings.Contains(err.Error(), "saved-b") {
		t.Fatalf("partial error = %v, want discovery and saved-b failures", err)
	}
	if len(panes) != 2 {
		t.Fatalf("panes = %+v, want successful tmux and saved-a observations", panes)
	}
	for _, route := range routes {
		if calls[route] != 1 {
			t.Fatalf("calls[%+v] = %d, want 1", route, calls[route])
		}
	}

	_, err = collectRuntimeLive(routes[1:], nil, func(runtimeReadRoute) ([]backend.LivePane, error) {
		return nil, errors.New("offline")
	})
	if err == nil || !strings.Contains(err.Error(), "saved-a") || !strings.Contains(err.Error(), "saved-b") {
		t.Fatalf("all-failed error = %v, want both herdr route labels", err)
	}
}
