package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/butaosuinu/fanout/internal/app/lifecycle"
)

const closePopupOpeningNotice = "opening close popup..."

// CloseChoiceRequest describes the pane close option prompt shown in an
// external surface, such as a tmux display-popup.
type CloseChoiceRequest struct {
	PaneLabel   string
	InitialMode lifecycle.CloseMode
}

// CloseChoicePopupFunc collects the close mode to use. canceled is true when
// the user dismisses the popup without choosing.
type CloseChoicePopupFunc func(CloseChoiceRequest) (mode lifecycle.CloseMode, canceled bool, err error)

// CloseChoicePopupOptions configures the standalone close-choice popup program.
type CloseChoicePopupOptions struct {
	PaneLabel   string
	InitialMode lifecycle.CloseMode
	Width       int
	Height      int
}

// RunCloseChoicePopup opens only the close-choice UI and returns the selected
// close mode.
func RunCloseChoicePopup(opts CloseChoicePopupOptions) (lifecycle.CloseMode, bool, error) {
	width := opts.Width
	if width <= 0 {
		width = 72
	}
	height := opts.Height
	if height <= 0 {
		height = 10
	}
	m := newModel(Options{})
	m.closeOnly = true
	m.mode = modeCloseChoice
	m.width = width
	m.height = height
	label := strings.TrimSpace(opts.PaneLabel)
	if label == "" {
		label = "pane"
	}
	idx := closeOptionIndexForMode(opts.InitialMode)
	m.pendingAction = &pendingLifecycleAction{
		action:           actionClose,
		pane:             paneView{TaskID: label},
		closeOptionIndex: idx,
		closeMode:        closeOptions()[idx].mode,
	}
	finalModel, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return lifecycle.ClosePaneOnly, false, err
	}
	final, ok := finalModel.(model)
	if !ok {
		return lifecycle.ClosePaneOnly, false, fmt.Errorf("unexpected close popup model %T", finalModel)
	}
	if !final.closeDone || final.closeCanceled {
		return lifecycle.ClosePaneOnly, true, nil
	}
	return final.closeResult, false, nil
}

func (m model) closeChoicePopupCmd() (model, tea.Cmd) {
	popup := m.opts.CloseChoicePopup
	if popup == nil || m.pendingAction == nil {
		m.mode = modeCloseChoice
		m.actionMessage = ""
		return m, nil
	}
	pending := *m.pendingAction
	m.notice = closePopupOpeningNotice
	m.actionMessage = ""
	m.closePopupOpen = true
	cmd := func() tea.Msg {
		mode, canceled, err := popup(CloseChoiceRequest{
			PaneLabel:   pending.pane.identityLabel(),
			InitialMode: pending.closeMode,
		})
		return closeChoicePopupDoneMsg{mode: mode, canceled: canceled, err: err}
	}
	return m, cmd
}

func (m model) closeChoiceView() string {
	label := "pane"
	optionIndex := 0
	if m.pendingAction != nil {
		label = m.pendingAction.pane.identityLabel()
		optionIndex = m.pendingAction.closeOptionIndex
	}
	lines := make([]string, 0, 6)
	if !m.closeOnly {
		lines = append(lines, titleStyle.Render("Close pane"))
	}
	lines = append(lines, fmt.Sprintf("Close %s?", label))
	for i, opt := range closeOptions() {
		marker := plainItemMarker
		style := lipgloss.NewStyle()
		if i == optionIndex {
			marker = selectedItemMarker
			style = titleStyle
		}
		lines = append(lines, style.Render(fmt.Sprintf("%s%d. %s - %s", marker, i+1, opt.label, opt.description)))
	}
	lines = append(lines, dimStyle.Render("up/down or 1-3 select, enter confirm, esc cancel"))
	content := strings.Join(lines, "\n")
	if m.closeOnly {
		return popupContentStyle.Width(m.closeChoiceModalWidth()).Render(content)
	}
	return modalStyle.Width(m.closeChoiceModalWidth()).Render(content)
}

func (m model) closeChoiceModalWidth() int {
	if m.width <= 0 {
		return 72
	}
	if m.closeOnly {
		return clampInt(m.width, 48, 82)
	}
	return clampInt(m.width-4, 48, 76)
}

func closeOptionIndexForMode(mode lifecycle.CloseMode) int {
	for i, opt := range closeOptions() {
		if opt.mode == mode {
			return i
		}
	}
	return 0
}
