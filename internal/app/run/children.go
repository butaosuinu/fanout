package run

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/core/naming"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/log"
)

// ChildLoadResult is the child enumeration both the issue lane and the TUI
// watch lane consume.
type ChildLoadResult struct {
	Children       []ghissue.Issue
	ParentBody     string
	StrongChildren map[int]bool
	ChildNoun      string
}

func loadChildren(cfg *cliflags.Config, gh ghissue.Runner, lg *log.Logger) (ChildLoadResult, exitcode.Code) {
	if cfg.ParentMode == cliflags.ModeProject {
		return loadProjectChildren(cfg, gh, lg)
	}
	return loadIssueChildren(cfg, gh, lg)
}

func loadIssueChildren(cfg *cliflags.Config, gh ghissue.Runner, lg *log.Logger) (ChildLoadResult, exitcode.Code) {
	lg.Info("fetching sub-issues of #%d", cfg.Parent)
	subIssues, err := gh.SubIssueList(cfg.Parent)
	if err != nil {
		lg.Err("sub-issues fetch failed: %v", err)
		return ChildLoadResult{}, exitcode.Env
	}

	parentBody, err := gh.ParentBody(cfg.Parent)
	if err != nil {
		lg.Warn("could not read parent body (#%d); skipping task-list scan", cfg.Parent)
		parentBody = ""
	}

	bodyNums := ghissue.TaskListNumbers(parentBody)
	loaded, added := mergeExtraChildren(cfg, gh, subIssues, bodyNums, true, lg)
	AssignTaskListWaves(loaded, ghissue.TaskListWaves(parentBody))
	if added > 0 {
		lg.Info("parent body / --include added %d extra child reference(s) not in sub-issue API", added)
	}

	return ChildLoadResult{
		Children:       loaded,
		ParentBody:     parentBody,
		StrongChildren: IssueSet(subIssues),
		ChildNoun:      "sub-issues",
	}, exitcode.OK
}

func loadProjectChildren(cfg *cliflags.Config, gh ghissue.Runner, lg *log.Logger) (ChildLoadResult, exitcode.Code) {
	repo, err := gh.RepoNameWithOwner()
	if err != nil {
		lg.Err("could not resolve repo via 'gh repo view' in project root (required for project mode cross-repo filter)")
		return ChildLoadResult{}, exitcode.Env
	}
	lg.Info("mode: project (status=%s, repo=%s)", cfg.ProjectStatus, repo)
	lg.Info("fetching items from Project %s", cfg.ParentRef)

	res, err := gh.ProjectItems(cfg.ProjectOwnerType, cfg.ProjectOwner, cfg.ProjectNumber, repo, cfg.ProjectStatus)
	if err != nil {
		lg.Err("%v", err)
		return ChildLoadResult{}, exitcode.Env
	}
	if res.MissingStatus && cfg.ProjectStatus != "all" {
		lg.Warn("Project '%s' has no Status field; ignoring --project-status %s and falling back to all items", res.ProjectTitle, cfg.ProjectStatus)
		cfg.ProjectStatus = "all"
	}
	for _, cross := range res.CrossRepoWarnings {
		lg.Warn("skipping cross-repo project item: %s (project root repo: %s)", cross, repo)
	}

	loaded, added := mergeExtraChildren(cfg, gh, res.Issues, nil, false, lg)
	if added > 0 {
		lg.Info("--include added %d extra child reference(s) not in project items", added)
	}

	return ChildLoadResult{
		Children:       loaded,
		StrongChildren: IssueSet(res.Issues),
		ChildNoun:      "project items",
	}, exitcode.OK
}

func mergeExtraChildren(cfg *cliflags.Config, gh ghissue.Runner, base []ghissue.Issue, bodyNums []int, skipParent bool, lg *log.Logger) ([]ghissue.Issue, int) {
	existing := map[int]bool{}
	if skipParent {
		existing[cfg.Parent] = true
	}
	for _, s := range base {
		existing[s.Number] = true
	}

	extraNums := append([]int{}, bodyNums...)
	extraNums = append(extraNums, cfg.Include...)
	var extra []ghissue.Issue
	for _, num := range extraNums {
		if existing[num] {
			continue
		}
		detail, err := gh.IssueDetail(num)
		if err != nil {
			lg.Warn("parent body / --include references #%d but issue lookup failed; skipping", num)
			continue
		}
		extra = append(extra, detail)
		existing[num] = true
	}
	return ghissue.MergeExtra(base, extra), len(extra)
}

// AssignTaskListWaves stamps parent task-list wave labels onto the matching
// child issues in place.
func AssignTaskListWaves(issues []ghissue.Issue, waves map[int]string) {
	for i := range issues {
		if wave := waves[issues[i].Number]; wave != "" {
			issues[i].Wave = wave
		}
	}
}

// IssueSet builds a membership set of the given issue numbers.
func IssueSet(issues []ghissue.Issue) map[int]bool {
	out := map[int]bool{}
	for _, issue := range issues {
		out[issue.Number] = true
	}
	return out
}

// ExistingWorktreeFanned reports which issues already have a worktree directory
// on disk, used as the action-mode migration fallback for the idempotency
// check. It is shared by the issue lane and the TUI watch lane.
func ExistingWorktreeFanned(cfg *cliflags.Config, projectRoot string, issues []ghissue.Issue, sharedAcrossParents map[int]bool) map[int]bool {
	out := map[int]bool{}
	worktreeNames := existingWorktreeNames(filepath.Join(projectRoot, ".fanout", "worktrees"))
	for _, issue := range issues {
		slug := naming.Slug(issue.Title, issue.Number)
		slugOverridden := false
		if name := cfg.FindName(issue.Number); name != nil && name.SlugHint != "" {
			slug = naming.EnsureIssueSuffix(name.SlugHint, issue.Number)
			slugOverridden = true
		}
		if sharedAcrossParents[issue.Number] {
			if !slugOverridden {
				slug = naming.QualifySlugForParent(slug, cfg.ParentRef, issue.Number)
			}
			if worktreeNameMatchesExact(worktreeNames, slug) {
				out[issue.Number] = true
			}
			continue
		}
		if worktreeNameMatchesIssue(worktreeNames, slug, issue.Number) {
			out[issue.Number] = true
		}
	}
	return out
}

func existingWorktreeNames(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names
}

func worktreeNameMatchesExact(names []string, slug string) bool {
	return slices.Contains(names, slug)
}

func worktreeNameMatchesIssue(names []string, exactSlug string, issueNum int) bool {
	issueSuffix := fmt.Sprintf("-%d", issueNum)
	for _, name := range names {
		if name == exactSlug || strings.HasSuffix(name, issueSuffix) {
			return true
		}
	}
	return false
}
