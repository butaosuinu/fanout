package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/agent"
	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/hooks"
	"github.com/butaosuinu/fanout/internal/log"
	fanoutnotify "github.com/butaosuinu/fanout/internal/notify"
	fanoutruntime "github.com/butaosuinu/fanout/internal/runtime"
	"github.com/butaosuinu/fanout/internal/settings"
	"github.com/butaosuinu/fanout/internal/state"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
	fanouttui "github.com/butaosuinu/fanout/internal/tui"
	"github.com/butaosuinu/fanout/internal/worktree"
)

const tuiPaneTitle = "fanout tui"

func isTUIRequest(args []string) bool {
	return len(args) == 0
}

func cmdTUI(commandName string, lg *log.Logger) exitcode.Code {
	projectRoot, err := tuiProjectRoot()
	if err != nil {
		lg.Err("%s", err.Error())
		return exitcode.Env
	}

	if !tmuxrun.InsideTmux() {
		return enterTUISession(projectRoot, commandName, lg)
	}

	session, err := tmuxrun.CurrentSession()
	if err != nil {
		lg.Err("%s", err.Error())
		return exitcode.Env
	}
	resolvedSettings := settings.Resolve(projectRoot, settings.CLIOverrides{}, lg.Warn)
	hookConfig := hooks.LoadUserConfig(lg)
	notifier, err := fanoutnotify.New(fanoutnotify.Config{
		Channels:        resolvedSettings.Notifications,
		TmuxTarget:      session,
		NtfyURL:         resolvedSettings.NtfyURL,
		SlackWebhookURL: resolvedSettings.SlackWebhookURL,
		BellWriter:      os.Stdout,
	})
	if err != nil {
		lg.Err("notifications: %v", err)
		return exitcode.Env
	}
	restoreTitle := markTUIRunning(projectRoot)
	defer restoreTitle()
	if err := fanouttui.Run(fanouttui.Options{
		ProjectRoot:   projectRoot,
		Session:       session,
		StateInterval: 2 * time.Second,
		GHInterval:    20 * time.Second,
		DefaultAgent:  defaultTUIAgent(),
		Hooks:         hookConfig,
		LaunchPane:    newTUILaunchPaneFunc(projectRoot, session, commandName, hookConfig),
		LaunchShell:   newTUILaunchShellFunc(projectRoot, session),
		Notifier:      notifier,
	}); err != nil {
		lg.Err("tui: %v", err)
		return exitcode.Env
	}
	return exitcode.OK
}

func newTUILaunchPaneFunc(projectRoot, session, commandName string, hookConfig hooks.Config) fanouttui.LaunchFunc {
	return func(req fanouttui.LaunchRequest) error {
		return launchManualPaneFromTUI(projectRoot, session, commandName, hookConfig, req)
	}
}

