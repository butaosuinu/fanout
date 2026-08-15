package panelaunch

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/butaosuinu/fanout/internal/app/briefing"
	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/naming"
	"github.com/butaosuinu/fanout/internal/core/planspec"
	"github.com/butaosuinu/fanout/internal/infra/backendtest"
	"github.com/butaosuinu/fanout/internal/infra/codexapp"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/log"
	fanoutruntime "github.com/butaosuinu/fanout/internal/infra/runtime"
	"github.com/butaosuinu/fanout/internal/infra/settings"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/team"
	"github.com/butaosuinu/fanout/internal/infra/tmuxbackend"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

// newNonTmuxBackend is the herdr-shaped fake the non-tmux launch tests use: it
// can roll a fresh pane back but exposes no liveness stamp, so the strict
// identity lane must fail closed against it.
func newNonTmuxBackend() *backendtest.FreshCloserFake {
	return backendtest.NewFreshCloser(
		backendtest.WithName(backend.Herdr),
		backendtest.WithPanes("w1:p1"),
	)
}

func TestManualPromptWithBriefingActionPreservesTrailingMention(t *testing.T) {
	// A prompt ending in an @-mention must keep a whitespace terminator before
	// the appended sentence, else "@cmd/main.go" + ". read" merges into the
	// non-existent path "@cmd/main.go.".
	got := manualPromptWithBriefingAction("look at @cmd/main.go", "/tmp/b.md", "begin")
	if strings.Contains(got, "@cmd/main.go.") {
		t.Fatalf("trailing mention corrupted by the briefing sentence: %q", got)
	}
	if !strings.Contains(got, "@cmd/main.go . read /tmp/b.md") {
		t.Fatalf("mention not space-separated from sentence: %q", got)
	}

	// A prompt that does not end in a mention keeps the plain ". read" join.
	plain := manualPromptWithBriefingAction("inspect the API", "/tmp/b.md", "begin")
	if !strings.Contains(plain, "inspect the API. read /tmp/b.md") {
		t.Fatalf("non-mention prompt should keep the plain period join: %q", plain)
	}
}

func TestShortIssueTitleTruncatesOnRuneBoundary(t *testing.T) {
	title := strings.Repeat("あ", 61)

	got := ShortIssueTitle(title)

	if !utf8.ValidString(got) {
		t.Fatalf("short title is invalid UTF-8: %q", got)
	}
	if gotRunes := utf8.RuneCountInString(got); gotRunes != 60 {
		t.Fatalf("short title rune count = %d, want 60", gotRunes)
	}
	if got != strings.Repeat("あ", 60) {
		t.Fatalf("unexpected short title: %q", got)
	}
}

func TestShortIssueTitleKeepsSixtyRunes(t *testing.T) {
	title := strings.Repeat("界", 60)

	if got := ShortIssueTitle(title); got != title {
		t.Fatalf("ShortIssueTitle changed 60-rune title:\nwant %q\ngot  %q", title, got)
	}
}

func TestStatePaneCapturesCreatedPaneFields(t *testing.T) {
	now := time.Date(2026, 6, 4, 1, 2, 3, 0, time.UTC)
	req := Request{
		ParentRef:           "81",
		Number:              83,
		Slug:                "state-idempotency-83",
		DisplayNameOverride: "State Idempotency",
		BranchName:          "fanout/state-idempotency-83",
		Prompt:              "[fanout #83 of #81] state-idempotency-83: read .fanout/briefings/fanout-fanout-83.md and begin.",
		Agent:               "codex",
		ShellKey:            "shell-state-pane",
		Wave:                "wave5",
		LaunchMode:          agent.ModePlan,
		Worktree:            worktree.Plan{BaseBranch: "main"},
	}

	got := statePane(req, "%42", "/repo/.fanout/worktrees/state-idempotency-83", now, codexapp.Status{
		ThreadID:  "thread-1",
		SessionID: "session-1",
	})

	if got.Parent != "81" || got.IssueNum != 83 || got.PaneID != "%42" {
		t.Fatalf("state pane identity = %+v", got)
	}
	if got.ShellKey != "shell-state-pane" {
		t.Fatalf("shellKey = %q, want shell-state-pane", got.ShellKey)
	}
	if got.DisplayName != "State Idempotency" {
		t.Fatalf("displayName = %q, want State Idempotency", got.DisplayName)
	}
	if got.CreatedAt != "2026-06-04T01:02:03Z" {
		t.Fatalf("createdAt = %q", got.CreatedAt)
	}
	if got.Wave != "wave5" {
		t.Fatalf("wave = %q, want wave5", got.Wave)
	}
	if got.BaseBranch != "main" {
		t.Fatalf("baseBranch = %q, want main", got.BaseBranch)
	}
	if got.AgentStatus != "running" {
		t.Fatalf("agentStatus = %q, want running (起動時記録)", got.AgentStatus)
	}
	if !got.PlanMode {
		t.Fatal("codexPlanMode = false, want passthrough of req.PlanMode")
	}
	if got.CodexThreadID != "thread-1" || got.CodexSessionID != "session-1" {
		t.Fatalf("codex session ids = %q/%q, want thread-1/session-1", got.CodexThreadID, got.CodexSessionID)
	}
}

func TestStatePaneForBackendCapturesExactManagedIdentity(t *testing.T) {
	session := &backend.AgentSessionRef{
		Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "thread-528",
	}
	live := &backend.LivePane{
		Ref:        backend.PaneRef{Backend: backend.Herdr, Workspace: "w2", Pane: "w2:p1"},
		TerminalID: "term-child", RepoKey: "/repo/.git", AgentID: "fanout-0123456789abcdef01234567",
		AgentSession: session, SessionID: "fanout-repo-abcd", SocketPath: "/tmp/herdr.sock",
		ProcessIdentity: &backend.ProcessIdentity{ShellPID: 10, ForegroundProcessGroup: 10, AgentPID: 11},
	}
	req := Request{ParentRef: "524", Number: 528, Slug: "herdr-528", Agent: "codex"}
	got := statePaneForBackend(req, "w2:p1", "/repo/child", time.Unix(0, 0).UTC(), codexapp.Status{}, backend.Herdr, live)
	if got.Backend != backend.Herdr || got.WorkspaceID != "w2" ||
		got.TerminalID != "term-child" || got.RepoKey != "/repo/.git" ||
		got.AgentID != live.AgentID || got.AgentSession == nil ||
		*got.AgentSession != *session || got.SessionID != "fanout-repo-abcd" ||
		got.SocketPath != "/tmp/herdr.sock" || got.ProcessIdentity == nil ||
		*got.ProcessIdentity != *live.ProcessIdentity {
		t.Fatalf("Herdr state identity = %+v", got)
	}
}

func TestPrepareAttachedLivenessRejectsManagedStartGate(t *testing.T) {
	req := Request{AgentStartGate: "tmux-only", ShellKey: "tmux-key"}
	err := prepareAttachedLiveness(backend.MutationJournaled, &req)
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("prepareAttachedLiveness() error = %v, want Herdr rejection", err)
	}
	if req.ShellKey != "tmux-key" {
		t.Fatalf("rejected request ShellKey = %q, want unchanged", req.ShellKey)
	}
}

// The launch lane follows the backend's declared mutation model, not its name:
// a journaled runtime reaches the intent-journal lane and an atomic one the
// direct lane, whichever name each reports. The atomic lane is the only one
// that writes a state row straight after the split, so its dry run is the
// marker that separates the two.
func TestLaunchWithResultSelectsLaneByMutationModel(t *testing.T) {
	const atomicLaneMarker = "would write .fanout/state.json with paneId <pane_id>"
	tests := []struct {
		name           string
		backend        backend.Backend
		wantAtomicLane bool
	}{
		{
			name: "journaled backend takes the intent-journal lane",
			backend: backendtest.New(
				backendtest.WithName(backend.Herdr),
				backendtest.WithMutationModel(backend.MutationJournaled),
			),
		},
		{
			name:           "herdr-named atomic backend still takes the direct lane",
			backend:        backendtest.New(backendtest.WithName(backend.Herdr)),
			wantAtomicLane: true,
		},
		{
			name:           "tmux backend takes the direct lane",
			backend:        tmuxbackend.New(),
			wantAtomicLane: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			launcher := &Launcher{
				Cfg:         &cliflags.Config{Agent: "claude", DryRun: true, NoRefresh: true},
				Log:         log.NewWith(&stdout, &stderr, false),
				Info:        &fanoutruntime.Info{Target: "%caller", ProjectRoot: "/repo"},
				Backend:     tt.backend,
				CommandName: "fanout",
			}
			if _, ok := launcher.LaunchWithResult(Request{
				ParentRef: "100", Number: 101, Slug: "child-101", Agent: "claude",
			}); !ok {
				t.Fatalf("LaunchWithResult() = false, stderr:\n%s", stderr.String())
			}
			if got := strings.Contains(stdout.String(), atomicLaneMarker); got != tt.wantAtomicLane {
				t.Fatalf("atomic lane reached = %t, want %t; stdout:\n%s", got, tt.wantAtomicLane, stdout.String())
			}
		})
	}
}

func TestRuntimeBackendBindingsIncludeSyntheticManagedCoordinatorOwner(t *testing.T) {
	store := state.Store{Panes: []state.Pane{{
		Parent: ManualParentRef, RuntimeParent: "524", IssueNum: -1, Backend: backend.Herdr,
	}}}
	got := RuntimeBackendBindings("/repo", store)
	if len(got) != 1 || got[0] != (backend.Binding{Parent: "524", Backend: backend.Herdr}) {
		t.Fatalf("bindings = %+v, want coordinator owner 524", got)
	}
}

