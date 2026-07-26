package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/app/run"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/gitroot"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/log"
	fanoutruntime "github.com/butaosuinu/fanout/internal/infra/runtime"
	"github.com/butaosuinu/fanout/internal/infra/settings"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/tmuxbackend"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

type runtimeBackendInputs struct {
	projectRoot        string
	cli                backend.Name
	environment        string
	herdrEnvironment   bool
	tmuxEnvironment    bool
	userDefault        backend.Name
	planSpecIdentity   string
	rows               []backend.Binding
	provisionalIntents []backend.Binding
	suppliedIntents    []backend.Binding

	herdrSession    string
	herdrSocketPath string
}

type launchBackendResolution struct {
	selection backend.Selection
	backend   backend.Backend
	verify    func(parent string, locked state.Store) error
}

type runtimeReadRoute struct {
	name            backend.Name
	herdrSession    string
	herdrSocketPath string
}

func (r runtimeReadRoute) observationRoute() backend.ObservationRoute {
	return backend.ObservationRoute{
		Backend:    backend.NormalizeName(r.name),
		SessionID:  r.herdrSession,
		SocketPath: r.herdrSocketPath,
	}
}

var runtimeListLiveForProject = runtimeListLiveCollector

const tuiWatcherPreflightRef = "@watcher"

// resolveLaunchRuntime is the composition-root boundary for issue, Project,
// and plan launches. Backend selection happens before runtime discovery and
// before app/run can lock state or prepare a worktree.
func resolveLaunchRuntime(cfg *cliflags.Config, provisionalIntents []backend.Binding, lg *log.Logger) (*run.Runtime, exitcode.Code) {
	projectRoot, err := gitroot.Toplevel("")
	if err != nil {
		lg.Err("%s", err.Error())
		return nil, exitcode.Env
	}
	store, err := state.LoadProject(projectRoot)
	if err != nil {
		lg.Err("%v", err)
		return nil, exitcode.Env
	}

	resolved, err := resolveLaunchBackend(cfg, projectRoot, store, provisionalIntents)
	if err != nil {
		lg.Err("runtime backend: %v", err)
		return nil, exitcode.Env
	}
	return run.ResolveRuntime(cfg, resolved.selection, resolved.backend, resolved.verify, lg)
}

// resolveTUILaunchRuntime applies the same launch-backend contract as the CLI
// while preserving the no-argument TUI's already-established tmux session and
// target. The TUI session bootstrap remains tmux-specific; issue, Project,
// plan, and watcher launches still resolve parent ownership before they can
// lock state or create launch artifacts.
func resolveTUILaunchRuntime(projectRoot, session string, cfg *cliflags.Config) (*run.Runtime, error) {
	return resolveTUILaunchRuntimeForTarget(projectRoot, session, tuiLaunchTarget(session), cfg, nil)
}

func resolveTUILaunchRuntimeForTarget(projectRoot, session, target string, cfg *cliflags.Config, provisionalIntents []backend.Binding) (*run.Runtime, error) {
	store, err := state.LoadProject(projectRoot)
	if err != nil {
		return nil, err
	}
	resolved, err := resolveLaunchBackend(cfg, projectRoot, store, provisionalIntents)
	if err != nil {
		return nil, fmt.Errorf("runtime backend: %w", err)
	}
	return &run.Runtime{
		Info: &fanoutruntime.Info{
			Session:     session,
			Target:      target,
			ProjectRoot: projectRoot,
		},
		GH:               ghissue.Runner{Cwd: projectRoot},
		Backend:          resolved.backend,
		BackendSelection: resolved.selection,
		VerifyBackend:    resolved.verify,
	}, nil
}