func launchManualPaneFromTUI(projectRoot, session, commandName string, hookConfig hooks.Config, req fanouttui.LaunchRequest) error {
	prompt := normalizeTUIPrompt(req.Prompt)
	if prompt == "" {
		return fmt.Errorf("prompt is required")
	}
	agentName := strings.TrimSpace(req.Agent)
	if agentName == "" {
		agentName = defaultTUIAgent()
	}
	if err := agent.ValidateKnown(agentName); err != nil {
		return err
	}
	if err := agent.ValidateInstalled(agentName); err != nil {
		return err
	}
	slug, err := normalizeTUISlug(req.Slug)
	if err != nil {
		return err
	}

	var stdout, stderr bytes.Buffer
	launchLogger := log.NewWith(&stdout, &stderr, false)
	cfg := &cliflags.Config{Agent: agentName}
	store, recorder, code := loadRunState(cfg, projectRoot, launchLogger)
	if code != exitcode.OK {
		return bufferedLaunchError(stdout, stderr, "load fanout state")
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
	paneReq := newManualPaneRequest(cfg, projectRoot, store, hookConfig, manualPaneOptionsForTUI(prompt, slug, agentName))
	if !createPane(cfg, launchLogger, info, paneReq, recorder, log.Palette{}, commandName) {
		return bufferedLaunchError(stdout, stderr, "create pane")
	}
	return nil
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

func tuiLaunchTarget(session string) string {
	if pane := strings.TrimSpace(os.Getenv("TMUX_PANE")); pane != "" {
		return pane
	}
	return session
}

func defaultTUIAgent() string {
	return tuiAgentOrDefault(os.Getenv("FANOUT_AGENT"))
}

func tuiAgentOrDefault(agentName string) string {
	if strings.TrimSpace(agentName) == "codex" {
		return "codex"
	}
	return "claude"
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

func bufferedLaunchError(stdout, stderr bytes.Buffer, fallback string) error {
	msg := strings.TrimSpace(stderr.String())
	if msg == "" {
		msg = strings.TrimSpace(stdout.String())
	}
	if msg == "" {
		msg = fallback
	}
	return fmt.Errorf("%s", msg)
}

func enterTUISession(projectRoot, commandName string, lg *log.Logger) exitcode.Code {
	session := fanoutTUISessionName(projectRoot)
	created := false
	if !tmuxrun.HasSession(session) {
		if err := tmuxrun.NewSession(session, projectRoot); err != nil {
			lg.Err("%s", err.Error())
			return exitcode.Env
		}
		created = true
	}

	pane, running, err := findTUIPane(session)
	if err != nil {
		lg.Err("%s", err.Error())
		return exitcode.Env
	}
	if !running {
		if created {
			pane, err = firstSessionPane(session)
		} else {
			pane, err = tmuxrun.NewWindow(session, tuiPaneTitle, projectRoot)
		}
		if err != nil {
			lg.Err("%s", err.Error())
			return exitcode.Env
		}
		if err := tmuxrun.SendKeys(pane.ID, tuiLaunchCommand(commandName, projectRoot), "Enter"); err != nil {
			lg.Err("%s", err.Error())
			return exitcode.Env
		}
	}
	if err := tmuxrun.FocusPane(pane); err != nil {
		lg.Err("%s", err.Error())
		return exitcode.Env
	}
	if err := tmuxrun.AttachOrSwitch(session); err != nil {
		lg.Err("%s", err.Error())
		return exitcode.Env
	}
	return exitcode.OK
}

func markTUIRunning(projectRoot string) func() {
	paneID := strings.TrimSpace(os.Getenv("TMUX_PANE"))
	if paneID == "" {
		return func() {}
	}
	_ = tmuxrun.SetPaneProjectRoot(paneID, projectRoot) // Best-effort dashboard keybinding hint.
	originalTitle, err := tmuxrun.PaneTitle(paneID)
	if err != nil {
		originalTitle = "fanout"
	}
	_ = tmuxrun.SetPaneTitle(paneID, tuiPaneTitle)
	return func() {
		_ = tmuxrun.SetPaneTitle(paneID, originalTitle)
	}
}

func findTUIPane(session string) (tmuxrun.PaneInfo, bool, error) {
	panes, err := tmuxrun.ListPanes(session)
	if err != nil {
		return tmuxrun.PaneInfo{}, false, err
	}
	for _, pane := range panes {
		if pane.Title == tuiPaneTitle {
			return pane, true, nil
		}
	}
	return tmuxrun.PaneInfo{}, false, nil
}

func firstSessionPane(session string) (tmuxrun.PaneInfo, error) {
	panes, err := tmuxrun.ListPanes(session)
	if err != nil {
		return tmuxrun.PaneInfo{}, err
	}
	if len(panes) == 0 {
		return tmuxrun.PaneInfo{}, fmt.Errorf("tmux session %s has no panes", session)
	}
	return panes[0], nil
}

func tuiProjectRoot() (string, error) {
	return resolveDisplayProjectRoot()
}

func fanoutTUISessionName(projectRoot string) string {
	sum := sha1.Sum([]byte(projectRoot))
	base := sanitizeSessionPart(filepath.Base(projectRoot))
	return "fanout-" + base + "-" + hex.EncodeToString(sum[:])[:8]
}

func sanitizeSessionPart(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		allowed := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if allowed {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "repo"
	}
	return out
}

func tuiLaunchCommand(commandName, projectRoot string) string {
	exe, err := os.Executable()
	if err != nil || strings.TrimSpace(exe) == "" {
		exe = commandName
	}
	return "cd " + shellQuote(projectRoot) + " && " + shellQuote(exe)
}
