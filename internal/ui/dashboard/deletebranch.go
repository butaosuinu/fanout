package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/butaosuinu/fanout/internal/app/prmerge"
	"github.com/butaosuinu/fanout/internal/app/sessionview"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
)

const deleteBranchPath = "/api/pr/delete-branch"

// deleteBranchRequestBody names the pull request whose head ref to remove. The
// head SHA fences the delete: the ref must still point at what the client saw,
// so a push that landed after the merge is not discarded.
type deleteBranchRequestBody struct {
	PRNumber int    `json:"prNumber"`
	HeadSha  string `json:"headSha"`
}

type deleteBranchResponse struct {
	PRNumber int    `json:"prNumber"`
	Branch   string `json:"branch"`
	Deleted  bool   `json:"deleted"`
}

// handleDeleteBranch removes a merged pull request's remote head ref.
//
// It is a separate route from the merge, the way GitHub's own "Delete branch"
// button is a separate action. That separation is the whole reason this handler
// is short: deleting a ref is idempotent, so it needs none of the machinery that
// keeps an ambiguous merge from being repeated. What it does need is proof of
// ownership — the ref must belong to this pull request, in this repository, and
// still point at the commit the client rendered.
func (s *Server) handleDeleteBranch(w http.ResponseWriter, r *http.Request) {
	if !s.mergeEnabled(w) {
		return
	}
	if s.deleteBranch == nil {
		apiError(w, http.StatusServiceUnavailable, "merge_unavailable",
			"this dashboard was started without branch deletion support", "")
		return
	}
	rr, ok := s.mergeRepo(w)
	if !ok {
		return
	}
	pv, ok := s.mergeRow(w, r)
	if !ok {
		return
	}
	req, ok := deleteBranchPayload(w, r, pv, rr)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), mergeDeadline)
	defer cancel()
	if err := s.deleteBranch(ctx, req); err != nil {
		status, code := deleteBranchStatus(err)
		apiError(w, status, code, "the branch was not deleted", redactGHDetail(err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(deleteBranchResponse{
		PRNumber: req.Number, Branch: req.Branch, Deleted: true,
	})
}

// deleteBranchPayload resolves which ref may be removed: the pull request has to
// be on the row, owned by it, and its head ref has to be one this repository
// actually holds.
func deleteBranchPayload(
	w http.ResponseWriter,
	r *http.Request,
	pv sessionview.PaneView,
	rr repoRef,
) (prmerge.DeleteRequest, bool) {
	var body deleteBranchRequestBody
	if err := decodeJSONBody(w, r, &body); err != nil {
		status, code := mergeBodyStatus(err)
		apiError(w, status, code, err.Error(), "")
		return prmerge.DeleteRequest{}, false
	}
	if body.PRNumber <= 0 || body.HeadSha == "" {
		apiError(w, http.StatusBadRequest, "invalid_body",
			"prNumber and headSha are required", "")
		return prmerge.DeleteRequest{}, false
	}
	ref, branch, err := ownedBranch(pv, rr, body.PRNumber)
	if err != nil {
		apiError(w, http.StatusConflict, mergePreflightCode(err), err.Error(), "")
		return prmerge.DeleteRequest{}, false
	}
	return prmerge.DeleteRequest{
		Owner: rr.owner, Repo: rr.repo, Number: ref.Number,
		Branch: branch, HeadSha: body.HeadSha,
	}, true
}

// ownedBranch resolves the ref this row may delete: the pull request has to be
// on the row, owned by it, and its head has to live in this repository.
func ownedBranch(
	pv sessionview.PaneView,
	rr repoRef,
	number int,
) (ghissue.PRRef, string, error) {
	repo := rr.owner + "/" + rr.repo
	ref, err := prmerge.SelectRef(pv, repo, number)
	if err != nil {
		return ghissue.PRRef{}, "", err
	}
	if err = prmerge.VerifyRowOwns(pv, ref, repo); err != nil {
		return ghissue.PRRef{}, "", err
	}
	branch, err := prmerge.PlanDelete(pv, ref, repo)
	return ref, branch, err
}

// deleteBranchStatus separates fanout's own refusals from GitHub's. The delete
// has no separate preflight step — its checks run inside the same call — so the
// sentinels arrive here mixed with transport and gh failures. Falling through to
// mergeFailureStatus would report every one of them as 422 "GitHub declined",
// which is both the wrong cause and the wrong machine code for a client that
// wants to tell "the branch is still in use" from "GitHub said no".
func deleteBranchStatus(err error) (int, string) {
	for _, c := range mergePreflightSentinels {
		if errors.Is(err, c.sentinel) {
			return http.StatusConflict, c.code
		}
	}
	return mergeFailureStatus(err)
}
