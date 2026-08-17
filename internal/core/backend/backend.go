// Package backend defines the runtime-neutral pane contract used by fanout.
// Implementations live in infra; this package contains no process or filesystem
// access.
package backend

import (
	"errors"
	"fmt"
	"strings"
)

// Name identifies a pane runtime implementation.
type Name string

const (
	Tmux  Name = "tmux"
	Herdr Name = "herdr"

	// HerdrObservationOnlyReason is the shared operator-facing explanation for
	// interactive TUI actions outside the admitted owned-session capability.
	HerdrObservationOnlyReason = "herdr backend interactive TUI action is unavailable"
)

// ParseName validates a configured backend name. An empty value means no
// selection and is rejected; persisted legacy rows should use NormalizeName.
func ParseName(raw string) (Name, error) {
	name := Name(strings.TrimSpace(raw))
	switch name {
	case Tmux, Herdr:
		return name, nil
	default:
		return "", fmt.Errorf("unknown runtime backend %q (supported: %s, %s)", raw, Tmux, Herdr)
	}
}

// NormalizeName maps the empty backend stored by legacy tmux rows to tmux.
// Non-empty values are returned unchanged so callers can validate them without
// silently treating corrupt state as tmux.
func NormalizeName(name Name) Name {
	if strings.TrimSpace(string(name)) == "" {
		return Tmux
	}
	return name
}

// PaneRef is the runtime-native address of one pane. Pane is deliberately an
// opaque string: tmux uses values such as %12 while herdr uses values such as
// w1:p1. Workspace is the backend-native containing workspace/session target.
type PaneRef struct {
	Backend   Name
	Workspace string
	Pane      string
}

// AgentSessionRef identifies one logical agent conversation independently of
// its terminal and process. Herdr currently exposes id and path refs; callers
// compare the full tuple byte-for-byte after validating it.
type AgentSessionRef struct {
	Source string `json:"source"`
	Agent  string `json:"agent"`
	Kind   string `json:"kind"`
	Value  string `json:"value"`
}

// ProcessIdentity binds one OS-verified agent process to its shell root and
// foreground process group. It is an observation result, not a portable PID.
type ProcessIdentity struct {
	ShellPID               int `json:"shellPid"`
	ForegroundProcessGroup int `json:"foregroundProcessGroup"`
	AgentPID               int `json:"agentPid"`
}

func (i ProcessIdentity) Valid() bool {
	return i.ShellPID > 1 && i.ForegroundProcessGroup > 1 && i.AgentPID > 1
}

// Valid reports whether every identity component is present and the ref kind
// is one admitted herdr version exposes. Validation does not normalize the tuple because
// liveness comparison is exact.
func (r AgentSessionRef) Valid() bool {
	if strings.TrimSpace(r.Source) == "" || strings.TrimSpace(r.Agent) == "" || strings.TrimSpace(r.Value) == "" {
		return false
	}
	return r.Kind == "id" || r.Kind == "path"
}

// AgentState is fanout's six-value display vocabulary.
type AgentState string

const (
	AgentRunning AgentState = "running"
	AgentWorking AgentState = "working"
	AgentPlan    AgentState = "plan"
	AgentBlocked AgentState = "blocked"
	AgentIdle    AgentState = "idle"
	AgentDone    AgentState = "done"
)

// ParseAgentState accepts only the six values fanout exposes. Backend-native
// unknown values stay outside this vocabulary until their mapping is proven.
func ParseAgentState(raw string) (AgentState, bool) {
	state := AgentState(strings.TrimSpace(raw))
	switch state {
	case AgentRunning, AgentWorking, AgentPlan, AgentBlocked, AgentIdle, AgentDone:
		return state, true
	default:
		return "", false
	}
}

// MapHerdrAgentState projects cooperative telemetry, then herdr's public
// agent status, into fanout's six-value display vocabulary. Either value is
// useful only while the snapshot contains the corresponding agent record.
func MapHerdrAgentState(agentPresent bool, native, reported string) AgentState {
	if !agentPresent {
		return ""
	}
	if state, ok := ParseAgentState(reported); ok {
		return state
	}
	state := AgentState(native)
	switch state {
	case AgentWorking, AgentBlocked, AgentIdle, AgentDone:
		return state
	default:
		return ""
	}
}

