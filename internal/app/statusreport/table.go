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
	Label   string
	Backend backend.Name
	PaneID  string
	State   string
	PRs     []ghissue.PRRef
}

// TableRow is one rendered status table row; PR-less sources collapse to
// a single dash row.
type TableRow struct {
	Label     string
	Backend   string
	PaneID    string
	State     string
	PR        string
	PRState   string
	CI        string
	Type      string
	Files     string
	Link      string
	Additions int
	Deletions int
	HasDiff   bool
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
			rows = append(rows, TableRow{
				Label:   src.Label,
				Backend: cliview.DashIfEmpty(string(src.Backend)),
				PaneID:  cliview.DashIfEmpty(src.PaneID),
				State:   cliview.DashIfEmpty(src.State),
				PR:      "-",
				PRState: "-",
				CI:      "-",
				Type:    "-",
				Files:   "-",
				Link:    "-",
			})
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
			rows = append(rows, TableRow{
				Label:     src.Label,
				Backend:   cliview.DashIfEmpty(string(src.Backend)),
				PaneID:    cliview.DashIfEmpty(src.PaneID),
				State:     cliview.DashIfEmpty(src.State),
				PR:        "#" + strconv.Itoa(pr.Number),
				PRState:   cliview.DashIfEmpty(pr.DisplayState()),
				CI:        cliview.DashIfEmpty(pr.CIStatus),
				Type:      conventionalType(stat.Title),
				Files:     strconv.Itoa(stat.ChangedFiles),
				Link:      fmt.Sprintf("https://github.com/%s/pull/%d", nwo, pr.Number),
				Additions: stat.Additions,
				Deletions: stat.Deletions,
				HasDiff:   true,
			})
		}
	}
	return rows, maxLines, addWidth, delWidth, exitcode.OK
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
	widths := []int{
		len(spec.FirstColHeader),
		len("BACKEND"),
		len("PANE"),
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
		widths[0] = max(widths[0], len(row.Label))
		widths[1] = max(widths[1], len(row.Backend))
		widths[2] = max(widths[2], len(row.PaneID))
		widths[3] = max(widths[3], len(row.State))
		widths[4] = max(widths[4], len(row.PR))
		widths[5] = max(widths[5], len(row.PRState))
		widths[6] = max(widths[6], len(row.CI))
		widths[7] = max(widths[7], len(row.Type))
		widths[8] = max(widths[8], len(row.Files))
	}

	headers := []string{spec.FirstColHeader, "BACKEND", "PANE", "STATE", "PR", "PR_STATE", "CI", "TYPE", "FILES", "DIFF", "LINK"}
	fmt.Fprintln(out, cliview.TableLine(headers, widths))
	separators := []string{
		strings.Repeat("-", widths[0]),
		strings.Repeat("-", widths[1]),
		strings.Repeat("-", widths[2]),
		strings.Repeat("-", widths[3]),
		strings.Repeat("-", widths[4]),
		strings.Repeat("-", widths[5]),
		strings.Repeat("-", widths[6]),
		strings.Repeat("-", widths[7]),
		strings.Repeat("-", widths[8]),
		strings.Repeat("-", diffWidth),
		strings.Repeat("-", widths[10]),
	}
	fmt.Fprintln(out, cliview.TableLine(separators, widths))

	for _, row := range rows {
		cols := []string{
			row.Label,
			row.Backend,
			row.PaneID,
			row.State,
			row.PR,
			row.PRState,
			row.CI,
			row.Type,
			row.Files,
			renderStatusDiff(row, maxLines, addWidth, delWidth, diffWidth, colors),
			row.Link,
		}
		fmt.Fprintln(out, cliview.TableLine(cols, widths))
	}
}

// WriteIssueTable renders the issue-mode report as the status table.
func WriteIssueTable(report Report, projectRoot string, lg *log.Logger) exitcode.Code {
	sources := make([]RowSource, 0, len(report.Children))
	for _, child := range report.Children {
		sources = append(sources, RowSource{
			Label:   "#" + strconv.Itoa(child.Num),
			Backend: child.Backend,
			PaneID:  child.PaneID,
			State:   child.State,
			PRs:     child.PRs,
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
