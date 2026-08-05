package panelaunch

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/app/briefing"
	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/core/naming"
	"github.com/butaosuinu/fanout/internal/core/planspec"
	"github.com/butaosuinu/fanout/internal/infra/codexapp"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/settings"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

// MaxInlineManualPromptBytes keeps a raw manual prompt far below Linux's
// per-argument exec limit after plan-template expansion and two shell-quoting
// layers. Larger prompts travel through a briefing file.
const MaxInlineManualPromptBytes = 4 * 1024

// fanoutTagPrefix opens the one-line child prompt tag "[fanout #N of #P]".
// Its format pairs with team.FanoutTagRE / team.ParseFanoutTag
// (internal/infra/team/detect.go); keep the two in sync.
const fanoutTagPrefix = "[fanout #"

// NewIssueRequest builds the pane request for one GitHub child issue.
func NewIssueRequest(cfg *cliflags.Config, projectRoot string, issue ghissue.Issue, resolvedSettings settings.Settings, hookConfig hooks.Config, sharedAcrossParents bool, teamCtx *briefing.TeamContext) Request {
	slug := naming.Slug(issue.Title, issue.Number)
	slugOverridden := false
	branchOverride := ""
	agentName := cfg.EffectiveAgentForIssue(issue.Number)
	req := Request{
		ParentRef:    cfg.ParentRef,
		Number:       issue.Number,
		Title:        issue.Title,
		Body:         issue.Body,
		Wave:         issue.Wave,
		BriefingPath: briefing.Path(projectRoot, issue.Number),
		ShortTitle:   ShortIssueTitle(issue.Title),
		Slug:         slug,
		Agent:        agentName,
		LaunchMode:   issueLaunchMode(cfg),
		Hooks:        hookConfig,
	}
	if name := cfg.FindName(issue.Number); name != nil {
		if name.SlugHint != "" {
			req.Slug = naming.EnsureIssueSuffix(name.SlugHint, issue.Number)
			slugOverridden = true
		}
		req.DisplayNameOverride = name.DisplayName
		branchOverride = name.BranchName
	}
	if sharedAcrossParents && !slugOverridden {
		req.Slug = naming.QualifySlugForParent(req.Slug, cfg.ParentRef, issue.Number)
	}
	req.BranchName = naming.BranchName(branchOverride, cfg.BranchPrefix, req.Slug)
	req.Worktree = worktree.BuildPlan(worktree.Options{
		ProjectRoot: projectRoot,
		Slug:        req.Slug,
		BranchName:  req.BranchName,
		BaseBranch:  cfg.BaseBranch,
		NoRefresh:   cfg.NoRefresh,
	})
	req.BriefingBody = briefing.Render(issue.Number, issue.Title, issue.Body, agentName, req.Worktree.BaseBranch, resolvedSettings, req.PlanMode(), teamCtx)
	req.Prompt = oneLinePrompt(req.ParentRef, req)
	if req.CodexPlanMode() {
		req.CodexPlanStatusPath = codexapp.StatusPath(projectRoot, issue.Number, cfg.DryRun)
	}
	req.CodexTeamRequested = teamCtx != nil && agentName == "codex"
	if req.CodexTeamRequested && !req.PlanMode() {
		req.CodexTeamMode = true
		req.CodexTeamStatusPath = codexapp.TeamStatusPath(projectRoot, strconv.Itoa(issue.Number), cfg.DryRun)
	}
	return req
}

// NewWatchRequest builds the pane request for a watcher-launched standalone
// issue under the reserved @watch parent. Standalone TUI issue sessions share
// this path and follow the same child Plan Mode setting.
func NewWatchRequest(cfg *cliflags.Config, projectRoot string, issue ghissue.Issue, resolvedSettings settings.Settings, hookConfig hooks.Config) Request {
	watchCfg := *cfg
	watchCfg.ParentRef = WatchParentRef
	watchCfg.PlanMode = new(resolvedSettings.ChildPlanMode)
	return NewIssueRequest(&watchCfg, projectRoot, issue, resolvedSettings, hookConfig, false, nil)
}

