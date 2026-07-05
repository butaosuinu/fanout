// Package tmuxrun contains the direct tmux operations used by fanout.
package tmuxrun

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

const (
	MinimumVersion           = "3.3"
	DashboardNotifyClientEnv = "FANOUT_DASHBOARD_NOTIFY_CLIENT"
	userShellExpr            = `"${SHELL:-/bin/sh}"`
	paneListFormat           = "#{pane_id}:#{window_id}:#{pane_index}:#{pane_active}:#{pane_title}"
	livePanePathFormat       = "#{pane_id}\t#{pane_current_path}"
	livePaneTitleFormat      = "#{pane_id}\t#{pane_title}"
	// agentStateOption is a tmux pane user option the BuildPaneLaunchCommand
	// wrapper sets to "running" before the agent starts and "done" after it
	// exits. It is the dashboard's agent-state signal: #{pane_current_command}
	// cannot be used because the non-interactive `sh -lc` wrapper runs without
	// job control, so the agent shares the wrapper's process group and tmux
	// reports the wrapper shell's name for the whole agent run (verified on
	// tmux 3.6a/macOS; Linux resolves the foreground pgrp leader the same way).
	// Pane user options die with the pane, matching the liveness boundary.
	agentStateOption           = "@fanout_agent_state"
	shellKeyOption             = "@fanout_shell_key"
	projectRootOption          = "@fanout_project_root"
	worktreePathOption         = "@fanout_worktree_path"
	livePaneAgentStateFormat   = "#{pane_id}\t#{" + agentStateOption + "}"
	livePaneShellKeyFormat     = "#{pane_id}\t#{" + shellKeyOption + "}"
	livePaneProjectRootFormat  = "#{pane_id}\t#{" + projectRootOption + "}"
	livePaneWorktreePathFormat = "#{pane_id}\t#{" + worktreePathOption + "}"
	livePaneRoleFormat         = "#{pane_id}\t#{" + roleOption + "}"
	livePaneSessionIDFormat    = "#{pane_id}\t#{session_id}"
	paneAlternateFormat        = "#{alternate_on}"
	// paneLabelOption is a tmux pane user option holding the border label fanout
	// shows on the pane's top border (e.g. "#123 · fix-login-bug-123"). It is set
	// per pane and referenced from paneBorderFormat. A dedicated user option keeps
	// the label stable: agents such as claude/codex rewrite the terminal title
	// (OSC), which clobbers #{pane_title}, but never touch pane user options.
	paneLabelOption = "@fanout_pane_label"
	// paneBorderFormat draws @fanout_pane_label (with padding) on each pane's top
	// border, falling back to #{pane_title} for panes fanout did not label. A
	// substituted option value is not re-expanded (verified on tmux 3.6a: #{ /
	// #( / ## stay literal), so the "#<digit>" parent prefix is safe; only "#["
	// is still interpreted at draw time as a style, which SetPaneLabel neutralizes.
	paneBorderFormat = " #{?" + paneLabelOption + ",#{" + paneLabelOption + "},#{pane_title}} "
	// paneActiveBorderStyle / paneBorderStyle recolor the pane borders to fanout's
	// PAPER BREEZE palette: 浅葱 (asagi) for the active border, in place of tmux's
	// default green, and 藍 (ai) for inactive borders. These are the site/light
	// values from the palette (site/assets/css/main.css; keep in sync with the
	// internal/tui AdaptiveColor and internal/infra/log 256-color copies). Like
	// internal/infra/log, tmux cannot query the terminal background, so it cannot pick
	// the AdaptiveColor dark variants the TUI uses; 浅葱 reads on both backgrounds
	// and the 藍 inactive border is intentionally subtle. Truecolor hex; tmux
	// falls back to the nearest 256-color when the terminal lacks RGB support.
	paneActiveBorderStyle = "fg=#00A3AF"
	paneBorderStyle       = "fg=#165E83"
	// popupBorderLines / popupBorderStyle make display-popup frames read as a
	// fanout-owned modal instead of blending into the surrounding terminal.
	popupBorderLines = "double"
	popupBorderStyle = paneActiveBorderStyle
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

// ClientSize is the current tmux client's drawable terminal size.
type ClientSize struct {
	Width  int
	Height int
}

// PopupOptions describes a centered tmux display-popup invocation.
type PopupOptions struct {
	Width    int
	Height   int
	StartDir string
	Title    string
	Command  string
}

type tmuxVersion struct {
	Major int
	Minor int
}

var tmuxVersionPattern = regexp.MustCompile(`tmux\s+([0-9]+)(?:\.([0-9]+))?`)

// CheckMinimumVersion verifies that tmux is available and new enough for
// display-popup, which fanout uses for TUI popup input.
func CheckMinimumVersion() error {
	out, err := exec.Command("tmux", "-V").Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("tmux %s+ (brew install tmux)", MinimumVersion)
		}
		return fmt.Errorf("tmux %s+ (tmux -V failed: %w)", MinimumVersion, err)
	}
	return checkMinimumVersionOutput(string(out))
}

