package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"

	"github.com/butaosuinu/fanout/internal/core/backend"
)

func TestCreatedPaneAttachReturnsWithoutFurtherInput(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(fmt.Sprintf("terminal=%t", terminal), func(t *testing.T) {
			if terminal && !term.IsTerminal(os.Stdin.Fd()) {
				t.Skip("run the test binary in a synthetic PTY to check the /dev/tty fallback")
			}
			for _, exitCode := range []int{0, 7} {
				t.Run(fmt.Sprintf("exit=%d", exitCode), func(t *testing.T) {
					reader, writer, err := os.Pipe()
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = reader.Close() }) // Close the owned test pipe.
					t.Cleanup(func() { _ = writer.Close() }) // Keep it open and silent during attach.
					input := newShiftEnterInput(reader)
					if terminal {
						var closeInput func()
						input, closeInput, err = newShiftEnterProgramInput(reader)
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(closeInput)
					}
					assertCreatedPaneAttachReturns(t, input, writer, terminal, exitCode)
				})
			}
		})
	}
}

func assertCreatedPaneAttachReturns(t *testing.T, input *shiftEnterInput, writer *os.File, terminal bool, exitCode int) {
	t.Helper()
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	command := fmt.Sprintf("exit %d", exitCode)
	if terminal {
		command = "test -t 0 || exit 99; " + command
	}
	m := newModel(Options{})
	m.terminalInput = input.src
	cmd, err := m.execCreatedPaneAttach(createdPaneAttachPreparedMsg{
		spec: backend.AttachExec{Path: shell, Argv: []string{shell, "-c", command}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, func() {
		_ = writer.Close()    // Unblock a regressed pipe read on timeout.
		_ = input.src.Close() // Release the owned input if the program is stuck.
	})
	defer stop()
	result, err := tea.NewProgram(attachReturnModel{cmd: cmd}, tea.WithInput(input),
		tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithoutSignalHandler()).Run()
	if err != nil || ctx.Err() != nil {
		t.Fatalf("attach did not return without further input: run=%v context=%v", err, ctx.Err())
	}
	done := result.(attachReturnModel).done
	if exitCode == 0 && done.err != nil {
		t.Fatalf("attach exit: %v", done.err)
	}
	if exitCode != 0 {
		exitErr, ok := errors.AsType[*exec.ExitError](done.err)
		if !ok || exitErr.ExitCode() != exitCode {
			t.Fatalf("attach exit = %v, want %d", done.err, exitCode)
		}
	}
	updated, _ := m.Update(done)
	if updated.(model).keyboardPaused {
		t.Fatal("keyboard remained paused after attach returned")
	}
}

type attachReturnModel struct {
	cmd  tea.Cmd
	done createdPaneAttachDoneMsg
}

func (m attachReturnModel) Init() tea.Cmd { return m.cmd }
func (m attachReturnModel) View() string  { return "" }
func (m attachReturnModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if done, ok := msg.(createdPaneAttachDoneMsg); ok {
		m.done = done
		return m, tea.Quit
	}
	return m, nil
}

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
