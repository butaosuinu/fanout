package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/app/run"
	"github.com/butaosuinu/fanout/internal/app/watch"
	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/settings"
	"github.com/butaosuinu/fanout/internal/infra/state"
	fanouttui "github.com/butaosuinu/fanout/internal/ui/tui"
)

var openTUIIssueBrowser = openBrowser

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
				Number:          issue.Number,
				Title:           issue.Title,
				Labels:          labels,
				HasSession:      recorded[issue.Number],
				HasParent:       issue.ParentNumber > 0,
				HasOpenChildren: issue.OpenSubIssueCount > 0,
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
		// Plan-lane rows (a coordinator, or the tasks it fanned out) reference
		// their issue only through slugs.
		if num, ok := panelaunch.PlanPaneIssueNum(pane); ok {
			recorded[num] = true
		}
		if num, ok := panelaunch.OrchestratorPaneIssueNum(pane); ok {
			recorded[num] = true
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
		open := run.OpenIssues(loaded.Children)
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
	return func(issueNum int, defaultAgent string, overrides map[string]string) (fanouttui.LaunchResult, error) {
		return launchIssueSessionFromTUI(projectRoot, session, commandName, resolvedSettings, hookConfig, issueNum, defaultAgent, overrides)
	}
}

func newTUIOpenIssueFunc(projectRoot string) fanouttui.IssueOpenFunc {
	return func(issueNum int) error {
		return openIssueFromTUI(projectRoot, issueNum)
	}
}

func openIssueFromTUI(projectRoot string, issueNum int) error {
	issueURL, err := issueURLFromRepo(projectRoot, issueNum)
	if err != nil {
		return err
	}
	if err := openTUIIssueBrowser(issueURL); err != nil {
		return fmt.Errorf("open issue #%d: %w", issueNum, err)
	}
	return nil
}

func issueURLFromRepo(projectRoot string, issueNum int) (string, error) {
	if issueNum <= 0 {
		return "", fmt.Errorf("issue number is required")
	}
	gh := ghissue.Runner{Cwd: projectRoot}
	nwo, err := gh.RepoNameWithOwner()
	if err != nil {
		return "", fmt.Errorf("resolve repo: %w", err)
	}
	owner, repo, ok := strings.Cut(nwo, "/")
	if !ok || !validGitHubPathPart(owner) || !validGitHubPathPart(repo) {
		return "", fmt.Errorf("unexpected repo nameWithOwner: %s", nwo)
	}
	return fmt.Sprintf("https://github.com/%s/%s/issues/%d", owner, repo, issueNum), nil
}

func validGitHubPathPart(part string) bool {
	if part == "" {
		return false
	}
	for _, r := range part {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.':
		default:
			return false
		}
	}
	return true
}

