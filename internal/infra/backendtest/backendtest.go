// Package backendtest is an in-process fake of the internal/core/backend
// contract. It lets app- and cmd-level tests drive launch orchestration without
// a live tmux server and without writing shell shims onto PATH.
//
// The files here are ordinary (non-_test.go) sources so other packages' tests
// can import them. Nothing in the product imports this package, so it is never
// linked into the fanout binary.
//
// Capability detection in core/backend is a type assertion, so one struct
// carrying every capability method would satisfy every probe and make the
// "capability is absent" branches untestable. Fake therefore implements the
// base backend.Backend and nothing else; the composed shapes in capability.go
// add exactly the capability method sets their names declare.
package backendtest

import (
	"sync"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
)

// The recorded method names. Assertions reference these constants so a call-log
// query cannot drift from the name the fake records.
const (
	MethodCheckAvailable         = "CheckAvailable"
	MethodLaunch                 = "Launch"
	MethodReleaseStartGate       = "ReleaseStartGate"
	MethodListLive               = "ListLive"
	MethodRead                   = "Read"
	MethodSendLine               = "SendLine"
	MethodFocus                  = "Focus"
	MethodClose                  = "Close"
	MethodCloseOwned             = "CloseOwned"
	MethodCloseFresh             = "CloseFresh"
	MethodSetPaneTitle           = "SetPaneTitle"
	MethodSetPaneLabel           = "SetPaneLabel"
	MethodEnablePaneBorderTitles = "EnablePaneBorderTitles"
	MethodSetPaneProjectRoot     = "SetPaneProjectRoot"
	MethodSetPaneWorktreePath    = "SetPaneWorktreePath"
	MethodStampPaneShellKey      = "StampPaneShellKey"
	MethodPreviewLaunch          = "PreviewLaunch"
	MethodRelayout               = "Relayout"

	MethodListLiveForIdentity = "ListLiveForIdentity"
	MethodListPanes           = "ListPanes"
	MethodServerStartTime     = "ServerStartTime"
	MethodPaneStartTime       = "PaneStartTime"
	MethodCanonicalPaneLabel  = "CanonicalPaneLabel"

	MethodCurrentClientSize            = "CurrentClientSize"
	MethodPaneGeometryForPane          = "PaneGeometryForPane"
	MethodShowPopup                    = "ShowPopup"
	MethodNotifyViewer                 = "NotifyViewer"
	MethodBindDashboardShortcuts       = "BindDashboardShortcuts"
	MethodUnbindDashboardShortcuts     = "UnbindDashboardShortcuts"
	MethodBindConsoleShortcuts         = "BindConsoleShortcuts"
	MethodUnbindConsoleShortcuts       = "UnbindConsoleShortcuts"
	MethodBindWorktreeActionShortcut   = "BindWorktreeActionShortcut"
	MethodUnbindWorktreeActionShortcut = "UnbindWorktreeActionShortcut"
	MethodFocusPaneForViewer           = "FocusPaneForViewer"

	MethodInsideSession                = "InsideSession"
	MethodCurrentSession               = "CurrentSession"
	MethodHasSession                   = "HasSession"
	MethodNewSession                   = "NewSession"
	MethodNewWindow                    = "NewWindow"
	MethodRunCommandInPane             = "RunCommandInPane"
	MethodFocusPaneInSession           = "FocusPaneInSession"
	MethodAttachOrSwitch               = "AttachOrSwitch"
	MethodEnableInputExtensions        = "EnableInputExtensions"
	MethodEnableInputExtensionsForTerm = "EnableInputExtensionsForTerm"
	MethodActivePaneInWindow           = "ActivePaneInWindow"
	MethodPaneTitle                    = "PaneTitle"
	MethodSetPaneRole                  = "SetPaneRole"

	MethodSetPaneAgentState = "SetPaneAgentState"
	MethodCapturePlanSource = "CapturePlanSource"
)

// Call is one recorded invocation. Args holds the method's arguments in
// declaration order, so an assertion reads them back with their contract types.
type Call struct {
	Method string
	Args   []any
}

