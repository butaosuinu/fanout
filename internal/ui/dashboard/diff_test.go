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
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/app/sessionview"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/gitstat"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

type fakeDiffCollector struct {
	mu      sync.Mutex
	calls   int
	path    string
	baseRef string
	patch   gitstat.Patch
	err     error
}

func (f *fakeDiffCollector) collect(path, baseRef string) (gitstat.Patch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.path = path
	f.baseRef = baseRef
	return f.patch, f.err
}

func (f *fakeDiffCollector) snapshot() (calls int, path string, baseRef string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.path, f.baseRef
}

func diffSnapshot(path string) sessionview.Snapshot {
	return sessionview.Snapshot{Sessions: []sessionview.Session{{
		Parent: "575",
		Panes: []sessionview.PaneView{{
			IssueNum:     578,
			Slug:         "dashboard-diff",
			DisplayName:  "dashboard diff",
			Agent:        "codex",
			BranchName:   "fanout/dashboard-diff",
			BaseBranch:   "main",
			PaneID:       "%5",
			WorktreePath: path,
			Alive:        false,
		}},
	}}}
}

func diffHandler(t *testing.T, token string, snap sessionview.Snapshot, collector func(string, string) (gitstat.Patch, error)) http.Handler {
	t.Helper()
	p := newPollerBase(t.TempDir(), newHub())
	p.latest = snap
	s := &Server{poller: p, token: token, diffWorktree: collector}
	h, err := s.handler()
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	return h
}

func requestDiff(t *testing.T, h http.Handler, method string, query url.Values) *httptest.ResponseRecorder {
	t.Helper()
	target := "/api/diff"
	if encoded := query.Encode(); encoded != "" {
		target += "?" + encoded
	}
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, httptest.NewRequest(method, target, nil))
	return recorder
}

func issueDiffQuery() url.Values {
	return url.Values{"parent": {"575"}, "issue": {"578"}}
}

func oneFilePatch(path string) gitstat.Patch {
	group := patchGroup(path, 256)
	return gitstat.Patch{
		MergeBase: strings.Repeat("a", 40),
		Patch:     group,
		Files: []gitstat.FileStat{{
			Path:          path,
			Additions:     1,
			PatchIncluded: true,
		}},
	}
}

func patchGroup(path string, size int) string {
	header := fmt.Sprintf("diff --git a/%s b/%s\n", path, path)
	if size <= len(header) {
		return header
	}
	return header + strings.Repeat("x", size-len(header)-1) + "\n"
}

func decodeDiffResponse(t *testing.T, recorder *httptest.ResponseRecorder) diffResponse {
	t.Helper()
	var response diffResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
	return response
}

func TestDiffTokenAndMethodGatesAreNoStore(t *testing.T) {
	path := t.TempDir()
	fake := &fakeDiffCollector{patch: oneFilePatch("file.txt")}
	h := diffHandler(t, "secret", diffSnapshot(path), fake.collect)

	for _, query := range []url.Values{
		issueDiffQuery(),
		{"parent": {"575"}, "issue": {"578"}, "token": {"wrong"}},
	} {
		response := requestDiff(t, h, http.MethodGet, query)
		if response.Code != http.StatusForbidden {
			t.Fatalf("token failure status = %d, want 403", response.Code)
		}
		if got := response.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("token failure Cache-Control = %q, want no-store", got)
		}
	}

	postQuery := issueDiffQuery()
	postQuery.Set("token", "secret")
	post := requestDiff(t, h, http.MethodPost, postQuery)
	if post.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", post.Code)
	}
	if got := post.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("POST Cache-Control = %q, want no-store", got)
	}

	getQuery := issueDiffQuery()
	getQuery.Set("token", "secret")
	get := requestDiff(t, h, http.MethodGet, getQuery)
	if get.Code != http.StatusOK {
		t.Fatalf("authorized GET status = %d, body %s", get.Code, get.Body.String())
	}
	if calls, _, _ := fake.snapshot(); calls != 1 {
		t.Fatalf("collector calls = %d, want 1 authorized GET only", calls)
	}
}

