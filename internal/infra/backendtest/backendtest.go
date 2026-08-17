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

// ListLive reports no live panes; liveness observation is out of scope for the
// launch orchestration this fake serves.
func (f *Fake) ListLive() ([]backend.LivePane, error) {
	f.record(MethodListLive)
	return nil, nil
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
