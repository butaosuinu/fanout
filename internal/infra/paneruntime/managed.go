package paneruntime

import (
	"context"
	"fmt"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

// ManagedSession is the repository-owned session handle. It is an alias, not a
// wrapper: the app-side ports (panelaunch.ManagedSessionRuntime,
// lifecycle.WorkspaceRuntime, peermsg.NudgeRuntime,
// panelaunch.ManagedRestartRuntime) are structural, so one value satisfies all
// of them while this package names none of them — infra must not import app.
//
// The alias exists so the composition root can hold the handle without naming
// the runtime that produced it. Narrowing it to an interface waits on the TUI
// call sites that still reach for the runtime-specific bound backend.
type ManagedSession = *herdrrun.OwnedSession

// openSession is the owned-session entry point, replaced in tests.
var openSession = herdrrun.OpenOwned

// Open opens the repository-owned session for one Git common dir. It never
// creates a session.
func Open(ctx context.Context, repoKey string) (ManagedSession, error) {
	return openSession(ctx, herdrrun.OwnedOptions{GitCommonDir: repoKey})
}

// OpenProject opens the owned session for a project root without creating one.
func OpenProject(projectRoot string) (ManagedSession, error) {
	repoKey, err := projectRepoKey(context.Background(), projectRoot)
	if err != nil {
		return nil, err
	}
	return Open(context.Background(), repoKey)
}

// EnsureProject opens the owned session for a project root, starting the
// repository-owned server when none is running.
func EnsureProject(projectRoot string) (ManagedSession, error) {
	repoKey, err := projectRepoKey(context.Background(), projectRoot)
	if err != nil {
		return nil, err
	}
	return herdrrun.EnsureOwned(context.Background(), herdrrun.OwnedOptions{GitCommonDir: repoKey})
}

func projectRepoKey(ctx context.Context, projectRoot string) (string, error) {
	identity, err := worktree.ResolveRepoIdentity(ctx, projectRoot)
	if err != nil {
		return "", err
	}
	return identity.RepoKey, nil
}

// NewLaunchBackend constructs the runtime for a resolved selection. A live
// owned-session launch gets a mutation-free preview plus a prepare step, so
// selection and validation complete before anything acquires a session.
func NewLaunchBackend(
	cfg Config,
	name backend.Name,
	inputs Inputs,
) (backend.Backend, func() (ManagedSession, error), error) {
	if name != backend.Herdr {
		runtimeBackend, err := NewBackend(name, inputs.SessionID, inputs.SocketPath)
		return runtimeBackend, nil, err
	}
	if cfg.DryRun {
		return herdrrun.NewPreview(), nil, nil
	}
	prepare := func() (ManagedSession, error) {
		return EnsureProject(inputs.ProjectRoot)
	}
	return herdrrun.NewPreview(), prepare, nil
}

// ServerOps is the owned-server lifecycle seam bound to one repository. The
// composition root maps it onto the app port that drives the journal-fenced
// transaction, so the app never names the runtime performing these calls.
type ServerOps struct {
	// Inspect reads the saved server identity without mutating it.
	Inspect func() (state.RuntimeServerIdentity, error)
	// Observe opens the owned server read-only and lists everything still
	// holding a resource on it.
	Observe func(context.Context) ([]backend.WorkspaceObservation, error)
	// Restart replaces the proven-dead generation named by the identity.
	Restart func(context.Context, state.RuntimeServerIdentity) (ManagedSession, error)
	// Shutdown retires the empty generation, calling the callback once at the
	// moment the signal becomes indeterminate.
	Shutdown func(context.Context, state.RuntimeServerIdentity, func() error) error
}

// NewServerOps binds the owned-server lifecycle calls to one Git common dir.
func NewServerOps(repoKey string) ServerOps {
	opts := herdrrun.OwnedOptions{GitCommonDir: repoKey}
	return ServerOps{
		Inspect: func() (state.RuntimeServerIdentity, error) {
			return herdrrun.InspectOwnedServer(opts)
		},
		Observe: func(ctx context.Context) ([]backend.WorkspaceObservation, error) {
			owned, err := herdrrun.OpenOwned(ctx, opts)
			if err != nil {
				return nil, err
			}
			return owned.ObserveWorkspaces(ctx)
		},
		Restart: func(ctx context.Context, identity state.RuntimeServerIdentity) (ManagedSession, error) {
			return herdrrun.RestartOwned(ctx, opts, identity)
		},
		Shutdown: func(
			ctx context.Context,
			identity state.RuntimeServerIdentity,
			markIssued func() error,
		) error {
			return herdrrun.ShutdownOwned(ctx, opts, identity, markIssued)
		},
	}
}

// ObservationRequest is one persisted launch binding to observe the current
// runtime for. It mirrors the app's telemetry target in core-typed fields so
// the observation crosses the layer boundary without naming an app type.
type ObservationRequest struct {
	GitCommonDir string
	Session      string
	SocketPath   string
	PaneID       string
}

// Observation is the current runtime state for one persisted launch binding.
// ProcessError is reported alongside the panes because a missing process is a
// telemetry outcome, not a failed observation.
type Observation struct {
	Panes        []backend.LivePane
	ProcessInfo  backend.PaneProcessInfo
	ProcessError error
}

// ObserveManaged reads the owned session named by a persisted launch binding.
// It refuses an owner whose current route drifted from the recorded one, so
// telemetry can never be attributed to a replacement session.
func ObserveManaged(ctx context.Context, req ObservationRequest) (Observation, error) {
	owned, err := Open(ctx, req.GitCommonDir)
	if err != nil {
		return Observation{}, err
	}
	if owned.Session != req.Session || owned.SocketPath != req.SocketPath {
		return Observation{}, fmt.Errorf("current Herdr owner route does not match launch binding")
	}
	panes, err := owned.LivePanes(ctx)
	if err != nil {
		return Observation{}, err
	}
	process, err := owned.ProcessInfo(ctx, req.PaneID)
	return Observation{Panes: panes, ProcessInfo: process, ProcessError: err}, nil
}
