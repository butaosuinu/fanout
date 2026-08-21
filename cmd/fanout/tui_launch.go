package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/app/briefing"
	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/app/run"
	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/codexapp"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/paneruntime"
	fanoutruntime "github.com/butaosuinu/fanout/internal/infra/runtime"
	"github.com/butaosuinu/fanout/internal/infra/settings"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
	fanouttui "github.com/butaosuinu/fanout/internal/ui/tui"
)

func newTUILaunchPaneFunc(projectRoot, session, commandName string, hookConfig hooks.Config) fanouttui.LaunchFunc {
	return func(req fanouttui.LaunchRequest) (fanouttui.LaunchResult, error) {
		return launchManualPaneFromTUI(projectRoot, session, commandName, hookConfig, req)
	}
}

func newTUIAttachAgentFunc(projectRoot, session, commandName string, hookConfig hooks.Config) fanouttui.AttachLaunchFunc {
	return func(req fanouttui.AttachLaunchRequest) (string, error) {
		return launchAttachedAgentFromTUI(projectRoot, session, commandName, hookConfig, req)
	}
}

func newTUIIssuePlanLaunchFunc(projectRoot, session, commandName string, hookConfig hooks.Config) fanouttui.IssuePlanLaunchFunc {
	return func(issueNum int, coordinatorAgent, workerAgent string) (fanouttui.LaunchResult, error) {
		return launchIssuePlanFromTUI(projectRoot, session, commandName, hookConfig, issueNum, coordinatorAgent, workerAgent)
	}
}

//nolint:gocognit,gocyclo,funlen // Keep the multi-agent partial-success transaction under one state and Herdr-intent lock.
func launchManualPaneFromTUI(projectRoot, session, commandName string, hookConfig hooks.Config, req fanouttui.LaunchRequest) (fanouttui.LaunchResult, error) {
	if req.PlanFanout {
		return launchPlanPromptFromTUI(projectRoot, session, commandName, hookConfig, req)
	}
	prompt := normalizeTUIPrompt(req.Prompt)
	if prompt == "" {
		return fanouttui.LaunchResult{}, fmt.Errorf("prompt is required")
	}
	agentNames := normalizeTUIAgents(req.Agents)
	for _, agentName := range agentNames {
		if validateErr := agent.ValidateKnown(agentName); validateErr != nil {
			return fanouttui.LaunchResult{}, validateErr
		}
		if validateErr := agent.ValidateInstalled(agentName); validateErr != nil {
			return fanouttui.LaunchResult{}, validateErr
		}
	}
	var stdout, stderr bytes.Buffer
	launchLogger := log.NewWith(&stdout, &stderr, false)
	cfg := newSessionConfigForTUIAgent(projectRoot, agentNames[0], launchLogger.Warn)
	rt, err := resolveTUILaunchRuntime(projectRoot, session, cfg)
	if err != nil {
		return fanouttui.LaunchResult{}, err
	}
	store, recorder, code := run.LoadState(cfg.DryRun, projectRoot, launchLogger)
	if code != exitcode.OK {
		return fanouttui.LaunchResult{}, bufferedLaunchError(stdout, stderr, "load fanout state")
	}
	if recorder != nil {
		defer func() {
			_ = recorder.Unlock()
		}()
	}
	if rt.VerifyBackend != nil {
		if err := rt.VerifyBackend(cfg.ParentRef, store); err != nil {
			return fanouttui.LaunchResult{}, fmt.Errorf("runtime backend: %w", err)
		}
	}
	if err := rt.PrepareLaunchBackend(); err != nil {
		return fanouttui.LaunchResult{}, fmt.Errorf("runtime backend: %w", err)
	}
	createdPaneIDs := make([]string, 0, len(agentNames))
	for _, agentName := range agentNames {
		cfg = newSessionConfigForTUIAgent(projectRoot, agentName, launchLogger.Warn)
		launchStore := store
		if recorder != nil {
			launchStore = recorder.Store
		}
		paneReq := panelaunch.NewManualRequest(cfg, projectRoot, launchStore, hookConfig, manualPaneOptionsForTUI(prompt, agentName))
		launcher := &panelaunch.Launcher{Cfg: cfg, Log: launchLogger, Info: rt.Info, Backend: rt.Backend, Managed: rt.Managed, Recorder: recorder, Palette: log.Palette{}, CommandName: commandName}
		if result, ok := launcher.LaunchWithResult(paneReq); ok {
			if result.PaneID != "" {
				createdPaneIDs = append(createdPaneIDs, result.PaneID)
			}
			continue
		}
		if len(createdPaneIDs) > 0 {
			return fanouttui.LaunchResult{
				Notice:         partialManualLaunchNotice(len(createdPaneIDs), stderr),
				CreatedPaneIDs: createdPaneIDs,
			}, nil
		}
		return fanouttui.LaunchResult{}, bufferedLaunchError(stdout, stderr, "create pane")
	}
	return fanouttui.LaunchResult{
		Notice:         bufferedLaunchNotice(stderr),
		CreatedPaneIDs: createdPaneIDs,
	}, nil
}

