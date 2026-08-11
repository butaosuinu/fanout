package statusreport

import (
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/cliview"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/log"
)

const diffBarWidth = 12

var conventionalTypeRE = regexp.MustCompile(`^(feat|fix|docs|chore|refactor|test|perf|build|ci|style|revert)(\(.+\))?!?:`)

// PRStatSource is the slice of ghissue.Runner the table builder consumes.
type PRStatSource interface {
	RepoNameWithOwner() (string, error)
	PRDiffStat(num int) (ghissue.PRDiffStat, error)
}

// RowSource is one status table input row: an issue label ("#123") or a
// plan task ID, its recorded runtime identity, display state, and PRs.
type RowSource struct {
	Label         string
	Backend       backend.Name
	PaneID        string
	ReportedState string
	State         string
	PRs           []ghissue.PRRef
}

// TableRow is one rendered status table row; PR-less sources collapse to
// a single dash row.
type TableRow struct {
	Label         string
	Backend       string
	PaneID        string
	ReportedState string
	State         string
	PR            string
	PRState       string
	CI            string
	Type          string
	Files         string
	Link          string
	Additions     int
	Deletions     int
	HasDiff       bool
}

// TableSpec carries the wording differences between the issue table and
// the plan table.
type TableSpec struct {
	Heading        string // summary line printed above the table
	EmptyText      string // printed instead of the table when rows is empty
	FirstColHeader string // "ISSUE" or "TASK"
}

// BuildTableRows expands sources into table rows, fetching each PR's diff
// stat. It also returns the largest diff line count and the widths of the
// widest +N / -N cells for bar scaling.
func BuildTableRows(gh PRStatSource, projectRoot string, sources []RowSource, lg *log.Logger) ([]TableRow, int, int, int, exitcode.Code) {
	if len(sources) == 0 {
		return nil, 0, len("+0"), len("-0"), exitcode.OK
	}

	nwo, err := gh.RepoNameWithOwner()
	if err != nil {
		lg.Err("--status: failed to resolve repo (gh repo view) in %s", projectRoot)
		return nil, 0, 0, 0, exitcode.GitHub
	}

	rows := make([]TableRow, 0, len(sources))
	maxLines := 0
	addWidth := len("+0")
	delWidth := len("-0")
	for _, src := range sources {
		if len(src.PRs) == 0 {
			row := newStatusTableRow(src)
			row.PR, row.PRState, row.CI = "-", "-", "-"
			row.Type, row.Files, row.Link = "-", "-", "-"
			rows = append(rows, row)
			continue
		}
		for _, pr := range src.PRs {
			stat, err := gh.PRDiffStat(pr.Number)
			if err != nil {
				lg.Err("--status: gh pr view #%d failed: %v", pr.Number, err)
				return nil, 0, 0, 0, exitcode.GitHub
			}
			addWidth = max(addWidth, len(fmt.Sprintf("+%d", stat.Additions)))
			delWidth = max(delWidth, len(fmt.Sprintf("-%d", stat.Deletions)))
			maxLines = max(maxLines, stat.Additions)
			maxLines = max(maxLines, stat.Deletions)
			rows = append(rows, statusTablePRRow(src, pr, stat, nwo))
		}
	}
	return rows, maxLines, addWidth, delWidth, exitcode.OK
}

func statusTablePRRow(src RowSource, pr ghissue.PRRef, stat ghissue.PRDiffStat, nwo string) TableRow {
	row := newStatusTableRow(src)
	row.PR = "#" + strconv.Itoa(pr.Number)
	row.PRState = cliview.DashIfEmpty(pr.DisplayState())
	row.CI = cliview.DashIfEmpty(pr.CIStatus)
	row.Type = conventionalType(stat.Title)
	row.Files = strconv.Itoa(stat.ChangedFiles)
	row.Link = fmt.Sprintf("https://github.com/%s/pull/%d", nwo, pr.Number)
	row.Additions, row.Deletions, row.HasDiff = stat.Additions, stat.Deletions, true
	return row
}

func newStatusTableRow(src RowSource) TableRow {
	return TableRow{
		Label: src.Label, Backend: cliview.DashIfEmpty(string(src.Backend)),
		PaneID: cliview.DashIfEmpty(src.PaneID), ReportedState: cliview.DashIfEmpty(src.ReportedState),
		State: cliview.DashIfEmpty(src.State),
	}
}

