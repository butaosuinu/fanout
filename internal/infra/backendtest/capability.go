package backendtest

import (
	"fmt"
	"slices"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
)

// Capability method sets are attached through the unexported mixins below.
// Each mixin embeds the same *Fake, so it records into the one shared call log;
// a shape embeds *Fake directly (depth 1) plus the mixins it wants, which both
// resolves the promoted base methods unambiguously and keeps the shape's method
// set to exactly the capabilities it names.
//
// Adding a combination means adding a shape struct and its constructor here. A
// single all-capability type is deliberately not reused for that, because the
// probes in core/backend are type assertions: a shape must not satisfy a
// capability it does not model.

type decorateMixin struct{ *Fake }

// SetPaneTitle records the pane title decoration.
func (m decorateMixin) SetPaneTitle(paneID, title string) error {
	m.record(MethodSetPaneTitle, paneID, title)
	return nil
}

// SetPaneLabel records the border label decoration.
func (m decorateMixin) SetPaneLabel(paneID, label string) error {
	m.record(MethodSetPaneLabel, paneID, label)
	return nil
}

// EnablePaneBorderTitles records the border-title enablement.
func (m decorateMixin) EnablePaneBorderTitles(paneID string) error {
	m.record(MethodEnablePaneBorderTitles, paneID)
	return nil
}

// SetPaneProjectRoot records the dashboard project-root hint.
func (m decorateMixin) SetPaneProjectRoot(paneID, projectRoot string) error {
	m.record(MethodSetPaneProjectRoot, paneID, projectRoot)
	return nil
}

// SetPaneWorktreePath records the worktree-path hint.
func (m decorateMixin) SetPaneWorktreePath(paneID, worktreePath string) error {
	m.record(MethodSetPaneWorktreePath, paneID, worktreePath)
	return nil
}

type stampMixin struct{ *Fake }

// StampPaneShellKey records the liveness stamp and applies WithStampError.
func (m stampMixin) StampPaneShellKey(paneID, shellKey string) error {
	m.record(MethodStampPaneShellKey, paneID, shellKey)
	return m.stampErr
}

type freshCloseMixin struct{ *Fake }

// CloseFresh records the rollback close and applies WithFreshCloseError. A ref
// naming another runtime is rejected before recording, mirroring the real
// adapters' wrong-backend screening so routing bugs cannot pass only in tests.
// The legacy empty backend normalizes exactly as the real adapters normalize
// it, so a fake accepts the same state.json compatibility shape production
// accepts.
func (m freshCloseMixin) CloseFresh(ref backend.PaneRef) error {
	if backend.NormalizeName(ref.Backend) != m.name {
		return fmt.Errorf("%s backend cannot close %s pane reference %s", m.name, ref.Backend, ref.Pane)
	}
	m.record(MethodCloseFresh, ref)
	return m.freshErr
}

type ownedCloseMixin struct{ *Fake }

// CloseOwned records the identity-checked close request and applies
// WithOwnedClose. A ref naming another runtime is rejected before recording,
// mirroring the real adapters' wrong-backend screening, with the legacy empty
// backend normalized exactly as the real adapters normalize it.
func (m ownedCloseMixin) CloseOwned(req backend.CloseRequest) (backend.CloseResult, error) {
	if backend.NormalizeName(req.Ref.Backend) != m.name {
		return backend.CloseResult{}, fmt.Errorf("%s backend cannot close %s pane reference %s", m.name, req.Ref.Backend, req.Ref.Pane)
	}
	m.record(MethodCloseOwned, req)
	return m.ownedResult, m.ownedErr
}

type previewMixin struct{ *Fake }

// PreviewLaunch records the preview request and returns WithPreviewLines.
func (m previewMixin) PreviewLaunch(preview backend.LaunchPreview) []string {
	m.record(MethodPreviewLaunch, preview)
	return m.previewLines
}

