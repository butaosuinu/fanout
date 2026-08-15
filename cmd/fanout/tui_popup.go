package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/app/lifecycle"
	"github.com/butaosuinu/fanout/internal/app/run"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/log"
	fanoutsettings "github.com/butaosuinu/fanout/internal/infra/settings"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
	fanouttui "github.com/butaosuinu/fanout/internal/ui/tui"
)

const (
	tuiClosePopupCommand       = "__tui-close-popup"
	tuiClosePopupMinHeight     = 10
	tuiHelpPopupCommand        = "__tui-help-popup"
	tuiHelpPopupMinHeight      = 21
	tuiNewPanePopupCommand     = "__tui-new-pane-popup"
	tuiNewPanePopupMinHeight   = 20
	tuiNewPanePopupMinWidth    = 54
	tuiSettingsPopupCommand    = "__tui-settings-popup"
	tuiSettingsPopupMinHeight  = 18
	tuiSettingsPopupMinWidth   = 54
	tuiNewPanePopupBorderInset = 2
	tuiNewPanePopupResultPoll  = 50 * time.Millisecond
	tuiNewPanePopupResultWait  = 24 * time.Hour
)

type tuiClosePopupGeometry struct {
	PopupWidth    int
	PopupHeight   int
	ContentWidth  int
	ContentHeight int
}

type tuiHelpPopupGeometry struct {
	PopupWidth    int
	PopupHeight   int
	ContentWidth  int
	ContentHeight int
}

type tuiNewPanePopupGeometry struct {
	PopupWidth   int
	PopupHeight  int
	PromptWidth  int
	PromptHeight int
}

type tuiNewPanePopupResult struct {
	Canceled       bool              `json:"canceled,omitempty"`
	Mode           string            `json:"mode,omitempty"` // "" (prompt) | "issue"
	Prompt         string            `json:"prompt,omitempty"`
	Issue          int               `json:"issue,omitempty"`
	PlanFanout     bool              `json:"planFanout,omitempty"`
	Agents         []string          `json:"agents,omitempty"`
	DefaultAgent   string            `json:"defaultAgent,omitempty"`
	AgentOverrides map[string]string `json:"agentOverrides,omitempty"`
	WorkerAgent    string            `json:"workerAgent,omitempty"`
	Error          string            `json:"error,omitempty"`
}

type tuiClosePopupRequest struct {
	PaneLabel       string `json:"paneLabel,omitempty"`
	Mode            string `json:"mode,omitempty"`
	RequireWorktree bool   `json:"requireWorktree,omitempty"`
}

type tuiClosePopupResult struct {
	Canceled bool   `json:"canceled,omitempty"`
	Mode     string `json:"mode,omitempty"`
	Error    string `json:"error,omitempty"`
}

type tuiSettingsPopupResult struct {
	Canceled bool   `json:"canceled,omitempty"`
	Saved    bool   `json:"saved,omitempty"`
	Scope    string `json:"scope,omitempty"`
	Path     string `json:"path,omitempty"`
	Error    string `json:"error,omitempty"`
}

func isTUINewPanePopupRequest(args []string) bool {
	return len(args) > 0 && args[0] == tuiNewPanePopupCommand
}

func isTUIHelpPopupRequest(args []string) bool {
	return len(args) > 0 && args[0] == tuiHelpPopupCommand
}

func isTUIClosePopupRequest(args []string) bool {
	return len(args) > 0 && args[0] == tuiClosePopupCommand
}

func isTUISettingsPopupRequest(args []string) bool {
	return len(args) > 0 && args[0] == tuiSettingsPopupCommand
}

func cmdTUIHelpPopup(args []string, lg *log.Logger) exitcode.Code {
	fs := flag.NewFlagSet(tuiHelpPopupCommand, flag.ContinueOnError)
	fs.SetOutput(lg.Stderr())
	width := fs.Int("width", 76, "help width")
	height := fs.Int("height", tuiHelpPopupMinHeight, "help height")
	if err := fs.Parse(args); err != nil {
		return exitcode.Invocation
	}
	if err := fanouttui.RunHelpPopup(fanouttui.HelpPopupOptions{
		Width:  *width,
		Height: *height,
	}); err != nil {
		lg.Err("help popup: %v", err)
		return exitcode.Env
	}
	return exitcode.OK
}