// resolveLaunchBackend is the shared read-only composition step for CLI and
// TUI launches. Herdr v1 is rejected before backend availability checks and
// before a launch path can lock state, write a briefing, or prepare a
// worktree. verify repeats parent stickiness against the store held under the
// launch lock.
func resolveLaunchBackend(cfg *cliflags.Config, projectRoot string, store state.Store, provisionalIntents []backend.Binding) (launchBackendResolution, error) {
	inputs, err := loadLaunchRuntimeBackendInputs(cfg, projectRoot, store, provisionalIntents)
	if err != nil {
		return launchBackendResolution{}, err
	}
	rows, err := runtimeBackendBindings(projectRoot, store)
	if err != nil {
		return launchBackendResolution{}, err
	}
	inputs.rows = rows
	selection, err := resolveBackendSelection(cfg.ParentRef, inputs)
	if err != nil {
		return launchBackendResolution{}, err
	}
	if validateErr := validateLaunchBackend(selection); validateErr != nil {
		return launchBackendResolution{}, validateErr
	}
	runtimeBackend, err := constructRuntimeBackend(selection.Name, inputs)
	if err != nil {
		return launchBackendResolution{}, err
	}
	return launchBackendResolution{
		selection: selection,
		backend:   runtimeBackend,
		verify:    backendSelectionVerifier(selection, inputs),
	}, nil
}

// backendSelectionVerifier closes over the immutable non-state resolver inputs
// and re-reads final rows from the store held under the launch lock. This
// catches a parent binding created after the composition root's read-only
// preflight without dropping caller-supplied provisional intents.
func backendSelectionVerifier(selection backend.Selection, inputs runtimeBackendInputs) func(string, state.Store) error {
	return func(parent string, locked state.Store) error {
		recheck := inputs
		rows, err := runtimeBackendBindings(inputs.projectRoot, locked)
		if err != nil {
			return err
		}
		recheck.rows = rows
		if err := refreshHerdrIntentBindings(&recheck, parent); err != nil {
			return err
		}
		got, resolveErr := resolveBackendSelection(parent, recheck)
		if resolveErr != nil {
			return resolveErr
		}
		if got.Name != selection.Name {
			return fmt.Errorf("selection changed from %s to %s while acquiring the launch lock", selection.Name, got.Name)
		}
		return nil
	}
}

// runtimeBackendBindings projects final rows from the current state plus every
// linked worktree state that can represent an independent fanout owner. Actual
// issue / Project stickiness is repository-wide: choosing from only the
// invoking worktree could create a conflicting backend row that the merged TUI
// detects only after the mutation. A plan without issue provenance remains
// worktree-local because sibling worktrees may use the same slug for unrelated
// specs. The current store is supplied by the caller so the lock-time check
// observes the exact state protected by that caller's launch lock.
func runtimeBackendBindings(projectRoot string, current state.Store) ([]backend.Binding, error) {
	rows := backendBindings(projectRoot, current)
	if strings.TrimSpace(projectRoot) == "" {
		return rows, nil
	}

	roots, err := worktree.ListRoots(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("list linked worktrees for runtime backend bindings: %w", err)
	}
	seen := map[string]bool{canonicalRuntimeRoot(projectRoot): true}
	for _, root := range roots {
		key := canonicalRuntimeRoot(root)
		if seen[key] {
			continue
		}
		seen[key] = true
		store, loadErr := state.LoadProject(root)
		if loadErr != nil {
			return nil, fmt.Errorf("load runtime backend bindings from %s: %w", root, loadErr)
		}
		for _, binding := range backendBindings(root, store) {
			if strings.HasPrefix(strings.TrimSpace(binding.Parent), "plan:") {
				continue
			}
			rows = append(rows, binding)
		}
	}
	return rows, nil
}

func canonicalRuntimeRoot(root string) string {
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		return filepath.Clean(resolved)
	}
	if absolute, err := filepath.Abs(root); err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(root)
}

func validateLaunchBackend(selection backend.Selection) error {
	if selection.Name == backend.Herdr {
		// Herdr v1 is observation-only. Reject it before provisional intents,
		// worktrees, or state rows are created. In particular, the read path must
		// not fill missing row identity from a snapshot: same-name sessions reuse
		// public IDs, so doing so would adopt a new terminal instead of reporting
		// the incomplete row as stale.
		return backend.Unsupported(backend.Herdr, "issue, Project, and plan launch in v1")
	}
	return nil
}

