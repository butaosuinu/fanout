package statusreport

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/cliview"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/log"
)

var (
	dashboardReviewEffortRE = regexp.MustCompile(`(?im)^\s*(?:[-*]\s*)?(?:\*\*)?Review effort(?:\*\*)?\s*:\s*([0-5])\b.*$`)
	dashboardTLDRLineRE     = regexp.MustCompile(`(?i)^\s*(?:#{1,6}\s*)?(?:[-*]\s*)?(?:[0-9]+\.\s*)?(?:\*\*)?TL;DR(?:\*\*)?\s*(?::|\x{2014}|-)?\s*(.*)$`)
)

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

// PostDashboard posts or updates the marker-tagged rollup comment on the
// parent issue from the --status report.
func PostDashboard(report Report, projectRoot string, lg *log.Logger) exitcode.Code {
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

func dashboardRows(report Report, projectRoot, nwo string, lg *log.Logger) ([]dashboardRow, exitcode.Code) {
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
				PRState: cliview.DashIfEmpty(pr.DisplayState()),
				CI:      cliview.DashIfEmpty(pr.CIStatus),
				Diff:    dashboardDiff(stat),
				Type:    conventionalType(stat.Title),
				TLDR:    tldr,
				Score:   score,
			})
		}
	}
	return rows, exitcode.OK
}

func buildDashboardBody(parent int, summary Summary, rows []dashboardRow) string {
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
