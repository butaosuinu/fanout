package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/butaosuinu/fanout/internal/app/prmerge"
	"github.com/butaosuinu/fanout/internal/app/sessionview"
	"github.com/butaosuinu/fanout/internal/infra/atomicfs"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

const (
	mergePath = "/api/pr/merge"
	// mergeBodyMaxBytes is generous for four scalar fields and still small
	// enough that a runaway client cannot make the server buffer anything.
	mergeBodyMaxBytes = 4 << 10
	mergeDeadline     = 30 * time.Second
	// mergeDetailMaxBytes bounds how much of gh's stderr reaches the browser.
	mergeDetailMaxBytes = 500
)

// mergeRequestBody is the wire request. Every field is required except
// deleteBranch; method has no default, because a request that does not name its
// strategy is a bug rather than an implicit squash.
type mergeRequestBody struct {
	PRNumber int    `json:"prNumber"`
	HeadSha  string `json:"headSha"`
	BaseRef  string `json:"baseRef"`
	Method   string `json:"method"`
}

// mergeBody is mergeRequestBody after validation: the method is a parsed enum
// and the PR number is known positive, so nothing downstream re-checks either.
type mergeBody struct {
	prNumber int
	rendered prmerge.RenderedRef
	method   ghissue.MergeMethod
}

type mergeResponse struct {
	PRNumber int    `json:"prNumber"`
	Method   string `json:"method"`
	Merged   bool   `json:"merged"`
	// Queued is GitHub accepting the request without merging yet (a merge queue).
	// Nothing was deleted, and the row is not merged.
	Queued bool `json:"queued"`
	// Unknown is the merge command succeeding without a confirmable outcome. The
	// client must block a resend until a snapshot settles it.
	Unknown       bool `json:"unknown"`
	RefreshQueued bool `json:"refreshQueued"`
}

// repoRef is the owner/repo pair the poller already resolved.
type repoRef struct {
	owner string
	repo  string
}

// apiError is peekError's shape plus a machine code and an optional detail, so
// the SPA can branch on the failure without parsing prose. The message stays
// fixed per code; anything variable belongs in detail.
func apiError(w http.ResponseWriter, status int, code, msg, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	body := map[string]string{"error": msg, "code": code}
	if detail != "" {
		body["detail"] = detail
	}
	_ = json.NewEncoder(w).Encode(body)
}

// handleMerge serves the dashboard's only mutation.
//
// Everything that can refuse the request happens in admitMerge; what remains
// here is the irreversible part and the report of it. The local repository is
// untouched throughout: the worktree, its branch, and state.json all survive a
// merge unchanged, and `fanout --cleanup` still owns their removal.
func (s *Server) handleMerge(w http.ResponseWriter, r *http.Request) {
	req, rr, release, ok := s.admitMerge(w, r)
	if !ok {
		return
	}
	defer release()

	ctx, cancel := context.WithTimeout(r.Context(), mergeDeadline)
	defer cancel()
	res, err := s.mergePR(ctx, req)
	if err != nil {
		status, code := mergeFailureStatus(err)
		apiError(w, status, code, "GitHub refused the merge", redactGHDetail(err))
		return
	}
	if res.Unknown || res.Queued {
		// Both states mean GitHub already changed something: Unknown may have
		// merged, and a queued request has auto-merge armed. Losing only the
		// response would otherwise let another tab or a reload send it again, so
		// the refusal lives on the server until a poll settles the pull request.
		s.holdUnconfirmed(rr, req.Number)
	}
	writeMergeResponse(w, req, res, s.poller.requestGHRefresh())
}

