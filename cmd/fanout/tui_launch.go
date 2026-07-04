package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/agent"
	"github.com/butaosuinu/fanout/internal/briefing"
	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/hooks"
	"github.com/butaosuinu/fanout/internal/log"
	"github.com/butaosuinu/fanout/internal/naming"
	"github.com/butaosuinu/fanout/internal/panelayout"
	fanoutruntime "github.com/butaosuinu/fanout/internal/runtime"
	"github.com/butaosuinu/fanout/internal/state"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
	fanouttui "github.com/butaosuinu/fanout/internal/tui"
	"github.com/butaosuinu/fanout/internal/worktree"
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
	_, recorder, code := loadRunState(cfg, projectRoot, launchLogger)
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
		paneReq := newManualPaneRequest(cfg, projectRoot, recorder.Store, hookConfig, manualPaneOptionsForTUI(prompt, agentName))
		if createPane(cfg, launchLogger, info, paneReq, recorder, log.Palette{}, commandName) {
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
	livenessKey, err := newShellPaneKey()
	if err != nil {
		return "", err
	}
	paneReq := newPlanPromptPaneRequest(projectRoot, recorder.Store, hookConfig, prompt, agentName, livenessKey)
	if !createAttachedPane(cfg, launchLogger, info, paneReq, projectRoot, recorder, commandName) {
		return "", bufferedLaunchError(stdout, stderr, "create plan coordinator pane")
	}
	return fmt.Sprintf("started plan coordinator (%s): %s", agentName, paneReq.Prompt), nil
}