func cmdTUIClosePopup(args []string, lg *log.Logger) exitcode.Code {
	fs := flag.NewFlagSet(tuiClosePopupCommand, flag.ContinueOnError)
	fs.SetOutput(lg.Stderr())
	requestFile := fs.String("request-file", "", "request file")
	resultFile := fs.String("result-file", "", "result file")
	width := fs.Int("width", 72, "popup width")
	height := fs.Int("height", 10, "popup height")
	if err := fs.Parse(args); err != nil {
		return exitcode.Invocation
	}
	if strings.TrimSpace(*requestFile) == "" || strings.TrimSpace(*resultFile) == "" {
		lg.Err("--request-file and --result-file are required")
		return exitcode.Invocation
	}

	request, err := readTUIClosePopupRequest(*requestFile)
	result, code := runTUIClosePopupRequest(request, err, *width, *height)
	if writeErr := writeTUIClosePopupResult(*resultFile, result); writeErr != nil {
		lg.Err("write close popup result: %v", writeErr)
		return exitcode.Env
	}
	return code
}

func runTUIClosePopupRequest(request tuiClosePopupRequest, readErr error, width, height int) (tuiClosePopupResult, exitcode.Code) {
	if readErr != nil {
		return tuiClosePopupResult{Error: readErr.Error()}, exitcode.Env
	}
	mode, err := parseCloseModeName(request.Mode)
	if err != nil {
		return tuiClosePopupResult{Error: err.Error()}, exitcode.Invocation
	}
	selected, canceled, err := fanouttui.RunCloseChoicePopup(fanouttui.CloseChoicePopupOptions{
		PaneLabel: request.PaneLabel, InitialMode: mode, RequireWorktree: request.RequireWorktree,
		Width: width, Height: height,
	})
	if err != nil {
		return tuiClosePopupResult{Error: err.Error()}, exitcode.Env
	}
	return tuiClosePopupResult{Canceled: canceled, Mode: closeModeName(selected)}, exitcode.OK
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
		ProjectRoot:       *projectRoot,
		DefaultAgent:      *defaultAgent,
		Width:             *width,
		Height:            *height,
		ListRepoFiles:     worktree.ListFiles,
		ListOpenIssues:    newTUIListOpenIssuesFunc(*projectRoot),
		ListIssueChildren: newTUIListIssueChildrenFunc(*projectRoot),
		OpenIssue:         newTUIOpenIssueFunc(*projectRoot),
	})
	result := tuiNewPanePopupResult{
		Canceled:       canceled,
		Mode:           string(req.Mode),
		Prompt:         req.Prompt,
		Issue:          req.Issue,
		PlanFanout:     req.PlanFanout,
		Agents:         req.Agents,
		DefaultAgent:   req.DefaultAgent,
		AgentOverrides: req.AgentOverrides,
		WorkerAgent:    req.WorkerAgent,
	}
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

func cmdTUISettingsPopup(args []string, lg *log.Logger) exitcode.Code {
	fs := flag.NewFlagSet(tuiSettingsPopupCommand, flag.ContinueOnError)
	fs.SetOutput(lg.Stderr())
	projectRoot := fs.String("project-root", "", "project root")
	resultFile := fs.String("result-file", "", "result file")
	width := fs.Int("width", 90, "popup width")
	height := fs.Int("height", 24, "popup height")
	if err := fs.Parse(args); err != nil {
		return exitcode.Invocation
	}
	if strings.TrimSpace(*projectRoot) == "" || strings.TrimSpace(*resultFile) == "" {
		lg.Err("--project-root and --result-file are required")
		return exitcode.Invocation
	}
	result, canceled, err := fanouttui.RunSettingsPopup(fanouttui.SettingsPopupOptions{
		ProjectRoot: *projectRoot,
		Width:       *width,
		Height:      *height,
	})
	out := tuiSettingsPopupResult{
		Canceled: canceled,
		Saved:    result.Saved,
		Scope:    result.Scope,
		Path:     result.Path,
	}
	code := exitcode.OK
	if err != nil {
		out = tuiSettingsPopupResult{Error: err.Error()}
		code = exitcode.Env
	}
	if writeErr := writeTUISettingsPopupResult(*resultFile, out); writeErr != nil {
		lg.Err("write settings popup result: %v", writeErr)
		return exitcode.Env
	}
	return code
}

