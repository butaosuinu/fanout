package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/app/prmerge"
	"github.com/butaosuinu/fanout/internal/app/sessionview"
	"github.com/butaosuinu/fanout/internal/infra/atomicfs"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

const (
	testHostPort = "127.0.0.1:8081"
	// Every merge test carries a token: the endpoint refuses outright when the
	// token gate is off, which TestMergeRefusesWithoutATokenGate pins separately.
	testToken   = "secret"
	testHeadSha = "0123456789abcdef0123456789abcdef01234567"
)

type fakeMerger struct {
	mu      sync.Mutex
	calls   int
	last    prmerge.Request
	res     prmerge.Result
	err     error
	release chan struct{} // when non-nil, merge blocks until it is closed
	// entered is closed as the first call arrives. Waiting on it is how a test
	// knows a merge is in flight; polling the counter instead only ever
	// approximates that, and on a loaded runner the approximation is wrong.
	entered chan struct{}
}

func (f *fakeMerger) merge(_ context.Context, req prmerge.Request) (prmerge.Result, error) {
	f.mu.Lock()
	f.calls++
	f.last = req
	if f.entered != nil && f.calls == 1 {
		close(f.entered)
	}
	release, res, err := f.release, f.res, f.err
	f.mu.Unlock()
	if release != nil {
		<-release
	}
	return res, err
}

func (f *fakeMerger) snapshot() (int, prmerge.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.last
}

func mergeSnapshot(prs ...ghissue.PRRef) sessionview.Snapshot {
	if prs == nil {
		prs = []ghissue.PRRef{openPR()}
	}
	return sessionview.Snapshot{Sessions: []sessionview.Session{{
		Parent: "575",
		Panes: []sessionview.PaneView{{
			IssueNum:     578,
			Slug:         "dashboard-merge",
			DisplayName:  "dashboard merge",
			BranchName:   "fanout/dashboard-merge",
			BaseBranch:   "main",
			WorktreePath: "/tmp/does-not-need-to-exist",
			PRs:          prs,
		}},
	}}}
}

func openPR() ghissue.PRRef {
	return ghissue.PRRef{
		Number:    701,
		State:     "OPEN",
		Mergeable: "MERGEABLE",
		HeadSha:   testHeadSha,
		HeadRef:   "fanout/dashboard-merge",
		HeadRepo:  "owner/repo",
		BaseRepo:  "owner/repo",
		BaseRef:   "main",
	}
}

// reviewPR is openPR with one review/CI signal overridden.
func reviewPR(over ghissue.PRRef) ghissue.PRRef {
	pr := openPR()
	pr.ReviewDecision, pr.CIStatus = over.ReviewDecision, over.CIStatus
	return pr
}

func mergeHandler(t *testing.T, token string, snap sessionview.Snapshot, m *fakeMerger) http.Handler {
	t.Helper()
	p := newPollerBase(t.TempDir(), newHub())
	p.latest, p.repo, p.resolved = snap, "owner/repo", true
	s := &Server{poller: p, token: token, hostPort: testHostPort, mergeInFlight: map[string]struct{}{}, mergeHeld: map[string]time.Time{}}
	if m != nil {
		s.mergePR = m.merge
	}
	h, err := s.handler()
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	return h
}

func mergeQuery(token string) url.Values {
	return url.Values{"parent": {"575"}, "issue": {"578"}, "token": {token}}
}

func mergeBodyJSON() string {
	return fmt.Sprintf(
		`{"prNumber":701,"headSha":%q,"baseRef":"main","method":"squash"}`,
		testHeadSha)
}

// requestMerge sends a well-formed same-origin POST unless an option changes it.
func requestMerge(
	t *testing.T,
	h http.Handler,
	method string,
	query url.Values,
	body string,
	opts ...func(*http.Request),
) *httptest.ResponseRecorder {
	t.Helper()
	target := mergePath
	if encoded := query.Encode(); encoded != "" {
		target += "?" + encoded
	}
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Host = testHostPort
	req.Header.Set("Content-Type", "application/json")
	for _, opt := range opts {
		opt(req)
	}
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, req)
	return recorder
}

func decodeAPIError(t *testing.T, recorder *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", recorder.Body.String(), err)
	}
	return body
}

func assertAPIError(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d (body %s)", recorder.Code, status, recorder.Body.String())
	}
	if got := decodeAPIError(t, recorder)["code"]; got != code {
		t.Fatalf("error code = %q, want %q", got, code)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestMergeIsPostOnly(t *testing.T) {
	fake := &fakeMerger{}
	h := mergeHandler(t, testToken, mergeSnapshot(), fake)
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			response := requestMerge(t, h, method, mergeQuery(testToken), mergeBodyJSON())
			if response.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s status = %d, want 405", method, response.Code)
			}
			if got := response.Header().Get("Allow"); got != "POST" {
				t.Fatalf("Allow = %q, want POST", got)
			}
			if got := response.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", got)
			}
		})
	}
	if calls, _ := fake.snapshot(); calls != 0 {
		t.Fatalf("merge calls = %d, want 0", calls)
	}
}