func loadRuntimeBackendInputs(cfg *cliflags.Config, projectRoot string, store state.Store, provisionalIntents []backend.Binding) runtimeBackendInputs {
	var cliOverride *backend.Name
	if cfg.Backend != "" {
		cliOverride = new(cfg.Backend)
	}
	// The launch lane resolves settings again after runtime preflight and owns
	// tolerant configuration warnings. Suppress them here so the existing lane
	// prints each diagnostic exactly once.
	resolved := settings.Resolve(projectRoot, settings.CLIOverrides{RuntimeBackend: cliOverride}, nil)
	userDefault := backend.Name("")
	if resolved.RuntimeBackendSource == settings.RuntimeBackendSourceUserConfig {
		userDefault = resolved.RuntimeBackend
	}
	return runtimeBackendInputs{
		projectRoot:        projectRoot,
		cli:                cfg.Backend,
		environment:        os.Getenv("FANOUT_BACKEND"),
		herdrEnvironment:   os.Getenv("HERDR_ENV") == "1",
		tmuxEnvironment:    os.Getenv("TMUX") != "",
		userDefault:        userDefault,
		planSpecIdentity:   cfg.PlanSpecIdentity,
		rows:               backendBindings(projectRoot, store),
		provisionalIntents: append([]backend.Binding(nil), provisionalIntents...),
		suppliedIntents:    append([]backend.Binding(nil), provisionalIntents...),
		herdrSession:       os.Getenv("HERDR_SESSION"),
		herdrSocketPath:    os.Getenv("HERDR_SOCKET_PATH"),
	}
}

func loadLaunchRuntimeBackendInputs(
	cfg *cliflags.Config,
	projectRoot string,
	store state.Store,
	provisionalIntents []backend.Binding,
) (runtimeBackendInputs, error) {
	inputs := loadRuntimeBackendInputs(cfg, projectRoot, store, provisionalIntents)
	if err := refreshHerdrIntentBindings(&inputs, cfg.ParentRef); err != nil {
		return runtimeBackendInputs{}, err
	}
	return inputs, nil
}

func refreshHerdrIntentBindings(inputs *runtimeBackendInputs, parent string) error {
	if strings.TrimSpace(inputs.projectRoot) == "" {
		if inputs.suppliedIntents != nil {
			inputs.provisionalIntents = append([]backend.Binding(nil), inputs.suppliedIntents...)
		}
		return nil
	}
	control, err := state.LoadHerdrControl(inputs.projectRoot)
	if err != nil {
		return fmt.Errorf("load provisional herdr launch intents: %w", err)
	}
	repoIdentity, err := worktree.ResolveHerdrRepoIdentity(inputs.projectRoot)
	if err != nil {
		return fmt.Errorf("resolve provisional herdr binding scope: %w", err)
	}
	persistedIntents, err := control.ProvisionalBindings(state.HerdrBindingScope{
		Parent:             parent,
		SourceRootPhysical: repoIdentity.RepoRoot,
		PlanSpecIdentity:   inputs.planSpecIdentity,
	})
	if err != nil {
		return fmt.Errorf("resolve provisional herdr launch intents: %w", err)
	}
	inputs.provisionalIntents = append(
		append([]backend.Binding(nil), inputs.suppliedIntents...),
		persistedIntents...,
	)
	return nil
}

func resolveBackendSelection(parent string, inputs runtimeBackendInputs) (backend.Selection, error) {
	return backend.Resolve(backend.ResolveInput{
		Parent:             parent,
		CLI:                string(inputs.cli),
		Environment:        inputs.environment,
		HerdrEnvironment:   inputs.herdrEnvironment,
		TmuxEnvironment:    inputs.tmuxEnvironment,
		UserDefault:        string(inputs.userDefault),
		Rows:               inputs.rows,
		ProvisionalIntents: inputs.provisionalIntents,
	})
}

// resolveDisplayBackendSelection resolves the ambient runtime for read-only
// TUI/dashboard composition. It deliberately has no parent, final rows, or
// provisional intents: persisted rows still choose their own observation
// routes, while this selection controls host-specific UI integration.
func resolveDisplayBackendSelection(projectRoot string) (backend.Selection, error) {
	inputs := loadRuntimeBackendInputs(&cliflags.Config{}, projectRoot, state.Store{}, nil)
	return resolveBackendSelection("", inputs)
}

