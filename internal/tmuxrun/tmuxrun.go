// Package tmuxrun contains the direct tmux operations used by fanout.
package tmuxrun

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

const (
	userShellExpr       = `"${SHELL:-/bin/sh}"`
	paneListFormat      = "#{pane_id}:#{window_id}:#{pane_index}:#{pane_active}:#{pane_title}"
	livePanePathFormat  = "#{pane_id}\t#{pane_current_path}"
	livePaneTitleFormat = "#{pane_id}\t#{pane_title}"
	// agentStateOption is a tmux pane user option the BuildPaneLaunchCommand
	// wrapper sets to "running" before the agent starts and "done" after it
	// exits. It is the dashboard's agent-state signal: #{pane_current_command}
	// cannot be used because the non-interactive `sh -lc` wrapper runs without
	// job control, so the agent shares the wrapper's process group and tmux
	// reports the wrapper shell's name for the whole agent run (verified on
	// tmux 3.6a/macOS; Linux resolves the foreground pgrp leader the same way).
	// Pane user options die with the pane, matching the liveness boundary.
	agentStateOption         = "@fanout_agent_state"
	livePaneAgentStateFormat = "#{pane_id}\t#{" + agentStateOption + "}"
	paneAlternateFormat      = "#{alternate_on}"
)

// paneIDPattern matches a well-formed tmux pane id (%N). The live-pane parsers
// skip lines whose id field does not match, so a crafted directory name
// containing newlines cannot inject arbitrary phantom rows into the liveness
// sweep.
var paneIDPattern = regexp.MustCompile(`^%[0-9]+$`)

// PaneInfo describes a pane currently known to tmux.
type PaneInfo struct {
	ID       string
	WindowID string
	Index    int
	Active   bool
	Title    string
}

// SplitPane splits the target pane/session rooted at worktreePath and returns its pane id.
func SplitPane(target, worktreePath string) (string, error) {
	return splitPane(target, worktreePath, "")
}

// SplitPaneWithAgentCommand splits the target pane/session and starts the agent
// command through a shell wrapper that keeps the pane alive after the agent exits.
func SplitPaneWithAgentCommand(target, worktreePath, agentCommand string) (string, error) {
	return splitPane(target, worktreePath, BuildPaneLaunchCommand(agentCommand))
}

func splitPane(target, worktreePath, launchCommand string) (string, error) {
	args := []string{"split-window"}
	if target != "" {
		args = append(args, "-t", target)
	}
	args = append(args, "-d", "-h", "-P", "-F", "#{pane_id}", "-c", worktreePath)
	if strings.TrimSpace(launchCommand) != "" {
		args = append(args, launchCommand)
	}
	out, err := exec.Command("tmux", args...).Output()
	if err != nil {
		return "", fmt.Errorf("tmux split-window: %w", err)
	}
	paneID := strings.TrimSpace(string(out))
	if paneID == "" {
		return "", fmt.Errorf("tmux split-window returned an empty pane id")
	}
	return paneID, nil
}

// BuildPaneLaunchCommand returns a tmux shell-command that starts the agent via
// a POSIX wrapper and leaves the user's shell behind after the agent exits.
//
// The wrapper also reports the agent's run state explicitly through the
// agentStateOption pane user option ("running" before the agent starts,
// "done" once it exited). The pane must be targeted with an explicit
// -t "$TMUX_PANE" (tmux exports it into pane processes): a bare
// `set-option -p` resolves the *active* pane of the current window, which is
// not the fanout pane — split-window -d leaves the invoking pane active
// (verified on tmux 3.6a). Failures are silenced and never block the agent
// launch; the state is display-only telemetry.
func BuildPaneLaunchCommand(agentCommand string) string {
	agentCommand = strings.TrimSpace(agentCommand)
	if agentCommand == "" {
		return ""
	}
	setState := func(value string) string {
		return `tmux set-option -p -t "$TMUX_PANE" ` + agentStateOption + " " + value + " 2>/dev/null; "
	}
	body := setState("running") +
		agentCommand +
		`; __fanout_status=$?; ` + setState("done") + `printf '\n[fanout] agent exited with status %d; returning to shell.\n' "$__fanout_status"; exec ` + userShellExpr + ` -l`
	return "exec /bin/sh -lc " + shellQuote(body)
}