// TestReadOnlyRoutesStayGetOnly guards the half of the carve-out that is easy to
// lose: adding one POST route must not turn the reading endpoints into a
// method-agnostic surface.
func TestReadOnlyRoutesStayGetOnly(t *testing.T) {
	h := mergeHandler(t, testToken, mergeSnapshot(), &fakeMerger{})
	for _, path := range []string{"/api/diff", "/api/snapshot", "/api/peek", "/api/plan", "/healthz", "/"} {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
			req.Host = testHostPort
			req.Header.Set("Content-Type", "application/json")
			h.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusMethodNotAllowed {
				t.Fatalf("POST %s status = %d, want 405", path, recorder.Code)
			}
			if got := recorder.Header().Get("Allow"); got != "GET, HEAD" {
				t.Fatalf("POST %s Allow = %q, want \"GET, HEAD\"", path, got)
			}
		})
	}
}

func TestMergeSameOriginGate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*http.Request)
		status int
		code   string
	}{
		{
			name:   "a rebound host is refused",
			mutate: func(r *http.Request) { r.Host = "evil.test:8081" },
			status: http.StatusForbidden, code: "host",
		},
		{
			name:   "a foreign Origin is refused",
			mutate: func(r *http.Request) { r.Header.Set("Origin", "http://evil.test") },
			status: http.StatusForbidden, code: "origin",
		},
		{
			name:   "a cross-site fetch is refused",
			mutate: func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
			status: http.StatusForbidden, code: "site",
		},
		{
			name:   "the page's own Origin passes",
			mutate: func(r *http.Request) { r.Header.Set("Origin", "http://"+testHostPort) },
			status: http.StatusOK,
		},
		{
			name:   "a same-origin fetch passes",
			mutate: func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-origin") },
			status: http.StatusOK,
		},
		{
			// Non-browser clients send neither header; the token still gates them.
			name:   "both headers absent passes",
			mutate: func(*http.Request) {},
			status: http.StatusOK,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := mergeHandler(t, testToken, mergeSnapshot(), &fakeMerger{})
			response := requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON(), tt.mutate)
			if tt.code == "" {
				if response.Code != tt.status {
					t.Fatalf("status = %d, want %d (body %s)", response.Code, tt.status, response.Body.String())
				}
				return
			}
			assertAPIError(t, response, tt.status, tt.code)
		})
	}
}

// TestMergeFailsClosedWithoutABoundHost pins the fail-closed branch: a Server
// assembled without New has no host to pin against, and the mutation route must
// not be the one path that silently loses its DNS-rebinding guard.
func TestMergeFailsClosedWithoutABoundHost(t *testing.T) {
	p := newPollerBase(t.TempDir(), newHub())
	p.latest, p.repo, p.resolved = mergeSnapshot(), "owner/repo", true
	fake := &fakeMerger{}
	s := &Server{poller: p, token: testToken, mergePR: fake.merge, mergeInFlight: map[string]struct{}{}, mergeHeld: map[string]time.Time{}}
	h, err := s.handler()
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	assertAPIError(t, requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON()),
		http.StatusForbidden, "host")
	if calls, _ := fake.snapshot(); calls != 0 {
		t.Fatalf("merge calls = %d, want 0", calls)
	}
}

func TestMergeMediaTypeAndSize(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		mutate func(*http.Request)
		status int
		code   string
	}{
		{
			name: "a form post cannot set application/json", body: mergeBodyJSON(),
			mutate: func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") },
			status: http.StatusUnsupportedMediaType, code: "unsupported_media_type",
		},
		{
			name: "a charset parameter is tolerated", body: mergeBodyJSON(),
			mutate: func(r *http.Request) { r.Header.Set("Content-Type", "application/json; charset=utf-8") },
			status: http.StatusOK,
		},
		{
			name:   "an oversized body is refused before decoding",
			body:   `{"prNumber":701,"headSha":"` + strings.Repeat("a", mergeBodyMaxBytes) + `","method":"squash"}`,
			status: http.StatusRequestEntityTooLarge, code: "body_too_large",
		},
		{
			name:   "an unknown field is refused",
			body:   `{"prNumber":701,"method":"squash","admin":true}`,
			status: http.StatusBadRequest, code: "invalid_body",
		},
		{
			name:   "a second JSON document cannot ride along",
			body:   mergeBodyJSON() + `{"prNumber":999,"method":"squash"}`,
			status: http.StatusBadRequest, code: "invalid_body",
		},
		{
			name:   "an unknown merge method is refused",
			body:   `{"prNumber":701,"method":"fast-forward"}`,
			status: http.StatusBadRequest, code: "invalid_body",
		},
		{
			name:   "a missing merge method does not default to squash",
			body:   `{"prNumber":701}`,
			status: http.StatusBadRequest, code: "invalid_body",
		},
		{
			name:   "a non-positive pr number is refused",
			body:   `{"prNumber":0,"baseRef":"main","method":"squash"}`,
			status: http.StatusBadRequest, code: "invalid_body",
		},
		{
			// Omitting it would otherwise switch the retarget fence off.
			name:   "a missing baseRef is refused",
			body:   fmt.Sprintf(`{"prNumber":701,"headSha":%q,"method":"squash"}`, testHeadSha),
			status: http.StatusBadRequest, code: "invalid_body",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := mergeHandler(t, testToken, mergeSnapshot(), &fakeMerger{})
			opts := []func(*http.Request){}
			if tt.mutate != nil {
				opts = append(opts, tt.mutate)
			}
			response := requestMerge(t, h, http.MethodPost, mergeQuery(testToken), tt.body, opts...)
			if tt.code == "" {
				if response.Code != tt.status {
					t.Fatalf("status = %d, want %d (body %s)", response.Code, tt.status, response.Body.String())
				}
				return
			}
			assertAPIError(t, response, tt.status, tt.code)
		})
	}
}

