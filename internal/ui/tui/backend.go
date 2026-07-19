package tui

import (
	"fmt"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/backend"
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

func (m model) runtimeActionDisabledReason(pane *paneView, action string) string {
	if m.selectedBackend() == backend.Herdr {
		return herdrActionDisabledReason(action)
	}
	if pane != nil && backend.NormalizeName(pane.Backend) == backend.Herdr {
		return herdrActionDisabledReason(action)
	}
	return ""
}

func (m model) peekDisabledReason(pane paneView) string {
	if m.selectedBackend() == backend.Herdr || backend.NormalizeName(pane.Backend) == backend.Herdr {
		return backend.HerdrContentReadReason
	}
	return ""
}
