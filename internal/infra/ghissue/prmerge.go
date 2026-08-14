package ghissue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/errs"
	"github.com/butaosuinu/fanout/internal/infra/execx"
)

// MergeMethod is the strategy `gh pr merge` applies. The values are the wire
// tokens the dashboard sends, and ParseMergeMethod is the only way to obtain
// one, so an unvalidated string can never reach the argv.
type MergeMethod string

const (
	MergeSquash MergeMethod = "squash"
	MergeCommit MergeMethod = "merge"
	MergeRebase MergeMethod = "rebase"
)

// ParseMergeMethod accepts exactly the three wire tokens. There is no default:
// a request that does not name its method is a bug, not a squash.
func ParseMergeMethod(s string) (MergeMethod, bool) {
	switch MergeMethod(s) {
	case MergeSquash, MergeCommit, MergeRebase:
		return MergeMethod(s), true
	}
	return "", false
}

func (m MergeMethod) flag() string {
	return "--" + string(m)
}

// MergePRRequest is one `gh pr merge` invocation. HeadSha "" omits
// --match-head-commit; every other field is required.
type MergePRRequest struct {
	Owner   string
	Repo    string
	Number  int
	Method  MergeMethod
	HeadSha string
}

func (req MergePRRequest) repoFlag() string {
	return req.Owner + "/" + req.Repo
}

// MergePR merges one pull request on GitHub.
//
// It never passes --admin, --auto, or --delete-branch. fanout does not bypass
// branch protection, does not arm a merge that fires later without a human, and
// never lets gh touch a local branch: gh's -d deletes the local branch too, and
// fanout's child branches are checked out in linked worktrees, so that delete
// fails and turns an already-completed merge into an error. Deleting the remote
// ref is a separate call (DeleteRemoteBranch) for the same reason.
func (r Runner) MergePR(ctx context.Context, req MergePRRequest) (err error) {
	defer errs.Wrap(&err, "merge pull request #%d", req.Number)

	if err = req.validate(); err != nil {
		return err
	}
	args := []string{
		"pr", "merge", strconv.Itoa(req.Number),
		"-R", req.repoFlag(),
		req.Method.flag(),
	}
	if req.HeadSha != "" {
		args = append(args, "--match-head-commit", req.HeadSha)
	}
	_, err = r.ghContext(ctx, args...)
	return err
}

func (req MergePRRequest) validate() error {
	if req.Owner == "" || req.Repo == "" {
		return errors.New("owner and repo are required")
	}
	if req.Number <= 0 {
		return fmt.Errorf("pull request number %d is not positive", req.Number)
	}
	if _, ok := ParseMergeMethod(string(req.Method)); !ok {
		return fmt.Errorf("unknown merge method %q", req.Method)
	}
	return nil
}

// PRTarget is a live read of the fields a merge must be fenced on. The snapshot
// is up to one poll stale, and a pull request can be retargeted or pushed to in
// that window, so these come straight from GitHub.
type PRTarget struct {
	Merged  bool
	BaseRef string
	HeadSha string
}

// PRState reads the pull request as GitHub sees it right now.
//
// It is used on both sides of the merge. Before: `gh pr merge` pins the head
// with --match-head-commit but says nothing about the base, so a retarget since
// the last poll would land the merge on a branch nobody reviewed. After: gh
// exiting 0 does not mean "merged" — a merge-queue base enqueues and returns
// success, and treating that as merged would report a lie and hand the head ref
// to a delete while its commits live only on that branch.
func (r Runner) PRState(ctx context.Context, owner, repo string, number int) (_ PRTarget, err error) {
	defer errs.Wrap(&err, "read state of pull request #%d", number)

	out, err := r.ghContext(ctx, "pr", "view", strconv.Itoa(number),
		"-R", owner+"/"+repo, "--json", "state,mergedAt,baseRefName,headRefOid")
	if err != nil {
		return PRTarget{}, err
	}
	var view struct {
		State       string  `json:"state"`
		MergedAt    *string `json:"mergedAt"`
		BaseRefName string  `json:"baseRefName"`
		HeadRefOid  string  `json:"headRefOid"`
	}
	if err := json.Unmarshal(out, &view); err != nil {
		return PRTarget{}, err
	}
	return PRTarget{
		Merged:  strings.EqualFold(view.State, "MERGED") || view.MergedAt != nil,
		BaseRef: view.BaseRefName,
		HeadSha: view.HeadRefOid,
	}, nil
}