type layoutMixin struct{ *Fake }

// Relayout records the layout repair request.
func (m layoutMixin) Relayout(target string, trigger backend.LayoutTrigger) error {
	m.record(MethodRelayout, target, trigger)
	return nil
}

type restoreMixin struct{ *Fake }

// ListLiveForIdentity records the strict sweep and returns WithIdentityPanes.
func (m restoreMixin) ListLiveForIdentity() ([]backend.LivePane, error) {
	m.record(MethodListLiveForIdentity)
	return m.identityPanes, m.identityErr
}

// ServerStartTime records the generation-clock read and returns
// WithServerStartTime.
func (m restoreMixin) ServerStartTime() (time.Time, error) {
	m.record(MethodServerStartTime)
	return m.serverStart, m.serverStartErr
}

// PaneStartTime records the per-pane provenance read and answers with
// WithPaneStartTime.
func (m restoreMixin) PaneStartTime(paneID string) (time.Time, error) {
	m.record(MethodPaneStartTime, paneID)
	if m.paneStart == nil {
		return time.Time{}, nil
	}
	return m.paneStart(paneID)
}

// CanonicalPaneLabel records the query and returns the label unchanged: the
// fake stores labels verbatim, so its canonical form is the input.
func (m restoreMixin) CanonicalPaneLabel(label string) string {
	m.record(MethodCanonicalPaneLabel, label)
	return label
}

type popupMixin struct{ *Fake }

// CurrentClientSize records the measurement and returns WithClientSize.
func (m popupMixin) CurrentClientSize() (backend.ClientSize, error) {
	m.record(MethodCurrentClientSize)
	return m.clientSize, m.clientSizeErr
}

// PaneGeometryForPane records the measured pane and returns WithPaneGeometry.
func (m popupMixin) PaneGeometryForPane(paneID string) (backend.PaneGeometry, error) {
	m.record(MethodPaneGeometryForPane, paneID)
	return m.paneGeometry, m.paneGeometryErr
}

// ShowPopup records the popup invocation and applies WithPopupError.
func (m popupMixin) ShowPopup(opts backend.PopupOptions) error {
	m.record(MethodShowPopup, opts)
	return m.popupErr
}

// NotifyViewer records the viewer-scoped status line message.
func (m popupMixin) NotifyViewer(viewerID, message string) error {
	m.record(MethodNotifyViewer, viewerID, message)
	return nil
}

type shortcutMixin struct{ *Fake }

// BindDashboardShortcuts records the dashboard shortcut registration.
func (m shortcutMixin) BindDashboardShortcuts(prefixKey, directKey, fanoutBin string) error {
	m.record(MethodBindDashboardShortcuts, prefixKey, directKey, fanoutBin)
	return m.shortcutErr
}

// UnbindDashboardShortcuts records the dashboard shortcut removal.
func (m shortcutMixin) UnbindDashboardShortcuts(prefixKey, directKey string) error {
	m.record(MethodUnbindDashboardShortcuts, prefixKey, directKey)
	return m.shortcutErr
}

// BindConsoleShortcuts records the console-return shortcut registration.
func (m shortcutMixin) BindConsoleShortcuts(prefixKey, directKey, fanoutBin string) error {
	m.record(MethodBindConsoleShortcuts, prefixKey, directKey, fanoutBin)
	return m.shortcutErr
}

// UnbindConsoleShortcuts records the console-return shortcut removal.
func (m shortcutMixin) UnbindConsoleShortcuts(prefixKey, directKey string) error {
	m.record(MethodUnbindConsoleShortcuts, prefixKey, directKey)
	return m.shortcutErr
}

// BindWorktreeActionShortcut records the worktree-action shortcut registration.
func (m shortcutMixin) BindWorktreeActionShortcut(key, fanoutBin string) error {
	m.record(MethodBindWorktreeActionShortcut, key, fanoutBin)
	return m.shortcutErr
}