// launchPlanPromptFromTUI launches one coordinator pane at the project root
// that runs the fanout-plan skill against the raw prompt. It normalizes the
// prompt, requires exactly one coordinator agent, and delegates the pane launch
// to launchPlanCoordinator.
func launchPlanPromptFromTUI(projectRoot, session, commandName string, hookConfig hooks.Config, req fanouttui.LaunchRequest) (fanouttui.LaunchResult, error) {
	prompt := normalizeTUIPrompt(req.Prompt)
	if prompt == "" {
		return fanouttui.LaunchResult{}, fmt.Errorf("prompt is required")
	}
	agentNames := normalizeTUIAgents(req.Agents)
	if len(agentNames) != 1 {
		return fanouttui.LaunchResult{}, fmt.Errorf("plan fan-out launches one coordinator agent; select exactly one")
	}
	agentName := agentNames[0]
	paneReq, paneID, launchNotice, err := launchPlanCoordinator(projectRoot, session, commandName, panelaunch.ManualParentRef, agentName, nil,
		func(store state.Store, livenessKey string, cfg *cliflags.Config) panelaunch.Request {
			return newPlanPromptPaneRequest(projectRoot, store, hookConfig, prompt, cfg, livenessKey)
		})
	if err != nil {
		return fanouttui.LaunchResult{}, err
	}
	notice := fmt.Sprintf("started plan coordinator (%s): %s", agentName, paneReq.Prompt)
	if launchNotice != "" {
		notice += "; " + launchNotice
	}
	return fanouttui.LaunchResult{
		Notice:         notice,
		CreatedPaneIDs: []string{paneID},
	}, nil
}

// launchPlanCoordinator validates the coordinator agent, locks state, and
// attaches one project-root coordinator pane built by buildReq. guard, when
// non-nil, runs on the locked store before any pane is created, so callers can
// reject duplicate launches without a lock race. The coordinator decomposes its
// source and fans the tasks out itself, so it runs directly on the project root:
// running `fanout plan` inside a worktree would resolve the git root there and
// nest state under the coordinator's worktree. Its initial posture follows
// newSessionPlanMode.
//
//nolint:funlen // Coordinator validation, backend admission, and the one attach remain one lock-scoped transaction.
func launchPlanCoordinator(projectRoot, session, commandName, parentRef, agentName string, guard func(state.Store) error, buildReq func(store state.Store, livenessKey string, cfg *cliflags.Config) panelaunch.Request) (panelaunch.Request, string, string, error) {
	if validateErr := agent.ValidateKnown(agentName); validateErr != nil {
		return panelaunch.Request{}, "", "", validateErr
	}
	if validateErr := agent.ValidateInstalled(agentName); validateErr != nil {
		return panelaunch.Request{}, "", "", validateErr
	}
	cfg := newSessionConfigForTUIAgent(projectRoot, agentName, nil)
	cfg.ParentRef = parentRef
	rt, err := resolveTUILaunchRuntime(projectRoot, session, cfg)
	if err != nil {
		return panelaunch.Request{}, "", "", err
	}
	if excludeErr := worktree.EnsureLocalExclude(projectRoot); excludeErr != nil {
		return panelaunch.Request{}, "", "", fmt.Errorf("prepare local git exclude: %w", excludeErr)
	}

	recorder, err := state.LockProjectForLaunch(projectRoot)
	if err != nil {
		return panelaunch.Request{}, "", "", err
	}
	defer func() {
		_ = recorder.Unlock()
	}()
	if rt.VerifyBackend != nil {
		if err := rt.VerifyBackend(parentRef, recorder.Store); err != nil {
			return panelaunch.Request{}, "", "", fmt.Errorf("runtime backend: %w", err)
		}
	}
	if err := rt.PrepareLaunchBackend(); err != nil {
		return panelaunch.Request{}, "", "", fmt.Errorf("runtime backend: %w", err)
	}
	return launchPlanCoordinatorLockedWithConfig(
		projectRoot, session, commandName, rt.Backend, rt.Managed,
		cfg, recorder.Store, recorder, guard, buildReq,
	)
}

