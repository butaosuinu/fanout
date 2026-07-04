package tui

import (
	"github.com/charmbracelet/lipgloss"
)

// PAPER BREEZE palette (site/assets/css/main.css; keep the internal/log
// 256-color approximations and the internal/tmuxrun pane-border hex in sync).
// Light = site values (紅 is an addition;
// the site defines no red), dark = same hue lifted for dark backgrounds.
var (
	colorAi     = lipgloss.AdaptiveColor{Light: "#165E83", Dark: "#6FAECE"} // 藍
	colorAsagi  = lipgloss.AdaptiveColor{Light: "#00A3AF", Dark: "#2BC4CF"} // 浅葱
	colorInk    = lipgloss.AdaptiveColor{Light: "#797D80", Dark: "#8A9096"} // 墨60%
	colorSuna   = lipgloss.AdaptiveColor{Light: "#E2D9C8", Dark: "#5C564C"} // 砂
	colorTsuchi = lipgloss.AdaptiveColor{Light: "#9A6B2F", Dark: "#C9974F"} // 土
	colorBeni   = lipgloss.AdaptiveColor{Light: "#B5495B", Dark: "#E07A8B"} // 紅
)

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(colorAi)
	dimStyle   = lipgloss.NewStyle().Foreground(colorInk)
	warnStyle  = lipgloss.NewStyle().Foreground(colorTsuchi)
	errStyle   = lipgloss.NewStyle().Foreground(colorBeni)
	panelStyle = lipgloss.NewStyle().Border(lipgloss.NormalBorder(), true, false, false, false).BorderForeground(colorSuna)
	modalStyle = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).Padding(1, 2).BorderForeground(colorAsagi)
	// popupContentStyle drops the modal border when the popup runs inside a tmux
	// display-popup: the popup frame is the only border, so the content only
	// keeps a 1-column left/right gutter.
	popupContentStyle = lipgloss.NewStyle().Padding(0, 1)
	// inputBoxStyle / inputBoxFocusStyle frame the modal's text inputs so the
	// field bounds are visible; the border color also doubles as a focus cue.
	inputBoxStyle      = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(colorSuna)
	inputBoxFocusStyle = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(colorAsagi)
)