// LivePane is one live tmux pane: its server-scoped id, the cwd of its
// foreground process, its pane title (empty when tmux reports none), and the
// agent run state its launch wrapper recorded (empty when unknown).
type LivePane struct {
	ID          string
	CurrentPath string
	Title       string
	// AgentState は pane user option @fanout_agent_state の値。fanout の起動
	// ラッパー(BuildPaneLaunchCommand)が agent 起動前に "running"、終了後に
	// "done" を設定する。旧版 fanout やラッパー外で起動した pane では未設定で
	// ""。listing が失敗したとき・join 済み id に対応する行が無いときも空。
	AgentState string
}

// ListLivePanes returns every live tmux pane across all sessions with its
// current path and title. The dashboard matches a recorded pane on BOTH its id
// and a current path under its worktree: pane ids are server-scoped and reused
// after a tmux server restart, so id-only matching would falsely mark a stale
// row live when an unrelated new pane reuses the same %N. An error (e.g. tmux
// absent) lets callers degrade.
//
// It issues three list-panes calls (livePanePathFormat, livePaneTitleFormat,
// then livePaneAgentStateFormat) so each variable-content field is last on its
// line and survives strings.Cut with embedded tabs intact — both pane paths
// and pane titles may legally contain tabs.
//
// Injection defense: pane_current_path is just a directory name, so a crafted
// path containing a newline can forge whole extra lines in the path listing.
// Two layers stop that from producing phantom panes: (1) both parsers skip
// lines whose id field is not a well-formed %N pane id, discarding junk
// fragments; (2) a forged line that does imitate "%N\t<path>" is still dropped
// unless the same id appears in the title listing, which cannot be forged
// because tmux rejects newlines in pane titles (verified on tmux 3.6a). So a
// LivePane is emitted only for ids present in BOTH outputs. If the title call
// fails entirely, panes are returned with empty titles rather than failing the
// sweep — titles are cosmetic, liveness is not.
//
// Distinct from ListPanes(target), which returns richer PaneInfo for a single
// target; this one is the all-sessions id+cwd liveness sweep the dashboard needs.
func ListLivePanes() ([]LivePane, error) {
	pathOut, err := exec.Command("tmux", "list-panes", "-a", "-F", livePanePathFormat).Output()
	if err != nil {
		return nil, fmt.Errorf("tmux list-panes -a: %w", err)
	}
	panes := parseLivePanePaths(string(pathOut))
	titleOut, err := exec.Command("tmux", "list-panes", "-a", "-F", livePaneTitleFormat).Output()
	if err != nil {
		return panes, nil //nolint:nilerr // titles are cosmetic; degrade to empty titles instead of failing the liveness sweep
	}
	titles := parseLivePaneField(string(titleOut))
	// 第 3 の listing: 各 pane の @fanout_agent_state(起動ラッパーが agent
	// 実行前後に設定する pane user option)。ダッシュボードが agent 実行中/完了
	// の表示判定に使う。タイトル同様この値は表示専用で liveness の根拠ではない
	// ので、失敗は空値へ degrade する。join 規則も不変: path+title のクロス
	// チェックが唯一の liveness 根拠であり、agent 状態はそれを通過した検証済み
	// id のルックアップのみ(欠落は "")。pane 内のプロセスは自ペインの option
	// を任意の文字列(改行入り含む)に設定できるが、parseLivePaneField の
	// duplicate-id drop が他ペイン行の上書きを防ぎ、残る影響は最悪でも自ペイン
	// の表示上の agent 状態のみ — stale な pane を live に見せることはできない。
	agentStates := map[string]string{}
	if stateOut, err := exec.Command("tmux", "list-panes", "-a", "-F", livePaneAgentStateFormat).Output(); err == nil {
		agentStates = parseLivePaneField(string(stateOut))
	}
	// A real tmux listing emits each pane id exactly once; a duplicate means a
	// newline-bearing pane_current_path forged an extra row reusing a REAL id
	// (which would pass the title-listing check below). Conservatively drop
	// such ids entirely — we cannot tell the genuine row from the forgery.
	idCounts := make(map[string]int, len(panes))
	for _, pane := range panes {
		idCounts[pane.ID]++
	}
	var joined []LivePane
	for _, pane := range panes {
		if idCounts[pane.ID] != 1 {
			continue
		}
		title, ok := titles[pane.ID]
		if !ok {
			// Absent from the unforgeable title listing: a phantom row
			// injected via a newline-bearing pane_current_path. Drop it.
			continue
		}
		pane.Title = title
		pane.AgentState = agentStates[pane.ID]
		joined = append(joined, pane)
	}
	return joined, nil
}

