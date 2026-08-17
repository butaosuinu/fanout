package tmuxbackend

import (
	"fmt"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
)

type fakeOps struct {
	geom     backend.Geometry
	geomErr  error
	panes    []backend.WindowPane
	panesErr error
	applyErr error
	mainvErr error
	spacerID string

	panesCalls int
	applied    []string
	mainvCalls int
	tiledCalls int
	killed     []string
	splits     int
}

func (f *fakeOps) WindowGeometry(string) (backend.Geometry, error) { return f.geom, f.geomErr }
func (f *fakeOps) WindowPanes(string) ([]backend.WindowPane, error) {
	f.panesCalls++
	return f.panes, f.panesErr
}

func (f *fakeOps) SplitSpacer(string) (string, error) {
	f.splits++
	id := f.spacerID
	if id == "" {
		id = "%900"
	}
	return id, nil
}
func (f *fakeOps) KillPane(id string) error             { f.killed = append(f.killed, id); return nil }
func (f *fakeOps) ApplyLayout(_, l string) error        { f.applied = append(f.applied, l); return f.applyErr }
func (f *fakeOps) SelectMainVertical(string, int) error { f.mainvCalls++; return f.mainvErr }
func (f *fakeOps) SelectTiled(string) error             { f.tiledCalls++; return nil }

func newTestApplier(f *fakeOps) *layoutApplier {
	return &layoutApplier{ops: f, cfg: defaultLayoutConfig(), memo: map[string]string{}}
}

func wp(num string, idx int, role string, spacer bool) backend.WindowPane {
	return backend.WindowPane{ID: "%" + num, NumericID: num, Index: idx, Role: role, Spacer: spacer}
}

func TestApplyNoSidebarGrid(t *testing.T) {
	f := &fakeOps{
		geom:  backend.Geometry{WindowID: "@1", Width: 200, Height: 50},
		panes: []backend.WindowPane{wp("1079", 0, "", false), wp("1080", 1, "", false)},
	}
	a := newTestApplier(f)
	if err := a.apply("%1079", backend.LayoutCreate); err != nil {
		t.Fatal(err)
	}
	if len(f.applied) != 1 {
		t.Fatalf("applied = %v", f.applied)
	}
	want := "2cc9,200x50,0,0{100x50,0,0,1079,99x50,101,0,1080}"
	if f.applied[0] != want {
		t.Errorf("layout = %q, want %q", f.applied[0], want)
	}
	if f.mainvCalls != 0 || f.tiledCalls != 0 {
		t.Errorf("unexpected fallback: mainv=%d tiled=%d", f.mainvCalls, f.tiledCalls)
	}
}

func TestApplySidebarGrid(t *testing.T) {
	f := &fakeOps{
		geom: backend.Geometry{WindowID: "@1", Width: 200, Height: 50},
		panes: []backend.WindowPane{
			wp("1093", 0, backend.RoleConsole, false),
			wp("1094", 1, "", false),
			wp("1095", 2, "", false),
		},
	}
	a := newTestApplier(f)
	if err := a.apply("%1093", backend.LayoutCreate); err != nil {
		t.Fatal(err)
	}
	want := "fab2,200x50,0,0{40x50,0,0,1093,159x50,41,0{79x50,41,0,1094,79x50,121,0,1095}}"
	if len(f.applied) != 1 || f.applied[0] != want {
		t.Fatalf("layout = %v, want %q", f.applied, want)
	}
}

func TestApplyAddsSpacerWithConsole(t *testing.T) {
	// A console + 5 grid panes on a wide window picks a short, wide last row that
	// warrants a spacer; spacers are only created when a console is present.
	f := &fakeOps{
		geom:     backend.Geometry{WindowID: "@1", Width: 320, Height: 50},
		panes:    append([]backend.WindowPane{wp("c", 0, backend.RoleConsole, false)}, panesN(5)...),
		spacerID: "%9",
	}
	a := newTestApplier(f)
	if err := a.apply("%c", backend.LayoutCreate); err != nil {
		t.Fatal(err)
	}
	if f.splits != 1 {
		t.Fatalf("splits = %d, want 1", f.splits)
	}
	// Sidebar layout closes with the spacer cell, the content's [], then the root {}.
	if len(f.applied) != 1 || !strings.HasSuffix(f.applied[0], ",9}]}") {
		t.Errorf("spacer cell (id 9) not the trailing cell: %q", f.applied[0])
	}
}

func TestApplyNoSpacerWithoutConsole(t *testing.T) {
	// Same spacer-warranting geometry but no console (batch run): no resident
	// process would reconcile a spacer, so none is created.
	f := &fakeOps{
		geom:  backend.Geometry{WindowID: "@1", Width: 320, Height: 50},
		panes: panesN(5),
	}
	a := newTestApplier(f)
	if err := a.apply("%1", backend.LayoutCreate); err != nil {
		t.Fatal(err)
	}
	if f.splits != 0 {
		t.Errorf("splits = %d, want 0 (no spacer without a console)", f.splits)
	}
}

