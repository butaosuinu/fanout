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
	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/hooks"
	"github.com/butaosuinu/fanout/internal/log"
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

func launchManualPaneFromTUI(projectRoot, session, commandName string, hookConfig hooks.Config, req fanouttui.LaunchRequest) (string, error) {
	prompt := normalizeTUIPrompt(req.Prompt)
	if prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}
	agentNames := normalizeTUIAgents(req.Agents)
	for _, agentName := range agentNames {
		if err := agent.ValidateKnown(agentName); err != nil {
			return "", err
		}
		if err := agent.ValidateInstalled(agentName); err != nil {
			return "", err
		}
	}
	slug, err := normalizeTUISlug(req.Slug)
	if err != nil {
		return "", err
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
	for i, agentName := range agentNames {
		cfg = manualPaneConfigForTUIAgent(agentName)
		paneSlug := manualPaneSlugForAgent(slug, agentName, i, agentNames)
		paneReq := newManualPaneRequest(cfg, projectRoot, recorder.Store, hookConfig, manualPaneOptionsForTUI(prompt, paneSlug, agentName))
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

func partialManualLaunchNotice(createdCount int, stderr bytes.Buffer) string {
	notice := fmt.Sprintf("created %d new agent pane(s); stopped after a later pane failed", createdCount)
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
	paneID, err := tmuxrun.SplitPane(tuiLaunchTarget(session), targetPath)
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
	_ = tmuxrun.SetPaneProjectRoot(paneID, projectRoot)
	_ = tmuxrun.SelectTiled(tuiLaunchTarget(session))
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
		return fmt.Errorf("write fanout state: %w", err)
	}
	return nil
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

func manualPaneOptionsForTUI(prompt, slug, agentName string) manualPaneOptions {
	title := firstPromptLine(prompt)
	opts := manualPaneOptions{
		Title:  title,
		Slug:   slug,
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

func manualPaneSlugForAgent(slug, agentName string, index int, agents []string) string {
	if slug == "" || len(agents) == 1 {
		return slug
	}
	suffix := agentName
	seen := 0
	totalForAgent := 0
	for i, name := range agents {
		if name != agentName {
			continue
		}
		totalForAgent++
		if i <= index {
			seen++
		}
	}
	if totalForAgent > 1 {
		suffix = fmt.Sprintf("%s-%s", agentName, launchOrdinal(seen))
	}
	return strings.TrimRight(slug, "-") + "-" + suffix
}

func launchOrdinal(n int) string {
	switch n {
	case 1:
		return "one"
	case 2:
		return "two"
	case 3:
		return "three"
	default:
		return fmt.Sprintf("run%d", n)
	}
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

func normalizeTUISlug(raw string) (string, error) {
	slug := strings.TrimSpace(raw)
	if slug == "" {
		return "", nil
	}
	if !isKebabSlug(slug) {
		return "", fmt.Errorf("slug must be lowercase kebab-case (alnum+hyphens, starting with alnum), got: %q", slug)
	}
	if hasIssueLikeNumericSuffix(slug) {
		return "", fmt.Errorf("manual slug must not end with an issue-like numeric suffix: %q", slug)
	}
	return slug, nil
}

func isKebabSlug(slug string) bool {
	for i, r := range slug {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i > 0:
		default:
			return false
		}
	}
	return slug != ""
}

func hasIssueLikeNumericSuffix(slug string) bool {
	i := strings.LastIndex(slug, "-")
	if i < 0 || i == len(slug)-1 {
		return false
	}
	for _, r := range slug[i+1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
