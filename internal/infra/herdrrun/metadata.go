package herdrrun

// Display-only sidebar metadata for the fanout-owned Herdr session. Tokens are
// presentation data: they never carry backend state, liveness, nudge authority,
// or completion. Because a report cannot bind the server generation, every
// report is bracketed by an exact target recheck, and a mismatch on either side
// makes the outcome unknown — the caller reports it and does not retry.

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// MetadataSource is the fixed reporter ID on every fanout report. Herdr lets
// the last accepted reporter win per token key, so a stable ID keeps fanout
// replacing its own values and never another reporter's.
const MetadataSource = "fanout"

// MaxMetadataTokenValue is Herdr's per-value character limit after trimming and
// control-character removal. Herdr truncates silently, so callers shorten values
// themselves and keep the reported value and the sidebar identical.
const MaxMetadataTokenValue = 80

// maxMetadataTokensPerReport is Herdr's per-report token cap.
const maxMetadataTokensPerReport = 16

// MetadataReportBudget bounds one ReportMetadata end to end. The call makes six
// commandTimeout-bounded Herdr calls — the owned probe's version and status, an
// identity snapshot on each side of the bracket, and the two reports — so the
// budget is deliberately shorter than their sum: metadata is display-only and
// runs after a launch already succeeded, so a slow Herdr must skip the report
// rather than hold up the next child.
const MetadataReportBudget = 4 * commandTimeout

var metadataTokenName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// MetadataToken is one entry of a fanout-owned token patch. An empty Value
// clears the token: a report always writes fanout's complete token set for a
// resource, so a reused workspace or pane never keeps a stale fanout value.
type MetadataToken struct {
	Name  string
	Value string
}

// MetadataTarget is the exact workspace and pane identity that must be live
// immediately before and after a report.
type MetadataTarget struct {
	WorkspaceID  string
	Label        string
	RepoKey      string
	RepoRoot     string
	CheckoutPath string
	PaneID       string
	TerminalID   string
}

// MetadataReport is one target's token patch. Either patch may be empty, which
// skips that resource; a report with no tokens at all is rejected.
type MetadataReport struct {
	Target          MetadataTarget
	WorkspaceTokens []MetadataToken
	PaneTokens      []MetadataToken
}

// ReportMetadata publishes display-only tokens for one launched child.
func (s *OwnedSession) ReportMetadata(ctx context.Context, report MetadataReport) error {
	if s == nil || s.backend == nil {
		return fmt.Errorf("herdr owned session is nil")
	}
	if err := validateMetadataReport(report); err != nil {
		return err
	}
	admission, lock, err := s.backend.acquireOwnedOperation(ctx)
	if err != nil {
		return err
	}
	defer unlockPrivateFile(lock)
	probed, err := s.backend.probeOwned(ctx, admission)
	if err != nil {
		return err
	}
	return s.backend.reportBracketedMetadata(ctx, probed, report)
}

func (b *Backend) reportBracketedMetadata(
	ctx context.Context,
	probed probeResult,
	report MetadataReport,
) error {
	if err := b.verifyMetadataTarget(ctx, probed, report.Target); err != nil {
		return fmt.Errorf("verify Herdr metadata target: %w", err)
	}
	if err := b.runMetadataReports(ctx, probed, report); err != nil {
		return err
	}
	if err := b.verifyMetadataTarget(ctx, probed, report.Target); err != nil {
		return fmt.Errorf("herdr metadata report outcome is unknown: %w", err)
	}
	return nil
}

// runMetadataReports issues the patch one resource at a time. Herdr has no
// multi-resource report, so a failure after the first call leaves the patch
// half applied; say so, because the tokens already written stay written and
// nothing rewrites them.
func (b *Backend) runMetadataReports(
	ctx context.Context,
	probed probeResult,
	report MetadataReport,
) error {
	for i, args := range metadataCalls(report) {
		err := b.runMetadataReport(ctx, probed, args)
		switch {
		case err == nil:
		case i > 0:
			return fmt.Errorf("%w; earlier reports in this patch already applied", err)
		default:
			return err
		}
	}
	return nil
}

// runMetadataReport dispatches one report. Herdr answers a successful
// report-metadata with no output at all, so any output is an unexpected result.
func (b *Backend) runMetadataReport(ctx context.Context, probed probeResult, args []string) error {
	out, err := b.runContext(ctx, commandTimeout, probed.binary, probed.route, args...)
	if err != nil {
		return fmt.Errorf("herdr %s report-metadata: %w", args[0], err)
	}
	if strings.TrimSpace(string(out)) != "" {
		return fmt.Errorf("herdr %s report-metadata returned an unexpected response", args[0])
	}
	return nil
}

func metadataCalls(report MetadataReport) [][]string {
	calls := make([][]string, 0, 2)
	if len(report.WorkspaceTokens) > 0 {
		calls = append(calls, metadataArgs("workspace", report.Target.WorkspaceID, report.WorkspaceTokens))
	}
	if len(report.PaneTokens) > 0 {
		calls = append(calls, metadataArgs("pane", report.Target.PaneID, report.PaneTokens))
	}
	return calls
}

