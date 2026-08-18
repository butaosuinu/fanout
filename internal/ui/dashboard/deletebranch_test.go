package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/app/prmerge"
	"github.com/butaosuinu/fanout/internal/app/sessionview"
)

type fakeDeleter struct {
	mu    sync.Mutex
	calls int
	last  prmerge.DeleteRequest
	err   error
}

func (f *fakeDeleter) delete(_ context.Context, req prmerge.DeleteRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.last = req
	return f.err
}

func (f *fakeDeleter) snapshot() (int, prmerge.DeleteRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.last
}

func deleteHandler(t *testing.T, snap sessionview.Snapshot, d *fakeDeleter) http.Handler {
	t.Helper()
	p := newPollerBase(t.TempDir(), newHub())
	p.latest, p.repo, p.resolved = snap, "owner/repo", true
	s := &Server{
		poller: p, token: testToken, hostPort: testHostPort,
		mergeInFlight: map[string]struct{}{}, mergeHeld: map[string]time.Time{},
		// The delete route shares mergeEnabled's wiring gate.
		mergePR: (&fakeMerger{}).merge,
	}
	if d != nil {
		s.deleteBranch = d.delete
	}
	h, err := s.handler()
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	return h
}

func deleteBody() string {
	return fmt.Sprintf(`{"prNumber":701,"headSha":%q}`, testHeadSha)
}

func requestDeleteBranch(
	t *testing.T,
	h http.Handler,
	method string,
	query url.Values,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	target := deleteBranchPath
	if encoded := query.Encode(); encoded != "" {
		target += "?" + encoded
	}
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Host = testHostPort
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, req)
	return recorder
}

func mergedSnapshot() sessionview.Snapshot {
	pr := openPR()
	pr.State = "MERGED"
	return mergeSnapshot(pr)
}

func TestDeleteBranchPassesTheOwnedRefThrough(t *testing.T) {
	fake := &fakeDeleter{}
	h := deleteHandler(t, mergedSnapshot(), fake)
	response := requestDeleteBranch(t, h, http.MethodPost, mergeQuery(testToken), deleteBody())
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", response.Code, response.Body.String())
	}
	calls, got := fake.snapshot()
	if calls != 1 {
		t.Fatalf("delete calls = %d, want 1", calls)
	}
	want := prmerge.DeleteRequest{
		Owner: "owner", Repo: "repo", Number: 701,
		Branch: "fanout/dashboard-merge", HeadSha: testHeadSha,
	}
	if got != want {
		t.Fatalf("delete request = %#v, want %#v", got, want)
	}
	var decoded deleteBranchResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.Deleted || decoded.Branch != "fanout/dashboard-merge" {
		t.Fatalf("response = %#v, want the branch reported deleted", decoded)
	}
}

func TestDeleteBranchIsPostOnly(t *testing.T) {
	h := deleteHandler(t, mergedSnapshot(), &fakeDeleter{})
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			response := requestDeleteBranch(t, h, method, mergeQuery(testToken), deleteBody())
			if response.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s status = %d, want 405", method, response.Code)
			}
			if got := response.Header().Get("Allow"); got != "POST" {
				t.Fatalf("Allow = %q, want POST", got)
			}
		})
	}
}

func TestDeleteBranchGates(t *testing.T) {
	tests := []struct {
		name   string
		token  string
		body   string
		status int
		code   string
	}{
		{
			name: "a wrong token is refused", token: "wrong", body: deleteBody(),
			status: http.StatusForbidden,
		},
		{
			name: "a missing head sha is refused", token: testToken,
			body:   `{"prNumber":701}`,
			status: http.StatusBadRequest, code: "invalid_body",
		},
		{
			name: "a pr not on the row is refused", token: testToken,
			body:   fmt.Sprintf(`{"prNumber":999,"headSha":%q}`, testHeadSha),
			status: http.StatusConflict, code: "pr_not_on_row",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeDeleter{}
			h := deleteHandler(t, mergedSnapshot(), fake)
			response := requestDeleteBranch(t, h, http.MethodPost, mergeQuery(tt.token), tt.body)
			if response.Code != tt.status {
				t.Fatalf("status = %d, want %d (body %s)", response.Code, tt.status, response.Body.String())
			}
			if tt.code != "" {
				if got := decodeAPIError(t, response)["code"]; got != tt.code {
					t.Fatalf("error code = %q, want %q", got, tt.code)
				}
			}
			if calls, _ := fake.snapshot(); calls != 0 {
				t.Fatalf("delete calls = %d, want 0", calls)
			}
		})
	}
}

