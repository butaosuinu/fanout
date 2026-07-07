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
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/log"
	fanoutruntime "github.com/butaosuinu/fanout/internal/infra/runtime"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
	fanouttui "github.com/butaosuinu/fanout/internal/ui/tui"
)

func newTUILaunchPaneFunc(projectRoot, session, commandName string, hookConfig hooks.Config) fanouttui.LaunchFunc {
	return func(req fanouttui.LaunchRequest) (string, error) {
		return launchManualPaneFromTUI(projectRoot, session, commandName, hookConfig, req)
	}
}

func newTUIAttachAgentFunc(projectRoot, session, commandName string, hookConfig hooks.Config) fanouttui.AttachLaunchFunc {
	return func(req fanouttui.AttachLaunchRequest) (string, error) {
		return launchAttachedAgentFromTUI(projectRoot, session, commandName, hookConfig, req)
	}
}

func launchManualPaneFromTUI(projectRoot, session, commandName string, hookConfig hooks.Config, req fanouttui.LaunchRequest) (string, error) {
	if req.PlanFanout {
		return launchPlanPromptFromTUI(projectRoot, session, commandName, hookConfig, req)
	}
	prompt := normalizeTUIPrompt(req.Prompt)
	if prompt == "" {
		return "", fmt.Errorf("prompt is required")
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
	var stdout, stderr bytes.Buffer
	launchLogger := log.NewWith(&stdout, &stderr, false)
	cfg := manualPaneConfigForTUIAgent(agentNames[0])
	_, recorder, code := run.LoadState(cfg.DryRun, projectRoot, launchLogger)
	if code != exitcode.OK {
		return "", bufferedLaunchError(stdout, stderr, "load fanout state")
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
	createdCount := 0
	for _, agentName := range agentNames {
		cfg = manualPaneConfigForTUIAgent(agentName)
		paneReq := panelaunch.NewManualRequest(cfg, projectRoot, recorder.Store, hookConfig, manualPaneOptionsForTUI(prompt, agentName))
		launcher := &panelaunch.Launcher{Cfg: cfg, Log: launchLogger, Info: info, Recorder: recorder, Palette: log.Palette{}, CommandName: commandName}
		if launcher.LaunchOK(paneReq) {
			createdCount++
			continue
		}
		if createdCount > 0 {
			return partialManualLaunchNotice(createdCount, stderr), nil
		}
		return "", bufferedLaunchError(stdout, stderr, "create pane")
	}
	return bufferedLaunchNotice(stderr), nil
}

// launchPlanPromptFromTUI launches one coordinator pane at the project root
// that runs the fanout-plan skill against the raw prompt. The coordinator
// decomposes the prompt into parallel tasks and fans them out itself, so it
// runs as a normal agent (never Codex Plan Mode) directly on the project root:
// running `fanout plan` inside a worktree would resolve the git root there and
// nest state under the coordinator's worktree.
func launchPlanPromptFromTUI(projectRoot, session, commandName string, hookConfig hooks.Config, req fanouttui.LaunchRequest) (string, error) {
	prompt := normalizeTUIPrompt(req.Prompt)
	if prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}
	agentNames := normalizeTUIAgents(req.Agents)
	if len(agentNames) != 1 {
		return "", fmt.Errorf("plan fan-out launches one coordinator agent; select exactly one")
	}
	agentName := agentNames[0]
	if validateErr := agent.ValidateKnown(agentName); validateErr != nil {
		return "", validateErr
	}
	if validateErr := agent.ValidateInstalled(agentName); validateErr != nil {
		return "", validateErr
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
		return "", err
	}
	paneReq := newPlanPromptPaneRequest(projectRoot, recorder.Store, hookConfig, prompt, agentName, livenessKey)
	launcher := &panelaunch.Launcher{Cfg: cfg, Log: launchLogger, Info: info, Recorder: recorder, Palette: log.Palette{}, CommandName: commandName}
	if !launcher.Attach(paneReq, projectRoot) {
		return "", bufferedLaunchError(stdout, stderr, "create plan coordinator pane")
	}
	return fmt.Sprintf("started plan coordinator (%s): %s", agentName, paneReq.Prompt), nil
}

// newPlanPromptPaneRequest builds the plan fan-out coordinator's pane request:
// a project-root pane (no worktree) whose prompt invokes the fanout-plan skill
// on the raw prompt written to BriefingPath. livenessKey becomes the pane's
// @fanout_shell_key: with the repo root as WorktreePath, path containment is
// too broad to detect tmux pane id reuse, so liveness matches on the key.
func newPlanPromptPaneRequest(projectRoot string, store state.Store, hookConfig hooks.Config, prompt, agentName, livenessKey string) panelaunch.Request {
	number := panelaunch.NextSyntheticPaneNumber(store, panelaunch.ManualParentRef)
	title := "plan: " + panelaunch.FirstPromptLine(prompt)
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
	opts := panelaunch.ManualOptions{
		Title:  title,
		Agent:  agentName,
		Prompt: title,
	}
	if strings.Contains(prompt, "\n") {
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