// PaneValue is one recorded (paneID, value) metadata write: a pane title, a
// border label, a dashboard hint, or a liveness token.
type PaneValue struct {
	PaneID string
	Value  string
}

// Fake is the bare backend: it implements backend.Backend and no capability,
// so every backend.As* probe against it reports absence. Use it directly for
// the "backend without this capability" cases and wrap it in one of the shapes
// in capability.go otherwise.
//
// ListPanes is the one capability method that lives here rather than on a
// mixin. Both RestoreOps and ConsoleHost name that same observation, and a
// shape carrying both mixins would find it at equal depth in each and satisfy
// neither. Holding it on the shared base leaves it at depth 1 for every shape.
type Fake struct {
	name         backend.Name
	mutation     backend.MutationModel
	panes        []string
	launchErr    error
	stampErr     error
	freshErr     error
	ownedResult  backend.CloseResult
	ownedErr     error
	previewLines []string

	livePanes       []backend.LivePane
	livePanesErr    error
	identityPanes   []backend.LivePane
	identityErr     error
	targetPanes     []backend.PaneInfo
	targetPanesErr  error
	serverStart     time.Time
	serverStartErr  error
	paneStart       func(paneID string) (time.Time, error)
	clientSize      backend.ClientSize
	clientSizeErr   error
	paneGeometry    backend.PaneGeometry
	paneGeometryErr error
	popupErr        error
	shortcutErr     error
	focusViewerErr  error

	currentSession   string
	existingSessions []string
	windowPane       backend.PaneInfo
	activePane       string
	activePaneErr    error
	paneTitle        string
	paneTitleErr     error
	consoleErr       error
	agentStateErr    error
	planSource       string
	planSourceErr    error

	mu        sync.Mutex
	calls     []Call
	launchIdx int
}

var _ backend.Backend = (*Fake)(nil)

// Option configures a Fake before any shape wraps it.
type Option func(*Fake)