// admitMerge runs every check that must pass before gh is invoked, in cost
// order, and reserves the pull request. It writes the refusal itself and
// reports ok=false; on success the caller owns the returned release func.
func (s *Server) admitMerge(
	w http.ResponseWriter,
	r *http.Request,
) (prmerge.Request, repoRef, func(), bool) {
	if !s.mergeEnabled(w) {
		return prmerge.Request{}, repoRef{}, nil, false
	}
	rr, ok := s.mergeRepo(w)
	if !ok {
		return prmerge.Request{}, repoRef{}, nil, false
	}
	pv, ok := s.mergeRow(w, r)
	if !ok {
		return prmerge.Request{}, repoRef{}, nil, false
	}
	req, ok := mergePayload(w, r, pv, rr)
	if !ok {
		return prmerge.Request{}, repoRef{}, nil, false
	}
	release, ok := s.claimMerge(w, rr, req.Number)
	if !ok {
		return prmerge.Request{}, repoRef{}, nil, false
	}
	return req, rr, release, true
}

// mergeEnabled refuses when the capability is unwired, and when the token gate
// is off.
//
// --no-token is documented for single-user laptops, and for reads that framing
// holds. A mutation is different: the loopback port is reachable by every local
// process and every other user on the machine, so an ungated merge endpoint
// hands them the ability to merge your pull requests. The revised invariant says
// this route sits behind requireToken; with no token that is vacuous, so the
// route closes instead of pretending.
func (s *Server) mergeEnabled(w http.ResponseWriter) bool {
	if s.mergePR == nil {
		apiError(w, http.StatusServiceUnavailable, "merge_unavailable",
			"this dashboard was started without merge support", "")
		return false
	}
	if s.token == "" {
		apiError(w, http.StatusForbidden, "token_required",
			"merging needs the access token; restart the dashboard without --no-token", "")
		return false
	}
	return true
}

func writeMergeResponse(
	w http.ResponseWriter,
	req prmerge.Request,
	res prmerge.Result,
	refreshQueued bool,
) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(mergeResponse{
		PRNumber:      req.Number,
		Method:        string(req.Method),
		Merged:        res.Merged,
		Queued:        res.Queued,
		Unknown:       res.Unknown,
		RefreshQueued: refreshQueued,
	})
}

// mergeClaimsFile is where unconfirmed merges are remembered. It sits next to
// dashboard.json rather than in memory because the guard has to outlive the
// process: a dashboard restarted after a lost response would otherwise let the
// same merge be fired again.
const mergeClaimsFile = "merge-claims.json"

func (s *Server) mergeClaimsPath() string {
	return filepath.Join(s.poller.projectRoot, ".fanout", mergeClaimsFile)
}

// readMergeClaims returns the recorded holds. A missing file reads as empty; a
// corrupt one does too, which is the one gap the in-memory mirror covers for the
// life of the process. Callers hold mergeMu.
func (s *Server) readMergeClaims() map[string]string {
	claims := map[string]string{}
	if _, err := atomicfs.ReadJSON(s.mergeClaimsPath(), &claims); err != nil {
		return map[string]string{}
	}
	return claims
}

// writeMergeClaims persists the holds. A failure is not reported: the in-memory
// mirror already keeps this process fail-closed, and the only thing lost is the
// cross-restart half of the guard, which there is no useful action to take on.
// Callers hold mergeMu.
func (s *Server) writeMergeClaims(claims map[string]string) {
	path := s.mergeClaimsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = atomicfs.WriteJSON(path, claims, 0o600)
}

// holdUnconfirmed records a pull request whose merge outcome is unreadable, so
// every entrypoint refuses a second attempt until GitHub settles it.
func (s *Server) holdUnconfirmed(rr repoRef, number int) {
	key := claimKey(rr, number)
	s.mergeMu.Lock()
	defer s.mergeMu.Unlock()
	// The in-process hold is unconditional. A disk that cannot be written (full,
	// read-only) costs the cross-restart half of the guard, but it must not also
	// hand this process a way to fire the same merge again.
	s.mergeHeld[key] = time.Now()
	claims := s.readMergeClaims()
	claims[key] = time.Now().UTC().Format(time.RFC3339)
	s.writeMergeClaims(claims)
}

