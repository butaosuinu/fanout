package tui

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/butaosuinu/fanout/internal/core/backend"
)

var runCreatedPaneAttachProcess = tea.ExecProcess

type createdPaneAttachPreparedMsg struct {
	binding       backend.PaneBinding
	contextNotice string
	spec          backend.AttachExec
	err           error
}

type createdPaneAttachDoneMsg struct {
	paneID        string
	contextNotice string
	err           error
}

func (m model) prepareCreatedPaneAttachCmd(binding backend.PaneBinding, contextNotice string) tea.Cmd {
	prepare := m.opts.PrepareCreatedPaneAttach
	return func() tea.Msg {
		if prepare == nil {
			return createdPaneAttachPreparedMsg{
				binding: binding, contextNotice: contextNotice,
				err: fmt.Errorf("created pane attach is not configured"),
			}
		}
		spec, err := prepare(binding)
		return createdPaneAttachPreparedMsg{
			binding: binding, contextNotice: contextNotice, spec: spec, err: err,
		}
	}
}

func (m *model) execCreatedPaneAttach(msg createdPaneAttachPreparedMsg) (tea.Cmd, error) {
	cmd, err := createdPaneAttachCommand(msg.spec)
	if err != nil {
		return nil, err
	}
	keyboard := m.opts.keyboard
	keyboard.Disable()
	m.keyboardPaused = true
	return runCreatedPaneAttachProcess(cmd, func(err error) tea.Msg {
		keyboard.Enable()
		return createdPaneAttachDoneMsg{
			paneID: msg.binding.Ref.Pane, contextNotice: msg.contextNotice, err: err,
		}
	}), nil
}

func createdPaneAttachCommand(spec backend.AttachExec) (*exec.Cmd, error) {
	if !filepath.IsAbs(spec.Path) || len(spec.Argv) == 0 || spec.Argv[0] != spec.Path {
		return nil, fmt.Errorf("created pane attach process image is invalid")
	}
	return &exec.Cmd{
		Path: spec.Path, Args: slices.Clone(spec.Argv), Env: slices.Clone(spec.Env),
	}, nil
}
