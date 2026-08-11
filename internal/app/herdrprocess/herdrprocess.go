// Package herdrprocess verifies a live Herdr agent process against its saved launch argv.
package herdrprocess

import (
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/infra/codexapp"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
)

// Identity is the process portion of one persisted Herdr launch binding.
type Identity struct {
	WorktreePath string
	Executable   string
	Args         []string
	Agent        string
}

// VerifyAgent accepts the direct agent process or one exact interpreter root
// with exactly one matching native descendant in the foreground process group.
func VerifyAgent(info herdrrun.PaneProcessInfo, identity Identity) error {
	root, processes, ok := agentProcessRoot(info, identity)
	if !ok {
		return mismatchError()
	}
	if identity.Agent == "codex" && len(identity.Args) > 0 && identity.Args[0] == codexapp.PlanTUICommand {
		return verifyCodexPlanController(info, identity, root, processes)
	}
	if directAgentProcess(root, identity) {
		return nil
	}
	if !interpreterAgentProcess(root, identity) ||
		countAgentDescendants(info, identity, root, processes) != 1 {
		return mismatchError()
	}
	return nil
}

func verifyCodexPlanController(
	info herdrrun.PaneProcessInfo,
	identity Identity,
	root herdrrun.PaneProcess,
	processes map[int]herdrrun.PaneProcess,
) error {
	codexPath, ok := codexPlanExecutable(identity.Args)
	if !ok || !directAgentProcess(root, identity) ||
		countCodexPlanTUIRoots(info, identity.WorktreePath, codexPath, root, processes) != 1 {
		return mismatchError()
	}
	return nil
}

func codexPlanExecutable(args []string) (string, bool) {
	path := ""
	for index := 1; index < len(args); index++ {
		if args[index] != "--codex" {
			continue
		}
		if path != "" || index+1 >= len(args) {
			return "", false
		}
		path = args[index+1]
		index++
	}
	return path, filepath.IsAbs(path) && filepath.Clean(path) == path
}

func countCodexPlanTUIRoots(
	info herdrrun.PaneProcessInfo,
	worktreePath, codexPath string,
	root herdrrun.PaneProcess,
	processes map[int]herdrrun.PaneProcess,
) int {
	matches := 0
	for _, process := range processes {
		if validCodexPlanTUIChain(info, worktreePath, codexPath, root, process, processes) {
			matches++
		}
	}
	return matches
}

func validCodexPlanTUIChain(
	info herdrrun.PaneProcessInfo,
	worktreePath, codexPath string,
	root, process herdrrun.PaneProcess,
	processes map[int]herdrrun.PaneProcess,
) bool {
	if !codexPlanTUIRootCandidate(info, worktreePath, root, process, processes) {
		return false
	}
	args, direct, ok := codexPlanTUIArgs(process, codexPath)
	if !ok || !validCodexRemoteTUIArgs(args) {
		return false
	}
	nativeCount := countCodexNativeDescendants(info, worktreePath, process, args, processes)
	return direct && nativeCount == 0 || !direct && nativeCount == 1
}

func codexPlanTUIRootCandidate(
	info herdrrun.PaneProcessInfo,
	worktreePath string,
	root, process herdrrun.PaneProcess,
	processes map[int]herdrrun.PaneProcess,
) bool {
	return process.ParentPID == root.PID && process.CWD == worktreePath &&
		process.ProcessGroup == info.ForegroundProcessGroup &&
		processDescendsFrom(process.PID, root.PID, processes)
}

func codexPlanTUIArgs(process herdrrun.PaneProcess, codexPath string) ([]string, bool, bool) {
	if observedExecutableMatches(process.Executable, codexPath) && process.Argv0 == codexPath {
		return process.Argv, true, true
	}
	if observedExecutableMatches(process.Executable, process.Argv0) && process.Argv0 != codexPath &&
		len(process.Argv) > 0 && process.Argv[0] == codexPath {
		return process.Argv[1:], false, true
	}
	return nil, false, false
}

func validCodexRemoteTUIArgs(args []string) bool {
	if len(args) != 2 && len(args) != 4 || args[0] != "--remote" || !validCodexRemoteAddress(args[1]) {
		return false
	}
	return len(args) == 2 || args[2] == "resume" && strings.TrimSpace(args[3]) != ""
}

