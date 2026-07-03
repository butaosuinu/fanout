package main

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/butaosuinu/fanout/internal/agent"
	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/hooks"
	"github.com/butaosuinu/fanout/internal/settings"
	"github.com/butaosuinu/fanout/internal/state"
	fanouttui "github.com/butaosuinu/fanout/internal/tui"
	"github.com/butaosuinu/fanout/internal/watch"
)

// newTUIListOpenIssuesFunc lists OPEN issues for the new-session picker and
// marks the ones that already have a recorded fanout pane.
func newTUIListOpenIssuesFunc(projectRoot string) func() ([]fanouttui.IssueListItem, error) {
	return func() ([]fanouttui.IssueListItem, error) {
		gh := ghissue.Runner{Cwd: projectRoot}
		issues, err := gh.ListOpenIssues()
		if err != nil {
			return nil, err
		}
		recorded := recordedIssueNumbers(projectRoot)
		items := make([]fanouttui.IssueListItem, 0, len(issues))
		for _, issue := range issues {
			labels := make([]string, 0, len(issue.Labels))
			for _, label := range issue.Labels {
				labels = append(labels, label.Name)
			}
			items = append(items, fanouttui.IssueListItem{
				Number:     issue.Number,
				Title:      issue.Title,
				Labels:     labels,
				HasSession: recorded[issue.Number],
			})
		}
		return items, nil
	}
}

// recordedIssueNumbers reads state without locking (a display-only read) and
// returns the issue numbers recorded as a pane or as a fan-out parent. A read
// failure degrades to "no session hints" rather than failing the picker.
func recordedIssueNumbers(projectRoot string) map[int]bool {
	store, err := state.LoadProject(projectRoot)
	if err != nil {
		return nil
	}
	recorded := map[int]bool{}
	for _, pane := range store.Panes {
		if pane.IssueNum > 0 {
			recorded[pane.IssueNum] = true
		}
		if parent, err := strconv.Atoi(pane.Parent); err == nil && parent > 0 {
			recorded[parent] = true
		}
	}
	return recorded
}

// newTUIListIssueChildrenFunc lists the OPEN children the agent-assignment
// step offers. It reuses the watcher's child union (sub-issues + parent body
// task list) but skips per-child blocker hydration: an override for a child
// that ends up deferred is harmless, so the list does not need launch-exact
// filtering.
func newTUIListIssueChildrenFunc(projectRoot string) func(parent int) ([]fanouttui.ChildTarget, error) {
	return func(parent int) ([]fanouttui.ChildTarget, error) {
		gh := ghissue.Runner{Cwd: projectRoot}
		loaded, err := loadWatchParentChildren(gh, parent)
		if err != nil {
			return nil, err
		}
		open := openIssues(loaded.Children)
		targets := make([]fanouttui.ChildTarget, 0, len(open))
		for _, child := range open {
			targets = append(targets, fanouttui.ChildTarget{
				Number: child.Number,
				Title:  child.Title,
				Wave:   child.Wave,
			})
		}
		return targets, nil
	}
}

func newTUIIssueLaunchFunc(projectRoot, session, commandName string, resolvedSettings settings.Settings, hookConfig hooks.Config) fanouttui.IssueLaunchFunc {
	return func(issueNum int, defaultAgent string, overrides map[string]string) (string, error) {
		return launchIssueSessionFromTUI(projectRoot, session, commandName, resolvedSettings, hookConfig, issueNum, defaultAgent, overrides)
	}
}