// NewTaskRequest builds the pane request for one issue-less plan task.
func NewTaskRequest(cfg *cliflags.Config, projectRoot string, spec planspec.Spec, task planspec.Task, resolvedSettings settings.Settings, hookConfig hooks.Config, teamCtx *briefing.TeamContext) Request {
	slug := PlanTaskSlug(spec.Plan.Slug, task)
	branchName := task.Branch
	if branchName == "" {
		branchName = naming.BranchName("", cfg.BranchPrefix, slug)
	}
	agentName := cfg.EffectiveAgent(task.ID)
	req := Request{
		ParentRef:           PlanParentRef(spec.Plan.Slug),
		Number:              0,
		TaskID:              task.ID,
		Title:               task.Title,
		Body:                task.Briefing,
		Wave:                task.Wave,
		BriefingPath:        briefing.TaskPath(projectRoot, spec.Plan.Slug, task.ID),
		ShortTitle:          ShortIssueTitle(task.Title),
		Slug:                slug,
		DisplayNameOverride: task.DisplayName,
		BranchName:          branchName,
		Agent:               agentName,
		LaunchMode:          childLaunchMode(resolvedSettings.ChildPlanMode),
		Hooks:               hookConfig,
		Worktree: worktree.BuildPlan(worktree.Options{
			ProjectRoot:        projectRoot,
			Slug:               slug,
			BranchName:         branchName,
			BaseBranch:         cfg.BaseBranch,
			NoRefresh:          cfg.NoRefresh,
			AllowMissingOrigin: true,
		}),
	}
	req.BriefingBody = briefing.RenderTask(spec.Plan.Slug, spec.Plan.Title, task.ID, task.Title, task.Briefing, agentName, req.Worktree.BaseBranch, resolvedSettings, req.PlanMode(), teamCtx)
	req.Prompt = taskOneLinePrompt(spec.Plan.Slug, req)
	if req.CodexPlanMode() {
		req.CodexPlanStatusPath = codexapp.TaskStatusPath(projectRoot, spec.Plan.Slug, task.ID, cfg.DryRun)
	}
	req.CodexTeamRequested = teamCtx != nil && agentName == "codex"
	if req.CodexTeamRequested && !req.PlanMode() {
		req.CodexTeamMode = true
		req.CodexTeamStatusPath = codexapp.TeamStatusPath(projectRoot, task.ID, cfg.DryRun)
	}
	return req
}