// UnbindWorktreeActionShortcut records the worktree-action shortcut removal.
func (m shortcutMixin) UnbindWorktreeActionShortcut(key string) error {
	m.record(MethodUnbindWorktreeActionShortcut, key)
	return m.shortcutErr
}

type consoleFocusMixin struct{ *Fake }

// FocusPaneForViewer records the viewer-scoped focus switch.
func (m consoleFocusMixin) FocusPaneForViewer(viewerID string, ref backend.PaneRef) error {
	m.record(MethodFocusPaneForViewer, viewerID, ref)
	return m.focusViewerErr
}

type consoleMixin struct{ *Fake }

// InsideSession reports whether WithConsoleSession named a session.
func (m consoleMixin) InsideSession() bool {
	m.record(MethodInsideSession)
	return m.currentSession != ""
}

// CurrentSession records the query and returns WithConsoleSession.
func (m consoleMixin) CurrentSession() (string, error) {
	m.record(MethodCurrentSession)
	return m.currentSession, m.consoleErr
}

// HasSession records the query and answers from WithExistingSessions.
func (m consoleMixin) HasSession(session string) bool {
	m.record(MethodHasSession, session)
	return slices.Contains(m.existingSessions, session)
}

// NewSession records the session creation and applies WithConsoleError.
func (m consoleMixin) NewSession(session, startDir string) error {
	m.record(MethodNewSession, session, startDir)
	return m.consoleErr
}

// NewWindow records the container creation and returns WithConsoleWindowPane.
func (m consoleMixin) NewWindow(session, title, startDir string) (backend.PaneInfo, error) {
	m.record(MethodNewWindow, session, title, startDir)
	return m.windowPane, m.consoleErr
}

// RunCommandInPane records the command submitted to a pane's shell.
func (m consoleMixin) RunCommandInPane(paneID, command string) error {
	m.record(MethodRunCommandInPane, paneID, command)
	return m.consoleErr
}

// FocusPaneInSession records the pane brought into view inside its session.
func (m consoleMixin) FocusPaneInSession(pane backend.PaneInfo) error {
	m.record(MethodFocusPaneInSession, pane)
	return m.consoleErr
}

// AttachOrSwitch records the terminal attach and applies WithConsoleError.
func (m consoleMixin) AttachOrSwitch(session string) error {
	m.record(MethodAttachOrSwitch, session)
	return m.consoleErr
}

// EnableInputExtensions records the modified-key request for the attached
// terminal.
func (m consoleMixin) EnableInputExtensions() {
	m.record(MethodEnableInputExtensions)
}

// EnableInputExtensionsForTerm records the same request for a named terminal.
func (m consoleMixin) EnableInputExtensionsForTerm(term string) {
	m.record(MethodEnableInputExtensionsForTerm, term)
}

// ActivePaneInWindow records the focus query and returns WithActivePane.
func (m consoleMixin) ActivePaneInWindow(paneID string) (string, error) {
	m.record(MethodActivePaneInWindow, paneID)
	return m.activePane, m.activePaneErr
}

// PaneTitle records the title read-back and returns WithPaneTitle.
func (m consoleMixin) PaneTitle(paneID string) (string, error) {
	m.record(MethodPaneTitle, paneID)
	return m.paneTitle, m.paneTitleErr
}

// SetPaneRole records the layout-role stamp.
func (m consoleMixin) SetPaneRole(paneID, role string) error {
	m.record(MethodSetPaneRole, paneID, role)
	return m.consoleErr
}

type paneSelfMixin struct{ *Fake }

// SetPaneAgentState records the state a pane published about itself.
func (m paneSelfMixin) SetPaneAgentState(paneID, state string) error {
	m.record(MethodSetPaneAgentState, paneID, state)
	return m.agentStateErr
}