// launchIssueSessionFromTUI starts a session for one issue picked in the TUI:
// a full fan-out when the issue has OPEN children (the watcher's parent lane),
// a single pane otherwise (the watcher's standalone lane).
func launchIssueSessionFromTUI(projectRoot, session, commandName string, resolvedSettings settings.Settings, hookConfig hooks.Config, issueNum int, defaultAgent string, overrides map[string]string) (string, error) {
	if issueNum <= 0 {
		return "", fmt.Errorf("issue number is required")
	}
	if err := validateTUIAgentSelection(defaultAgent, overrides); err != nil {
		return "", err
	}
	gh := ghissue.Runner{Cwd: projectRoot}
	// Re-fetch the issue: the picker list may be stale, and the detail carries
	// the body the standalone briefing needs.
	detail, err := gh.IssueDetail(issueNum)
	if err != nil {
		return "", err
	}
	if detail.State != "OPEN" {
		return "", fmt.Errorf("issue #%d is not OPEN", issueNum)
	}
	openChildren, err := countOpenChildTargets(gh, issueNum)
	if err != nil {
		return "", err
	}
	cfg := tuiIssueLaunchConfig(issueNum, defaultAgent, overrides)
	if openChildren == 0 {
		if launchErr := launchStandaloneIssuePane(projectRoot, session, commandName, cfg, resolvedSettings, hookConfig, detail); launchErr != nil {
			if errors.Is(launchErr, watch.ErrAlreadyFanned) {
				return "", fmt.Errorf("issue #%d already has a fanout pane", issueNum)
			}
			return "", launchErr
		}
		return fmt.Sprintf("started session for #%d", issueNum), nil
	}
	before := recordedPaneCountForParent(projectRoot, cfg.ParentRef)
	result, err := launchParentIssueFanout(projectRoot, session, commandName, cfg)
	if err != nil {
		return "", err
	}
	created := recordedPaneCountForParent(projectRoot, cfg.ParentRef) - before
	notice := fmt.Sprintf("fanned out #%d: created %d pane(s)", issueNum, created)
	if created <= 0 {
		notice = fmt.Sprintf("#%d: no new panes (children already have one)", issueNum)
	}
	if result.Deferred {
		notice += "; blocked/deferred children remain - re-select the issue later"
	}
	return notice, nil
}

// recordedPaneCountForParent counts state rows under one fan-out parent; the
// before/after difference is the created-pane count runWithRuntime does not
// return. A read failure degrades to 0 rather than failing the launch report.
func recordedPaneCountForParent(projectRoot, parentRef string) int {
	store, err := state.LoadProject(projectRoot)
	if err != nil {
		return 0
	}
	return len(store.FannedNumbersForParent(parentRef))
}

// validateTUIAgentSelection rejects unknown agent names up front so a typo
// surfaces as one clear error. Installation is checked only for the default
// agent: an override may sit on a blocked/deferred target the launch skips,
// and the launch lanes (validateIssueAgents / validateTaskAgents) already
// install-check the agents of the targets they actually launch.
func validateTUIAgentSelection(defaultAgent string, overrides map[string]string) error {
	if err := agent.ValidateKnown(defaultAgent); err != nil {
		return err
	}
	if err := agent.ValidateInstalled(defaultAgent); err != nil {
		return err
	}
	seen := map[string]bool{defaultAgent: true}
	for _, name := range overrides {
		if seen[name] {
			continue
		}
		seen[name] = true
		if err := agent.ValidateKnown(name); err != nil {
			return err
		}
	}
	return nil
}

// tuiIssueLaunchConfig mirrors newWatchLaunchConfig but carries the user's
// agent selection. UnblockedOnly stays true: the picker shows no blocker
// info, and launchParentIssueFanout's deferred re-detection assumes it.
func tuiIssueLaunchConfig(issueNum int, defaultAgent string, overrides map[string]string) *cliflags.Config {
	cfg := &cliflags.Config{
		Parent:          issueNum,
		ParentRef:       strconv.Itoa(issueNum),
		ParentMode:      cliflags.ModeIssue,
		Agent:           defaultAgent,
		SleepBetween:    cliflags.DefaultSleepBetween,
		PopupTimeoutSec: cliflags.DefaultPopupTimeout,
		ProjectStatus:   cliflags.DefaultProjectStatus,
		Format:          cliflags.DefaultFormat,
		UnblockedOnly:   true,
	}
	for target, name := range overrides {
		cfg.AgentOverrides = cliflags.UpsertAgentOverride(cfg.AgentOverrides, target, name)
	}
	return cfg
}