// parseLivePanePaths parses livePanePathFormat lines into LivePanes with empty
// titles. The path is the last field, so strings.Cut keeps any embedded tabs
// intact. Lines whose id field is not a well-formed %N pane id are skipped:
// a path containing newlines splits into extra lines whose first field will
// not normally look like a pane id (forged "%N\t..." lines are handled by the
// cross-check in ListLivePanes).
func parseLivePanePaths(out string) []LivePane {
	var panes []LivePane
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimRight(line, "\r")
		id, path, ok := strings.Cut(line, "\t")
		if !ok || !paneIDPattern.MatchString(id) {
			continue
		}
		panes = append(panes, LivePane{ID: id, CurrentPath: path})
	}
	return panes
}

// parseLivePaneField parses "#{pane_id}\t<field>" lines — the shared parser
// for livePaneTitleFormat and livePaneAgentStateFormat — into an id-to-field
// map. The field is the last value on its line, so strings.Cut keeps any
// embedded tabs intact.
//
// Injection defense: tmux rejects newlines in pane titles, so the title
// listing cannot forge extra lines. The agent-state listing CAN be forged: a
// pane user option is settable to an arbitrary string (newlines included) by
// any process inside the pane, so a hostile pane can emit a fake "%N\t<field>"
// line imitating another pane. (A pane_current_command listing would be
// forgeable the same way, via newline-bearing process names — argv[0] on
// Linux, executable basename on macOS.) Two layers contain that: lines whose
// id fails the %N check are skipped, and an id that appears more than once is
// dropped entirely — the genuine line cannot be told apart from the forgery,
// so the field conservatively degrades to absent ("" at the join) instead of
// letting a later forged line overwrite another pane's entry. The residual
// impact is the attacker mis-reporting its own pane's display-only field.
func parseLivePaneField(out string) map[string]string {
	fields := make(map[string]string)
	dropped := map[string]bool{}
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimRight(line, "\r")
		id, field, ok := strings.Cut(line, "\t")
		if !ok || !paneIDPattern.MatchString(id) {
			continue
		}
		if dropped[id] {
			continue
		}
		if _, seen := fields[id]; seen {
			delete(fields, id)
			dropped[id] = true
			continue
		}
		fields[id] = field
	}
	return fields
}

// BindDashboardKey registers a tmux key binding (under the prefix table) that
// opens the read-only web dashboard. It binds the key directly to `new-window`
// (not run-shell), so:
//
//   - tmux expands `#{pane_current_path}` to the *active pane's* cwd at keypress
//     time, making a single global key open the dashboard for whichever repo the
//     pressing pane belongs to (multi-repo / multi-session safe). cmdDashboard
//     then resolves that cwd to the main working tree, so pressing from a child
//     worktree pane still reads the parent `.fanout/state.json`.
//   - The command runs through exactly one shell (new-window's), so the binary
//     path needs a single level of quoting — handling install paths with spaces
//     without the fragile double-quoting a run-shell wrapper would require.
//
// The detached `fanout-dashboard` window keeps the server alive past the
// keypress; reuse-if-running makes repeated presses just reopen the existing
// URL. The binding lives in the running tmux server (it never edits
// ~/.tmux.conf) and re-registering is idempotent.
//
// fanoutBin should be an absolute path (os.Executable) so the binding does not
// depend on PATH.
func BindDashboardKey(key, fanoutBin string) error {
	if strings.TrimSpace(key) == "" || strings.TrimSpace(fanoutBin) == "" {
		return fmt.Errorf("tmux bind-key: key and fanout binary path are required")
	}
	launch := shellQuote(fanoutBin) + " dashboard --web --open"
	args := []string{
		"bind-key", key, "new-window", "-d", "-n", "fanout-dashboard",
		"-c", "#{pane_current_path}", launch,
	}
	if err := exec.Command("tmux", args...).Run(); err != nil {
		return fmt.Errorf("tmux bind-key %s: %w", key, err)
	}
	return nil
}

