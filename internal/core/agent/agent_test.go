package agent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
)

// TestClaudeHookSettingsJSONPinsAgentStateHooks pins the exact inline
// --settings JSON injected into claude launches: key order (dry-run goldens
// embed the string verbatim), the event -> @fanout_agent_state mapping, the
// best-effort `|| true` suffix on every hook command, and the Notification
// stdin filter that keeps the ~60s idle_prompt reminder from flipping an idle
// pane to blocked.
func TestClaudeHookSettingsJSONPinsAgentStateHooks(t *testing.T) {
	hook := func(command string) string {
		return `[{"hooks":[{"type":"command","command":"` + command + `"}]}]`
	}
	stateHook := func(state string) string {
		return hook(`tmux set-option -p -t \"$TMUX_PANE\" @fanout_agent_state ` + state + ` 2>/dev/null || true`)
	}
	blockedHook := hook(`grep -Eq '\"notification_type\"[[:space:]]*:[[:space:]]*\"(permission_prompt|agent_needs_input|elicitation_dialog)\"' - && tmux set-option -p -t \"$TMUX_PANE\" @fanout_agent_state blocked 2>/dev/null || true`)
	want := `{"hooks":{` +
		`"UserPromptSubmit":` + stateHook("working") + `,` +
		`"PreToolUse":` + stateHook("working") + `,` +
		`"PostToolUse":` + stateHook("working") + `,` +
		`"Notification":` + blockedHook + `,` +
		`"Stop":` + stateHook("idle") + `}}`
	if claudeHookSettingsJSON != want {
		t.Fatalf("claudeHookSettingsJSON = %q, want %q", claudeHookSettingsJSON, want)
	}
	if !json.Valid([]byte(claudeHookSettingsJSON)) {
		t.Fatalf("claudeHookSettingsJSON is not valid JSON: %q", claudeHookSettingsJSON)
	}
}

func TestBuildClaudeHookSettingsJSONKeepsSessionEndSynchronous(t *testing.T) {
	settingsJSON := BuildClaudeHookSettingsJSON(ClaudeHookCommands{
		Working: "emit working", Blocked: "emit blocked", Idle: "emit idle", Done: "emit done",
		DoneMatcher: "logout|other", DoneTimeoutSeconds: 15, Background: true,
	})
	var settings claudeHookSettings
	if err := json.Unmarshal([]byte(settingsJSON), &settings); err != nil {
		t.Fatal(err)
	}
	want := []struct {
		name       string
		matchers   []claudeHookMatcher
		command    string
		background bool
		matcher    string
		timeout    int
	}{
		{name: "UserPromptSubmit", matchers: settings.Hooks.UserPromptSubmit, command: "emit working", background: true},
		{name: "PreToolUse", matchers: settings.Hooks.PreToolUse, command: "emit working", background: true},
		{name: "PostToolUse", matchers: settings.Hooks.PostToolUse, command: "emit working", background: true},
		{name: "Notification", matchers: settings.Hooks.Notification, command: "emit blocked", background: true},
		{name: "Stop", matchers: settings.Hooks.Stop, command: "emit idle", background: true},
		{
			name: "SessionEnd", matchers: settings.Hooks.SessionEnd, command: "emit done",
			matcher: "logout|other", timeout: 15,
		},
	}
	for _, event := range want {
		if len(event.matchers) != 1 || len(event.matchers[0].Hooks) != 1 {
			t.Fatalf("%s hooks = %#v", event.name, event.matchers)
		}
		command := event.matchers[0].Hooks[0].Command
		if !strings.Contains(command, event.command+" || true") ||
			strings.Contains(command, "} &") != event.background {
			t.Fatalf("%s command = %q", event.name, command)
		}
		if out, err := exec.Command("sh", "-n", "-c", command).CombinedOutput(); err != nil {
			t.Fatalf("%s command is not valid POSIX shell: %v: %s", event.name, err, out)
		}
		if event.matchers[0].Matcher != event.matcher || event.matchers[0].Hooks[0].Timeout != event.timeout {
			t.Fatalf("%s matcher = %#v", event.name, event.matchers[0])
		}
	}
	blocked := settings.Hooks.Notification[0].Hooks[0].Command
	for notificationType := range strings.SplitSeq(blockedNotificationTypes, "|") {
		if !strings.Contains(blocked, notificationType) {
			t.Fatalf("Notification command %q lacks %q", blocked, notificationType)
		}
	}
}