// launchIssueSessionFromTUI starts a session for one issue picked in the TUI:
// a project-root orchestrator followed by a full fan-out when the issue has
// OPEN children, or a single pane otherwise (the watcher's standalone lane).
//
//nolint:gocognit,gocyclo,funlen // The transaction keeps standalone, parent fan-out, rollback, and start-gate outcomes ordered.
func launchIssueSessionFromTUI(projectRoot, session, commandName string, resolvedSettings settings.Settings, hookConfig hooks.Config, issueNum int, defaultAgent string, overrides map[string]string) (fanouttui.LaunchResult, error) {
	if issueNum <= 0 {
		return fanouttui.LaunchResult{}, fmt.Errorf("issue number is required")
	}
	if err := validateTUIAgentSelection(defaultAgent, overrides); err != nil {
		return fanouttui.LaunchResult{}, err
	}
	detail, openChildren, err := fetchLaunchableIssue(projectRoot, issueNum)
	if err != nil {
		return fanouttui.LaunchResult{}, err
	}
	cfg := tuiIssueLaunchConfig(issueNum, defaultAgent, overrides)
	if openChildren == 0 {
		launchResult, launchErr := launchStandaloneIssuePaneWithResult(projectRoot, session, commandName, cfg, resolvedSettings, hookConfig, detail)
		if launchErr != nil {
			if errors.Is(launchErr, watch.ErrAlreadyFanned) {
				return fanouttui.LaunchResult{}, fmt.Errorf("issue #%d already has a fanout pane", issueNum)
			}
			return fanouttui.LaunchResult{}, launchErr
		}
		return fanouttui.LaunchResult{
			Notice:         combinedLaunchNotice([]string{fmt.Sprintf("started session for #%d", issueNum)}, launchResult.Notice),
			CreatedPaneIDs: []string{launchResult.PaneID},
		}, nil
	}
	var orchestratorReq panelaunch.Request
	var orchestratorPaneID string
	var orchestratorCreated bool
	var orchestratorNotice string
	ready := func(
		store state.Store,
		recorder panelaunch.StateRecorder,
		runtimeBackend backend.Backend,
		herdr panelaunch.ManagedSessionRuntime,
	) error {
		if runtimeBackend.Name() == backend.Herdr {
			guardErr := guardLinkedIssueOrchestrator(projectRoot, store, issueNum)
			if errors.Is(guardErr, errIssueOrchestratorRecorded) {
				return nil
			}
			return guardErr
		}
		var launchErr error
		orchestratorReq, orchestratorPaneID, orchestratorCreated, orchestratorNotice, launchErr = launchIssueOrchestratorPrepared(
			projectRoot, session, commandName, runtimeBackend, herdr, store, recorder,
			hookConfig, detail, defaultAgent, resolvedSettings.OrchestratorPlanMode,
		)
		return launchErr
	}
	after := func(
		store state.Store,
		recorder panelaunch.StateRecorder,
		runtimeBackend backend.Backend,
		herdr panelaunch.ManagedSessionRuntime,
		progress run.IssueAfterContext,
	) error {
		if !launchHerdrOrchestratorAfterChildren(runtimeBackend.Name(), progress) {
			return nil
		}
		var launchErr error
		orchestratorReq, orchestratorPaneID, orchestratorCreated, orchestratorNotice, launchErr = launchIssueOrchestratorPrepared(
			projectRoot, session, commandName, runtimeBackend, herdr, store, recorder,
			hookConfig, detail, defaultAgent, resolvedSettings.OrchestratorPlanMode,
		)
		return launchErr
	}
	result, err := launchParentIssueFanoutWithCallbacks(projectRoot, session, commandName, cfg, ready, after)
	runtimeBackend := result.runtimeBackend
	if err != nil && len(result.CreatedPaneIDs) == 0 && orchestratorCreated {
		if cleanupErr := cleanupIssueOrchestrator(projectRoot, session, runtimeBackend, result.herdr, orchestratorReq, orchestratorPaneID); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("cleanup issue orchestrator: %w", cleanupErr))
		} else {
			orchestratorPaneID = ""
			if releaseErr := releaseCleanedIssueOrchestratorGate(runtimeBackend, orchestratorReq); releaseErr != nil {
				err = errors.Join(err, fmt.Errorf("release cleaned issue orchestrator gate: %w", releaseErr))
			}
		}
	} else if orchestratorCreated {
		if releaseErr := panelaunch.ReleaseAgentStartGate(runtimeBackend, orchestratorReq); releaseErr != nil {
			err = errors.Join(err, fmt.Errorf("release issue orchestrator gate: %w", releaseErr))
			if cleanupErr := cleanupIssueOrchestrator(projectRoot, session, runtimeBackend, result.herdr, orchestratorReq, orchestratorPaneID); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("cleanup gated issue orchestrator: %w", cleanupErr))
			} else {
				orchestratorPaneID = ""
			}
		}
	}
	if orchestratorPaneID != "" && orchestratorNotice != "" {
		if result.Notice == "" {
			result.Notice = orchestratorNotice
		} else {
			result.Notice = orchestratorNotice + "; " + result.Notice
		}
	}
	orchestratorAfterChildren := runtimeBackend != nil && runtimeBackend.Name() == backend.Herdr
	return finishTUIIssueParentLaunch(issueNum, orchestratorAfterChildren, orchestratorPaneID, result, err)
}

