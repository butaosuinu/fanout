package dashboard

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/ghissue"
)

type fakeGH struct{}

func (fakeGH) IssuePRs(num int) (string, []ghissue.PRRef, error) { return "OPEN", nil, nil }

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

func TestIndexServedAtRoot(t *testing.T) {
	srv := newTestServer(t, "")
	resp, err := http.Get(srv.base + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "fanout dashboard") {
		t.Fatalf("index missing title: %s", body)
	}
}
