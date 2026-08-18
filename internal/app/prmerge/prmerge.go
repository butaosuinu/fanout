// Package prmerge owns the domain decisions behind the dashboard's single
// mutation: which pull request on a snapshot row a merge request may address,
// whether that PR is in a state fanout will act on, and the two-step GitHub call
// (merge, then optionally delete the remote head ref).
//
// The decisions live here rather than in the HTTP handler because they are the
// part a future TUI merge action would reuse, and because they are testable as
// plain values. Nothing in this package touches the local repository.
package prmerge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/butaosuinu/fanout/internal/app/sessionview"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
)

// The states fanout refuses on its own, before GitHub is contacted. Each one
// means "from what fanout can see, this request cannot be what the user meant",
// which is a different failure from "GitHub declined".
var (
	ErrPRNotOnRow    = errors.New("pull request is not on this session row")
	ErrAlreadyMerged = errors.New("pull request is already merged")
	ErrPRClosed      = errors.New("pull request is closed")
	ErrPRDraft       = errors.New("pull request is a draft")
	ErrPRConflicting = errors.New("pull request conflicts with its base branch")
	ErrPRPending     = errors.New("GitHub is already holding a merge for this pull request")
	ErrStaleHead     = errors.New("pull request head moved since the page rendered")
	ErrStaleBase     = errors.New("pull request was retargeted since the page rendered")
	ErrNoBranch      = errors.New("no remote branch to delete for this pull request")
	ErrForkHead      = errors.New("the pull request head lives in another repository")
	ErrForeignPR     = errors.New("pull request does not belong to this session row")
	ErrNotMerged     = errors.New("pull request is not merged")
	ErrBranchInUse   = errors.New("another open pull request still uses this branch")
)

// VerifyRowOwns rejects a pull request the row does not actually own.
//
// Two different gaps close here.
//
// Every row: the PR must target this repository. GitHub lets a pull request
// close an issue elsewhere ("Fixes owner/repo#N"), so closedByPullRequestsReferences
// can hand back a PR based in another repository. The number would then be
// resolved by `gh pr merge <N> -R <this repo>` against the wrong repository —
// either failing, or hitting whatever PR happens to carry that number here.
//
// Branch-backed rows: the head must be this repository's branch of that exact
// name. Issue rows attribute PRs through the closing-PR link, which is an
// identity — a fork's pull request legitimately closes your issue, and merging it
// is the point. Issue-less rows (plan tasks, @manual) instead ask
// "pullRequests(headRefName: <branch>)", which returns every PR whose head branch
// carries that name, a stranger's fork included. Displaying those was harmless;
// merging them is not.
func VerifyRowOwns(pv sessionview.PaneView, ref ghissue.PRRef, repo string) error {
	base := strings.TrimSpace(ref.BaseRepo)
	if base == "" {
		return fmt.Errorf("%w: the base repository of #%d is unknown", ErrForeignPR, ref.Number)
	}
	if !strings.EqualFold(base, repo) {
		return fmt.Errorf("%w: #%d is based on %s", ErrForeignPR, ref.Number, base)
	}
	if pv.IssueNum > 0 {
		return nil
	}
	branch := strings.TrimSpace(pv.BranchName)
	if branch == "" {
		return nil
	}
	head := strings.TrimSpace(ref.HeadRepo)
	if head == "" {
		return fmt.Errorf("%w: the head repository of #%d is unknown", ErrForeignPR, ref.Number)
	}
	if !strings.EqualFold(head, repo) {
		return fmt.Errorf("%w: #%d is headed from %s", ErrForeignPR, ref.Number, head)
	}
	if strings.TrimSpace(ref.HeadRef) != branch {
		return fmt.Errorf("%w: #%d heads %q, not %q", ErrForeignPR, ref.Number, ref.HeadRef, branch)
	}
	return nil
}

