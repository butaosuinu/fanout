package tmuxrun

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
)

const (
	// roleOption marks a pane's role for the auto-layout orchestrator. The TUI
	// console pane is stamped "console" so it can be reserved as a sidebar.
	roleOption = "@fanout_role"
	// spacerOption marks a filler pane the orchestrator inserts to keep grid
	// panes from stretching past a comfortable width. Spacers are never recorded
	// in state.json and are reconciled away on every relayout.
	spacerOption = "@fanout_spacer"
	// RoleConsole is the roleOption value for the resident TUI console pane.
	RoleConsole = corebackend.RoleConsole

	windowPaneFormat = "#{pane_id}\t#{pane_index}\t#{pane_active}\t#{" + roleOption + "}\t#{" + spacerOption + "}"
	windowGeomFormat = "#{window_id}\t#{window_width}\t#{window_height}"
)

type (
	Geometry   = corebackend.Geometry
	WindowPane = corebackend.WindowPane
)

// WindowGeometry resolves target (a pane id, window id, or session name) to the
// window that holds it and returns that window's id and interior size. Callers
// use the returned WindowID as the canonical handle for the rest of a relayout.
func WindowGeometry(target string) (Geometry, error) {
	target = strings.TrimSpace(target)
	args := []string{"display-message", "-p"}
	if target != "" {
		args = append(args, "-t", target)
	}
	args = append(args, "-F", windowGeomFormat)
	out, err := exec.Command("tmux", args...).Output()
	if err != nil {
		return Geometry{}, fmt.Errorf("tmux display-message window geometry: %w", err)
	}
	fields := strings.Split(strings.TrimRight(string(out), "\r\n"), "\t")
	if len(fields) != 3 {
		return Geometry{}, fmt.Errorf("parse tmux window geometry: expected 3 fields, got %d", len(fields))
	}
	width, err := strconv.Atoi(fields[1])
	if err != nil {
		return Geometry{}, fmt.Errorf("parse tmux window width: %w", err)
	}
	height, err := strconv.Atoi(fields[2])
	if err != nil {
		return Geometry{}, fmt.Errorf("parse tmux window height: %w", err)
	}
	id := strings.TrimSpace(fields[0])
	if id == "" {
		return Geometry{}, fmt.Errorf("tmux did not report a window id")
	}
	return Geometry{WindowID: id, Width: width, Height: height}, nil
}

// WindowOfPane returns the window id that contains paneID. Lifecycle captures
// this before killing a pane so it can relayout the surviving window.
func WindowOfPane(paneID string) (string, error) {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return "", fmt.Errorf("pane id is required")
	}
	out, err := exec.Command("tmux", "display-message", "-p", "-t", paneID, "#{window_id}").Output()
	if err != nil {
		return "", fmt.Errorf("tmux display-message window_id: %w", err)
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", fmt.Errorf("tmux did not report a window id for pane %s", paneID)
	}
	return id, nil
}

// WindowPanes lists the panes of one window with their auto-layout roles. It is
// window-scoped on purpose: the dashboard's all-sessions ListLivePanes sweep
// and its injection-defense join stay untouched.
func WindowPanes(windowTarget string) ([]WindowPane, error) {
	windowTarget = strings.TrimSpace(windowTarget)
	args := []string{"list-panes"}
	if windowTarget != "" {
		args = append(args, "-t", windowTarget)
	}
	args = append(args, "-F", windowPaneFormat)
	out, err := exec.Command("tmux", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("tmux list-panes window: %w", err)
	}
	var panes []WindowPane
	for lineNum, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 5)
		if len(fields) != 5 {
			return nil, fmt.Errorf("parse tmux window pane line %d: expected 5 fields, got %d", lineNum+1, len(fields))
		}
		id := strings.TrimSpace(fields[0])
		if !paneIDPattern.MatchString(id) {
			return nil, fmt.Errorf("parse tmux window pane line %d: malformed pane id %q", lineNum+1, id)
		}
		index, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, fmt.Errorf("parse tmux window pane line %d index: %w", lineNum+1, err)
		}
		active, err := parsePaneActive(fields[2])
		if err != nil {
			return nil, fmt.Errorf("parse tmux window pane line %d active: %w", lineNum+1, err)
		}
		panes = append(panes, WindowPane{
			ID:        id,
			NumericID: strings.TrimPrefix(id, "%"),
			Index:     index,
			Active:    active,
			Role:      strings.TrimSpace(fields[3]),
			Spacer:    strings.TrimSpace(fields[4]) == "1",
		})
	}
	return panes, nil
}