// newPlanPromptPaneRequest builds the plan fan-out coordinator's pane request:
// a project-root pane (no worktree) whose prompt invokes the fanout-plan skill
// on the raw prompt written to BriefingPath. livenessKey becomes the pane's
// @fanout_shell_key: with the repo root as WorktreePath, path containment is
// too broad to detect tmux pane id reuse, so liveness matches on the key.
func newPlanPromptPaneRequest(projectRoot string, store state.Store, hookConfig hooks.Config, prompt, agentName, livenessKey string) paneRequest {
	number := nextSyntheticPaneNumber(store, manualPaneParentRef)
	title := "plan: " + firstPromptLine(prompt)
	briefingPath := planPromptPath(projectRoot, number)
	return paneRequest{
		ParentRef:           manualPaneParentRef,
		Number:              number,
		Title:               title,
		Body:                prompt,
		ShortTitle:          shortIssueTitle(title),
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
// /tmp/fanout-<repo>-plan-prompt-<N>.md.
func planPromptPath(projectRoot string, number int) string {
	if number < 0 {
		number = -number
	}
	return fmt.Sprintf("/tmp/fanout-%s-plan-prompt-%d.md", filepath.Base(projectRoot), number)
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
		if createAttachedPane(cfg, launchLogger, info, paneReq, targetPath, recorder, commandName) {
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

func newAttachedPaneRequest(cfg *cliflags.Config, projectRoot string, store state.Store, hookConfig hooks.Config, prompt, targetPath string, target fanouttui.AttachTarget) paneRequest {
	parentRef := strings.TrimSpace(target.SourceParent)
	if parentRef == "" {
		parentRef = manualPaneParentRef
	}
	number := nextSyntheticPaneNumber(store, parentRef)
	agentName := cfg.Agent
	title := attachedPaneTitle(agentName, target.SourceLabel, targetPath)
	slug := attachedPaneSlug(targetPath, agentName, number)
	body := prompt
	shortPrompt := firstPromptLine(prompt)
	if shortPrompt == "" {
		shortPrompt = title
	}
	briefingPath := ""
	briefingBody := ""
	switch {
	case cfg.CodexPlanModeEnabled():
		prompt = briefing.RenderManualPlan(title, body)
	case strings.Contains(prompt, "\n"):
		briefingPath = attachedBriefingPath(projectRoot, parentRef, target, number)
		briefingBody = body
		prompt = manualPromptWithBriefing(shortPrompt, briefingPath)
	default:
		prompt = shortPrompt
	}

	req := paneRequest{
		ParentRef:           parentRef,
		Number:              number,
		Title:               title,
		Body:                body,
		ShortTitle:          shortIssueTitle(title),
		Slug:                slug,
		DisplayNameOverride: title,
		BranchName:          strings.TrimSpace(target.SourceBranchName),
		Prompt:              prompt,
		SourceParent:        parentRef,
		SourceIssueNum:      target.SourceIssueNum,
		SourceTaskID:        strings.TrimSpace(target.SourceTaskID),
		Agent:               agentName,
		Hooks:               hookConfig,
		BriefingPath:        briefingPath,
		BriefingBody:        briefingBody,
		CodexPlanMode:       cfg.CodexPlanModeEnabled(),
	}
	if req.CodexPlanMode {
		req.CodexPlanStatusPath = codexPlanStatusPath(projectRoot, number, cfg.DryRun)
	}
	return req
}

func attachedBriefingPath(projectRoot, parentRef string, target fanouttui.AttachTarget, number int) string {
	parentSlug := naming.Slugify(parentRef)
	if parentSlug == "" {
		parentSlug = "manual"
	}
	source := strings.TrimSpace(target.SourceTaskID)
	if source == "" && target.SourceIssueNum > 0 {
		source = fmt.Sprintf("issue-%d", target.SourceIssueNum)
	}
	if source == "" {
		source = strings.TrimSpace(target.SourceLabel)
	}
	sourceSlug := naming.Slugify(source)
	if sourceSlug == "" {
		sourceSlug = "source"
	}
	if number < 0 {
		number = -number
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("fanout-%s-attach-%s-%s-a%d.md", filepath.Base(projectRoot), parentSlug, sourceSlug, number))
}

func attachedPaneTitle(agentName, sourceLabel, targetPath string) string {
	sourceLabel = strings.TrimSpace(sourceLabel)
	if sourceLabel == "" {
		sourceLabel = filepath.Base(targetPath)
	}
	if sourceLabel == "" || sourceLabel == "." || sourceLabel == string(filepath.Separator) {
		sourceLabel = "worktree"
	}
	return agentName + " for " + sourceLabel
}

func attachedPaneSlug(targetPath, agentName string, number int) string {
	base := naming.Slugify(filepath.Base(targetPath))
	if base == "" {
		base = "worktree"
	}
	suffixNum := number
	if suffixNum < 0 {
		suffixNum = -suffixNum
	}
	suffix := fmt.Sprintf("-%s-a%d", naming.Slugify(agentName), suffixNum)
	maxBase := max(naming.MaxSlugLength-len(suffix), 1)
	if len(base) > maxBase {
		base = strings.Trim(base[:maxBase], "-")
		if base == "" {
			base = "worktree"
		}
	}
	return base + suffix
}

func createAttachedPane(cfg *cliflags.Config, lg *log.Logger, info *fanoutruntime.Info, req paneRequest, targetPath string, recorder paneStateRecorder, commandName string) bool {
	agentCmd, err := buildAgentCommand(cfg, req, commandName)
	if err != nil {
		lg.Err("%s: %v", paneLogLabel(req), err)
		return false
	}
	req.AgentCommand = agentCmd
	if req.BriefingPath != "" {
		if err = os.WriteFile(req.BriefingPath, []byte(req.BriefingBody), 0o644); err != nil {
			lg.Err("%s: write briefing: %v", paneLogLabel(req), err)
			return false
		}
	}

	lg.Info("%s: attach %s to %s", paneLogLabel(req), req.Agent, targetPath)
	lg.Dim("  slug -> %s", req.Slug)
	lg.Dim("  worktree -> %s", targetPath)
	if req.CodexPlanMode {
		lg.Dim("  codex-plan-mode -> app-server Plan thread + interactive Codex TUI")
	}
	hooks.RunBackground(hooks.BeforePaneCreate, paneHookContext(req, info.ProjectRoot, targetPath, ""), req.Hooks, lg)

	paneID, err := tmuxrun.SplitPaneWithAgentCommand(info.Target, targetPath, req.AgentCommand)
	if err != nil {
		lg.Err("%s: %v", paneLogLabel(req), err)
		return false
	}
	// The liveness key is not best-effort: a recorded key that never reaches
	// the tmux pane would leave the row permanently stale.
	if req.ShellKey != "" {
		if err := tmuxrun.SetPaneShellKey(paneID, req.ShellKey); err != nil {
			lg.Err("%s: set pane liveness key: %v", paneLogLabel(req), err)
			cleanupFailedAttachedLaunch(info.Target, paneID)
			return false
		}
	}
	if err := tmuxrun.SetPaneTitle(paneID, paneTitle(req)); err != nil {
		lg.Warn("%s: %v", paneLogLabel(req), err)
	}
	if err := tmuxrun.SetPaneLabel(paneID, paneBorderLabel(req)); err != nil {
		lg.Warn("%s: pane border label: %v", paneLogLabel(req), err)
	}
	if err := tmuxrun.EnablePaneBorderTitles(paneID); err != nil {
		lg.Warn("%s: pane border titles: %v", paneLogLabel(req), err)
	}
	if err := tmuxrun.SetPaneProjectRoot(paneID, info.ProjectRoot); err != nil {
		lg.Warn("%s: dashboard project root hint: %v", paneLogLabel(req), err)
	}
	if err := tmuxrun.SetPaneWorktreePath(paneID, targetPath); err != nil {
		lg.Warn("%s: worktree path hint: %v", paneLogLabel(req), err)
	}
	if err := panelayout.Apply(info.Target, panelayout.Create); err != nil {
		lg.Warn("%s: %v", paneLogLabel(req), err)
	}
	codexPlanStatus := codexPlanTUIStatus{}
	if req.CodexPlanMode {
		var planErr error
		codexPlanStatus, planErr = waitForCodexPlanTUIReadyStatus(req.CodexPlanStatusPath, codexPlanTUIStartupTimeout)
		if planErr != nil {
			lg.Err("%s: start Codex Plan Mode TUI in pane %s: %v", paneLogLabel(req), paneID, planErr)
			cleanupFailedAttachedLaunch(info.Target, paneID)
			return false
		}
		_ = os.Remove(req.CodexPlanStatusPath)
	}
	entry := statePane(req, paneID, targetPath, time.Now().UTC(), codexPlanStatus)
	entry.Kind = state.PaneKindAttachedAgent
	if err := recorder.RecordPane(entry); err != nil {
		lg.Err("%s: write fanout state: %v", paneLogLabel(req), err)
		cleanupFailedAttachedLaunch(info.Target, paneID)
		return false
	}
	lg.Ok("%s: pane %s attached to %s", paneLogLabel(req), paneID, targetPath)
	return true
}

func cleanupFailedAttachedLaunch(target, paneID string) {
	_ = tmuxrun.KillPane(paneID)
	_ = panelayout.Apply(target, panelayout.Close)
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
		if i := strings.Index(line, baseRefreshSkippedNotice); i >= 0 {
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
	rawPath := strings.TrimSpace(req.TargetPath)
	if rawPath == "" {
		return fmt.Errorf("terminal path is required")
	}
	targetPath, err := filepath.Abs(rawPath)
	if err != nil {
		return fmt.Errorf("resolve terminal path: %w", err)
	}
	st, statErr := os.Stat(targetPath)
	if statErr != nil {
		return fmt.Errorf("terminal path: %w", statErr)
	} else if !st.IsDir() {
		return fmt.Errorf("terminal path is not a directory: %s", targetPath)
	}

	if excludeErr := worktree.EnsureLocalExclude(projectRoot); excludeErr != nil {
		return fmt.Errorf("prepare local git exclude: %w", excludeErr)
	}

	recorder, err := state.LockProject(projectRoot)
	if err != nil {
		return err
	}
	defer func() {
		_ = recorder.Unlock()
	}()

	number := nextSyntheticPaneNumber(recorder.Store, manualPaneParentRef)
	slug := shellPaneSlug(targetPath, req.Root, number)
	title := shellPaneTitle(targetPath, req.Root)
	shellKey, err := newShellPaneKey()
	if err != nil {
		return err
	}
	paneID, err := tmuxrun.SplitPane(target, targetPath)
	if err != nil {
		return err
	}
	if err := tmuxrun.SetPaneShellKey(paneID, shellKey); err != nil {
		_ = tmuxrun.KillPane(paneID)
		return err
	}
	// Shell pane ergonomics are best-effort; the recorded pane id is enough to
	// keep the terminal usable when tmux metadata/layout updates fail.
	_ = tmuxrun.SetPaneTitle(paneID, title)
	_ = tmuxrun.SetPaneLabel(paneID, borderLabel(manualPaneParentRef, title))
	_ = tmuxrun.EnablePaneBorderTitles(paneID)
	_ = tmuxrun.SetPaneProjectRoot(paneID, projectRoot)
	_ = tmuxrun.SetPaneWorktreePath(paneID, targetPath)
	if err := recorder.RecordPane(state.Pane{
		Parent:       manualPaneParentRef,
		IssueNum:     number,
		Kind:         state.PaneKindShell,
		Slug:         slug,
		PaneID:       paneID,
		ShellKey:     shellKey,
		Agent:        state.PaneKindShell,
		DisplayName:  title,
		WorktreePath: targetPath,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		_ = tmuxrun.KillPane(paneID)
		// Reconcile any spacer a concurrent resize relayout may have created for
		// this now-killed pane, so no blank pane is left behind.
		_ = panelayout.Apply(target, panelayout.Close)
		return fmt.Errorf("write fanout state: %w", err)
	}
	// Re-layout only after the pane is recorded, so a failed/rolled-back launch
	// never leaves the window arranged around a pane that no longer exists or an
	// orphaned spacer behind.
	_ = panelayout.Apply(target, panelayout.Create)
	return nil
}

func launchOwnerProjectRoot(defaultRoot, sourceProjectRoot string) string {
	if root := strings.TrimSpace(sourceProjectRoot); root != "" {
		return root
	}
	return defaultRoot
}

func newShellPaneKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate terminal identity: %w", err)
	}
	return "shell-" + hex.EncodeToString(b[:]), nil
}

func shellPaneSlug(targetPath string, root bool, number int) string {
	base := "root"
	if !root {
		base = sanitizeSessionPart(filepath.Base(targetPath))
	}
	if base == "" {
		base = "terminal"
	}
	n := number
	if n < 0 {
		n = -n
	}
	return fmt.Sprintf("terminal-%s-%d", base, n)
}

func shellPaneTitle(targetPath string, root bool) string {
	if root {
		return "root terminal"
	}
	base := strings.TrimSpace(filepath.Base(targetPath))
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = "worktree"
	}
	return "terminal " + base
}

func manualPaneOptionsForTUI(prompt, agentName string) manualPaneOptions {
	title := firstPromptLine(prompt)
	opts := manualPaneOptions{
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

func firstPromptLine(prompt string) string {
	for line := range strings.Lines(prompt) {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return strings.TrimSpace(prompt)
}