func TestDiffRejectsMalformedIdentityBeforeCollection(t *testing.T) {
	path := t.TempDir()
	fake := &fakeDiffCollector{patch: oneFilePatch("file.txt")}
	h := diffHandler(t, "", diffSnapshot(path), fake.collect)
	tests := []struct {
		name  string
		query url.Values
	}{
		{name: "missing parent", query: url.Values{"issue": {"578"}}},
		{name: "missing kind", query: url.Values{"parent": {"575"}}},
		{name: "issue and task", query: url.Values{"parent": {"575"}, "issue": {"578"}, "task": {"api"}, "source": {"x"}}},
		{name: "zero issue", query: url.Values{"parent": {"575"}, "issue": {"0"}}},
		{name: "noncanonical issue", query: url.Values{"parent": {"575"}, "issue": {"0578"}}},
		{name: "positive issue source", query: url.Values{"parent": {"575"}, "issue": {"578"}, "source": {"x"}}},
		{name: "negative issue without source", query: url.Values{"parent": {"575"}, "issue": {"-1"}}},
		{name: "task without source", query: url.Values{"parent": {"575"}, "task": {"api"}}},
		{name: "duplicate parent", query: url.Values{"parent": {"575", "576"}, "issue": {"578"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := requestDiff(t, h, http.MethodGet, tt.query)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %s", response.Code, response.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body["error"] == "" {
				t.Fatalf("error body = %q, want JSON error", response.Body.String())
			}
		})
	}
	if calls, _, _ := fake.snapshot(); calls != 0 {
		t.Fatalf("collector ran %d time(s) for malformed identities", calls)
	}
}

func TestDiffIdentitySupportsIssueTaskAndNegativeRows(t *testing.T) {
	path := t.TempDir()
	tests := []struct {
		name  string
		pane  sessionview.PaneView
		query url.Values
	}{
		{
			name:  "github issue",
			pane:  sessionview.PaneView{IssueNum: 578},
			query: url.Values{"parent": {"575"}, "issue": {"578"}},
		},
		{
			name:  "plan task",
			pane:  sessionview.PaneView{TaskID: "api", SourceKey: "worktree-a"},
			query: url.Values{"parent": {"575"}, "task": {"api"}, "source": {"worktree-a"}},
		},
		{
			name:  "negative synthetic issue",
			pane:  sessionview.PaneView{IssueNum: -1, SourceKey: "worktree-a"},
			query: url.Values{"parent": {"575"}, "issue": {"-1"}, "source": {"worktree-a"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pane := tt.pane
			pane.PaneID = "w1:p1"
			pane.Backend = backend.Herdr
			pane.BranchName = "fanout/diff"
			pane.BaseBranch = "main"
			pane.WorktreePath = path
			pane.Alive = false
			snap := sessionview.Snapshot{Sessions: []sessionview.Session{{Parent: "575", Panes: []sessionview.PaneView{pane}}}}
			fake := &fakeDiffCollector{patch: oneFilePatch("file.txt")}
			response := requestDiff(t, diffHandler(t, "", snap, fake.collect), http.MethodGet, tt.query)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 for dead Herdr row; body %s", response.Code, response.Body.String())
			}
			got := decodeDiffResponse(t, response)
			if got.PaneID != "w1:p1" || got.BaseBranch != "main" || got.Files == nil {
				t.Fatalf("response metadata = %+v", got)
			}
		})
	}
}

