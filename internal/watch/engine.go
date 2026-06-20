// Package watch plans and runs one repository watcher cycle from labeled
// GitHub issues to fanout launches. All external effects are dependency
// injected so the planner can be tested without gh, tmux, git, or settings.
package watch

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/state"
)

const (
	defaultBackoffBase          = 60 * time.Second
	defaultDisableAfterFailures = 3
)

// LaunchKind classifies the launch fanout should perform for one labeled issue.
type LaunchKind string

const (
	LaunchStandalone LaunchKind = "standalone"
	LaunchParent     LaunchKind = "parent"
)

// SkipReason explains why a labeled issue was not eligible this cycle.
type SkipReason string

const (
	SkipClosed         SkipReason = "closed"
	SkipAlreadyRunning SkipReason = "already_running"
	SkipAlreadyFanned  SkipReason = "already_fanned"
	SkipDisabled       SkipReason = "disabled"
)

// DeferReason explains why an otherwise eligible issue should be retried in a
// later watcher cycle.
type DeferReason string

const (
	DeferBackoff     DeferReason = "backoff"
	DeferMaxSessions DeferReason = "max_sessions"
)

// FailureStage identifies the effect that failed during a cycle.
type FailureStage string

const (
	FailureCountChildren FailureStage = "count_open_children"
	FailureSwapLabels    FailureStage = "swap_labels"
	FailureLaunch        FailureStage = "launch"
)

// Config is the settings-shaped input for Engine. TUI/settings code maps its
// own config into this small struct; this package does not import settings.
type Config struct {
	TriggerLabel string
	RunningLabel string
	// MaxSessions is a repo-wide live pane budget. A value <= 0 means unlimited.
	MaxSessions int
	// BackoffBase defaults to 60s. Launch failures wait BackoffBase, then double
	// after each consecutive failure.
	BackoffBase time.Duration
	// DisableAfterFailures defaults to 3 consecutive launch failures for the
	// lifetime of this Engine.
	DisableAfterFailures int
	Now                  func() time.Time
}

// IO is the watcher's complete external boundary. Tests should provide fakes;
// production code wires these to ghissue/state/tmux/cmd fanout helpers.
type IO struct {
	ListLabeled       func(label string) ([]ghissue.Issue, error)
	CountOpenChildren func(issue ghissue.Issue) (int, error)
	SwapLabels        func(issue ghissue.Issue, removeLabel, addLabel string) error
	LoadState         func() (state.Store, error)
	PaneAlive         func(pane state.Pane) (bool, error)
	LaunchStandalone  func(issue ghissue.Issue) error
	LaunchParent      func(issue ghissue.Issue, limit int) (ParentLaunchResult, error)
}

// Engine keeps process-local launch failure state between cycles.
type Engine struct {
	cfg            Config
	io             IO
	failures       map[int]failureState
	runningRetries map[int]runningRetry
}

type failureState struct {
	Attempts    int
	NextRetryAt time.Time
	Disabled    bool
}

type runningRetry struct {
	kind LaunchKind
}

// NewEngine returns a watcher engine with process-local failure memory.
func NewEngine(cfg Config, io IO) *Engine {
	return &Engine{
		cfg:            cfg,
		io:             io,
		failures:       map[int]failureState{},
		runningRetries: map[int]runningRetry{},
	}
}

// ParentLaunchResult reports whether the underlying parent fan-out left more
// children for a later watcher cycle.
type ParentLaunchResult struct {
	Deferred bool
}

// Action is one launch the watcher should perform after acquiring the running
// label.
type Action struct {
	Issue        ghissue.Issue
	Kind         LaunchKind
	OpenChildren int
	// RetryRunning means the issue is already on the running label after a failed
	// requeue swap, so RunCycle should not acquire it again.
	RetryRunning bool
	// Limit is non-zero only when MaxSessions is finite and this is a parent
	// fan-out. The caller should pass it through as fanout's --limit value.
	Limit int
}

// Skip is a candidate removed by idempotency or an already-terminal condition.
type Skip struct {
	Issue  ghissue.Issue
	Reason SkipReason
}

// Deferred is a candidate left for a later cycle.
type Deferred struct {
	Issue      ghissue.Issue
	Reason     DeferReason
	RetryAfter time.Duration
	RetryAt    time.Time
}