func TestMergeTokenGate(t *testing.T) {
	for _, token := range []string{"", "wrong"} {
		t.Run("token "+token, func(t *testing.T) {
			fake := &fakeMerger{}
			h := mergeHandler(t, testToken, mergeSnapshot(), fake)
			response := requestMerge(t, h, http.MethodPost, mergeQuery(token), mergeBodyJSON())
			if response.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", response.Code)
			}
			if got := response.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", got)
			}
			if calls, _ := fake.snapshot(); calls != 0 {
				t.Fatalf("merge calls = %d, want 0", calls)
			}
		})
	}
}

func TestMergePassesRequestThrough(t *testing.T) {
	fake := &fakeMerger{res: prmerge.Result{Merged: true}}
	h := mergeHandler(t, testToken, mergeSnapshot(), fake)
	body := fmt.Sprintf(`{"prNumber":701,"headSha":%q,"baseRef":"main","method":"rebase"}`, testHeadSha)
	response := requestMerge(t, h, http.MethodPost, mergeQuery(testToken), body)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", response.Code, response.Body.String())
	}
	calls, got := fake.snapshot()
	if calls != 1 {
		t.Fatalf("merge calls = %d, want 1", calls)
	}
	want := prmerge.Request{
		Owner: "owner", Repo: "repo", Number: 701,
		Method: ghissue.MergeRebase, HeadSha: testHeadSha, BaseRef: "main",
	}
	if got != want {
		t.Fatalf("merge request = %#v, want %#v", got, want)
	}
	var decoded mergeResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.Merged || decoded.Method != "rebase" || !decoded.RefreshQueued {
		t.Fatalf("response = %#v, want a merged rebase", decoded)
	}
}

func TestMergeRejectsMalformedIdentity(t *testing.T) {
	tests := []struct {
		name  string
		query url.Values
	}{
		{name: "no parent", query: url.Values{"issue": {"578"}}},
		{name: "neither issue nor task", query: url.Values{"parent": {"575"}}},
		{name: "both issue and task", query: url.Values{"parent": {"575"}, "issue": {"578"}, "task": {"t1"}}},
		{name: "repeated issue", query: url.Values{"parent": {"575"}, "issue": {"578", "579"}}},
		{name: "non-numeric issue", query: url.Values{"parent": {"575"}, "issue": {"578x"}}},
		{name: "positive issue with a source", query: url.Values{"parent": {"575"}, "issue": {"578"}, "source": {"k"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeMerger{}
			h := mergeHandler(t, testToken, mergeSnapshot(), fake)
			tt.query.Set("token", testToken)
			assertAPIError(t, requestMerge(t, h, http.MethodPost, tt.query, mergeBodyJSON()),
				http.StatusBadRequest, "invalid_identity")
			if calls, _ := fake.snapshot(); calls != 0 {
				t.Fatalf("merge calls = %d, want 0", calls)
			}
		})
	}
}

func TestMergeRowNotFound(t *testing.T) {
	twoMatches := mergeSnapshot()
	twoMatches.Sessions[0].Panes = append(twoMatches.Sessions[0].Panes, twoMatches.Sessions[0].Panes[0])

	tests := []struct {
		name string
		snap sessionview.Snapshot
	}{
		{name: "no matching row", snap: sessionview.Snapshot{}},
		{name: "two rows share the identity", snap: twoMatches},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeMerger{}
			h := mergeHandler(t, testToken, tt.snap, fake)
			assertAPIError(t, requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON()),
				http.StatusNotFound, "row_not_found")
			if calls, _ := fake.snapshot(); calls != 0 {
				t.Fatalf("merge calls = %d, want 0", calls)
			}
		})
	}
}

func TestMergePreflightRefusals(t *testing.T) {
	merged := openPR()
	merged.State = "MERGED"
	mergedAt := openPR()
	mergedAt.MergedAt = new(string)
	closed := openPR()
	closed.State = "CLOSED"
	draft := openPR()
	draft.IsDraft = true
	conflicting := openPR()
	conflicting.Mergeable = "CONFLICTING"
	tests := []struct {
		name string
		prs  []ghissue.PRRef
		body string
		code string
	}{
		{name: "the row does not carry this PR", prs: []ghissue.PRRef{openPR()}, body: `{"prNumber":999,"baseRef":"main","method":"squash"}`, code: "pr_not_on_row"},
		{name: "already merged by state", prs: []ghissue.PRRef{merged}, code: "already_merged"},
		{name: "already merged by mergedAt alone", prs: []ghissue.PRRef{mergedAt}, code: "already_merged"},
		{name: "closed", prs: []ghissue.PRRef{closed}, code: "pr_closed"},
		{name: "draft", prs: []ghissue.PRRef{draft}, code: "pr_draft"},
		{name: "conflicting", prs: []ghissue.PRRef{conflicting}, code: "pr_conflicting"},
		{
			name: "the head moved since the page rendered", prs: []ghissue.PRRef{openPR()},
			body: `{"prNumber":701,"headSha":"stale","baseRef":"main","method":"squash"}`, code: "stale_head",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := tt.body
			if body == "" {
				body = mergeBodyJSON()
			}
			fake := &fakeMerger{}
			h := mergeHandler(t, testToken, mergeSnapshot(tt.prs...), fake)
			assertAPIError(t, requestMerge(t, h, http.MethodPost, mergeQuery(testToken), body),
				http.StatusConflict, tt.code)
			if calls, _ := fake.snapshot(); calls != 0 {
				t.Fatalf("merge calls = %d, want 0", calls)
			}
		})
	}
}