// CapturePlanSource records the transcript request and returns WithPlanSource.
func (m paneSelfMixin) CapturePlanSource(paneID string, lines int) (string, error) {
	m.record(MethodCapturePlanSource, paneID, lines)
	return m.planSource, m.planSourceErr
}

// DecoratorFake is a backend whose only capability is PaneDecorator.
type DecoratorFake struct {
	*Fake
	decorateMixin
}

// FreshCloserFake is a backend whose only capability is FreshCloser: it can
// roll a just-launched pane back, but it cannot stamp a durable identity, so a
// caller that requires one must fail closed against it.
type FreshCloserFake struct {
	*Fake
	freshCloseMixin
}

// LivenessFake carries the pair the strict launch lane needs: LivenessStamper
// to stamp the pane, and FreshCloser to roll it back when the stamp fails.
type LivenessFake struct {
	*Fake
	stampMixin
	freshCloseMixin
}

// HostFake carries the three console host capabilities and none of the launch
// ones: it is the shape a runtime that only lends its terminal to fanout's own
// console has.
type HostFake struct {
	*Fake
	popupMixin
	shortcutMixin
	consoleFocusMixin
}

// RestoreFake carries exactly the capabilities console restore calls: the
// restore observations plus the stamp, rollback, identity-checked close,
// decoration, and relayout it drives them with. It stops short of the console
// host set so a restore test cannot accidentally depend on a popup.
type RestoreFake struct {
	*Fake
	restoreMixin
	decorateMixin
	stampMixin
	freshCloseMixin
	ownedCloseMixin
	layoutMixin
}

// ConsoleFake carries exactly the capabilities the resident console's own
// session entry and pane bookkeeping call. It stops short of restore and the
// popup set so a console-entry test cannot accidentally depend on either.
type ConsoleFake struct {
	*Fake
	consoleMixin
	decorateMixin
	ownedCloseMixin
	layoutMixin
}

// PaneSelfFake carries the two capabilities a controller running inside a pane
// uses about that pane, and nothing else: it is the shape a self-exec process
// resolves, which never launches or lays anything out.
type PaneSelfFake struct {
	*Fake
	paneSelfMixin
}

// TmuxFake carries every capability, matching the shape of the tmux backend.
type TmuxFake struct {
	*Fake
	restoreMixin
	consoleMixin
	paneSelfMixin
	decorateMixin
	stampMixin
	freshCloseMixin
	ownedCloseMixin
	previewMixin
	layoutMixin
	popupMixin
	shortcutMixin
	consoleFocusMixin
}

var (
	_ backend.PaneDecorator   = DecoratorFake{}
	_ backend.FreshCloser     = FreshCloserFake{}
	_ backend.LivenessStamper = LivenessFake{}
	_ backend.FreshCloser     = LivenessFake{}
	_ backend.PopupHost       = HostFake{}
	_ backend.ShortcutBinder  = HostFake{}
	_ backend.ConsoleFocus    = HostFake{}
	_ backend.RestoreOps      = RestoreFake{}
	_ backend.PaneDecorator   = RestoreFake{}
	_ backend.LivenessStamper = RestoreFake{}
	_ backend.FreshCloser     = RestoreFake{}
	_ backend.OwnedCloser     = RestoreFake{}
	_ backend.LayoutManager   = RestoreFake{}
	_ backend.ConsoleHost     = ConsoleFake{}
	_ backend.PaneDecorator   = ConsoleFake{}
	_ backend.OwnedCloser     = ConsoleFake{}
	_ backend.LayoutManager   = ConsoleFake{}

	_ backend.AgentStateReporter = PaneSelfFake{}
	_ backend.PlanCapture        = PaneSelfFake{}

	_ backend.RestoreOps         = TmuxFake{}
	_ backend.ConsoleHost        = TmuxFake{}
	_ backend.AgentStateReporter = TmuxFake{}
	_ backend.PlanCapture        = TmuxFake{}
	_ backend.PaneDecorator      = TmuxFake{}
	_ backend.LivenessStamper    = TmuxFake{}
	_ backend.FreshCloser        = TmuxFake{}
	_ backend.OwnedCloser        = TmuxFake{}
	_ backend.DryRunPreviewer    = TmuxFake{}
	_ backend.LayoutManager      = TmuxFake{}
	_ backend.PopupHost          = TmuxFake{}
	_ backend.ShortcutBinder     = TmuxFake{}
	_ backend.ConsoleFocus       = TmuxFake{}
)

