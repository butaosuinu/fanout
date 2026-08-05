package tui

import (
	"reflect"
	"testing"
	"time"
)

// The worktree collector owns an untracked-file cache. Rebuilding it per tick
// would discard that cache and re-spawn a git process for every untracked file
// on the 2-second refresh, so the model has to hold one instance.
func TestModelReusesOneWorktreeCollectorAcrossTicks(t *testing.T) {
	m := newModel(Options{ProjectRoot: t.TempDir()})
	if m.worktreeStat == nil {
		t.Fatal("newModel() left worktreeStat nil; loadPaneViews would get no collector")
	}
	first := reflect.ValueOf(m.worktreeStat).Pointer()

	next, _ := m.Update(stateLoadedMsg{at: time.Now()})
	after, ok := next.(model)
	if !ok {
		t.Fatalf("Update() returned %T, want model", next)
	}
	if after.worktreeStat == nil ||
		reflect.ValueOf(after.worktreeStat).Pointer() != first {
		t.Fatal("Update() replaced the worktree collector; its cache would not survive a tick")
	}
}
