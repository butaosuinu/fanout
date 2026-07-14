package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/butaosuinu/fanout/internal/app/briefing"
	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/app/run"
	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/log"
	fanoutruntime "github.com/butaosuinu/fanout/internal/infra/runtime"
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
	cfg := manualPaneConfigForTUIAgent(agentNames[0])
	_, recorder, code := run.LoadState(cfg.DryRun, projectRoot, launchLogger)
	if code != exitcode.OK {
		return fanouttui.LaunchResult{}, bufferedLaunchError(stdout, stderr, "load fanout state")
	}
	if recorder != nil {
		defer func() {
			_ = recorder.Unlock()
		}()
	}

	info := &fanoutruntime.Info{
		Session:     session,
		Target:      tuiLaunchTarget(session),
		ProjectRoot: projectRoot,
	}
	createdPaneIDs := make([]string, 0, len(agentNames))
	for _, agentName := range agentNames {
		cfg = manualPaneConfigForTUIAgent(agentName)
		paneReq := panelaunch.NewManualRequest(cfg, projectRoot, recorder.Store, hookConfig, manualPaneOptionsForTUI(prompt, agentName))
		launcher := &panelaunch.Launcher{Cfg: cfg, Log: launchLogger, Info: info, Recorder: recorder, Palette: log.Palette{}, CommandName: commandName}
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
	paneReq, paneID, err := launchPlanCoordinator(projectRoot, session, commandName, agentName, nil,
		func(store state.Store, livenessKey string) panelaunch.Request {
			return newPlanPromptPaneRequest(projectRoot, store, hookConfig, prompt, agentName, livenessKey)
		})
	if err != nil {
		return fanouttui.LaunchResult{}, err
	}
	return fanouttui.LaunchResult{
		Notice:         fmt.Sprintf("started plan coordinator (%s): %s", agentName, paneReq.Prompt),
		CreatedPaneIDs: []string{paneID},
	}, nil
}

// launchPlanCoordinator validates the coordinator agent, locks state, and
// attaches one project-root coordinator pane built by buildReq. guard, when
// non-nil, runs on the locked store before any pane is created, so callers can
// reject duplicate launches without a lock race. The coordinator decomposes its
// source and fans the tasks out itself, so it runs as a normal agent (never
// Codex Plan Mode) directly on the project root: running `fanout plan` inside a
// worktree would resolve the git root there and nest state under the
// coordinator's worktree.
func launchPlanCoordinator(projectRoot, session, commandName, agentName string, guard func(state.Store) error, buildReq func(store state.Store, livenessKey string) panelaunch.Request) (panelaunch.Request, string, error) {
	if validateErr := agent.ValidateKnown(agentName); validateErr != nil {
		return panelaunch.Request{}, "", validateErr
	}
	if validateErr := agent.ValidateInstalled(agentName); validateErr != nil {
		return panelaunch.Request{}, "", validateErr
	}
	if excludeErr := worktree.EnsureLocalExclude(projectRoot); excludeErr != nil {
		return panelaunch.Request{}, "", fmt.Errorf("prepare local git exclude: %w", excludeErr)
	}

	recorder, err := state.LockProject(projectRoot)
	if err != nil {
		return panelaunch.Request{}, "", err
	}
	defer func() {
		_ = recorder.Unlock()
	}()
	return launchPlanCoordinatorLocked(projectRoot, session, commandName, agentName, recorder.Store, recorder, guard, buildReq)
}

