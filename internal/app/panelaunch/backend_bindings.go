package panelaunch

import (
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

// RuntimeBackendBindings projects one worktree-local state store into the
// parent keys used by runtime backend stickiness.
func RuntimeBackendBindings(projectRoot string, store state.Store) []backend.Binding {
	rows := make([]backend.Binding, 0, len(store.Panes)*2)
	planParents := map[string]string{}
	for _, pane := range store.Panes {
		if pane.IsAttachedAgent() {
			parent := strings.TrimSpace(pane.SourceParent)
			if parent == "" {
				parent = strings.TrimSpace(pane.Parent)
			}
			switch {
			case pane.SourceIssueNum > 0 &&
				(parent == ManualParentRef || parent == WatchParentRef || parent == ""):
				parent = strconv.Itoa(pane.SourceIssueNum)
			case strings.HasPrefix(parent, "plan:"):
				planSlug := strings.TrimPrefix(parent, "plan:")
				if planSlug != "" {
					parent = SavedPlanRuntimeParentRef(projectRoot, planSlug)
				}
			}
			if parent != "" && parent != ManualParentRef && parent != WatchParentRef {
				rows = append(rows, backend.Binding{Parent: parent, Backend: pane.Backend})
			}
			continue
		}
		if issueNum, ok := PaneIssueParentNum(pane); ok {
			rows = append(rows, backend.Binding{
				Parent: strconv.Itoa(issueNum), Backend: pane.Backend,
			})
			continue
		}
		if planSlug, ok := strings.CutPrefix(pane.Parent, "plan:"); ok && planSlug != "" {
			parent, seen := planParents[planSlug]
			if !seen {
				parent = SavedPlanRuntimeParentRef(projectRoot, planSlug)
				planParents[planSlug] = parent
			}
			rows = append(rows, backend.Binding{Parent: parent, Backend: pane.Backend})
			continue
		}
		if pane.Parent != ManualParentRef && pane.Parent != WatchParentRef {
			rows = append(rows, backend.Binding{Parent: pane.Parent, Backend: pane.Backend})
		}
	}
	return rows
}