// SelectTiled applies tmux's tiled layout to the target pane/session.
func SelectTiled(session string) error {
	args := []string{"select-layout"}
	if session != "" {
		args = append(args, "-t", session)
	}
	args = append(args, "tiled")
	if err := exec.Command("tmux", args...).Run(); err != nil {
		return fmt.Errorf("tmux select-layout tiled: %w", err)
	}
	return nil
}

// ListPanes returns pane metadata for a target pane, window, or session.
func ListPanes(target string) ([]PaneInfo, error) {
	target = strings.TrimSpace(target)
	args := []string{"list-panes"}
	if target != "" {
		if shouldListSessionPanes(target) {
			args = append(args, "-s")
			target = exactSessionTarget(target)
		}
		args = append(args, "-t", target)
	}
	args = append(args, "-F", paneListFormat)
	out, err := exec.Command("tmux", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("tmux list-panes: %w", err)
	}
	return parseListPanesOutput(string(out))
}

// ListAllPanes returns pane metadata across every tmux session.
func ListAllPanes() ([]PaneInfo, error) {
	out, err := exec.Command("tmux", "list-panes", "-a", "-F", paneListFormat).Output()
	if err != nil {
		return nil, fmt.Errorf("tmux list-panes -a: %w", err)
	}
	return parseListPanesOutput(string(out))
}

// NewWindow creates a detached window in session and returns its initial pane.
func NewWindow(session, name, startDir string) (PaneInfo, error) {
	session = strings.TrimSpace(session)
	if session == "" {
		return PaneInfo{}, fmt.Errorf("session name is required")
	}
	args := []string{"new-window", "-d", "-P", "-F", paneListFormat, "-t", exactSessionTarget(session)}
	if strings.TrimSpace(name) != "" {
		args = append(args, "-n", name)
	}
	if strings.TrimSpace(startDir) != "" {
		args = append(args, "-c", startDir)
	}
	out, err := exec.Command("tmux", args...).Output()
	if err != nil {
		return PaneInfo{}, fmt.Errorf("tmux new-window: %w", err)
	}
	panes, err := parseListPanesOutput(string(out))
	if err != nil {
		return PaneInfo{}, err
	}
	if len(panes) != 1 {
		return PaneInfo{}, fmt.Errorf("tmux new-window returned %d panes, want 1", len(panes))
	}
	return panes[0], nil
}

func shouldListSessionPanes(target string) bool {
	return HasSession(target)
}

func exactSessionTarget(name string) string {
	if isQualifiedSessionTarget(name) {
		return name
	}
	return "=" + name
}

func isQualifiedSessionTarget(name string) bool {
	return strings.HasPrefix(name, "=") ||
		strings.HasPrefix(name, "$") ||
		strings.HasPrefix(name, "%") ||
		strings.ContainsAny(name, "*?[")
}

func parseListPanesOutput(out string) ([]PaneInfo, error) {
	var panes []PaneInfo
	for lineNum, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, ":", 5)
		if len(fields) != 5 {
			return nil, fmt.Errorf("parse tmux list-panes line %d: expected 5 fields, got %d", lineNum+1, len(fields))
		}
		index, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, fmt.Errorf("parse tmux list-panes line %d index: %w", lineNum+1, err)
		}
		active, err := parsePaneActive(fields[3])
		if err != nil {
			return nil, fmt.Errorf("parse tmux list-panes line %d active: %w", lineNum+1, err)
		}
		panes = append(panes, PaneInfo{
			ID:       fields[0],
			WindowID: fields[1],
			Index:    index,
			Active:   active,
			Title:    fields[4],
		})
	}
	return panes, nil
}