// NewDecorator returns a fake that decorates panes and nothing else.
func NewDecorator(opts ...Option) *DecoratorFake {
	f := New(opts...)
	return &DecoratorFake{Fake: f, decorateMixin: decorateMixin{f}}
}

// NewFreshCloser returns a fake that can only roll a fresh pane back.
func NewFreshCloser(opts ...Option) *FreshCloserFake {
	f := New(opts...)
	return &FreshCloserFake{Fake: f, freshCloseMixin: freshCloseMixin{f}}
}

// NewLiveness returns a fake that stamps liveness tokens and rolls fresh panes
// back.
func NewLiveness(opts ...Option) *LivenessFake {
	f := New(opts...)
	return &LivenessFake{Fake: f, stampMixin: stampMixin{f}, freshCloseMixin: freshCloseMixin{f}}
}

// NewHost returns a fake that lends its terminal to fanout's console — popups,
// global shortcuts, and viewer-scoped focus — and carries no launch capability.
func NewHost(opts ...Option) *HostFake {
	f := New(opts...)
	return &HostFake{
		Fake:              f,
		popupMixin:        popupMixin{f},
		shortcutMixin:     shortcutMixin{f},
		consoleFocusMixin: consoleFocusMixin{f},
	}
}

// NewRestore returns a fake carrying the capability set console restore
// requires and nothing else.
func NewRestore(opts ...Option) *RestoreFake {
	f := New(opts...)
	return &RestoreFake{
		Fake:            f,
		restoreMixin:    restoreMixin{f},
		decorateMixin:   decorateMixin{f},
		stampMixin:      stampMixin{f},
		freshCloseMixin: freshCloseMixin{f},
		ownedCloseMixin: ownedCloseMixin{f},
		layoutMixin:     layoutMixin{f},
	}
}

// NewConsole returns a fake carrying the capability set the resident console's
// session entry requires and nothing else.
func NewConsole(opts ...Option) *ConsoleFake {
	f := New(opts...)
	return &ConsoleFake{
		Fake:            f,
		consoleMixin:    consoleMixin{f},
		decorateMixin:   decorateMixin{f},
		ownedCloseMixin: ownedCloseMixin{f},
		layoutMixin:     layoutMixin{f},
	}
}

// NewPaneSelf returns a fake that only answers for the pane a controller is
// running in: its state report and its rendered transcript.
func NewPaneSelf(opts ...Option) *PaneSelfFake {
	f := New(opts...)
	return &PaneSelfFake{Fake: f, paneSelfMixin: paneSelfMixin{f}}
}

// NewTmux returns a fully capable, tmux-shaped fake.
func NewTmux(opts ...Option) *TmuxFake {
	f := New(opts...)
	return &TmuxFake{
		Fake:              f,
		restoreMixin:      restoreMixin{f},
		consoleMixin:      consoleMixin{f},
		paneSelfMixin:     paneSelfMixin{f},
		decorateMixin:     decorateMixin{f},
		stampMixin:        stampMixin{f},
		freshCloseMixin:   freshCloseMixin{f},
		ownedCloseMixin:   ownedCloseMixin{f},
		previewMixin:      previewMixin{f},
		layoutMixin:       layoutMixin{f},
		popupMixin:        popupMixin{f},
		shortcutMixin:     shortcutMixin{f},
		consoleFocusMixin: consoleFocusMixin{f},
	}
}
