package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/core/planspec"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
)

func TestValidateIssueAgentsSkipsInstalledCheckForLimitDeferredAgents(t *testing.T) {
	installOnlyFakeAgent(t, "claude")
	cfg := &cliflags.Config{
		Agent:          "claude",
		AgentOverrides: []cliflags.AgentOverride{{Target: "102", Name: "codex"}},
	}

	err := validateIssueAgents(
		cfg,
		[]ghissue.Issue{{Number: 101}},
		[]ghissue.Issue{{Number: 102}},
	)
	if err != nil {
		t.Fatalf("validateIssueAgents() returned error: %v", err)
	}
}

func TestValidateTaskAgentsSkipsInstalledCheckForLimitDeferredAgents(t *testing.T) {
	installOnlyFakeAgent(t, "claude")
	cfg := &cliflags.Config{
		Agent:          "claude",
		AgentOverrides: []cliflags.AgentOverride{{Target: "docs", Name: "codex"}},
	}

	err := validateTaskAgents(
		cfg,
		[]planspec.Task{{ID: "api-client"}},
		[]planspec.Task{{ID: "docs"}},
	)
	if err != nil {
		t.Fatalf("validateTaskAgents() returned error: %v", err)
	}
}

func TestValidateIssueAgentsReportsNonCodexTargetInPlanMode(t *testing.T) {
	cfg := &cliflags.Config{
		Agent:          "codex",
		AgentOverrides: []cliflags.AgentOverride{{Target: "102", Name: "claude"}},
		CodexPlanMode:  new(true),
		DryRun:         true,
	}

	err := validateIssueAgents(cfg, []ghissue.Issue{{Number: 101}, {Number: 102}}, nil)

	if err == nil {
		t.Fatal("validateIssueAgents() error = nil, want mixed-agent rejection")
	}
	want := "codex plan mode requires every selected child to use agent codex; #102 resolves to claude"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("validateIssueAgents() error = %q, want %q", err, want)
	}
}

func installOnlyFakeAgent(t *testing.T, name string) {
	t.Helper()
	binDir := t.TempDir()
	path := filepath.Join(binDir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
}
