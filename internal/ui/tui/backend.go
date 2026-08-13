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

func (m model) lifecycleActionDisabledReason(pane *paneView, action string) string {
	if pane == nil {
		return m.runtimeActionDisabledReason(nil, action)
	}
	if pane.isPaneOnly() && (action != "close" || backend.NormalizeName(pane.Backend) == backend.Herdr) {
		return fmt.Sprintf("%s unavailable for %s", action, paneOnlyKindLabel(*pane))
	}
	return m.lifecycleBackendActionDisabledReason(*pane, action)
}

func (m model) lifecycleBackendActionDisabledReason(pane paneView, action string) string {
	switch backend.NormalizeName(pane.Backend) {
	case backend.Tmux:
		return m.tmuxLifecycleActionDisabledReason(action)
	case backend.Herdr:
		return m.herdrLifecycleActionDisabledReason(pane, action)
	default:
		return m.runtimeActionDisabledReason(&pane, action)
	}
}

func (m model) tmuxLifecycleActionDisabledReason(action string) string {
	if action == "merge" || m.opts.LifecycleCloseOwned != nil {
		return ""
	}
	return fmt.Sprintf("runtime backend tmux does not support %s in this TUI", action)
}

func (m model) herdrLifecycleActionDisabledReason(pane paneView, action string) string {
	if m.selectedBackend() == backend.Herdr {
		if m.opts.HerdrActionDisabled == nil {
			return herdrActionDisabledReason(action)
		}
		if reason := strings.TrimSpace(m.opts.HerdrActionDisabled(pane.savedPane)); reason != "" {
			return reason
		}
	}
	if m.herdrLifecycleConfigured(pane, action) {
		return ""
	}
	return fmt.Sprintf("runtime backend herdr does not support %s in this TUI", action)
}

func (m model) herdrLifecycleConfigured(pane paneView, action string) bool {
	if m.opts.LifecycleHerdrRuntimeForRoot == nil {
		return false
	}
	routes := m.lifecycleActionRoutes(pane)
	roots := routes.cleanupRoots
	switch action {
	case "close":
		roots = routes.closeRoots
	case "merge":
		roots = []string{routes.paneRoot}
	}
	for _, root := range roots {
		if m.opts.LifecycleHerdrRuntimeForRoot(root) == nil {
			return false
		}
	}
	return true
}

func (m model) peekDisabledReason(pane paneView) string {
	return m.runtimeActionDisabledReason(&pane, "peek")
}
