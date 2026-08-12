package run

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/log"
	fanoutruntime "github.com/butaosuinu/fanout/internal/infra/runtime"
	"github.com/butaosuinu/fanout/internal/infra/settings"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/tmuxbackend"
)

func TestExecutePlanSleepsBetweenDryRunIssues(t *testing.T) {
	dir := t.TempDir()

	oldSleep := sleepBetweenIssues
	var sleeps []time.Duration
	sleepBetweenIssues = func(d time.Duration) {
		sleeps = append(sleeps, d)
	}
	t.Cleanup(func() { sleepBetweenIssues = oldSleep })

	cfg := &cliflags.Config{
		Agent:        "claude",
		DryRun:       true,
		SleepBetween: 0.25,
	}
	lg := log.NewWith(io.Discard, io.Discard, false)
	info := &fanoutruntime.Info{
		Session:     "test",
		Target:      "%caller",
		ProjectRoot: dir,
	}
	targets := []ghissue.Issue{
		{Number: 1, Title: "one", State: "OPEN", Body: "body"},
		{Number: 2, Title: "two", State: "OPEN", Body: "body"},
	}

	result := executePlan(cfg, lg, info, tmuxbackend.New(), nil, targets, nil, settings.Defaults(), hooks.EmptyConfig(), nil, nil, log.Palette{}, "fanout", nil)

	if result.Created != 2 || result.Failed != 0 {
		t.Fatalf("executePlan result = %+v, want 2 created and 0 failed", result)
	}
	if len(result.CreatedPaneIDs) != 0 {
		t.Fatalf("dry-run CreatedPaneIDs = %v, want empty", result.CreatedPaneIDs)
	}
	if len(sleeps) != 1 {
		t.Fatalf("sleep calls = %d, want 1", len(sleeps))
	}
	if want := 250 * time.Millisecond; sleeps[0] != want {
		t.Fatalf("sleep duration = %s, want %s", sleeps[0], want)
	}
}

func TestEffectiveIssueLaunchConfigUsesResolvedPlanModeWithoutMutatingParsedConfig(t *testing.T) {
	cfg := &cliflags.Config{
		Agent:          "codex",
		AgentOverrides: []cliflags.AgentOverride{{Target: "102", Name: "codex"}},
	}
	resolved := settings.Defaults()
	resolved.ChildPlanMode = true

	got := effectiveIssueLaunchConfig(cfg, resolved)

	if !got.PlanModeEnabled() {
		t.Fatal("effective PlanModeEnabled() = false, want resolved true")
	}
	if cfg.PlanMode != nil {
		t.Fatalf("parsed PlanMode = %v, want nil so rerun hints preserve only explicit flags", cfg.PlanMode)
	}
	if got == cfg {
		t.Fatal("effectiveIssueLaunchConfig() returned the parsed config instead of a copy")
	}
	if !reflect.DeepEqual(got.AgentOverrides, cfg.AgentOverrides) {
		t.Fatalf("AgentOverrides = %v, want shallow-copy value %v", got.AgentOverrides, cfg.AgentOverrides)
	}
}

func TestPrepareIssueLaunchDefersBackendUntilTargetAndAgentValidate(t *testing.T) {
	tests := []struct {
		name      string
		agentName string
		targets   []ghissue.Issue
		wantOK    bool
		wantCalls int
	}{
		{name: "no targets", agentName: "claude", wantOK: true},
		{name: "invalid agent", agentName: "unknown", targets: []ghissue.Issue{{Number: 101}}, wantOK: false},
		{name: "valid target", agentName: "claude", targets: []ghissue.Issue{{Number: 101}}, wantOK: true, wantCalls: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			rt := &Runtime{PrepareBackend: func() error {
				calls++
				return nil
			}}
			cfg := &cliflags.Config{Agent: test.agentName, DryRun: true}
			got := prepareIssueLaunch(cfg, Plan{Targets: test.targets}, rt, state.Store{}, nil, nil, log.NewWith(io.Discard, io.Discard, false))
			if got != test.wantOK || calls != test.wantCalls {
				t.Fatalf("prepareIssueLaunch() = %t, calls %d; want %t, %d", got, calls, test.wantOK, test.wantCalls)
			}
		})
	}
}

func TestPrepareIssueCallbacksForCompletedReplay(t *testing.T) {
	calls := 0
	rt := &Runtime{PrepareBackend: func() error {
		calls++
		return nil
	}}
	after := func(state.Store, panelaunch.StateRecorder, IssueAfterContext) error { return nil }
	if err := prepareIssueCallbacks(rt, nil, after); err != nil || calls != 1 {
		t.Fatalf("prepareIssueCallbacks() error = %v, calls %d; want nil, 1", err, calls)
	}
}

func TestExecutePlanPreservesCreatedPaneIDsOnFailFastError(t *testing.T) {
	repo := t.TempDir()
	gitCmdTest(t, repo, "init", "-b", "main")
	gitCmdTest(t, repo, "config", "user.name", "fanout test")
	gitCmdTest(t, repo, "config", "user.email", "fanout@example.com")
	gitCmdTest(t, repo, "commit", "--allow-empty", "-m", "initial")
	installFakeIssueLaunchCommands(t)

	cfg := &cliflags.Config{
		Agent:      "claude",
		ParentRef:  "100",
		BaseBranch: "main",
		NoRefresh:  true,
	}
	info := &fanoutruntime.Info{Session: "test", Target: "%caller", ProjectRoot: repo}
	targets := []ghissue.Issue{
		{Number: 101, Title: "first", State: "OPEN", Body: "body"},
		{Number: 102, Title: "second", State: "OPEN", Body: "body"},
		{Number: 103, Title: "third", State: "OPEN", Body: "body"},
	}
	failedReq := panelaunch.NewIssueRequest(cfg, repo, targets[2], settings.Defaults(), hooks.EmptyConfig(), false, nil)
	if err := os.MkdirAll(failedReq.Worktree.WorktreePath, 0o755); err != nil {
		t.Fatal(err)
	}

	result := executePlan(
		cfg,
		log.NewWith(io.Discard, io.Discard, false),
		info,
		tmuxbackend.New(),
		nil,
		targets,
		nil,
		settings.Defaults(),
		hooks.EmptyConfig(),
		nil,
		nil,
		log.Palette{},
		"fanout",
		nil,
	)

	if result.Created != 2 || result.Failed != 1 {
		t.Fatalf("executePlan result = %+v, want 2 created and 1 failed", result)
	}
	if want := []int{101, 102}; !reflect.DeepEqual(result.CreatedNums, want) {
		t.Fatalf("CreatedNums = %v, want %v", result.CreatedNums, want)
	}
	if want := []string{"%701", "%702"}; !reflect.DeepEqual(result.CreatedPaneIDs, want) {
		t.Fatalf("CreatedPaneIDs = %v, want %v", result.CreatedPaneIDs, want)
	}
}

func installFakeIssueLaunchCommands(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "tmux-splits")
	t.Setenv("FANOUT_TEST_TMUX_STATE", statePath)
	tmuxScript := `#!/bin/sh
if [ "$1" = "split-window" ]; then
  count=0
  if [ -f "$FANOUT_TEST_TMUX_STATE" ]; then
    count=$(cat "$FANOUT_TEST_TMUX_STATE")
  fi
  count=$((count + 1))
  printf '%s\n' "$count" > "$FANOUT_TEST_TMUX_STATE"
  printf '%%%s\n' "$((700 + count))"
fi
`
	for name, body := range map[string]string{
		"claude": "#!/bin/sh\nexit 0\n",
		"tmux":   tmuxScript,
	} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