// TestMergeRejectsRowKindsTheUINeverOffers keeps the server's addressable set
// aligned with what the client can draw a button for. rowQuery returns null for
// both kinds, and handleDiff refuses them explicitly.
func TestMergeRejectsRowKindsTheUINeverOffers(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*sessionview.PaneView)
	}{
		{name: "not-started synthetic row", mutate: func(pv *sessionview.PaneView) { pv.NotStarted = true }},
		{name: "shell row", mutate: func(pv *sessionview.PaneView) { pv.Kind = state.PaneKindShell }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := mergeSnapshot()
			tt.mutate(&snap.Sessions[0].Panes[0])
			fake := &fakeMerger{}
			h := mergeHandler(t, testToken, snap, fake)
			assertAPIError(t, requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON()),
				http.StatusNotFound, "row_not_found")
			if calls, _ := fake.snapshot(); calls != 0 {
				t.Fatalf("merge calls = %d, want 0", calls)
			}
		})
	}
}

// TestMergeRefusesAPRBasedElsewhere pins the cross-repository fence. A pull
// request can close an issue in another repository, so a row's PR list is not
// proof the PR lives here — and merging by number would resolve it against the
// wrong repository.
func TestMergeRefusesAPRBasedElsewhere(t *testing.T) {
	foreign := openPR()
	foreign.BaseRepo = "other/repo"
	fake := &fakeMerger{}
	h := mergeHandler(t, testToken, mergeSnapshot(foreign), fake)
	assertAPIError(t, requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON()),
		http.StatusConflict, "pr_not_on_row")
	if calls, _ := fake.snapshot(); calls != 0 {
		t.Fatalf("merge calls = %d, want 0", calls)
	}
}

// TestMergeReportsAnUnconfirmableOutcome pins that a merge whose result could
// not be read comes back as unknown rather than as a failure — the client must
// not offer a retry that would merge again against a state nobody has seen.
func TestMergeReportsAnUnconfirmableOutcome(t *testing.T) {
	fake := &fakeMerger{res: prmerge.Result{Unknown: true}}
	h := mergeHandler(t, testToken, mergeSnapshot(), fake)
	response := requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON())
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	var decoded mergeResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.Unknown || decoded.Merged {
		t.Fatalf("response = %#v, want an unknown outcome", decoded)
	}
}

// TestMergeHoldsAnUnconfirmedPullRequest pins the server-side half of "never
// repeat an ambiguous mutation": a second tab, a reload, or a canceled fetch
// would otherwise get past preflight on a stale OPEN snapshot and fire a second
// `gh pr merge`.
func TestMergeHoldsAnUnconfirmedPullRequest(t *testing.T) {
	fake := &fakeMerger{res: prmerge.Result{Unknown: true}}
	h := mergeHandler(t, testToken, mergeSnapshot(), fake)

	first := requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON())
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200", first.Code)
	}
	assertAPIError(t, requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON()),
		http.StatusConflict, "merge_unconfirmed")
	if calls, _ := fake.snapshot(); calls != 1 {
		t.Fatalf("merge calls = %d, want 1", calls)
	}
}

// TestMergeHoldSurvivesARestart pins the reason the claim is on disk. A lost
// response plus a restarted dashboard would otherwise forget that a merge may
// already have reached GitHub, and a stale OPEN snapshot would let it fire again.
func TestMergeHoldSurvivesARestart(t *testing.T) {
	root := t.TempDir()
	newServer := func(m *fakeMerger) http.Handler {
		p := newPollerBase(root, newHub())
		p.latest, p.repo, p.resolved = mergeSnapshot(), "owner/repo", true
		s := &Server{
			poller: p, hostPort: testHostPort, token: testToken,
			mergePR: m.merge, mergeInFlight: map[string]struct{}{},
			mergeHeld: map[string]time.Time{},
		}
		h, err := s.handler()
		if err != nil {
			t.Fatalf("handler: %v", err)
		}
		return h
	}

	first := &fakeMerger{res: prmerge.Result{Unknown: true}}
	if code := requestMerge(t, newServer(first), http.MethodPost, mergeQuery(testToken), mergeBodyJSON()).Code; code != http.StatusOK {
		t.Fatalf("first status = %d, want 200", code)
	}

	restarted := &fakeMerger{res: prmerge.Result{Merged: true}}
	assertAPIError(t, requestMerge(t, newServer(restarted), http.MethodPost, mergeQuery(testToken), mergeBodyJSON()),
		http.StatusConflict, "merge_unconfirmed")
	if calls, _ := restarted.snapshot(); calls != 0 {
		t.Fatalf("merge calls after restart = %d, want 0", calls)
	}
}

// TestMergeReleasesTheHoldOnceGitHubSettles pins the only way out: GitHub. Time
// is not evidence about an outcome, so there is no TTL — a poll showing the pull
// request resolved is what clears the hold (the documented manual path is to
// delete the entry from .fanout/merge-claims.json).
func TestMergeReleasesTheHoldOnceGitHubSettles(t *testing.T) {
	fake := &fakeMerger{res: prmerge.Result{Unknown: true}}
	p := newPollerBase(t.TempDir(), newHub())
	p.latest, p.repo, p.resolved = mergeSnapshot(), "owner/repo", true
	s := &Server{
		poller: p, hostPort: testHostPort, token: testToken,
		mergePR: fake.merge, mergeInFlight: map[string]struct{}{},
		mergeHeld: map[string]time.Time{},
	}
	h, err := s.handler()
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if code := requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON()).Code; code != http.StatusOK {
		t.Fatalf("first status = %d, want 200", code)
	}

	merged := openPR()
	merged.State = "MERGED"
	p.mu.Lock()
	p.latest = mergeSnapshot(merged)
	p.mu.Unlock()

	// The hold is gone; preflight now refuses for the honest reason instead.
	assertAPIError(t, requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON()),
		http.StatusConflict, "already_merged")
}