func writeTUINewPanePopupResult(path string, result tuiNewPanePopupResult) error {
	return writePopupJSON(path, result)
}

func writeTUIClosePopupRequest(path string, request tuiClosePopupRequest) error {
	return writePopupJSON(path, request)
}

func writeTUIClosePopupResult(path string, result tuiClosePopupResult) error {
	return writePopupJSON(path, result)
}

func writeTUISettingsPopupResult(path string, result tuiSettingsPopupResult) error {
	return writePopupJSON(path, result)
}

func writePopupJSON(path string, value any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(value)
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

// errPopupHostUnavailable reports a runtime that draws no popups. The console
// gates its popup-driven actions on the capability before offering them, so
// reaching this means a pane outlived the runtime that created it.
var errPopupHostUnavailable = errors.New("runtime backend does not draw popups")

// tuiPopupHost is the console's popup drawing context: the runtime capability
// that measures the terminal and draws the popup, plus the pane the console
// itself runs in, which places each popup beside rather than over it.
//
// The popup layout arithmetic below stays here on purpose. It is entangled with
// the shell command each popup runs and with the operator-facing minimum-size
// messages, so only the three measure-and-draw calls cross into the runtime;
// the math works on core DTOs and stays identical for every runtime.
type tuiPopupHost struct {
	display backend.PopupHost
	// consolePane is the runtime-native id of the pane the console runs in, empty
	// when the environment does not name one. Then popups fall back to centering.
	consolePane string
}

// newTUIPopupHost resolves the popup capability of the console's runtime.
func newTUIPopupHost(runtimeBackend backend.Backend, consolePane string) tuiPopupHost {
	display, _ := backend.AsPopupHost(runtimeBackend)
	return tuiPopupHost{display: display, consolePane: strings.TrimSpace(consolePane)}
}

func (h tuiPopupHost) clientSize() (backend.ClientSize, error) {
	if h.display == nil {
		return backend.ClientSize{}, errPopupHostUnavailable
	}
	return h.display.CurrentClientSize()
}

func (h tuiPopupHost) show(opts backend.PopupOptions) error {
	if h.display == nil {
		return errPopupHostUnavailable
	}
	return h.display.ShowPopup(opts)
}

// positionBesideConsole places a popup next to the console pane. Every failure
// degrades to a nil position, which centers the popup — a popup that cannot be
// placed precisely is still worth showing.
func (h tuiPopupHost) positionBesideConsole(popupWidth, popupHeight int) *backend.PopupPosition {
	if h.display == nil || h.consolePane == "" {
		return nil
	}
	geom, err := h.display.PaneGeometryForPane(h.consolePane)
	if err != nil {
		return nil
	}
	return tuiPopupPositionAdjacentToPane(geom, popupWidth, popupHeight)
}

func newTUIHelpPopupFunc(host tuiPopupHost, projectRoot, commandName string) fanouttui.HelpPopupFunc {
	return func() error {
		size, err := host.clientSize()
		if err != nil {
			return err
		}
		geometry, err := tuiHelpPopupGeometryForClient(size)
		if err != nil {
			return err
		}
		return host.show(backend.PopupOptions{
			Width:    geometry.PopupWidth,
			Height:   geometry.PopupHeight,
			StartDir: projectRoot,
			Title:    "Keyboard shortcuts",
			Command:  tuiHelpPopupShellCommand(commandName, geometry.ContentWidth, geometry.ContentHeight),
			Position: host.positionBesideConsole(geometry.PopupWidth, geometry.PopupHeight),
		})
	}
}

func newTUICloseChoicePopupFunc(host tuiPopupHost, projectRoot, commandName string) fanouttui.CloseChoicePopupFunc {
	return func(req fanouttui.CloseChoiceRequest) (lifecycle.CloseMode, bool, error) {
		launch, err := startTUICloseChoicePopup(host, projectRoot, commandName, req)
		if err != nil {
			return lifecycle.ClosePaneOnly, false, err
		}
		defer launch.cleanup()
		return finishTUICloseChoicePopup(launch)
	}
}

type tuiClosePopupLaunch struct {
	resultFile string
	doneFile   string
	displayErr error
	cleanup    func()
}

func startTUICloseChoicePopup(host tuiPopupHost, projectRoot, commandName string, req fanouttui.CloseChoiceRequest) (tuiClosePopupLaunch, error) {
	size, err := host.clientSize()
	if err != nil {
		return tuiClosePopupLaunch{}, err
	}
	geometry, err := tuiClosePopupGeometryForClient(size)
	if err != nil {
		return tuiClosePopupLaunch{}, err
	}
	resultFile, doneFile, cleanup, err := newPopupResultPaths()
	if err != nil {
		return tuiClosePopupLaunch{}, err
	}
	requestFile := filepath.Join(filepath.Dir(resultFile), "request.json")
	request := tuiClosePopupRequest{
		PaneLabel: req.PaneLabel, Mode: closeModeName(req.InitialMode), RequireWorktree: req.RequireWorktree,
	}
	if err := writeTUIClosePopupRequest(requestFile, request); err != nil {
		cleanup()
		return tuiClosePopupLaunch{}, err
	}
	displayErr := host.show(tuiClosePopupDisplayOptions(
		host, projectRoot, commandName, requestFile, resultFile, doneFile, geometry,
	))
	return tuiClosePopupLaunch{
		resultFile: resultFile, doneFile: doneFile, displayErr: displayErr, cleanup: cleanup,
	}, nil
}

func tuiClosePopupDisplayOptions(
	host tuiPopupHost,
	projectRoot, commandName, requestFile, resultFile, doneFile string,
	geometry tuiClosePopupGeometry,
) backend.PopupOptions {
	return backend.PopupOptions{
		Width: geometry.PopupWidth, Height: geometry.PopupHeight,
		StartDir: projectRoot, Title: "Close pane",
		Command: tuiClosePopupShellCommand(
			commandName, requestFile, resultFile, doneFile, geometry.ContentWidth, geometry.ContentHeight,
		),
		Position: host.positionBesideConsole(geometry.PopupWidth, geometry.PopupHeight),
	}
}

func finishTUICloseChoicePopup(launch tuiClosePopupLaunch) (lifecycle.CloseMode, bool, error) {
	result, readErr := readTUIClosePopupResult(launch.resultFile)
	if os.IsNotExist(readErr) && launch.displayErr == nil {
		result, readErr = waitForTUIClosePopupResult(launch.resultFile, launch.doneFile, tuiNewPanePopupResultWait)
	}
	if readErr != nil {
		if launch.displayErr != nil {
			return lifecycle.ClosePaneOnly, false, launch.displayErr
		}
		return lifecycle.ClosePaneOnly, false, readErr
	}
	if result.Error != "" {
		return lifecycle.ClosePaneOnly, false, fmt.Errorf("%s", result.Error)
	}
	if result.Canceled {
		return lifecycle.ClosePaneOnly, true, nil
	}
	if launch.displayErr != nil {
		return lifecycle.ClosePaneOnly, false, launch.displayErr
	}
	mode, err := parseCloseModeName(result.Mode)
	return mode, false, err
}

func newTUINewPanePromptFunc(host tuiPopupHost, projectRoot, commandName string) fanouttui.NewPanePromptFunc {
	return func(req fanouttui.NewPanePromptRequest) (fanouttui.LaunchRequest, bool, error) {
		size, err := host.clientSize()
		if err != nil {
			return fanouttui.LaunchRequest{}, false, err
		}
		geometry, err := tuiNewPanePopupGeometryForClient(size)
		if err != nil {
			return fanouttui.LaunchRequest{}, false, err
		}
		resultFile, doneFile, cleanupPopupResult, err := newPopupResultPaths()
		if err != nil {
			return fanouttui.LaunchRequest{}, false, err
		}
		defer cleanupPopupResult()
		command := tuiNewPanePopupShellCommand(
			commandName,
			projectRoot,
			resultFile,
			doneFile,
			req.DefaultAgent,
			geometry.PromptWidth,
			geometry.PromptHeight,
		)
		displayErr := host.show(backend.PopupOptions{
			Width:    geometry.PopupWidth,
			Height:   geometry.PopupHeight,
			StartDir: projectRoot,
			Title:    "New agent pane",
			Command:  command,
			Position: host.positionBesideConsole(geometry.PopupWidth, geometry.PopupHeight),
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
		return fanouttui.LaunchRequest{
			Mode:           fanouttui.LaunchMode(result.Mode),
			Prompt:         result.Prompt,
			Issue:          result.Issue,
			PlanFanout:     result.PlanFanout,
			Agents:         result.Agents,
			DefaultAgent:   result.DefaultAgent,
			AgentOverrides: result.AgentOverrides,
			WorkerAgent:    result.WorkerAgent,
		}, false, nil
	}
}

func newTUISettingsPopupFunc(host tuiPopupHost, projectRoot, commandName string) fanouttui.SettingsPopupFunc {
	return func(fanouttui.SettingsPopupRequest) (fanouttui.SettingsPopupResult, bool, error) {
		size, err := host.clientSize()
		if err != nil {
			return fanouttui.SettingsPopupResult{}, false, err
		}
		geometry, err := tuiSettingsPopupGeometryForClient(size)
		if err != nil {
			return fanouttui.SettingsPopupResult{}, false, err
		}
		resultFile, doneFile, cleanupPopupResult, err := newPopupResultPaths()
		if err != nil {
			return fanouttui.SettingsPopupResult{}, false, err
		}
		defer cleanupPopupResult()
		command := tuiSettingsPopupShellCommand(
			commandName,
			projectRoot,
			resultFile,
			doneFile,
			geometry.PromptWidth,
			geometry.PromptHeight,
		)
		displayErr := host.show(backend.PopupOptions{
			Width:    geometry.PopupWidth,
			Height:   geometry.PopupHeight,
			StartDir: projectRoot,
			Title:    "Settings",
			Command:  command,
			Position: host.positionBesideConsole(geometry.PopupWidth, geometry.PopupHeight),
		})
		result, readErr := readTUISettingsPopupResult(resultFile)
		if os.IsNotExist(readErr) && displayErr == nil {
			result, readErr = waitForTUISettingsPopupResult(resultFile, doneFile, tuiNewPanePopupResultWait)
		}
		if readErr != nil {
			if displayErr != nil {
				return fanouttui.SettingsPopupResult{}, false, displayErr
			}
			return fanouttui.SettingsPopupResult{}, false, readErr
		}
		if result.Error != "" {
			return fanouttui.SettingsPopupResult{}, false, fmt.Errorf("%s", result.Error)
		}
		if result.Canceled {
			return fanouttui.SettingsPopupResult{}, true, nil
		}
		if displayErr != nil {
			return fanouttui.SettingsPopupResult{}, false, displayErr
		}
		return fanouttui.SettingsPopupResult{
			Saved: result.Saved,
			Scope: result.Scope,
			Path:  result.Path,
		}, false, nil
	}
}

func tuiPopupPositionAdjacentToPane(geom backend.PaneGeometry, popupWidth, popupHeight int) *backend.PopupPosition {
	if popupWidth <= 0 || popupHeight <= 0 || geom.ClientWidth <= 0 || geom.ClientHeight <= 0 {
		return nil
	}
	maxX := max(geom.ClientWidth-popupWidth, 0)
	maxY := max(geom.ClientHeight-popupHeight, 0)
	rightX := geom.Left + geom.Width + 1
	leftX := geom.Left - popupWidth - 1
	x := rightX
	switch {
	case rightX <= maxX:
	case leftX >= 0:
		x = leftX
	default:
		x = nearestHorizontalPopupEdge(geom, popupWidth)
	}
	return &backend.PopupPosition{
		X: min(max(x, 0), maxX),
		Y: min(max(geom.Top, 0), maxY),
	}
}

func nearestHorizontalPopupEdge(geom backend.PaneGeometry, popupWidth int) int {
	leftOverlap := overlapWidth(0, popupWidth, geom.Left, geom.Left+geom.Width)
	rightX := max(geom.ClientWidth-popupWidth, 0)
	rightOverlap := overlapWidth(rightX, rightX+popupWidth, geom.Left, geom.Left+geom.Width)
	if leftOverlap <= rightOverlap {
		return 0
	}
	return rightX
}

func overlapWidth(aStart, aEnd, bStart, bEnd int) int {
	return max(0, min(aEnd, bEnd)-max(aStart, bStart))
}

func tuiHelpPopupGeometryForClient(size backend.ClientSize) (tuiHelpPopupGeometry, error) {
	minPopupHeight := tuiHelpPopupMinHeight + tuiNewPanePopupBorderInset
	if size.Width < 54 || size.Height < minPopupHeight {
		return tuiHelpPopupGeometry{}, fmt.Errorf("tmux client is too small for the help popup: %dx%d", size.Width, size.Height)
	}
	popupWidth := min(90, size.Width-4)
	targetPopupHeight := min(int(math.Floor(float64(size.Height)*0.8)), size.Height-2)
	popupHeight := max(targetPopupHeight, minPopupHeight)
	// Bordered tmux display-popup subtracts the frame from the child pty.
	return tuiHelpPopupGeometry{
		PopupWidth:    popupWidth,
		PopupHeight:   popupHeight,
		ContentWidth:  popupWidth - tuiNewPanePopupBorderInset,
		ContentHeight: popupHeight - tuiNewPanePopupBorderInset,
	}, nil
}

func tuiClosePopupGeometryForClient(size backend.ClientSize) (tuiClosePopupGeometry, error) {
	minPopupHeight := tuiClosePopupMinHeight + tuiNewPanePopupBorderInset
	if size.Width < 54 || size.Height < minPopupHeight {
		return tuiClosePopupGeometry{}, fmt.Errorf("tmux client is too small for the close popup: %dx%d", size.Width, size.Height)
	}
	popupWidth := min(78, size.Width-4)
	targetPopupHeight := min(int(math.Floor(float64(size.Height)*0.45)), size.Height-2)
	popupHeight := max(targetPopupHeight, minPopupHeight)
	return tuiClosePopupGeometry{
		PopupWidth:    popupWidth,
		PopupHeight:   popupHeight,
		ContentWidth:  popupWidth - tuiNewPanePopupBorderInset,
		ContentHeight: popupHeight - tuiNewPanePopupBorderInset,
	}, nil
}

func tuiNewPanePopupGeometryForClient(size backend.ClientSize) (tuiNewPanePopupGeometry, error) {
	minPopupHeight := tuiNewPanePopupMinHeight + tuiNewPanePopupBorderInset
	minClientWidth := tuiNewPanePopupMinWidth + tuiNewPanePopupBorderInset + 4
	if size.Width < minClientWidth || size.Height < minPopupHeight {
		return tuiNewPanePopupGeometry{}, fmt.Errorf("tmux client is too small for the new pane popup: %dx%d", size.Width, size.Height)
	}
	popupWidth := min(90, size.Width-4)
	targetPopupHeight := min(int(math.Floor(float64(size.Height)*0.8)), size.Height-2)
	popupHeight := max(targetPopupHeight, minPopupHeight)
	// Bordered tmux display-popup subtracts the frame from the child pty.
	return tuiNewPanePopupGeometry{
		PopupWidth:   popupWidth,
		PopupHeight:  popupHeight,
		PromptWidth:  popupWidth - tuiNewPanePopupBorderInset,
		PromptHeight: popupHeight - tuiNewPanePopupBorderInset,
	}, nil
}

func tuiSettingsPopupGeometryForClient(size backend.ClientSize) (tuiNewPanePopupGeometry, error) {
	minPopupHeight := tuiSettingsPopupMinHeight + tuiNewPanePopupBorderInset
	minClientWidth := tuiSettingsPopupMinWidth + tuiNewPanePopupBorderInset + 4
	if size.Width < minClientWidth || size.Height < minPopupHeight {
		return tuiNewPanePopupGeometry{}, fmt.Errorf("tmux client is too small for the settings popup: %dx%d", size.Width, size.Height)
	}
	popupWidth := min(90, size.Width-4)
	targetPopupHeight := min(int(math.Floor(float64(size.Height)*0.8)), size.Height-2)
	popupHeight := max(targetPopupHeight, minPopupHeight)
	return tuiNewPanePopupGeometry{
		PopupWidth:   popupWidth,
		PopupHeight:  popupHeight,
		PromptWidth:  popupWidth - tuiNewPanePopupBorderInset,
		PromptHeight: popupHeight - tuiNewPanePopupBorderInset,
	}, nil
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

func tuiHelpPopupShellCommand(commandName string, width, height int) string {
	exe, err := os.Executable()
	if err != nil || strings.TrimSpace(exe) == "" {
		exe = commandName
	}
	parts := []string{
		run.ShellQuote(exe),
		tuiHelpPopupCommand,
		"--width", fmt.Sprintf("%d", width),
		"--height", fmt.Sprintf("%d", height),
	}
	// display-popup runs under the tmux server's environment. Forward PATH so a
	// fallback command name resolves the same way as the parent fanout process.
	if path := os.Getenv("PATH"); path != "" {
		parts = append([]string{"PATH=" + run.ShellQuote(path)}, parts...)
	}
	return strings.Join(parts, " ")
}

func tuiClosePopupShellCommand(commandName, requestFile, resultFile, doneFile string, width, height int) string {
	exe, err := os.Executable()
	if err != nil || strings.TrimSpace(exe) == "" {
		exe = commandName
	}
	parts := []string{
		run.ShellQuote(exe),
		tuiClosePopupCommand,
		"--request-file", run.ShellQuote(requestFile),
		"--result-file", run.ShellQuote(resultFile),
		"--width", fmt.Sprintf("%d", width),
		"--height", fmt.Sprintf("%d", height),
	}
	prefix := ""
	if path := os.Getenv("PATH"); path != "" {
		prefix = "PATH=" + run.ShellQuote(path) + " "
	}
	command := prefix + strings.Join(parts, " ")
	markDone := "printf '' > " + run.ShellQuote(doneFile)
	return "trap " + run.ShellQuote(markDone) + " EXIT HUP INT TERM; " + command
}

func tuiNewPanePopupShellCommand(commandName, projectRoot, resultFile, doneFile, defaultAgent string, width, height int) string {
	exe, err := os.Executable()
	if err != nil || strings.TrimSpace(exe) == "" {
		exe = commandName
	}
	parts := []string{
		run.ShellQuote(exe),
		tuiNewPanePopupCommand,
		"--project-root", run.ShellQuote(projectRoot),
		"--result-file", run.ShellQuote(resultFile),
		"--default-agent", run.ShellQuote(defaultAgent),
		"--width", fmt.Sprintf("%d", width),
		"--height", fmt.Sprintf("%d", height),
	}
	// display-popup runs under the tmux server's environment, not the parent
	// fanout process's. Forward non-secret path environment so helper commands
	// resolve tools and config files the same way as the parent process.
	prefix := tuiPopupShellEnvPrefix()
	parts = append([]string{prefix}, parts...)
	command := strings.Join(parts, " ")
	markDone := "printf '' > " + run.ShellQuote(doneFile)
	return "trap " + run.ShellQuote(markDone) + " EXIT HUP INT TERM; " + command
}

func tuiSettingsPopupShellCommand(commandName, projectRoot, resultFile, doneFile string, width, height int) string {
	exe, err := os.Executable()
	if err != nil || strings.TrimSpace(exe) == "" {
		exe = commandName
	}
	parts := []string{
		run.ShellQuote(exe),
		tuiSettingsPopupCommand,
		"--project-root", run.ShellQuote(projectRoot),
		"--result-file", run.ShellQuote(resultFile),
		"--width", fmt.Sprintf("%d", width),
		"--height", fmt.Sprintf("%d", height),
	}
	prefix := tuiPopupShellEnvPrefix()
	parts = append([]string{prefix}, parts...)
	command := strings.Join(parts, " ")
	markDone := "printf '' > " + run.ShellQuote(doneFile)
	return "trap " + run.ShellQuote(markDone) + " EXIT HUP INT TERM; " + command
}

func tuiPopupShellEnvPrefix() string {
	parts := []string{
		fanouttui.SettingsEnvOverridesEnv + "=" + run.ShellQuote(settingsEnvOverrideNames()),
		fanouttui.EnhancedKeysEnv + "=" + run.ShellQuote(os.Getenv(fanouttui.EnhancedKeysEnv)),
	}
	for _, key := range []string{"XDG_CONFIG_HOME", "HOME", "PATH"} {
		if value, ok := os.LookupEnv(key); ok || key != "PATH" {
			parts = append([]string{key + "=" + run.ShellQuote(value)}, parts...)
		}
	}
	return strings.Join(parts, " ")
}

func settingsEnvOverrideNames() string {
	names := []string{}
	for _, spec := range fanoutsettings.ConfigKeys() {
		if spec.Env == "" {
			continue
		}
		if _, ok := os.LookupEnv(spec.Env); ok {
			names = append(names, spec.Env)
		}
	}
	return strings.Join(names, ",")
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

func waitForTUIClosePopupResult(resultFile, doneFile string, timeout time.Duration) (tuiClosePopupResult, error) {
	deadline := time.Now().Add(timeout)
	for {
		result, err := readTUIClosePopupResult(resultFile)
		if err == nil {
			return result, nil
		}
		if !os.IsNotExist(err) {
			return tuiClosePopupResult{}, err
		}
		if _, err := os.Stat(doneFile); err == nil {
			return tuiClosePopupResult{Canceled: true}, nil
		} else if !os.IsNotExist(err) {
			return tuiClosePopupResult{}, err
		}
		if timeout > 0 && time.Now().After(deadline) {
			return tuiClosePopupResult{}, fmt.Errorf("timed out waiting for close popup result after %s", timeout)
		}
		time.Sleep(tuiNewPanePopupResultPoll)
	}
}

func waitForTUISettingsPopupResult(resultFile, doneFile string, timeout time.Duration) (tuiSettingsPopupResult, error) {
	deadline := time.Now().Add(timeout)
	for {
		result, err := readTUISettingsPopupResult(resultFile)
		if err == nil {
			return result, nil
		}
		if !os.IsNotExist(err) {
			return tuiSettingsPopupResult{}, err
		}
		if _, err := os.Stat(doneFile); err == nil {
			return tuiSettingsPopupResult{Canceled: true}, nil
		} else if !os.IsNotExist(err) {
			return tuiSettingsPopupResult{}, err
		}
		if timeout > 0 && time.Now().After(deadline) {
			return tuiSettingsPopupResult{}, fmt.Errorf("timed out waiting for settings popup result after %s", timeout)
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

func readTUISettingsPopupResult(path string) (tuiSettingsPopupResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return tuiSettingsPopupResult{}, err
	}
	var result tuiSettingsPopupResult
	if err := json.Unmarshal(data, &result); err != nil {
		return tuiSettingsPopupResult{}, err
	}
	return result, nil
}

func readTUIClosePopupRequest(path string) (tuiClosePopupRequest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return tuiClosePopupRequest{}, err
	}
	var request tuiClosePopupRequest
	if err := json.Unmarshal(data, &request); err != nil {
		return tuiClosePopupRequest{}, err
	}
	return request, nil
}

func readTUIClosePopupResult(path string) (tuiClosePopupResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return tuiClosePopupResult{}, err
	}
	var result tuiClosePopupResult
	if err := json.Unmarshal(data, &result); err != nil {
		return tuiClosePopupResult{}, err
	}
	return result, nil
}

func closeModeName(mode lifecycle.CloseMode) string {
	switch mode {
	case lifecycle.ClosePaneOnly:
		return "pane"
	case lifecycle.CloseWorktree:
		return "worktree"
	case lifecycle.CloseEverything:
		return "everything"
	default:
		return "pane"
	}
}

func parseCloseModeName(name string) (lifecycle.CloseMode, error) {
	switch strings.TrimSpace(name) {
	case "", "pane":
		return lifecycle.ClosePaneOnly, nil
	case "worktree":
		return lifecycle.CloseWorktree, nil
	case "everything":
		return lifecycle.CloseEverything, nil
	default:
		return lifecycle.ClosePaneOnly, fmt.Errorf("unknown close mode %q", name)
	}
}
