package dashboard

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/sessionview"
)

// errPaneGone simulates capture-pane failing on a stale pane / dead tmux server.
var errPaneGone = errors.New("can't find pane: %5")

// fakeCapture is an injectable CapturePane that records its arguments so tests
// can assert pass-through, clamping, and that HEAD never triggers a capture.
type fakeCapture struct {
	mu     sync.Mutex
	calls  int
	paneID string
	lines  int
	out    string
	err    error
}

func (f *fakeCapture) capture(paneID string, lines int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.paneID = paneID
	f.lines = lines
	return f.out, f.err
}

func (f *fakeCapture) snapshot() (int, string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.paneID, f.lines
}

// peekSnapshot is the fixture /api/peek validates against: one session with a
// single pane "%5" whose tmux liveness is controlled by alive. Tests publish
// it into the poller directly instead of seeding state.json because the real
// poller recomputes Alive from the live tmux server, which a test cannot (and
// should not) control.
func peekSnapshot(alive bool) sessionview.Snapshot {
	return sessionview.Snapshot{
		Sessions: []sessionview.Session{{
			Parent: "#1",
			Panes: []sessionview.PaneView{{
				IssueNum:    2,
				Slug:        "child",
				DisplayName: "child",
				Agent:       "claude",
				BranchName:  "fanout/child",
				PaneID:      "%5",
				CreatedAt:   "2026-06-12T00:00:00Z",
				Alive:       alive,
			}},
		}},
	}
}

// publishSnapshot installs snap as the poller's committed snapshot. Safe at
// any point in these tests: newPeekServer never starts the poller loop, so no
// rebuild overwrites the fixture.
func publishSnapshot(srv *Server, snap sessionview.Snapshot) {
	srv.poller.mu.Lock()
	defer srv.poller.mu.Unlock()
	srv.poller.latest = snap
}