// SelectRef returns the row's PR ref carrying exactly this number.
//
// It deliberately does not fall back to ghissue.PrimaryPR: that helper prefers
// the first MERGED ref, which is the wrong PR to act on — it would pick an old
// merged PR over the open one the button was drawn next to.
func SelectRef(pv sessionview.PaneView, repo string, number int) (ghissue.PRRef, error) {
	// Numbers repeat across repositories, and `Fixes owner/repo#N` puts other
	// repositories' pull requests on a row. Taking the first match by number
	// alone can hand back a foreign #7 while this repository's #7 sits behind
	// it — VerifyRowOwns then refuses a request that was right all along.
	var fallback *ghissue.PRRef
	for i, pr := range pv.PRs {
		if pr.Number != number {
			continue
		}
		if strings.EqualFold(pr.BaseRepo, repo) {
			return pr, nil
		}
		if fallback == nil {
			fallback = &pv.PRs[i]
		}
	}
	if fallback != nil {
		// Kept so the caller's ownership check reports why, rather than this
		// reporting the number as absent from a row that carries it.
		return *fallback, nil
	}
	return ghissue.PRRef{}, fmt.Errorf("%w: #%d", ErrPRNotOnRow, number)
}

// Preflight rejects the states fanout will not act on.
//
// Review decision and CI status are deliberately not gates. Enforcing branch
// protection is GitHub's job, and duplicating it here would kill the button
// permanently in a repository that requires neither. The snapshot is also up to
// one GitHub poll stale, so gating on CI would routinely refuse a merge whose
// checks are already green.
//
// A retarget is refused the same way a moved head is: the reviewer approved a
// merge into one branch, and GitHub lets the base change without touching the
// head.
//
// An empty Mergeable passes: it means "not known" — every merged or closed PR
// reports it, as does the window while GitHub recomputes after a base push — and
// treating it as a conflict would block ordinary merges.
func Preflight(ref ghissue.PRRef, rendered RenderedRef) error {
	switch {
	case strings.EqualFold(ref.State, "MERGED") || ref.MergedAt != nil:
		return ErrAlreadyMerged
	case strings.EqualFold(ref.State, "CLOSED"):
		return ErrPRClosed
	case ref.IsDraft:
		return ErrPRDraft
	case ref.HasConflict():
		return ErrPRConflicting
	case ref.AutoMerge || ref.Queued:
		// Someone already asked GitHub to merge this — the web UI, another gh, or
		// an earlier click here. Sending a second request would not make it merge
		// any sooner, and the button promises to be inactive while GitHub holds
		// one.
		return ErrPRPending
	}
	return rendered.check(ref)
}

// check compares the live ref against what the client had on screen. A field the
// client never rendered cannot be pinned, so an empty value skips its guard.
func (rendered RenderedRef) check(ref ghissue.PRRef) error {
	if ref.HeadSha != "" && ref.HeadSha != rendered.HeadSha {
		return fmt.Errorf("%w: row has %s", ErrStaleHead, ref.HeadSha)
	}
	if ref.BaseRef != "" && rendered.BaseRef != "" && ref.BaseRef != rendered.BaseRef {
		return fmt.Errorf("%w: now targets %s, not %s", ErrStaleBase, ref.BaseRef, rendered.BaseRef)
	}
	return nil
}

// RenderedRef is what the client had on screen. Both fields are compared against
// the live ref: a pull request can be retargeted without its head moving, so the
// SHA alone does not pin where the merge lands.
type RenderedRef struct {
	HeadSha string
	BaseRef string
}

