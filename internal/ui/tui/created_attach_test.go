package tui

import (
	"errors"
	"os/exec"
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/butaosuinu/fanout/internal/core/backend"
)

func TestCreatedPaneAttachCommandPreservesStructuredProcessImage(t *testing.T) {
	spec := backend.AttachExec{
		Path: "/usr/bin/herdr",
		Argv: []string{"/usr/bin/herdr", "terminal", "attach", "terminal-1"},
		Env:  []string{"PATH=/usr/bin", "HERDR_SESSION=owned"},
	}
	cmd, err := createdPaneAttachCommand(spec)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Path != spec.Path || !reflect.DeepEqual(cmd.Args, spec.Argv) || !reflect.DeepEqual(cmd.Env, spec.Env) {
		t.Fatalf("command = %+v, want process image %+v", cmd, spec)
	}
	spec.Argv[3], spec.Env[0] = "changed", "changed"
	if cmd.Args[3] != "terminal-1" || cmd.Env[0] != "PATH=/usr/bin" {
		t.Fatal("created command aliases the caller's process image")
	}
	if _, err := createdPaneAttachCommand(backend.AttachExec{Path: "herdr", Argv: []string{"herdr"}}); err == nil {
		t.Fatal("relative attach binary was accepted")
	}
}

func TestCreatedPaneAttachSuspendsAndRestoresKeyboardProtocols(t *testing.T) {
	protocols := &fakeKeyboardProtocols{}
	binding := backend.PaneBinding{Ref: backend.PaneRef{Backend: backend.Herdr, Pane: "w1:p1"}}
	spec := backend.AttachExec{
		Path: "/usr/bin/herdr",
		Argv: []string{"/usr/bin/herdr", "terminal", "attach", "terminal-1"},
	}
	m := newModel(Options{
		PrepareCreatedPaneAttach: func(got backend.PaneBinding) (backend.AttachExec, error) {
			if !got.Equal(binding) {
				t.Fatalf("binding = %+v, want %+v", got, binding)
			}
			return spec, nil
		},
		keyboard: protocols,
	})
	prepared, ok := m.prepareCreatedPaneAttachCmd(binding, "created pane")().(createdPaneAttachPreparedMsg)
	if !ok || prepared.err != nil {
		t.Fatalf("prepared message = %#v", prepared)
	}

	originalRun := runCreatedPaneAttachProcess
	t.Cleanup(func() { runCreatedPaneAttachProcess = originalRun })
	var gotCmd *exec.Cmd
	var callback tea.ExecCallback
	runCreatedPaneAttachProcess = func(cmd *exec.Cmd, fn tea.ExecCallback) tea.Cmd {
		gotCmd, callback = cmd, fn
		return func() tea.Msg { return nil }
	}
	cmd, err := m.execCreatedPaneAttach(prepared)
	if err != nil || cmd == nil {
		t.Fatalf("execCreatedPaneAttach() = cmd %v err %v", cmd, err)
	}
	if gotCmd == nil || callback == nil || protocols.disableCount != 1 || !m.keyboardPaused {
		t.Fatalf("attach start = cmd %+v callback %v protocols %+v paused %t", gotCmd, callback != nil, protocols, m.keyboardPaused)
	}

	done, ok := callback(nil).(createdPaneAttachDoneMsg)
	if !ok || done.err != nil || protocols.enableCount != 1 {
		t.Fatalf("attach callback = %#v protocols %+v", done, protocols)
	}
	updated, next := m.Update(done)
	m = updated.(model)
	if next != nil || m.keyboardPaused || m.notice != "created pane; detached from w1:p1; returned to fanout tui" {
		t.Fatalf("attach return = next %v paused %t notice %q", next, m.keyboardPaused, m.notice)
	}

	failure := errors.New("attach exited")
	done = callback(failure).(createdPaneAttachDoneMsg)
	updated, _ = m.Update(done)
	m = updated.(model)
	if !errors.Is(done.err, failure) || m.notice != "created pane; attach failed for w1:p1: attach exited" {
		t.Fatalf("attach failure = %#v notice %q", done, m.notice)
	}
}

func TestCreatedPaneAttachRefusesRawPaneIDFallback(t *testing.T) {
	prepareCalls := 0
	m := newModel(Options{
		ProjectRoot: t.TempDir(),
		PrepareCreatedPaneAttach: func(backend.PaneBinding) (backend.AttachExec, error) {
			prepareCalls++
			return backend.AttachExec{}, nil
		},
	})
	updated, cmd := m.Update(launchPaneMsg{count: 1, createdPaneIDs: []string{"w1:p1"}})
	m = updated.(model)
	if cmd == nil || prepareCalls != 0 || m.notice != "created new agent pane; focus skipped: created pane binding is unavailable" {
		t.Fatalf("raw fallback = cmd %v calls %d notice %q", cmd, prepareCalls, m.notice)
	}
}

func TestCreatedPaneAttachRefusesIncompleteBindingOrder(t *testing.T) {
	prepareCalls := 0
	m := newModel(Options{
		ProjectRoot:      t.TempDir(),
		BackendSelection: backend.Selection{Name: backend.Herdr},
		PrepareCreatedPaneAttach: func(backend.PaneBinding) (backend.AttachExec, error) {
			prepareCalls++
			return backend.AttachExec{}, nil
		},
	})
	updated, cmd := m.Update(launchPaneMsg{
		count: 2, createdPaneIDs: []string{"w1:p1", "w2:p1"},
		createdBindings: []backend.PaneBinding{{Ref: backend.PaneRef{Backend: backend.Herdr, Pane: "w2:p1"}}},
	})
	m = updated.(model)
	if cmd == nil || prepareCalls != 0 || m.notice != "created 2 new agent panes; focus skipped: created pane bindings do not match creation order" {
		t.Fatalf("incomplete bindings = cmd %v calls %d notice %q", cmd, prepareCalls, m.notice)
	}
}
