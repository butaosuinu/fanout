// Package displayname applies --name <NUM>=|<display-name> overrides post-
// pane-creation. Mirrors fanout:1039-1130.
//
// Two writes per issue:
//
//  1. dmux.config.json — panes[i].displayName for the [fanout #N]-tagged pane
//  2. <worktreePath>/.dmux/worktree-metadata.json — merge {"displayName":...}
//
// (1) takes effect within dmux's enforcePaneTitles loop (5-30s); (2) ensures
// reopenWorktree restores the displayName across dmux restarts. Either alone
// is volatile / delayed; doing both is required.
package displayname

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/butaosuinu/fanout/internal/atomicfs"
	"github.com/butaosuinu/fanout/internal/dmuxconfig"
	"github.com/butaosuinu/fanout/internal/log"
)

// LogFns is the indirection between this package and internal/log; we route
// status / warning / error messages through callbacks so the dry-run printer
// can keep its own writer.
type LogFns struct {
	Info func(format string, a ...any)
	Warn func(format string, a ...any)
	Dim  func(format string, a ...any)
	Err  func(format string, a ...any)
}

type Override struct {
	Num         int
	DisplayName string
}

// ApplyAll updates panes[].displayName and worktree-metadata.json for every
// override whose DisplayName is non-empty. Already-tracked panes that have
// not been (re)created in this run can still be updated, matching the bash
// behavior — apply_display_names looks panes up by [fanout #N] prompt.
//
// In dry-run mode the plan is printed unconditionally — no pane lookup is
// done, because for fresh runs the panes don't exist yet and the dry-run
// printer must reflect intent regardless.
func ApplyAll(configPath string, overrides []Override, dryRun bool, dryOut io.Writer, palette log.Palette, fns LogFns) {
	if len(overrides) == 0 {
		return
	}

	if dryRun {
		applied := 0
		for _, o := range overrides {
			if o.DisplayName == "" {
				continue
			}
			fmt.Fprintf(dryOut, "    %s# would set panes[].displayName for #%d = %q%s\n", palette.Dim, o.Num, o.DisplayName, palette.Reset)
			fmt.Fprintf(dryOut, "    %s# would merge displayName into <worktree>/.dmux/worktree-metadata.json%s\n", palette.Dim, palette.Reset)
			applied++
		}
		if applied > 0 {
			fns.Info("applied displayName for %d pane(s)", applied)
		}
		return
	}

	cfg, err := dmuxconfig.Load(configPath)
	if err != nil {
		fns.Warn("could not read dmux config for displayName: %v", err)
		return
	}

	applied := 0
	for _, o := range overrides {
		if o.DisplayName == "" {
			continue
		}
		slug, worktreePath := cfg.FindPaneByFanoutTag(o.Num)
		if slug == "" {
			fns.Warn("displayName for #%d: no pane with [fanout #%d] prompt found", o.Num, o.Num)
			continue
		}

		if err := dmuxconfig.SetDisplayNameByFanoutTag(configPath, o.Num, o.DisplayName); err != nil {
			fns.Warn("displayName for #%d: %v", o.Num, err)
			continue
		}
		// Worktree metadata is the persistence half (a) panes[].displayName is
		// the volatile half. We've already written (a); skip (b) only when the
		// worktreePath is missing, mirroring the bash `-n $path && -d $path`
		// guard. Without this check, an empty/stale path causes a metadata
		// file to be created somewhere unintended (cwd if path is "").
		if worktreePath == "" {
			fns.Warn("worktree-metadata.json for #%d: pane has no worktreePath; skipping persistence write", o.Num)
		} else if st, err := os.Stat(worktreePath); err != nil || !st.IsDir() {
			fns.Warn("worktree-metadata.json for #%d: worktreePath %s missing; skipping persistence write", o.Num, worktreePath)
		} else if err := mergeWorktreeMetadata(worktreePath, o.DisplayName); err != nil {
			fns.Warn("worktree-metadata.json for #%d: %v", o.Num, err)
			continue
		}
		applied++
	}
	if applied > 0 {
		fns.Info("applied displayName for %d pane(s)", applied)
	}
}

func mergeWorktreeMetadata(worktreePath, displayName string) error {
	dir := filepath.Join(worktreePath, ".dmux")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "worktree-metadata.json")
	m := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &m)
	} else if !os.IsNotExist(err) {
		return err
	}
	m["displayName"] = displayName
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return atomicfs.WriteFile(path, append(out, '\n'), 0o644)
}
