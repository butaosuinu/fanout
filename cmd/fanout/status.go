package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/blockers"
	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/log"
	"github.com/butaosuinu/fanout/internal/state"
)

const statusDiffBarWidth = 12

var (
	conventionalTypeRE      = regexp.MustCompile(`^(feat|fix|docs|chore|refactor|test|perf|build|ci|style|revert)(\(.+\))?!?:`)
	dashboardReviewEffortRE = regexp.MustCompile(`(?im)^\s*(?:[-*]\s*)?(?:\*\*)?Review effort(?:\*\*)?\s*:\s*([0-5])\b.*$`)
	dashboardTLDRLineRE     = regexp.MustCompile(`(?i)^\s*(?:#{1,6}\s*)?(?:[-*]\s*)?(?:[0-9]+\.\s*)?(?:\*\*)?TL;DR(?:\*\*)?\s*(?::|\x{2014}|-)?\s*(.*)$`)
)

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
	Body        string          `json:"-"`
	Blocked     bool            `json:"-"`
}

type statusSummary struct {
	Total     int  `json:"total"`
	Merged    int  `json:"merged"`
	Pending   int  `json:"pending"`
	Blocked   int  `json:"blocked"`
	AllMerged bool `json:"all_merged"`
}

type statusTableRow struct {
	Issue      string
	IssueState string
	PR         string
	PRState    string
	CI         string
	Type       string
	Files      string
	Link       string
	Additions  int
	Deletions  int
	HasDiff    bool
}

type dashboardRow struct {
	Issue   string
	PR      string
	PRState string
	CI      string
	Diff    string
	Type    string
	TLDR    string
	Score   string
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
			if code = writeStatusTable(report, rt.projectRoot, lg); code != exitcode.OK {
				return code
			}
		} else if code = writeStatusReport(report, lg); code != exitcode.OK {
			return code
		}
		if cfg.PostDashboard {
			return postStatusDashboard(report, rt.projectRoot, lg)
		}
		return exitcode.OK
	}

	children, code := statusChildren(rt.projectRoot, nums, "--status", lg)
	if code != exitcode.OK {
		return code
	}
	markStatusBlockers(rt.projectRoot, cfg.Parent, children)
	report := newStatusReport(cfg.Parent, children)
	if cfg.Format == "table" {
		if code := writeStatusTable(report, rt.projectRoot, lg); code != exitcode.OK {
			return code
		}
	} else if code := writeStatusReport(report, lg); code != exitcode.OK {
		return code
	}
	if cfg.PostDashboard {
		return postStatusDashboard(report, rt.projectRoot, lg)
	}
	return exitcode.OK
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
		snapshot, err := gh.IssueSnapshotWithPRs(owner, repo, num)
		if err != nil {
			lg.Err("%s: gh api graphql for #%d failed or returned no issue (auth / network / not found)", mode, num)
			return nil, exitcode.GitHub
		}
		child := statusChild{Num: num, State: snapshot.State, Body: snapshot.Body, PRs: snapshot.PRs}
		for _, pr := range snapshot.PRs {
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
	blocked := 0
	for _, child := range children {
		if child.HasMergedPR {
			merged++
		}
		if child.Blocked {
			blocked++
		}
	}
	return statusReport{
		Parent:   parent,
		Children: children,
		Summary: statusSummary{
			Total:     len(children),
			Merged:    merged,
			Pending:   len(children) - merged,
			Blocked:   blocked,
			AllMerged: len(children) > 0 && merged == len(children),
		},
	}
}

func markStatusBlockers(projectRoot string, parent int, children []statusChild) {
	gh := ghissue.Runner{Cwd: projectRoot}
	parentBody, err := gh.ParentBody(parent)
	if err != nil {
		parentBody = ""
	}
	childStates := make(map[int]string, len(children))
	for _, child := range children {
		childStates[child.Num] = child.State
	}
	stateCache := map[int]string{}
	issueState := func(num int) string {
		if state, ok := childStates[num]; ok && strings.TrimSpace(state) != "" {
			return state
		}
		if state, ok := stateCache[num]; ok {
			return state
		}
		state, err := gh.IssueState(num)
		if err != nil {
			state = "UNKNOWN"
		}
		stateCache[num] = state
		return state
	}

	for i := range children {
		refs := blockers.Dedupe(
			blockers.FromChildBody(children[i].Body),
			blockers.FromParentRow(parentBody, children[i].Num),
		)
		children[i].Blocked = hasOpenStatusBlocker(refs, issueState)
	}
}