func launchHerdrOrchestratorAfterChildren(runtimeBackend backend.Name, progress run.IssueAfterContext) bool {
	return runtimeBackend == backend.Herdr && (progress.Failed == 0 || progress.Created > 0)
}

func releaseCleanedIssueOrchestratorGate(runtimeBackend backend.Backend, req panelaunch.Request) error {
	if runtimeBackend.Name() != backend.Tmux {
		return nil
	}
	return panelaunch.ReleaseAgentStartGate(runtimeBackend, req)
}

func finishTUIIssueParentLaunch(issueNum int, orchestratorAfterChildren bool, orchestratorPaneID string, result parentIssueFanoutResult, err error) (fanouttui.LaunchResult, error) {
	created := len(result.CreatedPaneIDs)
	createdPaneIDs := orderedTUIIssuePaneIDs(orchestratorAfterChildren, orchestratorPaneID, result.CreatedPaneIDs)
	if err != nil {
		if len(createdPaneIDs) > 0 {
			// The fail-fast loop may have created panes before the failure;
			// report a partial success so the TUI reloads state and focuses the
			// first running agent while preserving the failure in its notice.
			notice := fmt.Sprintf("created %d pane(s), then failed: %v", created, err)
			if orchestratorPaneID != "" && created > 0 {
				notice = fmt.Sprintf("started orchestrator + %d child pane(s), then failed: %v", created, err)
			} else if orchestratorPaneID != "" {
				notice = fmt.Sprintf("started orchestrator, then failed: %v", err)
			}
			if result.Notice != "" {
				notice += "; " + result.Notice
			}
			return fanouttui.LaunchResult{Notice: notice, CreatedPaneIDs: createdPaneIDs}, nil
		}
		return fanouttui.LaunchResult{}, err
	}
	notice := fmt.Sprintf("fanned out #%d: created %d pane(s)", issueNum, created)
	switch {
	case orchestratorPaneID != "" && created > 0:
		notice = fmt.Sprintf("fanned out #%d: started orchestrator + %d child pane(s)", issueNum, created)
	case orchestratorPaneID != "":
		notice = fmt.Sprintf("started orchestrator for #%d; children already have panes", issueNum)
	case created <= 0:
		notice = fmt.Sprintf("#%d: no new panes (children already have one)", issueNum)
	}
	if result.Watch.Deferred {
		notice += "; blocked/deferred children remain - re-select the issue later"
	}
	if result.Notice != "" {
		notice += "; " + result.Notice
	}
	return fanouttui.LaunchResult{Notice: notice, CreatedPaneIDs: createdPaneIDs}, nil
}

func orderedTUIIssuePaneIDs(orchestratorAfterChildren bool, orchestratorPaneID string, childPaneIDs []string) []string {
	if orchestratorPaneID == "" {
		return childPaneIDs
	}
	if orchestratorAfterChildren {
		return append(append([]string{}, childPaneIDs...), orchestratorPaneID)
	}
	return append([]string{orchestratorPaneID}, childPaneIDs...)
}

// fetchLaunchableIssue is the shared launch preamble for the TUI issue lanes:
// it re-fetches the issue (the picker list may be stale, and the detail carries
// the body the briefing needs), rejects non-OPEN issues, and counts the OPEN
// children that decide between the standalone, fan-out, and plan lanes.
func fetchLaunchableIssue(projectRoot string, issueNum int) (ghissue.Issue, int, error) {
	gh := ghissue.Runner{Cwd: projectRoot}
	detail, err := gh.IssueDetail(issueNum)
	if err != nil {
		return ghissue.Issue{}, 0, err
	}
	if detail.State != "OPEN" {
		return ghissue.Issue{}, 0, fmt.Errorf("issue #%d is not OPEN", issueNum)
	}
	openChildren, err := countOpenChildTargets(gh, issueNum)
	if err != nil {
		return ghissue.Issue{}, 0, err
	}
	return detail, openChildren, nil
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
		TUIInteractive:  true,
	}
	for target, name := range overrides {
		cfg.AgentOverrides = cliflags.UpsertAgentOverride(cfg.AgentOverrides, target, name)
	}
	return cfg
}