// NewManualRequest builds the pane request for a TUI-launched manual pane
// under the reserved @manual parent with a synthetic negative number.
func NewManualRequest(cfg *cliflags.Config, projectRoot string, store state.Store, hookConfig hooks.Config, opts ManualOptions) Request {
	number := NextSyntheticPaneNumber(store, ManualParentRef)
	title := opts.Title
	if title == "" {
		title = "Manual agent"
	}
	slug := ManualPaneSlug(title, number)
	branchName := naming.BranchName("", cfg.BranchPrefix, slug)
	// A state-only close (close the pane, or remove the worktree but keep the
	// branch) can leave an orphaned worktree dir or branch behind.
	// NextSyntheticPaneNumber only sees state, so re-creating the same-titled
	// manual pane would otherwise reuse that slug and either fail preparing the
	// duplicate worktree or silently inherit the old branch. Skip to a lower
	// (still state-unique) number until the derived worktree dir and branch are
	// both free.
	for worktree.SlugInUse(projectRoot, slug, branchName) {
		number--
		slug = ManualPaneSlug(title, number)
		branchName = naming.BranchName("", cfg.BranchPrefix, slug)
	}
	agentName := opts.Agent
	if agentName == "" {
		agentName = cfg.Agent
	}
	prompt := opts.Prompt
	if prompt == "" {
		prompt = title
	}
	briefingPath := ""
	briefingBody := ""
	launchMode := launchModeFromPlanFlag(cfg)
	if launchMode == agent.ModePlan && agentName == "codex" {
		body := opts.Body
		if strings.TrimSpace(body) == "" {
			body = prompt
		}
		planPrompt := briefing.RenderManualPlan(title, body)
		if len(body) > MaxInlineManualPromptBytes {
			briefingPath = briefing.Path(projectRoot, number)
			briefingBody = planPrompt
			prompt = manualPromptWithBriefingAction(ShortIssueTitle(title), briefingPath, "investigate, then propose a plan")
		} else {
			prompt = planPrompt
		}
	} else if opts.Body != "" {
		briefingPath = briefing.Path(projectRoot, number)
		briefingBody = opts.Body
		prompt = manualPromptWithBriefing(prompt, briefingPath)
	}
	req := Request{
		ParentRef:    ManualParentRef,
		Number:       number,
		Title:        title,
		Body:         opts.Body,
		ShortTitle:   ShortIssueTitle(title),
		Slug:         slug,
		BranchName:   branchName,
		Prompt:       prompt,
		Agent:        agentName,
		Hooks:        hookConfig,
		BriefingPath: briefingPath,
		BriefingBody: briefingBody,
		LaunchMode:   launchMode,
		Worktree:     worktree.BuildPlan(worktree.Options{ProjectRoot: projectRoot, Slug: slug, BranchName: branchName, BaseBranch: cfg.BaseBranch, NoRefresh: cfg.NoRefresh, AllowMissingOrigin: true, RefreshBestEffort: true}),
	}
	if req.CodexPlanMode() {
		req.CodexPlanStatusPath = codexapp.StatusPath(projectRoot, number, cfg.DryRun)
	}
	return req
}

// AttachTarget carries the source identity of the recorded pane an
// attached-agent request derives from. It mirrors the source fields of the
// TUI's AttachTarget (internal/ui/tui) without depending on the ui layer.
type AttachTarget struct {
	SourceParent     string
	SourceIssueNum   int
	SourceTaskID     string
	SourceLabel      string
	SourceBranchName string
}

// NewAttachedRequest builds the pane request for an agent attached to an
// existing worktree directory: a synthetic sibling of target's source pane,
// with no worktree of its own.
func NewAttachedRequest(cfg *cliflags.Config, projectRoot string, store state.Store, hookConfig hooks.Config, prompt, targetPath string, target AttachTarget) Request {
	parentRef := strings.TrimSpace(target.SourceParent)
	if parentRef == "" {
		parentRef = ManualParentRef
	}
	number := NextSyntheticPaneNumber(store, parentRef)
	agentName := cfg.Agent
	title := attachedPaneTitle(agentName, target.SourceLabel, targetPath)
	slug := attachedPaneSlug(targetPath, agentName, number)
	body := prompt
	shortPrompt := FirstPromptLine(prompt)
	if shortPrompt == "" {
		shortPrompt = title
	}
	oversized := len(body) > MaxInlineManualPromptBytes
	if oversized {
		shortPrompt = ShortIssueTitle(shortPrompt)
	}
	briefingPath := ""
	briefingBody := ""
	launchMode := launchModeFromPlanFlag(cfg)
	switch {
	case launchMode == agent.ModePlan && agentName == "codex":
		planPrompt := briefing.RenderManualPlan(title, body)
		if oversized {
			briefingPath = attachedBriefingPath(projectRoot, parentRef, target, number)
			briefingBody = planPrompt
			prompt = manualPromptWithBriefingAction(shortPrompt, briefingPath, "investigate, then propose a plan")
		} else {
			prompt = planPrompt
		}
	case strings.Contains(prompt, "\n") || oversized:
		briefingPath = attachedBriefingPath(projectRoot, parentRef, target, number)
		briefingBody = body
		prompt = manualPromptWithBriefing(shortPrompt, briefingPath)
	default:
		prompt = shortPrompt
	}

	req := Request{
		ParentRef:           parentRef,
		Number:              number,
		Title:               title,
		Body:                body,
		ShortTitle:          ShortIssueTitle(title),
		Slug:                slug,
		DisplayNameOverride: title,
		BranchName:          strings.TrimSpace(target.SourceBranchName),
		Prompt:              prompt,
		SourceParent:        parentRef,
		SourceIssueNum:      target.SourceIssueNum,
		SourceTaskID:        strings.TrimSpace(target.SourceTaskID),
		Agent:               agentName,
		Hooks:               hookConfig,
		BriefingPath:        briefingPath,
		BriefingBody:        briefingBody,
		LaunchMode:          launchMode,
	}
	if req.CodexPlanMode() {
		req.CodexPlanStatusPath = codexapp.StatusPath(projectRoot, number, cfg.DryRun)
	}
	return req
}