// PlanDelete resolves the remote head ref the caller may delete after a merge.
//
// The name must come from the pull request itself. Falling back to the row's
// recorded branch would delete a ref with no evidence it is the one the merge
// consumed, and the mismatch check below would compare the value against itself
// and always pass. An empty HeadRef means the ref came from a non-GraphQL path,
// which is the same degraded case where HeadSha is empty and the merge already
// runs without --match-head-commit; refusing to delete there keeps the two
// guards from going dark together.
//
// A disagreement is refused for the same reason: the row's branch is fanout's
// own name for the work, so deleting on a mismatch removes a branch this row
// never owned.
func PlanDelete(pv sessionview.PaneView, ref ghissue.PRRef, repo string) (string, error) {
	branch := strings.TrimSpace(ref.HeadRef)
	if branch == "" {
		return "", fmt.Errorf("%w: the pull request head ref is unknown", ErrNoBranch)
	}
	// A fork's head ref lives in another repository, so deleting by name in the
	// base repo would remove a same-named branch this pull request never owned.
	// Issue rows accept fork PRs, which makes an unknown head repository exactly
	// the case that must not fall through to "same repo by default".
	head := strings.TrimSpace(ref.HeadRepo)
	if head == "" {
		return "", fmt.Errorf("%w: the head repository of #%d is unknown", ErrForkHead, ref.Number)
	}
	if !strings.EqualFold(head, repo) {
		return "", fmt.Errorf("%w: head is on %s, not %s", ErrForkHead, head, repo)
	}
	if recorded := strings.TrimSpace(pv.BranchName); recorded != "" && branch != recorded {
		return "", fmt.Errorf("%w: row records %q but the pull request head is %q", ErrNoBranch, recorded, branch)
	}
	return branch, nil
}

// Port is the GitHub mutation surface this package needs. ghissue.Runner
// satisfies it.
type Port interface {
	MergePR(ctx context.Context, req ghissue.MergePRRequest) error
	PRState(ctx context.Context, owner, repo string, number int) (ghissue.PRTarget, error)
	OpenPRNumbersForHead(ctx context.Context, owner, repo, branch string) ([]int, error)
	DeleteRemoteBranch(ctx context.Context, owner, repo, branch, expectedOID string) error
}

// Request is one fully-resolved merge. Callers build it from SelectRef,
// Preflight, and PlanDelete; Merge repeats none of those checks.
type Request struct {
	Owner   string
	Repo    string
	Number  int
	Method  ghissue.MergeMethod
	HeadSha string
	// BaseRef is the merge target the client rendered, re-checked live before the
	// merge: a retarget does not move the head, so the SHA cannot pin it.
	BaseRef string
	// Row is how the snapshot row claimed this pull request, re-checked live for
	// the same reason: the claim can be edited away without moving a commit.
	Row RowIdentity
}

// RowIdentity is what makes a pull request this row's to merge. An issue row
// owns it through the closing-issue link; a branch-backed row owns it by heading
// that branch in this repository. Exactly one applies.
type RowIdentity struct {
	IssueNum int
	Branch   string
}

// DeleteRequest is one remote head-ref delete, which is a separate action taken
// after a merge has landed — the way GitHub's own "Delete branch" button works.
//
// Keeping it separate is what makes it simple: deleting a ref is idempotent
// (already gone counts as done), so none of the never-repeat-an-ambiguous-
// mutation machinery the merge needs applies here.
type DeleteRequest struct {
	Owner   string
	Repo    string
	Number  int
	Branch  string
	HeadSha string
}

// Result reports what actually happened. Merged is GitHub's own answer, not
// "gh exited 0": a merge-queue base accepts the request and merges later, so the
// two are different facts.
type Result struct {
	Merged bool
	// Queued is true when GitHub accepted the request but the pull request is
	// not merged yet (a merge queue).
	Queued bool
	// Unknown is true when the merge command succeeded but its outcome could not
	// be confirmed. The pull request may well be merged, so the caller must not
	// present this as a failure to retry — resending would fire a second merge
	// against an unknown state.
	Unknown bool
}

type Service struct{ GH Port }