// launchPlanCoordinatorLocked is the state-lock-held entry for the issue
// parent lane. That lane already owns the child fan-out lock when its validated
// plan becomes ready, so it reuses that recorder instead of taking a nested
// lock.
func launchPlanCoordinatorLocked(projectRoot, session, commandName string, runtimeBackend backend.Backend, managed panelaunch.ManagedSessionRuntime, agentName, runtimeParent string, store state.Store, recorder panelaunch.StateRecorder, guard func(state.Store) error, buildReq func(store state.Store, livenessKey string) panelaunch.Request) (panelaunch.Request, string, string, error) {
	cfg := &cliflags.Config{Agent: agentName, ParentRef: runtimeParent}
	return launchPlanCoordinatorLockedWithConfig(projectRoot, session, commandName, runtimeBackend, managed, cfg, store, recorder, guard,
		func(store state.Store, livenessKey string, _ *cliflags.Config) panelaunch.Request {
			return buildReq(store, livenessKey)
		})
}

// launchPlanCoordinatorLockedWithConfig carries a lane-specific launch mode
// through the shared coordinator attach path.
func launchPlanCoordinatorLockedWithConfig(projectRoot, session, commandName string, runtimeBackend backend.Backend, managed panelaunch.ManagedSessionRuntime, cfg *cliflags.Config, store state.Store, recorder panelaunch.StateRecorder, guard func(state.Store) error, buildReq func(store state.Store, livenessKey string, cfg *cliflags.Config) panelaunch.Request) (panelaunch.Request, string, string, error) {
	var stdout, stderr bytes.Buffer
	launchLogger := log.NewWith(&stdout, &stderr, false)
	if guard != nil {
		if guardErr := guard(store); guardErr != nil {
			return panelaunch.Request{}, "", "", guardErr
		}
	}

	info := &fanoutruntime.Info{
		Session:     "",
		Target:      tuiLaunchTarget(session),
		ProjectRoot: projectRoot,
	}
	livenessKey, err := panelaunch.NewShellPaneKey()
	if err != nil {
		return panelaunch.Request{}, "", "", err
	}
	paneReq := coordinatorRuntimeRequest(runtimeBackend.MutationModel(), cfg.ParentRef, buildReq(store, livenessKey, cfg))
	launcher := &panelaunch.Launcher{Cfg: cfg, Log: launchLogger, Info: info, Backend: runtimeBackend, Managed: managed, Recorder: recorder, Palette: log.Palette{}, CommandName: commandName}
	result, ok := launcher.AttachWithResult(paneReq, projectRoot)
	if !ok {
		return panelaunch.Request{}, "", "", bufferedLaunchError(stdout, stderr, "create plan coordinator pane")
	}
	return paneReq, result.PaneID, bufferedLaunchNotice(stderr), nil
}

// coordinatorRuntimeRequest applies the identity contract of the launch lane
// the mutation model selects, matching prepareAttachedLiveness.
func coordinatorRuntimeRequest(model backend.MutationModel, runtimeParent string, req panelaunch.Request) panelaunch.Request {
	if model != backend.MutationJournaled {
		return req
	}
	// A journaled root coordinator never uses the atomic lane's liveness key. Its
	// lifecycle binds to the actual parent while the display row stays manual.
	req.ShellKey = ""
	req.AgentStartGate = ""
	if runtimeParent != panelaunch.ManualParentRef {
		req.RuntimeParent = runtimeParent
	}
	return req
}

