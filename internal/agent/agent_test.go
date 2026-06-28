package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildCommandQuotesPrompt(t *testing.T) {
	got, err := BuildCommand("claude", "[fanout #1] it's ready")
	if err != nil {
		t.Fatal(err)
	}
	want := "claude '[fanout #1] it'\\''s ready'"
	if got != want {
		t.Fatalf("BuildCommand() = %q, want %q", got, want)
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
	if got != "claude --continue" {
		t.Fatalf("BuildResumeCommand(claude) = %q, want claude --continue", got)
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
	want := "PATH=" + ShellQuote(os.Getenv("PATH")) + " " + ShellQuote(exe) + " prompt"
	if got != want {
		t.Fatalf("BuildResolvedCommand() = %q, want %q", got, want)
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