// ObservationRoute identifies one independently queried runtime route. Tmux
// has one route with empty session/socket fields; herdr routes are scoped by
// their verified named session and socket path.
type ObservationRoute struct {
	Backend    Name
	SessionID  string
	SocketPath string
}

type observationRouteUnavailableError struct {
	route ObservationRoute
	cause error
}

// ObservationRouteUnavailable scopes cause to one runtime observation route.
// Its text and unwrap chain remain the cause's, so adding route metadata does
// not rewrite existing user-facing diagnostics or break errors.Is/errors.As.
func ObservationRouteUnavailable(route ObservationRoute, cause error) error {
	if cause == nil {
		return nil
	}
	return &observationRouteUnavailableError{route: route, cause: cause}
}

func (e *observationRouteUnavailableError) Error() string { return e.cause.Error() }

func (e *observationRouteUnavailableError) Unwrap() error { return e.cause }

func (e *observationRouteUnavailableError) observationRoute() ObservationRoute { return e.route }

// ClassifyObservationError separates route-scoped failures from failures that
// make every route indeterminate. It recursively follows errors.Join and %w
// wrappers. Once a route wrapper is reached its cause remains scoped to that
// route; an unwrapped, untyped leaf anywhere else sets all=true.
func ClassifyObservationError(err error) (routes map[ObservationRoute]bool, all bool) {
	routes = map[ObservationRoute]bool{}
	var visit func(error)
	visit = func(current error) {
		if current == nil {
			return
		}
		if scoped, ok := current.(interface{ observationRoute() ObservationRoute }); ok {
			routes[scoped.observationRoute()] = true
			return
		}
		if joined, ok := current.(interface{ Unwrap() []error }); ok {
			children := joined.Unwrap()
			if len(children) == 0 {
				all = true
				return
			}
			for _, child := range children {
				visit(child)
			}
			return
		}
		if wrapped, ok := current.(interface{ Unwrap() error }); ok {
			child := wrapped.Unwrap()
			if child == nil {
				all = true
				return
			}
			visit(child)
			return
		}
		all = true
	}
	visit(err)
	return routes, all
}

// LivePane is the backend-neutral observation of one live pane. Runtime-owned
// identity fields remain separate from display metadata so later liveness code
// can choose the evidence appropriate for each backend.
type LivePane struct {
	Ref         PaneRef
	CurrentPath string
	Title       string

	// FocusKnown distinguishes an observed false focus value from a backend that
	// does not expose focus in its aggregate liveness snapshot.
	FocusKnown bool
	Focused    bool

	AgentState       AgentState
	NativeAgentState string
	WorkspaceLabel   string
	TerminalID       string
	AgentID          string
	AgentProvider    string
	AgentSession     *AgentSessionRef
	ProcessIdentity  *ProcessIdentity
	AgentPresent     bool
	ShellKey         string
	RepoKey          string
	ProjectRoot      string
	WorktreePath     string
	Role             string
	SessionID        string
	SocketPath       string
}

// LaunchRequest contains only runtime-neutral launch inputs. Popup, keybind,
// layout, pane decoration, and session management stay implementation details.
type LaunchRequest struct {
	Workspace    string
	Target       string
	WorktreePath string
	Command      string
	StartGate    string
}

// LaunchPreview describes the pane a dry run would have created. It carries
// the runtime-neutral launch inputs plus the display metadata the decoration
// step would apply, so a backend can render the commands it would have run
// without the caller knowing that runtime's command vocabulary.
type LaunchPreview struct {
	// Target is the backend-native container the pane would be created in. It
	// is empty when the runtime picks the container itself.
	Target      string
	ProjectRoot string
	// WorktreePath is the worktree the pane would run in; the worktree itself is
	// not created by a dry run.
	WorktreePath string
	BranchName   string
	// Command is the agent command, before any runtime-specific wrapper.
	Command string
	// StartGate is the optional launch lock the pane's command would wait on.
	StartGate string
	// PaneTitle and PaneLabel are the display metadata the decoration step would
	// apply. An empty PaneTitle means the pane would keep the runtime's own.
	PaneTitle string
	PaneLabel string
}