// unconfirmed reports whether an earlier unreadable merge still blocks this pull
// request, clearing the hold once a poll shows the PR settled or the TTL passes.
// Callers hold mergeMu.
func (s *Server) unconfirmed(key string, number int) bool {
	if !s.held(key) {
		return false
	}
	// Only GitHub releases the hold. Time is not evidence about an outcome, so
	// there is no TTL: a merge that may have happened stays un-repeatable until a
	// poll shows the pull request merged or closed. The way out of a state that
	// never resolves is to delete the entry from .fanout/merge-claims.json, which
	// is the documented manual path.
	if !s.poller.prSettled(number) {
		return true
	}
	delete(s.mergeHeld, key)
	claims := s.readMergeClaims()
	delete(claims, key)
	s.writeMergeClaims(claims)
	return false
}

// held reports whether a hold is recorded, in memory or on disk (a restart has
// only the file). Callers hold mergeMu.
func (s *Server) held(key string) bool {
	if _, ok := s.mergeHeld[key]; ok {
		return true
	}
	_, ok := s.readMergeClaims()[key]
	return ok
}

func claimKey(rr repoRef, number int) string {
	return fmt.Sprintf("%s/%s#%d", rr.owner, rr.repo, number)
}

// mergeRepo reads the owner/repo the poller already resolved rather than
// resolving it again, so a degraded GitHub tier answers without touching gh.
func (s *Server) mergeRepo(w http.ResponseWriter) (repoRef, bool) {
	label, _, ghErr := s.poller.ghIdentity()
	owner, repo, found := strings.Cut(label, "/")
	if ghErr != nil || !found || owner == "" || repo == "" {
		apiError(w, http.StatusServiceUnavailable, "github_unresolved",
			"GitHub is unavailable for this checkout", "")
		return repoRef{}, false
	}
	return repoRef{owner: owner, repo: repo}, true
}

// mergeRow resolves the query identity to exactly one snapshot row, reusing the
// /api/diff parser so both endpoints address rows the same way.
func (s *Server) mergeRow(w http.ResponseWriter, r *http.Request) (sessionview.PaneView, bool) {
	identity, err := parseDiffIdentity(r.URL.Query())
	if err != nil {
		apiError(w, http.StatusBadRequest, "invalid_identity", err.Error(), "")
		return sessionview.PaneView{}, false
	}
	pv, ok := s.poller.snapshotDiffPane(identity)
	if !ok {
		apiError(w, http.StatusNotFound, "row_not_found", "no matching session row", "")
		return sessionview.PaneView{}, false
	}
	/* handleDiff と同じ行種ガード。クライアントの rowQuery はこの 2 種に null を
	 * 返してボタンを描かないので、サーバの addressable set を広いままにすると
	 * 3 段照合が守るはずの「画面で見えているものだけが対象」からずれる。 */
	if pv.NotStarted || pv.Kind == state.PaneKindShell {
		apiError(w, http.StatusNotFound, "row_not_found", "no matching session row", "")
		return sessionview.PaneView{}, false
	}
	return pv, true
}

// mergePayload validates the body against the row: the pull request must still
// be on it, and its state must be one fanout acts on. The client names the PR
// and the head SHA it rendered, so a row whose PR set changed under the page is
// refused here rather than merged by number alone.
func mergePayload(w http.ResponseWriter, r *http.Request, pv sessionview.PaneView, rr repoRef) (prmerge.Request, bool) {
	body, err := decodeMergeBody(w, r)
	if err != nil {
		status, code := mergeBodyStatus(err)
		apiError(w, status, code, err.Error(), "")
		return prmerge.Request{}, false
	}
	ref, err := selectMergeRef(pv, body, rr)
	if err != nil {
		apiError(w, http.StatusConflict, mergePreflightCode(err), err.Error(), "")
		return prmerge.Request{}, false
	}
	return mergeRequestFor(rr, ref, body), true
}