func TestDiffIdentityMustMatchExactlyOneSnapshotRow(t *testing.T) {
	path := t.TempDir()
	fake := &fakeDiffCollector{patch: oneFilePatch("file.txt")}

	unknown := requestDiff(t, diffHandler(t, "", diffSnapshot(path), fake.collect), http.MethodGet,
		url.Values{"parent": {"575"}, "issue": {"999"}})
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown identity status = %d, want 404", unknown.Code)
	}

	duplicate := diffSnapshot(path)
	duplicate.Sessions[0].Panes = append(duplicate.Sessions[0].Panes, duplicate.Sessions[0].Panes[0])
	response := requestDiff(t, diffHandler(t, "", duplicate, fake.collect), http.MethodGet, issueDiffQuery())
	if response.Code != http.StatusNotFound {
		t.Fatalf("duplicate identity status = %d, want 404", response.Code)
	}
	if calls, _, _ := fake.snapshot(); calls != 0 {
		t.Fatalf("collector ran %d time(s) without exactly one row", calls)
	}
}

func TestDiffRowsWithoutAvailableWorktreeAre404(t *testing.T) {
	existing := t.TempDir()
	tests := []struct {
		name string
		edit func(*sessionview.PaneView)
	}{
		{name: "shell", edit: func(p *sessionview.PaneView) { p.Kind = state.PaneKindShell }},
		{name: "not started", edit: func(p *sessionview.PaneView) { p.NotStarted = true }},
		{name: "empty path", edit: func(p *sessionview.PaneView) { p.WorktreePath = "" }},
		{name: "cleaned up", edit: func(p *sessionview.PaneView) { p.WorktreePath = filepath.Join(existing, "gone") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := diffSnapshot(existing)
			tt.edit(&snap.Sessions[0].Panes[0])
			fake := &fakeDiffCollector{patch: oneFilePatch("file.txt")}
			response := requestDiff(t, diffHandler(t, "", snap, fake.collect), http.MethodGet, issueDiffQuery())
			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404; body %s", response.Code, response.Body.String())
			}
			if calls, _, _ := fake.snapshot(); calls != 0 {
				t.Fatalf("collector ran %d time(s)", calls)
			}
		})
	}
}

func TestDiffUsesOnlyRecordedPathAndBase(t *testing.T) {
	path := t.TempDir()
	fake := &fakeDiffCollector{patch: oneFilePatch("file.txt")}
	h := diffHandler(t, "", diffSnapshot(path), fake.collect)
	query := issueDiffQuery()
	query.Set("path", "/attacker/path")
	query.Set("base", "attacker-ref")
	response := requestDiff(t, h, http.MethodGet, query)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", response.Code, response.Body.String())
	}
	if calls, gotPath, gotBase := fake.snapshot(); calls != 1 || gotPath != path || gotBase != "main" {
		t.Fatalf("collector = calls:%d path:%q base:%q, want recorded path/base", calls, gotPath, gotBase)
	}
}