// previewBareRunes is the exact character class a preview token may carry
// unquoted: ASCII letters and digits plus the path and flag punctuation that a
// shell leaves alone.
const previewBareRunes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/:._-"

// PreviewQuote renders s as one copy-paste-safe POSIX shell token for dry-run
// preview text. Backend-neutral and backend-specific preview lines are pinned
// byte-for-byte by the Tier 2 goldens, so every producer must quote alike.
func PreviewQuote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool { return !strings.ContainsRune(previewBareRunes, r) }) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// MutationModel is the domain property that decides how fanout must realize a
// launch on a runtime: whether one container mutation settles locally and
// atomically, or has to be carried through fanout's own durable intent journal.
//
// It is a declared property rather than something orchestration discovers by
// trying the cheap path first and degrading, because a wrong guess is not
// recoverable: issuing a journaled mutation without having recorded the intent
// leaves remote state that no later run can attribute or roll back.
type MutationModel int

const (
	// MutationAtomic means one container mutation is a single local call whose
	// outcome is observed synchronously. A failure leaves nothing behind, so the
	// launch, its decoration, and its state row need no external journal.
	MutationAtomic MutationModel = iota
	// MutationJournaled means container mutations are remote and non-atomic: a
	// request can be issued without its response ever arriving. Fanout records
	// the intent before issuing it and reconciles the outcome afterwards, so
	// this lane owns a launch lock, a rollback journal, and a recovery pass.
	MutationJournaled
)

// Backend is the minimum runtime surface. Implementations may expose
// backend-specific helpers, but orchestration depends only on these methods.
type Backend interface {
	Name() Name
	// MutationModel reports how this runtime's container mutations settle. It
	// selects the realization strategy orchestration runs; it is not a display
	// value and must not be derived from Name by callers.
	MutationModel() MutationModel
	CheckAvailable() error
	Launch(LaunchRequest) (PaneRef, error)
	ReleaseStartGate(string) error
	ListLive() ([]LivePane, error)
	Read(PaneRef, int) (string, error)
	SendLine(PaneRef, string) error
	Focus(PaneRef) error
	Close(PaneRef) error
}

// CloseStatus classifies a destructive close whose runtime identity was
// checked before and after the close attempt.
type CloseStatus int

const (
	// CloseFailed is the zero value so an incomplete adapter cannot accidentally
	// authorize state or worktree removal.
	CloseFailed CloseStatus = iota
	CloseConfirmed
	CloseStale
)

// CloseRequest carries the durable identity needed to avoid closing a pane
// whose runtime-native id was reused.
type CloseRequest struct {
	Ref          PaneRef
	WorktreePath string
	ShellKey     string
}

// CloseResult reports the identity-checked result. ContainerID is an optional
// runtime-native layout hint (a tmux window id for the tmux adapter).
type CloseResult struct {
	Status      CloseStatus
	ContainerID string
}

// OwnedCloser is an optional destructive capability. It stays separate from
// Backend.Close because only runtimes with a proven durable identity contract
// may let lifecycle cleanup remove state after a close attempt.
type OwnedCloser interface {
	CloseOwned(CloseRequest) (CloseResult, error)
}

// OwnedClosingBackend is a runtime backend already bound to one admitted owned
// target: a full backend that also issues the identity-gated close. Binding
// returns it so the caller never re-derives the admission it just proved.
type OwnedClosingBackend interface {
	Backend
	OwnedCloser
}

// FreshCloser is an optional capability for a pane returned by the immediately
// preceding Launch call, before its durable identity has been stamped.
type FreshCloser interface {
	CloseFresh(PaneRef) error
}

// PaneDecorator is an optional capability for runtimes that annotate a pane
// with fanout's display metadata. paneID is the runtime-native id the caller
// already holds for a pane it just created or is running in. Every method is
// display-only, so a backend without this capability still launches usable
// panes: they simply carry no fanout title, border label, or dashboard hint,
// and callers skip decoration silently instead of failing the launch.
type PaneDecorator interface {
	SetPaneTitle(paneID, title string) error
	SetPaneLabel(paneID, label string) error
	EnablePaneBorderTitles(paneID string) error
	SetPaneProjectRoot(paneID, projectRoot string) error
	SetPaneWorktreePath(paneID, worktreePath string) error
}

