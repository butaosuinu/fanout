package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/log"
	"github.com/butaosuinu/fanout/internal/state"
)

const statusDiffBarWidth = 12

var conventionalTypeRE = regexp.MustCompile(`^(feat|fix|docs|chore|refactor|test|perf|build|ci|style|revert)(\(.+\))?!?:`)

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

type statusTableRow struct {
	Issue      string
	IssueState string
	PR         string
	PRState    string
	Type       string
	Files      string
	Link       string
	Additions  int
	Deletions  int
	HasDiff    bool
}

func cmdStatus(cfg *cliflags.Config, lg *log.Logger) exitcode.Code {
	rt, code := resolveStateRuntimeForMode("--status", lg)
	if code != exitcode.OK {
		return code
	}
	if rt.projectRoot == "" || !dirExists(rt.projectRoot) {
		lg.Err("--status: project_root is not a directory: %s (state=%s)", emptyLabel(rt.projectRoot), rt.statePath)
		return exitcode.Invocation
	}

	store, err := state.Load(rt.statePath)
	if err != nil {
		lg.Err("--status: fanout state at %s is not valid JSON or has an invalid schema: %v", rt.statePath, err)
		return exitcode.Invocation
	}
	nums := sortedKeys(store.FannedNumbersForParent(cfg.ParentRef))
	if len(nums) == 0 {
		report := statusReport{
			Parent:   cfg.Parent,
			Children: []statusChild{},
			Summary:  statusSummary{AllMerged: false},
		}
		if cfg.Format == "table" {
			return writeStatusTable(report, rt.projectRoot, lg)
		}
		return writeStatusReport(report, lg)
	}

	children, code := statusChildren(rt.projectRoot, nums, "--status", lg)
	if code != exitcode.OK {
		return code
	}
	report := newStatusReport(cfg.Parent, children)
	if cfg.Format == "table" {
		return writeStatusTable(report, rt.projectRoot, lg)
	}
	return writeStatusReport(report, lg)
}