func attachedBriefingPath(projectRoot, parentRef string, target AttachTarget, number int) string {
	parentSlug := naming.Slugify(parentRef)
	if parentSlug == "" {
		parentSlug = "manual"
	}
	source := strings.TrimSpace(target.SourceTaskID)
	if source == "" && target.SourceIssueNum > 0 {
		source = fmt.Sprintf("issue-%d", target.SourceIssueNum)
	}
	if source == "" {
		source = strings.TrimSpace(target.SourceLabel)
	}
	sourceSlug := naming.Slugify(source)
	if sourceSlug == "" {
		sourceSlug = "source"
	}
	if number < 0 {
		number = -number
	}
	return filepath.Join(briefing.Dir(projectRoot), fmt.Sprintf("fanout-%s-attach-%s-%s-a%d.md", filepath.Base(projectRoot), parentSlug, sourceSlug, number))
}

func attachedPaneTitle(agentName, sourceLabel, targetPath string) string {
	sourceLabel = strings.TrimSpace(sourceLabel)
	if sourceLabel == "" {
		sourceLabel = filepath.Base(targetPath)
	}
	if sourceLabel == "" || sourceLabel == "." || sourceLabel == string(filepath.Separator) {
		sourceLabel = "worktree"
	}
	return agentName + " for " + sourceLabel
}

func attachedPaneSlug(targetPath, agentName string, number int) string {
	base := naming.Slugify(filepath.Base(targetPath))
	if base == "" {
		base = "worktree"
	}
	suffixNum := number
	if suffixNum < 0 {
		suffixNum = -suffixNum
	}
	suffix := fmt.Sprintf("-%s-a%d", naming.Slugify(agentName), suffixNum)
	maxBase := max(naming.MaxSlugLength-len(suffix), 1)
	if len(base) > maxBase {
		base = strings.Trim(base[:maxBase], "-")
		if base == "" {
			base = "worktree"
		}
	}
	return base + suffix
}

// manualPromptWithBriefing appends the "read <briefing> ... and begin."
// sentence to a manual prompt unless it already references the briefing path.
func manualPromptWithBriefing(prompt, briefingPath string) string {
	return manualPromptWithBriefingAction(prompt, briefingPath, "begin")
}

func manualPromptWithBriefingAction(prompt, briefingPath, action string) string {
	prompt = strings.TrimSpace(prompt)
	if strings.Contains(prompt, briefingPath) {
		return prompt
	}
	prompt = strings.TrimRight(prompt, ".")
	if prompt == "" {
		return fmt.Sprintf("read %s and %s.", briefingPath, action)
	}
	// A prompt ending in an "@path" file mention needs a whitespace terminator;
	// gluing ". read ..." straight on yields "@path." — a non-existent path the
	// agent cannot expand. Keep a separating space before the sentence in that
	// case (the new-session @-completion makes mention-terminated prompts common).
	sep := "."
	if endsWithFileMention(prompt) {
		sep = " ."
	}
	return fmt.Sprintf("%s%s read %s for additional context and %s.", prompt, sep, briefingPath, action)
}

