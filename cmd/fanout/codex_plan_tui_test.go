package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/log"
)

func TestParseCodexPlanTUIArgsAllowsResumeThreadWithoutPrompt(t *testing.T) {
	var stdout, stderr bytes.Buffer
	cfg, help, code := parseCodexPlanTUIArgs([]string{
		"--codex", "/bin/codex",
		"--resume-thread-id", "thread-1",
		"--resume-session-id", "session-1",
		"--status-file", "/tmp/status.json",
	}, log.NewWith(&stdout, &stderr, false))

	if help {
		t.Fatalf("parseCodexPlanTUIArgs help = true, want false")
	}

	if code != exitcode.OK {
		t.Fatalf("parseCodexPlanTUIArgs code = %v, stderr = %q", code, stderr.String())
	}
	if cfg.ResumeThreadID != "thread-1" || cfg.ResumeSessionID != "session-1" || cfg.Prompt != "" {
		t.Fatalf("cfg = %+v, want resume thread without prompt", cfg)
	}
}

func TestParseCodexPlanTUIArgsStillRequiresPromptOrResumeThread(t *testing.T) {
	var stdout, stderr bytes.Buffer
	_, _, code := parseCodexPlanTUIArgs([]string{
		"--codex", "/bin/codex",
		"--status-file", "/tmp/status.json",
	}, log.NewWith(&stdout, &stderr, false))

	if code != exitcode.Env {
		t.Fatalf("parseCodexPlanTUIArgs code = %v, want Env", code)
	}
	if !strings.Contains(stderr.String(), "--prompt is required") {
		t.Fatalf("stderr = %q, want prompt error", stderr.String())
	}
}