// Failure records a per-issue cycle failure. RunCycle uses it for swap/launch
// failures; PlanCycle uses it for child-count failures.
type Failure struct {
	Issue       ghissue.Issue
	Stage       FailureStage
	Err         error
	RevertErr   error
	Attempts    int
	NextRetryAt time.Time
	Disabled    bool
}

// Report is the full outcome of one planned or executed watcher cycle.
type Report struct {
	Candidates        int
	RunningSessions   int
	MaxSessions       int
	RemainingSessions int
	Actions           []Action
	Launched          []Action
	Skipped           []Skip
	Deferred          []Deferred
	Failures          []Failure
}

// PlanCycle reads candidates and state, then returns the launch actions for one
// cycle. It does not mutate labels or launch panes.
func (e *Engine) PlanCycle() (Report, error) {
	if e == nil {
		return Report{}, errors.New("watch engine is nil")
	}
	cfg := normalizeConfig(e.cfg)
	if err := validatePlanConfig(cfg); err != nil {
		return Report{}, err
	}
	if err := validatePlanIO(e.io); err != nil {
		return Report{}, err
	}

	candidates, err := e.io.ListLabeled(cfg.TriggerLabel)
	if err != nil {
		return Report{}, fmt.Errorf("list labeled issues %q: %w", cfg.TriggerLabel, err)
	}
	var runningCandidates []ghissue.Issue
	if len(e.runningRetries) > 0 {
		runningCandidates, err = e.io.ListLabeled(cfg.RunningLabel)
		if err != nil {
			return Report{}, fmt.Errorf("list labeled issues %q: %w", cfg.RunningLabel, err)
		}
	}
	store, err := e.io.LoadState()
	if err != nil {
		return Report{}, fmt.Errorf("load fanout state: %w", err)
	}
	running, err := countLivePanes(store, e.io.PaneAlive)
	if err != nil {
		return Report{}, err
	}

	remaining := unlimitedSessions
	if cfg.MaxSessions > 0 {
		remaining = max(cfg.MaxSessions-running, 0)
	}
	plannedCandidates := e.planCandidates(candidates, runningCandidates)
	report := Report{
		Candidates:        len(plannedCandidates),
		RunningSessions:   running,
		MaxSessions:       cfg.MaxSessions,
		RemainingSessions: reportRemaining(remaining),
	}
	now := cfg.Now()

	for _, candidate := range plannedCandidates {
		issue := candidate.issue
		if strings.ToUpper(strings.TrimSpace(issue.State)) != "OPEN" {
			if candidate.retryKind != "" {
				delete(e.runningRetries, issue.Number)
			}
			report.Skipped = append(report.Skipped, Skip{Issue: issue, Reason: SkipClosed})
			continue
		}
		if hasLabel(issue, cfg.RunningLabel) && candidate.retryKind == "" {
			report.Skipped = append(report.Skipped, Skip{Issue: issue, Reason: SkipAlreadyRunning})
			continue
		}
		if failure := e.failures[issue.Number]; failure.Disabled {
			report.Skipped = append(report.Skipped, Skip{Issue: issue, Reason: SkipDisabled})
			continue
		} else if !failure.NextRetryAt.IsZero() && now.Before(failure.NextRetryAt) {
			report.Deferred = append(report.Deferred, Deferred{
				Issue:      issue,
				Reason:     DeferBackoff,
				RetryAfter: failure.NextRetryAt.Sub(now),
				RetryAt:    failure.NextRetryAt,
			})
			continue
		}
		if remaining == 0 {
			report.Deferred = append(report.Deferred, Deferred{Issue: issue, Reason: DeferMaxSessions})
			continue
		}

		openChildren, err := e.io.CountOpenChildren(issue)
		if err != nil {
			report.Failures = append(report.Failures, Failure{Issue: issue, Stage: FailureCountChildren, Err: err})
			continue
		}
		if openChildren < 0 {
			openChildren = 0
		}
		action := Action{
			Issue:        issue,
			Kind:         LaunchStandalone,
			OpenChildren: openChildren,
			RetryRunning: candidate.retryRunning,
		}
		consumes := 1
		if openChildren > 0 || candidate.retryKind == LaunchParent {
			action.Kind = LaunchParent
			consumes = openChildren
		}
		if alreadyFanned(store, action) {
			report.Skipped = append(report.Skipped, Skip{Issue: issue, Reason: SkipAlreadyFanned})
			continue
		}
		if action.Kind == LaunchParent && remaining > 0 {
			action.Limit = remaining
			consumes = min(openChildren, remaining)
		}
		report.Actions = append(report.Actions, action)
		if remaining > 0 {
			remaining = max(remaining-consumes, 0)
		}
	}
	report.RemainingSessions = reportRemaining(remaining)
	return report, nil
}

