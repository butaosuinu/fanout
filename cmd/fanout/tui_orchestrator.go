package main

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/butaosuinu/fanout/internal/app/briefing"
	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

var errIssueOrchestratorRecorded = errors.New("issue orchestrator is already recorded")

const codexOrchestratorPlanFallbackNotice = "Codex Plan Mode is incompatible with the orchestrator start gate; using normal codex"

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

func guardLinkedIssueOrchestrator(projectRoot string, current state.Store, issueNum int) error {
	return guardLinkedIssueSession(projectRoot, current, func(root string, store state.Store) error {
		return guardIssueOrchestrator(root, store, issueNum)
	})
}

// launchIssueOrchestratorPrepared attaches one agent to the project root in
// the configured initial mode after child planning and agent validation. The
// caller's locked recorder keeps the orchestrator row and child rows in one
// launch transaction.
func launchIssueOrchestratorPrepared(projectRoot, session, commandName string, runtimeBackend backend.Backend, managed panelaunch.ManagedSessionRuntime, store state.Store, recorder panelaunch.StateRecorder, hookConfig hooks.Config, issue ghissue.Issue, agentName string, orchestratorPlanMode bool) (panelaunch.Request, string, bool, string, error) {
	var fallbackNotice string
	req, paneID, launchNotice, err := launchPlanCoordinatorLocked(projectRoot, session, commandName, runtimeBackend, managed, agentName, fmt.Sprintf("%d", issue.Number), store, recorder,
		func(store state.Store) error {
			return guardLinkedIssueOrchestrator(projectRoot, store, issue.Number)
		},
		func(store state.Store, livenessKey string) panelaunch.Request {
			var req panelaunch.Request
			req, fallbackNotice = newIssueOrchestratorPaneRequest(projectRoot, store, hookConfig, issue, agentName, orchestratorPlanMode, livenessKey)
			return req
		})
	if errors.Is(err, errIssueOrchestratorRecorded) {
		return panelaunch.Request{}, "", false, "", nil
	}
	if err != nil {
		return panelaunch.Request{}, "", false, "", err
	}
	notice := fallbackNotice
	if notice == "" {
		notice = launchNotice
	} else if launchNotice != "" {
		notice += "; " + launchNotice
	}
	return req, paneID, true, notice, nil
}

// newIssueOrchestratorPaneRequest mirrors an issue-plan coordinator request,
// but its one-line prompt starts parent coordination instead of plan fan-out.
func newIssueOrchestratorPaneRequest(projectRoot string, store state.Store, hookConfig hooks.Config, issue ghissue.Issue, agentName string, orchestratorPlanMode bool, livenessKey string) (panelaunch.Request, string) {
	number := panelaunch.NextManagedSyntheticPaneNumber(projectRoot, store, panelaunch.ManualParentRef)
	title := fmt.Sprintf("orchestrator: #%d %s", issue.Number, issue.Title)
	briefingPath := orchestratorIssueBriefingPath(projectRoot, issue.Number, number)
	launchMode := agent.ModeBuild
	if orchestratorPlanMode {
		launchMode = agent.ModePlan
	}
	req := panelaunch.Request{
		ParentRef:           panelaunch.ManualParentRef,
		Number:              number,
		Title:               title,
		Body:                issue.Body,
		ShortTitle:          panelaunch.ShortIssueTitle(title),
		Slug:                panelaunch.OrchestratorIssueSlug(issue.Number, number),
		DisplayNameOverride: title,
		Prompt:              fmt.Sprintf("orchestrate fanout for #%d. read %s and begin.", issue.Number, briefingPath),
		Agent:               agentName,
		LaunchMode:          launchMode,
		ShellKey:            livenessKey,
		AgentStartGate:      "fanout-orchestrator-start-" + livenessKey,
		Hooks:               hookConfig,
		BriefingPath:        briefingPath,
		BriefingBody:        briefing.RenderIssueOrchestrator(issue.Number, issue.Title, issue.Body),
	}
	if req.CodexPlanMode() && req.AgentStartGate != "" {
		req.LaunchMode = agent.ModeBuild
		return req, codexOrchestratorPlanFallbackNotice
	}
	return req, ""
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
func cleanupIssueOrchestrator(
	projectRoot, session string,
	runtimeBackend backend.Backend,
	owned panelaunch.ManagedSessionRuntime,
	req panelaunch.Request,
	paneID string,
) (err error) {
	recorder, err := state.LockProject(projectRoot)
	if err != nil {
		return err
	}
	defer func() {
		if unlockErr := recorder.Unlock(); unlockErr != nil {
			err = errors.Join(err, fmt.Errorf("unlock fanout state: %w", unlockErr))
		}
	}()
	recorded, found := recorder.Find(req.ParentRef, req.Number)
	if issueOrchestratorIdentityChanged(runtimeBackend.MutationModel(), recorded, found, req, paneID) {
		return fmt.Errorf("recorded orchestrator identity changed for %s/%d", req.ParentRef, req.Number)
	}
	runtimeBackend, err = issueOrchestratorCloseBackend(runtimeBackend, owned, recorded, found, req)
	if err != nil {
		return err
	}
	if err := panelaunch.KillAttachedPane(runtimeBackend, tuiLaunchTarget(session), paneID, req.ShellKey); err != nil {
		return err
	}
	if err := recorder.RemovePane(req.ParentRef, req.Number); err != nil {
		return fmt.Errorf("remove orchestrator state: %w", err)
	}
	return nil
}

// issueOrchestratorIdentityChanged applies the identity contract of the launch
// lane the mutation model selects, matching prepareAttachedLiveness: the atomic
// lane stamps a local liveness key and checks it alongside the pane id, while
// the journaled lane records the remote identity instead and has no key to
// compare.
func issueOrchestratorIdentityChanged(
	model backend.MutationModel,
	recorded state.Pane,
	found bool,
	req panelaunch.Request,
	paneID string,
) bool {
	if !found || model == backend.MutationJournaled {
		return found && recorded.PaneID != paneID
	}
	return recorded.PaneID != paneID || recorded.ShellKey != req.ShellKey
}

// issueOrchestratorCloseBackend resolves the runtime the rollback closes
// through. A journaled launch lives in a workspace of the repository-owned
// session, so the close has to be bound to that workspace first; the atomic
// lane closes through the runtime it launched on.
func issueOrchestratorCloseBackend(
	runtimeBackend backend.Backend,
	owned panelaunch.ManagedSessionRuntime,
	recorded state.Pane,
	found bool,
	req panelaunch.Request,
) (backend.Backend, error) {
	if runtimeBackend.MutationModel() != backend.MutationJournaled {
		return runtimeBackend, nil
	}
	if !found {
		return nil, fmt.Errorf("recorded Herdr orchestrator identity is missing for %s/%d", req.ParentRef, req.Number)
	}
	return bindManagedWorkspaceClose(owned, recorded)
}
