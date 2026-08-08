package panelaunch

// Herdr sidebar tokens for a finished child launch. fanout owns five token
// names and reports the complete set for each resource, clearing the ones it
// has no value for so a reused workspace or pane never shows a stale value.
// The tokens are display-only: nothing reads them back.

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
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

// herdrMetadataTimeout bounds the whole report: two identity snapshots around
// two report calls, each of which the runtime bounds separately.
const herdrMetadataTimeout = 30 * time.Second

// reportHerdrSidebarMetadata publishes the display-only tokens once the launch
// has a verified live identity. Metadata is never authority, so a failure
// leaves the launch standing; fanout logs it and does not retry, because a lost
// outcome must not race a replaced session.
func (l *Launcher) reportHerdrSidebarMetadata(req Request, intent state.HerdrIntent) {
	ctx, cancel := context.WithTimeout(context.Background(), herdrMetadataTimeout)
	defer cancel()
	if err := l.Herdr.ReportMetadata(ctx, herdrSidebarMetadata(req, intent)); err != nil {
		l.Log.Warn("%s: report Herdr sidebar metadata: %v", paneLogLabel(req), err)
	}
}

func herdrSidebarMetadata(req Request, intent state.HerdrIntent) herdrrun.MetadataReport {
	return herdrrun.MetadataReport{
		Target: herdrrun.MetadataTarget{
			WorkspaceID: intent.Resource.WorkspaceID, Label: intent.Resource.Label,
			RepoKey: intent.Resource.RepoKey, RepoRoot: intent.Resource.RepoRoot,
			CheckoutPath: intent.WorktreePath,
			PaneID:       intent.Resource.PaneID, TerminalID: intent.Resource.TerminalID,
		},
		WorkspaceTokens: []herdrrun.MetadataToken{
			{Name: metadataIssueToken, Value: herdrChildToken(req)},
			{Name: metadataSlugToken, Value: herdrMetadataValue(req.Slug)},
		},
		// A launch has no PR or CI reading yet, and v1 has no later reporter,
		// so both are cleared rather than left at whatever the resource held.
		PaneTokens: []herdrrun.MetadataToken{
			{Name: metadataParentToken, Value: herdrMetadataValue(parentDisplay(req.ParentRef))},
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

// herdrMetadataValue shapes one value the way Herdr stores it — trimmed,
// control characters removed, at most MaxMetadataTokenValue characters. Herdr
// truncates silently, so fanout shortens first and the reported value and the
// sidebar stay the same string.
func herdrMetadataValue(raw string) string {
	cleaned := strings.TrimSpace(strings.Map(dropControlRune, raw))
	runes := []rune(cleaned)
	if len(runes) > herdrrun.MaxMetadataTokenValue {
		return strings.TrimSpace(string(runes[:herdrrun.MaxMetadataTokenValue]))
	}
	return cleaned
}

func dropControlRune(r rune) rune {
	if unicode.IsControl(r) {
		return -1
	}
	return r
}