func checkMinimumVersionOutput(out string) error {
	version, err := parseTmuxVersion(out)
	if err != nil {
		return fmt.Errorf("tmux %s+ (could not parse tmux -V output %q)", MinimumVersion, strings.TrimSpace(out))
	}
	required, err := parseTmuxVersion("tmux " + MinimumVersion)
	if err != nil {
		return err
	}
	if compareTmuxVersions(version, required) < 0 {
		return fmt.Errorf("tmux %s+ (found %s; brew upgrade tmux)", MinimumVersion, version.String())
	}
	return nil
}

func parseTmuxVersion(out string) (tmuxVersion, error) {
	m := tmuxVersionPattern.FindStringSubmatch(strings.TrimSpace(out))
	if m == nil {
		return tmuxVersion{}, fmt.Errorf("missing tmux version")
	}
	major, err := strconv.Atoi(m[1])
	if err != nil {
		return tmuxVersion{}, err
	}
	minor := 0
	if m[2] != "" {
		minor, err = strconv.Atoi(m[2])
		if err != nil {
			return tmuxVersion{}, err
		}
	}
	return tmuxVersion{Major: major, Minor: minor}, nil
}

func compareTmuxVersions(a, b tmuxVersion) int {
	if a.Major != b.Major {
		return a.Major - b.Major
	}
	return a.Minor - b.Minor
}

func (v tmuxVersion) String() string {
	return fmt.Sprintf("%d.%d", v.Major, v.Minor)
}

// CurrentClientSize returns the tmux client dimensions, not the current pane
// dimensions. tmux display-popup is client-scoped, so pane width is irrelevant.
func CurrentClientSize() (ClientSize, error) {
	out, err := exec.Command("tmux", "display-message", "-p", "#{client_width} #{client_height}").Output()
	if err != nil {
		return ClientSize{}, fmt.Errorf("tmux display-message client size: %w", err)
	}
	size, err := parseClientSize(string(out))
	if err != nil {
		return ClientSize{}, fmt.Errorf("parse tmux client size: %w", err)
	}
	return size, nil
}

func parseClientSize(out string) (ClientSize, error) {
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return ClientSize{}, fmt.Errorf("expected 2 fields, got %d", len(fields))
	}
	width, err := strconv.Atoi(fields[0])
	if err != nil {
		return ClientSize{}, fmt.Errorf("width: %w", err)
	}
	height, err := strconv.Atoi(fields[1])
	if err != nil {
		return ClientSize{}, fmt.Errorf("height: %w", err)
	}
	if width <= 0 || height <= 0 {
		return ClientSize{}, fmt.Errorf("dimensions must be positive, got %dx%d", width, height)
	}
	return ClientSize{Width: width, Height: height}, nil
}

// DisplayPopup opens command in a centered tmux popup.
func DisplayPopup(opts PopupOptions) error {
	args, err := displayPopupArgs(opts)
	if err != nil {
		return err
	}
	if err := exec.Command("tmux", args...).Run(); err != nil {
		return fmt.Errorf("tmux display-popup: %w", err)
	}
	return nil
}