// TestMergeHoldIgnoresASameNumberedPRElsewhere pins the claim to a repository.
// `Fixes owner/repo#N` puts other repositories' pull requests on a row, and
// numbers repeat across repositories — so a merged #7 somewhere else must not
// release the hold on this repository's #7 and let the merge be sent again.
func TestMergeHoldIgnoresASameNumberedPRElsewhere(t *testing.T) {
	fake := &fakeMerger{res: prmerge.Result{Unknown: true}}
	p := newPollerBase(t.TempDir(), newHub())
	p.latest, p.repo, p.resolved = mergeSnapshot(), "owner/repo", true
	s := &Server{
		poller: p, hostPort: testHostPort, token: testToken,
		mergePR: fake.merge, mergeInFlight: map[string]struct{}{},
		mergeHeld: map[string]time.Time{},
	}
	h, err := s.handler()
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if code := requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON()).Code; code != http.StatusOK {
		t.Fatalf("first status = %d, want 200", code)
	}

	// The addressed row keeps its own open PR; a neighboring row carries a
	// merged pull request that only shares the number.
	elsewhere := openPR()
	elsewhere.State, elsewhere.BaseRepo = "MERGED", "other/repo"
	withNeighbour := mergeSnapshot()
	session := &withNeighbour.Sessions[0]
	neighbor := session.Panes[0]
	neighbor.IssueNum, neighbor.Slug, neighbor.BranchName = 579, "other", "fanout/other"
	neighbor.PRs = []ghissue.PRRef{elsewhere}
	session.Panes = append(session.Panes, neighbor)
	p.mu.Lock()
	p.latest = withNeighbour
	p.mu.Unlock()

	assertAPIError(t, requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON()),
		http.StatusConflict, "merge_unconfirmed")
	if calls, _ := fake.snapshot(); calls != 1 {
		t.Fatalf("merge calls = %d, want 1 (the hold must still block)", calls)
	}
}

// TestQueuedHoldEndsWhenTheAutoMergeIsCancelled pins the second ending of a
// queued hold. `gh pr merge` arms an auto-merge on a queue-required base, and
// `gh pr merge --disable-auto` cancels it without closing the pull request — a
// state "is it merged yet" can never satisfy, so the row would stay unmergeable
// forever.
func TestQueuedHoldEndsWhenTheAutoMergeIsCancelled(t *testing.T) {
	armed := openPR()
	armed.AutoMerge = true
	fake := &fakeMerger{res: prmerge.Result{Queued: true}}
	p := newPollerBase(t.TempDir(), newHub())
	p.latest, p.repo, p.resolved = mergeSnapshot(armed), "owner/repo", true
	s := &Server{
		poller: p, hostPort: testHostPort, token: testToken,
		mergePR: fake.merge, mergeInFlight: map[string]struct{}{},
		mergeHeld: map[string]time.Time{},
	}
	h, err := s.handler()
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if code := requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON()).Code; code != http.StatusOK {
		t.Fatalf("first status = %d, want 200", code)
	}

	// Still armed: the enqueue is live, so the hold stands.
	assertAPIError(t, requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON()),
		http.StatusConflict, "merge_unconfirmed")

	p.mu.Lock()
	p.latest = mergeSnapshot(openPR()) // auto-merge canceled, PR still open
	p.mu.Unlock()

	// The hold is gone; the merge is sent again because nothing is pending.
	if code := requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON()).Code; code != http.StatusOK {
		t.Fatalf("status after cancellation = %d, want 200", code)
	}
	if calls, _ := fake.snapshot(); calls != 2 {
		t.Fatalf("merge calls = %d, want 2", calls)
	}
}

// TestQueuedHoldEndsWhenTheQueueEntryGoes covers the other shape of a queued
// merge. gh enqueues directly when the checks are already finished, so there is
// no auto-merge to watch — the entry in the merge queue is what GitHub is
// holding, and its removal is the same cancellation.
func TestQueuedHoldEndsWhenTheQueueEntryGoes(t *testing.T) {
	enqueued := openPR()
	enqueued.Queued = true
	fake := &fakeMerger{res: prmerge.Result{Queued: true}}
	p := newPollerBase(t.TempDir(), newHub())
	p.latest, p.repo, p.resolved = mergeSnapshot(enqueued), "owner/repo", true
	s := &Server{
		poller: p, hostPort: testHostPort, token: testToken,
		mergePR: fake.merge, mergeInFlight: map[string]struct{}{},
		mergeHeld: map[string]time.Time{},
	}
	h, err := s.handler()
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if code := requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON()).Code; code != http.StatusOK {
		t.Fatalf("first status = %d, want 200", code)
	}
	assertAPIError(t, requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON()),
		http.StatusConflict, "merge_unconfirmed")

	p.mu.Lock()
	p.latest = mergeSnapshot(openPR()) // dequeued, still open
	p.mu.Unlock()

	if code := requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON()).Code; code != http.StatusOK {
		t.Fatalf("status after dequeue = %d, want 200", code)
	}
}

