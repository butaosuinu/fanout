// Package herdrprocess verifies a live Herdr agent process against its saved launch argv.
package herdrprocess

import (
	"fmt"
	"slices"

	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
)

// Identity is the process portion of one persisted Herdr launch binding.
type Identity struct {
	WorktreePath string
	Executable   string
	Args         []string
}

// VerifyAgent accepts the direct agent process or one exact interpreter root
// with exactly one matching native descendant in the foreground process group.
func VerifyAgent(info herdrrun.PaneProcessInfo, identity Identity) error {
	root, processes, ok := agentProcessRoot(info, identity)
	if !ok {
		return mismatchError()
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
	return process.Argv0 == identity.Executable && slices.Equal(process.Argv, identity.Args)
}

func interpreterAgentProcess(process herdrrun.PaneProcess, identity Identity) bool {
	want := append([]string{identity.Executable}, identity.Args...)
	return process.Argv0 != identity.Executable && slices.Equal(process.Argv, want)
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
		slices.Equal(process.Argv, identity.Args) &&
		processDescendsFrom(process.PID, root.PID, processes)
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