func displayPopupArgs(opts PopupOptions) ([]string, error) {
	if opts.Width <= 0 {
		return nil, fmt.Errorf("popup width must be positive")
	}
	if opts.Height <= 0 {
		return nil, fmt.Errorf("popup height must be positive")
	}
	if strings.TrimSpace(opts.Command) == "" {
		return nil, fmt.Errorf("popup command is required")
	}
	args := []string{
		"display-popup", "-E",
		"-b", popupBorderLines,
		"-S", popupBorderStyle,
		"-w", strconv.Itoa(opts.Width),
		"-h", strconv.Itoa(opts.Height),
	}
	if strings.TrimSpace(opts.StartDir) != "" {
		args = append(args, "-d", opts.StartDir)
	}
	args = append(args, "-x", "C", "-y", "C")
	if strings.TrimSpace(opts.Title) != "" {
		args = append(args, "-T", opts.Title)
	}
	args = append(args, opts.Command)
	return args, nil
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
	// ShellKey is @fanout_shell_key for TUI shell panes. It lets callers match
	// shell rows without trusting broad repo-root WorktreePath prefixes.
	ShellKey string
	// ProjectRoot is @fanout_project_root, the state owner root fanout records
	// on created panes so keybindings still resolve correctly when
	// pane_current_path is stale inside agent TUIs.
	ProjectRoot string
	// WorktreePath is @fanout_worktree_path, the recorded worktree path for this
	// pane. It gives action keybindings a stable match that does not depend on
	// pane_current_path.
	WorktreePath string
	// Role is @fanout_role, the auto-layout role fanout stamps on panes it
	// manages (RoleConsole for the TUI console). Like every pane user option it
	// is settable by the process inside the pane, so it is a display/UX signal,
	// not a security boundary. It can also degrade to "": a failed role
	// listing, or a forged duplicate row reusing this pane's id (see
	// parseLivePaneField), blanks it — the console-return keys then report no
	// console until the next keypress re-lists.
	Role string
	// SessionID is #{session_id}, the tmux-generated $N id of the session
	// holding the pane. tmux produces it, so unlike the user options above a
	// pane's process cannot forge its value.
	SessionID string
}