func hasOpenStatusBlocker(refs []int, issueState func(int) string) bool {
	for _, num := range refs {
		if issueState(num) == "OPEN" {
			return true
		}
	}
	return false
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

func postStatusDashboard(report statusReport, projectRoot string, lg *log.Logger) exitcode.Code {
	gh := ghissue.Runner{Cwd: projectRoot}
	nwo, err := gh.RepoNameWithOwner()
	if err != nil {
		lg.Err("--status --post-dashboard: failed to resolve repo (gh repo view) in %s", projectRoot)
		return exitcode.GitHub
	}
	owner, repo, ok := strings.Cut(nwo, "/")
	if !ok || owner == "" || repo == "" {
		lg.Err("--status --post-dashboard: unexpected nameWithOwner from gh: %s", nwo)
		return exitcode.GitHub
	}

	rows, code := dashboardRows(report, projectRoot, nwo, lg)
	if code != exitcode.OK {
		return code
	}
	body := buildDashboardBody(report.Parent, report.Summary, rows)
	marker := dashboardMarker(report.Parent)
	comment, found, err := gh.FindDashboardComment(report.Parent, marker)
	if err != nil {
		lg.Err("--status --post-dashboard: gh api issue comments for #%d failed: %v", report.Parent, err)
		return exitcode.GitHub
	}
	if found {
		if err := gh.EditIssueComment(owner, repo, comment.ID, body); err != nil {
			lg.Err("--status --post-dashboard: failed to update dashboard comment %s on #%d: %v", comment.ID, report.Parent, err)
			return exitcode.GitHub
		}
		fmt.Fprintf(lg.Stderr(), "[ ok ] updated dashboard comment %s on issue #%d\n", comment.ID, report.Parent)
		return exitcode.OK
	}
	if err := gh.PostIssueComment(report.Parent, body); err != nil {
		lg.Err("--status --post-dashboard: failed to post dashboard comment on #%d: %v", report.Parent, err)
		return exitcode.GitHub
	}
	fmt.Fprintf(lg.Stderr(), "[ ok ] posted dashboard comment on issue #%d\n", report.Parent)
	return exitcode.OK
}

func dashboardRows(report statusReport, projectRoot, nwo string, lg *log.Logger) ([]dashboardRow, exitcode.Code) {
	if len(report.Children) == 0 {
		return nil, exitcode.OK
	}
	gh := ghissue.Runner{Cwd: projectRoot}
	rows := make([]dashboardRow, 0, len(report.Children))
	for _, child := range report.Children {
		if len(child.PRs) == 0 {
			rows = append(rows, dashboardRow{
				Issue:   "#" + strconv.Itoa(child.Num),
				PR:      "-",
				PRState: "-",
				CI:      "-",
				Diff:    "-",
				Type:    "-",
				TLDR:    "No PR yet",
				Score:   "-",
			})
			continue
		}
		for _, pr := range child.PRs {
			stat, err := gh.PRDiffStat(pr.Number)
			if err != nil {
				lg.Err("--status --post-dashboard: gh pr view #%d failed: %v", pr.Number, err)
				return nil, exitcode.GitHub
			}
			tldr, score := extractDashboardPRBody(stat.Body)
			rows = append(rows, dashboardRow{
				Issue:   "#" + strconv.Itoa(child.Num),
				PR:      fmt.Sprintf("[#%d](https://github.com/%s/pull/%d)", pr.Number, nwo, pr.Number),
				PRState: dashIfEmpty(pr.DisplayState()),
				CI:      dashIfEmpty(pr.CIStatus),
				Diff:    dashboardDiff(stat),
				Type:    conventionalType(stat.Title),
				TLDR:    tldr,
				Score:   score,
			})
		}
	}
	return rows, exitcode.OK
}

func buildDashboardBody(parent int, summary statusSummary, rows []dashboardRow) string {
	var b strings.Builder
	b.WriteString(dashboardMarker(parent))
	b.WriteString("\n")
	fmt.Fprintf(&b, "## fanout dashboard #%d\n\n", parent)
	fmt.Fprintf(&b, "Total: %d | Merged: %d | Pending: %d | Blocked: %d | All merged: %t\n\n",
		summary.Total, summary.Merged, summary.Pending, summary.Blocked, summary.AllMerged)
	b.WriteString("| Sub-issue # | PR | PR state | CI | +/- | Type | TL;DR | Score |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- |\n")
	if len(rows) == 0 {
		b.WriteString("| - | - | - | - | - | - | No recorded children | - |\n")
		return b.String()
	}
	for _, row := range rows {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s |\n",
			markdownCell(row.Issue),
			markdownCell(row.PR),
			markdownCell(row.PRState),
			markdownCell(row.CI),
			markdownCell(row.Diff),
			markdownCell(row.Type),
			markdownCell(row.TLDR),
			markdownCell(row.Score),
		)
	}
	return b.String()
}