// launchIssuePlanFromTUI launches one plan coordinator pane that decomposes a
// single OPEN issue (no OPEN children) into issue-less fanout plan tasks run by
// workerAgent. countOpenChildTargets is the backstop behind the TUI gray-out:
// the picker's child markers can be stale, so an issue that grew children after
// the picker loaded fans out its children instead.
func launchIssuePlanFromTUI(projectRoot, session, commandName string, hookConfig hooks.Config, issueNum int, coordinatorAgent, workerAgent string) (fanouttui.LaunchResult, error) {
	if issueNum <= 0 {
		return fanouttui.LaunchResult{}, fmt.Errorf("issue number is required")
	}
	// Fail fast on both agents before any gh call. The worker is the default
	// agent of every fanned-out task, so its install check runs up front like
	// issue mode's default agent (validateTUIAgentSelection): failing later,
	// when the coordinator runs `fanout plan --agent <worker>`, would leave a
	// launched coordinator pane behind.
	for _, name := range []string{coordinatorAgent, workerAgent} {
		if validateErr := agent.ValidateKnown(name); validateErr != nil {
			return fanouttui.LaunchResult{}, validateErr
		}
	}
	for _, name := range []string{coordinatorAgent, workerAgent} {
		if validateErr := agent.ValidateInstalled(name); validateErr != nil {
			return fanouttui.LaunchResult{}, validateErr
		}
	}
	detail, openChildren, err := fetchLaunchableIssue(projectRoot, issueNum)
	if err != nil {
		return fanouttui.LaunchResult{}, err
	}
	if openChildren > 0 {
		// Short enough to render unwrapped as the form's one error line.
		return fanouttui.LaunchResult{}, fmt.Errorf("issue #%d has %d open children; uncheck the plan checkbox", issueNum, openChildren)
	}
	paneReq, paneID, launchNotice, err := launchPlanCoordinator(projectRoot, session, commandName, strconv.Itoa(issueNum), coordinatorAgent,
		func(store state.Store) error { return guardLinkedIssuePlanCoordinator(projectRoot, store, issueNum) },
		func(store state.Store, livenessKey string, cfg *cliflags.Config) panelaunch.Request {
			return newIssuePlanPaneRequest(projectRoot, store, hookConfig, detail, cfg, workerAgent, livenessKey)
		})
	if err != nil {
		return fanouttui.LaunchResult{}, err
	}
	notice := fmt.Sprintf("started plan coordinator for #%d (%s): %s", issueNum, coordinatorAgent, paneReq.Prompt)
	if launchNotice != "" {
		notice += "; " + launchNotice
	}
	return fanouttui.LaunchResult{
		Notice:         notice,
		CreatedPaneIDs: []string{paneID},
	}, nil
}

// guardIssuePlanCoordinator is the plan-checkbox lane's dedupe, run on the
// locked store: it mirrors the standalone lane's ErrAlreadyFanned so a repeated
// submit never creates a second coordinator, overwrites a brief another
// coordinator may still be reading, or regenerates a spec over live plan tasks.
func guardIssuePlanCoordinator(projectRoot string, store state.Store, issueNum int) error {
	if issuePlanRecorded(projectRoot, store, issueNum) {
		return fmt.Errorf("issue #%d already has a plan session", issueNum)
	}
	if hasRecordedIssuePane(projectRoot, store, issueNum) {
		return fmt.Errorf("issue #%d already has a fanout pane", issueNum)
	}
	return nil
}

func guardLinkedIssuePlanCoordinator(projectRoot string, current state.Store, issueNum int) error {
	return guardLinkedIssueSession(projectRoot, current, func(root string, store state.Store) error {
		return guardIssuePlanCoordinator(root, store, issueNum)
	})
}

func guardLinkedIssueSession(projectRoot string, current state.Store, guard func(string, state.Store) error) error {
	stores, err := paneruntime.ProjectStores(projectRoot, current)
	if err != nil {
		return fmt.Errorf("inspect linked worktree issue sessions: %w", err)
	}
	for _, entry := range stores {
		if guardErr := guard(entry.Root, entry.Store); guardErr != nil {
			return guardErr
		}
	}
	return nil
}

