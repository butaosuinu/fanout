package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/app/run"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/gitroot"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/paneruntime"
	fanoutruntime "github.com/butaosuinu/fanout/internal/infra/runtime"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/team"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

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
	runtime, code := run.ResolveRuntime(cfg, resolved.Selection, resolved.Backend, resolved.Verify, lg)
	if runtime != nil {
		bindLaunchBackend(runtime, resolved)
	}
	return runtime, code
}

// resolveTUILaunchRuntime applies the same launch-backend contract as the CLI
// while carrying the no-argument TUI's already-established runtime identifiers.
// Issue, Project, plan, and watcher launches resolve parent ownership before
// they can lock state or create launch artifacts.
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
	runtime := &run.Runtime{
		Info: &fanoutruntime.Info{
			Session:     session,
			Target:      target,
			ProjectRoot: projectRoot,
		},
		GH:               ghissue.Runner{Cwd: projectRoot},
		Backend:          resolved.Backend,
		BackendSelection: resolved.Selection,
		VerifyBackend:    resolved.Verify,
	}
	bindLaunchBackend(runtime, resolved)
	return runtime, nil
}

// runtimeConfig maps CLI flags onto the neutral selection input. The two
// callbacks stay here because the row projection is an app rule and the flag
// combinations a runtime cannot serve are CLI vocabulary; paneruntime only
// sequences them around selection.
func runtimeConfig(cfg *cliflags.Config, projectRoot string) paneruntime.Config {
	return paneruntime.Config{
		ProjectRoot: projectRoot,
		Backend:     cfg.Backend,
		Session:     cfg.Session,
		DryRun:      cfg.DryRun,
		Bindings:    backendBindings,
		Validate: func(selection backend.Selection) error {
			return validateLaunchBackend(cfg, selection)
		},
	}
}

// resolveLaunchBackend is the shared composition step for CLI and TUI launch
// lanes. Owned Herdr launch lanes acquire the same repository runtime. Verify
// repeats parent stickiness against the store held under the launch lock.
func resolveLaunchBackend(cfg *cliflags.Config, projectRoot string, store state.Store, provisionalIntents []backend.Binding) (paneruntime.Resolution, error) {
	return paneruntime.Resolve(runtimeConfig(cfg, projectRoot), cfg.ParentRef, store, provisionalIntents)
}

func bindLaunchBackend(runtime *run.Runtime, resolved paneruntime.Resolution) {
	if resolved.Prepare == nil {
		return
	}
	var once sync.Once
	var prepareErr error
	runtime.PrepareBackend = func() error {
		once.Do(func() {
			owned, err := resolved.Prepare()
			if err != nil {
				prepareErr = err
				return
			}
			runtime.Backend = owned.Backend()
			runtime.Managed = owned
		})
		return prepareErr
	}
}

func validateLaunchBackend(
	cfg *cliflags.Config,
	selection backend.Selection,
) error {
	if selection.Name != backend.Herdr {
		return nil
	}
	return validateHerdrLaunchBackend(cfg)
}

func validateHerdrLaunchBackend(cfg *cliflags.Config) error {
	if cfg.Team {
		dbPath := os.Getenv(team.DBPathEnv)
		if dbPath != "" && !filepath.IsAbs(dbPath) {
			return fmt.Errorf("%s must be absolute with --backend herdr --team", team.DBPathEnv)
		}
	}
	if cfg.Session != "" {
		return fmt.Errorf("--session is only supported by the tmux backend")
	}
	if !configHasLaunchAgent(cfg) {
		return fmt.Errorf("agent is required; pass --agent <name> or set FANOUT_AGENT")
	}
	return nil
}

func configHasLaunchAgent(cfg *cliflags.Config) bool {
	agentName := cfg.Agent
	if agentName == "" {
		agentName = os.Getenv("FANOUT_AGENT")
	}
	return agentName != "" || len(cfg.AgentOverrides) > 0
}

func loadRuntimeBackendInputs(cfg *cliflags.Config, projectRoot string, store state.Store, provisionalIntents []backend.Binding) paneruntime.Inputs {
	return paneruntime.LoadInputs(runtimeConfig(cfg, projectRoot), store, provisionalIntents)
}

func resolveBackendSelection(parent string, inputs paneruntime.Inputs) (backend.Selection, error) {
	return paneruntime.ResolveSelection(parent, inputs)
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
	return panelaunch.RuntimeBackendBindings(projectRoot, store)
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
			runtimeBackend, err := paneruntime.NewBackend(
				route.name, route.herdrSession, route.herdrSocketPath,
			)
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
	hasHerdrRoute := false
	for _, root := range roots {
		key := paneruntime.CanonicalRoot(root)
		if seenRoots[key] {
			continue
		}
		seenRoots[key] = true
		store, err := state.LoadProject(root)
		if err != nil {
			if key == paneruntime.CanonicalRoot(projectRoot) {
				routeErr = errors.Join(routeErr, err)
			}
			continue
		}
		for i, pane := range store.Panes {
			switch name := backend.NormalizeName(pane.Backend); name {
			case backend.Tmux:
				addRoute(runtimeReadRoute{name: backend.Tmux})
			case backend.Herdr:
				hasHerdrRoute = true
				session := strings.TrimSpace(pane.SessionID)
				socketPath := strings.TrimSpace(pane.SocketPath)
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

	control, controlErr := state.LoadLaunchJournal(projectRoot)
	if controlErr != nil {
		hasHerdrRoute = true
		routeErr = errors.Join(routeErr, fmt.Errorf("load Herdr control routes: %w", controlErr))
	} else {
		for i, intent := range control.Intents {
			hasHerdrRoute = true
			session, socketPath := herdrIntentRuntimeRoute(intent)
			if session == "" || socketPath == "" {
				routeErr = errors.Join(routeErr, backend.ObservationRouteUnavailable(
					backend.ObservationRoute{
						Backend: backend.Herdr, SessionID: session, SocketPath: socketPath,
					},
					fmt.Errorf("herdr control intent %d requires session and socketPath", i),
				))
				continue
			}
			addRoute(runtimeReadRoute{
				name:            backend.Herdr,
				herdrSession:    session,
				herdrSocketPath: socketPath,
			})
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
			if !hasHerdrRoute {
				addRoute(runtimeReadRoute{
					name:            backend.Herdr,
					herdrSession:    strings.TrimSpace(inputs.HerdrSession),
					herdrSocketPath: strings.TrimSpace(inputs.HerdrSocketPath),
				})
			}
		}
	}
	return routes, routeErr
}

func herdrIntentRuntimeRoute(intent state.LaunchIntent) (string, string) {
	if state.IsServerLifecycleKind(intent.Kind) && intent.Server != nil {
		return strings.TrimSpace(intent.Server.Session), strings.TrimSpace(intent.Server.SocketPath)
	}
	return strings.TrimSpace(intent.Session), strings.TrimSpace(intent.SocketPath)
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