// ListLivePanes returns every live tmux pane across all sessions with its
// current path and title. The dashboard matches a recorded pane on BOTH its id
// and a current path under its worktree: pane ids are server-scoped and reused
// after a tmux server restart, so id-only matching would falsely mark a stale
// row live when an unrelated new pane reuses the same %N. An error (e.g. tmux
// absent) lets callers degrade.
//
// It issues separate list-panes calls for each field (livePanePathFormat,
// livePaneTitleFormat, livePaneAgentStateFormat, livePaneShellKeyFormat,
// livePaneProjectRootFormat, livePaneWorktreePathFormat, livePaneRoleFormat,
// then livePaneSessionIDFormat) so each variable-content field is last on its
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
	shellKeys := map[string]string{}
	if shellKeyOut, err := exec.Command("tmux", "list-panes", "-a", "-F", livePaneShellKeyFormat).Output(); err == nil {
		shellKeys = parseLivePaneField(string(shellKeyOut))
	}
	projectRoots := map[string]string{}
	if projectRootOut, err := exec.Command("tmux", "list-panes", "-a", "-F", livePaneProjectRootFormat).Output(); err == nil {
		projectRoots = parseLivePaneField(string(projectRootOut))
	}
	worktreePaths := map[string]string{}
	if worktreePathOut, err := exec.Command("tmux", "list-panes", "-a", "-F", livePaneWorktreePathFormat).Output(); err == nil {
		worktreePaths = parseLivePaneField(string(worktreePathOut))
	}
	roles := map[string]string{}
	if roleOut, err := exec.Command("tmux", "list-panes", "-a", "-F", livePaneRoleFormat).Output(); err == nil {
		roles = parseLivePaneField(string(roleOut))
	}
	sessionIDs := map[string]string{}
	if sessionIDOut, err := exec.Command("tmux", "list-panes", "-a", "-F", livePaneSessionIDFormat).Output(); err == nil {
		sessionIDs = parseLivePaneField(string(sessionIDOut))
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
		pane.ShellKey = shellKeys[pane.ID]
		pane.ProjectRoot = projectRoots[pane.ID]
		pane.WorktreePath = worktreePaths[pane.ID]
		pane.Role = roles[pane.ID]
		pane.SessionID = sessionIDs[pane.ID]
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
// impact is the attacker mis-reporting its own pane's display-only field —
// or, by forging a duplicate that reuses ANOTHER pane's real id, blanking
// that pane's field. For @fanout_role that blanks the console's Role and the
// console-return keys report "no live console": an accepted nuisance in the
// same trust bucket as role stamping itself (focus-console is a UX primitive,
// not a security boundary; the sweep re-reads on every keypress).
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

// BindDashboardKeys registers tmux key bindings that open the read-only web
// dashboard. The prefix key is bound under the prefix table; the direct key is
// bound in the root table so it works without tmux's prefix. Both bindings run
// the same `run-shell` wrapper, so:
//
//   - the wrapper uses @fanout_project_root (when fanout recorded it on the
//     pane) or `#{pane_current_path}` at keypress time, shell-quoted in each
//     conditional branch for new-window's -c argument. That makes a single
//     global key open the dashboard for whichever repo the pressing pane
//     belongs to (multi-repo / multi-session safe). The explicit pane option
//     covers agent TUIs such as Codex where tmux's current-path signal can
//     remain stuck on the parent shell's cwd. cmdDashboard then resolves that
//     cwd to the main working tree, so pressing from a child worktree pane still
//     reads the parent `.fanout/state.json`.
//   - The new window receives the pressing tmux client's tty in an environment
//     variable. cmdDashboard uses that as a target-client when reporting the URL
//     back to tmux's status line; pane targets are only a format context for
//     display-message, not the display destination.
//   - run-shell expands the socket path, session id, and client tty before
//     invoking the tmux client. The nested client uses `-S #{socket_path}` and
//     `-t #{session_id}:`, so non-default sockets and multi-session servers
//     still open the detached window beside the pressed pane. The wrapper
//     avoids run-shell -c because fanout supports tmux 3.3, where that option
//     does not exist.
//
// The detached `fanout-dashboard` window keeps the server alive past the
// keypress; reuse-if-running makes repeated presses just reopen the existing
// URL. The binding lives in the running tmux server (it never edits
// ~/.tmux.conf) and re-registering is idempotent.
//
// fanoutBin should be an absolute path (os.Executable) so the bindings do not
// depend on PATH.
func BindDashboardKeys(prefixKey, directKey, fanoutBin string) error {
	if strings.TrimSpace(prefixKey) == "" || strings.TrimSpace(directKey) == "" || strings.TrimSpace(fanoutBin) == "" {
		return fmt.Errorf("tmux bind-key: prefix key, direct key, and fanout binary path are required")
	}
	startDir := "#{?@fanout_project_root,#{q:@fanout_project_root},#{q:pane_current_path}}"
	launch := dashboardNewWindowShellCommand(fanoutBin, startDir)
	prefixArgs := []string{
		"bind-key", prefixKey, "run-shell", "-b", launch,
	}
	if err := exec.Command("tmux", prefixArgs...).Run(); err != nil {
		return fmt.Errorf("tmux bind-key %s: %w", prefixKey, err)
	}
	directArgs := []string{
		"bind-key", "-n", directKey, "run-shell", "-b", launch,
	}
	if err := exec.Command("tmux", directArgs...).Run(); err != nil {
		return fmt.Errorf("tmux bind-key -n %s: %w", directKey, err)
	}
	return nil
}

func dashboardNewWindowShellCommand(fanoutBin, startDir string) string {
	launch := tmuxLiteral(shellQuote(fanoutBin)) + " dashboard --web --open"
	notifyClientEnv := DashboardNotifyClientEnv + "=#{client_tty}"
	return "__fanout_start_dir=" + startDir + `; __fanout_start_dir=$(printf '%s' "$__fanout_start_dir" | sed 's/#/####/g'); ` +
		"tmux -S #{q:socket_path} new-window -d -n fanout-dashboard -t #{q:session_id}: -c \"$__fanout_start_dir\" -e " + shellQuote(notifyClientEnv) + " " + shellQuote(launch)
}

// BindConsoleKeys registers tmux key bindings that return focus to the fanout
// TUI console from any pane. The prefix key is bound under the prefix table;
// the direct key is bound in the root table so it works without tmux's prefix.
//
// Unlike BindDashboardKeys the launch command runs through `run-shell`, not
// `new-window`: returning to the console must not create a window. run-shell
// is still exactly one shell level, and the binding itself is registered via
// argv, so the binary path needs a single shellQuote for the shell layer —
// plus '#' doubling, because run-shell format-expands its argument at
// keypress time (that is what resolves #{pane_id} / #{client_name}), and
// format expansion pierces shell quotes, so an unescaped '#' in an install
// path would be mangled (or a '#(' even command-substituted) before the shell
// runs. `#{pane_id}` / `#{client_name}` expand to the pressing pane and
// client, letting `fanout focus-console` prefer the console recorded for that
// pane's repo and pin the switch to the client that pressed the key. Baking a
// console pane id into the binding instead would go stale on every console
// restart and turn multi-repo registrations into an overwrite war; resolving
// the target at keypress time keeps one global binding correct.
//
// Any failure of the spawned command (the recorded binary was rebuilt or
// deleted, the console died mid-switch) degrades to a status-line notice: the
// `|| tmux display-message` tail keeps run-shell's exit status zero, because
// a non-zero exit would pop tmux's copy-mode error view over whatever pane
// the user was working in.
//
// fanoutBin should be an absolute path (os.Executable) so the bindings do not
// depend on PATH.
func BindConsoleKeys(prefixKey, directKey, fanoutBin string) error {
	if strings.TrimSpace(prefixKey) == "" || strings.TrimSpace(directKey) == "" || strings.TrimSpace(fanoutBin) == "" {
		return fmt.Errorf("tmux bind-key: prefix key, direct key, and fanout binary path are required")
	}
	launch := tmuxLiteral(shellQuote(fanoutBin)) +
		` focus-console --from "#{pane_id}" --client "#{client_name}" >/dev/null 2>&1` +
		` || tmux display-message "fanout: focus-console failed; restart fanout to refresh this key"`
	if err := exec.Command("tmux", "bind-key", prefixKey, "run-shell", launch).Run(); err != nil {
		return fmt.Errorf("tmux bind-key %s: %w", prefixKey, err)
	}
	if err := exec.Command("tmux", "bind-key", "-n", directKey, "run-shell", launch).Run(); err != nil {
		return fmt.Errorf("tmux bind-key -n %s: %w", directKey, err)
	}
	return nil
}

// BindWorktreeActionKey registers a tmux prefix-table keybinding that opens a
// small popup for actions against the currently focused fanout pane's recorded
// worktree.
func BindWorktreeActionKey(key, fanoutBin string) error {
	if strings.TrimSpace(key) == "" || strings.TrimSpace(fanoutBin) == "" {
		return fmt.Errorf("tmux bind-key: key and fanout binary path are required")
	}
	launch := shellQuote(fanoutBin) + " __worktree-action --pane #{pane_id}"
	startDir := "#{?@fanout_project_root,#{@fanout_project_root},#{pane_current_path}}"
	args := []string{
		"bind-key", key, "display-popup", "-E",
		"-b", popupBorderLines,
		"-S", popupBorderStyle,
		"-d", startDir,
		launch,
	}
	if err := exec.Command("tmux", args...).Run(); err != nil {
		return fmt.Errorf("tmux bind-key %s: %w", key, err)
	}
	return nil
}

// SetPaneProjectRoot records the fanout state owner on a pane. The dashboard
// keybinding prefers this over #{pane_current_path}, which can be stale inside
// agent TUIs such as Codex that do not update tmux's foreground cwd signal.
func SetPaneProjectRoot(paneID, projectRoot string) error {
	if strings.TrimSpace(paneID) == "" {
		return fmt.Errorf("pane id is required")
	}
	if strings.TrimSpace(projectRoot) == "" {
		return fmt.Errorf("project root is required")
	}
	if err := exec.Command("tmux", "set-option", "-p", "-t", paneID, projectRootOption, projectRoot).Run(); err != nil {
		return fmt.Errorf("tmux set-option %s: %w", projectRootOption, err)
	}
	return nil
}

// SetPaneWorktreePath records the worktree path a fanout pane belongs to.
func SetPaneWorktreePath(paneID, worktreePath string) error {
	if strings.TrimSpace(paneID) == "" {
		return fmt.Errorf("pane id is required")
	}
	if strings.TrimSpace(worktreePath) == "" {
		return fmt.Errorf("worktree path is required")
	}
	if err := exec.Command("tmux", "set-option", "-p", "-t", paneID, worktreePathOption, worktreePath).Run(); err != nil {
		return fmt.Errorf("tmux set-option %s: %w", worktreePathOption, err)
	}
	return nil
}

// SetPaneShellKey records a shell-pane liveness token on a tmux pane.
func SetPaneShellKey(paneID, shellKey string) error {
	if strings.TrimSpace(paneID) == "" {
		return fmt.Errorf("pane id is required")
	}
	if strings.TrimSpace(shellKey) == "" {
		return fmt.Errorf("shell key is required")
	}
	if err := exec.Command("tmux", "set-option", "-p", "-t", paneID, shellKeyOption, shellKey).Run(); err != nil {
		return fmt.Errorf("tmux set-option %s: %w", shellKeyOption, err)
	}
	return nil
}

// PaneBorderFormat returns the pane-border-format string EnablePaneBorderTitles
// applies, single-sourced here so the --dry-run preview matches the live command.
func PaneBorderFormat() string { return paneBorderFormat }

// PaneActiveBorderStyle and PaneBorderStyle return the pane border styles
// EnablePaneBorderTitles applies, single-sourced here so the --dry-run preview
// matches the live command.
func PaneActiveBorderStyle() string { return paneActiveBorderStyle }
func PaneBorderStyle() string       { return paneBorderStyle }

// SetPaneLabel records the border label fanout displays on a pane's top border.
// An empty label is allowed (the pane then falls back to #{pane_title}). The
// label is neutralized first so a "#[...]" sequence in a --name / display
// override cannot inject a tmux style into the border.
func SetPaneLabel(paneID, label string) error {
	if strings.TrimSpace(paneID) == "" {
		return fmt.Errorf("pane id is required")
	}
	if err := exec.Command("tmux", "set-option", "-p", "-t", paneID, paneLabelOption, NeutralizePaneLabel(label)).Run(); err != nil {
		return fmt.Errorf("tmux set-option %s: %w", paneLabelOption, err)
	}
	return nil
}

// paneLabelStyleRE matches a run of one or more "#" immediately before a "[".
// Stripping the whole run (not a single "#[") is what makes NeutralizePaneLabel
// overlap-safe: a lone ReplaceAll("#[","[") turns "##[" into "#[" and re-arms
// the style, but "#+\[" consumes the maximal run greedily so no "#[" survives.
var paneLabelStyleRE = regexp.MustCompile(`#+\[`)

// NeutralizePaneLabel defuses the only metacharacter the pane-border renderer
// interprets at draw time: "#[" introduces a tmux style sequence. fanout's own
// label parts (the "#<digit>" parent prefix, slugs, "·") never contain it, but a
// --name / display override could, which would recolor or drop part of the
// border text. Dropping the "#"(s) makes "#[fg=red]" render as the literal
// "[fg=red]". Other "#" forms ("#{", "#(", "##") are not re-interpreted in a
// substituted option value (verified on tmux 3.6a), so they need no handling.
// SetPaneLabel applies it for live panes; the --dry-run preview calls it so the
// printed command matches what would actually run.
func NeutralizePaneLabel(label string) string {
	return paneLabelStyleRE.ReplaceAllString(label, "[")
}

// EnablePaneBorderTitles turns on top pane-border titles for the window holding
// paneID, points pane-border-format at @fanout_pane_label, and recolors the pane
// borders to fanout's theme (浅葱 active, 藍 inactive). pane-border-status and the
// two *-border-style options are window options (their finest granularity), so
// this affects every pane in the window: non-fanout panes fall back to
// #{pane_title}, and any pane-border-style / pane-active-border-style the user
// configured is overwritten for the window with no teardown (only the active
// border defaults to green; the inactive default is the terminal foreground). On
// the documented "use the current pane" path this restyles the user's existing
// window — the same window-scope trade-off pane-border-status/-format already
// make. Re-applying is idempotent.
func EnablePaneBorderTitles(paneID string) error {
	if strings.TrimSpace(paneID) == "" {
		return fmt.Errorf("pane id is required")
	}
	if err := exec.Command("tmux", "set-option", "-w", "-t", paneID, "pane-border-status", "top").Run(); err != nil {
		return fmt.Errorf("tmux set-option pane-border-status: %w", err)
	}
	if err := exec.Command("tmux", "set-option", "-w", "-t", paneID, "pane-border-format", paneBorderFormat).Run(); err != nil {
		return fmt.Errorf("tmux set-option pane-border-format: %w", err)
	}
	if err := exec.Command("tmux", "set-option", "-w", "-t", paneID, "pane-active-border-style", paneActiveBorderStyle).Run(); err != nil {
		return fmt.Errorf("tmux set-option pane-active-border-style: %w", err)
	}
	if err := exec.Command("tmux", "set-option", "-w", "-t", paneID, "pane-border-style", paneBorderStyle).Run(); err != nil {
		return fmt.Errorf("tmux set-option pane-border-style: %w", err)
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

// ZoomPane zooms paneID's window onto it via resize-pane -Z. The tmux flag is
// a toggle, so an already-zoomed window is left zoomed instead of being
// toggled back to the split layout; callers focus the pane first, which makes
// paneID the zoomed pane in that case.
func ZoomPane(paneID string) error {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return fmt.Errorf("pane id is required")
	}
	out, err := exec.Command("tmux", "display-message", "-p", "-t", paneID, "#{window_zoomed_flag}").Output()
	if err != nil {
		return fmt.Errorf("tmux display-message: %w", err)
	}
	if strings.TrimSpace(string(out)) == "1" {
		return nil
	}
	if err := exec.Command("tmux", "resize-pane", "-Z", "-t", paneID).Run(); err != nil {
		return fmt.Errorf("tmux resize-pane -Z: %w", err)
	}
	return nil
}

// SelectPaneForClient is SelectPane pinned to one client. Keybinding-driven
// commands run through run-shell with no client context, where a bare
// switch-client falls back to the most recently active client — with two
// clients attached to the server that can yank the wrong terminal. clientName
// comes from #{client_name} expanded at keypress; when empty, this falls back
// to SelectPane's current-client resolution.
func SelectPaneForClient(clientName, paneID string) error {
	clientName = strings.TrimSpace(clientName)
	if clientName == "" {
		return SelectPane(paneID)
	}
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return fmt.Errorf("pane id is required")
	}
	if err := exec.Command("tmux", "switch-client", "-c", clientName, "-t", paneID).Run(); err != nil {
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

// EnableExtendedKeys best-effort configures the tmux server so Shift+Enter (and
// other modified keys) reach the fanout TUI distinctly instead of collapsing to
// a bare Enter. It turns on extended-keys forwarding and advertises the extkeys
// terminal feature for the attached client's terminal. Both are server-scoped
// tmux options that outlive the console (left intact on exit since other fanout
// panes may rely on them); the writes are idempotent and non-clobbering so
// repeated runs do not grow terminal-features or override an explicit
// on/always. Errors are ignored: on terminals or tmux builds without support
// the TUI falls back to Ctrl+J for newlines.
func EnableExtendedKeys() {
	enableExtendedKeys(clientTermName())
}

// EnableExtendedKeysForTerm configures extended keys for a known terminal name.
// Use it before a client has attached (e.g. creating the managed session from a
// plain shell), where #{client_termname} is not yet resolvable but the outer
// TERM is. Advertising extkeys before the first attach means the new client
// picks it up without needing a re-attach.
func EnableExtendedKeysForTerm(term string) {
	enableExtendedKeys(term)
}

func enableExtendedKeys(term string) {
	if extendedKeysNeedsEnable(tmuxShowOption("-sv", "extended-keys")) {
		_ = exec.Command("tmux", "set-option", "-s", "extended-keys", "on").Run()
	}
	if term = strings.TrimSpace(term); term == "" {
		return
	}
	if !terminalFeaturesHaveExtkeys(tmuxShowOption("-s", "terminal-features"), term) {
		// Lead with a comma: the portable tmux idiom that guarantees a new
		// terminal-features array entry rather than concatenating onto the last
		// one. tmux consumes the comma as a separator, so the stored value stays
		// "<term>:extkeys" (which the idempotency check above looks for).
		_ = exec.Command("tmux", "set-option", "-as", "terminal-features", ","+term+":extkeys").Run()
	}
}

// extendedKeysNeedsEnable reports whether the server extended-keys value is off
// (or unknown), so an explicit on/always set by the user is left intact.
func extendedKeysNeedsEnable(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on", "always":
		return false
	default:
		return true
	}
}

// terminalFeaturesHaveExtkeys reports whether the terminal-features listing
// already advertises extkeys for term, keeping the append idempotent.
func terminalFeaturesHaveExtkeys(features, term string) bool {
	term = strings.TrimSpace(term)
	return term != "" && strings.Contains(features, term+":extkeys")
}

// tmuxShowOption returns the trimmed `tmux show-options` output, or "" on error.
func tmuxShowOption(args ...string) string {
	out, err := exec.Command("tmux", append([]string{"show-options"}, args...)...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// clientTermName reports the TERM of the attached tmux client (the outer
// terminal), or "" if it cannot be resolved.
func clientTermName() string {
	out, err := exec.Command("tmux", "display-message", "-p", "#{client_termname}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
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

// SendLiteralLine types text into paneID literally and submits it with Enter,
// as `fanout msg nudge` does to drop a one-line hint into a peer agent's
// input. It targets the pane id directly (not through exactSessionTarget,
// which is the session-name "=" seam) and uses two send-keys calls on purpose:
// -l forces literal interpretation so a text containing tmux key names ("C-c",
// "Enter") is typed verbatim, but -l applies to every argument of one call, so
// the submitting Enter must be a separate, non-literal send-keys. The "--"
// terminates option parsing before the payload: without it a text starting
// with "-" (e.g. "-n ...") is rejected as an unknown flag (verified on tmux
// 3.6a), which would matter for future reusers of this primitive even though
// today's only caller passes a fixed hint. The split is not transactional: if
// the literal send succeeds but the Enter fails the hint sits unsubmitted in
// the input buffer, which is harmless (it is just text) and fits the
// best-effort contract — the message it points at is already persisted, so a
// failed nudge never loses information.
func SendLiteralLine(paneID, text string) error {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return fmt.Errorf("pane id is required")
	}
	if err := exec.Command("tmux", "send-keys", "-t", paneID, "-l", "--", text).Run(); err != nil {
		return fmt.Errorf("tmux send-keys -l: %w", err)
	}
	if err := exec.Command("tmux", "send-keys", "-t", paneID, "Enter").Run(); err != nil {
		return fmt.Errorf("tmux send-keys Enter: %w", err)
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
