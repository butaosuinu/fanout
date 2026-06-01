// Package popup intercepts dmux's `tmux display-popup` driven prompts.
// See fanout:851-956 and the README "Why this looks weird" section.
//
// The flow:
//
//  1. Snapshot the set of dmux popup PIDs as a baseline.
//  2. Send Esc + n at the dmux control pane.
//  3. Watch for a NEW popup PID matching `<pattern>` (e.g. newPanePopup\.js).
//  4. Read its argv via `ps -o args=`, extract the resultFile path.
//  5. Atomically write {"success":true,"data":...} to that file.
//  6. Kill the popup process. dmux's parent reads the file we wrote.
//
// dmux always launches an agent-choice popup after the new-pane popup closes
// with a non-empty prompt, even when only one agent is enabled. fanout drives
// both popups in sequence.
package popup

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Patterns matched against `pgrep -f` argv. These are dmux-internal popup
// script names, not public API — when bumping dmux, re-verify against
// dist/components/popups/.
const (
	AnyPopupPattern    = `/popups/.*Popup\.js`
	NewPanePattern     = `newPanePopup\.js`
	AgentChoicePattern = `agentChoicePopup\.js`
)

// Field order matches dmux's PopupWrapper.writeSuccessAndExit (success first,
// data second). encoding/json marshals struct fields in declaration order, so
// don't reorder these without regenerating Tier 2 goldens.

type stringPayload struct {
	Success bool   `json:"success"`
	Data    string `json:"data"`
}

type newPaneInput struct {
	Prompt     string `json:"prompt"`
	BranchName string `json:"branchName"`
}

type objectPayload struct {
	Success bool         `json:"success"`
	Data    newPaneInput `json:"data"`
}

type stringsPayload struct {
	Success bool     `json:"success"`
	Data    []string `json:"data"`
}

// MakeNewPanePayload returns the bytes fanout writes to the newPanePopup
// resultFile.
func MakeNewPanePayload(prompt, branchName string) ([]byte, error) {
	if branchName != "" {
		return json.Marshal(objectPayload{Success: true, Data: newPaneInput{Prompt: prompt, BranchName: branchName}})
	}
	return json.Marshal(stringPayload{Success: true, Data: prompt})
}

// MakeAgentPayload returns the bytes fanout writes to the agentChoicePopup
// resultFile (a single-element list).
func MakeAgentPayload(agent string) ([]byte, error) {
	return json.Marshal(stringsPayload{Success: true, Data: []string{agent}})
}

// BaselinePIDs captures the PIDs of any dmux popup currently running so
// FindNew() can tell new popups from leftovers.
func BaselinePIDs() (map[int]bool, error) {
	pids, err := pgrepF(AnyPopupPattern)
	if err != nil {
		// pgrep returns 1 with empty output when nothing matches — treat
		// that as "no baseline" rather than an error. exec.ExitError is the
		// only way to detect this short of inspecting the exit code.
		if _, ok := err.(*exec.ExitError); ok {
			return map[int]bool{}, nil
		}
		return nil, err
	}
	out := make(map[int]bool, len(pids))
	for _, p := range pids {
		out[p] = true
	}
	return out, nil
}

