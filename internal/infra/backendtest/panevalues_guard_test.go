package backendtest

import (
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
)

// TestPaneValuesToleratesSingleArgMethods pins that PaneValues degrades to an
// empty pair list for methods recorded with fewer than two string arguments
// instead of panicking.
func TestPaneValuesToleratesSingleArgMethods(t *testing.T) {
	f := NewTmux()
	if err := f.EnablePaneBorderTitles("%7"); err != nil {
		t.Fatalf("EnablePaneBorderTitles(%%7) = %v, want nil", err)
	}
	if got := f.PaneValues(MethodEnablePaneBorderTitles); got != nil {
		t.Fatalf("PaneValues(MethodEnablePaneBorderTitles) = %v, want nil", got)
	}
}

// TestCloseAcceptsLegacyEmptyBackendRef pins that the fake normalizes the
// legacy empty backend name exactly as the real adapters do, so legacy-row
// cleanup tests see production behavior.
func TestCloseAcceptsLegacyEmptyBackendRef(t *testing.T) {
	f := NewTmux()
	if err := f.CloseFresh(backend.PaneRef{Pane: "%9"}); err != nil {
		t.Fatalf("CloseFresh(legacy empty backend) = %v, want nil", err)
	}
	if _, err := f.CloseOwned(backend.CloseRequest{Ref: backend.PaneRef{Pane: "%9"}}); err != nil {
		t.Fatalf("CloseOwned(legacy empty backend) = %v, want nil", err)
	}
}