// RunCycle executes a planned cycle. It swaps trigger->running before launching
// and fails closed when the label swap cannot be applied.
func (e *Engine) RunCycle() (Report, error) {
	if e == nil {
		return Report{}, errors.New("watch engine is nil")
	}
	cfg := normalizeConfig(e.cfg)
	if err := validateRunIO(e.io); err != nil {
		return Report{}, err
	}
	report, err := e.PlanCycle()
	if err != nil {
		return report, err
	}

	for _, action := range report.Actions {
		if !action.RetryRunning {
			if err := e.io.SwapLabels(action.Issue, cfg.TriggerLabel, cfg.RunningLabel); err != nil {
				report.Failures = append(report.Failures, Failure{Issue: action.Issue, Stage: FailureSwapLabels, Err: err})
				continue
			}
		}

		var launchErr error
		var parentResult ParentLaunchResult
		switch action.Kind {
		case LaunchParent:
			parentResult, launchErr = e.io.LaunchParent(action.Issue, action.Limit)
		default:
			launchErr = e.io.LaunchStandalone(action.Issue)
		}
		if launchErr != nil {
			revertErr := e.io.SwapLabels(action.Issue, cfg.RunningLabel, cfg.TriggerLabel)
			if revertErr != nil {
				e.runningRetries[action.Issue.Number] = runningRetry{kind: action.Kind}
			} else {
				delete(e.runningRetries, action.Issue.Number)
			}
			failure := e.recordLaunchFailure(action.Issue.Number, cfg.Now())
			report.Failures = append(report.Failures, Failure{
				Issue:       action.Issue,
				Stage:       FailureLaunch,
				Err:         launchErr,
				RevertErr:   revertErr,
				Attempts:    failure.Attempts,
				NextRetryAt: failure.NextRetryAt,
				Disabled:    failure.Disabled,
			})
			continue
		}
		if action.Kind == LaunchParent && parentResult.Deferred {
			report.Deferred = append(report.Deferred, Deferred{Issue: action.Issue, Reason: DeferMaxSessions})
			if err := e.io.SwapLabels(action.Issue, cfg.RunningLabel, cfg.TriggerLabel); err != nil {
				e.runningRetries[action.Issue.Number] = runningRetry{kind: LaunchParent}
				report.Failures = append(report.Failures, Failure{Issue: action.Issue, Stage: FailureSwapLabels, Err: err})
			} else {
				delete(e.runningRetries, action.Issue.Number)
			}
		} else {
			delete(e.runningRetries, action.Issue.Number)
		}
		delete(e.failures, action.Issue.Number)
		report.Launched = append(report.Launched, action)
	}
	return report, nil
}

type planCandidate struct {
	issue        ghissue.Issue
	retryKind    LaunchKind
	retryRunning bool
}

func (e *Engine) planCandidates(triggered, running []ghissue.Issue) []planCandidate {
	candidates := make([]planCandidate, 0, len(triggered)+len(running))
	seen := map[int]bool{}
	for _, issue := range triggered {
		retry := e.runningRetries[issue.Number]
		candidates = append(candidates, planCandidate{issue: issue, retryKind: retry.kind})
		seen[issue.Number] = true
	}
	for _, issue := range running {
		if retry, ok := e.runningRetries[issue.Number]; ok && !seen[issue.Number] {
			candidates = append(candidates, planCandidate{
				issue:        issue,
				retryKind:    retry.kind,
				retryRunning: true,
			})
			seen[issue.Number] = true
		}
	}
	for num := range e.runningRetries {
		if !seen[num] {
			delete(e.runningRetries, num)
		}
	}
	return candidates
}

