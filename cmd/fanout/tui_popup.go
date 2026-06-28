package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/log"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
	fanouttui "github.com/butaosuinu/fanout/internal/tui"
	"github.com/butaosuinu/fanout/internal/worktree"
)

const (
	tuiNewPanePopupCommand      = "__tui-new-pane-popup"
	tuiNewPanePopupMinHeight    = 18
	tuiNewPanePopupResultPoll   = 50 * time.Millisecond
	tuiNewPanePopupResultWait   = 24 * time.Hour
	tuiNewPanePopupEnhancedKeys = fanouttui.EnhancedKeysEnv + "=1"
)

type tuiNewPanePopupResult struct {
	Canceled bool     `json:"canceled,omitempty"`
	Prompt   string   `json:"prompt,omitempty"`
	Agents   []string `json:"agents,omitempty"`
	Error    string   `json:"error,omitempty"`
}

func isTUINewPanePopupRequest(args []string) bool {
	return len(args) > 0 && args[0] == tuiNewPanePopupCommand
}

func cmdTUINewPanePopup(args []string, lg *log.Logger) exitcode.Code {
	fs := flag.NewFlagSet(tuiNewPanePopupCommand, flag.ContinueOnError)
	fs.SetOutput(lg.Stderr())
	projectRoot := fs.String("project-root", "", "project root")
	resultFile := fs.String("result-file", "", "result file")
	defaultAgent := fs.String("default-agent", defaultTUIAgent(), "default agent")
	width := fs.Int("width", 90, "prompt width")
	height := fs.Int("height", 24, "prompt height")
	if err := fs.Parse(args); err != nil {
		return exitcode.Invocation
	}
	if strings.TrimSpace(*projectRoot) == "" || strings.TrimSpace(*resultFile) == "" {
		lg.Err("--project-root and --result-file are required")
		return exitcode.Invocation
	}
	req, canceled, err := fanouttui.RunNewPanePrompt(fanouttui.NewPanePromptOptions{
		ProjectRoot:   *projectRoot,
		DefaultAgent:  *defaultAgent,
		Width:         *width,
		Height:        *height,
		ListRepoFiles: worktree.ListFiles,
	})
	result := tuiNewPanePopupResult{Canceled: canceled, Prompt: req.Prompt, Agents: req.Agents}
	code := exitcode.OK
	if err != nil {
		result = tuiNewPanePopupResult{Error: err.Error()}
		code = exitcode.Env
	}
	if writeErr := writeTUINewPanePopupResult(*resultFile, result); writeErr != nil {
		lg.Err("write popup result: %v", writeErr)
		return exitcode.Env
	}
	return code
}

