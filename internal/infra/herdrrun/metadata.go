package herdrrun

// Display-only sidebar metadata for the fanout-owned Herdr session. Tokens are
// presentation data: they never carry backend state, liveness, nudge authority,
// or completion. Because a report cannot bind the server generation, every
// report is bracketed by an exact target recheck, and a mismatch on either side
// makes the outcome unknown — the caller reports it and does not retry.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
)

// MetadataSource is the fixed reporter ID on every fanout report. Herdr lets
// the last accepted reporter win per token key, so a stable ID keeps fanout
// replacing its own values and never another reporter's.
const MetadataSource = "fanout"

// maxMetadataTokensPerReport is Herdr's per-report token cap.
const maxMetadataTokensPerReport = 16

// MetadataReportBudget bounds one ReportMetadata end to end. The call makes
// several commandTimeout-bounded Herdr calls — the owned probe, an identity
// snapshot around every report, and the reports themselves — and the budget is
// deliberately shorter than their sum: metadata is display-only and runs after
// a launch already succeeded, so a slow Herdr must skip the report rather than
// hold up the next child.
const MetadataReportBudget = 4 * commandTimeout

var metadataTokenName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// ReportMetadata publishes display-only tokens for one launched child.
func (s *OwnedSession) ReportMetadata(ctx context.Context, report corebackend.MetadataReport) error {
	if s == nil || s.backend == nil {
		return fmt.Errorf("herdr owned session is nil")
	}
	if err := validateMetadataReport(report); err != nil {
		return err
	}
	admission, lock, err := s.backend.acquireOwnedMutation(ctx)
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

// reportBracketedMetadata brackets every report individually: the target is
// revalidated immediately before each one, and once more after the last. Herdr
// has no multi-resource report, so the workspace patch can land while the pane
// is replaced underneath the next call, and only a per-mutation bracket catches
// that. A failed report closes with the same recheck, because a lost response
// leaves the mutation ambiguous.
func (b *Backend) reportBracketedMetadata(
	ctx context.Context,
	probed probeResult,
	report corebackend.MetadataReport,
) error {
	issued := 0
	for _, args := range metadataCalls(report) {
		if err := b.verifyMetadataTarget(ctx, probed, report.Target); err != nil {
			return metadataBracketError(issued, err)
		}
		reportErr := b.runMetadataReport(ctx, probed, args)
		issued++
		if reportErr != nil {
			closing := b.verifyMetadataTarget(ctx, probed, report.Target)
			return errors.Join(reportErr, metadataBracketError(issued, closing))
		}
	}
	return metadataBracketError(issued, b.verifyMetadataTarget(ctx, probed, report.Target))
}

// metadataBracketError names which side of the patch a failed recheck leaves
// uncertain. Before any report nothing was written; after one, the tokens
// already written may belong to a resource that has since been replaced.
// fanout does not retry in either case.
func metadataBracketError(issued int, err error) error {
	switch {
	case err == nil:
		return nil
	case issued == 0:
		return fmt.Errorf("verify Herdr metadata target: %w", err)
	default:
		return fmt.Errorf("herdr metadata outcome is unknown after %d issued report(s): %w", issued, err)
	}
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

func metadataCalls(report corebackend.MetadataReport) [][]string {
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
func metadataArgs(resource, id string, tokens []corebackend.MetadataToken) []string {
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
	target corebackend.MetadataTarget,
) error {
	snapshot, err := b.observeOwnedSnapshot(ctx, probed)
	if err != nil {
		return err
	}
	if !metadataTargetLive(target, snapshot) {
		return fmt.Errorf("%w: metadata target is not live", corebackend.ErrOwnedIdentityMismatch)
	}
	return nil
}

// metadataTargetLive relies on projectSnapshot having already rejected
// duplicate workspace and pane IDs, so the first match is the only match.
func metadataTargetLive(target corebackend.MetadataTarget, snapshot snapshotJSON) bool {
	return metadataWorkspaceLive(target, *snapshot.Workspaces) &&
		metadataPaneLive(target, *snapshot.Panes)
}

func metadataWorkspaceLive(target corebackend.MetadataTarget, workspaces []workspaceJSON) bool {
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
func metadataProvenanceLive(target corebackend.MetadataTarget, worktree *worktreeInfoJSON) bool {
	return worktree != nil && worktree.RepoKey == target.RepoKey &&
		worktree.RepoRoot == target.RepoRoot &&
		filepath.Clean(worktree.CheckoutPath) == filepath.Clean(target.CheckoutPath)
}

func metadataPaneLive(target corebackend.MetadataTarget, panes []paneJSON) bool {
	for _, pane := range panes {
		if pane.PaneID != target.PaneID {
			continue
		}
		return pane.WorkspaceID == target.WorkspaceID && pane.TerminalID == target.TerminalID
	}
	return false
}

func validateMetadataReport(report corebackend.MetadataReport) error {
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

func validateMetadataTarget(target corebackend.MetadataTarget) error {
	identity := []string{target.WorkspaceID, target.Label, target.PaneID, target.TerminalID}
	if slices.Contains(identity, "") {
		return fmt.Errorf("%w: metadata target is incomplete", corebackend.ErrOwnedIdentityMismatch)
	}
	provenance := []string{target.RepoKey, target.RepoRoot, target.CheckoutPath}
	if slices.Contains(provenance, "") {
		return fmt.Errorf("%w: metadata target worktree provenance is incomplete", corebackend.ErrOwnedIdentityMismatch)
	}
	return nil
}

func validateMetadataTokens(tokens []corebackend.MetadataToken) error {
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
	if len([]rune(value)) > corebackend.MaxMetadataTokenValue {
		return fmt.Errorf("value exceeds %d characters", corebackend.MaxMetadataTokenValue)
	}
	if strings.TrimSpace(value) != value || strings.ContainsFunc(value, unicode.IsControl) {
		return fmt.Errorf("value is not trimmed control-free display text")
	}
	return nil
}
