package herdrrun

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"
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