// FindNew polls every 100ms for a new popup matching pattern. Returns the
// popup's PID and resultFile path, or an error after maxWait.
func FindNew(pattern string, baseline map[int]bool, maxWait time.Duration) (int, string, error) {
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		pids, err := pgrepF(pattern)
		if err != nil {
			if _, ok := err.(*exec.ExitError); !ok {
				return 0, "", err
			}
			pids = nil
		}
		for _, pid := range pids {
			if baseline[pid] {
				continue
			}
			comm, err := psField(pid, "comm")
			if err != nil {
				continue
			}
			// macOS BSD ps returns the full path; procps-ng returns the
			// basename. Normalize so all of /usr/local/bin/node, node,
			// node24 match.
			base := filepath.Base(strings.TrimSpace(comm))
			if !strings.HasPrefix(base, "node") {
				continue
			}
			args, err := psField(pid, "args")
			if err != nil {
				continue
			}
			rf := resultFileRE.FindString(args)
			if rf != "" {
				return pid, rf, nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return 0, "", fmt.Errorf("popup did not appear within %s", maxWait)
}

// WriteResult atomically writes payload to path. dmux reads the file only
// after its child process exits, so as long as the rename happens before the
// kill, the read sees the complete payload.
func WriteResult(path string, payload []byte) error {
	tmp := path + ".fanout.tmp"
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Intercept performs the full sequence for one popup.
func Intercept(pattern string, baseline map[int]bool, payload []byte, label string, maxWait time.Duration) error {
	return InterceptWithDebug(pattern, baseline, payload, label, maxWait, nil)
}

// InterceptWithDebug performs the full sequence for one popup and emits
// detailed progress when debugf is non-nil.
func InterceptWithDebug(pattern string, baseline map[int]bool, payload []byte, label string, maxWait time.Duration, debugf func(format string, a ...any)) error {
	debugLabel := strings.TrimSpace(label)
	if debugf != nil {
		debugf("%s: waiting pattern=%s timeout=%s baseline_pids=%d payload=%s", debugLabel, pattern, maxWait, len(baseline), string(payload))
	}
	pid, rf, err := FindNew(pattern, baseline, maxWait)
	if err != nil {
		if debugf != nil {
			debugf("%s: popup lookup failed: %v", debugLabel, err)
		}
		return fmt.Errorf("%s %w", label, err)
	}
	if debugf != nil {
		debugf("%s: found pid=%d resultFile=%s", debugLabel, pid, rf)
	}
	// Give dmux's PopupWrapper time to mount and write its readyFile so the
	// parent's readyPromise resolves via the ready-file path rather than via
	// child.close after we kill. 150ms covers the React Ink useEffect tick.
	time.Sleep(150 * time.Millisecond)
	writeErr := WriteResult(rf, payload)
	if debugf != nil {
		if writeErr != nil {
			debugf("%s: write result failed: %v", debugLabel, writeErr)
		} else {
			debugf("%s: wrote resultFile=%s payload=%s", debugLabel, rf, string(payload))
		}
	}
	// Kill the popup even when the write failed: a doomed attempt must not
	// orphan the Node popup inside `tmux display-popup`, or the stale popup
	// blocks the next display-popup and freezes the dmux TUI. The shell's
	// intercept_popup is invoked as `intercept_popup ... || return 1`, which
	// suspends `set -euo pipefail` inside it, so it likewise always reaches
	// `kill "$pid"` (fanout:1754) after the write, regardless of write success.
	//
	// Kill right after the write so the pid is still this popup. There is an
	// inherent pgrep/ps/kill race (the pid could be reused between FindNew's ps
	// check and this Kill), but the window is microseconds: dmux's
	// PopupWrapper.writeSuccessAndExit exits promptly once it detects the result
	// file, so the pid is almost always still that short-lived Node popup. A
	// missed/reused pid is treated as non-fatal.
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		// Process may have already exited; not fatal.
		if debugf != nil {
			debugf("%s: SIGTERM pid=%d failed: %v", debugLabel, pid, err)
		}
	} else if debugf != nil {
		debugf("%s: sent SIGTERM pid=%d", debugLabel, pid)
	}
	if writeErr != nil {
		return fmt.Errorf("%s write result: %w", label, writeErr)
	}
	return nil
}

// resultFileRE extracts the popup's resultFile path from its argv. The
// character class permits '.' and '-', so a path containing ".." (e.g.
// /tmp/../etc/dmux-popup-1.json) would match and be written unvalidated. This
// is intentionally byte-identical to the shell's grep -oE pattern
// (fanout:1688): the path originates from dmux's own popup process argv, not
// from user input, and dmux builds it with path.join under its tmpdir, so no
// traversal sequence is ever produced. Tightening the regex (rejecting "..")
// only in Go would diverge from the shell source-of-truth without closing a
// real attack surface; keep the patterns in lockstep.
var resultFileRE = regexp.MustCompile(`[[:alnum:]/_.\-]+/dmux-popup-[0-9]+\.json`)

func pgrepF(pattern string) ([]int, error) {
	out, err := exec.Command("pgrep", "-f", pattern).Output()
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		p, err := strconv.Atoi(line)
		if err != nil {
			continue
		}
		pids = append(pids, p)
	}
	return pids, nil
}

func psField(pid int, field string) (string, error) {
	out, err := exec.Command("ps", "-o", field+"=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
