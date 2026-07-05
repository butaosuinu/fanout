package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestWindowResizeSchedulesDebouncedRelayout(t *testing.T) {
	called := 0
	m := newModel(Options{Relayout: func() error { called++; return nil }})

	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	mm := updated.(model)
	if mm.relayoutGen != 1 {
		t.Fatalf("relayoutGen = %d, want 1", mm.relayoutGen)
	}
	if cmd == nil {
		t.Fatal("resize did not schedule a debounce tick")
	}

	// The matching tick triggers the relayout command.
	updated, tickCmd := mm.Update(relayoutTickMsg{gen: 1})
	mm = updated.(model)
	if tickCmd == nil {
		t.Fatal("matching tick did not produce a relayout command")
	}
	if _, ok := tickCmd().(relayoutDoneMsg); !ok {
		t.Fatal("relayout command did not return relayoutDoneMsg")
	}
	if called != 1 {
		t.Fatalf("Relayout called %d times, want 1", called)
	}
}

func TestStaleResizeTickIsSuperseded(t *testing.T) {
	called := 0
	m := newModel(Options{Relayout: func() error { called++; return nil }})

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})              // gen 1
	updated, _ = updated.(model).Update(tea.WindowSizeMsg{Width: 240, Height: 50}) // gen 2
	mm := updated.(model)
	if mm.relayoutGen != 2 {
		t.Fatalf("relayoutGen = %d, want 2", mm.relayoutGen)
	}

	// The earlier tick is stale and must not relayout.
	_, staleCmd := mm.Update(relayoutTickMsg{gen: 1})
	if staleCmd != nil {
		t.Fatal("stale tick should be a no-op")
	}

	// Only the latest tick fires.
	_, freshCmd := mm.Update(relayoutTickMsg{gen: 2})
	if freshCmd == nil {
		t.Fatal("latest tick should produce a relayout command")
	}
	freshCmd()
	if called != 1 {
		t.Fatalf("Relayout called %d times, want 1", called)
	}
}

func TestResizeWithoutRelayoutIsNoop(t *testing.T) {
	m := newModel(Options{}) // no Relayout wired

	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	if cmd != nil {
		t.Fatal("resize with no Relayout callback should not schedule anything")
	}
	if updated.(model).relayoutGen != 0 {
		t.Fatal("relayoutGen should stay 0 when no Relayout is wired")
	}
}