// metadataArgs pins the 0.7.5 report-metadata argv. The presentation fields —
// title, display agent, state labels — are never passed: fanout owns its own
// tokens and nothing else on the resource.
func metadataArgs(resource, id string, tokens []MetadataToken) []string {
	args := make([]string, 0, 5+2*len(tokens))
	args = append(args, resource, "report-metadata", id, "--source", MetadataSource)
	for _, token := range tokens {
		if token.Value == "" {
			args = append(args, "--clear-token", token.Name)
			continue
		}
		args = append(args, "--token", token.Name+"="+token.Value)
	}
	return args
}

// verifyMetadataTarget reads one snapshot and inspects only this target.
// Projecting every workspace would fail on any unrelated pane-less one — Herdr
// drops a pane record when its agent exits — and a sibling child exiting has
// nothing to say about whether this target is live.
func (b *Backend) verifyMetadataTarget(
	ctx context.Context,
	probed probeResult,
	target MetadataTarget,
) error {
	snapshot, err := b.observeOwnedSnapshot(ctx, probed)
	if err != nil {
		return err
	}
	if !metadataTargetLive(target, snapshot) {
		return fmt.Errorf("%w: metadata target is not live", ErrOwnedIdentityMismatch)
	}
	return nil
}

// metadataTargetLive relies on projectSnapshot having already rejected
// duplicate workspace and pane IDs, so the first match is the only match.
func metadataTargetLive(target MetadataTarget, snapshot snapshotJSON) bool {
	return metadataWorkspaceLive(target, *snapshot.Workspaces) &&
		metadataPaneLive(target, *snapshot.Panes)
}

func metadataWorkspaceLive(target MetadataTarget, workspaces []workspaceJSON) bool {
	for _, workspace := range workspaces {
		if workspace.WorkspaceID != target.WorkspaceID {
			continue
		}
		return workspace.Label == target.Label && metadataProvenanceLive(target, workspace.Worktree)
	}
	return false
}

// metadataProvenanceLive compares the checkout path the way every other Herdr
// identity check does, so a report cannot reject a target the launch accepted.
func metadataProvenanceLive(target MetadataTarget, worktree *worktreeInfoJSON) bool {
	return worktree != nil && worktree.RepoKey == target.RepoKey &&
		worktree.RepoRoot == target.RepoRoot &&
		filepath.Clean(worktree.CheckoutPath) == filepath.Clean(target.CheckoutPath)
}

func metadataPaneLive(target MetadataTarget, panes []paneJSON) bool {
	for _, pane := range panes {
		if pane.PaneID != target.PaneID {
			continue
		}
		return pane.WorkspaceID == target.WorkspaceID && pane.TerminalID == target.TerminalID
	}
	return false
}

func validateMetadataReport(report MetadataReport) error {
	if err := validateMetadataTarget(report.Target); err != nil {
		return err
	}
	if len(report.WorkspaceTokens) == 0 && len(report.PaneTokens) == 0 {
		return fmt.Errorf("herdr metadata report carries no token patch")
	}
	if err := validateMetadataTokens(report.WorkspaceTokens); err != nil {
		return fmt.Errorf("herdr workspace metadata: %w", err)
	}
	if err := validateMetadataTokens(report.PaneTokens); err != nil {
		return fmt.Errorf("herdr pane metadata: %w", err)
	}
	return nil
}

func validateMetadataTarget(target MetadataTarget) error {
	identity := []string{target.WorkspaceID, target.Label, target.PaneID, target.TerminalID}
	if slices.Contains(identity, "") {
		return fmt.Errorf("%w: metadata target is incomplete", ErrOwnedIdentityMismatch)
	}
	provenance := []string{target.RepoKey, target.RepoRoot, target.CheckoutPath}
	if slices.Contains(provenance, "") {
		return fmt.Errorf("%w: metadata target worktree provenance is incomplete", ErrOwnedIdentityMismatch)
	}
	return nil
}

func validateMetadataTokens(tokens []MetadataToken) error {
	if len(tokens) > maxMetadataTokensPerReport {
		return fmt.Errorf("a report updates at most %d tokens", maxMetadataTokensPerReport)
	}
	seen := make(map[string]bool, len(tokens))
	for _, token := range tokens {
		if !metadataTokenName.MatchString(token.Name) {
			return fmt.Errorf("token name %q is invalid", token.Name)
		}
		if seen[token.Name] {
			return fmt.Errorf("token name %q is repeated", token.Name)
		}
		seen[token.Name] = true
		if err := validateMetadataValue(token.Value); err != nil {
			return fmt.Errorf("token %q: %w", token.Name, err)
		}
	}
	return nil
}

// validateMetadataValue fails closed on values Herdr would silently reshape,
// so the reported value and the value the sidebar shows stay the same string.
func validateMetadataValue(value string) error {
	if len([]rune(value)) > MaxMetadataTokenValue {
		return fmt.Errorf("value exceeds %d characters", MaxMetadataTokenValue)
	}
	if strings.TrimSpace(value) != value || strings.ContainsFunc(value, unicode.IsControl) {
		return fmt.Errorf("value is not trimmed control-free display text")
	}
	return nil
}
