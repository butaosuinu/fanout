package backendtest

import "testing"

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
