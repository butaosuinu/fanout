package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/agent"
	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/log"
	fanoutruntime "github.com/butaosuinu/fanout/internal/runtime"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
	fanouttui "github.com/butaosuinu/fanout/internal/tui"
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
	restoreTitle := markTUIRunning()
	defer restoreTitle()
	if err := fanouttui.Run(fanouttui.Options{
		ProjectRoot:   projectRoot,
		Session:       session,
		StateInterval: 2 * time.Second,
		GHInterval:    20 * time.Second,
		DefaultAgent:  defaultTUIAgent(),
		LaunchPane:    newTUILaunchPaneFunc(projectRoot, session, commandName),
	}); err != nil {
		lg.Err("tui: %v", err)
		return exitcode.Env
	}
	return exitcode.OK
}

func newTUILaunchPaneFunc(projectRoot, session, commandName string) fanouttui.LaunchFunc {
	return func(req fanouttui.LaunchRequest) error {
		return launchManualPaneFromTUI(projectRoot, session, commandName, req)
	}
}

func launchManualPaneFromTUI(projectRoot, session, commandName string, req fanouttui.LaunchRequest) error {
	prompt := strings.TrimSpace(req.Prompt)
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
	paneReq := newManualPaneRequest(cfg, projectRoot, store, manualPaneOptions{
		Title:  prompt,
		Slug:   slug,
		Agent:  agentName,
		Prompt: prompt,
	})
	if !createPane(cfg, launchLogger, info, paneReq, recorder, log.Palette{}, commandName) {
		return bufferedLaunchError(stdout, stderr, "create pane")
	}
	return nil
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

func markTUIRunning() func() {
	paneID := strings.TrimSpace(os.Getenv("TMUX_PANE"))
	if paneID == "" {
		return func() {}
	}
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
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("current directory is not inside a git work tree")
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", fmt.Errorf("git rev-parse --show-toplevel returned an empty path")
	}
	return root, nil
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
