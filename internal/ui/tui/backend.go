package tui

import (
	"fmt"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func normalizeBackendSelection(selection backend.Selection) backend.Selection {
	selection.Name = backend.NormalizeName(selection.Name)
	if selection.Reason == "" {
		selection.Reason = backend.ReasonDefault
	}
	return selection
}

func backendSelectionReasonLabel(reason backend.SelectionReason) string {
	switch reason {
	case backend.ReasonExistingParent:
		return "existing parent"
	case backend.ReasonCLI:
		return "--backend"
	case backend.ReasonEnvironment:
		return "FANOUT_BACKEND"
	case backend.ReasonHerdrContext:
		return "HERDR_ENV"
	case backend.ReasonTmuxContext:
		return "TMUX"
	case backend.ReasonUserConfig:
		return "user config"
	default:
		return "default"
	}
}

func backendSelectionText(selection backend.Selection) string {
	selection = normalizeBackendSelection(selection)
	return fmt.Sprintf("backend: %s (%s)", selection.Name, backendSelectionReasonLabel(selection.Reason))
}

func (m model) selectedBackend() backend.Name {
	return normalizeBackendSelection(m.opts.BackendSelection).Name
}

func herdrActionDisabledReason(action string) string {
	action = strings.TrimSpace(action)
	if action == "" {
		return backend.HerdrObservationOnlyReason
	}
	return fmt.Sprintf("%s; %s is unavailable", backend.HerdrObservationOnlyReason, action)
}

func herdrInteractiveActionSupported(action string) bool {
	switch strings.TrimSpace(action) {
	case "focus", "peek", "attach", "terminal launch", "launch", "runtime action":
		return true
	default:
		return false
	}
}

func (m model) runtimeActionDisabledReason(pane *paneView, action string) string {
	applies, reason := herdrActionScope(m.selectedBackend(), pane)
	if !applies {
		return ""
	}
	if reason != "" {
		return reason
	}
	if !herdrInteractiveActionSupported(action) {
		return herdrActionDisabledReason(action)
	}
	if m.opts.HerdrActionDisabled == nil {
		return herdrActionDisabledReason(action)
	}
	saved := state.Pane{}
	if pane != nil {
		saved = pane.savedPane
	}
	if reason := strings.TrimSpace(m.opts.HerdrActionDisabled(saved)); reason != "" {
		return reason
	}
	return ""
}

func herdrActionScope(selected backend.Name, pane *paneView) (bool, string) {
	selectedHerdr := selected == backend.Herdr
	paneHerdr := pane != nil && backend.NormalizeName(pane.Backend) == backend.Herdr
	if !selectedHerdr && !paneHerdr {
		return false, ""
	}
	if selectedHerdr && pane != nil && !paneHerdr {
		return true, "pane is not in this repository's fanout-owned Herdr session"
	}
	if paneHerdr && !pane.canFocus() {
		return true, "pane is not in this repository's fanout-owned Herdr session"
	}
	return true, ""
}

func (m model) peekDisabledReason(pane paneView) string {
	return m.runtimeActionDisabledReason(&pane, "peek")
}