// issuePlanRecorded reports whether any plan-lane row is linked to the issue: a
// coordinator, or — after the coordinator closed — the plan task rows whose
// saved spec declares the issue as its source. It keeps the issue-plan and
// plain issue lanes mutually exclusive in both directions and lifecycle phases.
func issuePlanRecorded(projectRoot string, store state.Store, issueNum int) bool {
	return panelaunch.PlanLinkedIssueNums(projectRoot, store)[issueNum]
}

// newPlanPromptPaneRequest builds the plan fan-out coordinator's pane request:
// a project-root pane (no worktree) whose prompt invokes the fanout-plan skill
// on the raw prompt written to BriefingPath. livenessKey becomes the pane's
// @fanout_shell_key: with the repo root as WorktreePath, path containment is
// too broad to detect tmux pane id reuse, so liveness matches on the key.
func newPlanPromptPaneRequest(projectRoot string, store state.Store, hookConfig hooks.Config, prompt string, cfg *cliflags.Config, livenessKey string) panelaunch.Request {
	number := panelaunch.NextManagedSyntheticPaneNumber(projectRoot, store, panelaunch.ManualParentRef)
	title := panelaunch.ShortIssueTitle("plan: " + panelaunch.FirstPromptLine(prompt))
	briefingPath := planPromptPath(projectRoot, number)
	req := panelaunch.Request{
		ParentRef:           panelaunch.ManualParentRef,
		Number:              number,
		Title:               title,
		Body:                prompt,
		ShortTitle:          panelaunch.ShortIssueTitle(title),
		Slug:                planPromptSlug(number),
		DisplayNameOverride: title,
		Prompt:              planSkillPrompt(cfg.Agent, briefingPath),
		Agent:               cfg.Agent,
		LaunchMode:          coordinatorLaunchMode(cfg.Agent, cfg.PlanModeEnabled()),
		ShellKey:            livenessKey,
		Hooks:               hookConfig,
		BriefingPath:        briefingPath,
		BriefingBody:        prompt,
	}
	if req.CodexPlanMode() {
		req.CodexPlanStatusPath = codexapp.StatusPath(projectRoot, number, cfg.DryRun)
	}
	return req
}

// newIssuePlanPaneRequest builds the issue-sourced plan coordinator's pane
// request: a project-root pane (no worktree) whose prompt invokes the
// fanout-plan skill on the issue-derived coordinator brief written to
// BriefingPath. It mirrors newPlanPromptPaneRequest but sources the title,
// body, and briefing from the GitHub issue. It never sets Source*.
func newIssuePlanPaneRequest(projectRoot string, store state.Store, hookConfig hooks.Config, issue ghissue.Issue, cfg *cliflags.Config, workerAgent, livenessKey string) panelaunch.Request {
	number := panelaunch.NextManagedSyntheticPaneNumber(projectRoot, store, panelaunch.ManualParentRef)
	title := fmt.Sprintf("plan: #%d %s", issue.Number, issue.Title)
	briefingPath := planIssuePromptPath(projectRoot, issue.Number, number)
	req := panelaunch.Request{
		ParentRef:           panelaunch.ManualParentRef,
		Number:              number,
		Title:               title,
		Body:                issue.Body,
		ShortTitle:          panelaunch.ShortIssueTitle(title),
		Slug:                panelaunch.PlanIssueSlug(issue.Number, number),
		DisplayNameOverride: title,
		Prompt:              planSkillPrompt(cfg.Agent, briefingPath),
		Agent:               cfg.Agent,
		LaunchMode:          coordinatorLaunchMode(cfg.Agent, cfg.PlanModeEnabled()),
		ShellKey:            livenessKey,
		Hooks:               hookConfig,
		BriefingPath:        briefingPath,
		BriefingBody:        briefing.RenderIssuePlanCoordinator(issue.Number, issue.Title, issue.Body, workerAgent),
	}
	if req.CodexPlanMode() {
		req.CodexPlanStatusPath = codexapp.StatusPath(projectRoot, number, cfg.DryRun)
	}
	return req
}

func coordinatorLaunchMode(agentName string, planMode bool) agent.LaunchMode {
	if planMode && (agentName == "claude" || agentName == "codex") {
		return agent.ModePlan
	}
	return agent.ModeBuild
}