// TestQueuedHoldSurvivesTheSnapshotThatPredatesIt pins the ordering the release
// depends on. `gh pr merge` is what arms the auto-merge, so the snapshot taken
// before the click still says there is none — reading that as a cancellation
// would drop the hold seconds after taking it, which is exactly when an
// impatient second click lands.
func TestQueuedHoldSurvivesTheSnapshotThatPredatesIt(t *testing.T) {
	fake := &fakeMerger{res: prmerge.Result{Queued: true}}
	p := newPollerBase(t.TempDir(), newHub())
	p.latest, p.repo, p.resolved = mergeSnapshot(), "owner/repo", true // not armed yet
	s := &Server{
		poller: p, hostPort: testHostPort, token: testToken,
		mergePR: fake.merge, mergeInFlight: map[string]struct{}{},
		mergeHeld: map[string]time.Time{},
	}
	h, err := s.handler()
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if code := requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON()).Code; code != http.StatusOK {
		t.Fatalf("first status = %d, want 200", code)
	}

	assertAPIError(t, requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON()),
		http.StatusConflict, "merge_unconfirmed")
	if calls, _ := fake.snapshot(); calls != 1 {
		t.Fatalf("merge calls = %d, want 1", calls)
	}
}

// TestUnknownHoldIgnoresTheAutoMergeSignal keeps the cancellation escape on the
// queued kind only. An unreadable outcome may already be a merge, so nothing
// short of GitHub showing the pull request settled may release it.
func TestUnknownHoldIgnoresTheAutoMergeSignal(t *testing.T) {
	fake := &fakeMerger{res: prmerge.Result{Unknown: true}}
	p := newPollerBase(t.TempDir(), newHub())
	p.latest, p.repo, p.resolved = mergeSnapshot(), "owner/repo", true
	s := &Server{
		poller: p, hostPort: testHostPort, token: testToken,
		mergePR: fake.merge, mergeInFlight: map[string]struct{}{},
		mergeHeld: map[string]time.Time{},
	}
	h, err := s.handler()
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if code := requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON()).Code; code != http.StatusOK {
		t.Fatalf("first status = %d, want 200", code)
	}
	assertAPIError(t, requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON()),
		http.StatusConflict, "merge_unconfirmed")
}

// TestMergeReportsAQueuedMergeAsNotMerged pins the merge-queue answer reaching
// the wire: `gh pr merge` exits 0 after enqueueing, and calling that "merged"
// would both lie to the user and hand the head ref to a delete.
func TestMergeReportsAQueuedMergeAsNotMerged(t *testing.T) {
	fake := &fakeMerger{res: prmerge.Result{Queued: true}}
	h := mergeHandler(t, testToken, mergeSnapshot(), fake)
	response := requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON())
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	var decoded mergeResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Merged || !decoded.Queued {
		t.Fatalf("response = %#v, want queued and not merged", decoded)
	}
}

// TestMergeRefusesARetargetedPullRequest pins the base fence. GitHub lets a PR
// be retargeted without touching its head, so number + SHA + snapshot +
// --match-head-commit all agree while the merge lands on a branch nobody
// reviewed it against.
func TestMergeRefusesARetargetedPullRequest(t *testing.T) {
	retargeted := openPR()
	retargeted.BaseRef = "release"
	fake := &fakeMerger{}
	h := mergeHandler(t, testToken, mergeSnapshot(retargeted), fake)
	assertAPIError(t, requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON()),
		http.StatusConflict, "stale_base")
	if calls, _ := fake.snapshot(); calls != 0 {
		t.Fatalf("merge calls = %d, want 0", calls)
	}
}

// TestMergeAcceptsTheDefaultHTTPPort pins the authority comparison. Browsers drop
// :80 from the URL, so `--port 80` would send Host: 127.0.0.1 and fail every POST
// while the page itself loaded fine.
func TestMergeAcceptsTheDefaultHTTPPort(t *testing.T) {
	p := newPollerBase(t.TempDir(), newHub())
	p.latest, p.repo, p.resolved = mergeSnapshot(), "owner/repo", true
	fake := &fakeMerger{res: prmerge.Result{Merged: true}}
	s := &Server{
		poller: p, hostPort: "127.0.0.1:80", token: testToken,
		mergePR: fake.merge, mergeInFlight: map[string]struct{}{},
		mergeHeld: map[string]time.Time{},
	}
	h, err := s.handler()
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	response := requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON(),
		func(r *http.Request) {
			r.Host = "127.0.0.1"
			r.Header.Set("Origin", "http://127.0.0.1")
		})
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", response.Code, response.Body.String())
	}
}

// TestMergeAcceptsUnknownMergeability pins the asymmetry in PRRef.Mergeable: ""
// means "GitHub has not told us", which every merged PR and every post-push
// recompute window reports. Refusing it would block ordinary merges.
func TestMergeAcceptsUnknownMergeability(t *testing.T) {
	unknown := openPR()
	unknown.Mergeable = ""
	fake := &fakeMerger{res: prmerge.Result{Merged: true}}
	h := mergeHandler(t, testToken, mergeSnapshot(unknown), fake)
	response := requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON())
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", response.Code, response.Body.String())
	}
}

