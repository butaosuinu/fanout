package panelaunch

// Herdr sidebar tokens for a finished child launch. fanout owns five token
// names and reports the complete set for each resource, clearing the ones it
// has no value for so a reused workspace or pane never shows a stale value.
// The tokens are display-only: nothing reads them back.

import (
	"context"
	"strconv"
	"strings"
	"unicode"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

// Token placement is fixed: the workspace (sidebar Space row) names the child,
// and the pane (sidebar Agent row) carries the fan-out it belongs to and its
// work status.
const (
	metadataIssueToken  = "fanout_issue"
	metadataSlugToken   = "fanout_slug"
	metadataParentToken = "fanout_parent"
	metadataPRToken     = "fanout_pr"
	metadataCIToken     = "fanout_ci"
)

// reportHerdrSidebarMetadata publishes the display-only tokens once the launch
// has a verified live identity. Metadata is never authority, so a failure
// leaves the launch standing; fanout notes it and does not retry, because a
// lost outcome must not race a replaced session.
//
// The note is dim rather than a warning on purpose: the TUI turns every [warn]
// line from a successful launch into its result banner (bufferedLaunchNotice),
// and a cosmetic token failure must not read as a broken launch.
func (l *Launcher) reportHerdrSidebarMetadata(req Request, intent state.LaunchIntent) {
	ctx, cancel := context.WithTimeout(context.Background(), l.Herdr.MetadataReportBudget())
	defer cancel()
	if err := l.Herdr.ReportMetadata(ctx, herdrSidebarMetadata(req, intent)); err != nil {
		l.Log.Dim("%s: Herdr sidebar metadata not reported: %v", paneLogLabel(req), err)
	}
}

func herdrSidebarMetadata(req Request, intent state.LaunchIntent) backend.MetadataReport {
	return backend.MetadataReport{
		Target: backend.MetadataTarget{
			WorkspaceID: intent.Resource.WorkspaceID, Label: intent.Resource.Label,
			RepoKey: intent.Resource.RepoKey, RepoRoot: intent.Resource.RepoRoot,
			CheckoutPath: intent.WorktreePath,
			PaneID:       intent.Resource.PaneID, TerminalID: intent.Resource.TerminalID,
		},
		WorkspaceTokens: []backend.MetadataToken{
			{Name: metadataIssueToken, Value: herdrChildToken(req)},
			{Name: metadataSlugToken, Value: herdrMetadataValue(req.Slug)},
		},
		// A launch has no PR or CI reading yet, and v1 has no later reporter,
		// so both are cleared rather than left at whatever the resource held.
		PaneTokens: []backend.MetadataToken{
			{Name: metadataParentToken, Value: herdrParentToken(req)},
			{Name: metadataPRToken},
			{Name: metadataCIToken},
		},
	}
}

// herdrChildToken names the child: "#<issue>" for issue and Project children,
// the task ID for a plan task. Synthetic negative rows have neither and clear.
func herdrChildToken(req Request) string {
	if req.Number > 0 {
		return "#" + strconv.Itoa(req.Number)
	}
	return herdrMetadataValue(req.TaskID)
}

// herdrParentToken names the fan-out the pane belongs to. A watcher launch
// stores the synthetic @watch parent, and the rest of the launch path resolves
// it to the issue the watcher picked up — herdrCoordinatorRuntimeParent does
// exactly this for the coordinator workspace — so the sidebar names that issue
// instead of the marker.
func herdrParentToken(req Request) string {
	if canonicalHerdrParent(req.ParentRef) == WatchParentRef && req.Number > 0 {
		return herdrChildToken(req)
	}
	return herdrMetadataValue(parentDisplay(req.ParentRef))
}

// herdrMetadataValue shapes one value the way Herdr stores it — trimmed,
// control characters removed, at most MaxMetadataTokenValue characters. Herdr
// truncates silently, so fanout shortens first and the reported value and the
// sidebar stay the same string.
func herdrMetadataValue(raw string) string {
	cleaned := strings.TrimSpace(strings.Map(dropControlRune, raw))
	runes := []rune(cleaned)
	if len(runes) > backend.MaxMetadataTokenValue {
		return strings.TrimSpace(string(runes[:backend.MaxMetadataTokenValue]))
	}
	return cleaned
}

func dropControlRune(r rune) rune {
	if unicode.IsControl(r) {
		return -1
	}
	return r
}