// endsWithFileMention reports whether prompt's final whitespace-delimited token
// is an "@path" file mention (more than just the bare "@").
func endsWithFileMention(prompt string) bool {
	fields := strings.Fields(prompt)
	if len(fields) == 0 {
		return false
	}
	last := fields[len(fields)-1]
	return len(last) > 1 && last[0] == '@'
}

// NextSyntheticPaneNumber returns the next negative synthetic pane number
// unused by parentRef's recorded panes.
func NextSyntheticPaneNumber(store state.Store, parentRef string) int {
	next := -1
	for _, pane := range store.PanesForParent(parentRef) {
		if pane.IssueNum <= next {
			next = pane.IssueNum - 1
		}
	}
	return next
}

// ManualPaneSlug derives the bounded "manual-<n>-<title>-pane" slug for a
// manual pane.
func ManualPaneSlug(title string, number int) string {
	base := naming.Slugify(title)
	if base == "" {
		base = "agent"
	}
	if number < 0 {
		number = -number
	}
	prefix := fmt.Sprintf("manual-%d-", number)
	suffix := "-pane"
	maxBase := max(naming.MaxSlugLength-len(prefix)-len(suffix), 1)
	if len(base) > maxBase {
		base = strings.Trim(base[:maxBase], "-")
		if base == "" {
			base = "agent"
		}
	}
	return prefix + base + suffix
}

// ShortIssueTitle truncates a title to 60 runes on a rune boundary.
func ShortIssueTitle(title string) string {
	const maxRunes = 60
	count := 0
	for i := range title {
		if count == maxRunes {
			return title[:i]
		}
		count++
	}
	return title
}

