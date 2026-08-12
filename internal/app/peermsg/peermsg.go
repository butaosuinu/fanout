// Package peermsg executes the `fanout msg` verbs: identity resolution,
// per-parent SQLite messaging (peers / inbox / board / send / post /
// mark-read / register), and the best-effort runtime nudge. The CLI boundary —
// argv parsing, usage text, invocation-error wording — stays in cmd/fanout;
// this package receives the parsed Request and owns everything after it.
package peermsg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/team"
)

// HerdrNudger is the already-owned runtime surface used by one best-effort
// nudge. Production opens it without creating or restarting a session.
type HerdrNudger interface {
	LivePanes(context.Context) ([]backend.LivePane, error)
	ProcessInfo(context.Context, string) (herdrrun.PaneProcessInfo, error)
	PrepareNudge(context.Context, herdrrun.NudgeTarget, string) (herdrrun.NudgePrompt, error)
}

// Request is the parsed, validated form of a `fanout msg` invocation
// (cmd/fanout's msgFlags after parse/validate). Field semantics mirror the
// CLI flags one-to-one.
type Request struct {
	Verb string
	JSON bool
	Self int // 0 = not given; resolved synthetic number for SelfRaw
	// SelfRaw holds an explicit --self task id (plan parents) until the parent
	// is known and it can be mapped to a synthetic number; "" otherwise.
	SelfRaw string
	Parent  string
	To      int // 0 = not given; resolved synthetic number for ToRaw
	// ToRaw holds a --to/nudge task id (plan parents) until the parent is known
	// and it can be mapped to a synthetic number; "" otherwise.
	ToRaw    string
	Kind     string
	IDs      []int64
	All      bool
	MarkRead bool
	Body     string
	// Interval is watch's poll interval in seconds (default 2, validated >= 1
	// by the CLI parser); unused by every other verb.
	Interval int
}

// Deps are the IO seams Run drives. Tests inject fakes so no live
// tmux/state/git environment is needed (the struct form of the package-var
// seams the cmd layer used before this package existed).
type Deps struct {
	// DetectIdentity resolves the invoking pane's identity from fanout state.
	DetectIdentity func() (team.Identity, error)
	// ListLive and SendLine are the tmux runtime seams nudge drives.
	ListLive func() ([]backend.LivePane, error)
	SendLine func(backend.PaneRef, string) error
	// OpenHerdr opens the existing owned Herdr runtime named by the recipient's
	// saved repo key. ReadLockedState performs the initial and final telemetry
	// reads under the owning state lock and call deadline.
	OpenHerdr       func(context.Context, string) (HerdrNudger, error)
	ReadLockedState func(context.Context, func(state.Store) error) error
	// LoadState resolves and loads the owner checkout's .fanout/state.json
	// read-only — the recipient's recorded pane id lives there, not in the
	// messages DB.
	LoadState func() (state.Store, error)
	// Tick paces the watch poll loop; production is time.After, tests inject
	// an immediately-ready channel.
	Tick func(d time.Duration) <-chan time.Time
}

// DefaultDeps wires production dependencies around the backend selected by the
// composition root. This package never constructs an infra backend itself.
func DefaultDeps(runtimeBackend backend.Backend) Deps {
	deps := Deps{
		DetectIdentity:  team.Detect,
		LoadState:       defaultLoadState,
		ReadLockedState: defaultReadLockedState,
		Tick:            time.After,
	}
	if runtimeBackend != nil {
		deps.ListLive = runtimeBackend.ListLive
		deps.SendLine = runtimeBackend.SendLine
	}
	return deps
}

// withDefaults fills nil seams from DefaultDeps. The exported watch entry
// points apply it so an in-process caller passing a zero-value or partial
// Deps (a valid pattern for every seam it does not exercise) degrades to the
// production wiring instead of panicking on a nil function call.
func (d Deps) withDefaults() Deps {
	def := DefaultDeps(nil)
	if d.DetectIdentity == nil {
		d.DetectIdentity = def.DetectIdentity
	}
	if d.LoadState == nil {
		d.LoadState = def.LoadState
	}
	if d.Tick == nil {
		d.Tick = def.Tick
	}
	return d
}

const fanoutStatePathEnv = "FANOUT_STATE_PATH"

// defaultLoadState resolves and loads the owner checkout's .fanout/state.json
// read-only. It resolves the path the way team.Detect does
// (FANOUT_STATE_PATH, else OwnerProjectRoot), NOT cmd/fanout's
// resolveStateRuntime: nudge is normally run FROM a child worktree pane
// (<owner>/.fanout/worktrees/<slug>), whose own git toplevel has no
// state.json. Only OwnerProjectRoot climbs to the owner that holds it — the
// same resolver every other msg verb uses (openMsgDB) — so resolveStateRuntime
// would silently load an empty store and report every peer "not recorded".
func defaultLoadState() (state.Store, error) {
	statePath, err := defaultStatePath()
	if err != nil {
		return state.Store{}, err
	}
	return state.Load(statePath)
}

func defaultReadLockedState(ctx context.Context, read func(state.Store) error) error {
	statePath, err := defaultStatePath()
	if err != nil {
		return err
	}
	locked, err := state.LockContext(ctx, statePath)
	if err != nil {
		return err
	}
	return errors.Join(read(locked.Store), locked.Unlock())
}

func defaultStatePath() (string, error) {
	statePath := os.Getenv(fanoutStatePathEnv)
	if statePath != "" {
		if abs, err := filepath.Abs(statePath); err == nil {
			statePath = abs
		}
	} else {
		root, err := team.OwnerProjectRoot()
		if err != nil {
			return "", err
		}
		statePath = state.Path(root)
	}
	return statePath, nil
}

// Run executes a parsed msg request: resolve identity, short-circuit nudge,
// open the per-parent team DB, then run the verb.
func Run(req Request, deps Deps, lg *log.Logger) exitcode.Code {
	// watch is part of the exported in-process surface (see OpenWatcher), so
	// fill missing seams before anything dereferences them; the other verbs
	// keep the caller's Deps verbatim.
	if req.Verb == "watch" {
		deps = deps.withDefaults()
	}
	self, parent, pane, code := resolveMsgIdentity(&req, deps, lg)
	if code != exitcode.OK {
		return code
	}

	// nudge reads neither the messages DB nor store identity: it resolves the
	// recipient from state.json and pushes via its recorded runtime, so short it out before
	// openMsgDB.
	if req.Verb == "nudge" {
		return runMsgNudge(&req, parent, deps, lg)
	}

	// watch loops with its own DB handle (Watcher.Close), so it must not run
	// under this function's defer db.Close(); short it out like nudge.
	if req.Verb == "watch" {
		return runMsgWatch(&req, self, parent, pane, deps, lg)
	}

	db, store, code := openMsgStore(req.Verb, parent, lg)
	if code != exitcode.OK {
		return code
	}
	defer func() { _ = db.Close() }()
	return runMsgVerb(&req, store, self, parent, pane, lg)
}
