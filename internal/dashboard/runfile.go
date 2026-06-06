package dashboard

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/butaosuinu/fanout/internal/atomicfs"
)

// runFileRelPath is the per-repo record of the currently running dashboard,
// used to reuse a live server instead of starting a second one.
const runFileRelPath = ".fanout/dashboard.json"

// RunFile records a live dashboard so a second `fanout dashboard` invocation
// (including repeated keybind presses) reuses it rather than spawning another.
type RunFile struct {
	URL       string `json:"url"`
	PID       int    `json:"pid"`
	Token     string `json:"token"`
	StartedAt string `json:"startedAt"`
}

// RunFilePath returns the dashboard run-file path for projectRoot.
func RunFilePath(projectRoot string) string {
	return filepath.Join(projectRoot, runFileRelPath)
}

// ReadRunFile loads the run file. A missing file returns (nil, nil).
func ReadRunFile(projectRoot string) (*RunFile, error) {
	data, err := os.ReadFile(RunFilePath(projectRoot))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rf RunFile
	if err := json.Unmarshal(data, &rf); err != nil {
		return nil, err
	}
	return &rf, nil
}

// WriteRunFile atomically records the running dashboard. It is written 0600:
// the file holds the access token, so it must not be readable by other local
// users — otherwise they could lift the token and call /api/* despite the gate.
func WriteRunFile(projectRoot string, rf RunFile) error {
	out, err := json.MarshalIndent(rf, "", "  ")
	if err != nil {
		return err
	}
	path := RunFilePath(projectRoot)
	// A repo that has never been fanned out has no .fanout/ dir yet; create it so
	// the atomic write (and thus reuse-if-running) does not fail with ENOENT.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return atomicfs.WriteFile(path, append(out, '\n'), 0o600)
}

// RemoveRunFile deletes the run file (best effort; missing is fine).
func RemoveRunFile(projectRoot string) error {
	err := os.Remove(RunFilePath(projectRoot))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// RemoveOwnRunFile deletes the run file only if it still records this process
// (matching pid, and token when one is set). A dashboard that shut down must not
// clobber a newer server's run file written after it stopped serving — doing so
// would leave the replacement running but undiscoverable, defeating reuse.
func RemoveOwnRunFile(projectRoot, token string, pid int) error {
	rf, err := ReadRunFile(projectRoot)
	if err != nil || rf == nil {
		return nil
	}
	if rf.PID != pid {
		return nil
	}
	if token != "" && rf.Token != token {
		return nil
	}
	return RemoveRunFile(projectRoot)
}

// IsLive reports whether the recorded server is still serving: it records a live
// PID, names a loopback HTTP endpoint, AND /healthz answers 200 within a short
// timeout. A stale run file (server died) returns false so the caller overwrites
// it and starts fresh.
//
// The PID and loopback checks are also a trust gate, not just liveness. The run
// file lives at .fanout/dashboard.json, which is only git-locally excluded — a
// cloned repo can ship one. WriteRunFile always stores os.Getpid() and a
// 127.0.0.1 URL, so a missing/dead PID or a non-loopback URL means the file is
// stale or hand-crafted; trusting it would let any host that answers 200 on
// /healthz make `fanout dashboard --open` navigate the browser off-loopback,
// breaking the 127.0.0.1-only invariant.
func (rf *RunFile) IsLive() bool {
	if rf == nil || rf.URL == "" {
		return false
	}
	if rf.PID <= 0 || !pidAlive(rf.PID) {
		return false
	}
	// rf.URL carries the token query (e.g. http://127.0.0.1:PORT/?token=...), so
	// concatenating healthzPath would hit /?token=.../healthz (i.e. the SPA root)
	// rather than the token-free /healthz probe. Rebuild the URL from scheme+host.
	u, err := url.Parse(rf.URL)
	if err != nil || u.Host == "" {
		return false
	}
	if !isLoopbackHTTP(u) {
		return false
	}
	healthURL := (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: healthzPath}).String()
	client := http.Client{Timeout: 300 * time.Millisecond}
	resp, err := client.Get(healthURL)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// isLoopbackHTTP reports whether u is a plain-HTTP URL whose host resolves to the
// loopback interface. The dashboard only ever binds 127.0.0.1, so the reuse path
// must refuse anything else rather than probe (and then open) an arbitrary host.
func isLoopbackHTTP(u *url.URL) bool {
	if u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func pidAlive(pid int) bool {
	// signal 0 probes for existence without delivering a signal.
	return syscall.Kill(pid, 0) == nil
}

// LockStartup takes an exclusive lock that serializes the reuse-check → bind →
// run-file-write critical section, so two near-simultaneous launches (e.g. a
// double `prefix + D` press) cannot both pass the "no server running" check and
// each bind a fresh ephemeral port, leaving duplicate servers. Release it with
// UnlockStartup once the run file is written and the listener is bound.
func LockStartup(projectRoot string) (*os.File, error) {
	lockPath := RunFilePath(projectRoot) + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// UnlockStartup releases a LockStartup lock. Safe to call with nil.
func UnlockStartup(f *os.File) {
	if f == nil {
		return
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}