// IsTransportFailure reports whether an error leaves the outcome unknown: gh
// could not reach GitHub, or was cut off, so the request may or may not have
// landed. A clean non-zero exit — GitHub answering "no" — is not one of these.
func IsTransportFailure(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range transportMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

var transportMarkers = []string{
	"executable file not found", "no such file or directory",
	"authentication", "gh auth login", "connection refused",
	"no route to host", "dial tcp", "i/o timeout", "connection reset",
	"eof", "broken pipe",
}

// refPath builds the git-refs API path. Each branch segment is escaped
// separately so the slashes stay path separators while everything else — `#` in
// particular, which is a legal ref character and would otherwise truncate the
// URL at a fragment — survives.
func refPath(owner, repo, branch, verb string) string {
	segments := strings.Split(branch, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return fmt.Sprintf("repos/%s/%s/git/%s/heads/%s",
		url.PathEscape(owner), url.PathEscape(repo), verb, strings.Join(segments, "/"))
}

// BranchOID reads a branch's current commit. "" means the ref is already gone.
func (r Runner) BranchOID(ctx context.Context, owner, repo, branch string) (_ string, err error) {
	defer errs.Wrap(&err, "read branch %q", branch)

	out, err := r.ghContext(ctx, "api", refPath(owner, repo, branch, "ref"), "-q", ".object.sha")
	if err != nil {
		if isMissingRefError(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// DeleteRemoteBranch deletes one head ref on GitHub, but only when it still
// points at expectedOID.
//
// The fence matters because the delete runs after the merge, and anything can
// have moved in between: a pane that kept working pushes new commits onto the
// branch, and deleting then discards work that was never merged.
//
// The check is not atomic, and cannot be: GitHub's ref API has no conditional
// delete, and neither does the GraphQL deleteRef mutation. A push that lands
// between the read and the DELETE is not caught. What the fence does catch is
// the realistic case — a push that already landed — leaving a window of one API
// round trip after a confirmed merge. Closing it entirely would mean pushing a
// lease over the git protocol, which this endpoint deliberately does not do.
//
// A ref that is already gone — GitHub's own auto-delete-head-branches, or a concurrent delete
// — counts as success, so retrying after a partial failure is not punished.
//
// It touches nothing local: the worktree and its local branch survive, and
// `fanout --cleanup` still owns their removal.
func (r Runner) DeleteRemoteBranch(
	ctx context.Context,
	owner, repo, branch, expectedOID string,
) (err error) {
	defer errs.Wrap(&err, "delete remote branch %q", branch)

	if owner == "" || repo == "" || strings.TrimSpace(branch) == "" {
		return errors.New("owner, repo, and branch are required")
	}
	if expectedOID == "" {
		return errors.New("refusing to delete without the expected commit")
	}
	current, err := r.BranchOID(ctx, owner, repo, branch)
	if err != nil {
		return err
	}
	if current == "" {
		return nil // already gone
	}
	if current != expectedOID {
		return fmt.Errorf("branch moved to %s since the merge; leaving it alone", current)
	}
	_, err = r.ghContext(ctx, "api", "--method", "DELETE", refPath(owner, repo, branch, "refs"))
	if err != nil && isMissingRefError(err) {
		return nil
	}
	return err
}

// isMissingRefError recognizes the two shapes GitHub uses for "that ref is not
// there": a 404 on the ref path, and the 422 "Reference does not exist" it
// returns when the path parses but the branch does not exist.
func isMissingRefError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "reference does not exist") ||
		strings.Contains(msg, "http 404")
}

// ghContext is gh bound to ctx, so an HTTP handler deadline actually kills the
// gh process instead of leaking it past the response.
func (r Runner) ghContext(ctx context.Context, args ...string) ([]byte, error) {
	return r.runGH(func() ([]byte, error) {
		return execx.OutputContext(ctx, r.Cwd, nil, "gh", args...)
	})
}