// planSkillPrompt points the coordinator agent at the raw prompt file and
// invokes the fanout-plan skill to decompose it. Codex uses the `$fanout-plan`
// invocation; claude uses the `/fanout plan` slash command.
func planSkillPrompt(agentName, path string) string {
	if agentName == "codex" {
		return "$fanout-plan " + path
	}
	return "/fanout plan " + path
}

// planPromptPath mirrors briefing.Path for the coordinator's prompt file:
// <projectRoot>/.fanout/briefings/fanout-<repo>-plan-prompt-<N>.md.
func planPromptPath(projectRoot string, number int) string {
	if number < 0 {
		number = -number
	}
	return filepath.Join(briefing.Dir(projectRoot), fmt.Sprintf("fanout-%s-plan-prompt-%d.md", filepath.Base(projectRoot), number))
}

// planIssuePromptPath mirrors planPromptPath for the issue-sourced coordinator's
// briefing file. The per-launch synthetic pane number keeps relaunches (after a
// closed coordinator) from overwriting a brief an earlier coordinator may still
// be reading: <projectRoot>/.fanout/briefings/fanout-<repo>-plan-issue-<num>-<n>.md.
func planIssuePromptPath(projectRoot string, issueNum, number int) string {
	if number < 0 {
		number = -number
	}
	return filepath.Join(briefing.Dir(projectRoot), fmt.Sprintf("fanout-%s-plan-issue-%d-%d.md", filepath.Base(projectRoot), issueNum, number))
}

func planPromptSlug(number int) string {
	if number < 0 {
		number = -number
	}
	return fmt.Sprintf("plan-prompt-%d", number)
}

func launchAttachedAgentFromTUI(projectRoot, session, commandName string, hookConfig hooks.Config, req fanouttui.AttachLaunchRequest) (string, error) {
	ownerRoot := launchOwnerProjectRoot(projectRoot, req.Target.SourceProjectRoot)
	return launchAttachedAgent(ownerRoot, tuiLaunchTarget(session), commandName, hookConfig, req)
}

//nolint:gocognit,gocyclo,funlen // Keep all requested attached agents in one state and Herdr-intent lock with explicit partial success.
func launchAttachedAgent(projectRoot, target, commandName string, hookConfig hooks.Config, req fanouttui.AttachLaunchRequest) (string, error) {
	prompt := normalizeTUIPrompt(req.Prompt)
	if prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}
	targetPath, err := existingDirectory(req.Target.TargetPath)
	if err != nil {
		return "", err
	}
	agentNames := normalizeTUIAgents(req.Agents)
	for _, agentName := range agentNames {
		if validateErr := agent.ValidateKnown(agentName); validateErr != nil {
			return "", validateErr
		}
		if validateErr := agent.ValidateInstalled(agentName); validateErr != nil {
			return "", validateErr
		}
	}
	resolverParent := attachResolverParent(projectRoot, req.Target)
	resolvedTarget := req.Target
	resolvedTarget.SourceParent = resolverParent
	provisional := []backend.Binding{{
		Parent:  resolverParent,
		Backend: backend.NormalizeName(req.Target.Backend),
	}}
	var stdout, stderr bytes.Buffer
	launchLogger := log.NewWith(&stdout, &stderr, false)
	cfg := newSessionConfigForTUIAgent(projectRoot, agentNames[0], launchLogger.Warn)
	cfg.ParentRef = resolverParent
	rt, err := resolveTUILaunchRuntimeForTarget(projectRoot, "", target, cfg, provisional)
	if err != nil {
		return "", err
	}
	if excludeErr := worktree.EnsureLocalExclude(projectRoot); excludeErr != nil {
		return "", fmt.Errorf("prepare local git exclude: %w", excludeErr)
	}

	recorder, err := state.LockProjectForLaunch(projectRoot)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = recorder.Unlock()
	}()
	if rt.VerifyBackend != nil {
		if err := rt.VerifyBackend(resolverParent, recorder.Store); err != nil {
			return "", fmt.Errorf("runtime backend: %w", err)
		}
	}
	if err := rt.PrepareLaunchBackend(); err != nil {
		return "", fmt.Errorf("runtime backend: %w", err)
	}
	createdCount := 0
	for _, agentName := range agentNames {
		cfg := newSessionConfigForTUIAgent(projectRoot, agentName, launchLogger.Warn)
		cfg.ParentRef = resolverParent
		paneReq := newAttachedPaneRequest(cfg, projectRoot, recorder.Store, hookConfig, prompt, targetPath, resolvedTarget)
		launcher := &panelaunch.Launcher{Cfg: cfg, Log: launchLogger, Info: rt.Info, Backend: rt.Backend, Managed: rt.Managed, Recorder: recorder, Palette: log.Palette{}, CommandName: commandName}
		if launcher.Attach(paneReq, targetPath) {
			createdCount++
			continue
		}
		if createdCount > 0 {
			return partialAttachLaunchNotice(createdCount, stderr), nil
		}
		return "", bufferedLaunchError(stdout, stderr, "attach agent pane")
	}
	return bufferedLaunchNotice(stderr), nil
}

