package herdrrun

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
)

var (
	_ corebackend.Backend                 = (*Backend)(nil)
	_ corebackend.DashboardShortcutBinder = (*Backend)(nil)
	_ corebackend.OwnedCloser             = (*Backend)(nil)
	_ corebackend.DryRunPreviewer         = (*Backend)(nil)
)

// Backend observes one named herdr session. New returns an unowned handle, so
// targeted reads and mutations remain disabled until EnsureOwned and an
// immutable target admission explicitly bind them.
type Backend struct {
	*herdrCLI
	previewOnly bool
	owner       *ownedAdmission
	target      *ownedTargetAdmission
}

// New constructs a herdr backend for one named session. socketPath may be
// empty on the first probe; CheckAvailable resolves it through an explicit
// --session status call, then pins subsequent probes to the returned path.
func New(session, socketPath string) *Backend {
	return &Backend{herdrCLI: newHerdrCLI(strings.TrimSpace(session), socketPath)}
}

// NewPreview constructs a mutation-free launch preview backend. Availability
// checks only the CLI version and does not require or probe a named session.
func NewPreview() *Backend {
	backend := New("", "")
	backend.previewOnly = true
	return backend
}

func (b *Backend) Name() corebackend.Name { return corebackend.Herdr }

// MutationModel reports that herdr mutations are journaled: every workspace,
// worktree, and agent mutation crosses the herdr server, so a request can be
// issued without its response arriving. The caller must record the intent
// before issuing it and reconcile the outcome afterwards.
func (b *Backend) MutationModel() corebackend.MutationModel {
	return corebackend.MutationJournaled
}

// CheckAvailable verifies the stable version floor. Normal backends also
// require a connected server; preview backends stop after the CLI check.
func (b *Backend) CheckAvailable() error {
	if b.previewOnly {
		return b.checkPreviewAvailable()
	}
	_, err := b.probe()
	return err
}

func (b *Backend) checkPreviewAvailable() error {
	binary, err := b.lookPath(commandName)
	if err != nil {
		return fmt.Errorf("herdr stable >=%s is required: %w", minimumVersion, err)
	}
	versionOut, err := b.runContext(context.Background(), commandTimeout, binary, route{}, "--version")
	if err != nil {
		return fmt.Errorf("herdr --version: %w", err)
	}
	_, err = parseAdmittedVersion(versionOut)
	return err
}

// PreviewLaunch renders the herdr commands a launch would run, one per line
// and without indentation or color. The workspace and worktree ids a real
// launch resolves are shown as placeholders because a dry run creates nothing.
// The exact text is pinned by the Tier 2 dry-run goldens.
func (*Backend) PreviewLaunch(preview corebackend.LaunchPreview) []string {
	return []string{
		"$ herdr workspace create --cwd " + corebackend.PreviewQuote(preview.ProjectRoot) +
			" --label <coordinator_nonce> --no-focus",
		"$ herdr worktree create --workspace <coordinator_id> --branch " + corebackend.PreviewQuote(preview.BranchName) +
			" --path " + corebackend.PreviewQuote(preview.WorktreePath) + " --label <worktree_nonce> --no-focus",
		"# wait for the operation-bound fanout launcher marker, issue one token, and verify the exact agent session",
		"# agent argv: " + preview.Command,
		"# would write coordinator and child Herdr identities to .fanout/state.json",
		"# would report display-only sidebar tokens with --source " + MetadataSource + " to the child workspace and pane",
	}
}

// ListLive returns the aggregate session.snapshot projection. Connected status
// is rechecked for each call; binary version admission is digest-cached.
func (b *Backend) ListLive() ([]corebackend.LivePane, error) {
	probed, err := b.probe()
	if err != nil {
		return nil, err
	}
	return b.snapshot(context.Background(), commandTimeout, probed)
}

// Wait probes the exact compatibility tuple once, then polls only aggregate
// snapshots until match succeeds or the fixed budget terminates. A zero
// totalTimeout selects corebackend.DefaultWaitTimeout; non-zero values must be
// whole seconds and at least three seconds. match receives a cloned compatible
// snapshot and should perform only bounded in-memory inspection.
func (b *Backend) Wait(ctx context.Context, totalTimeout time.Duration, match func([]corebackend.LivePane) bool) corebackend.WaitResult {
	if ctx == nil {
		return failedWait(fmt.Errorf("herdr wait requires a context"))
	}
	totalTimeout, err := normalizeWaitTimeout(totalTimeout)
	if err != nil {
		return failedWait(err)
	}
	if match == nil {
		return failedWait(fmt.Errorf("herdr wait requires a snapshot predicate"))
	}

	waitCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	if cause := waitCtx.Err(); cause != nil {
		return cancelledWait(cause)
	}
	probed, err := b.probeContext(waitCtx)
	if err != nil {
		if cause := waitCtx.Err(); cause != nil {
			return cancelledWait(cause)
		}
		return failedWait(err)
	}

	deadline := b.now().Add(totalTimeout)
	callLimit := waitSnapshotCallLimit(totalTimeout)
	var (
		lastStart time.Time
		lastPanes []corebackend.LivePane
		lastErr   error
		lastValid bool
	)
	for attempt := range callLimit {
		if attempt > 0 {
			if result, done := b.waitForNextSnapshot(waitCtx, deadline, lastStart, lastPanes, lastErr, lastValid); done {
				return result
			}
		}
		if cause := waitCtx.Err(); cause != nil {
			return cancelledWait(cause)
		}

		now := b.now()
		remaining := deadline.Sub(now)
		if remaining <= 0 {
			return finishWait(lastPanes, lastErr, lastValid)
		}
		lastStart = now
		callTimeout := min(commandTimeout, remaining)
		panes, snapshotErr := b.snapshot(waitCtx, callTimeout, probed)
		if snapshotErr != nil {
			if cause := waitCtx.Err(); cause != nil {
				return cancelledWait(cause)
			}
			lastPanes = nil
			lastErr = snapshotErr
			lastValid = false
			if !corebackend.IsRetryableObservationError(snapshotErr) {
				return failedWait(snapshotErr)
			}
			continue
		}

		lastPanes = cloneLivePanes(panes)
		lastErr = nil
		lastValid = true
		if cause := waitCtx.Err(); cause != nil {
			return cancelledWait(cause)
		}
		if !b.now().Before(deadline) {
			return finishWait(lastPanes, nil, true)
		}
		matched := match(cloneLivePanes(panes))
		if cause := waitCtx.Err(); cause != nil {
			return cancelledWait(cause)
		}
		if !b.now().Before(deadline) {
			return finishWait(lastPanes, nil, true)
		}
		if matched {
			return corebackend.WaitResult{Status: corebackend.WaitMatched, Panes: cloneLivePanes(panes)}
		}
	}
	return finishWait(lastPanes, lastErr, lastValid)
}