// WriteTable prints the heading, then either spec.EmptyText or the full
// header/separator/rows table with the scaled diff bars.
func WriteTable(out io.Writer, colors log.Palette, spec TableSpec, rows []TableRow, maxLines, addWidth, delWidth int) {
	fmt.Fprintln(out, spec.Heading)
	if len(rows) == 0 {
		fmt.Fprintln(out, spec.EmptyText)
		return
	}

	diffWidth := statusDiffWidth(addWidth, delWidth)
	headers := []string{
		spec.FirstColHeader, "BACKEND", "PANE", "REPORTED_STATE", "STATE", "PR",
		"PR_STATE", "CI", "TYPE", "FILES", "DIFF", "LINK",
	}
	widths := make([]int, len(headers))
	for i, header := range headers {
		widths[i] = len(header)
	}
	widths[10] = diffWidth
	for _, row := range rows {
		columns := statusTableColumns(row, "")
		for i, value := range columns[:len(columns)-1] {
			widths[i] = max(widths[i], len(value))
		}
	}

	fmt.Fprintln(out, cliview.TableLine(headers, widths))
	separators := make([]string, len(widths))
	for i, width := range widths {
		separators[i] = strings.Repeat("-", width)
	}
	fmt.Fprintln(out, cliview.TableLine(separators, widths))

	for _, row := range rows {
		diff := renderStatusDiff(row, maxLines, addWidth, delWidth, diffWidth, colors)
		cols := statusTableColumns(row, diff)
		fmt.Fprintln(out, cliview.TableLine(cols, widths))
	}
}

func statusTableColumns(row TableRow, diff string) []string {
	return []string{
		row.Label, row.Backend, row.PaneID, row.ReportedState, row.State, row.PR,
		row.PRState, row.CI, row.Type, row.Files, diff, row.Link,
	}
}

// WriteIssueTable renders the issue-mode report as the status table.
func WriteIssueTable(report Report, projectRoot string, lg *log.Logger) exitcode.Code {
	sources := make([]RowSource, 0, len(report.Children))
	for _, child := range report.Children {
		sources = append(sources, RowSource{
			Label:         "#" + strconv.Itoa(child.Num),
			Backend:       child.Backend,
			PaneID:        child.PaneID,
			ReportedState: child.ReportedState,
			State:         child.State,
			PRs:           child.PRs,
		})
	}
	rows, maxLines, addWidth, delWidth, code := BuildTableRows(ghissue.Runner{Cwd: projectRoot}, projectRoot, sources, lg)
	if code != exitcode.OK {
		return code
	}
	spec := TableSpec{
		Heading: fmt.Sprintf("fanout status #%d: total=%d merged=%d pending=%d blocked=%d all_merged=%t",
			report.Parent, report.Summary.Total, report.Summary.Merged, report.Summary.Pending, report.Summary.Blocked, report.Summary.AllMerged),
		EmptyText:      "(no recorded children)",
		FirstColHeader: "ISSUE",
	}
	WriteTable(lg.Stdout(), lg.Colors(), spec, rows, maxLines, addWidth, delWidth)
	return exitcode.OK
}

func conventionalType(title string) string {
	m := conventionalTypeRE.FindStringSubmatch(title)
	if len(m) < 2 {
		return "-"
	}
	return m[1]
}

func renderStatusDiff(row TableRow, maxLines, addWidth, delWidth, diffWidth int, colors log.Palette) string {
	if !row.HasDiff {
		return cliview.PadRight("-", diffWidth)
	}
	addNum := fmt.Sprintf("+%d", row.Additions)
	delNum := fmt.Sprintf("-%d", row.Deletions)
	addBar := strings.Repeat("+", scaledStatusBar(row.Additions, maxLines))
	delBar := strings.Repeat("-", scaledStatusBar(row.Deletions, maxLines))
	return cliview.ColorPad(colors.Ok, colors.Reset, addNum, addWidth) + " " +
		cliview.ColorPad(colors.Ok, colors.Reset, addBar, diffBarWidth) + "  " +
		cliview.ColorPad(colors.Err, colors.Reset, delNum, delWidth) + " " +
		cliview.ColorPad(colors.Err, colors.Reset, delBar, diffBarWidth)
}

func statusDiffWidth(addWidth, delWidth int) int {
	return addWidth + 1 + diffBarWidth + 2 + delWidth + 1 + diffBarWidth
}

func scaledStatusBar(value, maxValue int) int {
	if value <= 0 || maxValue <= 0 {
		return 0
	}
	n := (value*diffBarWidth + maxValue - 1) / maxValue
	if n < 1 {
		return 1
	}
	if n > diffBarWidth {
		return diffBarWidth
	}
	return n
}