// New returns a bare fake with no capabilities. Defaults: the tmux backend
// name, the atomic mutation model, pane id "%1" for every launch, and no
// configured failure.
func New(opts ...Option) *Fake {
	f := &Fake{name: backend.Tmux, mutation: backend.MutationAtomic, panes: []string{"%1"}}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// WithName sets the name the fake reports, which decides the backend recorded
// on every PaneRef it returns. It deliberately leaves the mutation model
// alone: the two are independent properties, and a test that needs the
// journaled lane says so with WithMutationModel.
func WithName(name backend.Name) Option {
	return func(f *Fake) { f.name = name }
}

// WithMutationModel sets the realization strategy the fake declares, so a test
// can drive the journaled launch lane without a live herdr server.
func WithMutationModel(model backend.MutationModel) Option {
	return func(f *Fake) { f.mutation = model }
}

// WithPanes sets the pane ids successive launches return. The last id repeats
// once the list is exhausted, so a single-id fake serves any launch count.
func WithPanes(paneIDs ...string) Option {
	return func(f *Fake) {
		if len(paneIDs) > 0 {
			f.panes = paneIDs
		}
	}
}

// WithLaunchError fails every Launch.
func WithLaunchError(err error) Option {
	return func(f *Fake) { f.launchErr = err }
}

// WithStampError fails the LivenessStamper capability. It is meaningful only on
// a shape that carries that capability.
func WithStampError(err error) Option {
	return func(f *Fake) { f.stampErr = err }
}

// WithFreshCloseError fails the FreshCloser capability.
func WithFreshCloseError(err error) Option {
	return func(f *Fake) { f.freshErr = err }
}

// WithOwnedClose sets the result and error the OwnedCloser capability returns.
// The zero result is backend.CloseFailed, which is what an unconfigured fake
// reports.
func WithOwnedClose(result backend.CloseResult, err error) Option {
	return func(f *Fake) {
		f.ownedResult = result
		f.ownedErr = err
	}
}

// WithPreviewLines sets the lines the DryRunPreviewer capability returns.
func WithPreviewLines(lines ...string) Option {
	return func(f *Fake) { f.previewLines = lines }
}

// WithLivePanes sets the observation ListLive reports. Unset, the fake reports
// no live panes.
func WithLivePanes(panes ...backend.LivePane) Option {
	return func(f *Fake) { f.livePanes = panes }
}

// WithListLiveError fails every ListLive.
func WithListLiveError(err error) Option {
	return func(f *Fake) { f.livePanesErr = err }
}

// WithIdentityPanes sets the observation the RestoreOps strict sweep reports,
// and the error it reports instead. It is deliberately separate from
// WithLivePanes: the two sweeps have different failure contracts, and a test
// that pins the strict one must be able to fail it alone.
func WithIdentityPanes(panes []backend.LivePane, err error) Option {
	return func(f *Fake) {
		f.identityPanes = panes
		f.identityErr = err
	}
}

// WithTargetPanes sets the pane listing the RestoreOps target-scoped query
// reports, and the error it reports instead.
func WithTargetPanes(panes []backend.PaneInfo, err error) Option {
	return func(f *Fake) {
		f.targetPanes = panes
		f.targetPanesErr = err
	}
}

// WithServerStartTime sets the runtime generation start the RestoreOps
// capability reports, and the error it reports instead. The zero instant is
// what an unconfigured fake reports, which declines every adoption.
func WithServerStartTime(at time.Time, err error) Option {
	return func(f *Fake) {
		f.serverStart = at
		f.serverStartErr = err
	}
}

// WithPaneStartTime sets the per-pane provenance clock the RestoreOps
// capability answers with. A nil function reports the zero instant.
func WithPaneStartTime(fn func(paneID string) (time.Time, error)) Option {
	return func(f *Fake) { f.paneStart = fn }
}

// WithClientSize sets the viewer size the PopupHost capability measures, and
// the error it reports instead. It is meaningful only on a shape that carries
// that capability.
func WithClientSize(size backend.ClientSize, err error) Option {
	return func(f *Fake) {
		f.clientSize = size
		f.clientSizeErr = err
	}
}

// WithPaneGeometry sets the pane bounds the PopupHost capability measures, and
// the error it reports instead.
func WithPaneGeometry(geometry backend.PaneGeometry, err error) Option {
	return func(f *Fake) {
		f.paneGeometry = geometry
		f.paneGeometryErr = err
	}
}

// WithPopupError fails every ShowPopup.
func WithPopupError(err error) Option {
	return func(f *Fake) { f.popupErr = err }
}

// WithShortcutError fails every bind and unbind on the ShortcutBinder
// capability, the way an absent or too-old runtime server does.
func WithShortcutError(err error) Option {
	return func(f *Fake) { f.shortcutErr = err }
}

// WithFocusViewerError fails the ConsoleFocus capability.
func WithFocusViewerError(err error) Option {
	return func(f *Fake) { f.focusViewerErr = err }
}

// WithConsoleSession sets the session the ConsoleHost capability reports the
// calling process to be running in. Empty (the default) means the process is
// outside every session, which is what sends the console down its bootstrap
// path.
func WithConsoleSession(session string) Option {
	return func(f *Fake) { f.currentSession = session }
}

// WithExistingSessions names the sessions ConsoleHost.HasSession answers true
// for.
func WithExistingSessions(sessions ...string) Option {
	return func(f *Fake) { f.existingSessions = sessions }
}

// WithConsoleWindowPane sets the pane ConsoleHost.NewWindow returns.
func WithConsoleWindowPane(pane backend.PaneInfo) Option {
	return func(f *Fake) { f.windowPane = pane }
}

// WithActivePane sets the pane ConsoleHost.ActivePaneInWindow reports, and the
// error it reports instead.
func WithActivePane(paneID string, err error) Option {
	return func(f *Fake) {
		f.activePane = paneID
		f.activePaneErr = err
	}
}

// WithPaneTitle sets the title ConsoleHost.PaneTitle reads back, and the error
// it reports instead.
func WithPaneTitle(title string, err error) Option {
	return func(f *Fake) {
		f.paneTitle = title
		f.paneTitleErr = err
	}
}

// WithConsoleError fails the ConsoleHost calls that mutate a session — session
// and window creation, the console relaunch, focus, attach, and the role stamp.
// The observations have their own options because a test usually needs one to
// fail while the rest keep answering.
func WithConsoleError(err error) Option {
	return func(f *Fake) { f.consoleErr = err }
}

// WithAgentStateError fails the AgentStateReporter capability, the way a pane
// that disappeared under the controller reporting on it does.
func WithAgentStateError(err error) Option {
	return func(f *Fake) { f.agentStateErr = err }
}

// WithPlanSource sets the transcript the PlanCapture capability returns, and
// the error it returns instead.
func WithPlanSource(source string, err error) Option {
	return func(f *Fake) {
		f.planSource = source
		f.planSourceErr = err
	}
}

// Name reports the configured backend name.
func (f *Fake) Name() backend.Name { return f.name }

// MutationModel reports the configured realization strategy (atomic unless a
// test asked for another one).
func (f *Fake) MutationModel() backend.MutationModel { return f.mutation }

// CheckAvailable always succeeds; an in-process fake is always installed.
func (f *Fake) CheckAvailable() error {
	f.record(MethodCheckAvailable)
	return nil
}

// Launch records the request and returns the next configured pane id, scoped to
// the workspace the caller asked for. Recording and pane-id consumption happen
// under one lock acquisition so Launches()[i] pairs with the i-th returned ref
// even under concurrent callers.
//
// Contract divergence, on purpose: the returned ref carries req.Workspace so a
// workspace-scoped test can assert on it, while the real tmux adapter always
// returns an empty Workspace. Code must not start reading Workspace off a
// Launch ref on the strength of this fake alone.
func (f *Fake) Launch(req backend.LaunchRequest) (backend.PaneRef, error) {
	pane := f.recordLaunch(req)
	if f.launchErr != nil {
		return backend.PaneRef{}, f.launchErr
	}
	return backend.PaneRef{Backend: f.name, Workspace: req.Workspace, Pane: pane}, nil
}

func (f *Fake) recordLaunch(req backend.LaunchRequest) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Call{Method: MethodLaunch, Args: []any{req}})
	idx := min(f.launchIdx, len(f.panes)-1)
	f.launchIdx++
	return f.panes[idx]
}