// TestMergeLeavesReviewAndCIToGitHub pins that fanout does not reimplement
// branch protection: a repository that requires neither review nor green CI must
// stay mergeable from the dashboard, and one that requires them gets its refusal
// from GitHub as a 422.
func TestMergeLeavesReviewAndCIToGitHub(t *testing.T) {
	tests := []struct {
		name string
		pr   ghissue.PRRef
	}{
		{name: "review required", pr: reviewPR(ghissue.PRRef{ReviewDecision: "REVIEW_REQUIRED"})},
		{name: "changes requested", pr: reviewPR(ghissue.PRRef{ReviewDecision: "CHANGES_REQUESTED"})},
		{name: "failing CI", pr: reviewPR(ghissue.PRRef{CIStatus: "fail"})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeMerger{res: prmerge.Result{Merged: true}}
			h := mergeHandler(t, testToken, mergeSnapshot(tt.pr), fake)
			response := requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON())
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", response.Code, response.Body.String())
			}
		})
	}
}

func TestMergeUnavailableWhenNotWired(t *testing.T) {
	h := mergeHandler(t, testToken, mergeSnapshot(), nil)
	assertAPIError(t, requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON()),
		http.StatusServiceUnavailable, "merge_unavailable")
}

func TestMergeRefusesWhenGitHubIsUnresolved(t *testing.T) {
	tests := []struct {
		name  string
		repo  string
		ghErr error
	}{
		{name: "sticky gh failure", repo: "owner/repo", ghErr: errors.New("gh unavailable")},
		{name: "repo label never resolved", repo: ""},
		{name: "repo label is not owner/name", repo: "justaname"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newPollerBase(t.TempDir(), newHub())
			p.latest, p.repo, p.ghErr, p.resolved = mergeSnapshot(), tt.repo, tt.ghErr, true
			fake := &fakeMerger{}
			s := &Server{
				poller: p, hostPort: testHostPort, token: testToken,
				mergePR: fake.merge, mergeInFlight: map[string]struct{}{},
				mergeHeld: map[string]time.Time{},
			}
			h, err := s.handler()
			if err != nil {
				t.Fatalf("handler: %v", err)
			}
			assertAPIError(t, requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON()),
				http.StatusServiceUnavailable, "github_unresolved")
			if calls, _ := fake.snapshot(); calls != 0 {
				t.Fatalf("merge calls = %d, want 0", calls)
			}
		})
	}
}

func TestMergeErrorMapping(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "gh cooldown", err: ghissue.ErrRateLimited, status: http.StatusTooManyRequests, code: "rate_limited"},
		{name: "handler deadline", err: context.DeadlineExceeded, status: http.StatusGatewayTimeout, code: "timeout"},
		{
			name: "gh is not installed", err: errors.New(`exec: "gh": executable file not found in $PATH`),
			status: http.StatusBadGateway, code: "github_unavailable",
		},
		{
			name: "GitHub declined", err: errors.New("gh pr merge: Pull request is not mergeable"),
			status: http.StatusUnprocessableEntity, code: "github_rejected",
		},
		// The live fence runs inside the merge call, so its refusals come back
		// through this same path. They are fanout's, not GitHub's, and the client
		// tells the two apart by the code.
		{
			name: "the head moved before the send", err: prmerge.ErrStaleHead,
			status: http.StatusConflict, code: "stale_head",
		},
		{
			name: "the base moved before the send", err: prmerge.ErrStaleBase,
			status: http.StatusConflict, code: "stale_base",
		},
		{
			name: "someone else merged it first", err: prmerge.ErrAlreadyMerged,
			status: http.StatusConflict, code: "already_merged",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := mergeHandler(t, testToken, mergeSnapshot(), &fakeMerger{err: tt.err})
			assertAPIError(t, requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON()),
				tt.status, tt.code)
		})
	}
}

func TestMergeRedactsCredentialsFromDetail(t *testing.T) {
	leak := errors.New("gh pr merge: bad token ghp_0123456789abcdefghijklmnopqrstuvwxyz refused")
	h := mergeHandler(t, testToken, mergeSnapshot(), &fakeMerger{err: leak})
	response := requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON())
	detail := decodeAPIError(t, response)["detail"]
	if strings.Contains(detail, "ghp_") {
		t.Fatalf("detail = %q, want the credential redacted", detail)
	}
	if !strings.Contains(detail, "[redacted]") || !strings.Contains(detail, "refused") {
		t.Fatalf("detail = %q, want the redaction plus the remaining gh message", detail)
	}
}

func TestMergeInFlightLock(t *testing.T) {
	fake := &fakeMerger{
		res:     prmerge.Result{Merged: true},
		release: make(chan struct{}),
		entered: make(chan struct{}),
	}
	h := mergeHandler(t, testToken, mergeSnapshot(), fake)

	first := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		first <- requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON())
	}()
	select {
	case <-fake.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the first merge never reached the fake")
	}

	second := requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON())
	assertAPIError(t, second, http.StatusConflict, "merge_in_flight")

	close(fake.release)
	if got := (<-first).Code; got != http.StatusOK {
		t.Fatalf("first status = %d, want 200", got)
	}
	if calls, _ := fake.snapshot(); calls != 1 {
		t.Fatalf("merge calls = %d, want 1", calls)
	}
}

