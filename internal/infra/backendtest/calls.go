package backendtest

import "github.com/butaosuinu/fanout/internal/core/backend"

// Calls returns the recorded invocations in call order, including the ones the
// capability mixins made.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// Methods returns the recorded method names in call order, for assertions that
// pin the order of a launch sequence rather than its arguments.
func (f *Fake) Methods() []string {
	calls := f.Calls()
	names := make([]string, len(calls))
	for i, call := range calls {
		names[i] = call.Method
	}
	return names
}

// Launches returns every recorded launch request.
func (f *Fake) Launches() []backend.LaunchRequest {
	return argAt[backend.LaunchRequest](f.Calls(), MethodLaunch, 0)
}

// ReleasedGates returns every start gate the launcher released.
func (f *Fake) ReleasedGates() []string {
	return argAt[string](f.Calls(), MethodReleaseStartGate, 0)
}

// CloseRequests returns every identity-checked close the OwnedCloser capability
// received.
func (f *Fake) CloseRequests() []backend.CloseRequest {
	return argAt[backend.CloseRequest](f.Calls(), MethodCloseOwned, 0)
}

// ClosedRefs returns the panes closed through Close and CloseFresh, in call
// order. The identity-checked OwnedCloser lane is separate: see CloseRequests.
func (f *Fake) ClosedRefs() []backend.PaneRef {
	var refs []backend.PaneRef
	for _, call := range f.Calls() {
		if call.Method != MethodClose && call.Method != MethodCloseFresh {
			continue
		}
		if ref, ok := call.Args[0].(backend.PaneRef); ok {
			refs = append(refs, ref)
		}
	}
	return refs
}

// PaneValues returns the (paneID, value) pairs recorded for a two-string
// metadata method such as MethodSetPaneProjectRoot or MethodStampPaneShellKey.
// It is nil when the method was never called, so a table-driven want of nil
// compares equal.
func (f *Fake) PaneValues(method string) []PaneValue {
	calls := f.Calls()
	paneIDs := argAt[string](calls, method, 0)
	values := argAt[string](calls, method, 1)
	var pairs []PaneValue
	for i := range paneIDs {
		pairs = append(pairs, PaneValue{PaneID: paneIDs[i], Value: values[i]})
	}
	return pairs
}

// argAt returns the index-th argument of every call to method. An argument of
// another type is skipped, which cannot happen while the fake records its own
// signatures.
func argAt[T any](calls []Call, method string, index int) []T {
	var out []T
	for _, call := range calls {
		if call.Method != method || index >= len(call.Args) {
			continue
		}
		if value, ok := call.Args[index].(T); ok {
			out = append(out, value)
		}
	}
	return out
}