// newPeekServer is newTestServer plus an injected fake CapturePane and a
// published snapshot whose one pane ("%5") is live. It serves HTTP without
// starting the poller loop so each test fully controls pane liveness.
func newPeekServer(t *testing.T, token string, capture *fakeCapture) *Server {
	t.Helper()
	srv, err := New(Options{
		ProjectRoot: t.TempDir(),
		Port:        0,
		Token:       token,
		ResolveGH:   func() (string, GHProvider, error) { return "o/n", fakeGH{}, nil },
		CapturePane: capture.capture,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	publishSnapshot(srv, peekSnapshot(true))
	go func() { _ = srv.httpServer.Serve(srv.listener) }()
	t.Cleanup(func() { _ = srv.httpServer.Close() })
	waitReady(t, srv.base)
	return srv
}

// peekURL builds /api/peek with properly URL-encoded query params (a tmux pane
// id "%5" must travel as pane=%255).
func peekURL(base string, params map[string]string) string {
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	return base + "/api/peek?" + q.Encode()
}

// getPeek issues a GET and returns the status code, headers, and fully read
// body. It closes the response body itself so call sites stay bodyclose-clean.
func getPeek(t *testing.T, rawURL string) (int, http.Header, []byte) {
	t.Helper()
	resp, err := http.Get(rawURL)
	if err != nil {
		t.Fatalf("GET %s: %v", rawURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, resp.Header, body
}

func TestPeekTokenGate(t *testing.T) {
	fake := &fakeCapture{out: "irrelevant"}
	srv := newPeekServer(t, "secrettoken", fake)

	status, _, _ := getPeek(t, peekURL(srv.base, map[string]string{"pane": "%5"}))
	if status != http.StatusForbidden {
		t.Fatalf("no-token status = %d want 403", status)
	}
	status, _, _ = getPeek(t, peekURL(srv.base, map[string]string{"pane": "%5", "token": "nope"}))
	if status != http.StatusForbidden {
		t.Fatalf("wrong-token status = %d want 403", status)
	}
	if calls, _, _ := fake.snapshot(); calls != 0 {
		t.Fatalf("capture ran %d time(s) behind the token gate", calls)
	}
}

func TestPeekPostIs405(t *testing.T) {
	srv := newPeekServer(t, "", &fakeCapture{})
	resp, err := http.Post(peekURL(srv.base, map[string]string{"pane": "%5"}), "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d want 405", resp.StatusCode)
	}
}

func TestPeekMalformedPaneIs400(t *testing.T) {
	fake := &fakeCapture{}
	srv := newPeekServer(t, "", fake)
	for _, pane := range []string{"abc", "%1;x", "", "%1 extra", "-p", "%"} {
		status, _, body := getPeek(t, peekURL(srv.base, map[string]string{"pane": pane}))
		if status != http.StatusBadRequest {
			t.Fatalf("pane %q status = %d want 400", pane, status)
		}
		var got map[string]string
		if err := json.Unmarshal(body, &got); err != nil || got["error"] == "" {
			t.Fatalf("pane %q body = %s want JSON error", pane, body)
		}
	}
	if calls, _, _ := fake.snapshot(); calls != 0 {
		t.Fatalf("capture ran %d time(s) on malformed pane ids", calls)
	}
}

func TestPeekInvalidLinesIs400(t *testing.T) {
	srv := newPeekServer(t, "", &fakeCapture{})
	status, _, _ := getPeek(t, peekURL(srv.base, map[string]string{"pane": "%5", "lines": "abc"}))
	if status != http.StatusBadRequest {
		t.Fatalf("lines=abc status = %d want 400", status)
	}
}

func TestPeekUnknownPaneIs404(t *testing.T) {
	fake := &fakeCapture{}
	srv := newPeekServer(t, "", fake)
	status, _, body := getPeek(t, peekURL(srv.base, map[string]string{"pane": "%999"}))
	if status != http.StatusNotFound {
		t.Fatalf("unknown pane status = %d want 404, body %s", status, body)
	}
	if calls, _, _ := fake.snapshot(); calls != 0 {
		t.Fatalf("capture ran %d time(s) for an unknown pane", calls)
	}
}

// TestPeekRecordedButDeadPaneIs404 locks in the pane-id-reuse defense: a pane
// id that is recorded in the snapshot but not Alive (e.g. an unrelated pane
// took over the id after a tmux server restart) must 404 without ever invoking
// capture-pane — even though the capture itself would succeed.
func TestPeekRecordedButDeadPaneIs404(t *testing.T) {
	fake := &fakeCapture{out: "an unrelated pane's terminal"}
	srv := newPeekServer(t, "", fake)
	publishSnapshot(srv, peekSnapshot(false))

	status, _, body := getPeek(t, peekURL(srv.base, map[string]string{"pane": "%5"}))
	if status != http.StatusNotFound {
		t.Fatalf("dead pane status = %d want 404, body %s", status, body)
	}
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil || !strings.Contains(got["error"], "not live") {
		t.Fatalf("body = %s want a JSON error mentioning the pane is not live", body)
	}
	if calls, _, _ := fake.snapshot(); calls != 0 {
		t.Fatalf("capture ran %d time(s) for a dead pane", calls)
	}
}

func TestPeekHappyPath(t *testing.T) {
	fake := &fakeCapture{out: "hello\nworld"}
	srv := newPeekServer(t, "", fake)

	status, header, body := getPeek(t, peekURL(srv.base, map[string]string{"pane": "%5"}))
	if status != http.StatusOK {
		t.Fatalf("status = %d want 200, body %s", status, body)
	}
	if ct := header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type = %q", ct)
	}
	if cc := header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("cache-control = %q want no-store", cc)
	}
	var got peekResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if got.PaneID != "%5" || got.Lines != 80 || got.Output != "hello\nworld" {
		t.Fatalf("response = %+v", got)
	}
	if _, err := time.Parse(time.RFC3339, got.CapturedAt); err != nil {
		t.Fatalf("capturedAt %q not RFC3339: %v", got.CapturedAt, err)
	}
	calls, paneID, lines := fake.snapshot()
	if calls != 1 || paneID != "%5" || lines != 80 {
		t.Fatalf("capture called %d time(s) with (%q, %d) want 1 with (%%5, 80)", calls, paneID, lines)
	}
}

func TestPeekLinesClamping(t *testing.T) {
	for _, tc := range []struct {
		lines string
		want  int
	}{
		{"999", 400},
		{"5", 5},
		{"0", 1},
		{"-3", 1},
	} {
		fake := &fakeCapture{out: "x"}
		srv := newPeekServer(t, "", fake)
		status, _, body := getPeek(t, peekURL(srv.base, map[string]string{"pane": "%5", "lines": tc.lines}))
		if status != http.StatusOK {
			t.Fatalf("lines=%s status = %d want 200, body %s", tc.lines, status, body)
		}
		var got peekResponse
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("unmarshal %s: %v", body, err)
		}
		_, _, captured := fake.snapshot()
		if captured != tc.want || got.Lines != tc.want {
			t.Fatalf("lines=%s: captured %d, reported %d, want %d", tc.lines, captured, got.Lines, tc.want)
		}
	}
}

func TestPeekCaptureErrorIs502(t *testing.T) {
	fake := &fakeCapture{err: errPaneGone}
	srv := newPeekServer(t, "", fake)
	status, _, body := getPeek(t, peekURL(srv.base, map[string]string{"pane": "%5"}))
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d want 502, body %s", status, body)
	}
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if !strings.HasPrefix(got["error"], "tmux capture-pane: ") || !strings.Contains(got["error"], errPaneGone.Error()) {
		t.Fatalf("error = %q", got["error"])
	}
}

func TestPeekHeadSkipsCapture(t *testing.T) {
	fake := &fakeCapture{out: "never returned"}
	srv := newPeekServer(t, "", fake)
	req, err := http.NewRequest(http.MethodHead, peekURL(srv.base, map[string]string{"pane": "%5"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD status = %d want 200", resp.StatusCode)
	}
	if calls, _, _ := fake.snapshot(); calls != 0 {
		t.Fatalf("HEAD triggered %d capture(s)", calls)
	}
}
