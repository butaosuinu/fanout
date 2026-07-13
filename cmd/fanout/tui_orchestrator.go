package main

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/butaosuinu/fanout/internal/app/briefing"
	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

var errIssueOrchestratorRecorded = errors.New("issue orchestrator is already recorded")

// issueOrchestratorRecorded matches only rows created by the TUI parent lane.
func issueOrchestratorRecorded(store state.Store, issueNum int) bool {
	for _, pane := range store.Panes {
		if num, ok := panelaunch.OrchestratorPaneIssueNum(pane); ok && num == issueNum {
			return true
		}
	}
	return false
}

// guardIssueOrchestrator keeps issue-plan and parent-orchestrator ownership
// mutually exclusive while treating an existing orchestrator as an idempotent skip.
func guardIssueOrchestrator(projectRoot string, store state.Store, issueNum int) error {
	if issuePlanRecorded(projectRoot, store, issueNum) {
		return fmt.Errorf("issue #%d already has a plan session; close it before fanning out children", issueNum)
	}
	if issueOrchestratorRecorded(store, issueNum) {
		return errIssueOrchestratorRecorded
	}
	return nil
}

// launchIssueOrchestratorPrepared attaches one normal agent to the project
// root after child planning and agent validation. The caller's locked recorder
// keeps the orchestrator row and child rows in one launch transaction.
func launchIssueOrchestratorPrepared(projectRoot, session, commandName string, store state.Store, recorder panelaunch.StateRecorder, hookConfig hooks.Config, issue ghissue.Issue, agentName string) (panelaunch.Request, string, bool, error) {
	req, paneID, err := launchPlanCoordinatorLocked(projectRoot, session, commandName, agentName, store, recorder,
		func(store state.Store) error {
			return guardIssueOrchestrator(projectRoot, store, issue.Number)
		},
		func(store state.Store, livenessKey string) panelaunch.Request {
			return newIssueOrchestratorPaneRequest(projectRoot, store, hookConfig, issue, agentName, livenessKey)
		})
	if errors.Is(err, errIssueOrchestratorRecorded) {
		return panelaunch.Request{}, "", false, nil
	}
	if err != nil {
		return panelaunch.Request{}, "", false, err
	}
	return req, paneID, true, nil
}

// newIssueOrchestratorPaneRequest mirrors an issue-plan coordinator request,
// but its one-line prompt starts parent coordination instead of plan fan-out.
func newIssueOrchestratorPaneRequest(projectRoot string, store state.Store, hookConfig hooks.Config, issue ghissue.Issue, agentName, livenessKey string) panelaunch.Request {
	number := panelaunch.NextSyntheticPaneNumber(store, panelaunch.ManualParentRef)
	title := fmt.Sprintf("orchestrator: #%d %s", issue.Number, issue.Title)
	briefingPath := orchestratorIssueBriefingPath(projectRoot, issue.Number, number)
	return panelaunch.Request{
		ParentRef:           panelaunch.ManualParentRef,
		Number:              number,
		Title:               title,
		Body:                issue.Body,
		ShortTitle:          panelaunch.ShortIssueTitle(title),
		Slug:                panelaunch.OrchestratorIssueSlug(issue.Number, number),
		DisplayNameOverride: title,
		Prompt:              fmt.Sprintf("orchestrate fanout for #%d. read %s and begin.", issue.Number, briefingPath),
		Agent:               agentName,
		ShellKey:            livenessKey,
		Hooks:               hookConfig,
		BriefingPath:        briefingPath,
		BriefingBody:        briefing.RenderIssueOrchestrator(issue.Number, issue.Title, issue.Body),
	}
}

// orchestratorIssueBriefingPath keeps each synthetic relaunch briefing unique.
func orchestratorIssueBriefingPath(projectRoot string, issueNum, number int) string {
	if number < 0 {
		number = -number
	}
	return filepath.Join(briefing.Dir(projectRoot), fmt.Sprintf("fanout-%s-orchestrator-issue-%d-%d.md", filepath.Base(projectRoot), issueNum, number))
}

// cleanupIssueOrchestrator rolls back a newly created parent pane when the
// child fan-out failed before creating any child pane.
func cleanupIssueOrchestrator(projectRoot, session string, req panelaunch.Request, paneID string) (err error) {
	recorder, err := state.LockProject(projectRoot)
	if err != nil {
		return err
	}
	defer func() {
		if unlockErr := recorder.Unlock(); unlockErr != nil {
			err = errors.Join(err, fmt.Errorf("unlock fanout state: %w", unlockErr))
		}
	}()
	if recorded, ok := recorder.Find(req.ParentRef, req.Number); ok &&
		(recorded.PaneID != paneID || recorded.ShellKey != req.ShellKey) {
		return fmt.Errorf("recorded orchestrator identity changed for %s/%d", req.ParentRef, req.Number)
	}
	if err := panelaunch.KillAttachedPane(tuiLaunchTarget(session), paneID, req.ShellKey); err != nil {
		return err
	}
	if err := recorder.RemovePane(req.ParentRef, req.Number); err != nil {
		return fmt.Errorf("remove orchestrator state: %w", err)
	}
	return nil
}