func backendBindings(projectRoot string, store state.Store) []backend.Binding {
	rows := make([]backend.Binding, 0, len(store.Panes)*2)
	planParents := map[string]string{}
	for _, pane := range store.Panes {
		if pane.IsAttachedAgent() {
			parent := strings.TrimSpace(pane.SourceParent)
			if parent == "" {
				parent = strings.TrimSpace(pane.Parent)
			}
			switch {
			case pane.SourceIssueNum > 0 && (parent == panelaunch.ManualParentRef || parent == panelaunch.WatchParentRef || parent == ""):
				parent = strconv.Itoa(pane.SourceIssueNum)
			case strings.HasPrefix(parent, "plan:"):
				planSlug := strings.TrimPrefix(parent, "plan:")
				if planSlug != "" {
					parent = panelaunch.SavedPlanRuntimeParentRef(projectRoot, planSlug)
				}
			}
			if parent != "" && parent != panelaunch.ManualParentRef && parent != panelaunch.WatchParentRef {
				rows = append(rows, backend.Binding{Parent: parent, Backend: pane.Backend})
			}
			continue
		}
		// @manual is a shared bucket for unrelated synthetic launches, so the raw
		// parent cannot establish stickiness. Provenance-bearing coordinator rows
		// below are instead attributed to their actual issue parent.
		if issueNum, ok := panelaunch.PaneIssueParentNum(pane); ok {
			rows = append(rows, backend.Binding{Parent: strconv.Itoa(issueNum), Backend: pane.Backend})
			continue
		}
		if planSlug, ok := strings.CutPrefix(pane.Parent, "plan:"); ok && planSlug != "" {
			parent, seen := planParents[planSlug]
			if !seen {
				parent = panelaunch.SavedPlanRuntimeParentRef(projectRoot, planSlug)
				planParents[planSlug] = parent
			}
			rows = append(rows, backend.Binding{Parent: parent, Backend: pane.Backend})
			continue
		}
		if pane.Parent != panelaunch.ManualParentRef && pane.Parent != panelaunch.WatchParentRef {
			rows = append(rows, backend.Binding{Parent: pane.Parent, Backend: pane.Backend})
		}
	}
	return rows
}

func constructRuntimeBackend(name backend.Name, inputs runtimeBackendInputs) (backend.Backend, error) {
	switch name {
	case backend.Tmux:
		return tmuxbackend.New(), nil
	case backend.Herdr:
		return herdrrun.New(inputs.herdrSession, inputs.herdrSocketPath), nil
	default:
		return nil, fmt.Errorf("unknown runtime backend %q", name)
	}
}

// runtimeListLiveCollector builds the read-only runtime observation port used
// by the dashboard and TUI. Persisted rows route to their own backend; distinct
// herdr named-session/socket tuples remain separate. With no persisted herdr
// route, the normal settings/env/context resolver can opt the read surface into
// the ambient named session even though herdr v1 launch remains unsupported.
//
// includeTmux adds the host tmux route even when no saved row selects it.
// Persisted rows still contribute their own routes for mixed-backend display.
func runtimeListLiveCollector(projectRoot string, includeTmux bool) func() ([]backend.LivePane, error) {
	var mu sync.Mutex
	return func() ([]backend.LivePane, error) {
		mu.Lock()
		defer mu.Unlock()

		routes, routeErr := runtimeReadRoutes(projectRoot, includeTmux)
		return collectRuntimeLive(routes, routeErr, func(route runtimeReadRoute) ([]backend.LivePane, error) {
			runtimeBackend, err := constructRuntimeBackend(route.name, runtimeBackendInputs{
				herdrSession:    route.herdrSession,
				herdrSocketPath: route.herdrSocketPath,
			})
			if err != nil {
				return nil, err
			}
			return runtimeBackend.ListLive()
		})
	}
}

