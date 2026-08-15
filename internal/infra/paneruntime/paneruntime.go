// Package paneruntime is the single place that depends on a concrete runtime
// backend. It gathers the selection inputs, resolves which runtime a launch
// runs on, constructs that runtime, publishes the hidden subcommands the
// runtimes re-execute, and hands out the owned-session handle.
//
// Everything above it stays runtime-neutral: internal/app names only core
// backend types and its own ports, and cmd/fanout composes through this
// package instead of importing tmuxbackend or herdrrun. Adding a runtime means
// editing this package and nothing else.
//
// The package deliberately sits in infra, not app: the owned-session handle
// carries infra state types (*state.LaunchCapsule, state.RuntimeServerIdentity)
// that no core interface may name, and infra must never import app.
package paneruntime

import (
	"fmt"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/tmuxbackend"
)

// HostBackend is the tmux host surface the console and the lifecycle lanes
// wire: the minimum runtime plus the two optional capabilities a tmux host
// always has. Naming it here keeps the concrete adapter type out of cmd.
type HostBackend interface {
	backend.Backend
	backend.OwnedCloser
	backend.LayoutManager
}

// Config is the neutral launch input the composition root supplies. The CLI
// owns its own flag vocabulary; this package only reads the few decisions that
// change which runtime a launch runs on.
type Config struct {
	ProjectRoot string
	// Backend is the --backend override, empty when the flag was not given.
	Backend backend.Name
	// Session is the --session override. Only tmux accepts one.
	Session string
	DryRun  bool
	// Bindings projects one worktree's saved rows into final backend bindings.
	// The projection is an app rule; this package only unions it across the
	// linked worktrees that can own an independent fanout session.
	Bindings func(projectRoot string, store state.Store) []backend.Binding
	// Validate rejects flag combinations the selected runtime cannot serve. It
	// runs between selection and construction, so a rejected launch never
	// reaches a runtime process.
	Validate func(backend.Selection) error
}

// NewTmux constructs the tmux host runtime.
func NewTmux() HostBackend { return tmuxbackend.New() }

// NewBackend constructs the runtime named by a resolved selection or a saved
// observation route. session and socketPath are the Herdr route and are
// ignored by every other runtime.
func NewBackend(name backend.Name, session, socketPath string) (backend.Backend, error) {
	switch name {
	case backend.Tmux:
		return tmuxbackend.New(), nil
	case backend.Herdr:
		return herdrrun.New(session, socketPath), nil
	default:
		return nil, fmt.Errorf("unknown runtime backend %q", name)
	}
}