// TestMergeKicksThePollerWithoutDroppingTheRow pins both halves: the merge pulls
// the next GitHub tick forward, and it leaves the cached PR state alone. Dropping
// the entry would let the independent 2-second cheap ticker broadcast the row
// with no PRs at all, which the UI reads as "this row lost its pull request".
func TestMergeKicksThePollerWithoutDroppingTheRow(t *testing.T) {
	fake := &fakeMerger{res: prmerge.Result{Merged: true}}
	p := newPollerBase(t.TempDir(), newHub())
	p.latest, p.repo, p.resolved = mergeSnapshot(), "owner/repo", true
	p.cache[578] = ghCacheEntry{state: "OPEN"}
	s := &Server{
		poller: p, hostPort: testHostPort, token: testToken,
		mergePR: fake.merge, mergeInFlight: map[string]struct{}{},
		mergeHeld: map[string]time.Time{},
	}
	h, err := s.handler()
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	response := requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON())
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", response.Code, response.Body.String())
	}
	p.cacheMu.Lock()
	_, stillCached := p.cache[578]
	p.cacheMu.Unlock()
	if !stillCached {
		t.Fatal("the row's PR cache was dropped; the cheap ticker would broadcast a PR-less row")
	}
	select {
	case <-p.refreshNow:
	default:
		t.Fatal("no GitHub refresh was queued after the merge")
	}
}

// TestMergeRefusesWithoutATokenGate pins the --no-token contract. Reads stay
// open on a single-user laptop, but the loopback port is reachable by every
// local process, so an ungated merge endpoint would hand them the ability to
// merge pull requests.
func TestMergeRefusesWithoutATokenGate(t *testing.T) {
	fake := &fakeMerger{}
	h := mergeHandler(t, "", mergeSnapshot(), fake)
	assertAPIError(t, requestMerge(t, h, http.MethodPost, mergeQuery(""), mergeBodyJSON()),
		http.StatusForbidden, "token_required")
	if calls, _ := fake.snapshot(); calls != 0 {
		t.Fatalf("merge calls = %d, want 0", calls)
	}
}

// TestMergeRefusesWhenTheClaimCannotBePersisted pins the ordering the guard
// depends on. The hold is written before gh runs, because the answer that never
// arrives is exactly the one that would have told us to write it — and a hold
// that cannot be written would not survive the restart it exists for, so the
// merge does not run at all.
func TestMergeRefusesWhenTheClaimCannotBePersisted(t *testing.T) {
	root := t.TempDir()
	// .fanout as a file, so creating the directory for the claims fails.
	if err := os.WriteFile(filepath.Join(root, ".fanout"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &fakeMerger{res: prmerge.Result{Merged: true}}
	p := newPollerBase(root, newHub())
	p.latest, p.repo, p.resolved = mergeSnapshot(), "owner/repo", true
	s := &Server{
		poller: p, hostPort: testHostPort, token: testToken,
		mergePR: fake.merge, mergeInFlight: map[string]struct{}{},
		mergeHeld: map[string]time.Time{},
	}
	h, err := s.handler()
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	assertAPIError(t, requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON()),
		http.StatusServiceUnavailable, "claim_unavailable")
	if calls, _ := fake.snapshot(); calls != 0 {
		t.Fatalf("merge calls = %d, want 0", calls)
	}
}

// TestConfirmedMergeLeavesNoClaim keeps the reservation from becoming a leak: a
// merge GitHub confirmed has nothing unresolved about it, so a later merge of
// another pull request on the same row must not meet a stale hold.
func TestConfirmedMergeLeavesNoClaim(t *testing.T) {
	root := t.TempDir()
	fake := &fakeMerger{res: prmerge.Result{Merged: true}}
	p := newPollerBase(root, newHub())
	p.latest, p.repo, p.resolved = mergeSnapshot(), "owner/repo", true
	s := &Server{
		poller: p, hostPort: testHostPort, token: testToken,
		mergePR: fake.merge, mergeInFlight: map[string]struct{}{},
		mergeHeld: map[string]time.Time{},
	}
	h, err := s.handler()
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if code := requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON()).Code; code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	claims := map[string]any{}
	if _, err := atomicfs.ReadJSON(filepath.Join(root, ".fanout", mergeClaimsFile), &claims); err == nil {
		if len(claims) != 0 {
			t.Fatalf("claims = %v, want none after a confirmed merge", claims)
		}
	}
	if len(s.mergeHeld) != 0 {
		t.Fatalf("in-memory holds = %v, want none", s.mergeHeld)
	}
}

// TestMergeRefusesWhenClaimsCannotBeRead pins the difference between "no file"
// and "unreadable file". Reading a corrupt claims file as empty would lose an
// unresolved merge and let the next reservation overwrite the only record of
// it — which is precisely the resend the hold exists to stop.
func TestMergeRefusesWhenClaimsCannotBeRead(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".fanout"), 0o755); err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(root, ".fanout", mergeClaimsFile)
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &fakeMerger{res: prmerge.Result{Merged: true}}
	p := newPollerBase(root, newHub())
	p.latest, p.repo, p.resolved = mergeSnapshot(), "owner/repo", true
	s := &Server{
		poller: p, hostPort: testHostPort, token: testToken,
		mergePR: fake.merge, mergeInFlight: map[string]struct{}{},
		mergeHeld: map[string]time.Time{},
	}
	h, err := s.handler()
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	assertAPIError(t, requestMerge(t, h, http.MethodPost, mergeQuery(testToken), mergeBodyJSON()),
		http.StatusServiceUnavailable, "claim_unavailable")
	if calls, _ := fake.snapshot(); calls != 0 {
		t.Fatalf("merge calls = %d, want 0", calls)
	}
	// The unreadable record is left exactly as it was.
	data, err := os.ReadFile(corrupt)
	if err != nil || string(data) != "{not json" {
		t.Fatalf("claims file = %q (%v), want it untouched", data, err)
	}
}
