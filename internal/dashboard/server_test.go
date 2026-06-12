package dashboard

import (
	"context"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/sessionview"
)

type fakeGH struct{}

func (fakeGH) IssuePRs(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil }

func (fakeGH) Waves(parent string, recordedNums []int) (map[int]sessionview.WaveInfo, error) {
	return nil, nil
}

// newTestServer binds an ephemeral server in a temp project root with no state
// file (empty snapshot) and runs it until the test ends.
func newTestServer(t *testing.T, token string) *Server {
	t.Helper()
	root := t.TempDir()
	srv, err := New(Options{
		ProjectRoot: root,
		Port:        0,
		Token:       token,
		ResolveGH:   func() (string, GHProvider, error) { return "o/n", fakeGH{}, nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go srv.Run(ctx)
	t.Cleanup(cancel)
	waitReady(t, srv.base)
	return srv
}

func waitReady(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + healthzPath)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server did not become ready")
}

func TestHealthzOK(t *testing.T) {
	srv := newTestServer(t, "")
	resp, err := http.Get(srv.base + healthzPath)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d", resp.StatusCode)
	}
}

func TestSnapshotEndpointReturnsJSON(t *testing.T) {
	srv := newTestServer(t, "")
	resp, err := http.Get(srv.base + "/api/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("snapshot status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type = %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"sessions"`) {
		t.Fatalf("snapshot body missing sessions: %s", body)
	}
}

func TestTokenGate(t *testing.T) {
	srv := newTestServer(t, "secrettoken")

	// no token -> 403
	resp, err := http.Get(srv.base + "/api/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no-token status = %d want 403", resp.StatusCode)
	}

	// wrong token -> 403
	resp, _ = http.Get(srv.base + "/api/snapshot?token=nope")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong-token status = %d want 403", resp.StatusCode)
	}

	// correct token -> 200
	resp, _ = http.Get(srv.base + "/api/snapshot?token=secrettoken")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("good-token status = %d want 200", resp.StatusCode)
	}

	// HTML shell stays token-free
	resp, _ = http.Get(srv.base + "/")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("root status = %d want 200", resp.StatusCode)
	}
}

func TestNonGetIs405(t *testing.T) {
	srv := newTestServer(t, "")
	resp, err := http.Post(srv.base+"/api/snapshot", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d want 405", resp.StatusCode)
	}
}

func TestStreamEmitsInitialFrame(t *testing.T) {
	srv := newTestServer(t, "")
	req, _ := http.NewRequest(http.MethodGet, srv.base+"/api/stream", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	buf := make([]byte, 256)
	n, _ := resp.Body.Read(buf)
	frame := string(buf[:n])
	if !strings.Contains(frame, "event: snapshot") || !strings.Contains(frame, "data:") {
		t.Fatalf("first SSE frame = %q", frame)
	}
}

func TestStreamHeadReturnsWithoutSubscribing(t *testing.T) {
	srv := newTestServer(t, "")
	req, _ := http.NewRequest(http.MethodHead, srv.base+"/api/stream", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req = req.WithContext(ctx)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HEAD /api/stream: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD /api/stream status = %d want 200", resp.StatusCode)
	}
	// A HEAD probe must not enter the streaming loop; if it did, the handler would
	// block holding a hub subscription until the client times out.
	time.Sleep(50 * time.Millisecond)
	if n := srv.hub.subscriberCount(); n != 0 {
		t.Fatalf("HEAD left %d subscriber(s) parked in the stream loop", n)
	}
}

// assetsBuilt reports whether the Vite bundle is present in the embedded FS.
// A fresh checkout tracks only static/.gitkeep, so asset-dependent tests skip
// themselves; CI runs `make build-web` first so they actually execute there.
func assetsBuilt(t *testing.T) bool {
	t.Helper()
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		t.Fatalf("fs.Sub: %v", err)
	}
	_, err = fs.Stat(sub, "index.html")
	return err == nil
}

// TestIndexServedAtRoot passes in both build states: with the bundle embedded
// it sees the real SPA shell, without it the fallback page — both carry the
// "fanout dashboard" title marker, and both must be 200 + no-store.
func TestIndexServedAtRoot(t *testing.T) {
	srv := newTestServer(t, "")
	resp, err := http.Get(srv.base + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d want 200", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("GET / Cache-Control = %q want no-store", cc)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "fanout dashboard") {
		t.Fatalf("index missing title: %s", body)
	}
}

// TestUnbuiltAssetsServeFallbackOnly guards the no-bundle path: "/" gets the
// instruction page (not http.FileServer's directory listing, which would
// expose .gitkeep), and other paths 404.
func TestUnbuiltAssetsServeFallbackOnly(t *testing.T) {
	if assetsBuilt(t) {
		t.Skip("web assets are built; the fallback path is unreachable")
	}
	srv := newTestServer(t, "")

	resp, err := http.Get(srv.base + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "make build-web") {
		t.Fatalf("fallback page missing build hint: %s", body)
	}

	resp, err = http.Get(srv.base + "/.gitkeep")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /.gitkeep status = %d want 404", resp.StatusCode)
	}
}

// TestStaticAssetsSmoke is a regression guard for the go:embed wiring: it
// asserts the built SPA bundle is served at its stable paths (index.html
// referencing assets/app.js) and that the bundle carries markers the live UI
// depends on (the HUD running counter and the agentState wiring — string
// literals/property names that survive minification).
func TestStaticAssetsSmoke(t *testing.T) {
	if !assetsBuilt(t) {
		t.Skip("web assets not built; run `make build-web` (CI builds them before go-test)")
	}
	srv := newTestServer(t, "")

	resp, err := http.Get(srv.base + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d want 200", resp.StatusCode)
	}
	for _, marker := range []string{`id="root"`, "assets/app.js"} {
		if !strings.Contains(string(body), marker) {
			t.Fatalf("index missing marker %q: %s", marker, body)
		}
	}

	resp, err = http.Get(srv.base + "/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /assets/app.js status = %d want 200", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("GET /assets/app.js Cache-Control = %q want no-store", cc)
	}
	for _, marker := range []string{"s-running", "agentState"} {
		if !strings.Contains(string(body), marker) {
			t.Fatalf("app.js missing marker %q", marker)
		}
	}
}