func TestDiffCollectorFailureIs502(t *testing.T) {
	path := t.TempDir()
	for _, name := range []string{"git failure", "base resolution failure"} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeDiffCollector{err: errors.New(name)}
			response := requestDiff(t, diffHandler(t, "", diffSnapshot(path), fake.collect), http.MethodGet, issueDiffQuery())
			if response.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502; body %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestDiffHeadValidatesWorktreeButSkipsGit(t *testing.T) {
	path := t.TempDir()
	fake := &fakeDiffCollector{patch: oneFilePatch("file.txt")}
	h := diffHandler(t, "", diffSnapshot(path), fake.collect)
	response := requestDiff(t, h, http.MethodHead, issueDiffQuery())
	if response.Code != http.StatusOK || response.Body.Len() != 0 {
		t.Fatalf("HEAD = status:%d body:%q, want 200 and empty body", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("HEAD Content-Type = %q", got)
	}
	if calls, _, _ := fake.snapshot(); calls != 0 {
		t.Fatalf("HEAD ran collector %d time(s)", calls)
	}

	missing := diffSnapshot(filepath.Join(path, "gone"))
	missingResponse := requestDiff(t, diffHandler(t, "", missing, fake.collect), http.MethodHead, issueDiffQuery())
	if missingResponse.Code != http.StatusNotFound {
		t.Fatalf("HEAD cleaned worktree status = %d, want 404", missingResponse.Code)
	}
}

func TestDiffRejectsMoreThan500Files(t *testing.T) {
	path := t.TempDir()
	patch := gitstat.Patch{Files: make([]gitstat.FileStat, diffMaxFiles+1)}
	for i := range patch.Files {
		patch.Files[i] = gitstat.FileStat{Path: "large-" + strconv.Itoa(i), OmittedReason: "tooLarge"}
	}
	response := requestDiff(t, diffHandler(t, "", diffSnapshot(path), (&fakeDiffCollector{patch: patch}).collect), http.MethodGet, issueDiffQuery())
	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body %s", response.Code, response.Body.String())
	}
}

func TestDiffCollectionAndResponseLimitsKeepAllFiles(t *testing.T) {
	path := t.TempDir()
	patch := gitstat.Patch{MergeBase: strings.Repeat("a", 40)}
	for i := range 42 {
		name := fmt.Sprintf("file-%02d.txt", i)
		patch.Files = append(patch.Files, gitstat.FileStat{
			Path:          name,
			Additions:     1,
			Deletions:     1,
			PatchIncluded: true,
		})
		patch.Patch += patchGroup(name, 256*1024)
	}
	fake := &fakeDiffCollector{patch: patch}
	response := requestDiff(t, diffHandler(t, "", diffSnapshot(path), fake.collect), http.MethodGet, issueDiffQuery())
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", response.Code, response.Body.String())
	}
	if response.Body.Len() > diffMaxBytes {
		t.Fatalf("response has %d bytes, limit %d", response.Body.Len(), diffMaxBytes)
	}
	got := decodeDiffResponse(t, response)
	if len(got.Files) != 42 {
		t.Fatalf("files = %d, want all 42", len(got.Files))
	}
	if got.TotalBytes != diffCollectionMaxBytes || !got.Truncated {
		t.Fatalf("totalBytes/truncated = %d/%v", got.TotalBytes, got.Truncated)
	}
	for i := 40; i < 42; i++ {
		file := got.Files[i]
		if file.OmittedReason != "collectionLimit" || file.PatchIncluded || file.Additions != nil || file.Deletions != nil {
			t.Fatalf("files[%d] = %+v, want collectionLimit with null stats", i, file)
		}
	}
	if got.Files[39].OmittedReason != "responseLimit" {
		t.Fatalf("files[39] reason = %q, want responseLimit without overriding later collectionLimit", got.Files[39].OmittedReason)
	}
}

func TestDiffResponseLimitUsesCompleteFileGroups(t *testing.T) {
	path := t.TempDir()
	patch := gitstat.Patch{MergeBase: strings.Repeat("a", 40)}
	for i := range 20 {
		name := fmt.Sprintf("file-%02d.txt", i)
		patch.Files = append(patch.Files, gitstat.FileStat{Path: name, Additions: 1, PatchIncluded: true})
		patch.Patch += patchGroup(name, 64*1024)
	}
	response := requestDiff(t, diffHandler(t, "", diffSnapshot(path), (&fakeDiffCollector{patch: patch}).collect), http.MethodGet, issueDiffQuery())
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", response.Code, response.Body.String())
	}
	got := decodeDiffResponse(t, response)
	included, omitted := 0, 0
	for _, file := range got.Files {
		switch file.OmittedReason {
		case "":
			included++
		case "responseLimit":
			omitted++
		default:
			t.Fatalf("unexpected omittedReason %q", file.OmittedReason)
		}
	}
	if included == 0 || omitted == 0 || !got.Truncated {
		t.Fatalf("included/omitted/truncated = %d/%d/%v", included, omitted, got.Truncated)
	}
	if got.TotalBytes != len(patch.Patch) || got.Patch != patch.Patch[:len(got.Patch)] {
		t.Fatalf("response did not keep the largest complete patch prefix")
	}
}