// Merge performs the merge and confirms it with GitHub.
//
// gh exiting 0 is not proof of a merge: a merge-queue base enqueues the pull
// request and returns success, so GitHub is asked before anything is claimed.
//
// A failed confirmation comes back as Unknown rather than as an error. The merge
// command already ran, so calling it a failure invites a retry that would merge
// again against a state nobody has looked at.
//
// There is no errs.Wrap here: ghissue.MergePR already names the pull request,
// and docs/error-handling.ja.md forbids stating the same identity twice — the
// doubled prefix reads as two different failures once it reaches the UI.
func (s Service) Merge(ctx context.Context, req Request) (Result, error) {
	if s.GH == nil {
		return Result{}, errors.New("no GitHub port configured")
	}
	// The snapshot is up to one poll stale, and --match-head-commit pins only the
	// head. Re-read the live pull request so a retarget in that window cannot
	// land the merge on a branch nobody reviewed it against.
	if err := s.fenceLive(ctx, req); err != nil {
		return Result{}, err
	}
	if err := s.GH.MergePR(ctx, ghissue.MergePRRequest{
		Owner: req.Owner, Repo: req.Repo, Number: req.Number,
		Method: req.Method, HeadSha: req.HeadSha,
	}); err != nil {
		return s.classifySendFailure(ctx, req, err)
	}
	live, err := s.GH.PRState(ctx, req.Owner, req.Repo, req.Number)
	if err != nil {
		//nolint:nilerr // See the doc comment: an executed mutation must not be
		// reported as a retryable failure.
		return Result{Unknown: true}, nil
	}
	return Result{Merged: live.Merged, Queued: !live.Merged}, nil
}

// fenceLive re-checks the fields the snapshot cannot keep fresh, immediately
// before the irreversible call.
func (s Service) fenceLive(ctx context.Context, req Request) error {
	live, err := s.GH.PRState(ctx, req.Owner, req.Repo, req.Number)
	if err != nil {
		return err
	}
	if live.Merged {
		return ErrAlreadyMerged
	}
	if live.AutoMerge || live.Queued {
		// The snapshot is up to one poll old, so a merge GitHub started holding
		// since then only shows up here — armed as an auto-merge, or sitting in
		// the queue.
		return ErrPRPending
	}
	if err := req.stillOwns(live); err != nil {
		return err
	}
	return RenderedRef{HeadSha: req.HeadSha, BaseRef: req.BaseRef}.check(ghissue.PRRef{
		HeadSha: live.HeadSha,
		BaseRef: live.BaseRef,
	})
}

// stillOwns re-checks the link that made this pull request the row's, against
// what GitHub says now.
//
// Admission checked it against the snapshot, which is up to one poll old, and
// neither way of losing the claim moves a commit: an issue row loses it when the
// closing keyword is edited out of the body, and a branch row loses it when the
// head branch is renamed. The head SHA and base fences see neither.
//
// The issue side asks for the whole reference, repository included. `Fixes
// owner/repo#N` closes issues elsewhere and numbers repeat, so a link retargeted
// to another repository's #7 would otherwise read as unchanged. An empty list is
// a refusal, not a pass: the field is always requested here, so nothing coming
// back means this pull request closes nothing.
func (req Request) stillOwns(live ghissue.PRTarget) error {
	row := req.Row
	if row.IssueNum > 0 {
		if slices.ContainsFunc(live.ClosesIssues, req.closes) {
			return nil
		}
		return fmt.Errorf("%w: #%d no longer closes %s#%d",
			ErrForeignPR, req.Number, req.Owner+"/"+req.Repo, row.IssueNum)
	}
	if row.Branch == "" || live.HeadRef == "" || live.HeadRef == row.Branch {
		return nil
	}
	return fmt.Errorf("%w: #%d now heads %q, not %q",
		ErrForeignPR, req.Number, live.HeadRef, row.Branch)
}

func (req Request) closes(issue ghissue.ClosingIssue) bool {
	return issue.Number == req.Row.IssueNum &&
		strings.EqualFold(issue.Repo, req.Owner+"/"+req.Repo)
}