func attachResolverParent(projectRoot string, target fanouttui.AttachTarget) string {
	parent := strings.TrimSpace(target.SourceParent)
	if (parent == panelaunch.WatchParentRef || parent == panelaunch.ManualParentRef || parent == "") && target.SourceIssueNum > 0 {
		return strconv.Itoa(target.SourceIssueNum)
	}
	if planSlug, ok := strings.CutPrefix(parent, "plan:"); ok && planSlug != "" {
		return panelaunch.SavedPlanRuntimeParentRef(projectRoot, planSlug)
	}
	if parent == "" {
		return panelaunch.ManualParentRef
	}
	return parent
}

func existingDirectory(rawPath string) (string, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return "", fmt.Errorf("target worktree path is required")
	}
	targetPath, err := filepath.Abs(rawPath)
	if err != nil {
		return "", fmt.Errorf("resolve target worktree path: %w", err)
	}
	st, statErr := os.Stat(targetPath)
	if statErr != nil {
		return "", fmt.Errorf("target worktree path: %w", statErr)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("target worktree path is not a directory: %s", targetPath)
	}
	return targetPath, nil
}

// newAttachedPaneRequest adapts the TUI's attach target to the panelaunch
// builder (the app layer cannot import internal/ui/tui).
func newAttachedPaneRequest(cfg *cliflags.Config, projectRoot string, store state.Store, hookConfig hooks.Config, prompt, targetPath string, target fanouttui.AttachTarget) panelaunch.Request {
	return panelaunch.NewAttachedRequest(cfg, projectRoot, store, hookConfig, prompt, targetPath, panelaunch.AttachTarget{
		SourceParent:     target.SourceParent,
		SourceIssueNum:   target.SourceIssueNum,
		SourceTaskID:     target.SourceTaskID,
		SourceLabel:      target.SourceLabel,
		SourceBranchName: target.SourceBranchName,
	})
}

func partialManualLaunchNotice(createdCount int, stderr bytes.Buffer) string {
	notice := fmt.Sprintf("created %d new agent pane(s); stopped after a later pane failed", createdCount)
	if s := strings.TrimSpace(stderr.String()); s != "" {
		return notice + ": " + compactLaunchError(s)
	}
	return notice
}

func partialAttachLaunchNotice(createdCount int, stderr bytes.Buffer) string {
	notice := fmt.Sprintf("attached %d new agent pane(s); stopped after a later pane failed", createdCount)
	if s := strings.TrimSpace(stderr.String()); s != "" {
		return notice + ": " + compactLaunchError(s)
	}
	return notice
}

func compactLaunchError(s string) string {
	lines := strings.Split(s, "\n")
	for _, raw := range slices.Backward(lines) {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "[err ]") {
			return line
		}
	}
	if len(s) > 180 {
		return s[:180] + "..."
	}
	return s
}

// bufferedLaunchNotice extracts warnings from a successful launch's buffered
// log so the TUI can show them on success. Base-refresh warnings keep their
// concise historical wording.
func bufferedLaunchNotice(stderr bytes.Buffer) string {
	var notices []string
	for line := range strings.SplitSeq(stderr.String(), "\n") {
		if i := strings.Index(line, panelaunch.BaseRefreshSkippedNotice); i >= 0 {
			notices = appendUniqueNotice(notices, strings.TrimSpace(line[i:]))
			continue
		}
		if _, warning, ok := strings.Cut(line, "[warn]"); ok {
			notices = appendUniqueNotice(notices, strings.TrimSpace(warning))
		}
	}
	return strings.Join(notices, "; ")
}

