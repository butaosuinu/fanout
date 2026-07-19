package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
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