// TestDeleteBranchRefusesAForkHead pins that a fork's head ref is never deleted
// by name in the base repository — the row would otherwise lose a same-named
// branch this pull request never owned.
func TestDeleteBranchRefusesAForkHead(t *testing.T) {
	fork := openPR()
	fork.State, fork.HeadRepo = "MERGED", "stranger/fork"
	fake := &fakeDeleter{}
	h := deleteHandler(t, mergeSnapshot(fork), fake)
	response := requestDeleteBranch(t, h, http.MethodPost, mergeQuery(testToken), deleteBody())
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", response.Code, response.Body.String())
	}
	if calls, _ := fake.snapshot(); calls != 0 {
		t.Fatalf("delete calls = %d, want 0", calls)
	}
}

// TestDeleteBranchSurfacesItsOwnRefusals keeps fanout's refusals separable from
// GitHub's. The delete has no separate preflight step, so these arrive mixed
// with gh failures; reporting them as 422 "GitHub declined" would name the wrong
// cause and drop the machine code the client renders from.
func TestDeleteBranchSurfacesItsOwnRefusals(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
	}{
		{name: "not merged yet", err: prmerge.ErrNotMerged, code: "not_merged"},
		{name: "another open PR still uses the branch", err: prmerge.ErrBranchInUse, code: "branch_in_use"},
		{name: "the head moved after the click", err: prmerge.ErrStaleHead, code: "stale_head"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeDeleter{err: tt.err}
			h := deleteHandler(t, mergedSnapshot(), fake)
			assertAPIError(t,
				requestDeleteBranch(t, h, http.MethodPost, mergeQuery(testToken), deleteBody()),
				http.StatusConflict, tt.code)
		})
	}
}

func TestDeleteBranchRedactsCredentials(t *testing.T) {
	leak := errors.New("gh: token ghp_0123456789abcdefghijklmnopqrstuvwxyz refused")
	fake := &fakeDeleter{err: leak}
	h := deleteHandler(t, mergedSnapshot(), fake)
	response := requestDeleteBranch(t, h, http.MethodPost, mergeQuery(testToken), deleteBody())
	detail := decodeAPIError(t, response)["detail"]
	if strings.Contains(detail, "ghp_") {
		t.Fatalf("detail = %q, want the credential redacted", detail)
	}
}

func TestDeleteBranchUnavailableWhenNotWired(t *testing.T) {
	h := deleteHandler(t, mergedSnapshot(), nil)
	assertAPIError(t, requestDeleteBranch(t, h, http.MethodPost, mergeQuery(testToken), deleteBody()),
		http.StatusServiceUnavailable, "merge_unavailable")
}

// TestDeleteBranchRefusesACommitTheRowDoesNotShow keeps the body an echo of the
// row rather than a free choice of commit. Naming the branch's current tip —
// work pushed after the merge — would otherwise satisfy both the live check and
// the OID fence, and take that work with it.
func TestDeleteBranchRefusesACommitTheRowDoesNotShow(t *testing.T) {
	fake := &fakeDeleter{}
	h := deleteHandler(t, mergedSnapshot(), fake)
	body := `{"prNumber":701,"headSha":"9999999999999999999999999999999999999999"}`
	assertAPIError(t, requestDeleteBranch(t, h, http.MethodPost, mergeQuery(testToken), body),
		http.StatusConflict, "stale_head")
	if calls, _ := fake.snapshot(); calls != 0 {
		t.Fatalf("delete calls = %d, want 0", calls)
	}
}