func parsePaneActive(raw string) (bool, error) {
	switch raw {
	case "1":
		return true, nil
	case "0":
		return false, nil
	default:
		return false, fmt.Errorf("expected 0 or 1, got %q", raw)
	}
}

// CapturePaneOutput returns a read-only output snapshot from a tmux pane.
func CapturePaneOutput(paneID string, lines int) (string, error) {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return "", fmt.Errorf("pane id is required")
	}
	if lines < 0 {
		return "", fmt.Errorf("lines must be non-negative")
	}
	if paneAlternateOn(paneID) {
		if out, err := capturePaneOutput(paneID, 0, true); err == nil {
			return out, nil
		}
	}
	return capturePaneOutput(paneID, lines, false)
}

// CapturePlanSource はダッシュボードの GET /api/plan が <proposed_plan> ブロック
// を探すための読み取り専用テキストを返す: 通常スクリーンの履歴
// (capture-pane -p -J -S -<lines>。-J が折返し行を結合するので、ペイン幅で
// 分断されたタグも一行に戻る)に、alternate screen 中(codex TUI)はその内容
// (-a -p -J)を末尾へ連結する。最新レンダが末尾に来るので、last-block 検索では
// alternate screen 側の plan が勝つ。CapturePaneOutput(peek 用、最新画面優先)
// とは目的が違うため別関数にしてある。
func CapturePlanSource(paneID string, lines int) (string, error) {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return "", fmt.Errorf("pane id is required")
	}
	if lines <= 0 {
		// 0 を許すと -S が付かず viewport のみの浅い capture に静かに退化する
		return "", fmt.Errorf("lines must be positive")
	}
	out, err := capturePlanScreen(paneID, lines, false)
	if err != nil {
		return "", err
	}
	if paneAlternateOn(paneID) {
		// alternate screen の取得失敗は履歴のみへ degrade(peek と同じ精神:
		// 追加情報は best-effort、本体の履歴が読めていれば応答は返す)。
		if alt, altErr := capturePlanScreen(paneID, 0, true); altErr == nil {
			out += "\n" + alt
		}
	}
	return out, nil
}

// capturePlanScreen は CapturePlanSource 専用の capture-pane 呼び出し。
// capturePaneOutput と違い常に -J を付け、折返しで分断された
// <proposed_plan> タグを結合して検索可能にする。
func capturePlanScreen(paneID string, lines int, alternateScreen bool) (string, error) {
	args := []string{"capture-pane", "-p", "-J", "-t", paneID}
	if alternateScreen {
		args = []string{"capture-pane", "-a", "-p", "-J", "-t", paneID}
	}
	if !alternateScreen && lines > 0 {
		args = append(args, "-S", fmt.Sprintf("-%d", lines))
	}
	out, err := exec.Command("tmux", args...).Output()
	if err != nil {
		return "", fmt.Errorf("tmux capture-pane: %w", err)
	}
	return string(out), nil
}

func paneAlternateOn(paneID string) bool {
	out, err := exec.Command("tmux", "display-message", "-p", "-t", paneID, paneAlternateFormat).Output()
	return err == nil && strings.TrimSpace(string(out)) == "1"
}

func capturePaneOutput(paneID string, lines int, alternateScreen bool) (string, error) {
	args := []string{"capture-pane", "-p", "-t", paneID}
	if alternateScreen {
		args = []string{"capture-pane", "-a", "-p", "-t", paneID}
	}
	if !alternateScreen && lines > 0 {
		args = append(args, "-S", fmt.Sprintf("-%d", lines))
	}
	out, err := exec.Command("tmux", args...).Output()
	if err != nil {
		return "", fmt.Errorf("tmux capture-pane: %w", err)
	}
	return string(out), nil
}

// SelectPane selects paneID and brings its containing tmux window on screen.
func SelectPane(paneID string) error {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return fmt.Errorf("pane id is required")
	}
	if err := exec.Command("tmux", "switch-client", "-t", paneID).Run(); err != nil {
		return fmt.Errorf("tmux switch-client: %w", err)
	}
	return nil
}