// selectMergeRef resolves the addressed pull request and runs every check that
// depends on the snapshot row: the PR must be on the row, the row must own it,
// and its state must be one fanout acts on.
func selectMergeRef(
	pv sessionview.PaneView,
	body mergeBody,
	rr repoRef,
) (ghissue.PRRef, error) {
	ref, err := prmerge.SelectRef(pv, body.prNumber)
	if err != nil {
		return ghissue.PRRef{}, err
	}
	if err := prmerge.VerifyRowOwns(pv, ref, rr.owner+"/"+rr.repo); err != nil {
		return ghissue.PRRef{}, err
	}
	return ref, prmerge.Preflight(ref, body.rendered)
}

// mergeRequestFor assembles the resolved merge. BaseRef comes from the row's own
// ref rather than the client's echo: a caller that omits baseRef would otherwise
// switch the retarget fence off. The echo is still compared against this value
// in Preflight.
func mergeRequestFor(rr repoRef, ref ghissue.PRRef, body mergeBody) prmerge.Request {
	return prmerge.Request{
		Owner: rr.owner, Repo: rr.repo, Number: ref.Number,
		Method: body.method, HeadSha: ref.HeadSha, BaseRef: ref.BaseRef,
	}
}

var errMergeBodyTooLarge = errors.New("request body is too large")

// decodeMergeBody reads the wire body and validates it on its own terms: size,
// shape, and the two fields that have no sensible default. Checks that need the
// snapshot row live in mergePayload.
// decodeJSONBody reads one bounded JSON document, refusing unknown fields and a
// second document on the same body — a request must not smuggle one shape past
// the decoder and act on another.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, into any) error {
	r.Body = http.MaxBytesReader(w, r.Body, mergeBodyMaxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return errMergeBodyTooLarge
		}
		return fmt.Errorf("invalid request body: %w", err)
	}
	if dec.More() {
		return errors.New("invalid request body: unexpected trailing data")
	}
	return nil
}

func decodeMergeBody(w http.ResponseWriter, r *http.Request) (mergeBody, error) {
	var body mergeRequestBody
	if err := decodeJSONBody(w, r, &body); err != nil {
		return mergeBody{}, err
	}
	if body.PRNumber <= 0 {
		return mergeBody{}, fmt.Errorf("invalid prNumber %d: want a positive integer", body.PRNumber)
	}
	if strings.TrimSpace(body.BaseRef) == "" {
		return mergeBody{}, errors.New("baseRef is required: it pins where the merge lands")
	}
	method, ok := ghissue.ParseMergeMethod(body.Method)
	if !ok {
		return mergeBody{}, fmt.Errorf("unknown merge method %q", body.Method)
	}
	return mergeBody{
		prNumber: body.PRNumber,
		rendered: prmerge.RenderedRef{HeadSha: body.HeadSha, BaseRef: body.BaseRef},
		method:   method,
	}, nil
}

func mergeBodyStatus(err error) (int, string) {
	if errors.Is(err, errMergeBodyTooLarge) {
		return http.StatusRequestEntityTooLarge, "body_too_large"
	}
	return http.StatusBadRequest, "invalid_body"
}

// mergePreflightSentinels maps the states fanout itself refuses to their wire
// codes. They are all 409: the request is well-formed and addresses a real row,
// but from what the snapshot shows it cannot be what the user meant. GitHub's
// own refusals are 422 (see mergeFailureStatus), which keeps the two causes
// distinguishable in the UI.
var mergePreflightSentinels = []struct {
	sentinel error
	code     string
}{
	{prmerge.ErrPRNotOnRow, "pr_not_on_row"},
	{prmerge.ErrAlreadyMerged, "already_merged"},
	{prmerge.ErrPRClosed, "pr_closed"},
	{prmerge.ErrPRDraft, "pr_draft"},
	{prmerge.ErrPRConflicting, "pr_conflicting"},
	{prmerge.ErrStaleHead, "stale_head"},
	{prmerge.ErrStaleBase, "stale_base"},
	{prmerge.ErrNoBranch, "no_branch"},
	{prmerge.ErrForkHead, "fork_head"},
	{prmerge.ErrForeignPR, "pr_not_on_row"},
	{prmerge.ErrNotMerged, "not_merged"},
	{prmerge.ErrBranchInUse, "branch_in_use"},
}

