package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/dmuxconfig"
	"github.com/butaosuinu/fanout/internal/dmuxsession"
	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/log"
)

type statusRuntime struct {
	configPath  string
	projectRoot string
	config      *dmuxconfig.Config
}

type statusReport struct {
	Parent   int           `json:"parent"`
	Children []statusChild `json:"children"`
	Summary  statusSummary `json:"summary"`
}

type statusChild struct {
	Num         int             `json:"num"`
	State       string          `json:"state"`
	PRs         []ghissue.PRRef `json:"prs"`
	HasMergedPR bool            `json:"has_merged_pr"`
}

type statusSummary struct {
	Total     int  `json:"total"`
	Merged    int  `json:"merged"`
	Pending   int  `json:"pending"`
	AllMerged bool `json:"all_merged"`
}

func cmdStatus(cfg *cliflags.Config, lg *log.Logger) exitcode.Code {
	rt, code := resolveStatusRuntime(cfg, lg)
	if code != exitcode.OK {
		return code
	}
	if rt.projectRoot == "" || !dirExists(rt.projectRoot) {
		lg.Err("--status: project_root is not a directory: %s (config=%s)", emptyLabel(rt.projectRoot), rt.configPath)
		return exitcode.Invocation
	}

	fanned := rt.config.FannedNumbersForParent(cfg.ParentRef, nil)
	nums := sortedKeys(fanned)
	if len(nums) == 0 {
		return writeStatusReport(statusReport{
			Parent:   cfg.Parent,
			Children: []statusChild{},
			Summary:  statusSummary{AllMerged: false},
		}, lg)
	}

	gh := ghissue.Runner{Cwd: rt.projectRoot}
	nwo, err := gh.RepoNameWithOwner()
	if err != nil {
		lg.Err("--status: failed to resolve repo (gh repo view) in %s", rt.projectRoot)
		return exitcode.GitHub
	}
	owner, repo, ok := strings.Cut(nwo, "/")
	if !ok || owner == "" || repo == "" {
		lg.Err("--status: unexpected nameWithOwner from gh: %s", nwo)
		return exitcode.GitHub
	}

	children := make([]statusChild, 0, len(nums))
	for _, num := range nums {
		state, prs, err := gh.IssueWithPRs(owner, repo, num)
		if err != nil {
			lg.Err("--status: gh api graphql for #%d failed or returned no issue (auth / network / not found)", num)
			return exitcode.GitHub
		}
		child := statusChild{Num: num, State: state, PRs: prs}
		for _, pr := range prs {
			if pr.State == "MERGED" {
				child.HasMergedPR = true
				break
			}
		}
		children = append(children, child)
	}

	merged := 0
	for _, child := range children {
		if child.HasMergedPR {
			merged++
		}
	}
	report := statusReport{
		Parent:   cfg.Parent,
		Children: children,
		Summary: statusSummary{
			Total:     len(children),
			Merged:    merged,
			Pending:   len(children) - merged,
			AllMerged: len(children) > 0 && merged == len(children),
		},
	}
	return writeStatusReport(report, lg)
}

func resolveStatusRuntime(cfg *cliflags.Config, lg *log.Logger) (statusRuntime, exitcode.Code) {
	if p := os.Getenv("DMUX_CONFIG_PATH"); p != "" {
		cfg, err := loadStatusConfig(p, lg)
		if err != nil {
			return statusRuntime{}, exitcode.Invocation
		}
		root := cfg.ProjectRoot()
		if root == "" {
			root = filepath.Dir(filepath.Dir(p))
		}
		return statusRuntime{configPath: p, projectRoot: root, config: cfg}, exitcode.OK
	}

	sessions, err := listTmuxSessions()
	if err != nil {
		lg.Err("--status: no tmux server is running (set DMUX_CONFIG_PATH to bypass live-session discovery)")
		return statusRuntime{}, exitcode.Invocation
	}
	var dmuxSessions []string
	for _, s := range sessions {
		if dmuxsession.IsDmux(s) {
			dmuxSessions = append(dmuxSessions, s)
		}
	}
	if len(dmuxSessions) == 0 {
		lg.Err("--status: no active dmux session found (set DMUX_CONFIG_PATH to bypass)")
		return statusRuntime{}, exitcode.Invocation
	}

	session := ""
	if cfg.Session != "" {
		for _, s := range dmuxSessions {
			if s == cfg.Session {
				session = s
				break
			}
		}
		if session == "" {
			lg.Err("--status: tmux session '%s' is not running dmux. Active dmux sessions: %s", cfg.Session, strings.Join(dmuxSessions, " "))
			return statusRuntime{}, exitcode.Invocation
		}
	} else if len(dmuxSessions) > 1 {
		lg.Err("--status: multiple dmux sessions active (%s); pass --session <name> to pick one", strings.Join(dmuxSessions, " "))
		return statusRuntime{}, exitcode.Invocation
	} else {
		session = dmuxSessions[0]
	}

	configPath := dmuxsession.TmuxOption(session, "@dmux_config_path")
	projectRoot := dmuxsession.TmuxOption(session, "@dmux_project_root")
	if configPath == "" || !fileExists(configPath) {
		lg.Err("--status: dmux config not found at %s", emptyUnset(configPath))
		return statusRuntime{}, exitcode.Invocation
	}
	cfgFile, err := loadStatusConfig(configPath, lg)
	if err != nil {
		return statusRuntime{}, exitcode.Invocation
	}
	if projectRoot == "" {
		lg.Err("--status: session '%s' has no @dmux_project_root option", session)
		return statusRuntime{}, exitcode.Invocation
	}
	return statusRuntime{configPath: configPath, projectRoot: projectRoot, config: cfgFile}, exitcode.OK
}

func loadStatusConfig(path string, lg *log.Logger) (*dmuxconfig.Config, error) {
	if !fileExists(path) {
		lg.Err("--status: $DMUX_CONFIG_PATH points to non-existent file: %s", path)
		return nil, fmt.Errorf("missing config")
	}
	cfg, err := dmuxconfig.Load(path)
	if err != nil {
		lg.Err("--status: dmux config at %s is not valid JSON or .panes is not an array", path)
		return nil, err
	}
	return cfg, nil
}

func writeStatusReport(report statusReport, lg *log.Logger) exitcode.Code {
	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		lg.Err("--status: failed to encode report: %v", err)
		return exitcode.GitHub
	}
	fmt.Fprintln(lg.Stdout(), string(out))
	return exitcode.OK
}

func listTmuxSessions() ([]string, error) {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return nil, err
	}
	var sessions []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line != "" {
			sessions = append(sessions, line)
		}
	}
	return sessions, nil
}

func sortedKeys(set map[int]bool) []int {
	nums := make([]int, 0, len(set))
	for n := range set {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	return nums
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func emptyUnset(s string) string {
	if s == "" {
		return "<unset>"
	}
	return s
}

func emptyLabel(s string) string {
	if s == "" {
		return "<empty>"
	}
	return s
}
