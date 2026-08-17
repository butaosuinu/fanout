package backendtest

import "github.com/butaosuinu/fanout/internal/core/backend"

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

// CloseFresh records the rollback close and applies WithFreshCloseError.
func (m freshCloseMixin) CloseFresh(ref backend.PaneRef) error {
	m.record(MethodCloseFresh, ref)
	return m.freshErr
}

type ownedCloseMixin struct{ *Fake }

// CloseOwned records the identity-checked close request and applies
// WithOwnedClose.
func (m ownedCloseMixin) CloseOwned(req backend.CloseRequest) (backend.CloseResult, error) {
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

// TmuxFake carries every capability, matching the shape of the tmux backend.
type TmuxFake struct {
	*Fake
	decorateMixin
	stampMixin
	freshCloseMixin
	ownedCloseMixin
	previewMixin
	layoutMixin
}

var (
	_ backend.PaneDecorator   = DecoratorFake{}
	_ backend.FreshCloser     = FreshCloserFake{}
	_ backend.LivenessStamper = LivenessFake{}
	_ backend.FreshCloser     = LivenessFake{}
	_ backend.PaneDecorator   = TmuxFake{}
	_ backend.LivenessStamper = TmuxFake{}
	_ backend.FreshCloser     = TmuxFake{}
	_ backend.OwnedCloser     = TmuxFake{}
	_ backend.DryRunPreviewer = TmuxFake{}
	_ backend.LayoutManager   = TmuxFake{}
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

// NewTmux returns a fully capable, tmux-shaped fake.
func NewTmux(opts ...Option) *TmuxFake {
	f := New(opts...)
	return &TmuxFake{
		Fake:            f,
		decorateMixin:   decorateMixin{f},
		stampMixin:      stampMixin{f},
		freshCloseMixin: freshCloseMixin{f},
		ownedCloseMixin: ownedCloseMixin{f},
		previewMixin:    previewMixin{f},
		layoutMixin:     layoutMixin{f},
	}
}