func normalizeConfig(cfg Config) Config {
	if cfg.BackoffBase <= 0 {
		cfg.BackoffBase = defaultBackoffBase
	}
	if cfg.DisableAfterFailures <= 0 {
		cfg.DisableAfterFailures = defaultDisableAfterFailures
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return cfg
}

func validatePlanConfig(cfg Config) error {
	switch {
	case strings.TrimSpace(cfg.TriggerLabel) == "":
		return errors.New("watch trigger label is required")
	case strings.TrimSpace(cfg.RunningLabel) == "":
		return errors.New("watch running label is required")
	case cfg.TriggerLabel == cfg.RunningLabel:
		return errors.New("watch trigger and running labels must differ")
	default:
		return nil
	}
}

func validatePlanIO(io IO) error {
	switch {
	case io.ListLabeled == nil:
		return errors.New("watch IO ListLabeled is required")
	case io.CountOpenChildren == nil:
		return errors.New("watch IO CountOpenChildren is required")
	case io.LoadState == nil:
		return errors.New("watch IO LoadState is required")
	case io.PaneAlive == nil:
		return errors.New("watch IO PaneAlive is required")
	default:
		return nil
	}
}

func validateRunIO(io IO) error {
	if err := validatePlanIO(io); err != nil {
		return err
	}
	switch {
	case io.SwapLabels == nil:
		return errors.New("watch IO SwapLabels is required")
	case io.LaunchStandalone == nil:
		return errors.New("watch IO LaunchStandalone is required")
	case io.LaunchParent == nil:
		return errors.New("watch IO LaunchParent is required")
	default:
		return nil
	}
}

func countLivePanes(store state.Store, alive func(state.Pane) (bool, error)) (int, error) {
	count := 0
	for _, pane := range store.Panes {
		if pane.Kind == state.PaneKindShell {
			continue
		}
		ok, err := alive(pane)
		if err != nil {
			return 0, fmt.Errorf("check pane %s alive: %w", pane.PaneID, err)
		}
		if ok {
			count++
		}
	}
	return count, nil
}

func (e *Engine) recordLaunchFailure(issueNum int, now time.Time) failureState {
	cfg := normalizeConfig(e.cfg)
	failure := e.failures[issueNum]
	failure.Attempts++
	if failure.Attempts >= cfg.DisableAfterFailures {
		failure.Disabled = true
		failure.NextRetryAt = time.Time{}
	} else {
		failure.NextRetryAt = now.Add(backoffDelay(cfg.BackoffBase, failure.Attempts))
	}
	e.failures[issueNum] = failure
	return failure
}

func backoffDelay(base time.Duration, attempts int) time.Duration {
	if attempts <= 1 {
		return base
	}
	shift := min(attempts-1, 30)
	return base * time.Duration(1<<shift)
}

const unlimitedSessions = -1

func reportRemaining(remaining int) int {
	if remaining == unlimitedSessions {
		return 0
	}
	return remaining
}

func alreadyFanned(store state.Store, action Action) bool {
	if action.Issue.Number <= 0 {
		return false
	}
	if action.Kind == LaunchParent {
		return false
	}
	for _, pane := range store.Panes {
		if pane.IssueNum == action.Issue.Number {
			return true
		}
		if pane.IssueNum > 0 && paneWorktreeMatchesIssue(pane, action.Issue.Number) {
			return true
		}
	}
	return false
}

func paneWorktreeMatchesIssue(pane state.Pane, issueNum int) bool {
	for _, name := range []string{pane.Slug, filepath.Base(strings.TrimSpace(pane.WorktreePath))} {
		if worktreeNameMatchesIssue(name, issueNum) {
			return true
		}
	}
	return false
}

func worktreeNameMatchesIssue(name string, issueNum int) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	num := strconv.Itoa(issueNum)
	if name == num {
		return true
	}
	for _, sep := range []string{"-", "_"} {
		if strings.HasSuffix(name, sep+num) {
			return true
		}
	}
	return false
}

func hasLabel(issue ghissue.Issue, name string) bool {
	for _, label := range issue.Labels {
		if label.Name == name {
			return true
		}
	}
	return false
}