// launchPlanCoordinatorLocked is the state-lock-held half of
// launchPlanCoordinator. The issue parent lane already owns the child fan-out
// lock when its validated plan becomes ready, so it reuses that recorder
// instead of attempting a nested lock.
func launchPlanCoordinatorLocked(projectRoot, session, commandName, agentName string, store state.Store, recorder panelaunch.StateRecorder, guard func(state.Store) error, buildReq func(store state.Store, livenessKey string) panelaunch.Request) (panelaunch.Request, string, error) {
	var stdout, stderr bytes.Buffer
	launchLogger := log.NewWith(&stdout, &stderr, false)
	if guard != nil {
		if guardErr := guard(store); guardErr != nil {
			return panelaunch.Request{}, "", guardErr
		}
	}

	// A plain agent config: the coordinator runs fanout plan itself, so it must
	// not launch in Codex Plan Mode even when the agent is codex.
	cfg := &cliflags.Config{Agent: agentName}
	info := &fanoutruntime.Info{
		Session:     "",
		Target:      tuiLaunchTarget(session),
		ProjectRoot: projectRoot,
	}
	livenessKey, err := panelaunch.NewShellPaneKey()
	if err != nil {
		return panelaunch.Request{}, "", err
	}
	paneReq := buildReq(store, livenessKey)
	launcher := &panelaunch.Launcher{Cfg: cfg, Log: launchLogger, Info: info, Recorder: recorder, Palette: log.Palette{}, CommandName: commandName}
	result, ok := launcher.AttachWithResult(paneReq, projectRoot)
	if !ok {
		return panelaunch.Request{}, "", bufferedLaunchError(stdout, stderr, "create plan coordinator pane")
	}
	return paneReq, result.PaneID, nil
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
	paneReq, paneID, err := launchPlanCoordinator(projectRoot, session, commandName, coordinatorAgent,
		func(store state.Store) error { return guardIssuePlanCoordinator(projectRoot, store, issueNum) },
		func(store state.Store, livenessKey string) panelaunch.Request {
			return newIssuePlanPaneRequest(projectRoot, store, hookConfig, detail, coordinatorAgent, workerAgent, livenessKey)
		})
	if err != nil {
		return fanouttui.LaunchResult{}, err
	}
	return fanouttui.LaunchResult{
		Notice:         fmt.Sprintf("started plan coordinator for #%d (%s): %s", issueNum, coordinatorAgent, paneReq.Prompt),
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
func newPlanPromptPaneRequest(projectRoot string, store state.Store, hookConfig hooks.Config, prompt, agentName, livenessKey string) panelaunch.Request {
	number := panelaunch.NextSyntheticPaneNumber(store, panelaunch.ManualParentRef)
	title := panelaunch.ShortIssueTitle("plan: " + panelaunch.FirstPromptLine(prompt))
	briefingPath := planPromptPath(projectRoot, number)
	return panelaunch.Request{
		ParentRef:           panelaunch.ManualParentRef,
		Number:              number,
		Title:               title,
		Body:                prompt,
		ShortTitle:          panelaunch.ShortIssueTitle(title),
		Slug:                planPromptSlug(number),
		DisplayNameOverride: title,
		Prompt:              planSkillPrompt(agentName, briefingPath),
		Agent:               agentName,
		ShellKey:            livenessKey,
		Hooks:               hookConfig,
		BriefingPath:        briefingPath,
		BriefingBody:        prompt,
	}
}

// newIssuePlanPaneRequest builds the issue-sourced plan coordinator's pane
// request: a project-root pane (no worktree) whose prompt invokes the
// fanout-plan skill on the issue-derived coordinator brief written to
// BriefingPath. It mirrors newPlanPromptPaneRequest but sources the title,
// body, and briefing from the GitHub issue and never sets Source*/CodexPlanMode.
func newIssuePlanPaneRequest(projectRoot string, store state.Store, hookConfig hooks.Config, issue ghissue.Issue, coordinatorAgent, workerAgent, livenessKey string) panelaunch.Request {
	number := panelaunch.NextSyntheticPaneNumber(store, panelaunch.ManualParentRef)
	title := fmt.Sprintf("plan: #%d %s", issue.Number, issue.Title)
	briefingPath := planIssuePromptPath(projectRoot, issue.Number, number)
	return panelaunch.Request{
		ParentRef:           panelaunch.ManualParentRef,
		Number:              number,
		Title:               title,
		Body:                issue.Body,
		ShortTitle:          panelaunch.ShortIssueTitle(title),
		Slug:                panelaunch.PlanIssueSlug(issue.Number, number),
		DisplayNameOverride: title,
		Prompt:              planSkillPrompt(coordinatorAgent, briefingPath),
		Agent:               coordinatorAgent,
		ShellKey:            livenessKey,
		Hooks:               hookConfig,
		BriefingPath:        briefingPath,
		BriefingBody:        briefing.RenderIssuePlanCoordinator(issue.Number, issue.Title, issue.Body, workerAgent),
	}
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
	if excludeErr := worktree.EnsureLocalExclude(projectRoot); excludeErr != nil {
		return "", fmt.Errorf("prepare local git exclude: %w", excludeErr)
	}

	var stdout, stderr bytes.Buffer
	launchLogger := log.NewWith(&stdout, &stderr, false)
	recorder, err := state.LockProject(projectRoot)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = recorder.Unlock()
	}()

	info := &fanoutruntime.Info{
		Session:     "",
		Target:      target,
		ProjectRoot: projectRoot,
	}
	createdCount := 0
	for _, agentName := range agentNames {
		cfg := manualPaneConfigForTUIAgent(agentName)
		paneReq := newAttachedPaneRequest(cfg, projectRoot, recorder.Store, hookConfig, prompt, targetPath, req.Target)
		launcher := &panelaunch.Launcher{Cfg: cfg, Log: launchLogger, Info: info, Recorder: recorder, Palette: log.Palette{}, CommandName: commandName}
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

// bufferedLaunchNotice extracts the tolerated base-refresh skip line, if any,
// from a successful launch's buffered log so the TUI can show it on success.
func bufferedLaunchNotice(stderr bytes.Buffer) string {
	for line := range strings.SplitSeq(stderr.String(), "\n") {
		if i := strings.Index(line, panelaunch.BaseRefreshSkippedNotice); i >= 0 {
			return strings.TrimSpace(line[i:])
		}
	}
	return ""
}

func manualPaneConfigForTUIAgent(agentName string) *cliflags.Config {
	cfg := &cliflags.Config{Agent: agentName}
	if agentName == "codex" {
		codexPlanMode := true
		cfg.CodexPlanMode = &codexPlanMode
	}
	return cfg
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
	launcher := &panelaunch.Launcher{Info: &fanoutruntime.Info{Target: target, ProjectRoot: projectRoot}}
	return launcher.Shell(panelaunch.ShellRequest{TargetPath: req.TargetPath, Root: req.Root})
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
