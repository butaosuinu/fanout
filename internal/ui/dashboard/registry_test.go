package dashboard

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/butaosuinu/fanout/internal/infra/atomicfs"
)

func TestDashboardRegistryLifecycle(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir, dirErr := DashboardRegistryDir()
	if dirErr != nil {
		t.Fatal(dirErr)
	}
	if entries, err := ListDashboards(); err != nil || len(entries) != 0 {
		t.Fatalf("missing registry = %v, %v", entries, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("listing created a registry: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != healthzPath || r.URL.RawQuery != "" {
			t.Errorf("unexpected probe: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	rf := RunFile{URL: srv.URL + "/?token=test-secret", Token: "test-secret", PID: os.Getpid(), StartedAt: "2026-10-17T00:00:00Z"}
	root := filepath.Join(t.TempDir(), "repo with spaces")
	unregister, err := RegisterDashboard(root, rf)
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	entries, err := ListDashboards()
	want := DashboardEntry{ProjectRoot: root, URL: srv.URL + "/", PID: rf.PID, StartedAt: rf.StartedAt}
	if err != nil || len(entries) != 1 || entries[0] != want {
		t.Fatalf("live entries = %v, %v; want %v", entries, err, want)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatalf("registry files = %v, %v", files, err)
	}
	path := filepath.Join(dir, files[0].Name())
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("registry mode = %v, %v", info, err)
	}
	body, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(body), "test-secret") || strings.Contains(string(body), "token") {
		t.Fatalf("registry contains a token or cannot be read: %v", err)
	}
	for range 2 {
		if err := unregister(); err != nil {
			t.Fatal(err)
		}
	}
	if entries, err := ListDashboards(); err != nil || len(entries) != 0 {
		t.Fatalf("after cleanup = %v, %v", entries, err)
	}
}

func TestDashboardRegistryConcurrentOwners(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	rf := RunFile{URL: srv.URL + "/", PID: os.Getpid(), StartedAt: "2026-10-17T00:00:00Z"}
	root := t.TempDir()
	const count = 12
	cleanups := make([]func() error, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Go(func() {
			var err error
			cleanups[i], err = RegisterDashboard(root, rf)
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}
	if entries, err := ListDashboards(); err != nil || len(entries) != count {
		t.Fatalf("concurrent registrations = %d, %v", len(entries), err)
	}
	// An old owner's repeated cleanup must leave every replacement registration.
	for i := range count {
		wg.Go(func() {
			old := cleanups[i]
			if err := old(); err != nil {
				t.Error(err)
			}
			var err error
			cleanups[i], err = RegisterDashboard(root, rf)
			if err != nil {
				t.Error(err)
			}
			if err := old(); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}
	if entries, err := ListDashboards(); err != nil || len(entries) != count {
		t.Fatalf("replacement registrations = %d, %v", len(entries), err)
	}
	for _, cleanup := range cleanups {
		wg.Go(func() {
			if err := cleanup(); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if entries, err := ListDashboards(); err != nil || len(entries) != 0 {
		t.Fatalf("concurrent cleanup = %v, %v", entries, err)
	}
}

func TestDashboardRegistryRejectsStaleAndInvalidEntries(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir, err := DashboardRegistryDir()
	if err != nil {
		t.Fatal(err)
	}
	var probes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		probes.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	dead := exec.Command("sh", "-c", "exit 0")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	base := DashboardEntry{ProjectRoot: t.TempDir(), URL: srv.URL + "/", PID: os.Getpid(), StartedAt: "2026-10-17T00:00:00Z"}
	cases := []struct {
		name string
		edit func(*DashboardEntry)
	}{
		{"dead", func(e *DashboardEntry) { e.PID = dead.Process.Pid }},
		{"missing-pid", func(e *DashboardEntry) { e.PID = 0 }},
		{"external", func(e *DashboardEntry) { e.URL = "http://example.invalid/" }},
		{"relative-root", func(e *DashboardEntry) { e.ProjectRoot = "relative" }},
		{"missing-time", func(e *DashboardEntry) { e.StartedAt = "" }},
		{"query", func(e *DashboardEntry) { e.URL += "?token=test-secret" }},
		{"fragment", func(e *DashboardEntry) { e.URL += "#test-secret" }},
		{"credentials", func(e *DashboardEntry) { e.URL = strings.Replace(e.URL, "://", "://user:password@", 1) }},
		{"bad-url", func(e *DashboardEntry) { e.URL = "http://%/" }},
	}
	for _, tc := range cases {
		entry := base
		tc.edit(&entry)
		if err := atomicfs.WriteJSON(filepath.Join(dir, tc.name+".json"), entry, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Ignore symlinks and atomic-write temporary files, even when their targets are live.
	target := filepath.Join(t.TempDir(), "target.json")
	if err := atomicfs.WriteJSON(target, base, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "linked.json")); err != nil {
		t.Fatal(err)
	}
	if err := atomicfs.WriteJSON(filepath.Join(dir, ".fanout-test.tmp"), base, 0o600); err != nil {
		t.Fatal(err)
	}
	if entries, err := ListDashboards(); err != nil || len(entries) != 0 || probes.Load() != 0 {
		t.Fatalf("invalid entries = %v, %v; probes=%d", entries, err, probes.Load())
	}
}

func TestDashboardRegistryWriteFailureAndRecovery(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir, dirErr := DashboardRegistryDir()
	if dirErr != nil {
		t.Fatal(dirErr)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	rf := RunFile{URL: "http://127.0.0.1:1/", PID: os.Getpid(), StartedAt: "2026-10-17T00:00:00Z"}
	if cleanup, err := RegisterDashboard(t.TempDir(), rf); err == nil || cleanup != nil {
		t.Fatal("registration must fail without a cleanup owner")
	}
	if _, err := ListDashboards(); err == nil {
		t.Fatal("unreadable registry must report an error")
	}
	if body, err := os.ReadFile(dir); err != nil || string(body) != "keep" {
		t.Fatalf("failed registration modified blocker: %v", err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	cleanup, err := RegisterDashboard(t.TempDir(), rf)
	if err != nil {
		t.Fatal(err)
	}
	if entries, err := ListDashboards(); err != nil || len(entries) != 0 {
		t.Fatalf("unhealthy endpoint = %v, %v", entries, err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
}

func TestDashboardRegistryRequiresAbsoluteConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "relative")
	if _, err := DashboardRegistryDir(); err == nil {
		t.Fatal("relative config directory accepted")
	}
	if cleanup, err := RegisterDashboard(t.TempDir(), RunFile{}); err == nil || cleanup != nil {
		t.Fatal("registration accepted relative config")
	}
	if _, err := ListDashboards(); err == nil {
		t.Fatal("listing accepted relative config")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for i, rf := range []RunFile{{URL: "http://%/", PID: os.Getpid()}, {URL: "http://example.invalid/", PID: os.Getpid()}, {URL: "http://127.0.0.1/"}} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			if cleanup, err := RegisterDashboard(t.TempDir(), rf); err == nil || cleanup != nil {
				t.Fatal("invalid registration accepted")
			}
		})
	}
}

func TestDashboardRegistryReportsUnreadableRecord(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read mode 000 files")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir, err := DashboardRegistryDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "unreadable.json")
	if err := atomicfs.WriteJSON(path, DashboardEntry{}, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0o600)
	if _, err := ListDashboards(); err == nil {
		t.Fatal("read failure must not be reported as an empty registry")
	}
}