func writeTUINewPanePopupResult(path string, result tuiNewPanePopupResult) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".result-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	wrote := false
	defer func() {
		if !wrote {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	wrote = true
	return nil
}

func newTUINewPanePromptFunc(projectRoot, commandName string) fanouttui.NewPanePromptFunc {
	return func(req fanouttui.NewPanePromptRequest) (fanouttui.LaunchRequest, bool, error) {
		size, err := tmuxrun.CurrentClientSize()
		if err != nil {
			return fanouttui.LaunchRequest{}, false, err
		}
		width, height, err := tuiNewPanePopupSize(size)
		if err != nil {
			return fanouttui.LaunchRequest{}, false, err
		}
		resultFile, doneFile, cleanupPopupResult, err := newPopupResultPaths()
		if err != nil {
			return fanouttui.LaunchRequest{}, false, err
		}
		defer cleanupPopupResult()
		command := tuiNewPanePopupShellCommand(commandName, projectRoot, resultFile, doneFile, req.DefaultAgent, width, height)
		displayErr := tmuxrun.DisplayPopup(tmuxrun.PopupOptions{
			Width:    width,
			Height:   height,
			StartDir: projectRoot,
			Title:    "New agent pane",
			Command:  command,
		})
		result, readErr := readTUINewPanePopupResult(resultFile)
		if os.IsNotExist(readErr) && displayErr == nil {
			result, readErr = waitForTUINewPanePopupResult(resultFile, doneFile, tuiNewPanePopupResultWait)
		}
		if readErr != nil {
			if displayErr != nil {
				return fanouttui.LaunchRequest{}, false, displayErr
			}
			return fanouttui.LaunchRequest{}, false, readErr
		}
		if result.Error != "" {
			return fanouttui.LaunchRequest{}, false, fmt.Errorf("%s", result.Error)
		}
		if result.Canceled {
			return fanouttui.LaunchRequest{}, true, nil
		}
		if displayErr != nil {
			return fanouttui.LaunchRequest{}, false, displayErr
		}
		return fanouttui.LaunchRequest{Prompt: result.Prompt, Agents: result.Agents}, false, nil
	}
}

func tuiNewPanePopupSize(size tmuxrun.ClientSize) (int, int, error) {
	if size.Width < 54 || size.Height-2 < tuiNewPanePopupMinHeight {
		return 0, 0, fmt.Errorf("tmux client is too small for the new pane popup: %dx%d", size.Width, size.Height)
	}
	width := min(90, size.Width-4)
	height := min(int(math.Floor(float64(size.Height)*0.8)), size.Height-2)
	height = max(height, tuiNewPanePopupMinHeight)
	return width, height, nil
}

func newPopupResultPaths() (resultFile, doneFile string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "fanout-new-pane-*")
	if err != nil {
		return "", "", func() {}, err
	}
	cleanup = func() {
		_ = os.RemoveAll(dir)
	}
	return filepath.Join(dir, "result.json"), filepath.Join(dir, "done"), cleanup, nil
}

func tuiNewPanePopupShellCommand(commandName, projectRoot, resultFile, doneFile, defaultAgent string, width, height int) string {
	exe, err := os.Executable()
	if err != nil || strings.TrimSpace(exe) == "" {
		exe = commandName
	}
	parts := []string{
		shellQuote(exe),
		tuiNewPanePopupCommand,
		"--project-root", shellQuote(projectRoot),
		"--result-file", shellQuote(resultFile),
		"--default-agent", shellQuote(defaultAgent),
		"--width", fmt.Sprintf("%d", width),
		"--height", fmt.Sprintf("%d", height),
	}
	if os.Getenv(fanouttui.EnhancedKeysEnv) == "1" {
		parts = append([]string{tuiNewPanePopupEnhancedKeys}, parts...)
	}
	command := strings.Join(parts, " ")
	markDone := "printf '' > " + shellQuote(doneFile)
	return "trap " + shellQuote(markDone) + " EXIT HUP INT TERM; " + command
}

func waitForTUINewPanePopupResult(resultFile, doneFile string, timeout time.Duration) (tuiNewPanePopupResult, error) {
	deadline := time.Now().Add(timeout)
	for {
		result, err := readTUINewPanePopupResult(resultFile)
		if err == nil {
			return result, nil
		}
		if !os.IsNotExist(err) {
			return tuiNewPanePopupResult{}, err
		}
		if _, err := os.Stat(doneFile); err == nil {
			return tuiNewPanePopupResult{Canceled: true}, nil
		} else if !os.IsNotExist(err) {
			return tuiNewPanePopupResult{}, err
		}
		if timeout > 0 && time.Now().After(deadline) {
			return tuiNewPanePopupResult{}, fmt.Errorf("timed out waiting for new pane popup result after %s", timeout)
		}
		time.Sleep(tuiNewPanePopupResultPoll)
	}
}

func readTUINewPanePopupResult(path string) (tuiNewPanePopupResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return tuiNewPanePopupResult{}, err
	}
	var result tuiNewPanePopupResult
	if err := json.Unmarshal(data, &result); err != nil {
		return tuiNewPanePopupResult{}, err
	}
	return result, nil
}
