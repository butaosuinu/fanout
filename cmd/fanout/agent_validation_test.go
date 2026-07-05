package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/butaosuinu/fanout/internal/cliflags"
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

func installOnlyFakeAgent(t *testing.T, name string) {
	t.Helper()
	binDir := t.TempDir()
	path := filepath.Join(binDir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
}
