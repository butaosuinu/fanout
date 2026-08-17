package paneruntime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/settings"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

// Inputs is everything backend selection reads. It is gathered once before any
// lock is taken and re-read under the launch lock by Resolution.Verify, so the
// immutable half (environment, user default, supplied intents) stays fixed
// while only the state-derived half is refreshed.
type Inputs struct {
	ProjectRoot        string
	CLI                backend.Name
	Environment        string
	HerdrEnvironment   bool
	TmuxEnvironment    bool
	UserDefault        backend.Name
	Rows               []backend.Binding
	ProvisionalIntents []backend.Binding
	SuppliedIntents    []backend.Binding

	HerdrSession    string
	HerdrSocketPath string
}

// Resolution is the resolved launch runtime. Prepare is nil for every runtime
// that needs no deferred ownership; Verify repeats the selection against the
// store held under the launch lock.
type Resolution struct {
	Selection backend.Selection
	Backend   backend.Backend
	Prepare   func() (ManagedSession, error)
	Verify    func(parent string, locked state.Store) error
}

// LoadInputs probes the environment and settings for one project root. Rows
// come from the caller-supplied store so a lock-time re-read observes exactly
// the state that caller's lock protects.
func LoadInputs(cfg Config, store state.Store, provisionalIntents []backend.Binding) Inputs {
	return Inputs{
		ProjectRoot:        cfg.ProjectRoot,
		CLI:                cfg.Backend,
		Environment:        os.Getenv("FANOUT_BACKEND"),
		HerdrEnvironment:   os.Getenv("HERDR_ENV") == "1",
		TmuxEnvironment:    os.Getenv("TMUX") != "",
		UserDefault:        userDefaultBackend(cfg),
		Rows:               projectBindings(cfg, cfg.ProjectRoot, store),
		ProvisionalIntents: append([]backend.Binding(nil), provisionalIntents...),
		SuppliedIntents:    append([]backend.Binding(nil), provisionalIntents...),
		HerdrSession:       os.Getenv("HERDR_SESSION"),
		HerdrSocketPath:    os.Getenv("HERDR_SOCKET_PATH"),
	}
}

// userDefaultBackend reads the runtime backend only when the user config chose
// it. The launch lane resolves settings again after runtime preflight and owns
// tolerant configuration warnings, so they are suppressed here and each
// diagnostic is printed exactly once.
func userDefaultBackend(cfg Config) backend.Name {
	var cliOverride *backend.Name
	if cfg.Backend != "" {
		cliOverride = new(cfg.Backend)
	}
	resolved := settings.Resolve(cfg.ProjectRoot, settings.CLIOverrides{RuntimeBackend: cliOverride}, nil)
	if resolved.RuntimeBackendSource != settings.RuntimeBackendSourceUserConfig {
		return backend.Name("")
	}
	return resolved.RuntimeBackend
}

func projectBindings(cfg Config, projectRoot string, store state.Store) []backend.Binding {
	if cfg.Bindings == nil {
		return nil
	}
	return cfg.Bindings(projectRoot, store)
}

// errNoBindingProjection fails a selection closed rather than resolving against
// silently empty rows: without the projection, parent stickiness would look
// like a first-ever launch and could pick a runtime that conflicts with a
// saved row.
var errNoBindingProjection = errors.New("runtime backend selection needs a binding projection")

// ResolveSelection applies the backend precedence rules to gathered inputs.
func ResolveSelection(parent string, inputs Inputs) (backend.Selection, error) {
	return backend.Resolve(backend.ResolveInput{
		Parent:             parent,
		CLI:                string(inputs.CLI),
		Environment:        inputs.Environment,
		HerdrEnvironment:   inputs.HerdrEnvironment,
		TmuxEnvironment:    inputs.TmuxEnvironment,
		UserDefault:        string(inputs.UserDefault),
		Rows:               inputs.Rows,
		ProvisionalIntents: inputs.ProvisionalIntents,
	})
}

// Resolve is the shared composition step for every launch lane. It gathers
// inputs, resolves the runtime, validates the flag combination against it, and
// constructs the runtime — deferring live ownership to Resolution.Prepare so a
// dry run never acquires a session.
func Resolve(cfg Config, parent string, store state.Store, provisionalIntents []backend.Binding) (Resolution, error) {
	inputs, err := gatherInputs(cfg, store, provisionalIntents)
	if err != nil {
		return Resolution{}, err
	}
	selection, err := ResolveSelection(parent, inputs)
	if err != nil {
		return Resolution{}, err
	}
	if cfg.Validate != nil {
		if validateErr := cfg.Validate(selection); validateErr != nil {
			return Resolution{}, validateErr
		}
	}
	runtimeBackend, prepare, err := NewLaunchBackend(cfg, selection.Name, inputs)
	if err != nil {
		return Resolution{}, err
	}
	return Resolution{
		Selection: selection,
		Backend:   runtimeBackend,
		Prepare:   prepare,
		Verify:    NewSelectionVerifier(cfg, selection, inputs),
	}, nil
}