// FocusPane makes pane active in its window and selects that window in the session.
func FocusPane(pane PaneInfo) error {
	if strings.TrimSpace(pane.ID) == "" {
		return fmt.Errorf("pane id is required")
	}
	if strings.TrimSpace(pane.WindowID) != "" {
		if err := exec.Command("tmux", "select-window", "-t", pane.WindowID).Run(); err != nil {
			return fmt.Errorf("tmux select-window: %w", err)
		}
	}
	if err := exec.Command("tmux", "select-pane", "-t", pane.ID).Run(); err != nil {
		return fmt.Errorf("tmux select-pane: %w", err)
	}
	return nil
}

// IsPaneAlive reports whether tmux can resolve the pane id.
func IsPaneAlive(paneID string) bool {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return false
	}
	return exec.Command("tmux", "display-message", "-p", "-t", paneID).Run() == nil
}

// InsideTmux reports whether the current process is running under tmux.
func InsideTmux() bool {
	return strings.TrimSpace(os.Getenv("TMUX")) != ""
}

// CurrentSession returns tmux's current session name.
func CurrentSession() (string, error) {
	out, err := exec.Command("tmux", "display-message", "-p", "#{session_name}").Output()
	if err != nil {
		return "", fmt.Errorf("tmux display-message session_name: %w", err)
	}
	session := strings.TrimSpace(string(out))
	if session == "" {
		return "", fmt.Errorf("tmux did not report a current session name")
	}
	return session, nil
}

// HasSession reports whether a tmux session exists.
func HasSession(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	return exec.Command("tmux", "has-session", "-t", exactSessionTarget(name)).Run() == nil
}

// NewSession creates a detached tmux session rooted at startDir.
func NewSession(name, startDir string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("session name is required")
	}
	args := []string{"new-session", "-d", "-s", name}
	if strings.TrimSpace(startDir) != "" {
		args = append(args, "-c", startDir)
	}
	if err := exec.Command("tmux", args...).Run(); err != nil {
		return fmt.Errorf("tmux new-session: %w", err)
	}
	return nil
}

// SendKeys sends keys to a tmux target.
func SendKeys(target string, keys ...string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return fmt.Errorf("target is required")
	}
	if len(keys) == 0 {
		return fmt.Errorf("keys are required")
	}
	args := []string{"send-keys", "-t", exactSessionTarget(target)}
	args = append(args, keys...)
	if err := exec.Command("tmux", args...).Run(); err != nil {
		return fmt.Errorf("tmux send-keys: %w", err)
	}
	return nil
}

// AttachOrSwitch attaches to a session outside tmux or switches clients inside tmux.
func AttachOrSwitch(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("session name is required")
	}
	target := exactSessionTarget(name)
	insideTmux := InsideTmux()
	args := []string{"attach-session", "-t", target}
	if insideTmux {
		args = []string{"switch-client", "-t", target}
	}
	cmd := exec.Command("tmux", args...)
	if !insideTmux {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tmux %s: %w", args[0], err)
	}
	return nil
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return r != '/' && r != ':' && r != '.' && r != '-' && r != '_' && (r < '0' || r > '9') && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z')
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// PaneTitle returns tmux's current title for a pane.
func PaneTitle(paneID string) (string, error) {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return "", fmt.Errorf("pane id is required")
	}
	out, err := exec.Command("tmux", "display-message", "-p", "-t", paneID, "#{pane_title}").Output()
	if err != nil {
		return "", fmt.Errorf("tmux display-message pane_title: %w", err)
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}

// SetPaneTitle sets the tmux pane title.
func SetPaneTitle(paneID, title string) error {
	if strings.TrimSpace(paneID) == "" {
		return fmt.Errorf("pane id is required")
	}
	if err := exec.Command("tmux", "select-pane", "-t", paneID, "-T", title).Run(); err != nil {
		return fmt.Errorf("tmux select-pane -T: %w", err)
	}
	return nil
}

// KillPane closes a pane created during a failed launch attempt.
func KillPane(paneID string) error {
	if strings.TrimSpace(paneID) == "" {
		return nil
	}
	if err := exec.Command("tmux", "kill-pane", "-t", paneID).Run(); err != nil {
		return fmt.Errorf("tmux kill-pane: %w", err)
	}
	return nil
}