// classifySendFailure decides whether a failed merge command is safe to retry.
//
// A deadline or a dropped connection can land after GitHub already accepted the
// request, so the error alone does not say whether the merge happened. Errors
// that provably precede the send are excluded first. Asking GitHub settles the
// rest: a merge that went through is reported as merged, and a state that cannot
// be read at all becomes Unknown rather than a retryable failure. Only a clean
// refusal with the pull request still unmerged is handed back as the original
// error, because that is the one case where clicking again is correct.
func (s Service) classifySendFailure(ctx context.Context, req Request, sendErr error) (Result, error) {
	// Failures that provably precede the send are excluded before the probe: the
	// rate-limit gate refuses before running gh, and a missing binary or rejected
	// credentials never got as far as GitHub. Probing after one of these would
	// fail the same way and turn a clear, retryable error into a hold that no
	// poll can clear — the pull request stays OPEN, so nothing ever settles it.
	if errors.Is(sendErr, ghissue.ErrRateLimited) || ghissue.IsPreSendFailure(sendErr) {
		return Result{}, sendErr
	}
	live, probeErr := s.GH.PRState(ctx, req.Owner, req.Repo, req.Number)
	switch {
	case probeErr != nil:
		//nolint:nilerr // See the doc comment: the merge command may already have
		// reached GitHub, so neither error may be surfaced as retryable.
		return Result{Unknown: true}, nil
	case live.Merged:
		return Result{Merged: true}, nil
	case live.AutoMerge || live.Queued:
		// GitHub is holding a merge that only this command can have started —
		// armed as an auto-merge, or sitting in the queue — so the request landed
		// and the failure was in the response. Reporting it as a plain error would
		// invite a click that sends the same mutation again.
		return Result{Queued: true}, nil
	case ghissue.IsTransportFailure(sendErr):
		// Not merged yet, but the connection dropped rather than GitHub saying no.
		// A merge queue that accepted the entry looks exactly like this, so the
		// outcome is unknown and a retry would resend it.
		return Result{Unknown: true}, nil
	}
	return Result{}, sendErr
}

// DeleteBranch removes a merged pull request's remote head ref.
//
// It re-reads the pull request first. The button only appears on a row the
// snapshot calls merged, but the snapshot is up to one poll stale, and deleting
// the head ref of a pull request that is not actually merged discards commits
// that live nowhere else.
func (s Service) DeleteBranch(ctx context.Context, req DeleteRequest) error {
	if s.GH == nil {
		return errors.New("no GitHub port configured")
	}
	live, err := s.GH.PRState(ctx, req.Owner, req.Repo, req.Number)
	if err != nil {
		return err
	}
	if !live.Merged {
		return ErrNotMerged
	}
	if err = req.stillAddressed(live); err != nil {
		return err
	}
	// Two pull requests can share a head branch when they target different bases.
	// Merging one does not finish the branch: deleting it here would leave the
	// other one unmergeable, with its commits gone.
	others, err := s.GH.OpenPRNumbersForHead(ctx, req.Owner, req.Repo, req.Branch)
	if err != nil {
		return err
	}
	for _, num := range others {
		if num != req.Number {
			return fmt.Errorf("%w: #%d", ErrBranchInUse, num)
		}
	}
	return s.GH.DeleteRemoteBranch(ctx, req.Owner, req.Repo, req.Branch, live.HeadSha)
}

// stillAddressed checks that the merged pull request GitHub reports is the one
// the click was aimed at.
//
// The head commit is compared against GitHub's own view, never taken from the
// request body: fencing on a client-named SHA would only prove the client can
// name the ref's current tip, so a commit pushed onto the branch after the merge
// would be deleted along with it. The body is kept as an echo — it has to agree
// with what the row rendered.
//
// The branch name is checked separately because a rename moves the ref without
// moving the commit, so the SHA says nothing about it. Deleting the recorded
// name would then 404 — which this package reads as "already gone" — and report
// a cleanup that left the renamed branch standing.
func (req DeleteRequest) stillAddressed(live ghissue.PRTarget) error {
	if req.HeadSha != live.HeadSha {
		return ErrStaleHead
	}
	if live.HeadRef != "" && live.HeadRef != req.Branch {
		return fmt.Errorf("%w: head branch is now %q", ErrStaleHead, live.HeadRef)
	}
	return nil
}