// FirstPromptLine returns the first non-empty line of prompt, trimmed.
func FirstPromptLine(prompt string) string {
	for line := range strings.Lines(prompt) {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return strings.TrimSpace(prompt)
}

func oneLinePrompt(parentRef string, req Request) string {
	action := "begin"
	if req.PlanMode() {
		action = "investigate, then propose a plan"
	}
	return fmt.Sprintf("%s%d of #%s] %s: %s. read %s and %s.", fanoutTagPrefix, req.Number, parentRef, req.Slug, req.ShortTitle, req.BriefingPath, action)
}

// issueLaunchMode makes both postures explicit for issue children.
func issueLaunchMode(cfg *cliflags.Config) agent.LaunchMode {
	return childLaunchMode(cfg.PlanModeEnabled())
}

func childLaunchMode(planMode bool) agent.LaunchMode {
	if planMode {
		return agent.ModePlan
	}
	return agent.ModeBuild
}

// launchModeFromPlanFlag makes both new-session postures explicit for manual
// and attached panes.
func launchModeFromPlanFlag(cfg *cliflags.Config) agent.LaunchMode {
	if cfg.PlanModeEnabled() {
		return agent.ModePlan
	}
	return agent.ModeBuild
}

func taskOneLinePrompt(planSlug string, req Request) string {
	action := "begin"
	if req.PlanMode() {
		action = "investigate, then propose a plan"
	}
	return fmt.Sprintf("[fanout %s of plan:%s] %s: %s. read %s and %s.", req.TaskID, planSlug, req.Slug, oneLineText(req.ShortTitle), req.BriefingPath, action)
}

func oneLineText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// paneTitle is the pane's display name: the explicit override, else the slug.
func paneTitle(req Request) string {
	if req.DisplayNameOverride != "" {
		return req.DisplayNameOverride
	}
	return req.Slug
}

// paneBorderLabel is the text fanout shows on a pane's top border:
// "<parent> · <pane name>" (e.g. "#123 · fix-login-bug-123").
func paneBorderLabel(req Request) string {
	return BorderLabel(req.ParentRef, paneTitle(req))
}

// BorderLabel composes the "<parent> · <name>" border label shared by agent
// panes (paneBorderLabel) and TUI shell panes.
func BorderLabel(parent, name string) string {
	return parentDisplay(parent) + " · " + name
}

// parentDisplay renders a parent ref for display, mirroring the dashboard's
// parentLabel (web/src/shared/github.ts): numeric issue parents get a "#" prefix,
// GitHub Projects URLs drop the host prefix, and plan:<slug> / @manual pass
// through unchanged.
func parentDisplay(parent string) string {
	if naming.AllDigits(parent) {
		return "#" + parent
	}
	return strings.TrimPrefix(parent, "https://github.com/")
}

// PlanParentRef is the state parent key of an issue-less plan: "plan:<slug>".
func PlanParentRef(slug string) string {
	return "plan:" + slug
}

// PlanRuntimeParentRef returns the actual parent used for runtime backend
// stickiness. An issue-sourced plan shares its source issue's backend binding;
// every other plan remains scoped to plan:<slug>. The source must declare the
// issue explicitly — the slug is never treated as provenance.
func PlanRuntimeParentRef(slug, source string) string {
	if issueNum := planSourceIssue(source); issueNum > 0 {
		return strconv.Itoa(issueNum)
	}
	return PlanParentRef(slug)
}

// SavedPlanRuntimeParentRef resolves a recorded plan task row through the
// saved spec that created it. Missing, unreadable, or non-issue specs stay on
// plan:<slug>; callers must not infer issue ownership from an issue-like slug.
func SavedPlanRuntimeParentRef(projectRoot, planSlug string) string {
	if strings.TrimSpace(projectRoot) == "" {
		return PlanParentRef(planSlug)
	}
	if issueNum := savedPlanSourceIssue(projectRoot, planSlug); issueNum > 0 {
		return strconv.Itoa(issueNum)
	}
	return PlanParentRef(planSlug)
}

// PlanIssueSlug keys an issue-sourced plan coordinator's state row by issue
// number for the dedupe guards and by the synthetic pane number for uniqueness
// across relaunches: "plan-issue-<issue>-<n>".
func PlanIssueSlug(issueNum, number int) string {
	if number < 0 {
		number = -number
	}
	return fmt.Sprintf("plan-issue-%d-%d", issueNum, number)
}

// PlanPaneIssueNum returns the GitHub issue an issue-sourced plan coordinator
// row is linked to, parsed from its own PlanIssueSlug under the manual parent.
// Only this lane's launch code creates such rows, so the slug is explicit
// provenance. ok is false for every other row, including the prompt
// coordinator's "plan-prompt-<n>" and all plan task rows (see
// PlanLinkedIssueNums for their spec-verified link).
func PlanPaneIssueNum(pane state.Pane) (int, bool) {
	if pane.Parent != ManualParentRef {
		return 0, false
	}
	rest, found := strings.CutPrefix(pane.Slug, "plan-issue-")
	if !found {
		return 0, false
	}
	return parseLeadingIssueNum(rest)
}

// OrchestratorIssueSlug keys a TUI issue orchestrator lane's attached-agent
// row by issue number and the synthetic pane number for uniqueness across
// relaunches: "orchestrator-issue-<issue>-<n>". Only that lane creates these
// rows, so the slug is explicit provenance. Its "orchestrator-issue-" prefix
// is disjoint from "plan-issue-", and neither parser accepts the other lane.
func OrchestratorIssueSlug(issueNum, number int) string {
	if number < 0 {
		number = -number
	}
	return fmt.Sprintf("orchestrator-issue-%d-%d", issueNum, number)
}

// OrchestratorPaneIssueNum returns the GitHub issue linked to a TUI issue
// orchestrator row, parsed from its provenance slug under the manual parent.
// Only the complete "orchestrator-issue-<issue>-<numeric pane>" form matches.
func OrchestratorPaneIssueNum(pane state.Pane) (int, bool) {
	if pane.Parent != ManualParentRef {
		return 0, false
	}
	rest, found := strings.CutPrefix(pane.Slug, "orchestrator-issue-")
	if !found {
		return 0, false
	}
	issueNum, ok := parseLeadingIssueNum(rest)
	if !ok {
		return 0, false
	}
	_, paneNum, found := strings.Cut(rest, "-")
	if !found || !naming.AllDigits(paneNum) {
		return 0, false
	}
	return issueNum, true
}

// PaneIssueParentNum returns the actual issue parent carried by a row whose
// storage parent is synthetic. It accepts only watcher identity or the two
// coordinator slug formats created by fanout; unrelated @manual rows never
// acquire issue provenance by inference.
func PaneIssueParentNum(pane state.Pane) (int, bool) {
	if pane.Parent == WatchParentRef && pane.IssueNum > 0 {
		return pane.IssueNum, true
	}
	if issueNum, ok := PlanPaneIssueNum(pane); ok {
		return issueNum, true
	}
	return OrchestratorPaneIssueNum(pane)
}

// PlanLinkedIssueNums collects the issues owned by plan-lane rows so the issue
// fan-out lanes can treat them as already fanned: coordinator rows through
// PlanPaneIssueNum, and plan task rows through the saved spec's declared
// provenance (plan.source "issue #N" in .fanout/plans/<slug>.json, written by
// the coordinator per its briefing). A slug that merely looks issue-like never
// links — only a spec that declares its source issue does — so a hand-authored
// plan named "issue-123-migration" cannot block issue #123's normal lanes.
func PlanLinkedIssueNums(projectRoot string, store state.Store) map[int]bool {
	out := map[int]bool{}
	specSource := map[string]int{}
	for _, pane := range store.Panes {
		if num, ok := PlanPaneIssueNum(pane); ok {
			out[num] = true
			continue
		}
		planSlug, found := strings.CutPrefix(pane.Parent, "plan:")
		if !found || planSlug == "" {
			continue
		}
		num, seen := specSource[planSlug]
		if !seen {
			num = savedPlanSourceIssue(projectRoot, planSlug)
			specSource[planSlug] = num
		}
		if num > 0 {
			out[num] = true
		}
	}
	return out
}

// savedPlanSourceIssue returns the issue number a saved plan spec declares as
// its source ("issue #N"), or 0 when the spec is absent, unreadable, or names
// no issue. Read failures degrade to no link — the safe direction, because a
// false link would block the issue's normal lanes.
func savedPlanSourceIssue(projectRoot, planSlug string) int {
	data, err := os.ReadFile(filepath.Join(projectRoot, ".fanout", "plans", planSlug+".json"))
	if err != nil {
		return 0
	}
	var spec struct {
		Plan struct {
			Source string `json:"source"`
		} `json:"plan"`
	}
	if unmarshalErr := json.Unmarshal(data, &spec); unmarshalErr != nil {
		return 0
	}
	return planSourceIssue(spec.Plan.Source)
}

func planSourceIssue(source string) int {
	rest, found := strings.CutPrefix(strings.TrimSpace(source), "issue #")
	if !found {
		return 0
	}
	num, err := strconv.Atoi(strings.TrimSpace(rest))
	if err != nil || num <= 0 {
		return 0
	}
	return num
}

func parseLeadingIssueNum(rest string) (int, bool) {
	numStr, _, found := strings.Cut(rest, "-")
	if !found {
		return 0, false
	}
	num, err := strconv.Atoi(numStr)
	if err != nil || num <= 0 {
		return 0, false
	}
	return num, true
}

// PlanTaskSlug resolves a plan task's worktree slug: the explicit task slug,
// else the plan-qualified default bounded to naming.MaxSlugLength with a
// content hash suffix when truncation would otherwise collide.
func PlanTaskSlug(planSlug string, task planspec.Task) string {
	if task.Slug != "" {
		return task.Slug
	}
	slug := planSlug + "-" + task.ResolvedSlug()
	if len(slug) <= naming.MaxSlugLength {
		return slug
	}
	sum := sha1.Sum([]byte(slug))
	suffix := "-" + hex.EncodeToString(sum[:])[:8]
	baseLen := naming.MaxSlugLength - len(suffix)
	return strings.Trim(slug[:baseLen], "-") + suffix
}