func TestSplitAndDecorateSkipsTmuxForSuccessfulNonTmuxBackend(t *testing.T) {
	installFakeTmux(t, "%unexpected")
	calls := filepath.Join(t.TempDir(), "tmux.calls")
	t.Setenv("TMUX_CALLS", calls)
	runtimeBackend := newNonTmuxBackend()
	launcher := &Launcher{
		Backend: runtimeBackend,
		Info:    &fanoutruntime.Info{Session: "herdr-session", Target: "w1", ProjectRoot: "/repo"},
		Log:     log.NewWith(io.Discard, io.Discard, false),
	}

	paneID, ok := launcher.splitAndDecorate(Request{ParentRef: "425", Number: 426, Title: "child"}, "/repo/worktree", decorateOpts{})
	if !ok || paneID != "w1:p1" {
		t.Fatalf("splitAndDecorate() = (%q, %t), want (w1:p1, true)", paneID, ok)
	}
	if launches, closed := runtimeBackend.Launches(), runtimeBackend.ClosedRefs(); len(launches) != 1 || len(closed) != 0 {
		t.Fatalf("backend calls = launches:%d closed:%v", len(launches), closed)
	}
	if body, err := os.ReadFile(calls); err == nil && len(body) > 0 {
		t.Fatalf("non-tmux launch invoked tmux:\n%s", body)
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestSplitAndDecorateRejectsStrictIdentityForNonTmuxBackend(t *testing.T) {
	installFakeTmux(t, "%unexpected")
	calls := filepath.Join(t.TempDir(), "tmux.calls")
	t.Setenv("TMUX_CALLS", calls)
	runtimeBackend := newNonTmuxBackend()
	var stderr bytes.Buffer
	launcher := &Launcher{
		Backend: runtimeBackend,
		Info:    &fanoutruntime.Info{Session: "herdr-session", Target: "w1", ProjectRoot: "/repo"},
		Log:     log.NewWith(io.Discard, &stderr, false),
	}

	paneID, ok := launcher.splitAndDecorate(Request{ParentRef: "425", Number: -1, Title: "attached"}, "/repo", decorateOpts{strictShellKey: true})
	if ok || paneID != "" {
		t.Fatalf("splitAndDecorate(strict) = (%q, %t), want (empty, false)", paneID, ok)
	}
	if closed := runtimeBackend.ClosedRefs(); len(closed) != 1 || closed[0].Pane != "w1:p1" {
		t.Fatalf("closed refs = %#v, want launched non-tmux pane cleanup", closed)
	}
	if !strings.Contains(stderr.String(), "strict pane liveness keys are not supported by the herdr backend") {
		t.Fatalf("stderr = %q, want strict non-tmux identity rejection", stderr.String())
	}
	if body, err := os.ReadFile(calls); err == nil && len(body) > 0 {
		t.Fatalf("strict non-tmux rejection invoked tmux:\n%s", body)
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestStatePaneCapturesTaskID(t *testing.T) {
	now := time.Date(2026, 6, 13, 1, 2, 3, 0, time.UTC)
	req := Request{
		ParentRef:  "plan:launch-plan",
		TaskID:     "api-client",
		Slug:       "extract-api-client-api-client",
		BranchName: "fanout/extract-api-client-api-client",
		Prompt:     "[fanout api-client of plan:launch-plan]",
		Agent:      "claude",
		Worktree:   worktree.Plan{BaseBranch: "main"},
	}

	got := statePane(req, "%42", "/repo/.fanout/worktrees/extract-api-client-api-client", now, codexapp.Status{})

	if got.Parent != "plan:launch-plan" || got.IssueNum != 0 || got.TaskID != "api-client" {
		t.Fatalf("task state identity = %+v, want plan parent, issueNum 0, taskID", got)
	}
}

func TestCreatePaneAcceptsManualRequestWithoutParentIssue(t *testing.T) {
	cfg := &cliflags.Config{Agent: "claude", DryRun: true, NoRefresh: true}
	if got := NewManualRequest(cfg, "/repo", state.Store{}, hooks.EmptyConfig(), ManualOptions{Title: "First Manual"}); got.Number != -1 {
		t.Fatalf("first manual number = %d, want -1", got.Number)
	}
	store := state.Store{Panes: []state.Pane{{Parent: ManualParentRef, IssueNum: -1}}}
	req := NewManualRequest(cfg, "/repo", store, hooks.EmptyConfig(), ManualOptions{
		Title:  "Manual Diagnostics",
		Body:   "extra context",
		Agent:  "codex",
		Prompt: "inspect the workspace",
	})
	if req.ParentRef != ManualParentRef || req.Number != -2 {
		t.Fatalf("manual identity = parent %q number %d, want %q -2", req.ParentRef, req.Number, ManualParentRef)
	}
	if req.Agent != "codex" || !strings.Contains(req.Prompt, "inspect the workspace") {
		t.Fatalf("manual launch = prompt %q agent %q", req.Prompt, req.Agent)
	}
	if req.LaunchMode != agent.ModeBuild {
		t.Fatalf("manual LaunchMode = %q, want explicit build launch", req.LaunchMode)
	}
	wantBriefingPath := briefing.Path("/repo", -2)
	if req.BriefingPath != wantBriefingPath || req.BriefingBody != "extra context" {
		t.Fatalf("manual briefing = path %q body %q", req.BriefingPath, req.BriefingBody)
	}
	if !strings.Contains(req.Prompt, "read "+wantBriefingPath+" for additional context and begin") {
		t.Fatalf("manual prompt does not reference briefing path: %q", req.Prompt)
	}
	_ = os.Remove(req.BriefingPath)
	t.Cleanup(func() { _ = os.Remove(req.BriefingPath) })

	var stdout, stderr bytes.Buffer
	lg := log.NewWith(&stdout, &stderr, false)
	info := &fanoutruntime.Info{Target: "%caller", ProjectRoot: "/repo"}
	launcher := &Launcher{Cfg: cfg, Log: lg, Info: info, Backend: tmuxbackend.New(), Recorder: nil, Palette: log.Palette{}, CommandName: "fanout"}
	if !launcher.LaunchOK(req) {
		t.Fatalf("LaunchOK() = false, stderr:\n%s", stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"#-2: Manual Diagnostics", "slug -> manual-2-manual-diagnostics-pane", "dry-run complete"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(req.BriefingPath); !os.IsNotExist(err) {
		t.Fatalf("manual dry-run wrote briefing file %s: %v", req.BriefingPath, err)
	}
}

func TestNewManualRequestSkipsOrphanedWorktreeSlug(t *testing.T) {
	repo := t.TempDir()
	cfg := &cliflags.Config{Agent: "claude", DryRun: true, NoRefresh: true}
	// Simulate a state-only close: manual-1's worktree dir survives with no state
	// row, so the fresh same-titled request must skip that slug, not regenerate it.
	orphan := filepath.Join(repo, ".fanout", "worktrees", "manual-1-inspect-api-pane")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}

	req := NewManualRequest(cfg, repo, state.Store{}, hooks.EmptyConfig(), ManualOptions{Title: "inspect api"})

	if req.Slug != "manual-2-inspect-api-pane" || req.BranchName != "fanout/manual-2-inspect-api-pane" {
		t.Fatalf("slug/branch = %q/%q, want manual-2-inspect-api-pane (skip orphaned manual-1)", req.Slug, req.BranchName)
	}
	if req.Number != -2 {
		t.Fatalf("number = %d, want -2 (still state-unique after the skip)", req.Number)
	}
}

func TestNewManualRequestPlanModeUsesPlanControllerAndPlanPrompt(t *testing.T) {
	codexPlanMode := true
	cfg := &cliflags.Config{
		Agent:     "codex",
		DryRun:    true,
		NoRefresh: true,
		PlanMode:  &codexPlanMode,
	}
	req := NewManualRequest(cfg, "/repo", state.Store{}, hooks.EmptyConfig(), ManualOptions{
		Title:  "Inspect API",
		Agent:  "codex",
		Prompt: "Inspect API",
	})

	if !req.PlanMode() {
		t.Fatal("PlanMode = false, want true")
	}
	if req.BriefingPath != "" || req.BriefingBody != "" {
		t.Fatalf("manual Codex Plan Mode should not use briefing file: path %q body %q", req.BriefingPath, req.BriefingBody)
	}
	if req.CodexPlanStatusPath != "/tmp/fanout-codex-plan-repo--1.json" {
		t.Fatalf("codex plan status path = %q", req.CodexPlanStatusPath)
	}
	for _, want := range []string{
		"manual fanout Codex Plan Mode session",
		"Body:\nInspect API",
		"Before presenting a plan, follow normal Codex planning behavior",
		"After that investigation, present the implementation plan",
		"<proposed_plan>...</proposed_plan>",
	} {
		if !strings.Contains(req.Prompt, want) {
			t.Fatalf("manual plan prompt missing %q:\n%s", want, req.Prompt)
		}
	}
	for _, unexpected := range []string{
		"You are assigned GitHub issue",
		"commit and push",
		"Open a pull request",
		"$post-work-review",
		"codex review --uncommitted",
		"Your first response must",
		"read /tmp/",
	} {
		if strings.Contains(req.Prompt, unexpected) {
			t.Fatalf("manual plan prompt contains %q:\n%s", unexpected, req.Prompt)
		}
	}

	cmd, err := buildAgentCommand(cfg, req, "fanout-go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"fanout-go __codex-plan-tui --codex codex",
		"--prompt 'You are starting a manual fanout Codex Plan Mode session in this repository.",
		"<proposed_plan>...</proposed_plan>",
		"--status-file /tmp/fanout-codex-plan-repo--1.json",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("agent command missing %q:\n%s", want, cmd)
		}
	}
}

func TestNewManualRequestPlanModePreservesMultilinePrompt(t *testing.T) {
	codexPlanMode := true
	cfg := &cliflags.Config{Agent: "codex", DryRun: true, PlanMode: &codexPlanMode}
	req := NewManualRequest(cfg, "/repo", state.Store{}, hooks.EmptyConfig(), ManualOptions{
		Title:  "Inspect API",
		Body:   "Inspect API\n\nCheck handlers",
		Agent:  "codex",
		Prompt: "Inspect API",
	})

	if req.BriefingPath != "" || req.BriefingBody != "" {
		t.Fatalf("small manual Codex Plan Mode prompt should stay inline: path %q body %q", req.BriefingPath, req.BriefingBody)
	}
	if !strings.Contains(req.Prompt, "Body:\nInspect API\n\nCheck handlers") {
		t.Fatalf("manual plan prompt did not preserve multiline prompt:\n%s", req.Prompt)
	}
	if !strings.Contains(req.Prompt, "<proposed_plan>...</proposed_plan>") {
		t.Fatalf("manual plan prompt = %q, want plan action", req.Prompt)
	}
	cmd, err := buildAgentCommand(cfg, req, "fanout-go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd, "Body:\nInspect API\n\nCheck handlers") {
		t.Fatalf("manual plan command did not preserve multiline prompt:\n%s", cmd)
	}
}

func TestNewManualRequestUsesAgentIndependentBriefingAndExplicitModes(t *testing.T) {
	const body = "Inspect API\n\nCheck handlers"
	tests := []struct {
		name     string
		agent    string
		planMode bool
		wantArg  string
	}{
		{name: "claude plan", agent: "claude", planMode: true, wantArg: "--permission-mode plan"},
		{name: "claude build", agent: "claude", wantArg: "--permission-mode auto"},
		{name: "opencode plan", agent: "opencode", planMode: true, wantArg: "--agent plan"},
		{name: "opencode build", agent: "opencode", wantArg: "--agent build"},
		{name: "codex build", agent: "codex", wantArg: "codex"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &cliflags.Config{Agent: tt.agent, DryRun: true, PlanMode: &tt.planMode}
			req := NewManualRequest(cfg, "/repo", state.Store{}, hooks.EmptyConfig(), ManualOptions{
				Title:  "Inspect API",
				Body:   body,
				Agent:  tt.agent,
				Prompt: "Inspect API",
			})

			wantMode := agent.ModeBuild
			if tt.planMode {
				wantMode = agent.ModePlan
			}
			if req.LaunchMode != wantMode {
				t.Fatalf("LaunchMode = %q, want %q", req.LaunchMode, wantMode)
			}
			if req.BriefingBody != body || !strings.Contains(req.Prompt, req.BriefingPath) {
				t.Fatalf("manual briefing = path %q body %q prompt %q, want ordinary briefing", req.BriefingPath, req.BriefingBody, req.Prompt)
			}
			if strings.Contains(req.BriefingBody, "<proposed_plan>...</proposed_plan>") || strings.Contains(req.Prompt, "<proposed_plan>...</proposed_plan>") {
				t.Fatalf("non-Codex-plan request contains Codex plan contract: prompt %q body %q", req.Prompt, req.BriefingBody)
			}
			command, err := buildAgentCommand(cfg, req, "fanout-go")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(command, tt.wantArg) {
				t.Fatalf("command = %q, want %q", command, tt.wantArg)
			}
			if strings.Contains(command, codexapp.PlanTUICommand) {
				t.Fatalf("command = %q, unexpected Codex plan controller", command)
			}
		})
	}
}

func TestMaxInlineManualPromptFitsLinuxSingleArgumentBudget(t *testing.T) {
	codexPlanMode := true
	cfg := &cliflags.Config{Agent: "codex", DryRun: true, PlanMode: &codexPlanMode}
	prompt := strings.Repeat("'", MaxInlineManualPromptBytes)
	req := NewManualRequest(cfg, "/repo", state.Store{}, hooks.EmptyConfig(), ManualOptions{
		Title:  prompt,
		Agent:  "codex",
		Prompt: prompt,
	})
	command, err := buildAgentCommand(cfg, req, "fanout-go")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := tmuxrun.BuildPaneLaunchCommand(command)
	const linuxMaxArgStringBytes = 32 * 4096
	if got := len(wrapper) + 1; got >= linuxMaxArgStringBytes {
		t.Fatalf("wrapped inline prompt = %d bytes including NUL, want less than Linux single-argument budget %d", got, linuxMaxArgStringBytes)
	}
}

func TestNewAttachedRequestRoutesOversizedPromptThroughBriefing(t *testing.T) {
	prompt := strings.Repeat("x", MaxInlineManualPromptBytes+1)
	target := AttachTarget{
		SourceParent:     ManualParentRef,
		SourceLabel:      "source",
		SourceBranchName: "feature/source",
	}

	for _, tc := range []struct {
		name              string
		cfg               *cliflags.Config
		wantPlanMode      bool
		wantCodexContract bool
		wantArg           string
	}{
		{name: "claude build", cfg: &cliflags.Config{Agent: "claude", DryRun: true, PlanMode: new(false)}, wantArg: "--permission-mode auto"},
		{name: "claude plan", cfg: &cliflags.Config{Agent: "claude", DryRun: true, PlanMode: new(true)}, wantPlanMode: true, wantArg: "--permission-mode plan"},
		{name: "opencode build", cfg: &cliflags.Config{Agent: "opencode", DryRun: true, PlanMode: new(false)}, wantArg: "--agent build"},
		{name: "opencode plan", cfg: &cliflags.Config{Agent: "opencode", DryRun: true, PlanMode: new(true)}, wantPlanMode: true, wantArg: "--agent plan"},
		{name: "codex build", cfg: &cliflags.Config{Agent: "codex", DryRun: true, PlanMode: new(false)}, wantArg: "codex"},
		{name: "codex plan mode", cfg: func() *cliflags.Config {
			planMode := true
			return &cliflags.Config{Agent: "codex", DryRun: true, PlanMode: &planMode}
		}(), wantPlanMode: true, wantCodexContract: true, wantArg: codexapp.PlanTUICommand},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := NewAttachedRequest(tc.cfg, t.TempDir(), state.Store{}, hooks.EmptyConfig(), prompt, "/repo/worktree", target)
			if got := req.PlanMode(); got != tc.wantPlanMode {
				t.Fatalf("attached PlanMode = %t, want %t (launch mode %q)", got, tc.wantPlanMode, req.LaunchMode)
			}
			if req.BriefingPath == "" || !strings.Contains(req.BriefingBody, prompt) {
				t.Fatalf("briefing = path %q body length %d, want path containing full %d-byte prompt", req.BriefingPath, len(req.BriefingBody), len(prompt))
			}
			if strings.Contains(req.Prompt, prompt) || !strings.Contains(req.Prompt, req.BriefingPath) {
				t.Fatalf("launch prompt should reference briefing without embedding payload: %d bytes", len(req.Prompt))
			}
			if got := strings.Contains(req.BriefingBody, "<proposed_plan>...</proposed_plan>"); got != tc.wantCodexContract {
				t.Fatalf("briefing Codex plan instructions = %t, want %t", got, tc.wantCodexContract)
			}
			command, err := buildAgentCommand(tc.cfg, req, "fanout-go")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(command, tc.wantArg) {
				t.Fatalf("command = %q, want %q", command, tc.wantArg)
			}
			if !tc.wantCodexContract && strings.Contains(command, codexapp.PlanTUICommand) {
				t.Fatalf("command = %q, unexpected Codex plan controller", command)
			}
		})
	}
}

func TestPlanAndManualPaneRequestsAllowMissingOrigin(t *testing.T) {
	cfg := &cliflags.Config{Agent: "claude"}
	spec := planspec.Spec{Plan: planspec.Plan{Slug: "launch-plan", Title: "launch plan"}}
	task := planspec.Task{ID: "api-client", Title: "Extract API client", Briefing: "Do it"}

	taskReq := NewTaskRequest(cfg, "/repo", spec, task, settings.Defaults(), hooks.EmptyConfig(), nil)
	if taskReq.LaunchMode != agent.ModeBuild {
		t.Fatalf("task pane LaunchMode = %q, want explicit build launch", taskReq.LaunchMode)
	}
	if !taskReq.Worktree.AllowMissingOrigin {
		t.Fatal("task pane AllowMissingOrigin = false, want true")
	}
	if taskReq.Worktree.RefreshBestEffort {
		t.Fatal("task pane RefreshBestEffort = true, want false (plan tasks keep strict refresh)")
	}

	manualReq := NewManualRequest(cfg, "/repo", state.Store{}, hooks.EmptyConfig(), ManualOptions{Title: "Manual diagnostics"})
	if manualReq.LaunchMode != agent.ModeBuild {
		t.Fatalf("manual pane LaunchMode = %q, want explicit build launch", manualReq.LaunchMode)
	}
	if !manualReq.Worktree.AllowMissingOrigin {
		t.Fatal("manual pane AllowMissingOrigin = false, want true")
	}
	if !manualReq.Worktree.RefreshBestEffort {
		t.Fatal("manual pane RefreshBestEffort = false, want true (TUI new sessions tolerate a dirty local base)")
	}
}

func TestCreatePaneIssueDryRunDoesNotWriteBriefing(t *testing.T) {
	projectRoot := t.TempDir()
	cfg := &cliflags.Config{ParentRef: "100", Agent: "claude", BaseBranch: "main", DryRun: true, NoRefresh: true}
	req := NewIssueRequest(cfg, projectRoot, ghissue.Issue{Number: 101, Title: "First child", Body: "body"}, settings.Defaults(), hooks.EmptyConfig(), false, nil)
	if req.LaunchMode != agent.ModeBuild {
		t.Fatalf("issue LaunchMode = %q, want build", req.LaunchMode)
	}

	assertCreatePaneDryRunDoesNotWriteBriefing(t, cfg, projectRoot, req)
}

func TestCreatePaneTaskDryRunDoesNotWriteBriefing(t *testing.T) {
	projectRoot := t.TempDir()
	cfg := &cliflags.Config{Agent: "claude", BaseBranch: "main", DryRun: true, NoRefresh: true}
	spec := planspec.Spec{Plan: planspec.Plan{Slug: "launch-plan", Title: "launch plan"}}
	task := planspec.Task{ID: "api-client", Title: "Extract API client", Briefing: "## Goal\nExtract it"}
	req := NewTaskRequest(cfg, projectRoot, spec, task, settings.Defaults(), hooks.EmptyConfig(), nil)

	assertCreatePaneDryRunDoesNotWriteBriefing(t, cfg, projectRoot, req)
}

func TestLaunchWithResultDryRunHasNoPaneID(t *testing.T) {
	projectRoot := t.TempDir()
	cfg := &cliflags.Config{ParentRef: "100", Agent: "claude", BaseBranch: "main", DryRun: true, NoRefresh: true}
	req := NewIssueRequest(cfg, projectRoot, ghissue.Issue{Number: 101, Title: "First child", Body: "body"}, settings.Defaults(), hooks.EmptyConfig(), false, nil)
	launcher := &Launcher{
		Cfg:         cfg,
		Log:         log.NewWith(io.Discard, io.Discard, false),
		Info:        &fanoutruntime.Info{Target: "%caller", ProjectRoot: projectRoot},
		Backend:     tmuxbackend.New(),
		Palette:     log.Palette{},
		CommandName: "fanout",
	}

	result, ok := launcher.LaunchWithResult(req)

	if !ok {
		t.Fatal("LaunchWithResult() = false, want successful dry run")
	}
	if result.PaneID != "" {
		t.Fatalf("dry-run PaneID = %q, want empty", result.PaneID)
	}
}

func TestAttachWithResultReturnsExactPaneID(t *testing.T) {
	installFakeExecutable(t, "claude")
	targetPath := t.TempDir()
	cfg := &cliflags.Config{Agent: "claude"}
	recorder := &captureStateRecorder{}
	runtimeBackend := backendtest.NewTmux(backendtest.WithPanes("%314"))
	launcher := &Launcher{
		Cfg:         cfg,
		Log:         log.NewWith(io.Discard, io.Discard, false),
		Info:        &fanoutruntime.Info{Target: "%caller", ProjectRoot: targetPath},
		Backend:     runtimeBackend,
		Recorder:    recorder,
		Palette:     log.Palette{},
		CommandName: "fanout",
	}
	req := Request{
		ParentRef: ManualParentRef,
		Number:    -1,
		Slug:      "attached-pane",
		Prompt:    "inspect",
		Agent:     "claude",
		Hooks:     hooks.EmptyConfig(),
	}

	result, ok := launcher.AttachWithResult(req, targetPath)

	if !ok {
		t.Fatal("AttachWithResult() = false, want true")
	}
	if result.PaneID != "%314" {
		t.Fatalf("PaneID = %q, want %%314", result.PaneID)
	}
	if len(recorder.panes) != 1 || !strings.HasPrefix(recorder.panes[0].ShellKey, "shell-") {
		t.Fatalf("recorded panes = %+v, want generated liveness key", recorder.panes)
	}
	wantStamps := []backendtest.PaneValue{{PaneID: "%314", Value: recorder.panes[0].ShellKey}}
	if got := runtimeBackend.PaneValues(backendtest.MethodStampPaneShellKey); !reflect.DeepEqual(got, wantStamps) {
		t.Fatalf("liveness stamps = %+v, want %+v", got, wantStamps)
	}
}

func TestAttachRecordsRecoveryRowWhenLivenessStampAndFreshCloseFail(t *testing.T) {
	installFakeExecutable(t, "claude")
	targetPath := t.TempDir()
	recorder := &captureStateRecorder{}
	runtimeBackend := backendtest.NewLiveness(
		backendtest.WithPanes("%315"),
		backendtest.WithStampError(fmt.Errorf("stamp failed")),
		backendtest.WithFreshCloseError(fmt.Errorf("pane still live")),
	)
	launcher := &Launcher{
		Cfg:         &cliflags.Config{Agent: "claude"},
		Log:         log.NewWith(io.Discard, io.Discard, false),
		Info:        &fanoutruntime.Info{Target: "%caller", ProjectRoot: targetPath},
		Backend:     runtimeBackend,
		Recorder:    recorder,
		CommandName: "fanout",
	}
	req := Request{
		ParentRef:      ManualParentRef,
		Number:         -1,
		Slug:           "attached-pane",
		Prompt:         "inspect",
		Agent:          "claude",
		AgentStartGate: "recovery-gate",
		Hooks:          hooks.EmptyConfig(),
	}

	result, ok := launcher.AttachWithResult(req, targetPath)

	if ok || result.PaneID != "" {
		t.Fatalf("AttachWithResult() = %+v, %v, want failed launch", result, ok)
	}
	if len(recorder.panes) != 1 {
		t.Fatalf("recorded panes = %+v, want one recovery row", recorder.panes)
	}
	got := recorder.panes[0]
	if got.PaneID != "%315" || got.Kind != state.PaneKindAttachedAgent || !strings.HasPrefix(got.ShellKey, "shell-") {
		t.Fatalf("recovery pane = %+v, want keyed attached pane %%315", got)
	}
	if gates := runtimeBackend.ReleasedGates(); len(gates) != 0 {
		t.Fatalf("released gates = %v, want gate held while pane close is unconfirmed", gates)
	}
}

func TestAttachReleasesStartGateAfterConfirmedFreshCleanup(t *testing.T) {
	installFakeExecutable(t, "claude")
	runtimeBackend := backendtest.NewLiveness(
		backendtest.WithPanes("%315"),
		backendtest.WithStampError(fmt.Errorf("stamp failed")),
	)
	launcher := &Launcher{
		Cfg:         &cliflags.Config{Agent: "claude"},
		Log:         log.NewWith(io.Discard, io.Discard, false),
		Info:        &fanoutruntime.Info{Target: "%caller", ProjectRoot: t.TempDir()},
		Backend:     runtimeBackend,
		CommandName: "fanout",
	}
	req := Request{
		ParentRef:      ManualParentRef,
		Number:         -1,
		Slug:           "attached-pane",
		Prompt:         "inspect",
		Agent:          "claude",
		AgentStartGate: "cleanup-gate",
		Hooks:          hooks.EmptyConfig(),
	}

	result, ok := launcher.AttachWithResult(req, t.TempDir())

	if ok || result.PaneID != "" {
		t.Fatalf("AttachWithResult() = %+v, %v, want failed launch with confirmed cleanup", result, ok)
	}
	if got := runtimeBackend.ReleasedGates(); len(got) != 1 || got[0] != "cleanup-gate" {
		t.Fatalf("released gates = %v, want [cleanup-gate]", got)
	}
}

func TestAttachRecordsRecoveryRowWhenCodexStartupAndCloseFail(t *testing.T) {
	installFakeExecutable(t, "codex")
	targetPath := t.TempDir()
	statusPath := filepath.Join(t.TempDir(), "codex-status.json")
	if err := codexapp.WriteFailedStatus(statusPath, fmt.Errorf("startup failed")); err != nil {
		t.Fatal(err)
	}
	recorder := &captureStateRecorder{}
	launcher := &Launcher{
		Cfg:  &cliflags.Config{Agent: "codex"},
		Log:  log.NewWith(io.Discard, io.Discard, false),
		Info: &fanoutruntime.Info{Target: "%caller", ProjectRoot: targetPath},
		Backend: backendtest.NewTmux(
			backendtest.WithPanes("%316"),
			backendtest.WithOwnedClose(
				backend.CloseResult{Status: backend.CloseFailed},
				fmt.Errorf("pane still live"),
			),
		),
		Recorder:    recorder,
		CommandName: "fanout",
	}
	req := Request{
		ParentRef:           ManualParentRef,
		Number:              -1,
		Slug:                "codex-plan",
		Prompt:              "plan",
		Agent:               "codex",
		LaunchMode:          agent.ModePlan,
		CodexPlanStatusPath: statusPath,
		Hooks:               hooks.EmptyConfig(),
	}

	_, ok := launcher.AttachWithResult(req, targetPath)

	if ok {
		t.Fatal("AttachWithResult() = true, want startup failure")
	}
	if len(recorder.panes) != 1 || recorder.panes[0].PaneID != "%316" || recorder.panes[0].Kind != state.PaneKindAttachedAgent {
		t.Fatalf("recorded panes = %+v, want live failed-startup pane %%316", recorder.panes)
	}
}

func TestShellRecordsRecoveryRowWhenLivenessStampAndFreshCloseFail(t *testing.T) {
	repo := t.TempDir()
	gitCmdTest(t, repo, "init")
	// shellTmux splits through tmuxrun itself, so the pane id still comes from a
	// tmux shim; only the liveness stamp and the rollback close are faked here.
	installFakeTmux(t, "%317")
	stubCloseFreshPane(t, func(string) error { return fmt.Errorf("pane still live") })
	launcher := &Launcher{
		Info:    &fanoutruntime.Info{Target: "%caller", ProjectRoot: repo},
		Backend: backendtest.NewLiveness(backendtest.WithStampError(fmt.Errorf("stamp failed"))),
	}

	err := launcher.Shell(ShellRequest{TargetPath: repo, Root: true})

	if err == nil || !strings.Contains(err.Error(), "pane still live") {
		t.Fatalf("Shell() error = %v, want failed cleanup", err)
	}
	store, loadErr := state.LoadProject(repo)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(store.Panes) != 1 || store.Panes[0].PaneID != "%317" || store.Panes[0].Kind != state.PaneKindShell {
		t.Fatalf("state panes = %+v, want live shell recovery row %%317", store.Panes)
	}
}

type captureStateRecorder struct {
	panes []state.Pane
}

func (r *captureStateRecorder) RecordPane(pane state.Pane) error {
	r.panes = append(r.panes, pane)
	return nil
}

func (*captureStateRecorder) RemovePane(string, int) error { return nil }

func (*captureStateRecorder) RemoveTaskPane(string, string) error { return nil }

func assertCreatePaneDryRunDoesNotWriteBriefing(t *testing.T, cfg *cliflags.Config, projectRoot string, req Request) {
	t.Helper()
	if !cfg.DryRun {
		t.Fatal("test helper requires cfg.DryRun")
	}
	if req.BriefingPath == "" || req.BriefingBody == "" {
		t.Fatalf("briefing path/body must be populated: path %q body len %d", req.BriefingPath, len(req.BriefingBody))
	}
	_ = os.Remove(req.BriefingPath)
	t.Cleanup(func() { _ = os.Remove(req.BriefingPath) })

	var stdout, stderr bytes.Buffer
	lg := log.NewWith(&stdout, &stderr, false)
	info := &fanoutruntime.Info{Target: "%caller", ProjectRoot: projectRoot}
	launcher := &Launcher{Cfg: cfg, Log: lg, Info: info, Backend: tmuxbackend.New(), Recorder: nil, Palette: log.Palette{}, CommandName: "fanout"}
	if !launcher.LaunchOK(req) {
		t.Fatalf("LaunchOK() = false, stderr:\n%s", stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if !strings.Contains(stdout.String(), "briefing size") {
		t.Fatalf("dry-run output missing briefing size:\n%s", stdout.String())
	}
	if _, err := os.Stat(req.BriefingPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote briefing file %s: %v", req.BriefingPath, err)
	}
}

func TestLaunchFailsWhenWorktreeAppearsDuringLaunch(t *testing.T) {
	repo := t.TempDir()
	gitCmdTest(t, repo, "init")
	installFakeExecutable(t, "claude")
	worktreePath := filepath.Join(repo, ".fanout", "worktrees", "duplicate-title-77")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &cliflags.Config{
		Agent:      "claude",
		ParentRef:  "81",
		BaseBranch: "main",
		NoRefresh:  true,
	}
	var stderr bytes.Buffer
	lg := log.NewWith(io.Discard, &stderr, false)
	info := &fanoutruntime.Info{
		Session:     "test",
		Target:      "%caller",
		ProjectRoot: repo,
	}
	issue := ghissue.Issue{Number: 77, Title: "Duplicate Title", State: "OPEN", Body: "body"}

	launcher := &Launcher{Cfg: cfg, Log: lg, Info: info, Backend: tmuxbackend.New(), Palette: log.Palette{}, CommandName: "fanout"}
	if launcher.LaunchOK(NewIssueRequest(cfg, repo, issue, settings.Defaults(), hooks.EmptyConfig(), false, nil)) {
		t.Fatal("LaunchOK() = true, want false for launch-time worktree collision")
	}
	if got := stderr.String(); !strings.Contains(got, "worktree path already exists during launch") {
		t.Fatalf("stderr = %q, want launch collision message", got)
	}
}

func TestCodexTeamStartupFailureTearsDownPaneAndWorktree(t *testing.T) {
	repo := t.TempDir()
	gitCmdTest(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmdTest(t, repo, "add", "README.md")
	gitCmdTest(t, repo, "-c", "user.name=Fanout Test", "-c", "user.email=fanout@example.invalid", "commit", "-m", "initial")

	installFakeExecutable(t, "codex")
	runtimeBackend := backendtest.NewTmux(
		backendtest.WithPanes("%271"),
		backendtest.WithOwnedClose(backend.CloseResult{Status: backend.CloseConfirmed, ContainerID: "@7"}, nil),
	)

	cfg := &cliflags.Config{ParentRef: "100", Agent: "codex", BaseBranch: "main", NoRefresh: true}
	teamCtx := &briefing.TeamContext{ParentLabel: "#100", DBPath: "/tmp/team.db"}
	req := NewIssueRequest(cfg, repo, ghissue.Issue{Number: 101, Title: "Team child", Body: "body"}, settings.Defaults(), hooks.EmptyConfig(), false, teamCtx)
	req.CodexTeamStatusPath = filepath.Join(t.TempDir(), "team-status.json")
	if err := codexapp.WriteFailedStatus(req.CodexTeamStatusPath, fmt.Errorf("watcher boom")); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	launcher := &Launcher{
		Cfg:         cfg,
		Log:         log.NewWith(io.Discard, &stderr, false),
		Info:        &fanoutruntime.Info{Target: "%caller", ProjectRoot: repo},
		Backend:     runtimeBackend,
		Palette:     log.Palette{},
		CommandName: "fanout",
	}
	if launcher.LaunchOK(req) {
		t.Fatal("LaunchOK() = true, want startup failure")
	}
	if !strings.Contains(stderr.String(), "start Codex team TUI in pane %271: watcher boom") {
		t.Fatalf("stderr = %q, want team startup failure", stderr.String())
	}
	if _, err := os.Stat(req.Worktree.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists after failed startup: %s (%v)", req.Worktree.WorktreePath, err)
	}
	closes := runtimeBackend.CloseRequests()
	if len(closes) != 1 {
		t.Fatalf("identity-checked closes = %+v, want exactly one", closes)
	}
	closed := closes[0]
	if closed.Ref.Pane != "%271" || closed.WorktreePath != req.Worktree.WorktreePath {
		t.Fatalf("safe close identity = (%q, %q), want (%q, %q)", closed.Ref.Pane, closed.WorktreePath, "%271", req.Worktree.WorktreePath)
	}
	if !strings.HasPrefix(closed.ShellKey, "shell-") {
		t.Fatalf("safe close shell key = %q, want generated pane key", closed.ShellKey)
	}
	wantStamps := []backendtest.PaneValue{{PaneID: "%271", Value: closed.ShellKey}}
	if got := runtimeBackend.PaneValues(backendtest.MethodStampPaneShellKey); !reflect.DeepEqual(got, wantStamps) {
		t.Fatalf("liveness stamps = %+v, want %+v", got, wantStamps)
	}
	branch := exec.Command("git", "branch", "--list", req.BranchName)
	branch.Dir = repo
	out, err := branch.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("branch remains after failed startup: %s", out)
	}
}

func TestFailCleanupPreservesWorktreeWhenPaneCloseCannotBeConfirmed(t *testing.T) {
	worktreePath := t.TempDir()
	prepared := worktree.Result{Plan: worktree.Plan{
		ProjectRoot:  t.TempDir(),
		WorktreePath: worktreePath,
	}}
	runtimeBackend := backendtest.NewTmux(
		backendtest.WithOwnedClose(backend.CloseResult{Status: backend.CloseFailed}, fmt.Errorf("pane still live")),
	)

	if failCleanup(runtimeBackend, "#101", "%caller", "%271", worktreePath, "", &prepared, nil) {
		t.Fatal("failCleanup() = true, want unconfirmed pane close")
	}

	wantCloses := []backend.CloseRequest{{
		Ref:          backend.PaneRef{Backend: backend.Tmux, Pane: "%271"},
		WorktreePath: worktreePath,
	}}
	if got := runtimeBackend.CloseRequests(); !reflect.DeepEqual(got, wantCloses) {
		t.Fatalf("safe close identity = %+v, want %+v", got, wantCloses)
	}
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("worktree was removed after unconfirmed pane close: %v", err)
	}
}

func TestLaunchRejectsUnsupportedRefreshBaseInDryRun(t *testing.T) {
	repo := t.TempDir()
	cfg := &cliflags.Config{
		Agent:      "claude",
		ParentRef:  "81",
		BaseBranch: "refs/heads/main",
		DryRun:     true,
	}
	var stderr bytes.Buffer
	lg := log.NewWith(io.Discard, &stderr, false)
	info := &fanoutruntime.Info{
		Session:     "test",
		Target:      "%caller",
		ProjectRoot: repo,
	}
	issue := ghissue.Issue{Number: 77, Title: "Bad Base", State: "OPEN", Body: "body"}

	launcher := &Launcher{Cfg: cfg, Log: lg, Info: info, Backend: tmuxbackend.New(), Palette: log.Palette{}, CommandName: "fanout"}
	if launcher.LaunchOK(NewIssueRequest(cfg, repo, issue, settings.Defaults(), hooks.EmptyConfig(), false, nil)) {
		t.Fatal("LaunchOK() = true, want false for unsupported refresh base")
	}
	if got := stderr.String(); !strings.Contains(got, `base branch "refs/heads/main" is not refreshable`) {
		t.Fatalf("stderr = %q, want unsupported base message", got)
	}
}

func TestManualPaneSlugBoundsPromptDerivedSlug(t *testing.T) {
	got := ManualPaneSlug(strings.Repeat("a", naming.MaxSlugLength+50), -12)

	if len(got) > naming.MaxSlugLength {
		t.Fatalf("ManualPaneSlug len = %d, want <= %d: %q", len(got), naming.MaxSlugLength, got)
	}
	if !strings.HasPrefix(got, "manual-12-") || !strings.HasSuffix(got, "-pane") {
		t.Fatalf("ManualPaneSlug = %q, want manual-12-...-pane", got)
	}
	if strings.HasSuffix(got, "-12") {
		t.Fatalf("ManualPaneSlug = %q, must not end with an issue-like numeric suffix", got)
	}
}

func TestNewIssueRequestQualifiesDefaultSlugForSharedChild(t *testing.T) {
	cfg := &cliflags.Config{ParentRef: "200", Agent: "claude"}
	issue := ghissue.Issue{Number: 501, Title: "Shared child", Body: "body"}

	got := NewIssueRequest(cfg, "/repo", issue, settings.Defaults(), hooks.EmptyConfig(), true, nil)

	if got.Slug != "shared-child-parent-200-501" {
		t.Fatalf("slug = %q, want shared-child-parent-200-501", got.Slug)
	}
	if got.BranchName != "fanout/shared-child-parent-200-501" {
		t.Fatalf("branch = %q, want fanout/shared-child-parent-200-501", got.BranchName)
	}
	if !strings.Contains(got.Prompt, "[fanout #501 of #200] shared-child-parent-200-501:") {
		t.Fatalf("prompt = %q, want parent-qualified slug", got.Prompt)
	}
}

func TestNewIssueRequestPassesResolvedBaseBranchToBriefing(t *testing.T) {
	cfg := &cliflags.Config{ParentRef: "200", Agent: "codex", BaseBranch: "release/v1"}
	issue := ghissue.Issue{Number: 501, Title: "Release child", Body: "body"}

	got := NewIssueRequest(cfg, "/repo", issue, settings.Defaults(), hooks.EmptyConfig(), false, nil)

	if !strings.Contains(got.BriefingBody, "git diff --name-only release/v1...HEAD") {
		t.Fatalf("briefing did not include selected base branch:\n%s", got.BriefingBody)
	}
	if !strings.Contains(got.BriefingBody, "gh pr create --base release/v1") {
		t.Fatalf("briefing did not include PR base branch:\n%s", got.BriefingBody)
	}
}

func TestNewIssueRequestCarriesIssueWave(t *testing.T) {
	cfg := &cliflags.Config{ParentRef: "200", Agent: "claude"}
	issue := ghissue.Issue{Number: 501, Title: "Wave child", Body: "body", Wave: "wave5"}

	got := NewIssueRequest(cfg, "/repo", issue, settings.Defaults(), hooks.EmptyConfig(), false, nil)

	if got.Wave != "wave5" {
		t.Fatalf("Wave = %q, want wave5", got.Wave)
	}
}

func TestNewIssueRequestUsesIssueAgentOverride(t *testing.T) {
	cfg := &cliflags.Config{
		ParentRef: "200",
		Agent:     "claude",
		AgentOverrides: []cliflags.AgentOverride{
			{Target: "501", Name: "codex"},
		},
	}
	issue := ghissue.Issue{Number: 501, Title: "Codex child", Body: "body"}

	got := NewIssueRequest(cfg, "/repo", issue, settings.Defaults(), hooks.EmptyConfig(), false, nil)

	if got.Agent != "codex" {
		t.Fatalf("Agent = %q, want codex", got.Agent)
	}
	if !strings.Contains(got.BriefingBody, "$post-work-review") {
		t.Fatalf("briefing did not use codex-specific guidance:\n%s", got.BriefingBody)
	}
	if strings.Contains(got.BriefingBody, "Optional: Agent Teams") {
		t.Fatalf("codex briefing contains Claude-only Agent Teams guidance:\n%s", got.BriefingBody)
	}
}

func TestNewWatchRequestUsesReservedParentAndIssueBriefing(t *testing.T) {
	cfg := &cliflags.Config{
		ParentRef:    "220",
		Agent:        "codex",
		BaseBranch:   "main",
		BranchPrefix: "watch/",
		PlanMode:     new(false),
	}
	issue := ghissue.Issue{Number: 223, Title: "Watch runtime helper", Body: "body"}
	resolved := settings.Defaults()
	resolved.ChildPlanMode = true

	got := NewWatchRequest(cfg, "/repo", issue, resolved, hooks.EmptyConfig())

	if got.ParentRef != WatchParentRef || got.Number != 223 || got.TaskID != "" {
		t.Fatalf("watch identity = parent %q number %d task %q, want %q/223 with no task", got.ParentRef, got.Number, got.TaskID, WatchParentRef)
	}
	if got.Slug != "watch-runtime-helper-223" || got.BranchName != "watch/watch-runtime-helper-223" {
		t.Fatalf("slug/branch = %q/%q", got.Slug, got.BranchName)
	}
	if got.Worktree.WorktreePath != "/repo/.fanout/worktrees/watch-runtime-helper-223" {
		t.Fatalf("worktree path = %q", got.Worktree.WorktreePath)
	}
	wantBriefingPath := briefing.Path("/repo", 223)
	if got.BriefingPath != wantBriefingPath {
		t.Fatalf("briefing path = %q", got.BriefingPath)
	}
	wantPrompt := "[fanout #223 of #@watch] watch-runtime-helper-223: Watch runtime helper. read " + wantBriefingPath + " and investigate, then propose a plan."
	if got.Prompt != wantPrompt {
		t.Fatalf("prompt = %q, want %q", got.Prompt, wantPrompt)
	}
	if !got.PlanMode() || got.LaunchMode != agent.ModePlan || got.CodexPlanStatusPath == "" {
		t.Fatalf("plan mode = %t launch mode %q status %q, want child Plan Mode watch pane", got.PlanMode(), got.LaunchMode, got.CodexPlanStatusPath)
	}
	for _, want := range []string{
		"You are assigned GitHub issue #223",
		`Open a pull request with "Closes #223"`,
		"Closes #223",
	} {
		if !strings.Contains(got.BriefingBody, want) {
			t.Fatalf("briefing missing %q:\n%s", want, got.BriefingBody)
		}
	}
	if !strings.Contains(got.BriefingBody, "<proposed_plan>") {
		t.Fatalf("watch briefing does not use Codex Plan Mode body:\n%s", got.BriefingBody)
	}

	pane := statePane(got, "%42", got.Worktree.WorktreePath, time.Date(2026, 6, 20, 1, 2, 3, 0, time.UTC), codexapp.Status{})
	if pane.Parent != WatchParentRef || pane.IssueNum != 223 {
		t.Fatalf("state key = %q/%d, want %q/223", pane.Parent, pane.IssueNum, WatchParentRef)
	}
}

func TestNewWatchRequestUsesExplicitBuildModeWhenChildPlanModeIsDisabled(t *testing.T) {
	cfg := &cliflags.Config{ParentRef: "220", Agent: "opencode", PlanMode: new(true)}
	issue := ghissue.Issue{Number: 223, Title: "Watch runtime helper", Body: "body"}
	resolved := settings.Defaults()
	resolved.ChildPlanMode = false

	got := NewWatchRequest(cfg, "/repo", issue, resolved, hooks.EmptyConfig())

	if got.LaunchMode != agent.ModeBuild || got.PlanMode() {
		t.Fatalf("LaunchMode/PlanMode = %q/%t, want explicit build mode", got.LaunchMode, got.PlanMode())
	}
}

func TestNewWatchRequestConsumesChildPlanModeForEveryAgent(t *testing.T) {
	issue := ghissue.Issue{Number: 223, Title: "Watch runtime helper", Body: "body"}
	resolved := settings.Defaults()
	resolved.ChildPlanMode = true

	for _, agentName := range []string{"claude", "codex", "opencode"} {
		t.Run(agentName, func(t *testing.T) {
			cfg := &cliflags.Config{ParentRef: "220", Agent: agentName, DryRun: true}
			got := NewWatchRequest(cfg, "/repo", issue, resolved, hooks.EmptyConfig())

			if got.LaunchMode != agent.ModePlan || !got.PlanMode() {
				t.Fatalf("LaunchMode/PlanMode = %q/%t, want child Plan Mode", got.LaunchMode, got.PlanMode())
			}
		})
	}
}

func TestNewIssueRequestPassesResolvedSettingsAgentAndTeamToBriefing(t *testing.T) {
	cfg := &cliflags.Config{
		ParentRef:  "100",
		Agent:      "claude",
		BaseBranch: "main",
		DryRun:     true,
		AgentOverrides: []cliflags.AgentOverride{
			{Target: "501", Name: "codex"},
		},
	}
	resolvedSettings := settings.Defaults()
	resolvedSettings.AutoPullRequest = false
	issue := ghissue.Issue{Number: 501, Title: "First child", Body: "body"}
	// Mirrors cmd/fanout's buildTeamContext (team.go) for parent #100 with two
	// sibling children.
	teamCtx := &briefing.TeamContext{
		ParentLabel: "#100",
		DBPath:      team.DBPath("/repo/project_root", "100"),
		Siblings: []briefing.TeamSibling{
			{Num: 501, Title: "First child"},
			{Num: 502, Title: "Second child"},
		},
	}

	got := NewIssueRequest(cfg, "/repo/project_root", issue, resolvedSettings, hooks.EmptyConfig(), false, teamCtx)

	if got.Agent != "codex" {
		t.Fatalf("Agent = %q, want codex from issue override", got.Agent)
	}
	if !got.CodexTeamMode || got.CodexTeamStatusPath != "/tmp/fanout-codex-team-project_root-501.json" {
		t.Fatalf("codex team mode/status = %t/%q", got.CodexTeamMode, got.CodexTeamStatusPath)
	}
	if got.TeamDBPath != "/tmp/fanout-project_root-100.db" {
		t.Fatalf("TeamDBPath = %q", got.TeamDBPath)
	}
	for _, want := range []string{
		"## Coordinating with your sibling panes",
		"You are the pane for issue #501 (parent #100)",
		"- #501: First child (you)",
		"- #502: Second child",
		"/tmp/fanout-project_root-100.db",
		"$post-work-review",
		"Only after the committed branch review is clean and marked should you push the",
	} {
		if !strings.Contains(got.BriefingBody, want) {
			t.Fatalf("briefing missing %q:\n%s", want, got.BriefingBody)
		}
	}
	for _, unwanted := range []string{
		`Open a pull request with "Closes #501"`,
		"structure the PR body",
		"Diagram gate",
		"Optional: Agent Teams",
		"run the `/code-review` slash command",
		"open the PR",
	} {
		if strings.Contains(got.BriefingBody, unwanted) {
			t.Fatalf("briefing contains %q despite resolved settings/agent:\n%s", unwanted, got.BriefingBody)
		}
	}
}

func TestNewIssueRequestPlanModeUsesPlanPromptAndBriefing(t *testing.T) {
	cfg := &cliflags.Config{ParentRef: "200", Agent: "codex", PlanMode: new(true)}
	issue := ghissue.Issue{Number: 501, Title: "Plan child", Body: "body"}
	teamCtx := &briefing.TeamContext{ParentLabel: "#200", DBPath: "/tmp/team.db"}

	got := NewIssueRequest(cfg, "/repo", issue, settings.Defaults(), hooks.EmptyConfig(), false, teamCtx)

	if !got.PlanMode() {
		t.Fatal("PlanMode = false, want true")
	}
	if !got.CodexTeamRequested || got.CodexTeamMode || got.CodexTeamStatusPath != "" {
		t.Fatalf("Codex team request/bridge = %t/%t status %q, want Plan Mode precedence", got.CodexTeamRequested, got.CodexTeamMode, got.CodexTeamStatusPath)
	}
	if !strings.Contains(got.Prompt, "read "+briefing.Path("/repo", 501)+" and investigate, then propose a plan.") {
		t.Fatalf("prompt = %q, want plan action", got.Prompt)
	}
	if !strings.Contains(got.BriefingBody, "Before presenting a plan, follow normal Codex planning behavior") {
		t.Fatalf("briefing did not require investigation before planning:\n%s", got.BriefingBody)
	}
	if !strings.Contains(got.BriefingBody, "<proposed_plan>...</proposed_plan>") {
		t.Fatalf("briefing did not require proposed_plan wrapper:\n%s", got.BriefingBody)
	}
	for _, unexpected := range []string{"Your first response must"} {
		if strings.Contains(got.BriefingBody, unexpected) {
			t.Fatalf("plan briefing contains implementation instruction %q:\n%s", unexpected, got.BriefingBody)
		}
	}
}

func TestNewTaskRequestCodexTeamUsesTaskIdentityAndStatusPath(t *testing.T) {
	cfg := &cliflags.Config{Agent: "codex", DryRun: true}
	spec := planspec.Spec{Plan: planspec.Plan{Slug: "launch-plan", Title: "Launch plan"}}
	task := planspec.Task{ID: "api-client", Title: "Extract API client", Briefing: "body"}
	teamCtx := &briefing.TeamContext{ParentLabel: "plan:launch-plan", DBPath: "/tmp/team.db"}

	got := NewTaskRequest(cfg, "/repo", spec, task, settings.Defaults(), hooks.EmptyConfig(), teamCtx)

	if !got.CodexTeamMode {
		t.Fatal("CodexTeamMode = false, want true")
	}
	if got.CodexTeamStatusPath != "/tmp/fanout-codex-team-repo-api-client.json" {
		t.Fatalf("CodexTeamStatusPath = %q", got.CodexTeamStatusPath)
	}
	if got.TeamDBPath != "/tmp/team.db" {
		t.Fatalf("TeamDBPath = %q", got.TeamDBPath)
	}
	if member := codexTeamMember(got); member != "api-client" {
		t.Fatalf("codexTeamMember() = %q, want api-client", member)
	}
}

func TestNewTaskRequestConsumesChildPlanMode(t *testing.T) {
	spec := planspec.Spec{Plan: planspec.Plan{Slug: "launch-plan", Title: "Launch plan"}}
	task := planspec.Task{ID: "api-client", Title: "Extract API client", Briefing: "body"}
	resolved := settings.Defaults()
	resolved.ChildPlanMode = true

	for _, agentName := range []string{"claude", "codex", "opencode"} {
		t.Run(agentName, func(t *testing.T) {
			cfg := &cliflags.Config{Agent: agentName, DryRun: true, PlanMode: new(false)}
			got := NewTaskRequest(cfg, "/repo", spec, task, resolved, hooks.EmptyConfig(), nil)

			if got.LaunchMode != agent.ModePlan || !strings.Contains(got.Prompt, "investigate, then propose a plan") {
				t.Fatalf("LaunchMode/prompt = %q/%q, want child Plan Mode task launch", got.LaunchMode, got.Prompt)
			}
			if agentName == "codex" {
				if !strings.Contains(got.BriefingBody, "<proposed_plan>...</proposed_plan>") {
					t.Fatalf("Codex briefing does not include proposed_plan contract:\n%s", got.BriefingBody)
				}
			} else if !strings.Contains(got.BriefingBody, "Before implementation:") {
				t.Fatalf("briefing does not include native plan instructions:\n%s", got.BriefingBody)
			}
		})
	}
}

func TestNewTaskRequestCodexPlanModeDisablesTeamBridge(t *testing.T) {
	cfg := &cliflags.Config{Agent: "codex", DryRun: true}
	spec := planspec.Spec{Plan: planspec.Plan{Slug: "launch-plan", Title: "Launch plan"}}
	task := planspec.Task{ID: "api-client", Title: "Extract API client", Briefing: "body"}
	resolved := settings.Defaults()
	resolved.ChildPlanMode = true
	teamCtx := &briefing.TeamContext{ParentLabel: "plan:launch-plan", DBPath: "/tmp/team.db"}

	got := NewTaskRequest(cfg, "/repo", spec, task, resolved, hooks.EmptyConfig(), teamCtx)

	if got.LaunchMode != agent.ModePlan || !got.CodexPlanMode() {
		t.Fatalf("LaunchMode/CodexPlanMode = %q/%t, want Codex Plan Mode", got.LaunchMode, got.CodexPlanMode())
	}
	if got.CodexPlanStatusPath != "/tmp/fanout-codex-plan-repo-launch-plan-api-client.json" {
		t.Fatalf("CodexPlanStatusPath = %q", got.CodexPlanStatusPath)
	}
	if !got.CodexTeamRequested || got.CodexTeamMode || got.CodexTeamStatusPath != "" {
		t.Fatalf("Codex team request/bridge = %t/%t status %q, want Plan Mode precedence", got.CodexTeamRequested, got.CodexTeamMode, got.CodexTeamStatusPath)
	}
	if notice := launchNotice(got); !strings.Contains(notice, "plan mode takes precedence over --team; Codex team bridge is disabled for this pane") {
		t.Fatalf("launchNotice() = %q, want Plan Mode precedence warning", notice)
	}
}

func TestNewTaskRequestUsesTaskBriefingPathAndPrompt(t *testing.T) {
	cfg := &cliflags.Config{Agent: "claude", BaseBranch: "release/v1"}
	spec := planspec.Spec{
		Plan: planspec.Plan{Slug: "launch-plan", Title: "launch plan"},
	}
	task := planspec.Task{
		ID:          "api-client",
		Title:       "Extract API client",
		Briefing:    "## Goal\nExtract it",
		DisplayName: "API client",
		Wave:        "2",
	}

	got := NewTaskRequest(cfg, "/repo", spec, task, settings.Defaults(), hooks.EmptyConfig(), nil)

	if got.ParentRef != "plan:launch-plan" || got.TaskID != "api-client" || got.Number != 0 {
		t.Fatalf("task identity = parent %q task %q issue %d", got.ParentRef, got.TaskID, got.Number)
	}
	if got.Slug != "launch-plan-extract-api-client-api-client" || got.BranchName != "fanout/launch-plan-extract-api-client-api-client" {
		t.Fatalf("slug/branch = %q/%q", got.Slug, got.BranchName)
	}
	wantBriefingPath := briefing.TaskPath("/repo", "launch-plan", "api-client")
	if got.BriefingPath != wantBriefingPath {
		t.Fatalf("briefing path = %q", got.BriefingPath)
	}
	if !strings.Contains(got.Prompt, "[fanout api-client of plan:launch-plan] launch-plan-extract-api-client-api-client: Extract API client. read "+wantBriefingPath+" and begin.") {
		t.Fatalf("prompt = %q", got.Prompt)
	}
	if got.DisplayNameOverride != "API client" || got.Wave != "2" {
		t.Fatalf("display/wave = %q/%q", got.DisplayNameOverride, got.Wave)
	}
	if !strings.Contains(got.BriefingBody, "Plan: launch-plan / Task: api-client") {
		t.Fatalf("task briefing missing plan/task footer:\n%s", got.BriefingBody)
	}
}

func TestNewTaskRequestUsesTaskAgentOverride(t *testing.T) {
	cfg := &cliflags.Config{
		Agent:      "claude",
		BaseBranch: "main",
		AgentOverrides: []cliflags.AgentOverride{
			{Target: "api-client", Name: "codex"},
		},
	}
	spec := planspec.Spec{Plan: planspec.Plan{Slug: "launch-plan", Title: "launch plan"}}
	task := planspec.Task{ID: "api-client", Title: "Extract API client", Briefing: "## Goal\nExtract it"}

	got := NewTaskRequest(cfg, "/repo", spec, task, settings.Defaults(), hooks.EmptyConfig(), nil)

	if got.Agent != "codex" {
		t.Fatalf("Agent = %q, want codex", got.Agent)
	}
	if !strings.Contains(got.BriefingBody, "$post-work-review") {
		t.Fatalf("task briefing did not use codex-specific guidance:\n%s", got.BriefingBody)
	}
}

func TestNewTaskRequestCollapsesMultilineTitleInPrompt(t *testing.T) {
	cfg := &cliflags.Config{Agent: "claude", BaseBranch: "main"}
	spec := planspec.Spec{Plan: planspec.Plan{Slug: "launch-plan", Title: "launch plan"}}
	task := planspec.Task{
		ID:       "api-client",
		Title:    "Extract API\nclient\tlayer",
		Briefing: "## Goal\nExtract it",
	}

	got := NewTaskRequest(cfg, "/repo", spec, task, settings.Defaults(), hooks.EmptyConfig(), nil)

	if strings.ContainsAny(got.Prompt, "\n\t") {
		t.Fatalf("prompt contains embedded newline/tab: %q", got.Prompt)
	}
	if !strings.Contains(got.Prompt, "Extract API client layer. read ") {
		t.Fatalf("prompt did not collapse task title whitespace: %q", got.Prompt)
	}
}

func TestNewTaskRequestQualifiesDefaultSlugByPlan(t *testing.T) {
	cfg := &cliflags.Config{Agent: "claude", BaseBranch: "main"}
	task := planspec.Task{ID: "api-client", Title: "Extract API client", Briefing: "## Goal\nExtract it"}

	first := NewTaskRequest(cfg, "/repo", planspec.Spec{Plan: planspec.Plan{Slug: "launch-plan", Title: "launch plan"}}, task, settings.Defaults(), hooks.EmptyConfig(), nil)
	second := NewTaskRequest(cfg, "/repo", planspec.Spec{Plan: planspec.Plan{Slug: "cleanup-plan", Title: "Cleanup plan"}}, task, settings.Defaults(), hooks.EmptyConfig(), nil)

	if first.Slug == second.Slug || first.BranchName == second.BranchName {
		t.Fatalf("default task slugs must be plan-qualified, got %q/%q and %q/%q", first.Slug, first.BranchName, second.Slug, second.BranchName)
	}

	task.Slug = "shared-api-client"
	first = NewTaskRequest(cfg, "/repo", planspec.Spec{Plan: planspec.Plan{Slug: "launch-plan", Title: "launch plan"}}, task, settings.Defaults(), hooks.EmptyConfig(), nil)
	second = NewTaskRequest(cfg, "/repo", planspec.Spec{Plan: planspec.Plan{Slug: "cleanup-plan", Title: "Cleanup plan"}}, task, settings.Defaults(), hooks.EmptyConfig(), nil)
	if first.Slug != "shared-api-client" || second.Slug != "shared-api-client" {
		t.Fatalf("explicit slug should be shared exactly, got %q and %q", first.Slug, second.Slug)
	}
}

func TestBuildAgentCommandStartsCodexPlanTUIControllerInPlanModeDryRun(t *testing.T) {
	cfg := &cliflags.Config{Agent: "codex", DryRun: true, PlanMode: new(true)}
	req := Request{
		Number:              1,
		Prompt:              "[fanout #1] plan",
		Agent:               "codex",
		LaunchMode:          agent.ModePlan,
		CodexPlanStatusPath: "/tmp/fanout-codex-plan-repo-1.json",
	}

	got, err := buildAgentCommand(cfg, req, "fanout-go")
	if err != nil {
		t.Fatal(err)
	}

	want := "fanout-go __codex-plan-tui --codex codex --prompt '[fanout #1] plan' --status-file /tmp/fanout-codex-plan-repo-1.json"
	if got != want {
		t.Fatalf("buildAgentCommand() = %q, want %q", got, want)
	}
}

func TestBuildAgentCommandInjectsPlanModeForNonCodexAgents(t *testing.T) {
	for _, tc := range []struct {
		agent string
		want  string
	}{
		{agent: "claude", want: "--permission-mode plan"},
		{agent: "opencode", want: "--agent plan"},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			got, err := buildAgentCommand(
				&cliflags.Config{DryRun: true},
				Request{Agent: tc.agent, Prompt: "plan", LaunchMode: agent.ModePlan},
				"fanout-go",
			)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("buildAgentCommand() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildAgentCommandInjectsBuildModeForIssueAgents(t *testing.T) {
	for _, tc := range []struct {
		agent string
		want  string
	}{
		{agent: "claude", want: "--permission-mode auto"},
		{agent: "opencode", want: "--agent build"},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			got, err := buildAgentCommand(
				&cliflags.Config{DryRun: true},
				Request{Agent: tc.agent, Prompt: "work", LaunchMode: agent.ModeBuild},
				"fanout-go",
			)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("buildAgentCommand() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPreflightClaudeLaunchModeOmitsFlagsBelowMinimum(t *testing.T) {
	for _, tc := range []struct {
		name        string
		version     string
		wantMode    agent.LaunchMode
		wantWarning bool
	}{
		{name: "minimum", version: "2.1.207 (Claude Code)", wantMode: agent.ModePlan},
		{name: "newer", version: "2.2.0 (Claude Code)", wantMode: agent.ModePlan},
		{name: "older", version: "2.1.206 (Claude Code)", wantWarning: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installClaudeVersionExecutable(t, tc.version)
			var stderr bytes.Buffer
			launcher := &Launcher{Cfg: &cliflags.Config{}, Log: log.NewWith(io.Discard, &stderr, false)}
			req := Request{ParentRef: "100", Number: 101, Agent: "claude", LaunchMode: agent.ModePlan}

			launcher.preflightClaudeLaunchMode(&req)

			if req.LaunchMode != tc.wantMode {
				t.Fatalf("LaunchMode = %q, want %q", req.LaunchMode, tc.wantMode)
			}
			gotWarning := strings.Contains(stderr.String(), "Claude Code 2.1.207+ is required")
			if gotWarning != tc.wantWarning {
				t.Fatalf("warning = %q, want warning=%t", stderr.String(), tc.wantWarning)
			}
		})
	}
}

func TestLogPaneRequestWarnsOnceWhenPlanDisablesCodexTeamBridge(t *testing.T) {
	var stderr bytes.Buffer
	logPaneRequest(Request{
		ParentRef:          "100",
		Number:             101,
		Agent:              "codex",
		LaunchMode:         agent.ModePlan,
		CodexTeamRequested: true,
	}, log.NewWith(io.Discard, &stderr, false))

	want := "plan mode takes precedence over --team; Codex team bridge is disabled for this pane"
	if count := strings.Count(stderr.String(), want); count != 1 {
		t.Fatalf("warning count = %d, want 1:\n%s", count, stderr.String())
	}
}

func TestBuildAgentCommandStartsCodexTeamTUIControllerInDryRun(t *testing.T) {
	cfg := &cliflags.Config{Agent: "codex", DryRun: true}
	req := Request{
		ParentRef:           "100",
		Number:              501,
		Prompt:              "[fanout #501 of #100] begin",
		Agent:               "codex",
		CodexTeamMode:       true,
		CodexTeamStatusPath: "/tmp/fanout-codex-team-repo-501.json",
	}

	got, err := buildAgentCommand(cfg, req, "fanout-go")
	if err != nil {
		t.Fatal(err)
	}
	want := "fanout-go __codex-team-tui --codex codex --prompt '[fanout #501 of #100] begin' --self 501 --parent 100 --status-file /tmp/fanout-codex-team-repo-501.json"
	if got != want {
		t.Fatalf("buildAgentCommand() = %q, want %q", got, want)
	}
}

func TestBuildAgentCommandLeavesStartGateToBackend(t *testing.T) {
	got, err := buildAgentCommand(
		&cliflags.Config{Agent: "claude", DryRun: true},
		Request{Agent: "claude", Prompt: "review", AgentStartGate: "gate; touch /tmp/untrusted"},
		"fanout-go",
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "tmux wait-for") || !strings.Contains(got, "claude") {
		t.Fatalf("buildAgentCommand() = %q, want ungated agent command", got)
	}
}

func TestBuildAgentCommandRejectsStartGateInPlanMode(t *testing.T) {
	_, err := buildAgentCommand(
		&cliflags.Config{Agent: "codex", DryRun: true, PlanMode: new(true)},
		Request{Agent: "codex", LaunchMode: agent.ModePlan, AgentStartGate: "gate"},
		"fanout-go",
	)
	if err == nil || !strings.Contains(err.Error(), "agent start gate is not supported in Codex Plan Mode") {
		t.Fatalf("buildAgentCommand() error = %v, want start-gate rejection", err)
	}
}

func TestBuildAgentCommandPinsFanoutBinaryForLiveModes(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := "FANOUT_BIN=" + agent.ShellQuote(executable) + " "

	for _, tc := range []struct {
		name string
		cfg  *cliflags.Config
		req  Request
	}{
		{
			name: "normal agent",
			cfg:  &cliflags.Config{Agent: "claude"},
			req:  Request{Agent: "claude", Prompt: "review"},
		},
		{
			name: "Codex Plan Mode",
			cfg:  &cliflags.Config{Agent: "codex", PlanMode: new(true)},
			req: Request{
				Agent:               "codex",
				Prompt:              "plan",
				LaunchMode:          agent.ModePlan,
				CodexPlanStatusPath: "/tmp/status.json",
			},
		},
		{
			name: "Codex team TUI",
			cfg:  &cliflags.Config{Agent: "codex"},
			req: Request{
				ParentRef:           "100",
				Number:              501,
				Agent:               "codex",
				Prompt:              "work",
				CodexTeamMode:       true,
				CodexTeamStatusPath: "/tmp/status.json",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installFakeExecutable(t, tc.req.Agent)
			got, buildErr := buildAgentCommand(tc.cfg, tc.req, "fanout-go")
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			if !strings.HasPrefix(got, wantPrefix) {
				t.Fatalf("buildAgentCommand() = %q, want prefix %q", got, wantPrefix)
			}
		})
	}
}

func TestParentDisplay(t *testing.T) {
	tests := []struct {
		name   string
		parent string
		want   string
	}{
		{name: "numeric issue gets a hash prefix", parent: "123", want: "#123"},
		{name: "plan parent passes through", parent: "plan:launch-plan", want: "plan:launch-plan"},
		{name: "manual parent shows @manual", parent: ManualParentRef, want: "@manual"},
		{name: "watch parent shows @watch", parent: WatchParentRef, want: "@watch"},
		{name: "project url is stripped to its path", parent: "https://github.com/orgs/octo/projects/3", want: "orgs/octo/projects/3"},
		{name: "empty parent stays empty", parent: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parentDisplay(tt.parent); got != tt.want {
				t.Errorf("parentDisplay(%q) = %q, want %q", tt.parent, got, tt.want)
			}
		})
	}
}

func TestPaneBorderLabel(t *testing.T) {
	cases := []struct {
		name string
		req  Request
		want string
	}{
		{
			name: "issue child uses slug",
			req:  Request{ParentRef: "123", Slug: "fix-login-bug-123"},
			want: "#123 · fix-login-bug-123",
		},
		{
			name: "display name override wins over slug",
			req:  Request{ParentRef: "123", Slug: "fix-login-bug-123", DisplayNameOverride: "Login fix"},
			want: "#123 · Login fix",
		},
		{
			name: "plan parent passes through",
			req:  Request{ParentRef: "plan:my-feature", Slug: "task-slug"},
			want: "plan:my-feature · task-slug",
		},
		{
			name: "manual parent passes through",
			req:  Request{ParentRef: ManualParentRef, Slug: "scratch"},
			want: "@manual · scratch",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := paneBorderLabel(tc.req); got != tc.want {
				t.Errorf("paneBorderLabel() = %q, want %q", got, tc.want)
			}
		})
	}
}

// installFakeExecutable mirrors cmd/fanout's test helper: a no-op executable
// on PATH so agent validation passes.
func installFakeExecutable(t *testing.T, name string) {
	t.Helper()
	binDir := t.TempDir()
	path := filepath.Join(binDir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nif [ \"${1:-}\" = \"--version\" ]; then printf '2.1.207 (Claude Code)\\n'; fi\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func installClaudeVersionExecutable(t *testing.T, version string) {
	t.Helper()
	binDir := t.TempDir()
	path := filepath.Join(binDir, "claude")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' %s\n", agent.ShellQuote(version))
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// stubCloseFreshPane replaces the shell lane's tmux-only rollback close. The
// agent lanes go through the backend's FreshCloser capability instead.
func stubCloseFreshPane(t *testing.T, fn func(string) error) {
	t.Helper()
	original := closeFreshPane
	closeFreshPane = fn
	t.Cleanup(func() { closeFreshPane = original })
}

func installFakeTmux(t *testing.T, paneID string) {
	t.Helper()
	binDir := t.TempDir()
	path := filepath.Join(binDir, "tmux")
	script := "#!/bin/sh\nif [ -n \"$TMUX_CALLS\" ]; then\n  printf '%s\\n' \"$*\" >> \"$TMUX_CALLS\"\nfi\nif [ \"$1\" = \"split-window\" ]; then\n  printf '%s\\n' '" + paneID + "'\nfi\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func gitCmdTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

// TestPlanPaneIssueNum pins the coordinator-row issue link the fan-out dedupe
// relies on: only this lane's own PlanIssueSlug under the manual parent parses;
// plan task rows and issue-like slugs elsewhere never link here.
func TestPlanPaneIssueNum(t *testing.T) {
	tests := []struct {
		name string
		pane state.Pane
		want int
		ok   bool
	}{
		{name: "issue coordinator slug parses", pane: state.Pane{Parent: ManualParentRef, Slug: PlanIssueSlug(123, -4)}, want: 123, ok: true},
		{name: "prompt coordinator slug is not an issue link", pane: state.Pane{Parent: ManualParentRef, Slug: "plan-prompt-4"}, ok: false},
		{name: "missing launch suffix is rejected", pane: state.Pane{Parent: ManualParentRef, Slug: "plan-issue-123"}, ok: false},
		{name: "non-numeric issue segment is rejected", pane: state.Pane{Parent: ManualParentRef, Slug: "plan-issue-abc-1"}, ok: false},
		{name: "empty issue segment is rejected", pane: state.Pane{Parent: ManualParentRef, Slug: "plan-issue--1"}, ok: false},
		{name: "plan task rows never link through this parser", pane: state.Pane{Parent: "plan:issue-474-add-search", Slug: "issue-474-add-search-base"}, ok: false},
		// A work pane whose generated slug happens to start with plan-issue-
		// (issue #999 titled "Plan issue 123 migration") must not alias #123.
		{name: "non-manual pane slug is never parsed", pane: state.Pane{Parent: "700", IssueNum: 999, Slug: "plan-issue-123-migration"}, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := PlanPaneIssueNum(tt.pane)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("PlanPaneIssueNum(%+v) = %d, %v, want %d, %v", tt.pane, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestPlanRuntimeParentRefUsesOnlyDeclaredIssueSource(t *testing.T) {
	tests := []struct {
		name   string
		slug   string
		source string
		want   string
	}{
		{name: "declared issue", slug: "launch-plan", source: "issue #425", want: "425"},
		{name: "issue-like slug without declaration", slug: "issue-425-migration", source: "path-or-conversation-label", want: "plan:issue-425-migration"},
		{name: "malformed issue declaration", slug: "launch-plan", source: "issue 425", want: "plan:launch-plan"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PlanRuntimeParentRef(tt.slug, tt.source); got != tt.want {
				t.Fatalf("PlanRuntimeParentRef(%q, %q) = %q, want %q", tt.slug, tt.source, got, tt.want)
			}
		})
	}
}

func TestOrchestratorIssueSlug(t *testing.T) {
	tests := []struct {
		name     string
		issueNum int
		number   int
		want     string
	}{
		{name: "formats positive pane number", issueNum: 123, number: 4, want: "orchestrator-issue-123-4"},
		{name: "normalizes negative pane number", issueNum: 123, number: -4, want: "orchestrator-issue-123-4"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OrchestratorIssueSlug(tt.issueNum, tt.number); got != tt.want {
				t.Fatalf("OrchestratorIssueSlug(%d, %d) = %q, want %q", tt.issueNum, tt.number, got, tt.want)
			}
		})
	}
}

func TestOrchestratorPaneIssueNum(t *testing.T) {
	tests := []struct {
		name       string
		pane       state.Pane
		want       int
		ok         bool
		planParser bool
	}{
		{name: "orchestrator slug parses", pane: state.Pane{Parent: ManualParentRef, Slug: OrchestratorIssueSlug(123, -1)}, want: 123, ok: true},
		{name: "numeric parent is rejected", pane: state.Pane{Parent: "500", Slug: "orchestrator-issue-123-1"}, ok: false},
		{name: "watch parent is rejected", pane: state.Pane{Parent: "@watch", Slug: "orchestrator-issue-123-1"}, ok: false},
		{name: "plan slug is distinct", pane: state.Pane{Parent: ManualParentRef, Slug: "plan-issue-123-1"}, ok: false, planParser: true},
		{name: "longer issue number does not alias", pane: state.Pane{Parent: ManualParentRef, Slug: "orchestrator-issue-1234-1"}, want: 1234, ok: true},
		{name: "non-numeric issue segment is rejected", pane: state.Pane{Parent: ManualParentRef, Slug: "orchestrator-issue-abc-1"}, ok: false},
		{name: "non-numeric pane segment is rejected", pane: state.Pane{Parent: ManualParentRef, Slug: "orchestrator-issue-123-codex-a1"}, ok: false},
		{name: "empty pane segment is rejected", pane: state.Pane{Parent: ManualParentRef, Slug: "orchestrator-issue-123-"}, ok: false},
		{name: "extra pane segment is rejected", pane: state.Pane{Parent: ManualParentRef, Slug: "orchestrator-issue-123-1-extra"}, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := OrchestratorPaneIssueNum(tt.pane)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("OrchestratorPaneIssueNum(%+v) = %d, %v, want %d, %v", tt.pane, got, ok, tt.want, tt.ok)
			}

			_, planOK := PlanPaneIssueNum(tt.pane)
			if planOK != tt.planParser {
				t.Fatalf("PlanPaneIssueNum(%+v) ok = %v, want %v", tt.pane, planOK, tt.planParser)
			}
		})
	}
}

func TestKillAttachedPaneIgnoresEmptyPaneID(t *testing.T) {
	binDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "tmux-called")
	tmuxPath := filepath.Join(binDir, "tmux")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$TMUX_CALLS\"\n"
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_CALLS", marker)

	if err := KillAttachedPane(nil, "%caller", "", ""); err != nil {
		t.Fatalf("KillAttachedPane() error = %v, want nil", err)
	}

	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("tmux was called for an empty pane ID: %v", err)
	}
}

// TestPlanLinkedIssueNums pins the fanned-set contribution: coordinator rows
// link through their slug, plan task rows only through a saved spec whose
// plan.source declares the issue — an issue-like plan slug alone never links,
// so a hand-authored "issue-123-migration" plan cannot block issue #123.
func TestPlanLinkedIssueNums(t *testing.T) {
	root := t.TempDir()
	writeSpec := func(t *testing.T, slug, source string) {
		t.Helper()
		dir := filepath.Join(root, ".fanout", "plans")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf(`{"version":1,"plan":{"slug":%q,"title":"t","source":%q},"tasks":[]}`, slug, source)
		if err := os.WriteFile(filepath.Join(dir, slug+".json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeSpec(t, "issue-474-add-search", "issue #474")
	writeSpec(t, "issue-123-migration", "path-or-conversation-label")
	writeSpec(t, "launch-plan", "issue #99")

	store := state.Store{Panes: []state.Pane{
		{Parent: ManualParentRef, IssueNum: -1, Slug: PlanIssueSlug(123, -1)},
		{Parent: "plan:issue-474-add-search", TaskID: "base", Slug: "issue-474-add-search-base"},
		// Issue-like slug whose spec declares no issue source: never links.
		{Parent: "plan:issue-123-migration", TaskID: "move", Slug: "issue-123-migration-move"},
		// Neutral slug whose spec declares an issue source: links by declaration.
		{Parent: "plan:launch-plan", TaskID: "base", Slug: "launch-plan-base"},
		// Plan rows without a saved spec: no link.
		{Parent: "plan:issue-555-ghost", TaskID: "base", Slug: "issue-555-ghost-base"},
		{Parent: "700", IssueNum: 701, Slug: "child-701"},
	}}
	got := PlanLinkedIssueNums(root, store)
	want := map[int]bool{123: true, 474: true, 99: true}
	if len(got) != len(want) || !got[123] || !got[474] || !got[99] {
		t.Fatalf("PlanLinkedIssueNums(root, store) = %v, want %v", got, want)
	}
}