func gatherInputs(cfg Config, store state.Store, provisionalIntents []backend.Binding) (Inputs, error) {
	inputs := LoadInputs(cfg, store, provisionalIntents)
	rows, err := CollectBindings(cfg, cfg.ProjectRoot, store)
	if err != nil {
		return Inputs{}, err
	}
	inputs.Rows = rows
	if err := refreshJournalIntents(&inputs); err != nil {
		return Inputs{}, err
	}
	return inputs, nil
}

// NewSelectionVerifier closes over the immutable non-state resolver inputs and
// re-reads final rows from the store held under the launch lock. This catches a
// parent binding created after the composition root's read-only preflight
// without dropping caller-supplied provisional intents.
func NewSelectionVerifier(cfg Config, selection backend.Selection, inputs Inputs) func(string, state.Store) error {
	return func(parent string, locked state.Store) error {
		recheck := inputs
		rows, err := CollectBindings(cfg, inputs.ProjectRoot, locked)
		if err != nil {
			return err
		}
		recheck.Rows = rows
		if err := refreshJournalIntents(&recheck); err != nil {
			return err
		}
		got, resolveErr := ResolveSelection(parent, recheck)
		if resolveErr != nil {
			return resolveErr
		}
		if got.Name != selection.Name {
			return fmt.Errorf("selection changed from %s to %s while acquiring the launch lock", selection.Name, got.Name)
		}
		return nil
	}
}

// CollectBindings projects final rows from the current state plus every linked
// worktree state that can represent an independent fanout owner. Actual issue /
// Project stickiness is repository-wide: choosing from only the invoking
// worktree could create a conflicting backend row that the merged TUI detects
// only after the mutation. A plan without issue provenance remains
// worktree-local because sibling worktrees may use the same slug for unrelated
// specs. The current store is supplied by the caller so the lock-time check
// observes the exact state protected by that caller's launch lock.
func CollectBindings(cfg Config, projectRoot string, current state.Store) ([]backend.Binding, error) {
	if cfg.Bindings == nil {
		return nil, errNoBindingProjection
	}
	stores, err := ProjectStores(projectRoot, current)
	if err != nil {
		return nil, fmt.Errorf("list linked worktrees for runtime backend bindings: %w", err)
	}
	rows := make([]backend.Binding, 0)
	for i, entry := range stores {
		rows = append(rows, ownerBindings(cfg, entry, i > 0)...)
	}
	return rows, nil
}

// ownerBindings projects one owner's rows. A sibling worktree's plan rows stay
// worktree-local because sibling worktrees may use the same slug for unrelated
// specs.
func ownerBindings(cfg Config, owner ProjectStore, sibling bool) []backend.Binding {
	rows := make([]backend.Binding, 0)
	for _, binding := range projectBindings(cfg, owner.Root, owner.Store) {
		if sibling && strings.HasPrefix(strings.TrimSpace(binding.Parent), "plan:") {
			continue
		}
		rows = append(rows, binding)
	}
	return rows
}

// ProjectStore is one worktree root and the state it owns.
type ProjectStore struct {
	Root  string
	Store state.Store
}

// ProjectStores is the set of linked worktrees that can own an independent
// fanout session, current root first. Backend selection unions bindings over
// it, and the launch guards walk the same set, so both decide against the same
// owners.
func ProjectStores(projectRoot string, current state.Store) ([]ProjectStore, error) {
	stores := []ProjectStore{{Root: projectRoot, Store: current}}
	if strings.TrimSpace(projectRoot) == "" {
		return stores, nil
	}
	roots, err := worktree.ListRoots(projectRoot)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{CanonicalRoot(projectRoot): true}
	for _, root := range roots {
		key := CanonicalRoot(root)
		if seen[key] {
			continue
		}
		seen[key] = true
		store, loadErr := state.LoadProject(root)
		if loadErr != nil {
			return nil, fmt.Errorf("load linked worktree state from %s: %w", root, loadErr)
		}
		stores = append(stores, ProjectStore{Root: root, Store: store})
	}
	return stores, nil
}

// CanonicalRoot resolves a worktree root to the comparable form linked-worktree
// deduplication uses.
func CanonicalRoot(root string) string {
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		return filepath.Clean(resolved)
	}
	if absolute, err := filepath.Abs(root); err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(root)
}

// refreshJournalIntents re-reads the shared launch journal so a sibling
// worktree's in-flight owned-session intent participates in parent stickiness
// before any final row exists. Caller-supplied intents always survive.
func refreshJournalIntents(inputs *Inputs) error {
	if strings.TrimSpace(inputs.ProjectRoot) == "" {
		inputs.ProvisionalIntents = append([]backend.Binding(nil), inputs.SuppliedIntents...)
		return nil
	}
	journal, err := state.LoadLaunchJournal(inputs.ProjectRoot)
	if err != nil {
		return fmt.Errorf("load Herdr runtime bindings: %w", err)
	}
	ownerProjectRoot := CanonicalRoot(inputs.ProjectRoot)
	inputs.ProvisionalIntents = append(
		append([]backend.Binding(nil), inputs.SuppliedIntents...),
		journal.ProvisionalBindings(ownerProjectRoot)...,
	)
	return nil
}