func TestClaudeStateCommandAllocatesSequenceBeforeBackgrounding(t *testing.T) {
	got := claudeStateCommand("emit working", "next sequence", true)
	want := `__fanout_event_sequence="$(next sequence)" && { { emit working "$__fanout_event_sequence" || true; } & }`
	if got != want {
		t.Fatalf("claudeStateCommand() = %q, want %q", got, want)
	}
	if out, err := exec.Command("sh", "-n", "-c", got).CombinedOutput(); err != nil {
		t.Fatalf("sequenced hook is not valid POSIX shell: %v: %s", err, out)
	}
}

func TestBuildCommandQuotesPrompt(t *testing.T) {
	got, err := BuildCommand("claude", "[fanout #1] it's ready")
	if err != nil {
		t.Fatal(err)
	}
	want := "claude --settings " + ShellQuote(claudeHookSettingsJSON) + " '[fanout #1] it'\\''s ready'"
	if got != want {
		t.Fatalf("BuildCommand() = %q, want %q", got, want)
	}
}

// TestBuildCommandCodexStaysBare guarantees hook injection is claude-only:
// codex has no launch-time hook mechanism, so its command must stay unchanged.
func TestBuildCommandCodexStaysBare(t *testing.T) {
	got, err := BuildCommand("codex", "[fanout #1] go")
	if err != nil {
		t.Fatal(err)
	}
	want := "codex '[fanout #1] go'"
	if got != want {
		t.Fatalf("BuildCommand(codex) = %q, want %q", got, want)
	}
}

// TestBuildCommandOpencodeRoutesPromptThroughFlag pins the opencode launch
// contract verified in the #357 spike: opencode's positional argument is a
// project path, so the prompt must travel as the --prompt flag value.
func TestBuildCommandOpencodeRoutesPromptThroughFlag(t *testing.T) {
	got, err := BuildCommand("opencode", "[fanout #1] go")
	if err != nil {
		t.Fatal(err)
	}
	want := "opencode --prompt '[fanout #1] go'"
	if got != want {
		t.Fatalf("BuildCommand(opencode) = %q, want %q", got, want)
	}
}

// TestBuildCommandOpencodePromptFlagQuotesValue ensures the flag form keeps
// the prompt a single shell token even with shell metacharacters.
func TestBuildCommandOpencodePromptFlagQuotesValue(t *testing.T) {
	got, err := BuildCommand("opencode", "[fanout #1] it's ready")
	if err != nil {
		t.Fatal(err)
	}
	want := "opencode --prompt '[fanout #1] it'\\''s ready'"
	if got != want {
		t.Fatalf("BuildCommand(opencode) = %q, want %q", got, want)
	}
}

// TestBuildResumeCommandOpencodeOmitsPromptFlag pins resume without a prompt:
// the --prompt flag must not appear (it would swallow --continue as its
// value), leaving the bare --continue resume contract.
func TestBuildResumeCommandOpencodeOmitsPromptFlag(t *testing.T) {
	got, err := BuildResumeCommand("opencode")
	if err != nil {
		t.Fatal(err)
	}
	if got != "opencode --continue" {
		t.Fatalf("BuildResumeCommand(opencode) = %q, want opencode --continue", got)
	}
}

func TestBuildCommandForBackendOpencodeStaysBackendAgnostic(t *testing.T) {
	got, err := BuildCommandForBackend("opencode", "prompt", backend.Herdr)
	if err != nil {
		t.Fatal(err)
	}
	if got != "opencode --prompt prompt" {
		t.Fatalf("BuildCommandForBackend(opencode, herdr) = %q, want opencode --prompt prompt", got)
	}
}

func TestBuildCommandForBackendOmitsTmuxHooksForHerdr(t *testing.T) {
	got, err := BuildCommandForBackend("claude", "[fanout #1] go", backend.Herdr)
	if err != nil {
		t.Fatal(err)
	}
	want := "claude '[fanout #1] go'"
	if got != want {
		t.Fatalf("BuildCommandForBackend(claude, herdr) = %q, want %q", got, want)
	}
}

func TestBuildCommandForBackendKeepsTmuxHooks(t *testing.T) {
	got, err := BuildCommandForBackend("claude", "prompt", backend.Tmux)
	if err != nil {
		t.Fatal(err)
	}
	want := "claude --settings " + ShellQuote(claudeHookSettingsJSON) + " prompt"
	if got != want {
		t.Fatalf("BuildCommandForBackend(claude, tmux) = %q, want %q", got, want)
	}
}