func dashboardMarker(parent int) string {
	return fmt.Sprintf("<!-- fanout:dashboard parent=%d -->", parent)
}

func dashboardDiff(stat ghissue.PRDiffStat) string {
	files := "files"
	if stat.ChangedFiles == 1 {
		files = "file"
	}
	return fmt.Sprintf("+%d / -%d (%d %s)", stat.Additions, stat.Deletions, stat.ChangedFiles, files)
}

func extractDashboardPRBody(body string) (string, string) {
	lines := strings.Split(body, "\n")
	tldr := explicitDashboardTLDR(lines)
	if tldr == "" {
		tldr = fallbackDashboardTLDR(lines)
	}
	score := "-"
	if m := dashboardReviewEffortRE.FindStringSubmatch(body); len(m) == 2 {
		score = m[1]
	}
	return tldr, score
}

func explicitDashboardTLDR(lines []string) string {
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if m := dashboardTLDRLineRE.FindStringSubmatch(line); len(m) == 2 {
			if rest := strings.TrimSpace(m[1]); rest != "" {
				return rest
			}
			for _, next := range lines[i+1:] {
				next = strings.TrimSpace(next)
				if next == "" || dashboardReviewEffortRE.MatchString(next) {
					continue
				}
				return next
			}
		}
	}
	return ""
}

func fallbackDashboardTLDR(lines []string) string {
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || dashboardReviewEffortRE.MatchString(line) || dashboardTLDRLineRE.MatchString(line) {
			continue
		}
		return line
	}
	return "-"
}

func markdownCell(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "-"
	}
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", `\|`)
	return s
}

func writeStatusTable(report statusReport, projectRoot string, lg *log.Logger) exitcode.Code {
	rows, maxLines, addWidth, delWidth, code := statusTableRows(report, projectRoot, lg)
	if code != exitcode.OK {
		return code
	}

	out := lg.Stdout()
	fmt.Fprintf(out, "fanout status #%d: total=%d merged=%d pending=%d blocked=%d all_merged=%t\n",
		report.Parent, report.Summary.Total, report.Summary.Merged, report.Summary.Pending, report.Summary.Blocked, report.Summary.AllMerged)
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
		len("CI"),
		len("TYPE"),
		len("FILES"),
		diffWidth,
		len("LINK"),
	}
	for _, row := range rows {
		widths[0] = max(widths[0], len(row.Issue))
		widths[1] = max(widths[1], len(row.IssueState))
		widths[2] = max(widths[2], len(row.PR))
		widths[3] = max(widths[3], len(row.PRState))
		widths[4] = max(widths[4], len(row.CI))
		widths[5] = max(widths[5], len(row.Type))
		widths[6] = max(widths[6], len(row.Files))
	}

	headers := []string{"ISSUE", "STATE", "PR", "PR_STATE", "CI", "TYPE", "FILES", "DIFF", "LINK"}
	fmt.Fprintln(out, statusTableLine(headers, widths))
	separators := []string{
		strings.Repeat("-", widths[0]),
		strings.Repeat("-", widths[1]),
		strings.Repeat("-", widths[2]),
		strings.Repeat("-", widths[3]),
		strings.Repeat("-", widths[4]),
		strings.Repeat("-", widths[5]),
		strings.Repeat("-", widths[6]),
		strings.Repeat("-", diffWidth),
		strings.Repeat("-", widths[8]),
	}
	fmt.Fprintln(out, statusTableLine(separators, widths))

	colors := lg.Colors()
	for _, row := range rows {
		cols := []string{
			row.Issue,
			row.IssueState,
			row.PR,
			row.PRState,
			row.CI,
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
				CI:         "-",
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
			addWidth = max(addWidth, len(fmt.Sprintf("+%d", stat.Additions)))
			delWidth = max(delWidth, len(fmt.Sprintf("-%d", stat.Deletions)))
			maxLines = max(maxLines, stat.Additions)
			maxLines = max(maxLines, stat.Deletions)
			rows = append(rows, statusTableRow{
				Issue:      "#" + strconv.Itoa(child.Num),
				IssueState: dashIfEmpty(child.State),
				PR:         "#" + strconv.Itoa(pr.Number),
				PRState:    dashIfEmpty(pr.DisplayState()),
				CI:         dashIfEmpty(pr.CIStatus),
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
	return colorWrap(color, reset, s) + strings.Repeat(" ", max(0, width-len(s)))
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
	return s + strings.Repeat(" ", max(0, width-len(s)))
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func sortedKeys(set map[int]bool) []int {
	nums := make([]int, 0, len(set))
	for n := range set {
		nums = append(nums, n)
	}
	slices.Sort(nums)
	return nums
}
