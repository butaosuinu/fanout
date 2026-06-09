package dashboard

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestIsLiveProbesHealthzNotTokenizedRoot(t *testing.T) {
	// Only /healthz returns 200; everything else (including the tokenized root)
	// returns 500. A correct IsLive must strip the query and probe /healthz.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == healthzPath {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	rf := &RunFile{URL: srv.URL + "/?token=secret", PID: os.Getpid()}
	if !rf.IsLive() {
		t.Fatal("IsLive should probe /healthz (stripping the token query), got not-live")
	}
}

func TestRunFileRoundTrip(t *testing.T) {
	// Intentionally do NOT pre-create .fanout/ — WriteRunFile must create it so a
	// never-fanned repo can still record (and reuse) a running dashboard.
	root := t.TempDir()
	rf := RunFile{URL: "http://127.0.0.1:8787/", PID: os.Getpid(), Token: "abc", StartedAt: "2026-06-06T00:00:00Z"}
	if err := WriteRunFile(root, rf); err != nil {
		t.Fatalf("WriteRunFile: %v", err)
	}
	got, err := ReadRunFile(root)
	if err != nil {
		t.Fatalf("ReadRunFile: %v", err)
	}
	if got == nil || got.URL != rf.URL || got.PID != rf.PID || got.Token != rf.Token {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	// The run file holds the access token; it must not be world/group readable.
	info, err := os.Stat(RunFilePath(root))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("run file perm = %o, want 0600 (token must be private)", perm)
	}
	if err := RemoveRunFile(root); err != nil {
		t.Fatalf("RemoveRunFile: %v", err)
	}
	if got, _ := ReadRunFile(root); got != nil {
		t.Fatalf("run file should be gone, got %+v", got)
	}
}

func TestRemoveOwnRunFileLeavesAnotherProcessFile(t *testing.T) {
	root := t.TempDir()
	// A newer server (different pid/token) owns the run file.
	if err := WriteRunFile(root, RunFile{URL: "http://127.0.0.1:9/", PID: os.Getpid() + 1, Token: "newer"}); err != nil {
		t.Fatal(err)
	}
	// Our cleanup (our pid, our token) must not delete it.
	if err := RemoveOwnRunFile(root, "ours", os.Getpid()); err != nil {
		t.Fatalf("RemoveOwnRunFile: %v", err)
	}
	if got, _ := ReadRunFile(root); got == nil || got.Token != "newer" {
		t.Fatalf("a newer process's run file must survive, got %+v", got)
	}
}

func TestRemoveOwnRunFileRemovesOurFile(t *testing.T) {
	root := t.TempDir()
	if err := WriteRunFile(root, RunFile{URL: "http://127.0.0.1:9/", PID: os.Getpid(), Token: "ours"}); err != nil {
		t.Fatal(err)
	}
	if err := RemoveOwnRunFile(root, "ours", os.Getpid()); err != nil {
		t.Fatalf("RemoveOwnRunFile: %v", err)
	}
	if got, _ := ReadRunFile(root); got != nil {
		t.Fatalf("our own run file should be removed, got %+v", got)
	}
}

func TestReadRunFileMissingIsNil(t *testing.T) {
	got, err := ReadRunFile(t.TempDir())
	if err != nil || got != nil {
		t.Fatalf("missing run file = (%+v, %v), want (nil, nil)", got, err)
	}
}

func TestIsLiveFalseForDeadPID(t *testing.T) {
	// PID 0x7fffffff is almost certainly not a live process.
	rf := &RunFile{URL: "http://127.0.0.1:1/", PID: 0x7ffffffe}
	if rf.IsLive() {
		t.Fatal("IsLive should be false for a dead PID")
	}
}

func TestIsLiveFalseForEmptyURL(t *testing.T) {
	if (&RunFile{}).IsLive() {
		t.Fatal("IsLive should be false for empty URL")
	}
}

func TestIsLiveRejectsZeroPID(t *testing.T) {
	// A live, 200-answering loopback server is reachable, but the run file records
	// no PID (0). WriteRunFile always stores os.Getpid(), so a missing PID means
	// the file is stale/hand-crafted and must not be reused.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rf := &RunFile{URL: srv.URL + "/?token=x", PID: 0}
	if rf.IsLive() {
		t.Fatal("IsLive must reject a run file with no recorded PID")
	}
}

func TestIsLiveRejectsNonLoopbackURL(t *testing.T) {
	// A crafted run file (e.g. shipped in a cloned repo) points at a non-loopback
	// host. Even with a live PID, IsLive must reject it before probing so the
	// reuse path can never open an off-loopback URL. The loopback gate short-
	// circuits ahead of any network call, so example.com is never contacted.
	rf := &RunFile{URL: "http://example.com/?token=x", PID: os.Getpid()}
	if rf.IsLive() {
		t.Fatal("IsLive must reject a non-loopback URL")
	}
}