func TestDiffResponseLimitDoesNotCountTruncatedBooleanAsPatchBudget(t *testing.T) {
	const capturedAt = "2026-08-01T00:00:00Z"
	pv := diffSnapshot(t.TempDir()).Sessions[0].Panes[0]
	file := gitstat.FileStat{Path: "file.txt", Additions: 1, PatchIncluded: true}
	group := patchGroup(file.Path, 128)
	fullBody := func() []byte {
		t.Helper()
		additions := file.Additions
		body, err := json.Marshal(diffResponse{
			PaneID:     pv.PaneID,
			BranchName: pv.BranchName,
			BaseBranch: pv.BaseBranch,
			MergeBase:  strings.Repeat("a", 40),
			CapturedAt: capturedAt,
			Files: []diffFileEntry{{
				Path:          file.Path,
				Additions:     &additions,
				Deletions:     new(int),
				PatchIncluded: true,
			}},
			Patch:      group,
			TotalBytes: len(group),
		})
		if err != nil {
			t.Fatalf("marshal exact-boundary response: %v", err)
		}
		return body
	}

	for range 10 {
		delta := diffMaxBytes + 1 - len(fullBody())
		if delta == 0 {
			break
		}
		group = patchGroup(file.Path, len(group)+delta)
	}
	if got := len(fullBody()); got != diffMaxBytes+1 {
		t.Fatalf("full response bytes = %d, want %d", got, diffMaxBytes+1)
	}

	body, err := marshalDiffResponse(pv, gitstat.Patch{
		MergeBase: strings.Repeat("a", 40),
		Patch:     group,
		Files:     []gitstat.FileStat{file},
	}, capturedAt)
	if err != nil {
		t.Fatalf("marshalDiffResponse: %v", err)
	}
	var got diffResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Patch != "" || !got.Truncated || got.Files[0].PatchIncluded ||
		got.Files[0].OmittedReason != "responseLimit" {
		t.Fatalf("response = %+v, want the group omitted by responseLimit", got)
	}
}

func TestDiffMetadataOnlyBodyOverLimitIs502(t *testing.T) {
	path := t.TempDir()
	patch := gitstat.Patch{Files: make([]gitstat.FileStat, diffMaxFiles)}
	for i := range patch.Files {
		patch.Files[i] = gitstat.FileStat{
			Path:          strings.Repeat("p", 2500) + strconv.Itoa(i),
			OmittedReason: "tooLarge",
		}
	}
	response := requestDiff(t, diffHandler(t, "", diffSnapshot(path), (&fakeDiffCollector{patch: patch}).collect), http.MethodGet, issueDiffQuery())
	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body size %d", response.Code, response.Body.Len())
	}
}

func TestCollectWorktreePatchDeadline(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	started := make(chan struct{})
	_, err := collectWorktreePatch(context.Background(), func(context.Context, string, string) (gitstat.Patch, error) {
		close(started)
		<-release
		return gitstat.Patch{}, nil
	}, "/wt", "main", 10*time.Millisecond)
	<-started
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline exceeded", err)
	}
}

func TestSplitDiffPatchGroupsKeepsFileTypeChangeTogether(t *testing.T) {
	deleted := "diff --git a/entry b/entry\ndeleted file mode 100644\n"
	added := "diff --git a/entry b/entry\nnew file mode 120000\n"
	next := "diff --git a/next b/next\nnew file mode 100644\n"
	groups, err := splitDiffPatchGroups(deleted + added + next)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[0] != deleted+added || groups[1] != next {
		t.Fatalf("groups = %#v, want type-change pair plus next file", groups)
	}
}

