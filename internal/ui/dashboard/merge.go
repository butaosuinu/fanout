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
		// Every error path here is one where nothing merged: a fence refusal or a
		// pre-send failure never reached GitHub, and a clean refusal left the pull
		// request open. The reserved hold has nothing left to protect.
		s.releaseClaim(rr, req.Number)
		// The live fence runs inside the merge call, so its refusals arrive here
		// mixed with gh's. They are fanout's own — the pull request moved between
		// admission and the send — and reporting them as "GitHub refused" would
		// name the wrong cause and drop the code the client renders from.
		status, code := mergeCallStatus(err)
		apiError(w, status, code, mergeFailureTitle(status), redactGHDetail(err))
		return
	}
	s.settleClaim(rr, req.Number, res)
	writeMergeResponse(w, req, res, s.poller.requestGHRefresh())
}

// settleClaim decides what becomes of the hold reserved before the merge.
//
// Unknown and Queued both mean GitHub already changed something: Unknown may
// have merged, and a queued request has auto-merge armed. Losing only the
// response would otherwise let another tab or a reload send it again, so the
// hold is upgraded to say which and stays until a poll settles the pull
// request. A confirmed merge leaves nothing unresolved, so its hold goes.
func (s *Server) settleClaim(rr repoRef, number int, res prmerge.Result) {
	if res.Unknown || res.Queued {
		s.holdUnconfirmed(rr, number, res)
		return
	}
	s.releaseClaim(rr, number)
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

// takeClaim reads the recorded holds, refuses if this pull request already has
// one, and reserves a durable hold for the merge about to run. It writes its own
// refusal. Callers hold mergeMu.
//
// The two failures are both claim_unavailable and both stop the merge: a file
// that cannot be read may still describe an unresolved merge, and one that
// cannot be written leaves a guard that would not survive a restart.
func (s *Server) takeClaim(w http.ResponseWriter, key string, rr repoRef, number int) bool {
	var claimed bool
	if lockErr := s.withClaimLock(func() {
		claimed = s.takeClaimLocked(w, key, rr, number)
	}); lockErr != nil {
		apiError(w, http.StatusServiceUnavailable, "claim_unavailable",
			"the merge was not started: another dashboard holds the claim file", redactGHDetail(lockErr))
		return false
	}
	return claimed
}

func (s *Server) takeClaimLocked(w http.ResponseWriter, key string, rr repoRef, number int) bool {
	claims, err := s.readMergeClaims()
	if err != nil {
		apiError(w, http.StatusServiceUnavailable, "claim_unavailable",
			"the merge was not started: existing holds could not be read", redactGHDetail(err))
		return false
	}
	if s.unconfirmed(key, rr, claims, number) {
		apiError(w, http.StatusConflict, "merge_unconfirmed",
			"an earlier merge for this pull request has not been confirmed yet", "")
		return false
	}
	if err := s.reserveClaim(key, claims); err != nil {
		apiError(w, http.StatusServiceUnavailable, "claim_unavailable",
			"the merge was not started: its hold could not be recorded", redactGHDetail(err))
		return false
	}
	return true
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

// claimLockWait bounds how long one request waits for another process to finish
// its own read-decide-write on the claims file. The section is a read, a
// decision, and one atomic write, so a wait this long already means something is
// wrong rather than busy.
const claimLockWait = 2 * time.Second

// withClaimLock runs fn while holding the cross-process lock on the claims file.
//
// mergeMu only orders the goroutines inside one dashboard. Two dashboards can
// run against the same repository — the startup probe for an existing one can
// time out — and an atomic write does not make read-decide-write atomic, so
// without this both could read "no claim here" and both send the merge.
func (s *Server) withClaimLock(fn func()) error {
	release, err := atomicfs.Lock(s.mergeClaimsPath(), claimLockWait)
	if err != nil {
		return err
	}
	defer release()
	fn()
	return nil
}

func (s *Server) mergeClaimsPath() string {
	return filepath.Join(s.poller.projectRoot, ".fanout", mergeClaimsFile)
}

// readMergeClaims returns the recorded holds. Callers hold mergeMu.
//
// A missing file is no holds. An unreadable or corrupt one is not: treating it
// as empty would lose the holds a restart exists to honor, and the next reserve
// would overwrite the file that still described an unresolved merge. The caller
// refuses the mutation instead.
// mergeClaim is one held pull request. The kind decides what can release it:
// an unknown outcome only settles when GitHub shows the PR merged or closed,
// while a queued one is also over when the auto-merge that carried it is gone.
type mergeClaim struct {
	Kind string `json:"kind"`
	At   string `json:"at"`
	// Seen records that a poll has shown GitHub still holding this merge —
	// an auto-merge armed, or an entry in the merge queue. Only a true-to-false
	// transition is a cancellation: the snapshot taken before the merge says
	// false because the merge had not happened yet, and reading that as "canceled"
	// would drop the hold seconds after taking it, which is exactly when a second
	// click is likely.
	Seen bool `json:"seen,omitempty"`
}

const (
	// claimInflight is written before gh runs. A dashboard killed mid-merge
	// leaves it behind, which is the honest state: the merge may have reached
	// GitHub, and nothing local knows.
	claimInflight = "inflight"
	claimUnknown  = "unknown"
	claimQueued   = "queued"
)

func (s *Server) readMergeClaims() (map[string]mergeClaim, error) {
	claims := map[string]mergeClaim{}
	if _, err := atomicfs.ReadJSON(s.mergeClaimsPath(), &claims); err != nil {
		return nil, err
	}
	return claims, nil
}

// writeMergeClaims persists the holds. Callers hold mergeMu.
//
// The error matters at exactly one point — reserving a claim before the merge,
// where an unwritable file means the guard would not survive a restart and the
// merge is refused instead. Everywhere else the write is an update to a hold
// that already exists, and failing it leaves the stricter state in place.
func (s *Server) writeMergeClaims(claims map[string]mergeClaim) error {
	path := s.mergeClaimsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return atomicfs.WriteJSON(path, claims, 0o600)
}

// reserveClaim records the hold before gh is invoked, and reports whether it is
// durable.
//
// Writing it afterwards is too late: the answer that never arrives is exactly
// the one that would have told us to write. Taking the claim first means a
// crash, a kill, or a lost response all leave the same evidence behind — and a
// file that cannot be written is a guard that would not survive a restart, so
// the merge does not run at all rather than run unprotected.
func (s *Server) reserveClaim(key string, claims map[string]mergeClaim) error {
	claims[key] = mergeClaim{Kind: claimInflight, At: time.Now().UTC().Format(time.RFC3339)}
	if err := s.writeMergeClaims(claims); err != nil {
		return err
	}
	s.mergeHeld[key] = time.Now()
	return nil
}

// releaseClaim drops a hold whose outcome is known: the merge landed, or it
// never left. A write failure keeps the recorded hold, which refuses later
// attempts — the safe direction, and the documented manual way out is to delete
// the entry.
func (s *Server) releaseClaim(rr repoRef, number int) {
	key := claimKey(rr, number)
	s.mergeMu.Lock()
	defer s.mergeMu.Unlock()
	delete(s.mergeHeld, key)
	// A lock failure leaves the recorded hold in place, which refuses later
	// attempts — the safe direction for a merge that is already done.
	_ = s.withClaimLock(func() {
		claims, err := s.readMergeClaims()
		if err != nil {
			// Rewriting a file that cannot be read would discard whatever it
			// still says.
			return
		}
		delete(claims, key)
		_ = s.writeMergeClaims(claims)
	})
}

// holdUnconfirmed records a pull request whose merge outcome is unreadable, so
// every entrypoint refuses a second attempt until GitHub settles it.
func (s *Server) holdUnconfirmed(rr repoRef, number int, res prmerge.Result) {
	key := claimKey(rr, number)
	kind := claimUnknown
	if !res.Unknown {
		kind = claimQueued
	}
	s.mergeMu.Lock()
	defer s.mergeMu.Unlock()
	// The in-process hold is unconditional. A disk that cannot be written (full,
	// read-only) costs the cross-restart half of the guard, but it must not also
	// hand this process a way to fire the same merge again.
	s.mergeHeld[key] = time.Now()
	_ = s.withClaimLock(func() {
		claims, err := s.readMergeClaims()
		if err != nil {
			// The in-memory hold above already blocks this process; overwriting
			// an unreadable file would drop the other holds it may still carry.
			return
		}
		s.upgradeClaim(claims, key, kind)
	})
}

func (s *Server) upgradeClaim(claims map[string]mergeClaim, key, kind string) {
	claims[key] = mergeClaim{Kind: kind, At: time.Now().UTC().Format(time.RFC3339)}
	_ = s.writeMergeClaims(claims)
}

// unconfirmed reports whether an earlier unreadable merge still blocks this pull
// request, clearing the hold once a poll shows the PR settled or the TTL passes.
// Callers hold mergeMu.
func (s *Server) unconfirmed(key string, rr repoRef, claims map[string]mergeClaim, number int) bool {
	if !s.held(key, claims) {
		return false
	}
	// Only GitHub releases the hold. Time is not evidence about an outcome, so
	// there is no TTL: a merge that may have happened stays un-repeatable until a
	// poll shows the pull request settled. The way out of a state that never
	// resolves is to delete the entry from .fanout/merge-claims.json, which is
	// the documented manual path.
	if !s.claimOver(key, rr, claims, number) {
		return true
	}
	delete(s.mergeHeld, key)
	delete(claims, key)
	_ = s.writeMergeClaims(claims)
	return false
}

// claimOver reports whether GitHub has answered the question this hold was
// waiting on.
//
// Merged or closed ends any hold. A queued merge has a second ending: what `gh
// pr merge` produced on a queue-required base — an armed auto-merge, or an entry
// in the merge queue — can be taken away again (`--disable-auto`, or removal
// from the queue), leaving the pull request open with nothing pending. Without
// this the hold outlives the thing it was protecting and the row can never be
// merged again.
//
// The sighting is what makes the absence mean something. Before the poller has
// seen GitHub holding the merge, "nothing pending" is just the snapshot that
// predates the click.
func (s *Server) claimOver(key string, rr repoRef, claims map[string]mergeClaim, number int) bool {
	repo := rr.owner + "/" + rr.repo
	if s.poller.prSettled(repo, number) {
		return true
	}
	claim := claims[key]
	if claim.Kind != claimQueued {
		return false
	}
	pending, found := s.poller.prMergePending(repo, number)
	if !found {
		return false
	}
	if pending {
		if !claim.Seen {
			claim.Seen = true
			claims[key] = claim
			_ = s.writeMergeClaims(claims)
		}
		return false
	}
	return claim.Seen
}

// held reports whether a hold is recorded, in memory or on disk (a restart has
// only the file). Callers hold mergeMu.
func (s *Server) held(key string, claims map[string]mergeClaim) bool {
	if _, ok := s.mergeHeld[key]; ok {
		return true
	}
	_, ok := claims[key]
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
	ref, err := prmerge.SelectRef(pv, rr.owner+"/"+rr.repo, body.prNumber)
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

// mergeCallStatus classifies everything Service.Merge can return: fanout's own
// live-fence refusals first (409 with their own code), then gh's.
func mergeCallStatus(err error) (int, string) {
	for _, c := range mergePreflightSentinels {
		if errors.Is(err, c.sentinel) {
			return http.StatusConflict, c.code
		}
	}
	return mergeFailureStatus(err)
}

func mergeFailureTitle(status int) string {
	if status == http.StatusConflict {
		return "the merge was refused"
	}
	return "GitHub refused the merge"
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
	if !s.takeClaim(w, key, rr, number) {
		return nil, false
	}
	s.mergeInFlight[key] = struct{}{}
	return func() {
		s.mergeMu.Lock()
		defer s.mergeMu.Unlock()
		delete(s.mergeInFlight, key)
	}, true
}