// ReleaseStartGate records the released gate.
func (f *Fake) ReleaseStartGate(gate string) error {
	f.record(MethodReleaseStartGate, gate)
	return nil
}

// ListLive reports the observation WithLivePanes configured. Unset, it reports
// none, which is what the launch orchestration this fake was first written for
// expects.
func (f *Fake) ListLive() ([]backend.LivePane, error) {
	f.record(MethodListLive)
	return f.livePanes, f.livePanesErr
}

// ListPanes records the target-scoped listing and returns WithTargetPanes. It
// serves both the RestoreOps and the ConsoleHost shapes; see the note on Fake.
func (f *Fake) ListPanes(target string) ([]backend.PaneInfo, error) {
	f.record(MethodListPanes, target)
	return f.targetPanes, f.targetPanesErr
}

// Read returns empty pane output.
func (f *Fake) Read(ref backend.PaneRef, lines int) (string, error) {
	f.record(MethodRead, ref, lines)
	return "", nil
}

// SendLine records the line sent to a pane.
func (f *Fake) SendLine(ref backend.PaneRef, line string) error {
	f.record(MethodSendLine, ref, line)
	return nil
}

// Focus records the focused pane.
func (f *Fake) Focus(ref backend.PaneRef) error {
	f.record(MethodFocus, ref)
	return nil
}

// Close records the closed pane.
func (f *Fake) Close(ref backend.PaneRef) error {
	f.record(MethodClose, ref)
	return nil
}

func (f *Fake) record(method string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Call{Method: method, Args: args})
}