func TestBuildResolvedLaunchSpecWithBackendArgsKeepsInjectionBeforeModeAndPrompt(t *testing.T) {
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	spec, err := BuildResolvedLaunchSpecWithBackendArgs(
		"claude", "prompt", backend.Herdr, ModePlan,
		[]string{"--settings", `{"hooks":{}}`},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--settings", `{"hooks":{}}`, "--permission-mode", "plan", "prompt"}
	if !slices.Equal(spec.Args, want) {
		t.Fatalf("Args = %#v, want %#v", spec.Args, want)
	}
}

func TestBuildCommandWithModeDefaultsToTmux(t *testing.T) {
	got, err := BuildCommandWithMode("claude", "prompt", ModePlan)
	if err != nil {
		t.Fatal(err)
	}
	want := "claude --settings " + ShellQuote(claudeHookSettingsJSON) + " --permission-mode plan prompt"
	if got != want {
		t.Fatalf("BuildCommandWithMode() = %q, want %q", got, want)
	}
}

func TestBuildCommandForBackendWithMode(t *testing.T) {
	claudeSettings := "--settings " + ShellQuote(claudeHookSettingsJSON) + " "
	for _, tc := range []struct {
		name    string
		agent   string
		mode    LaunchMode
		backend backend.Name
		want    string
	}{
		{name: "claude build tmux", agent: "claude", mode: ModeBuild, backend: backend.Tmux, want: "claude " + claudeSettings + "--permission-mode auto prompt"},
		{name: "claude plan tmux", agent: "claude", mode: ModePlan, backend: backend.Tmux, want: "claude " + claudeSettings + "--permission-mode plan prompt"},
		{name: "claude build herdr", agent: "claude", mode: ModeBuild, backend: backend.Herdr, want: "claude --permission-mode auto prompt"},
		{name: "claude plan herdr", agent: "claude", mode: ModePlan, backend: backend.Herdr, want: "claude --permission-mode plan prompt"},
		{name: "codex build tmux", agent: "codex", mode: ModeBuild, backend: backend.Tmux, want: "codex prompt"},
		{name: "codex plan tmux", agent: "codex", mode: ModePlan, backend: backend.Tmux, want: "codex prompt"},
		{name: "codex build herdr", agent: "codex", mode: ModeBuild, backend: backend.Herdr, want: "codex prompt"},
		{name: "codex plan herdr", agent: "codex", mode: ModePlan, backend: backend.Herdr, want: "codex prompt"},
		{name: "opencode build tmux", agent: "opencode", mode: ModeBuild, backend: backend.Tmux, want: "opencode --agent build --prompt prompt"},
		{name: "opencode plan tmux", agent: "opencode", mode: ModePlan, backend: backend.Tmux, want: "opencode --agent plan --prompt prompt"},
		{name: "opencode build herdr", agent: "opencode", mode: ModeBuild, backend: backend.Herdr, want: "opencode --agent build --prompt prompt"},
		{name: "opencode plan herdr", agent: "opencode", mode: ModePlan, backend: backend.Herdr, want: "opencode --agent plan --prompt prompt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BuildCommandForBackendWithMode(tc.agent, "prompt", tc.backend, tc.mode)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("BuildCommandForBackendWithMode() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildCommandForBackendRejectsUnknownBackend(t *testing.T) {
	if _, err := BuildCommandForBackend("claude", "prompt", backend.Name("unknown")); err == nil {
		t.Fatal("BuildCommandForBackend() succeeded with an unknown backend")
	}
}

func TestBuildCommandRejectsUnknownAgent(t *testing.T) {
	if _, err := BuildCommand("bogus", "prompt"); err == nil {
		t.Fatal("expected unknown agent error")
	}
}

func TestBuildResumeCommandUsesAgentResumeArgs(t *testing.T) {
	got, err := BuildResumeCommand("codex")
	if err != nil {
		t.Fatal(err)
	}
	if got != "codex resume --last" {
		t.Fatalf("BuildResumeCommand(codex) = %q, want codex resume --last", got)
	}

	got, err = BuildResumeCommand("claude")
	if err != nil {
		t.Fatal(err)
	}
	want := "claude --settings " + ShellQuote(claudeHookSettingsJSON) + " --continue"
	if got != want {
		t.Fatalf("BuildResumeCommand(claude) = %q, want %q", got, want)
	}
}

func TestBuildResumeCommandForBackendOmitsTmuxHooksForHerdr(t *testing.T) {
	got, err := BuildResumeCommandForBackend("claude", backend.Herdr)
	if err != nil {
		t.Fatal(err)
	}
	if got != "claude --continue" {
		t.Fatalf("BuildResumeCommandForBackend(claude, herdr) = %q, want claude --continue", got)
	}
}

func TestBuildResumeCommandForBackendOmitsModeArgs(t *testing.T) {
	claudeSettings := "--settings " + ShellQuote(claudeHookSettingsJSON) + " "
	for _, tc := range []struct {
		name    string
		agent   string
		backend backend.Name
		want    string
	}{
		{name: "claude tmux", agent: "claude", backend: backend.Tmux, want: "claude " + claudeSettings + "--continue"},
		{name: "claude herdr", agent: "claude", backend: backend.Herdr, want: "claude --continue"},
		{name: "codex tmux", agent: "codex", backend: backend.Tmux, want: "codex resume --last"},
		{name: "codex herdr", agent: "codex", backend: backend.Herdr, want: "codex resume --last"},
		{name: "opencode tmux", agent: "opencode", backend: backend.Tmux, want: "opencode --continue"},
		{name: "opencode herdr", agent: "opencode", backend: backend.Herdr, want: "opencode --continue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BuildResumeCommandForBackend(tc.agent, tc.backend)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("BuildResumeCommandForBackend() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildResolvedCommandUsesAbsoluteExecutablePathAndPathPrefix(t *testing.T) {
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin with space")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(binDir, "claude")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, err := BuildResolvedCommand("claude", "prompt")
	if err != nil {
		t.Fatal(err)
	}
	want := "PATH=" + ShellQuote(os.Getenv("PATH")) + " " + ShellQuote(exe) + " --settings " + ShellQuote(claudeHookSettingsJSON) + " prompt"
	if got != want {
		t.Fatalf("BuildResolvedCommand() = %q, want %q", got, want)
	}
}

func TestBuildResolvedCommandForBackendOmitsTmuxHooksForHerdr(t *testing.T) {
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin with space")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(binDir, "claude")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, err := BuildResolvedCommandForBackend("claude", "prompt", backend.Herdr)
	if err != nil {
		t.Fatal(err)
	}
	want := "PATH=" + ShellQuote(os.Getenv("PATH")) + " " + ShellQuote(exe) + " prompt"
	if got != want {
		t.Fatalf("BuildResolvedCommandForBackend() = %q, want %q", got, want)
	}
}

func TestBuildResolvedCommandWithModeUsesAbsoluteExecutablePathAndModeArgs(t *testing.T) {
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin with space")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(binDir, "opencode")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, err := BuildResolvedCommandWithMode("opencode", "prompt", ModePlan)
	if err != nil {
		t.Fatal(err)
	}
	want := "PATH=" + ShellQuote(os.Getenv("PATH")) + " " + ShellQuote(exe) + " --agent plan --prompt prompt"
	if got != want {
		t.Fatalf("BuildResolvedCommandWithMode() = %q, want %q", got, want)
	}
}

func TestBuildResolvedResumeCommandUsesAbsoluteExecutablePathAndPathPrefix(t *testing.T) {
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin with space")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(binDir, "codex")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, err := BuildResolvedResumeCommand("codex")
	if err != nil {
		t.Fatal(err)
	}
	want := "PATH=" + ShellQuote(os.Getenv("PATH")) + " " + ShellQuote(exe) + " resume --last"
	if got != want {
		t.Fatalf("BuildResolvedResumeCommand() = %q, want %q", got, want)
	}
}

func TestBuildResolvedResumeCommandForBackendOmitsTmuxHooksForHerdr(t *testing.T) {
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin with space")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(binDir, "claude")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, err := BuildResolvedResumeCommandForBackend("claude", backend.Herdr)
	if err != nil {
		t.Fatal(err)
	}
	want := "PATH=" + ShellQuote(os.Getenv("PATH")) + " " + ShellQuote(exe) + " --continue"
	if got != want {
		t.Fatalf("BuildResolvedResumeCommandForBackend() = %q, want %q", got, want)
	}
}

func TestWithFanoutBinQuotesExecutablePath(t *testing.T) {
	t.Parallel()
	got := WithFanoutBin("PATH=/bin codex", "/tmp/fanout build/fanout-go")
	want := "FANOUT_BIN='/tmp/fanout build/fanout-go' PATH=/bin codex"
	if got != want {
		t.Fatalf("WithFanoutBin() = %q, want %q", got, want)
	}
}

func TestPaneStateRefined(t *testing.T) {
	for _, tc := range []struct {
		name  string
		agent string
		want  bool
	}{
		{name: "claude refines via tmux hooks", agent: "claude", want: true},
		{name: "codex refines via plan controller and team bridge", agent: "codex", want: true},
		{name: "opencode stays at the wrapper states", agent: "opencode", want: false},
		{name: "unknown agent fails safe", agent: "future-agent", want: false},
		{name: "empty agent fails safe", agent: "", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PaneStateRefined(tc.agent); got != tc.want {
				t.Fatalf("PaneStateRefined(%q) = %v, want %v", tc.agent, got, tc.want)
			}
		})
	}
}