func validCodexRemoteAddress(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "ws" || parsed.Hostname() != "127.0.0.1" ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return false
	}
	port, err := strconv.Atoi(parsed.Port())
	return err == nil && port > 0 && port <= 65535
}

func countCodexNativeDescendants(
	info herdrrun.PaneProcessInfo,
	worktreePath string,
	root herdrrun.PaneProcess,
	args []string,
	processes map[int]herdrrun.PaneProcess,
) int {
	matches := 0
	for _, process := range processes {
		if process.PID != root.PID && process.CWD == worktreePath &&
			process.ProcessGroup == info.ForegroundProcessGroup &&
			observedExecutableMatches(process.Executable, process.Argv0) && slices.Equal(process.Argv, args) &&
			processDescendsFrom(process.PID, root.PID, processes) {
			matches++
		}
	}
	return matches
}

func mismatchError() error {
	return fmt.Errorf("herdr agent process identity does not match launch binding")
}

func agentProcessRoot(
	info herdrrun.PaneProcessInfo,
	identity Identity,
) (herdrrun.PaneProcess, map[int]herdrrun.PaneProcess, bool) {
	if identity.Executable == "" || info.ShellPID <= 1 || info.ForegroundProcessGroup <= 1 {
		return herdrrun.PaneProcess{}, nil, false
	}
	processes, ok := indexAgentProcesses(info.ForegroundProcesses)
	if !ok {
		return herdrrun.PaneProcess{}, nil, false
	}
	root, found := processes[info.ShellPID]
	valid := found && root.ProcessGroup == info.ForegroundProcessGroup &&
		root.CWD == identity.WorktreePath
	return root, processes, valid
}

func indexAgentProcesses(observed []herdrrun.PaneProcess) (map[int]herdrrun.PaneProcess, bool) {
	processes := make(map[int]herdrrun.PaneProcess, len(observed))
	for _, process := range observed {
		if !validObservedProcess(process) || processes[process.PID].PID != 0 {
			return nil, false
		}
		processes[process.PID] = process
	}
	return processes, true
}

func validObservedProcess(process herdrrun.PaneProcess) bool {
	return process.PID > 1 && process.ParentPID >= 0 &&
		process.ProcessGroup > 1 && process.Executable != ""
}

func directAgentProcess(process herdrrun.PaneProcess, identity Identity) bool {
	return observedExecutableMatches(process.Executable, identity.Executable) &&
		process.Argv0 == identity.Executable && slices.Equal(process.Argv, identity.Args)
}

func interpreterAgentProcess(process herdrrun.PaneProcess, identity Identity) bool {
	want := append([]string{identity.Executable}, identity.Args...)
	return observedExecutableMatches(process.Executable, process.Argv0) &&
		process.Argv0 != identity.Executable && slices.Equal(process.Argv, want)
}

func countAgentDescendants(
	info herdrrun.PaneProcessInfo,
	identity Identity,
	root herdrrun.PaneProcess,
	processes map[int]herdrrun.PaneProcess,
) int {
	matches := 0
	for _, process := range processes {
		if matchesAgentDescendant(info, identity, root, process, processes) {
			matches++
		}
	}
	return matches
}

func matchesAgentDescendant(
	info herdrrun.PaneProcessInfo,
	identity Identity,
	root, process herdrrun.PaneProcess,
	processes map[int]herdrrun.PaneProcess,
) bool {
	return process.PID != root.PID && process.CWD == identity.WorktreePath &&
		process.ProcessGroup == info.ForegroundProcessGroup &&
		observedExecutableMatches(process.Executable, process.Argv0) &&
		slices.Equal(process.Argv, identity.Args) &&
		processDescendsFrom(process.PID, root.PID, processes)
}

func observedExecutableMatches(observed, argv0 string) bool {
	// ps comm is an OS-observed executable name on macOS, not an absolute path.
	return filepath.Base(observed) == filepath.Base(argv0)
}

func processDescendsFrom(pid, rootPID int, processes map[int]herdrrun.PaneProcess) bool {
	seen := map[int]bool{}
	for pid != rootPID {
		if pid <= 1 || seen[pid] {
			return false
		}
		seen[pid] = true
		process, found := processes[pid]
		if !found {
			return false
		}
		pid = process.ParentPID
	}
	return true
}
