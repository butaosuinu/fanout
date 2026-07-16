// Package peermsg executes the `fanout msg` verbs: identity resolution,
// per-parent SQLite messaging (peers / inbox / board / send / post /
// mark-read / register), and the best-effort tmux nudge. The CLI boundary —
// argv parsing, usage text, invocation-error wording — stays in cmd/fanout;
// this package receives the parsed Request and owns everything after it.
package peermsg

import (
	"os"
	"path/filepath"
	"time"

	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/msgstore"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/team"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
)

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
	// ListLivePanes and SendLine are the tmux seams nudge drives.
	ListLivePanes func() ([]tmuxrun.LivePane, error)
	SendLine      func(paneID, text string) error
	// LoadState resolves and loads the owner checkout's .fanout/state.json
	// read-only — the recipient's recorded pane id lives there, not in the
	// messages DB.
	LoadState func() (state.Store, error)
	// Tick paces the watch poll loop; production is time.After, tests inject
	// an immediately-ready channel.
	Tick func(d time.Duration) <-chan time.Time
}

// DefaultDeps wires the production implementations.
func DefaultDeps() Deps {
	return Deps{
		DetectIdentity: team.Detect,
		ListLivePanes:  tmuxrun.ListLivePanes,
		SendLine:       tmuxrun.SendLiteralLine,
		LoadState:      defaultLoadState,
		Tick:           time.After,
	}
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
	statePath := os.Getenv(fanoutStatePathEnv)
	if statePath != "" {
		if abs, err := filepath.Abs(statePath); err == nil {
			statePath = abs
		}
	} else {
		root, err := team.OwnerProjectRoot()
		if err != nil {
			return state.Store{}, err
		}
		statePath = state.Path(root)
	}
	return state.Load(statePath)
}

// Run executes a parsed msg request: resolve identity, short-circuit nudge,
// open the per-parent team DB, then run the verb.
func Run(req Request, deps Deps, lg *log.Logger) exitcode.Code {
	self, parent, pane, code := resolveMsgIdentity(&req, deps, lg)
	if code != exitcode.OK {
		return code
	}

	// nudge reads neither the messages DB nor store identity: it resolves the
	// recipient from state.json and pushes via tmux, so short it out before
	// openMsgDB.
	if req.Verb == "nudge" {
		return runMsgNudge(&req, parent, deps, lg)
	}

	// watch loops with its own DB handle (Watcher.Close), so it must not run
	// under this function's defer db.Close(); short it out like nudge.
	if req.Verb == "watch" {
		return runMsgWatch(&req, self, parent, pane, deps, lg)
	}

	db, code := openMsgDB(req.Verb, parent, lg)
	if code != exitcode.OK {
		return code
	}
	defer func() { _ = db.Close() }()
	store, err := msgstore.New(db, parent)
	if err != nil {
		lg.Err("msg %s: %v", req.Verb, err)
		return exitcode.Backend
	}
	return runMsgVerb(&req, store, self, parent, pane, lg)
}