// DryRunPreviewer is an optional capability for runtimes that describe the
// commands a launch would have run. Each returned element is one preview line
// carrying neither indentation nor color: the caller frames them alongside its
// own backend-neutral lines, which keeps the runtime's command vocabulary out
// of orchestration.
type DryRunPreviewer interface {
	PreviewLaunch(LaunchPreview) []string
}

// LivenessStamper is an optional capability for runtimes that stamp a durable
// liveness token on a pane. Unlike decoration it is not best-effort: a state
// row whose token never reached its pane can never be proven live again, so a
// caller that requires the stamp fails closed when the capability is absent.
type LivenessStamper interface {
	StampPaneShellKey(paneID, shellKey string) error
}

// LayoutTrigger is why a relayout was requested. It carries the caller's
// intent only: a create or a close changed the pane set, while a resize may
// have changed nothing a runtime cares about.
type LayoutTrigger int

const (
	// LayoutCreate follows a pane creation and LayoutClose a pane removal. Both
	// changed the pane set, so a runtime must not skip them.
	LayoutCreate LayoutTrigger = iota
	LayoutClose
	// LayoutResize follows a container geometry change, which a runtime may
	// legitimately skip when the arrangement it would produce is unchanged.
	LayoutResize
)

// LayoutManager is an optional capability for runtimes that arrange fanout's
// panes themselves. target is the runtime-native pane, container, or session
// address whose arrangement is stale. Every call is best-effort: a runtime
// that cannot lay out the target degrades internally instead of failing the
// launch or teardown that asked for the relayout.
type LayoutManager interface {
	Relayout(target string, trigger LayoutTrigger) error
}

// AsPaneDecorator resolves b's pane-decoration capability. ok=false means the
// backend leaves panes undecorated, which callers treat as skip, not failure.
func AsPaneDecorator(b Backend) (PaneDecorator, bool) {
	decorator, ok := b.(PaneDecorator)
	return decorator, ok
}

// AsDryRunPreviewer resolves b's dry-run preview capability. ok=false means the
// backend cannot describe its own launch commands, so a dry run prints its
// backend-neutral lines only rather than another runtime's commands.
func AsDryRunPreviewer(b Backend) (DryRunPreviewer, bool) {
	previewer, ok := b.(DryRunPreviewer)
	return previewer, ok
}

// AsLivenessStamper resolves b's liveness-stamp capability. ok=false means the
// backend cannot prove a pane's identity through a fanout token, so a caller
// that records the pane in fanout state must fail closed.
func AsLivenessStamper(b Backend) (LivenessStamper, bool) {
	stamper, ok := b.(LivenessStamper)
	return stamper, ok
}

// AsLayoutManager resolves b's pane-layout capability. ok=false means the
// backend arranges its panes on its own, so callers skip the relayout silently
// instead of failing.
func AsLayoutManager(b Backend) (LayoutManager, bool) {
	manager, ok := b.(LayoutManager)
	return manager, ok
}

// UnsupportedError reports an operation intentionally disabled by a backend.
// Herdr v1 uses this fail-closed result for every mutation and targeted read.
type UnsupportedError struct {
	Backend   Name
	Operation string
}

// ErrUnsupported is the sentinel matched by errors.Is for operations a
// backend deliberately disables.
var ErrUnsupported = errors.New("runtime backend operation unsupported")

func (e UnsupportedError) Error() string {
	return fmt.Sprintf("runtime backend %s does not support %s", e.Backend, e.Operation)
}

func (e UnsupportedError) Unwrap() error { return ErrUnsupported }

// Unsupported constructs a typed unsupported-operation error.
func Unsupported(name Name, operation string) error {
	return UnsupportedError{Backend: name, Operation: operation}
}

// IsUnsupported reports whether err is an UnsupportedError.
func IsUnsupported(err error) bool {
	var target UnsupportedError
	return errors.As(err, &target)
}