func appendUniqueNotice(notices []string, notice string) []string {
	if notice == "" || slices.Contains(notices, notice) {
		return notices
	}
	return append(notices, notice)
}

func combinedLaunchNotice(notices []string, extra string) string {
	combined := make([]string, 0, len(notices)+1)
	for _, notice := range notices {
		combined = appendUniqueNotice(combined, strings.TrimSpace(notice))
	}
	combined = appendUniqueNotice(combined, strings.TrimSpace(extra))
	return strings.Join(combined, "; ")
}

// newSessionConfigForTUIAgent resolves settings at launch time so a value saved
// from the running TUI applies to the next manual, attach, or plan coordinator.
func newSessionConfigForTUIAgent(projectRoot, agentName string, warnf settings.WarnFunc) *cliflags.Config {
	planMode := settings.Resolve(projectRoot, settings.CLIOverrides{}, warnf).NewSessionPlanMode
	return &cliflags.Config{
		ParentRef:      panelaunch.ManualParentRef,
		Agent:          agentName,
		PlanMode:       &planMode,
		TUIInteractive: true,
	}
}

func newTUILaunchShellFunc(projectRoot, session string) fanouttui.ShellLaunchFunc {
	return func(req fanouttui.ShellLaunchRequest) error {
		return launchShellPaneFromTUI(projectRoot, session, req)
	}
}

func launchShellPaneFromTUI(projectRoot, session string, req fanouttui.ShellLaunchRequest) error {
	ownerRoot := projectRoot
	if !req.Root {
		ownerRoot = launchOwnerProjectRoot(projectRoot, req.SourceProjectRoot)
	}
	return launchShellPane(ownerRoot, tuiLaunchTarget(session), req)
}

func launchShellPane(projectRoot, target string, req fanouttui.ShellLaunchRequest) error {
	launcher := &panelaunch.Launcher{
		Info:    &fanoutruntime.Info{Target: target, ProjectRoot: projectRoot},
		Backend: paneruntime.NewTmux(),
	}
	return launcher.Shell(panelaunch.ShellRequest{TargetPath: req.TargetPath, Root: req.Root})
}

func newManagedLaunchShellFunc(
	projectRoot string,
	owned paneruntime.ManagedSession,
) fanouttui.ShellLaunchFunc {
	return func(req fanouttui.ShellLaunchRequest) error {
		ownerRoot := projectRoot
		if !req.Root {
			ownerRoot = launchOwnerProjectRoot(projectRoot, req.SourceProjectRoot)
		}
		launcher := &panelaunch.Launcher{
			Info:    &fanoutruntime.Info{ProjectRoot: ownerRoot},
			Backend: owned.Backend(),
			Managed: owned,
		}
		return launcher.Shell(panelaunch.ShellRequest{
			TargetPath: req.TargetPath,
			Root:       req.Root,
		})
	}
}

func launchOwnerProjectRoot(defaultRoot, sourceProjectRoot string) string {
	if root := strings.TrimSpace(sourceProjectRoot); root != "" {
		return root
	}
	return defaultRoot
}

func manualPaneOptionsForTUI(prompt, agentName string) panelaunch.ManualOptions {
	title := panelaunch.FirstPromptLine(prompt)
	oversized := len(prompt) > panelaunch.MaxInlineManualPromptBytes
	if oversized {
		title = panelaunch.ShortIssueTitle(title)
	}
	opts := panelaunch.ManualOptions{
		Title:  title,
		Agent:  agentName,
		Prompt: title,
	}
	if strings.Contains(prompt, "\n") || oversized {
		opts.Body = prompt
	}
	return opts
}

func normalizeTUIAgents(raw []string) []string {
	var agents []string
	for _, agentName := range raw {
		agentName = strings.TrimSpace(agentName)
		if agentName != "" {
			agents = append(agents, agentName)
		}
	}
	if len(agents) == 0 {
		return []string{defaultTUIAgent()}
	}
	return agents
}

func normalizeTUIPrompt(raw string) string {
	prompt := strings.ReplaceAll(raw, "\r\n", "\n")
	prompt = strings.ReplaceAll(prompt, "\r", "\n")
	return strings.TrimSpace(prompt)
}