func TestApplyRemovesStaleSpacer(t *testing.T) {
	f := &fakeOps{
		geom: backend.Geometry{WindowID: "@1", Width: 200, Height: 50},
		panes: []backend.WindowPane{
			wp("1", 0, "", false),
			wp("2", 1, "", false),
			wp("8", 2, "", true), // stale spacer, no longer needed at this size
		},
	}
	a := newTestApplier(f)
	if err := a.apply("%1", backend.LayoutCreate); err != nil {
		t.Fatal(err)
	}
	if len(f.killed) != 1 || f.killed[0] != "%8" {
		t.Errorf("killed = %v, want [%%8]", f.killed)
	}
	if len(f.applied) != 1 || strings.Contains(f.applied[0], ",8") {
		t.Errorf("spacer still in layout: %q", f.applied[0])
	}
}

func TestApplyFallbackCascade(t *testing.T) {
	// Custom layout rejected → main-vertical.
	f := &fakeOps{
		geom:     backend.Geometry{WindowID: "@1", Width: 200, Height: 50},
		panes:    panesN(3),
		applyErr: fmt.Errorf("tmux rejected layout"),
	}
	a := newTestApplier(f)
	_ = a.apply("%1", backend.LayoutCreate)
	if f.mainvCalls != 1 || f.tiledCalls != 0 {
		t.Errorf("mainv=%d tiled=%d, want 1/0", f.mainvCalls, f.tiledCalls)
	}

	// Main-vertical also fails → tiled.
	f = &fakeOps{
		geom:     backend.Geometry{WindowID: "@1", Width: 200, Height: 50},
		panes:    panesN(3),
		applyErr: fmt.Errorf("rejected"),
		mainvErr: fmt.Errorf("rejected too"),
	}
	a = newTestApplier(f)
	_ = a.apply("%1", backend.LayoutCreate)
	if f.tiledCalls != 1 {
		t.Errorf("tiled = %d, want 1", f.tiledCalls)
	}
}

func TestApplyCrampedFallsBackToTiled(t *testing.T) {
	// A window too small for a comfortable grid skips the custom layout and uses
	// tiled (the layout that handles dense windows best), not main-vertical.
	f := &fakeOps{
		geom:  backend.Geometry{WindowID: "@1", Width: 50, Height: 10},
		panes: panesN(3),
	}
	a := newTestApplier(f)
	_ = a.apply("%1", backend.LayoutCreate)
	if len(f.applied) != 0 {
		t.Errorf("expected no custom layout for cramped window, got %v", f.applied)
	}
	if f.tiledCalls != 1 || f.mainvCalls != 0 {
		t.Errorf("cramped fallback = tiled %d / mainv %d, want 1/0", f.tiledCalls, f.mainvCalls)
	}
}

func TestApplyResizeMemoSkips(t *testing.T) {
	f := &fakeOps{
		geom:  backend.Geometry{WindowID: "@1", Width: 200, Height: 50},
		panes: panesN(2),
	}
	a := newTestApplier(f)
	if err := a.apply("%1", backend.LayoutCreate); err != nil {
		t.Fatal(err)
	}
	// Same geometry + pane set: a resize event should be a no-op.
	if err := a.apply("%1", backend.LayoutResize); err != nil {
		t.Fatal(err)
	}
	if len(f.applied) != 1 {
		t.Errorf("resize was not deduped: applied=%d", len(f.applied))
	}
	// A changed window size must reapply.
	f.geom.Width = 240
	if err := a.apply("%1", backend.LayoutResize); err != nil {
		t.Fatal(err)
	}
	if len(f.applied) != 2 {
		t.Errorf("resize after size change did not reapply: applied=%d", len(f.applied))
	}
}

func TestApplyCreateBypassesMemo(t *testing.T) {
	f := &fakeOps{
		geom:  backend.Geometry{WindowID: "@1", Width: 200, Height: 50},
		panes: panesN(2),
	}
	a := newTestApplier(f)
	_ = a.apply("%1", backend.LayoutCreate)
	_ = a.apply("%1", backend.LayoutCreate)
	if len(f.applied) != 2 {
		t.Errorf("LayoutCreate should not dedup: applied=%d", len(f.applied))
	}
}

func TestApplyWindowGone(t *testing.T) {
	f := &fakeOps{geomErr: fmt.Errorf("can't find window")}
	a := newTestApplier(f)
	if err := a.apply("%1", backend.LayoutCreate); err != nil {
		t.Fatal(err)
	}
	if f.panesCalls != 0 || len(f.applied) != 0 {
		t.Errorf("expected no-op for missing window: panesCalls=%d applied=%v", f.panesCalls, f.applied)
	}
}

func TestApplyConsoleOnly(t *testing.T) {
	f := &fakeOps{
		geom:  backend.Geometry{WindowID: "@1", Width: 200, Height: 50},
		panes: []backend.WindowPane{wp("5", 0, backend.RoleConsole, false)},
	}
	a := newTestApplier(f)
	if err := a.apply("%5", backend.LayoutCreate); err != nil {
		t.Fatal(err)
	}
	if len(f.applied) != 0 || f.mainvCalls != 0 {
		t.Errorf("console-only window should not lay out: applied=%v mainv=%d", f.applied, f.mainvCalls)
	}
}

func panesN(n int) []backend.WindowPane {
	out := make([]backend.WindowPane, n)
	for i := range out {
		out[i] = wp(fmt.Sprintf("%d", i+1), i, "", false)
	}
	return out
}
