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

func TestAgentSelectionValidation(t *testing.T) {
	for _, cfg := range []*cliflags.Config{
		{Agent: "opencode", Effort: "high", DryRun: true},
		{Agent: "unknown", DryRun: true},
		{Agent: "codex", Model: "gpt-6-astra", Team: true, DryRun: true},
		{Agent: "codex", Effort: "medium", PlanMode: new(true), DryRun: true},
	} {
		if err := validateIssueAgents(cfg, []ghissue.Issue{{Number: 1}}, nil); err == nil {
			t.Errorf("issue accepted %+v", cfg)
		}
		if err := validateTaskAgents(cfg, []planspec.Task{{ID: "task"}}, nil); err == nil {
			t.Errorf("plan accepted %+v", cfg)
		}
	}
	// A name-level dedup must not hide an unsupported selection on a later target.
	cfg := &cliflags.Config{
		Agent: "codex", Team: true, DryRun: true,
		AgentOverrides: []cliflags.AgentOverride{{Target: "2", Name: "codex", Model: "gpt-6-astra"}},
	}
	if err := validateIssueAgents(cfg, []ghissue.Issue{{Number: 1}, {Number: 2}}, nil); err == nil || !strings.Contains(err.Error(), "#791") {
		t.Fatalf("later target selection error = %v", err)
	}
}

func TestResolveDefaultAgentSelection(t *testing.T) {
	t.Setenv("FANOUT_AGENT", "codex:gpt-6-astra:medium")
	cfg := &cliflags.Config{}
	if err := resolveDefaultAgent(cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.EffectiveSelection("").String(); got != "codex:gpt-6-astra:medium" {
		t.Fatal(got)
	}
	t.Setenv("FANOUT_AGENT", "claude:a:b:c")
	if err := resolveDefaultAgent(&cliflags.Config{}); err == nil {
		t.Fatal("accepted malformed environment selection")
	}
}

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

func TestValidateIssueAgentsAllowsNonCodexTargetInPlanMode(t *testing.T) {
	cfg := &cliflags.Config{
		Agent:          "codex",
		AgentOverrides: []cliflags.AgentOverride{{Target: "102", Name: "claude"}},
		PlanMode:       new(true),
		DryRun:         true,
	}

	err := validateIssueAgents(cfg, []ghissue.Issue{{Number: 101}, {Number: 102}}, nil)
	if err != nil {
		t.Fatalf("validateIssueAgents() returned error: %v", err)
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