// mergePreflightCode names the refusal. The status is always 409, so it is the
// caller's literal rather than a return value.
func mergePreflightCode(err error) string {
	for _, c := range mergePreflightSentinels {
		if errors.Is(err, c.sentinel) {
			return c.code
		}
	}
	return "preflight_failed"
}

// mergeFailureStatus maps what came back from gh. A plain non-zero exit is 422
// "GitHub declined" — required checks, branch protection, a strategy the
// repository disables — which the client presents as "refresh and look", not as
// a fanout bug.
func mergeFailureStatus(err error) (int, string) {
	switch {
	case errors.Is(err, ghissue.ErrRateLimited):
		return http.StatusTooManyRequests, "rate_limited"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return http.StatusGatewayTimeout, "timeout"
	case isGHUnavailable(err):
		return http.StatusBadGateway, "github_unavailable"
	}
	return http.StatusUnprocessableEntity, "github_rejected"
}

// ghUnavailableMarkers are the failures that mean "gh could not reach GitHub at
// all", as opposed to "GitHub answered and said no".
var ghUnavailableMarkers = []string{
	"executable file not found", "no such file or directory",
	"authentication", "gh auth login", "connection refused",
	"no route to host", "dial tcp", "i/o timeout",
}

func isGHUnavailable(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, marker := range ghUnavailableMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// ghTokenRe matches GitHub's credential formats so a message the user pastes
// somewhere cannot carry one along. gh does not print tokens today; this is a
// fail-safe for a misconfigured environment's diagnostics, not a claim that it
// does.
var ghTokenRe = regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{16,}|github_pat_[A-Za-z0-9_]{20,}`)

// redactGHDetail bounds and scrubs gh's stderr before it reaches the browser.
// The endpoint is token-gated, so the audience is already the local user; what
// matters is that the text is finite and carries no credential.
func redactGHDetail(err error) string {
	return redactGHText(err.Error())
}

// redactGHText is the same scrub for a gh string that already left the error
// chain (Result.BranchDeleteError). Truncation backs up to a rune boundary:
// slicing mid-rune makes json.Encode rewrite the tail to U+FFFD, so the last
// visible character in the browser would be a replacement glyph.
func redactGHText(text string) string {
	if text == "" {
		return ""
	}
	out := ghTokenRe.ReplaceAllString(text, "[redacted]")
	out = strings.Join(strings.Fields(out), " ")
	if len(out) <= mergeDetailMaxBytes {
		return out
	}
	cut := mergeDetailMaxBytes
	for cut > 0 && !utf8.RuneStart(out[cut]) {
		cut--
	}
	return out[:cut] + "…"
}

// claimMerge serializes concurrent merges of the same pull request. Without it
// an impatient double-click spawns two gh processes racing on one PR, and the
// loser reports whatever GitHub happened to say — neither stable nor
// explainable in the UI.
func (s *Server) claimMerge(w http.ResponseWriter, rr repoRef, number int) (func(), bool) {
	key := claimKey(rr, number)
	s.mergeMu.Lock()
	defer s.mergeMu.Unlock()
	if _, busy := s.mergeInFlight[key]; busy {
		apiError(w, http.StatusConflict, "merge_in_flight",
			"a merge for this pull request is already running", "")
		return nil, false
	}
	if s.unconfirmed(key, number) {
		apiError(w, http.StatusConflict, "merge_unconfirmed",
			"an earlier merge for this pull request has not been confirmed yet", "")
		return nil, false
	}
	s.mergeInFlight[key] = struct{}{}
	return func() {
		s.mergeMu.Lock()
		defer s.mergeMu.Unlock()
		delete(s.mergeInFlight, key)
	}, true
}
