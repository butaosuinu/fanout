package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/app/run"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/core/planspec"
	"github.com/butaosuinu/fanout/internal/infra/log"
)

func TestPlanTaskSlugQualifiesDefaultAndHonorsExplicit(t *testing.T) {
	task := planspec.Task{ID: "api-client", Title: "Extract API client", Briefing: "## Goal\nExtract it"}

	if got := panelaunch.PlanTaskSlug("launch-plan", task); got != "launch-plan-extract-api-client-api-client" {
		t.Fatalf("default slug = %q", got)
	}
	task.Slug = "shared-api-client"
	if got := panelaunch.PlanTaskSlug("launch-plan", task); got != "shared-api-client" {
		t.Fatalf("explicit slug = %q", got)
	}
}

func TestCheckPlanDepsDoesNotRequireGhForUnblockedOnly(t *testing.T) {
	binDir := t.TempDir()
	for _, name := range []string{"git", "tmux"} {
		path := filepath.Join(binDir, name)
		body := "#!/bin/sh\nexit 0\n"
		if name == "tmux" {
			body = "#!/bin/sh\nprintf 'tmux 3.6a\\n'\n"
		}
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir)

	if missing := missingDeps(depNeeds{git: true, tmux: true}); len(missing) != 0 {
		t.Fatalf("missingDeps(plan launch needs) = %v, want no gh requirement", missing)
	}
}

func TestParsePlanAgentOverrides(t *testing.T) {
	cfg := parsePlanOK(t, "launch-plan",
		"--agent", "claude",
		"--agent", "api-client=codex",
		"--agent", "base-types=claude",
		"--agent", "api-client=claude",
		"--agent", "codex",
	)

	if cfg.Agent != "codex" {
		t.Fatalf("Agent = %q, want codex", cfg.Agent)
	}
	cliCfg := cfg.CLIConfig()
	if got := cliCfg.EffectiveAgent("api-client"); got != "claude" {
		t.Fatalf("EffectiveAgent(api-client) = %q, want claude", got)
	}
	if got := cliCfg.EffectiveAgent("base-types"); got != "claude" {
		t.Fatalf("EffectiveAgent(base-types) = %q, want claude", got)
	}
	if got := cliCfg.EffectiveAgent("docs"); got != "codex" {
		t.Fatalf("EffectiveAgent(docs) = %q, want codex", got)
	}
	if len(cfg.AgentOverrides) != 2 {
		t.Fatalf("AgentOverrides = %+v, want 2 last-wins entries", cfg.AgentOverrides)
	}
}

func TestParsePlanAgentOverrideRejectsInvalidShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty agent", raw: "api-client=", want: "agent name must not be empty"},
		{name: "empty task", raw: "=codex", want: "<task-id> must be lowercase kebab-case"},
		{name: "uppercase task", raw: "Api=codex", want: "<task-id> must be lowercase kebab-case"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			_, code := parsePlanCommand([]string{"launch-plan", "--agent", tc.raw}, log.NewWith(&stdout, &stderr, false))
			if code != exitcode.Env {
				t.Fatalf("parsePlanCommand() code = %d, want %d", code, exitcode.Env)
			}
			if got := stderr.String(); !strings.Contains(got, tc.want) {
				t.Fatalf("stderr = %q, want to contain %q", got, tc.want)
			}
		})
	}
}

func TestParsePlanStatusRejectsAgentOverride(t *testing.T) {
	var stdout, stderr bytes.Buffer
	_, code := parsePlanCommand([]string{"launch-plan", "--status", "--agent", "api-client=codex"}, log.NewWith(&stdout, &stderr, false))
	if code != exitcode.Invocation {
		t.Fatalf("parsePlanCommand() code = %d, want %d", code, exitcode.Invocation)
	}
	if got := stderr.String(); !strings.Contains(got, "--status cannot be combined with --agent") {
		t.Fatalf("stderr = %q, want --status/--agent conflict", got)
	}
}

func TestParsePlanLifecycleRejectsAgentOverride(t *testing.T) {
	var stdout, stderr bytes.Buffer
	_, code := parsePlanCommand([]string{"launch-plan", "--close", "api-client", "--agent", "api-client=codex"}, log.NewWith(&stdout, &stderr, false))
	if code != exitcode.Invocation {
		t.Fatalf("parsePlanCommand() code = %d, want %d", code, exitcode.Invocation)
	}
	if got := stderr.String(); !strings.Contains(got, "--close/--merge/--cleanup cannot be combined with --agent") {
		t.Fatalf("stderr = %q, want lifecycle --agent conflict", got)
	}
}

func TestParsePlanTeamFlagSetsConfig(t *testing.T) {
	var stdout, stderr bytes.Buffer
	cfg, code := parsePlanCommand([]string{"launch-plan", "--team"}, log.NewWith(&stdout, &stderr, false))
	if code != exitcode.OK {
		t.Fatalf("parsePlanCommand(--team) code = %d, want OK; stderr=%q", code, stderr.String())
	}
	if !cfg.Team {
		t.Fatal("cfg.Team = false, want true")
	}
	if !cfg.CLIConfig().Team {
		t.Fatal("CLIConfig().Team = false, want true (forwarded to the task launch lane)")
	}
}

func TestParsePlanTeamRejectedInStatusAndLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"status", []string{"launch-plan", "--status", "--team"}, "--status cannot be combined with --team"},
		{"lifecycle", []string{"launch-plan", "--cleanup", "--team"}, "--close/--merge/--cleanup cannot be combined with --team"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			_, code := parsePlanCommand(tc.args, log.NewWith(&stdout, &stderr, false))
			if code != exitcode.Invocation {
				t.Fatalf("parsePlanCommand(%v) code = %d, want %d", tc.args, code, exitcode.Invocation)
			}
			if got := stderr.String(); !strings.Contains(got, tc.want) {
				t.Fatalf("stderr = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPlanStatusAllowsBranchPrefixForFallbackBranches(t *testing.T) {
	cfg := run.PlanCommandConfig{StatusMode: true, Format: "json", BranchPrefix: "custom/"}

	if code := validatePlanActionFlags(cfg, "", "", false, log.New(false)); code != exitcode.OK {
		t.Fatalf("validatePlanActionFlags() = %d, want %d", code, exitcode.OK)
	}

	spec := planspec.Spec{Plan: planspec.Plan{Slug: "launch-plan"}}
	task := planspec.Task{ID: "ui-shell", Title: "Build UI shell"}
	if got := planTaskBranch(cfg, spec, task); got != "custom/launch-plan-build-ui-shell-ui-shell" {
		t.Fatalf("planTaskBranch() = %q, want custom prefix fallback branch", got)
	}
}

func parsePlanOK(t *testing.T, args ...string) run.PlanCommandConfig {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cfg, code := parsePlanCommand(args, log.NewWith(&stdout, &stderr, false))
	if code != exitcode.OK {
		t.Fatalf("parsePlanCommand(%q) failed with code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
	}
	return cfg
}