func runtimeReadRoutes(projectRoot string, includeTmux bool) ([]runtimeReadRoute, error) {
	routes := make([]runtimeReadRoute, 0, 3)
	seenRoutes := map[string]bool{}
	addRoute := func(route runtimeReadRoute) {
		key := string(route.name) + "\x00" + route.herdrSession + "\x00" + route.herdrSocketPath
		if seenRoutes[key] {
			return
		}
		seenRoutes[key] = true
		routes = append(routes, route)
	}
	if includeTmux {
		addRoute(runtimeReadRoute{name: backend.Tmux})
	}

	roots, listErr := worktree.ListRoots(projectRoot)
	var routeErr error
	if listErr != nil {
		routeErr = errors.Join(routeErr, fmt.Errorf("list linked worktrees for runtime observation: %w", listErr))
	}
	seenRoots := map[string]bool{}
	hasHerdrRow := false
	for _, root := range roots {
		key := canonicalRuntimeRoot(root)
		if seenRoots[key] {
			continue
		}
		seenRoots[key] = true
		store, err := state.LoadProject(root)
		if err != nil {
			if key == canonicalRuntimeRoot(projectRoot) {
				routeErr = errors.Join(routeErr, err)
			}
			continue
		}
		for i, pane := range store.Panes {
			switch name := backend.NormalizeName(pane.Backend); name {
			case backend.Tmux:
				addRoute(runtimeReadRoute{name: backend.Tmux})
			case backend.Herdr:
				hasHerdrRow = true
				session := strings.TrimSpace(pane.HerdrSession)
				socketPath := strings.TrimSpace(pane.HerdrSocketPath)
				if session == "" || socketPath == "" {
					routeErr = errors.Join(routeErr, backend.ObservationRouteUnavailable(
						backend.ObservationRoute{Backend: backend.Herdr, SessionID: session, SocketPath: socketPath},
						fmt.Errorf(
							"saved herdr runtime route at %s pane %d requires herdrSession and herdrSocketPath",
							state.Path(root), i,
						),
					))
					continue
				}
				addRoute(runtimeReadRoute{name: backend.Herdr, herdrSession: session, herdrSocketPath: socketPath})
			default:
				routeErr = errors.Join(routeErr, backend.ObservationRouteUnavailable(
					backend.ObservationRoute{Backend: name},
					fmt.Errorf(
						"saved runtime route at %s pane %d has unknown backend %q",
						state.Path(root), i, name,
					),
				))
			}
		}
	}

	inputs := loadRuntimeBackendInputs(&cliflags.Config{}, projectRoot, state.Store{}, nil)
	selection, err := resolveBackendSelection("", inputs)
	if err != nil {
		routeErr = errors.Join(routeErr, err)
	} else {
		switch selection.Name {
		case backend.Tmux:
			addRoute(runtimeReadRoute{name: backend.Tmux})
		case backend.Herdr:
			if !hasHerdrRow {
				addRoute(runtimeReadRoute{
					name:            backend.Herdr,
					herdrSession:    strings.TrimSpace(inputs.herdrSession),
					herdrSocketPath: strings.TrimSpace(inputs.herdrSocketPath),
				})
			}
		}
	}
	return routes, routeErr
}

// collectRuntimeLive keeps useful observations when one of several independent
// runtime routes is unavailable and returns the joined error alongside that
// partial result. Read-only views can render the successful routes while still
// marking the runtime snapshot degraded.
func collectRuntimeLive(
	routes []runtimeReadRoute,
	routeErr error,
	list func(runtimeReadRoute) ([]backend.LivePane, error),
) ([]backend.LivePane, error) {
	panes := make([]backend.LivePane, 0)
	joined := routeErr
	for _, route := range routes {
		got, err := list(route)
		if err != nil {
			joined = errors.Join(joined, backend.ObservationRouteUnavailable(
				route.observationRoute(),
				fmt.Errorf("list live panes via %s: %w", runtimeReadRouteLabel(route), err),
			))
			continue
		}
		panes = append(panes, got...)
	}
	return panes, joined
}

func runtimeReadRouteLabel(route runtimeReadRoute) string {
	if route.name != backend.Herdr {
		return string(route.name)
	}
	if route.herdrSession == "" {
		return string(route.name)
	}
	return fmt.Sprintf("%s session %q", route.name, route.herdrSession)
}