// ApplyLayout applies a custom tmux layout string (checksum,WxH,...) to target.
// The layout string is passed as a single argv element, so its commas and
// brackets are never word-split or shell-interpreted.
func ApplyLayout(target, layoutString string) error {
	if strings.TrimSpace(layoutString) == "" {
		return fmt.Errorf("layout string is required")
	}
	args := []string{"select-layout"}
	if strings.TrimSpace(target) != "" {
		args = append(args, "-t", target)
	}
	args = append(args, layoutString)
	if err := exec.Command("tmux", args...).Run(); err != nil {
		return fmt.Errorf("tmux select-layout custom: %w", err)
	}
	return nil
}

// SelectMainVertical is the coarse fallback when a custom layout is rejected: a
// fixed-width main pane on the left with the rest stacked on the right. The
// sidebar's grid fidelity is lost but the window stays usable.
func SelectMainVertical(target string, mainPaneWidth int) error {
	if mainPaneWidth > 0 {
		args := []string{"set-window-option"}
		if strings.TrimSpace(target) != "" {
			args = append(args, "-t", target)
		}
		args = append(args, "main-pane-width", strconv.Itoa(mainPaneWidth))
		if err := exec.Command("tmux", args...).Run(); err != nil {
			return fmt.Errorf("tmux set-window-option main-pane-width: %w", err)
		}
	}
	args := []string{"select-layout"}
	if strings.TrimSpace(target) != "" {
		args = append(args, "-t", target)
	}
	args = append(args, "main-vertical")
	if err := exec.Command("tmux", args...).Run(); err != nil {
		return fmt.Errorf("tmux select-layout main-vertical: %w", err)
	}
	return nil
}

// SetPaneRole stamps a pane's auto-layout role (e.g. RoleConsole). Pass an empty
// role to clear it (so a post-TUI shell is not mistaken for a console sidebar).
func SetPaneRole(paneID, role string) error {
	if strings.TrimSpace(paneID) == "" {
		return fmt.Errorf("pane id is required")
	}
	if strings.TrimSpace(role) == "" {
		if err := exec.Command("tmux", "set-option", "-pu", "-t", paneID, roleOption).Run(); err != nil {
			return fmt.Errorf("tmux set-option -u %s: %w", roleOption, err)
		}
		return nil
	}
	if err := exec.Command("tmux", "set-option", "-p", "-t", paneID, roleOption, role).Run(); err != nil {
		return fmt.Errorf("tmux set-option %s: %w", roleOption, err)
	}
	return nil
}

// SplitSpacerPane creates a blank filler pane in windowTarget, marks it with
// spacerOption, and returns its pane id. The marker is stamped from the parent
// using the explicit pane id split-window returned, so there is no active-pane
// race and the next relayout can immediately reconcile the spacer. The pane
// idles on a cleared screen until KillPane removes it.
//
// The marker is load-bearing: an unmarked filler pane would be misclassified as
// a real grid pane on the next relayout and never reconciled away. So if the
// marker cannot be set, the pane is killed and an error returned rather than
// leaving an orphaned blank pane behind.
func SplitSpacerPane(windowTarget string) (string, error) {
	args := []string{"split-window"}
	if strings.TrimSpace(windowTarget) != "" {
		args = append(args, "-t", windowTarget)
	}
	args = append(args, "-d", "-h", "-P", "-F", "#{pane_id}", spacerIdleCommand())
	out, err := exec.Command("tmux", args...).Output()
	if err != nil {
		return "", fmt.Errorf("tmux split-window spacer: %w", err)
	}
	paneID := strings.TrimSpace(string(out))
	if paneID == "" {
		return "", fmt.Errorf("tmux split-window spacer returned an empty pane id")
	}
	if err := exec.Command("tmux", "set-option", "-p", "-t", paneID, spacerOption, "1").Run(); err != nil {
		_ = KillPane(paneID) // don't leave an unmarked blank pane the grid would absorb
		return "", fmt.Errorf("tmux set-option %s: %w", spacerOption, err)
	}
	return paneID, nil
}

// spacerIdleCommand keeps a spacer pane visually blank: clear the screen, then
// block on cat so nothing renders and no shell prompt appears.
func spacerIdleCommand() string {
	return "exec /bin/sh -lc " + shellQuote("clear; exec cat")
}
