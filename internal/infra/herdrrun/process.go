package herdrrun

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
)

type paneProcessInspector func(context.Context, []PaneProcess) ([]PaneProcess, error)

var errPaneProcessChanged = errors.New("herdr pane process changed during identity inspection")

type processRelation struct {
	parentPID    int
	processGroup int
	executable   string
}

func (s *OwnedSession) inspectPaneProcesses(
	ctx context.Context,
	processes []PaneProcess,
) ([]PaneProcess, error) {
	inspect := inspectPaneProcessRelations
	if s.processInspector != nil {
		inspect = s.processInspector
	}
	return inspect(ctx, processes)
}

func inspectPaneProcessRelations(ctx context.Context, processes []PaneProcess) ([]PaneProcess, error) {
	pids, err := uniquePaneProcessIDs(processes)
	if err != nil {
		return nil, err
	}
	args := []string{"-p", strings.Join(pids, ","), "-o", "pid=,ppid=,pgid=,comm="}
	out, err := exec.CommandContext(ctx, "ps", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("inspect process table: %w", err)
	}
	relations, err := parseProcessRelations(string(out))
	if err != nil {
		return nil, err
	}
	return bindProcessRelations(processes, relations)
}

func uniquePaneProcessIDs(processes []PaneProcess) ([]string, error) {
	seen := map[int]bool{}
	pids := make([]string, 0, len(processes))
	for _, process := range processes {
		if process.PID <= 1 || seen[process.PID] {
			return nil, fmt.Errorf("herdr pane process list has an invalid or duplicate pid")
		}
		seen[process.PID] = true
		pids = append(pids, strconv.Itoa(process.PID))
	}
	if len(pids) == 0 {
		return nil, fmt.Errorf("herdr pane process list is empty")
	}
	return pids, nil
}

func parseProcessRelations(output string) (map[int]processRelation, error) {
	relations := map[int]processRelation{}
	for line := range strings.Lines(output) {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			return nil, fmt.Errorf("ps returned an invalid process relation")
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || relations[pid] != (processRelation{}) {
			return nil, fmt.Errorf("ps returned an invalid or duplicate pid")
		}
		parentPID, parentErr := strconv.Atoi(fields[1])
		processGroup, groupErr := strconv.Atoi(fields[2])
		if parentErr != nil || groupErr != nil || parentPID < 0 || processGroup <= 1 {
			return nil, fmt.Errorf("ps returned an invalid process relation")
		}
		relations[pid] = processRelation{
			parentPID: parentPID, processGroup: processGroup,
			executable: strings.Join(fields[3:], " "),
		}
	}
	return relations, nil
}

func bindProcessRelations(
	processes []PaneProcess,
	relations map[int]processRelation,
) ([]PaneProcess, error) {
	bound := slices.Clone(processes)
	for i := range bound {
		relation, found := relations[bound[i].PID]
		if !found || relation.executable == "" {
			return nil, fmt.Errorf("%w: pid %d disappeared", errPaneProcessChanged, bound[i].PID)
		}
		bound[i].ParentPID = relation.parentPID
		bound[i].ProcessGroup = relation.processGroup
		bound[i].Executable = relation.executable
	}
	return bound, nil
}

// MatchAgentProcess requires one exact direct or interpreter-backed agent
// process in the pane's current foreground process group.
func MatchAgentProcess(
	info PaneProcessInfo,
	executable string,
	args []string,
	cwd string,
) (corebackend.ProcessIdentity, error) {
	root, processes, ok := agentProcessRoot(info, cwd)
	if !ok {
		return corebackend.ProcessIdentity{}, fmt.Errorf("herdr agent process identity does not match saved launch")
	}
	if root.Argv0 == executable && slices.Equal(root.Argv, args) {
		return processIdentity(info, root.PID), nil
	}
	want := append([]string{executable}, args...)
	if root.Argv0 == executable || !slices.Equal(root.Argv, want) {
		return corebackend.ProcessIdentity{}, fmt.Errorf("herdr agent process identity does not match saved launch")
	}
	agentPID, matches := matchingAgentDescendant(info, root, processes, args, cwd)
	if matches != 1 {
		return corebackend.ProcessIdentity{}, fmt.Errorf("herdr agent process identity does not match saved launch")
	}
	return processIdentity(info, agentPID), nil
}

func agentProcessRoot(
	info PaneProcessInfo,
	cwd string,
) (PaneProcess, map[int]PaneProcess, bool) {
	if info.ShellPID <= 1 || info.ForegroundProcessGroup <= 1 {
		return PaneProcess{}, nil, false
	}
	processes, ok := indexAgentProcesses(info.ForegroundProcesses)
	if !ok {
		return PaneProcess{}, nil, false
	}
	root, found := processes[info.ShellPID]
	valid := found && root.ProcessGroup == info.ForegroundProcessGroup && root.CWD == cwd
	return root, processes, valid
}

func indexAgentProcesses(observed []PaneProcess) (map[int]PaneProcess, bool) {
	processes := make(map[int]PaneProcess, len(observed))
	for _, process := range observed {
		if !validAgentProcess(process) || processes[process.PID].PID != 0 {
			return nil, false
		}
		processes[process.PID] = process
	}
	return processes, true
}

func validAgentProcess(process PaneProcess) bool {
	return process.PID > 1 && process.ParentPID >= 0 &&
		process.ProcessGroup > 1 && process.Executable != ""
}

func matchingAgentDescendant(
	info PaneProcessInfo,
	root PaneProcess,
	processes map[int]PaneProcess,
	args []string,
	cwd string,
) (int, int) {
	matchedPID, matches := 0, 0
	for _, process := range processes {
		if process.PID != root.PID && process.CWD == cwd &&
			process.ProcessGroup == info.ForegroundProcessGroup &&
			slices.Equal(process.Argv, args) &&
			processDescendsFrom(process.PID, root.PID, processes) {
			matchedPID = process.PID
			matches++
		}
	}
	return matchedPID, matches
}

func processDescendsFrom(pid, rootPID int, processes map[int]PaneProcess) bool {
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

func processIdentity(info PaneProcessInfo, agentPID int) corebackend.ProcessIdentity {
	return corebackend.ProcessIdentity{
		ShellPID:               info.ShellPID,
		ForegroundProcessGroup: info.ForegroundProcessGroup,
		AgentPID:               agentPID,
	}
}