func (b *Backend) Launch(corebackend.LaunchRequest) (corebackend.PaneRef, error) {
	return corebackend.PaneRef{}, corebackend.Unsupported(corebackend.Herdr, "launch")
}

func (b *Backend) ReleaseStartGate(string) error {
	return corebackend.Unsupported(corebackend.Herdr, "release start gate")
}

func (b *Backend) Read(ref corebackend.PaneRef, lines int) (string, error) {
	return b.readCore(ref, lines)
}

func (b *Backend) SendLine(ref corebackend.PaneRef, line string) error {
	return b.sendLineCore(ref, line)
}

func (b *Backend) Focus(ref corebackend.PaneRef) error {
	return b.focusCore(ref)
}

func (b *Backend) Close(ref corebackend.PaneRef) error {
	return b.closeCore(ref)
}

func normalizeWaitTimeout(totalTimeout time.Duration) (time.Duration, error) {
	if totalTimeout == 0 {
		return corebackend.DefaultWaitTimeout, nil
	}
	if totalTimeout < minimumWaitTimeout || totalTimeout%time.Second != 0 {
		return 0, fmt.Errorf("herdr wait total_timeout must be a whole number of seconds at least 3, got %s", totalTimeout)
	}
	return totalTimeout, nil
}

func waitSnapshotCallLimit(totalTimeout time.Duration) int {
	return int((totalTimeout-1)/waitInterval) + 1
}

func (b *Backend) waitForNextSnapshot(
	ctx context.Context,
	deadline time.Time,
	lastStart time.Time,
	lastPanes []corebackend.LivePane,
	lastErr error,
	lastValid bool,
) (corebackend.WaitResult, bool) {
	now := b.now()
	if !now.Before(deadline) {
		return finishWait(lastPanes, lastErr, lastValid), true
	}
	nextStart := lastStart.Add(waitInterval)
	if !now.Before(nextStart) {
		return corebackend.WaitResult{}, false
	}
	delay := nextStart.Sub(now)
	if remaining := deadline.Sub(now); delay > remaining {
		delay = remaining
	}
	if err := b.sleep(ctx, delay); err != nil {
		if cause := ctx.Err(); cause != nil {
			return cancelledWait(cause), true
		}
		return failedWait(fmt.Errorf("wait for next herdr snapshot: %w", err)), true
	}
	if cause := ctx.Err(); cause != nil {
		return cancelledWait(cause), true
	}
	if !b.now().Before(deadline) {
		return finishWait(lastPanes, lastErr, lastValid), true
	}
	return corebackend.WaitResult{}, false
}

func finishWait(lastPanes []corebackend.LivePane, lastErr error, lastValid bool) corebackend.WaitResult {
	if lastValid {
		return corebackend.WaitResult{
			Status: corebackend.WaitTimedOut,
			Panes:  cloneLivePanes(lastPanes),
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("herdr wait ended without a compatible snapshot")
	}
	return failedWait(lastErr)
}

func cloneLivePanes(panes []corebackend.LivePane) []corebackend.LivePane {
	if panes == nil {
		return nil
	}
	cloned := append([]corebackend.LivePane(nil), panes...)
	for i := range cloned {
		if cloned[i].AgentSession != nil {
			session := *cloned[i].AgentSession
			cloned[i].AgentSession = &session
		}
		if cloned[i].ProcessIdentity != nil {
			identity := *cloned[i].ProcessIdentity
			cloned[i].ProcessIdentity = &identity
		}
	}
	return cloned
}

func failedWait(err error) corebackend.WaitResult {
	return corebackend.WaitResult{Status: corebackend.WaitFailed, Err: err}
}

func cancelledWait(err error) corebackend.WaitResult {
	return corebackend.WaitResult{Status: corebackend.WaitCancelled, Err: err}
}

func (b *Backend) snapshot(ctx context.Context, timeout time.Duration, probed probeResult) ([]corebackend.LivePane, error) {
	out, err := b.runContext(ctx, timeout, probed.binary, probed.route, "api", "snapshot")
	if err != nil {
		wrapped := readMethodError("session.snapshot", err)
		if retryableCommandError(err) {
			return nil, retryableObservationError{err: wrapped}
		}
		return nil, wrapped
	}
	var envelope snapshotEnvelope
	if parseErr := decodeOne(out, &envelope); parseErr != nil {
		return nil, methodUnavailable("session.snapshot")
	}
	panes, err := projectSnapshot(envelope, probed)
	if err != nil {
		return nil, methodUnavailable("session.snapshot")
	}
	return panes, nil
}
