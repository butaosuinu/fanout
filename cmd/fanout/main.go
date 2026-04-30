package main

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/blockers"
	"github.com/butaosuinu/fanout/internal/briefing"
	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/displayname"
	"github.com/butaosuinu/fanout/internal/dmuxconfig"
	"github.com/butaosuinu/fanout/internal/dmuxsession"
	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/log"
	"github.com/butaosuinu/fanout/internal/popup"
	"github.com/butaosuinu/fanout/internal/tmuxctl"
)

const fanoutTagPrefix = "[fanout #"

func main() {
	lg := log.New(false)

	pr := cliflags.Parse(os.Args[1:], lg, os.Stdout)
	if pr.Code != exitcode.OK || pr.Config == nil {
		os.Exit(int(pr.Code))
	}
	cfg := pr.Config
	if cfg.Debug {
		lg = log.New(true)
	}

	if missing := checkDeps(); len(missing) > 0 {
		lg.Err("missing dependencies:")
		for _, d := range missing {
			fmt.Fprintf(lg.Stderr(), "  - %s\n", d)
		}
		os.Exit(int(exitcode.Env))
	}

	os.Exit(int(run(cfg, lg)))
}

func run(cfg *cliflags.Config, lg *log.Logger) exitcode.Code {
	info, err := dmuxsession.Resolve(cfg.Session)
	if err != nil {
		lg.Err("%s", err.Error())
		return exitcode.Env
	}

	if _, err := os.Stat(info.ConfigPath); err != nil {
		lg.Err("dmux config not found at %s (session reports it but file is missing)", info.ConfigPath)
		return exitcode.Env
	}

	lg.Info("dmux session: %s", info.Session)
	lg.Info("control pane: %s", info.ControlPane)
	lg.Info("project root: %s", info.ProjectRoot)
	lg.Info("config:       %s", info.ConfigPath)

	dcfg, err := dmuxconfig.Load(info.ConfigPath)
	if err != nil {
		lg.Err("%s", err.Error())
		return exitcode.Env
	}

	// Agent auto-detect.
	if cfg.Agent == "" {
		if pid := os.Getenv("TMUX_PANE"); pid != "" {
			if a := dcfg.AgentForPane(pid); a != "" {
				cfg.Agent = a
				lg.Info("auto-detected agent: %s (from calling pane %s)", cfg.Agent, pid)
			}
		}
	}

	// Project root must be a git work tree (we use this as the gh cwd).
	if !isGitWorkTree(info.ProjectRoot) {
		lg.Err("project root %s is not a git work tree; cannot resolve GitHub repo", info.ProjectRoot)
		return exitcode.Env
	}

	gh := ghissue.Runner{Cwd: info.ProjectRoot}

	lg.Info("fetching sub-issues of #%d", cfg.Parent)
	subIssues, err := gh.SubIssueList(cfg.Parent)
	if err != nil {
		lg.Err("gh sub-issue list failed: %v", err)
		return exitcode.Env
	}

	parentBody, err := gh.ParentBody(cfg.Parent)
	if err != nil {
		lg.Warn("could not read parent body (#%d); skipping task-list scan", cfg.Parent)
		parentBody = ""
	}

	bodyNums := ghissue.TaskListNumbers(parentBody)
	bodyNums = append(bodyNums, cfg.Include...)

	existing := map[int]bool{cfg.Parent: true}
	for _, s := range subIssues {
		existing[s.Number] = true
	}

	extra := []ghissue.Issue{}
	for _, num := range bodyNums {
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

	children := ghissue.MergeExtra(subIssues, extra)
	if len(extra) > 0 {
		lg.Info("parent body / --include added %d extra child reference(s) not in sub-issue API", len(extra))
	}

	totalChildren := len(children)
	if totalChildren == 0 {
		lg.Info("no sub-issues on #%d. nothing to do.", cfg.Parent)
		return exitcode.OK
	}

	openChildren := make([]ghissue.Issue, 0, totalChildren)
	for _, c := range children {
		if c.State == "OPEN" {
			openChildren = append(openChildren, c)
		}
	}
	openCount := len(openChildren)

	lg.Info("sub-issues: %d total, %d OPEN", totalChildren, openCount)
	if openCount == 0 {
		lg.Info("no OPEN sub-issues. nothing to do.")
		return exitcode.OK
	}

	// --only / --skip filter.
	var filteredOnly, filteredSkip []ghissue.Issue
	if len(cfg.Only) > 0 || len(cfg.Skip) > 0 {
		openSet := map[int]bool{}
		for _, c := range openChildren {
			openSet[c.Number] = true
		}
		if len(cfg.Only) > 0 {
			for _, n := range cfg.Only {
				if !openSet[n] {
					lg.Warn("--only: #%d not in OPEN child set (ignored)", n)
				}
			}
		}
		onlySet := intSet(cfg.Only)
		skipSet := intSet(cfg.Skip)
		var kept []ghissue.Issue
		for _, c := range openChildren {
			switch {
			case len(cfg.Only) > 0 && !onlySet[c.Number]:
				filteredOnly = append(filteredOnly, c)
			case len(cfg.Skip) > 0 && skipSet[c.Number]:
				filteredSkip = append(filteredSkip, c)
			default:
				kept = append(kept, c)
			}
		}
		openChildren = kept
		if len(openChildren) == 0 {
			lg.Info("all OPEN sub-issues filtered out by --only/--skip. nothing to do.")
			return exitcode.OK
		}
	}

	// Eager body/labels hydration for --unblocked-only.
	if cfg.UnblockedOnly {
		for i := range openChildren {
			if openChildren[i].Body != "" {
				continue
			}
			if err := gh.HydrateBodyLabels(&openChildren[i]); err != nil {
				lg.Warn("#%d: could not fetch body/labels for blocker check; treating as unblocked", openChildren[i].Number)
			}
		}
	}

	// Idempotency: skip already-fanned issues.
	fanned := dcfg.FannedNumbers()
	var targets []ghissue.Issue
	var skipped []int
	for _, c := range openChildren {
		if fanned[c.Number] {
			skipped = append(skipped, c.Number)
		} else {
			targets = append(targets, c)
		}
	}

	if len(targets) == 0 {
		lg.Ok("all %d OPEN sub-issue(s) already have a fanout pane. nothing to do.", len(skipped))
		return exitcode.OK
	}

	// --unblocked-only blocker filter.
	type blockedRow struct {
		Issue ghissue.Issue
		Refs  string
	}
	var blockedRows []blockedRow
	if cfg.UnblockedOnly {
		stateCache := map[int]string{}
		var still []ghissue.Issue
		for _, t := range targets {
			b1 := blockers.FromChildBody(t.Body)
			b2 := blockers.FromParentRow(parentBody, t.Number)
			all := blockers.Dedupe(b1, b2)
			var openBlockers []int
			for _, b := range all {
				st, ok := stateCache[b]
				if !ok {
					st, _ = gh.IssueState(b)
					stateCache[b] = st
				}
				if st == "OPEN" {
					openBlockers = append(openBlockers, b)
				}
			}
			hasBlockedLabel := false
			for _, l := range t.Labels {
				if l.Name == "blocked" {
					hasBlockedLabel = true
					break
				}
			}
			if hasBlockedLabel && len(all) == 0 {
				lg.Warn("#%d: has 'blocked' label but no parseable blocker numbers — treating as unblocked", t.Number)
			}
			if len(openBlockers) > 0 {
				parts := make([]string, len(openBlockers))
				for i, b := range openBlockers {
					parts[i] = fmt.Sprintf("OPEN #%d", b)
				}
				refs := strings.Join(parts, ", ")
				lg.Info("deferred: #%d (blocked by %s)", t.Number, refs)
				blockedRows = append(blockedRows, blockedRow{Issue: t, Refs: refs})
			} else {
				still = append(still, t)
			}
		}
		targets = still
	}

	// --limit (after blocker filter).
	var limitDeferred []ghissue.Issue
	if cfg.Limit > 0 && len(targets) > cfg.Limit {
		limitDeferred = targets[cfg.Limit:]
		targets = targets[:cfg.Limit]
	}

	if len(skipped) > 0 {
		sort.Ints(skipped)
		parts := make([]string, len(skipped))
		for i, n := range skipped {
			parts[i] = fmt.Sprintf("#%d", n)
		}
		lg.Info("already fanned-out (skipping): %s", strings.Join(parts, " "))
	}

	lg.Info("will create %d pane(s); deferred (blocked): %d; deferred (--limit): %d",
		len(targets), len(blockedRows), len(limitDeferred))

	c := lg.Colors()

	if cfg.DryRun && (len(filteredOnly) > 0 || len(filteredSkip) > 0) {
		fmt.Fprintf(lg.Stdout(), "\n%sfiltered out:%s\n", c.Info, c.Reset)
		for _, r := range filteredOnly {
			fmt.Fprintf(lg.Stdout(), "  #%d %s — not in --only\n", r.Number, r.Title)
		}
		for _, r := range filteredSkip {
			fmt.Fprintf(lg.Stdout(), "  #%d %s — in --skip\n", r.Number, r.Title)
		}
		fmt.Fprintln(lg.Stdout())
	}

	if cfg.DryRun && len(blockedRows) > 0 {
		fmt.Fprintf(lg.Stdout(), "\n%sdeferred (blocked):%s\n", c.Info, c.Reset)
		for _, br := range blockedRows {
			fmt.Fprintf(lg.Stdout(), "  #%d %s — blocked by %s\n", br.Issue.Number, br.Issue.Title, br.Refs)
		}
		fmt.Fprintln(lg.Stdout())
	}

	// Main pane-creation loop.
	created := 0
	failed := 0
	var createdNums []int
	for i, t := range targets {
		// Hydrate body lazily for issues that came from the Sub-issues API
		// path (body=""), unless --unblocked-only already did it upfront.
		if t.Body == "" {
			if d, err := gh.IssueDetail(t.Number); err == nil {
				t.Body = d.Body
			}
		}
		ok := createPaneForIssue(cfg, lg, info, t, c)
		if ok {
			created++
			createdNums = append(createdNums, t.Number)
		} else {
			failed++
			break
		}
		if i < len(targets)-1 {
			if cfg.SleepBetween > 0 && !cfg.DryRun {
				time.Sleep(time.Duration(cfg.SleepBetween * float64(time.Second)))
			}
		}
	}

	// Apply displayNames once at the end.
	if len(createdNums) > 0 && cfg.HasAnyDisplayName() {
		var overrides []displayname.Override
		for _, n := range createdNums {
			if no := cfg.FindName(n); no != nil && no.DisplayName != "" {
				overrides = append(overrides, displayname.Override{Num: no.Num, DisplayName: no.DisplayName})
			}
		}
		displayname.ApplyAll(info.ConfigPath, overrides, cfg.DryRun, lg.Stdout(), displayname.LogFns{
			Info: lg.Info, Warn: lg.Warn, Dim: lg.Dim, Err: lg.Err,
		})
	}

	// Summary.
	fmt.Fprintln(lg.Stdout())
	if created > 0 {
		lg.Ok("created: %d", created)
	}
	if failed > 0 {
		lg.Err("failed:  %d", failed)
	}
	if len(skipped) > 0 {
		lg.Info("skipped (already fanned-out): %d", len(skipped))
	}
	if total := len(filteredOnly) + len(filteredSkip); total > 0 {
		lg.Info("skipped (filtered): %d", total)
	}
	if len(blockedRows) > 0 {
		lg.Info("deferred (blocked): %d", len(blockedRows))
	}

	if len(limitDeferred) > 0 {
		fmt.Fprintf(lg.Stdout(), "\n%sDeferred %d issue(s) due to --limit. Rerun with:%s\n", c.Info, len(limitDeferred), c.Reset)
		nums := make([]string, len(limitDeferred))
		for i, r := range limitDeferred {
			nums[i] = fmt.Sprintf("#%d", r.Number)
		}
		fmt.Fprintf(lg.Stdout(), "  %s\n", strings.Join(nums, " "))
		fmt.Fprintf(lg.Stdout(), "  fanout %d --limit %d%s%s%s%s\n",
			cfg.Parent, len(limitDeferred),
			optFlag("--only", cfg.OnlyArg)+optFlag("--skip", cfg.SkipArg),
			boolFlag(" --unblocked-only", cfg.UnblockedOnly),
			optFlag("--agent", cfg.Agent),
			optFlag("--session", cfg.Session))
	}

	if failed > 0 {
		return exitcode.Env
	}
	return exitcode.OK
}

func createPaneForIssue(cfg *cliflags.Config, lg *log.Logger, info *dmuxsession.Info, t ghissue.Issue, c log.Palette) bool {
	briefingPath := briefing.Path(info.ProjectRoot, t.Number)
	rendered := briefing.Render(t.Number, t.Title, t.Body)
	if !cfg.DryRun {
		if err := os.WriteFile(briefingPath, []byte(rendered), 0o644); err != nil {
			lg.Err("#%d: write briefing: %v", t.Number, err)
			return false
		}
	}

	shortTitle := t.Title
	if len(shortTitle) > 60 {
		shortTitle = shortTitle[:60]
	}
	var slugHint, displayNameOverride string
	if no := cfg.FindName(t.Number); no != nil {
		slugHint = no.SlugHint
		displayNameOverride = no.DisplayName
	}

	var oneLinePrompt string
	if slugHint != "" {
		oneLinePrompt = fmt.Sprintf("%s%d] %s: %s. read %s and begin.", fanoutTagPrefix, t.Number, slugHint, shortTitle, briefingPath)
	} else {
		oneLinePrompt = fmt.Sprintf("%s%d] %s: read %s and begin.", fanoutTagPrefix, t.Number, shortTitle, briefingPath)
	}

	lg.Info("#%d: %s", t.Number, shortTitle)
	lg.Dim("  briefing -> %s", briefingPath)
	if slugHint != "" {
		lg.Dim("  slug-hint -> %s", slugHint)
	}
	if displayNameOverride != "" {
		lg.Dim("  display-name -> %s", displayNameOverride)
	}

	// Re-read dmux.config.json each iteration: a previous iteration in this
	// run may have already grown panes[], so a cached baseline would let
	// waitForNewPane() return immediately for subsequent issues even if the
	// current pane creation actually failed.
	freshCfg, err := dmuxconfig.Load(info.ConfigPath)
	if err != nil {
		lg.Err("#%d: reload dmux config: %v", t.Number, err)
		return false
	}
	baseline := freshCfg.PanesLen()

	newpanePayload, err := popup.MakeNewPanePayload(oneLinePrompt)
	if err != nil {
		lg.Err("#%d: build newPane payload: %v", t.Number, err)
		return false
	}
	var agentPayload []byte
	if cfg.Agent != "" {
		agentPayload, err = popup.MakeAgentPayload(cfg.Agent)
		if err != nil {
			lg.Err("#%d: build agent payload: %v", t.Number, err)
			return false
		}
	}

	if cfg.DryRun {
		fmt.Fprintf(lg.Stdout(), "  %sbriefing size%s: %d bytes\n", c.Dim, c.Reset, len(rendered))
		fmt.Fprintf(lg.Stdout(), "  %scurrent panes[] length%s: %d\n", c.Dim, c.Reset, baseline)
		tmuxctl.PrintSendKeys(lg.Stdout(), c.Dim, c.Reset, info.ControlPane, "Escape")
		tmuxctl.PrintSendKeys(lg.Stdout(), c.Dim, c.Reset, info.ControlPane, "n")
		fmt.Fprintf(lg.Stdout(), "    %s# would intercept newPanePopup and write: %s%s\n", c.Dim, string(newpanePayload), c.Reset)
		if len(agentPayload) > 0 {
			fmt.Fprintf(lg.Stdout(), "    %s# would intercept agentChoicePopup and write: %s%s\n", c.Dim, string(agentPayload), c.Reset)
		} else {
			fmt.Fprintf(lg.Stdout(), "    %s# WOULD FAIL: agent is empty (pass --agent or run from a dmux pane)%s\n", c.Warn, c.Reset)
		}
		lg.Ok("#%d: dry-run complete", t.Number)
		return true
	}

	if len(agentPayload) == 0 {
		lg.Err("#%d: no agent resolved. dmux v5.6.3 always shows the agent-choice popup after the prompt popup, so fanout needs an agent name. Pass --agent <name> or invoke fanout from a dmux-managed pane.", t.Number)
		return false
	}

	pidBaseline, err := popup.BaselinePIDs()
	if err != nil {
		lg.Err("#%d: pgrep baseline: %v", t.Number, err)
		return false
	}

	if err := tmuxctl.SendKeys(info.ControlPane, "Escape"); err != nil {
		lg.Err("#%d: tmux send-keys Escape: %v", t.Number, err)
		return false
	}
	time.Sleep(200 * time.Millisecond)
	if err := tmuxctl.SendKeys(info.ControlPane, "n"); err != nil {
		lg.Err("#%d: tmux send-keys n: %v", t.Number, err)
		return false
	}

	popupTimeout := time.Duration(cfg.PopupTimeoutSec) * time.Second
	if err := popup.Intercept(popup.NewPanePattern, pidBaseline, newpanePayload, "  newPanePopup", popupTimeout); err != nil {
		lg.Err("#%d: %v", t.Number, err)
		return false
	}

	pidBaseline, _ = popup.BaselinePIDs()
	if err := popup.Intercept(popup.AgentChoicePattern, pidBaseline, agentPayload, "  agentChoicePopup", popupTimeout); err != nil {
		lg.Err("#%d: %v", t.Number, err)
		return false
	}

	if !waitForNewPane(info.ConfigPath, baseline, 60*time.Second) {
		lg.Err("#%d: timed out after 60s waiting for config.json to grow", t.Number)
		return false
	}
	current, _ := dmuxconfig.Load(info.ConfigPath)
	cur := 0
	if current != nil {
		cur = current.PanesLen()
	}
	lg.Ok("#%d: pane created (panes[] now %d)", t.Number, cur)
	return true
}

func waitForNewPane(configPath string, baseline int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := dmuxconfig.Load(configPath)
		if err == nil && c.PanesLen() > baseline {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

func optFlag(flag, value string) string {
	if value == "" {
		return ""
	}
	return " " + flag + " " + value
}

func boolFlag(flagWithLeadSpace string, on bool) string {
	if on {
		return flagWithLeadSpace
	}
	return ""
}

func intSet(nums []int) map[int]bool {
	m := make(map[int]bool, len(nums))
	for _, n := range nums {
		m[n] = true
	}
	return m
}

func isGitWorkTree(path string) bool {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = path
	return cmd.Run() == nil
}

// checkDeps mirrors fanout:387-407.
func checkDeps() []string {
	var missing []string
	check := func(cmd, hint string) {
		if _, err := exec.LookPath(cmd); err != nil {
			missing = append(missing, hint)
		}
	}
	check("gh", "gh (brew install gh)")
	check("tmux", "tmux (brew install tmux)")
	check("pgrep", "pgrep (procps-ng on Linux; preinstalled on macOS)")

	if !ghSubIssueAvailable() {
		missing = append(missing, "gh-sub-issue extension (gh extension install yahsan2/gh-sub-issue)")
	}
	return missing
}

func ghSubIssueAvailable() bool {
	out, err := exec.Command("gh", "extension", "list").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if first, _, ok := strings.Cut(line, "\t"); ok && first == "gh sub-issue" {
			return true
		}
	}
	return false
}
