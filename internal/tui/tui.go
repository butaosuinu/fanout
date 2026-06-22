// Package tui renders fanout's long-running pane monitor.
package tui

import (
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

// Run starts the Bubble Tea TUI.
func Run(opts Options) error {
	opts = normalizeOptions(opts)
	keyboard := newShiftEnterProtocols(os.Stdout)
	opts.keyboard = keyboard
	m := newModel(opts)
	input, closeInput, err := newShiftEnterProgramInput(os.Stdin)
	if err != nil {
		return err
	}
	defer closeInput()
	defer keyboard.Disable()
	_, err = tea.NewProgram(
		m,
		tea.WithAltScreen(),
		tea.WithInput(input),
		tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
			if _, ok := msg.(tea.QuitMsg); ok {
				keyboard.Disable()
			}
			return msg
		}),
	).Run()
	return err
}
