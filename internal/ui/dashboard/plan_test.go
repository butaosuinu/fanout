package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/app/sessionview"
	"github.com/butaosuinu/fanout/internal/core/backend"
)

// planSnapshot is peekSnapshot's pane "%5" recorded as codex and flagged (or
// not) as a plan-mode pane — the fixture /api/plan validates against.
func planSnapshot(alive, planMode bool) sessionview.Snapshot {
	snap := peekSnapshot(alive)
	snap.Sessions[0].Panes[0].Agent = "codex"
	snap.Sessions[0].Panes[0].PlanMode = planMode
	return snap
}

// newPlanServer is newPeekServer's twin for /api/plan: the fake feeds
// CapturePlan, and CapturePane gets a tripwire so a handler mix-up fails the
// test instead of running real tmux.
func newPlanServer(t *testing.T, token string, capture *fakeCapture) *Server {
	t.Helper()
	srv, err := New(Options{
		ProjectRoot: t.TempDir(),
		Port:        0,
		Token:       token,
		ResolveGH:   func() (string, GHProvider, error) { return "o/n", fakeGH{}, nil },
		CapturePane: func(string, int) (string, error) {
			t.Error("CapturePane called from /api/plan")
			return "", nil
		},
		CapturePlan: capture.capture,
		// Request-time revalidation is exercised by
		// TestPlanVerifyFailureIs404AndSkipsCapture; everywhere else a no-op
		// keeps the fixture's liveness authoritative.
		VerifyPane: func(sessionview.PaneView) error { return nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	publishSnapshot(srv, planSnapshot(true, true))
	go func() { _ = srv.httpServer.Serve(srv.listener) }()
	t.Cleanup(func() { _ = srv.httpServer.Close() })
	waitReady(t, srv.base)
	return srv
}

// planURL builds /api/plan with properly URL-encoded query params (a tmux pane
// id "%5" must travel as pane=%255).
func planURL(base string, params map[string]string) string {
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	return base + "/api/plan?" + q.Encode()
}

const planFixtureOutput = "noise\n<proposed_plan>\n## Plan\n1. do the thing\n</proposed_plan>\ntrailing"

func TestPlanTokenGate(t *testing.T) {
	fake := &fakeCapture{out: planFixtureOutput}
	srv := newPlanServer(t, "secrettoken", fake)

	status, _, _ := getPeek(t, planURL(srv.base, map[string]string{"pane": "%5"}))
	if status != http.StatusForbidden {
		t.Fatalf("no-token status = %d want 403", status)
	}
	status, _, _ = getPeek(t, planURL(srv.base, map[string]string{"pane": "%5", "token": "nope"}))
	if status != http.StatusForbidden {
		t.Fatalf("wrong-token status = %d want 403", status)
	}
	if calls, _, _ := fake.snapshot(); calls != 0 {
		t.Fatalf("capture ran %d time(s) behind the token gate", calls)
	}
}

func TestPlanPostIs405(t *testing.T) {
	srv := newPlanServer(t, "", &fakeCapture{})
	resp, err := http.Post(planURL(srv.base, map[string]string{"pane": "%5"}), "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d want 405", resp.StatusCode)
	}
}

func TestPlanMalformedPaneIs400(t *testing.T) {
	fake := &fakeCapture{}
	srv := newPlanServer(t, "", fake)
	for _, pane := range []string{"abc", "%1;x", "", "%1 extra", "-p", "%"} {
		status, _, body := getPeek(t, planURL(srv.base, map[string]string{"pane": pane}))
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

func TestPlanUnknownPaneIs404(t *testing.T) {
	fake := &fakeCapture{}
	srv := newPlanServer(t, "", fake)
	status, _, body := getPeek(t, planURL(srv.base, map[string]string{"pane": "%999"}))
	if status != http.StatusNotFound {
		t.Fatalf("unknown pane status = %d want 404, body %s", status, body)
	}
	if calls, _, _ := fake.snapshot(); calls != 0 {
		t.Fatalf("capture ran %d time(s) for an unknown pane", calls)
	}
}

func TestPlanRecordedButDeadPaneIs404(t *testing.T) {
	fake := &fakeCapture{out: planFixtureOutput}
	srv := newPlanServer(t, "", fake)
	publishSnapshot(srv, planSnapshot(false, true))

	status, _, body := getPeek(t, planURL(srv.base, map[string]string{"pane": "%5"}))
	if status != http.StatusNotFound {
		t.Fatalf("dead pane status = %d want 404, body %s", status, body)
	}
	if calls, _, _ := fake.snapshot(); calls != 0 {
		t.Fatalf("capture ran %d time(s) for a dead pane", calls)
	}
}

func TestPlanDuplicatePaneIDPrefersLiveTmuxRow(t *testing.T) {
	fake := &fakeCapture{out: planFixtureOutput}
	srv := newPlanServer(t, "", fake)
	srv.verifyPane = func(pv sessionview.PaneView) error {
		if pv.WorktreePath != "/wt/live" {
			return fmt.Errorf("verified stale duplicate %q", pv.WorktreePath)
		}
		return nil
	}
	snap := duplicatePaneSnapshot(true)
	for i := range snap.Sessions {
		snap.Sessions[i].Panes[0].Agent = "codex"
	}
	publishSnapshot(srv, snap)

	status, _, body := getPeek(t, planURL(srv.base, map[string]string{"pane": "%5"}))
	if status != http.StatusOK {
		t.Fatalf("duplicate pane status = %d want 200, body %s", status, body)
	}
	if calls, paneID, _ := fake.snapshot(); calls != 1 || paneID != "%5" {
		t.Fatalf("capture = %d call(s) for %q, want one live %%5 capture", calls, paneID)
	}
}

func TestPlanHerdrPaneIs404AndSkipsCapture(t *testing.T) {
	fake := &fakeCapture{out: planFixtureOutput}
	srv := newPlanServer(t, "", fake)
	snap := planSnapshot(true, true)
	snap.Sessions[0].Panes[0].Backend = backend.Herdr
	snap.Sessions[0].Panes[0].PaneID = "w1:p1"
	publishSnapshot(srv, snap)

	status, _, body := getPeek(t, planURL(srv.base, map[string]string{"pane": "w1:p1"}))
	if status != http.StatusNotFound {
		t.Fatalf("herdr pane status = %d want 404, body %s", status, body)
	}
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil || !strings.Contains(got["error"], backend.HerdrContentReadReason) {
		t.Fatalf("body = %s want the explicit herdr content-read reason", body)
	}
	if calls, _, _ := fake.snapshot(); calls != 0 {
		t.Fatalf("capture ran %d time(s) for a herdr pane", calls)
	}
}

// TestPlanNonPlanModePaneIs404 locks in the plan-specific gate: a live,
// verifiable pane that was NOT launched with --codex-plan-mode must 404
// without ever capturing — /api/plan never widens into a second generic
// capture endpoint.
func TestPlanNonPlanModePaneIs404(t *testing.T) {
	fake := &fakeCapture{out: planFixtureOutput}
	srv := newPlanServer(t, "", fake)
	publishSnapshot(srv, planSnapshot(true, false))

	status, _, body := getPeek(t, planURL(srv.base, map[string]string{"pane": "%5"}))
	if status != http.StatusNotFound {
		t.Fatalf("non-plan pane status = %d want 404, body %s", status, body)
	}
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil || !strings.Contains(got["error"], "plan-mode") {
		t.Fatalf("body = %s want a JSON error mentioning plan-mode", body)
	}
	if calls, _, _ := fake.snapshot(); calls != 0 {
		t.Fatalf("capture ran %d time(s) for a non-plan pane", calls)
	}
}

func TestPlanNonCodexPlanModePaneIs404(t *testing.T) {
	for _, agent := range []string{"claude", "opencode"} {
		t.Run(agent, func(t *testing.T) {
			fake := &fakeCapture{out: planFixtureOutput}
			srv := newPlanServer(t, "", fake)
			snap := planSnapshot(true, true)
			snap.Sessions[0].Panes[0].Agent = agent
			publishSnapshot(srv, snap)

			status, _, body := getPeek(t, planURL(srv.base, map[string]string{"pane": "%5"}))
			if status != http.StatusNotFound {
				t.Fatalf("%s plan-mode pane status = %d want 404, body %s", agent, status, body)
			}
			var got map[string]string
			if err := json.Unmarshal(body, &got); err != nil || !strings.Contains(got["error"], "codex") {
				t.Fatalf("body = %s want a JSON error mentioning codex", body)
			}
			if calls, _, _ := fake.snapshot(); calls != 0 {
				t.Fatalf("capture ran %d time(s) for a %s plan-mode pane", calls, agent)
			}
		})
	}
}

func TestPlanRowWithoutWorktreeIs404(t *testing.T) {
	t.Parallel()
	fake := &fakeCapture{out: "should never be read"}
	srv := newPlanServer(t, "", fake)
	snap := planSnapshot(true, true)
	snap.Sessions[0].Panes[0].WorktreePath = "" // legacy id-only-alive row
	publishSnapshot(srv, snap)

	status, _, body := getPeek(t, planURL(srv.base, map[string]string{"pane": "%5"}))
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body %s", status, body)
	}
	if calls, _, _ := fake.snapshot(); calls != 0 {
		t.Fatalf("capture calls = %d, want 0 (no worktree to verify against)", calls)
	}
}

func TestPlanVerifyFailureIs404AndSkipsCapture(t *testing.T) {
	t.Parallel()
	fake := &fakeCapture{out: "should never be read"}
	srv, err := New(Options{
		ProjectRoot: t.TempDir(),
		Port:        0,
		Token:       "",
		ResolveGH:   func() (string, GHProvider, error) { return "o/n", fakeGH{}, nil },
		CapturePlan: fake.capture,
		VerifyPane: func(pv sessionview.PaneView) error {
			if pv.WorktreePath != "/wt/child" {
				t.Errorf("VerifyPane worktree = %q, want recorded /wt/child", pv.WorktreePath)
			}
			return fmt.Errorf("pane %s is no longer at its recorded worktree", pv.PaneID)
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	publishSnapshot(srv, planSnapshot(true, true))
	go func() { _ = srv.httpServer.Serve(srv.listener) }()
	t.Cleanup(func() { _ = srv.httpServer.Close() })
	waitReady(t, srv.base)

	status, _, body := getPeek(t, planURL(srv.base, map[string]string{"pane": "%5"}))
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body %s", status, body)
	}
	if calls, _, _ := fake.snapshot(); calls != 0 {
		t.Fatalf("capture calls = %d, want 0 (verify failed first)", calls)
	}
}

func TestPlanHeadSkipsCapture(t *testing.T) {
	fake := &fakeCapture{out: "never returned"}
	srv := newPlanServer(t, "", fake)
	req, err := http.NewRequest(http.MethodHead, planURL(srv.base, map[string]string{"pane": "%5"}), nil)
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

func TestPlanCaptureErrorIs502(t *testing.T) {
	fake := &fakeCapture{err: errPaneGone}
	srv := newPlanServer(t, "", fake)
	status, _, body := getPeek(t, planURL(srv.base, map[string]string{"pane": "%5"}))
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

func TestPlanHappyPath(t *testing.T) {
	fake := &fakeCapture{out: planFixtureOutput}
	srv := newPlanServer(t, "", fake)

	status, header, body := getPeek(t, planURL(srv.base, map[string]string{"pane": "%5"}))
	if status != http.StatusOK {
		t.Fatalf("status = %d want 200, body %s", status, body)
	}
	if ct := header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type = %q", ct)
	}
	if cc := header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("cache-control = %q want no-store", cc)
	}
	var got planResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if got.PaneID != "%5" || !got.Found || got.Plan != "## Plan\n1. do the thing" {
		t.Fatalf("response = %+v", got)
	}
	if _, err := time.Parse(time.RFC3339, got.CapturedAt); err != nil {
		t.Fatalf("capturedAt %q not RFC3339: %v", got.CapturedAt, err)
	}
	calls, paneID, lines := fake.snapshot()
	if calls != 1 || paneID != "%5" || lines != planCaptureLines {
		t.Fatalf("capture called %d time(s) with (%q, %d) want 1 with (%%5, %d)", calls, paneID, lines, planCaptureLines)
	}
}

// TestPlanWithoutBlockIsFoundFalse: a plan-mode pane whose capturable output
// holds no complete block (not yet proposed, or scrolled out) is a successful
// 200 with found=false — the SPA distinguishes "no plan yet" from transport
// errors by this field, not by status code.
func TestPlanWithoutBlockIsFoundFalse(t *testing.T) {
	fake := &fakeCapture{out: "codex is still thinking\nno tags here"}
	srv := newPlanServer(t, "", fake)

	status, _, body := getPeek(t, planURL(srv.base, map[string]string{"pane": "%5"}))
	if status != http.StatusOK {
		t.Fatalf("status = %d want 200, body %s", status, body)
	}
	var got planResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if got.Found || got.Plan != "" {
		t.Fatalf("response = %+v want found=false with empty plan", got)
	}
}

// HEAD も PlanMode ゲートを通る: 非 plan-mode pane への HEAD は 404
// (GET が成功しないリクエストは HEAD でも成功しない)。capture は走らない。
func TestPlanHEADOnNonPlanPaneIs404(t *testing.T) {
	fake := &fakeCapture{}
	srv := newPlanServer(t, "", fake)
	publishSnapshot(srv, planSnapshot(true, false)) // %5 は live だが plan-mode でない
	req, err := http.NewRequest(http.MethodHead, planURL(srv.base, map[string]string{"pane": "%5"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("HEAD non-plan pane status = %d want 404", resp.StatusCode)
	}
	if calls, _, _ := fake.snapshot(); calls != 0 {
		t.Fatalf("capture ran %d time(s)", calls)
	}
}

func TestExtractLastPlan(t *testing.T) {
	for _, tc := range []struct {
		name      string
		out       string
		wantPlan  string
		wantFound bool
	}{
		{
			name:      "単一ブロック(前後空白は trim)",
			out:       "preamble\n<proposed_plan>\n  ## Plan\n- step 1\n</proposed_plan>\nepilogue",
			wantPlan:  "## Plan\n- step 1",
			wantFound: true,
		},
		{
			name:      "複数ブロックは最後の完全なブロックが勝つ",
			out:       "<proposed_plan>old plan</proposed_plan>\nrevise...\n<proposed_plan>new plan</proposed_plan>",
			wantPlan:  "new plan",
			wantFound: true,
		},
		{
			name: "最後の開きタグが未閉鎖でも直前の完全なブロックを返す",
			out:  "<proposed_plan>done plan</proposed_plan>\n<proposed_plan>still streaming",
			// LastIndex(close) は完結済みブロックの閉じタグを指し、その手前の
			// 開きタグと対になる。
			wantPlan:  "done plan",
			wantFound: true,
		},
		{
			name:      "開きタグのみ(閉じタグ未到達)は false",
			out:       "<proposed_plan>\nstreaming half a plan...",
			wantFound: false,
		},
		{
			name:      "タグなしは false",
			out:       "plain capture output without any tags",
			wantFound: false,
		},
		{
			name: "コードフェンス内のタグも本物と区別しない(既知の割り切り)",
			out:  "<proposed_plan>real plan\n```\n<proposed_plan>example</proposed_plan>\n```\ntail</proposed_plan>",
			// 素朴なテキスト検索なので、フェンス内の開きタグが「最後の閉じタグ
			// より前の最後の開きタグ」となり、そこから外側の閉じタグ直前までが
			// 返る。capture 出力に構造は無く、これ以上の解釈はしない。
			wantPlan:  "example</proposed_plan>\n```\ntail",
			wantFound: true,
		},
		{
			name:      "空文字列は false",
			out:       "",
			wantFound: false,
		},
		{
			name: "briefing 指示文のエコー(中身が ...)はスキップして前の実ブロックを返す",
			out:  "<proposed_plan>real plan</proposed_plan>\nwrapped in <proposed_plan>...</proposed_plan>.",
			// briefing は「wrapped in <proposed_plan>...</proposed_plan>」という
			// 指示文を含み、transcript にエコーされうる。中身 "..." は plan では
			// ないので後方走査でスキップする。
			wantPlan:  "real plan",
			wantFound: true,
		},
		{
			name:      "指示文のエコーしか無ければ false",
			out:       "wrapped in <proposed_plan>...</proposed_plan>.",
			wantFound: false,
		},
		{
			name:      "空白のみのブロックはスキップ(found:true で空 plan を返さない)",
			out:       "<proposed_plan>  \n</proposed_plan>",
			wantFound: false,
		},
		{
			name:      "空白のみのブロックの前に実ブロックがあればそれを返す",
			out:       "<proposed_plan>real</proposed_plan>\n<proposed_plan>\n</proposed_plan>",
			wantPlan:  "real",
			wantFound: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, found := extractLastPlan(tc.out)
			if found != tc.wantFound {
				t.Fatalf("found = %v want %v (plan %q)", found, tc.wantFound, plan)
			}
			if plan != tc.wantPlan {
				t.Fatalf("plan = %q want %q", plan, tc.wantPlan)
			}
		})
	}
}
