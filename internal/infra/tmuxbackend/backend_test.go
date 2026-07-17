package tmuxbackend_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/tmuxbackend"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
)

func installTmuxShim(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	tmuxPath := filepath.Join(dir, "tmux")
	script := "#!/bin/sh\n" +
		"printf 'CALL\\n' >> \"$TMUXBACKEND_CALLS\"\n" +
		"printf '%s\\n' \"$@\" >> \"$TMUXBACKEND_CALLS\"\n" + body
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUXBACKEND_CALLS", logPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func readCalls(t *testing.T, path string) [][]string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	for _, block := range strings.Split(strings.TrimSpace(string(body)), "CALL\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		calls = append(calls, strings.Split(block, "\n"))
	}
	return calls
}

func TestNameAndCheckAvailable(t *testing.T) {
	logPath := installTmuxShim(t, `
if [ "$1" = "-V" ]; then
  printf 'tmux 3.6a\n'
fi
`)
	b := tmuxbackend.New()
	if got := b.Name(); got != backend.Tmux {
		t.Fatalf("Name() = %q, want %q", got, backend.Tmux)
	}
	if err := b.CheckAvailable(); err != nil {
		t.Fatalf("CheckAvailable() failed: %v", err)
	}
	if got, want := readCalls(t, logPath), [][]string{{"-V"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tmux calls = %#v, want %#v", got, want)
	}
}

func TestLaunchLocksGateAndPrefixesPaneCommand(t *testing.T) {
	logPath := installTmuxShim(t, `
if [ "$1" = "split-window" ]; then
  printf '%%42\n'
fi
`)
	b := tmuxbackend.New()
	req := backend.LaunchRequest{
		Target:       "%1",
		WorktreePath: "/tmp/work tree",
		Command:      "codex 'inspect API'",
		StartGate:    "fanout-start-42",
	}
	got, err := b.Launch(req)
	if err != nil {
		t.Fatalf("Launch() failed: %v", err)
	}
	want := backend.PaneRef{Backend: backend.Tmux, Pane: "%42"}
	if got != want {
		t.Fatalf("Launch() = %#v, want %#v", got, want)
	}

	gatedCommand := tmuxrun.WaitForLockCommand(req.StartGate) + " && " + req.Command
	wantCalls := [][]string{
		{"wait-for", "-L", req.StartGate},
		{"split-window", "-t", req.Target, "-d", "-h", "-P", "-F", "#{pane_id}", "-c", req.WorktreePath, tmuxrun.BuildPaneLaunchCommand(gatedCommand)},
	}
	if calls := readCalls(t, logPath); !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("tmux calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestLaunchUnlocksGateAfterSplitFailure(t *testing.T) {
	logPath := installTmuxShim(t, `
if [ "$1" = "split-window" ]; then
  exit 1
fi
`)
	b := tmuxbackend.New()
	_, err := b.Launch(backend.LaunchRequest{
		Target:       "%1",
		WorktreePath: "/tmp/worktree",
		Command:      "codex prompt",
		StartGate:    "fanout-start-fail",
	})
	if err == nil || !strings.Contains(err.Error(), "tmux split-window") {
		t.Fatalf("Launch() error = %v, want split failure", err)
	}
	calls := readCalls(t, logPath)
	if len(calls) != 3 {
		t.Fatalf("tmux calls = %#v, want lock, split, unlock", calls)
	}
	if got, want := calls[0], []string{"wait-for", "-L", "fanout-start-fail"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lock call = %#v, want %#v", got, want)
	}
	if got, want := calls[2], []string{"wait-for", "-U", "fanout-start-fail"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unlock call = %#v, want %#v", got, want)
	}
}

func TestReleaseStartGate(t *testing.T) {
	logPath := installTmuxShim(t, "")
	if err := tmuxbackend.New().ReleaseStartGate("fanout-start-7"); err != nil {
		t.Fatalf("ReleaseStartGate() failed: %v", err)
	}
	if got, want := readCalls(t, logPath), [][]string{{"wait-for", "-U", "fanout-start-7"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tmux calls = %#v, want %#v", got, want)
	}
}

func TestListLiveMapsTmuxMetadata(t *testing.T) {
	installTmuxShim(t, `
case "$4" in
  *pane_current_path*) printf '%%7\t/tmp/repo\n%%8\t/tmp/other\n' ;;
  *pane_title*) printf '%%7\tworker\n%%8\tunknown-state\n' ;;
  *@fanout_agent_state*) printf '%%7\tworking\n%%8\tbackend-native\n' ;;
  *@fanout_shell_key*) printf '%%7\tshell-7\n%%8\t\n' ;;
  *@fanout_project_root*) printf '%%7\t/tmp/repo\n%%8\t/tmp/other\n' ;;
  *@fanout_worktree_path*) printf '%%7\t/tmp/repo/.fanout/worktrees/7\n%%8\t/tmp/other/.fanout/worktrees/8\n' ;;
  *@fanout_role*) printf '%%7\tagent\n%%8\tconsole\n' ;;
  *session_id*) printf '%%7\t$1\n%%8\t$2\n' ;;
esac
`)
	got, err := tmuxbackend.New().ListLive()
	if err != nil {
		t.Fatalf("ListLive() failed: %v", err)
	}
	want := []backend.LivePane{
		{
			Ref:              backend.PaneRef{Backend: backend.Tmux, Pane: "%7"},
			CurrentPath:      "/tmp/repo",
			Title:            "worker",
			AgentState:       backend.AgentWorking,
			NativeAgentState: "working",
			ShellKey:         "shell-7",
			ProjectRoot:      "/tmp/repo",
			WorktreePath:     "/tmp/repo/.fanout/worktrees/7",
			Role:             "agent",
			SessionID:        "$1",
		},
		{
			Ref:              backend.PaneRef{Backend: backend.Tmux, Pane: "%8"},
			CurrentPath:      "/tmp/other",
			Title:            "unknown-state",
			NativeAgentState: "backend-native",
			ProjectRoot:      "/tmp/other",
			WorktreePath:     "/tmp/other/.fanout/worktrees/8",
			Role:             "console",
			SessionID:        "$2",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListLive() = %#v, want %#v", got, want)
	}
}

func TestPaneOperationsDelegateToTmuxrun(t *testing.T) {
	logPath := installTmuxShim(t, `
if [ "$1" = "display-message" ]; then
  printf '0\n'
fi
if [ "$1" = "capture-pane" ]; then
  printf 'pane output\n'
fi
`)
	b := tmuxbackend.New()
	ref := backend.PaneRef{Backend: backend.Tmux, Pane: "%7"}
	output, err := b.Read(ref, 40)
	if err != nil {
		t.Fatalf("Read() failed: %v", err)
	}
	if output != "pane output\n" {
		t.Fatalf("Read() = %q, want pane output", output)
	}
	if err := b.SendLine(ref, "review message"); err != nil {
		t.Fatalf("SendLine() failed: %v", err)
	}
	if err := b.Focus(ref); err != nil {
		t.Fatalf("Focus() failed: %v", err)
	}
	if err := b.Close(ref); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}
	want := [][]string{
		{"display-message", "-p", "-t", "%7", "#{alternate_on}"},
		{"capture-pane", "-p", "-t", "%7", "-S", "-40"},
		{"send-keys", "-t", "%7", "-l", "--", "review message"},
		{"send-keys", "-t", "%7", "Enter"},
		{"switch-client", "-t", "%7"},
		{"kill-pane", "-t", "%7"},
	}
	if got := readCalls(t, logPath); !reflect.DeepEqual(got, want) {
		t.Fatalf("tmux calls = %#v, want %#v", got, want)
	}
}

func TestPaneOperationsRejectForeignBackend(t *testing.T) {
	b := tmuxbackend.New()
	ref := backend.PaneRef{Backend: backend.Herdr, Workspace: "w1", Pane: "p1"}
	if _, err := b.Read(ref, 10); err == nil || !strings.Contains(err.Error(), "cannot use herdr") {
		t.Fatalf("Read() error = %v, want foreign-backend rejection", err)
	}
	if err := b.SendLine(ref, "message"); err == nil {
		t.Fatal("SendLine() succeeded with a herdr reference")
	}
	if err := b.Focus(ref); err == nil {
		t.Fatal("Focus() succeeded with a herdr reference")
	}
	if err := b.Close(ref); err == nil {
		t.Fatal("Close() succeeded with a herdr reference")
	}
}

func TestPaneOperationsAcceptLegacyEmptyBackendAsTmux(t *testing.T) {
	logPath := installTmuxShim(t, "")
	if err := tmuxbackend.New().Close(backend.PaneRef{Pane: "%9"}); err != nil {
		t.Fatalf("Close() legacy reference failed: %v", err)
	}
	if got, want := readCalls(t, logPath), [][]string{{"kill-pane", "-t", "%9"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tmux calls = %#v, want %#v", got, want)
	}
}