func TestDiffDefaultCollectorIncludesUntrackedFileAndRejectsHEADBase(t *testing.T) {
	repo := t.TempDir()
	runDiffGit(t, repo, "init")
	runDiffGit(t, repo, "config", "user.email", "test@example.com")
	runDiffGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("tracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runDiffGit(t, repo, "add", "tracked.txt")
	runDiffGit(t, repo, "commit", "-m", "initial")
	runDiffGit(t, repo, "branch", "-M", "main")
	runDiffGit(t, repo, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	snap := diffSnapshot(repo)
	response := requestDiff(t, diffHandler(t, "", snap, nil), http.MethodGet, issueDiffQuery())
	if response.Code != http.StatusOK {
		t.Fatalf("untracked status = %d, body %s", response.Code, response.Body.String())
	}
	got := decodeDiffResponse(t, response)
	if len(got.Files) != 1 || got.Files[0].Path != "untracked.txt" || !strings.Contains(got.Patch, "+new") {
		t.Fatalf("untracked response = %+v patch %q", got.Files, got.Patch)
	}

	snap.Sessions[0].Panes[0].BaseBranch = "HEAD"
	badBase := requestDiff(t, diffHandler(t, "", snap, nil), http.MethodGet, issueDiffQuery())
	if badBase.Code != http.StatusBadGateway {
		t.Fatalf("HEAD base status = %d, want 502; body %s", badBase.Code, badBase.Body.String())
	}
}

func TestDiffDefaultCollectorReportsRenameAsOneFile(t *testing.T) {
	repo := t.TempDir()
	runDiffGit(t, repo, "init")
	runDiffGit(t, repo, "config", "user.email", "test@example.com")
	runDiffGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "moved.txt"), []byte("one\ntwo\nthree\nfour\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runDiffGit(t, repo, "add", "moved.txt")
	runDiffGit(t, repo, "commit", "-m", "initial")
	runDiffGit(t, repo, "branch", "-M", "main")
	runDiffGit(t, repo, "checkout", "-b", "feature")
	runDiffGit(t, repo, "mv", "moved.txt", "renamed.txt")
	runDiffGit(t, repo, "commit", "-m", "move it")

	snap := diffSnapshot(repo)
	response := requestDiff(t, diffHandler(t, "", snap, nil), http.MethodGet, issueDiffQuery())
	if response.Code != http.StatusOK {
		t.Fatalf("rename status = %d, body %s", response.Code, response.Body.String())
	}
	got := decodeDiffResponse(t, response)
	// One entry keyed by the new path, not a deletion plus an addition.
	if len(got.Files) != 1 {
		t.Fatalf("rename response = %+v, want one file", got.Files)
	}
	file := got.Files[0]
	if file.Path != "renamed.txt" || file.OldPath != "moved.txt" {
		t.Fatalf("rename response file = %+v, want renamed.txt from moved.txt", file)
	}
	if file.Additions == nil || *file.Additions != 0 || file.Deletions == nil || *file.Deletions != 0 {
		t.Fatalf("rename response file = %+v, want +0/-0", file)
	}
	if !strings.Contains(got.Patch, "rename from moved.txt") {
		t.Fatalf("rename response patch = %q, want a rename header", got.Patch)
	}
}

func TestMarshalDiffResponseOmitsOldPathForNonRenames(t *testing.T) {
	body, err := marshalDiffResponse(
		sessionview.PaneView{PaneID: "%1"},
		gitstat.Patch{
			MergeBase: "abc",
			Files: []gitstat.FileStat{
				{Path: "plain.txt", Additions: 1, PatchIncluded: true},
			},
			Patch: "diff --git a/plain.txt b/plain.txt\n+one\n",
		},
		"2026-08-05T00:00:00Z",
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "oldPath") {
		t.Fatalf("marshalDiffResponse() = %s, want no oldPath key", body)
	}
}

func TestValidDiffFileStat(t *testing.T) {
	tests := []struct {
		name string
		stat gitstat.FileStat
		want bool
	}{
		{name: "collected file carries its patch", stat: gitstat.FileStat{PatchIncluded: true}, want: true},
		{name: "collected file cannot be binary", stat: gitstat.FileStat{PatchIncluded: true, Binary: true}},
		{
			name: "binary file is listed without a patch",
			stat: gitstat.FileStat{Binary: true, OmittedReason: "binary"},
			want: true,
		},
		{name: "binary reason requires the binary flag", stat: gitstat.FileStat{OmittedReason: "binary"}},
		{name: "oversized file is listed without a patch", stat: gitstat.FileStat{OmittedReason: "tooLarge"}, want: true},
		{
			name: "collection limit is listed without a patch",
			stat: gitstat.FileStat{OmittedReason: "collectionLimit"},
			want: true,
		},
		{name: "unknown reason is rejected", stat: gitstat.FileStat{OmittedReason: "someday"}},
		{
			name: "rename must name two distinct paths",
			stat: gitstat.FileStat{Path: "same.txt", OldPath: "same.txt", PatchIncluded: true},
		},
		{
			name: "rename between two paths is fine",
			stat: gitstat.FileStat{Path: "new.txt", OldPath: "old.txt", PatchIncluded: true},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validDiffFileStat(tt.stat); got != tt.want {
				t.Fatalf("validDiffFileStat(%+v) = %t, want %t", tt.stat, got, tt.want)
			}
		})
	}
}

func runDiffGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmdArgs := append([]string{"-C", repo}, args...)
	out, err := exec.Command("git", cmdArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

// TestStableWorktreePatch pins the fence around a slow collection: what the
// response says about the patch has to describe the patch it actually returned,
// or there is nothing for the merge button's comparison to mean.
func TestStableWorktreePatch(t *testing.T) {
	patch := gitstat.Patch{MergeBase: "base", Patch: "diff"}
	collect := func() (gitstat.Patch, error) { return patch, nil }
	pushed := func(gitstat.Patch) (bool, error) { return true, nil }
	marks := func(seq ...worktreeMark) func() (worktreeMark, error) {
		i := 0
		return func() (worktreeMark, error) {
			out := seq[i]
			i++
			return out, nil
		}
	}
	clean := worktreeMark{head: "abc123"}

	t.Run("describes a worktree that stayed put", func(t *testing.T) {
		got, err := stableWorktreePatch(marks(clean, clean), collect, pushed)
		if err != nil {
			t.Fatal(err)
		}
		if got.Head != "abc123" || got.Dirty || !got.BasePushed {
			t.Fatalf("got %+v, want head abc123, clean, base pushed", got)
		}
	})

	t.Run("carries the dirty state the patch was taken with", func(t *testing.T) {
		dirty := worktreeMark{head: "abc123", dirty: true}
		got, err := stableWorktreePatch(marks(dirty, dirty), collect, pushed)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Dirty {
			t.Fatalf("Dirty = false, want true")
		}
	})

	t.Run("reports a base the remote does not have", func(t *testing.T) {
		local := func(gitstat.Patch) (bool, error) { return false, nil }
		got, err := stableWorktreePatch(marks(clean, clean), collect, local)
		if err != nil {
			t.Fatal(err)
		}
		if got.BasePushed {
			t.Fatal("BasePushed = true, want false")
		}
	})

	tests := []struct {
		name          string
		before, after worktreeMark
	}{
		{name: "a commit landed mid-collection", before: clean, after: worktreeMark{head: "def456"}},
		{
			// The patch already carries the edit; describing it as clean would
			// tell the button the patch equals a commit.
			name:   "an edit landed mid-collection",
			before: clean, after: worktreeMark{head: "abc123", dirty: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := stableWorktreePatch(marks(tt.before, tt.after), collect, pushed)
			if err == nil {
				t.Fatal("stableWorktreePatch() error = nil, want the refusal")
			}
			if got.Patch != "" {
				t.Fatalf("patch = %#v, want it withheld", got)
			}
		})
	}
}