func statusChildren(projectRoot string, nums []int, mode string, lg *log.Logger) ([]statusChild, exitcode.Code) {
	gh := ghissue.Runner{Cwd: projectRoot}
	nwo, err := gh.RepoNameWithOwner()
	if err != nil {
		lg.Err("%s: failed to resolve repo (gh repo view) in %s", mode, projectRoot)
		return nil, exitcode.GitHub
	}
	owner, repo, ok := strings.Cut(nwo, "/")
	if !ok || owner == "" || repo == "" {
		lg.Err("%s: unexpected nameWithOwner from gh: %s", mode, nwo)
		return nil, exitcode.GitHub
	}

	children := make([]statusChild, 0, len(nums))
	for _, num := range nums {
		state, prs, err := gh.IssueWithPRs(owner, repo, num)
		if err != nil {
			lg.Err("%s: gh api graphql for #%d failed or returned no issue (auth / network / not found)", mode, num)
			return nil, exitcode.GitHub
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
	return children, exitcode.OK
}

func newStatusReport(parent int, children []statusChild) statusReport {
	merged := 0
	for _, child := range children {
		if child.HasMergedPR {
			merged++
		}
	}
	return statusReport{
		Parent:   parent,
		Children: children,
		Summary: statusSummary{
			Total:     len(children),
			Merged:    merged,
			Pending:   len(children) - merged,
			AllMerged: len(children) > 0 && merged == len(children),
		},
	}
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

func writeStatusTable(report statusReport, projectRoot string, lg *log.Logger) exitcode.Code {
	rows, maxLines, addWidth, delWidth, code := statusTableRows(report, projectRoot, lg)
	if code != exitcode.OK {
		return code
	}

	out := lg.Stdout()
	fmt.Fprintf(out, "fanout status #%d: total=%d merged=%d pending=%d all_merged=%t\n",
		report.Parent, report.Summary.Total, report.Summary.Merged, report.Summary.Pending, report.Summary.AllMerged)
	if len(rows) == 0 {
		fmt.Fprintln(out, "(no recorded children)")
		return exitcode.OK
	}

	diffWidth := statusDiffWidth(addWidth, delWidth)
	widths := []int{
		len("ISSUE"),
		len("STATE"),
		len("PR"),
		len("PR_STATE"),
		len("TYPE"),
		len("FILES"),
		diffWidth,
		len("LINK"),
	}
	for _, row := range rows {
		widths[0] = maxInt(widths[0], len(row.Issue))
		widths[1] = maxInt(widths[1], len(row.IssueState))
		widths[2] = maxInt(widths[2], len(row.PR))
		widths[3] = maxInt(widths[3], len(row.PRState))
		widths[4] = maxInt(widths[4], len(row.Type))
		widths[5] = maxInt(widths[5], len(row.Files))
	}

	headers := []string{"ISSUE", "STATE", "PR", "PR_STATE", "TYPE", "FILES", "DIFF", "LINK"}
	fmt.Fprintln(out, statusTableLine(headers, widths))
	separators := []string{
		strings.Repeat("-", widths[0]),
		strings.Repeat("-", widths[1]),
		strings.Repeat("-", widths[2]),
		strings.Repeat("-", widths[3]),
		strings.Repeat("-", widths[4]),
		strings.Repeat("-", widths[5]),
		strings.Repeat("-", diffWidth),
		strings.Repeat("-", widths[7]),
	}
	fmt.Fprintln(out, statusTableLine(separators, widths))

	colors := lg.Colors()
	for _, row := range rows {
		cols := []string{
			row.Issue,
			row.IssueState,
			row.PR,
			row.PRState,
			row.Type,
			row.Files,
			renderStatusDiff(row, maxLines, addWidth, delWidth, diffWidth, colors),
			row.Link,
		}
		fmt.Fprintln(out, statusTableLine(cols, widths))
	}
	return exitcode.OK
}

func statusTableRows(report statusReport, projectRoot string, lg *log.Logger) ([]statusTableRow, int, int, int, exitcode.Code) {
	if len(report.Children) == 0 {
		return nil, 0, len("+0"), len("-0"), exitcode.OK
	}

	gh := ghissue.Runner{Cwd: projectRoot}
	nwo, err := gh.RepoNameWithOwner()
	if err != nil {
		lg.Err("--status: failed to resolve repo (gh repo view) in %s", projectRoot)
		return nil, 0, 0, 0, exitcode.GitHub
	}

	rows := make([]statusTableRow, 0, len(report.Children))
	maxLines := 0
	addWidth := len("+0")
	delWidth := len("-0")
	for _, child := range report.Children {
		if len(child.PRs) == 0 {
			rows = append(rows, statusTableRow{
				Issue:      "#" + strconv.Itoa(child.Num),
				IssueState: dashIfEmpty(child.State),
				PR:         "-",
				PRState:    "-",
				Type:       "-",
				Files:      "-",
				Link:       "-",
			})
			continue
		}
		for _, pr := range child.PRs {
			stat, err := gh.PRDiffStat(pr.Number)
			if err != nil {
				lg.Err("--status: gh pr view #%d failed: %v", pr.Number, err)
				return nil, 0, 0, 0, exitcode.GitHub
			}
			addWidth = maxInt(addWidth, len(fmt.Sprintf("+%d", stat.Additions)))
			delWidth = maxInt(delWidth, len(fmt.Sprintf("-%d", stat.Deletions)))
			maxLines = maxInt(maxLines, stat.Additions)
			maxLines = maxInt(maxLines, stat.Deletions)
			rows = append(rows, statusTableRow{
				Issue:      "#" + strconv.Itoa(child.Num),
				IssueState: dashIfEmpty(child.State),
				PR:         "#" + strconv.Itoa(pr.Number),
				PRState:    dashIfEmpty(pr.State),
				Type:       conventionalType(stat.Title),
				Files:      strconv.Itoa(stat.ChangedFiles),
				Link:       fmt.Sprintf("https://github.com/%s/pull/%d", nwo, pr.Number),
				Additions:  stat.Additions,
				Deletions:  stat.Deletions,
				HasDiff:    true,
			})
		}
	}
	return rows, maxLines, addWidth, delWidth, exitcode.OK
}

func conventionalType(title string) string {
	m := conventionalTypeRE.FindStringSubmatch(title)
	if len(m) < 2 {
		return "-"
	}
	return m[1]
}

func renderStatusDiff(row statusTableRow, maxLines, addWidth, delWidth, diffWidth int, colors log.Palette) string {
	if !row.HasDiff {
		return padRight("-", diffWidth)
	}
	addNum := fmt.Sprintf("+%d", row.Additions)
	delNum := fmt.Sprintf("-%d", row.Deletions)
	addBar := strings.Repeat("+", scaledStatusBar(row.Additions, maxLines))
	delBar := strings.Repeat("-", scaledStatusBar(row.Deletions, maxLines))
	return colorPad(colors.Ok, colors.Reset, addNum, addWidth) + " " +
		colorPad(colors.Ok, colors.Reset, addBar, statusDiffBarWidth) + "  " +
		colorPad(colors.Err, colors.Reset, delNum, delWidth) + " " +
		colorPad(colors.Err, colors.Reset, delBar, statusDiffBarWidth)
}

func statusDiffWidth(addWidth, delWidth int) int {
	return addWidth + 1 + statusDiffBarWidth + 2 + delWidth + 1 + statusDiffBarWidth
}

func scaledStatusBar(value, maxValue int) int {
	if value <= 0 || maxValue <= 0 {
		return 0
	}
	n := (value*statusDiffBarWidth + maxValue - 1) / maxValue
	if n < 1 {
		return 1
	}
	if n > statusDiffBarWidth {
		return statusDiffBarWidth
	}
	return n
}

func colorPad(color, reset, s string, width int) string {
	return colorWrap(color, reset, s) + strings.Repeat(" ", maxInt(0, width-len(s)))
}

func colorWrap(color, reset, s string) string {
	if color == "" || s == "" {
		return s
	}
	return color + s + reset
}

func statusTableLine(cols []string, widths []int) string {
	var b strings.Builder
	for i, col := range cols {
		if i > 0 {
			b.WriteString("  ")
		}
		if i == len(cols)-1 {
			b.WriteString(col)
			continue
		}
		b.WriteString(padRight(col, widths[i]))
	}
	return b.String()
}

func padRight(s string, width int) string {
	return s + strings.Repeat(" ", maxInt(0, width-len(s)))
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func sortedKeys(set map[int]bool) []int {
	nums := make([]int, 0, len(set))
	for n := range set {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	return nums
}
