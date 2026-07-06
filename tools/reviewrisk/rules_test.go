package main

import "testing"

// TestClassifyPath pins classifyPath against a representative path from every
// doc entry plus the evaluation-order edges (Go test pairing, web test
// override, longest-prefix wins, and the intentional fail-closed gaps).
func TestClassifyPath(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		want  Class
		found bool
	}{
		// --- package-table representatives, one per class band ---
		{name: "arch layer guard is H", path: "internal/arch/arch_test.go", want: ClassH, found: true},
		{name: "infra state is H", path: "internal/infra/state/store.go", want: ClassH, found: true},
		{name: "infra worktree is H", path: "internal/infra/worktree/add.go", want: ClassH, found: true},
		{name: "infra settings is H", path: "internal/infra/settings/settings.go", want: ClassH, found: true},
		{name: "infra ghissue is M", path: "internal/infra/ghissue/issue.go", want: ClassM, found: true},
		{name: "infra atomicfs is M", path: "internal/infra/atomicfs/write.go", want: ClassM, found: true},
		{name: "infra log is A", path: "internal/infra/log/log.go", want: ClassA, found: true},
		{name: "infra browser is A", path: "internal/infra/browser/open.go", want: ClassA, found: true},
		{name: "app briefing is H", path: "internal/app/briefing/render.go", want: ClassH, found: true},
		{name: "app panelaunch is H", path: "internal/app/panelaunch/launch.go", want: ClassH, found: true},
		{name: "app run is M", path: "internal/app/run/run.go", want: ClassM, found: true},
		{name: "app cliflags is M", path: "internal/app/cliflags/flags.go", want: ClassM, found: true},
		{name: "core naming is M", path: "internal/core/naming/slug.go", want: ClassM, found: true},
		{name: "core blockers is M", path: "internal/core/blockers/blockers.go", want: ClassM, found: true},
		{name: "core exitcode is A", path: "internal/core/exitcode/code.go", want: ClassA, found: true},
		{name: "core cliview is A", path: "internal/core/cliview/view.go", want: ClassA, found: true},

		// --- cmd/fanout: 7 H files plus the catch-all M ---
		{name: "cmd main is H", path: "cmd/fanout/main.go", want: ClassH, found: true},
		{name: "cmd tui_popup is H", path: "cmd/fanout/tui_popup.go", want: ClassH, found: true},
		{name: "cmd tui_launch is H", path: "cmd/fanout/tui_launch.go", want: ClassH, found: true},
		{name: "cmd worktree_action is H", path: "cmd/fanout/worktree_action.go", want: ClassH, found: true},
		{name: "cmd codex_plan_tui is H", path: "cmd/fanout/codex_plan_tui.go", want: ClassH, found: true},
		{name: "cmd tui_restore is H", path: "cmd/fanout/tui_restore.go", want: ClassH, found: true},
		{name: "cmd tui_watch is H", path: "cmd/fanout/tui_watch.go", want: ClassH, found: true},
		{name: "cmd rest status.go is M", path: "cmd/fanout/status.go", want: ClassM, found: true},

		// --- dashboard: every file is enumerated; a new file falls through ---
		{name: "dashboard server is H", path: "internal/ui/dashboard/server.go", want: ClassH, found: true},
		{name: "dashboard runfile is H", path: "internal/ui/dashboard/runfile.go", want: ClassH, found: true},
		{name: "dashboard peek is H", path: "internal/ui/dashboard/peek.go", want: ClassH, found: true},
		{name: "dashboard plan is H", path: "internal/ui/dashboard/plan.go", want: ClassH, found: true},
		{name: "dashboard poller is M", path: "internal/ui/dashboard/poller.go", want: ClassM, found: true},
		{name: "dashboard sse is M", path: "internal/ui/dashboard/sse.go", want: ClassM, found: true},
		{name: "dashboard embed is M", path: "internal/ui/dashboard/embed.go", want: ClassM, found: true},
		{name: "dashboard new file is unclassified", path: "internal/ui/dashboard/newthing.go", found: false},

		// --- tui: actions H, view/compact/styles A, everything else M ---
		{name: "tui actions is H", path: "internal/ui/tui/actions.go", want: ClassH, found: true},
		{name: "tui view is A", path: "internal/ui/tui/view.go", want: ClassA, found: true},
		{name: "tui compact is A", path: "internal/ui/tui/compact.go", want: ClassA, found: true},
		{name: "tui styles is A", path: "internal/ui/tui/styles.go", want: ClassA, found: true},
		{name: "tui rest filter.go is M", path: "internal/ui/tui/filter.go", want: ClassM, found: true},

		// --- Go test pairing: inherit the paired .go file, never below package ---
		{name: "cmd main_test.go inherits H", path: "cmd/fanout/main_test.go", want: ClassH, found: true},
		{name: "cmd dispatch_test.go has no H pair so it is M", path: "cmd/fanout/dispatch_test.go", want: ClassM, found: true},
		{name: "dashboard poller_test.go inherits M", path: "internal/ui/dashboard/poller_test.go", want: ClassM, found: true},
		{name: "dashboard server_test.go inherits H", path: "internal/ui/dashboard/server_test.go", want: ClassH, found: true},
		{name: "tui view_test.go stays at package M not A", path: "internal/ui/tui/view_test.go", want: ClassM, found: true},

		// --- web ---
		{name: "web index.html is H", path: "web/index.html", want: ClassH, found: true},
		{name: "web hooks transport is M", path: "web/src/hooks/useSnapshot.ts", want: ClassM, found: true},
		{name: "web lib transport is M", path: "web/src/lib/api.ts", want: ClassM, found: true},
		{name: "web components are A", path: "web/src/components/App.tsx", want: ClassA, found: true},
		// web test override beats the hooks transport prefix.
		{name: "web hooks test file overrides to A", path: "web/src/hooks/useDrawerWidth.test.tsx", want: ClassA, found: true},
		{name: "web test harness dir is A", path: "web/src/test/server.ts", want: ClassA, found: true},
		// longest-prefix: lib(M) beats web/src/(A) for a non-test lib file.
		{name: "web lib file beats web src by longest prefix", path: "web/src/lib/x.ts", want: ClassM, found: true},
		{name: "web root unknown file is unclassified", path: "web/unknown.txt", found: false},

		// --- extra top-level and prefix rules ---
		{name: "go.mod is H", path: "go.mod", want: ClassH, found: true},
		{name: "go.sum is M", path: "go.sum", want: ClassM, found: true},
		{name: "install.sh is H", path: "install.sh", want: ClassH, found: true},
		{name: "Makefile is H", path: "Makefile", want: ClassH, found: true},
		{name: "LICENSE is NONE", path: "LICENSE", want: ClassNone, found: true},
		{name: "README is NONE", path: "README.md", want: ClassNone, found: true},
		{name: "README.ja is NONE", path: "README.ja.md", want: ClassNone, found: true},
		{name: "architecture doc overrides docs NONE to M", path: "docs/architecture.ja.md", want: ClassM, found: true},
		{name: "other docs are NONE", path: "docs/review-checklist.ja.md", want: ClassNone, found: true},
		{name: "workflow is H", path: ".github/workflows/test.yml", want: ClassH, found: true},
		{name: "github rest is M", path: ".github/CODEOWNERS", want: ClassM, found: true},
		{name: "claude settings is H", path: ".claude/settings.json", want: ClassH, found: true},
		{name: "codex tools shell is H", path: "codex/tools/post-work-review.sh", want: ClassH, found: true},
		{name: "codex prompts are M", path: "codex/skills/rescue/SKILL.md", want: ClassM, found: true},
		{name: "claude prompts are M", path: "claude/commands/fanout.md", want: ClassM, found: true},
		{name: "tests bin yardstick is H", path: "tests/bin/gh", want: ClassH, found: true},
		{name: "tests bats suite is M", path: "tests/bats/status.bats", want: ClassM, found: true},
		{name: "tests golden is M", path: "tests/golden/scenario-plan.txt", want: ClassM, found: true},
		{name: "site is NONE", path: "site/content/docs/index.md", want: ClassNone, found: true},
		{name: "hack is M", path: "hack/release.sh", want: ClassM, found: true},

		// --- this tool: repo meta-tooling is H ---
		{name: "tools reviewrisk rules is H", path: "tools/reviewrisk/rules.go", want: ClassH, found: true},

		// --- genuinely unknown top-level file ---
		{name: "unknown root file is unclassified", path: "somefile", found: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, ok := classifyPath(tt.path)
			if ok != tt.found {
				t.Fatalf("classifyPath(%q) ok = %v, want %v", tt.path, ok, tt.found)
			}
			if ok && r.Class != tt.want {
				t.Errorf("classifyPath(%q) class = %v, want %v", tt.path, r.Class, tt.want)
			}
		})
	}
}

// TestClassifyPathSource pins the doc-table vs extra provenance the docsync
// test keys off: package-table rows are SourceDocTable, and files with no such
// row are SourceExtra.
func TestClassifyPathSource(t *testing.T) {
	tests := []struct {
		name string
		path string
		want RuleSource
	}{
		{name: "package row is doc-table", path: "internal/infra/state/store.go", want: SourceDocTable},
		{name: "cmd catch-all is doc-table", path: "cmd/fanout/status.go", want: SourceDocTable},
		{name: "tools row is doc-table", path: "tools/reviewrisk/rules.go", want: SourceDocTable},
		{name: "go.mod has no package row so it is extra", path: "go.mod", want: SourceExtra},
		{name: "architecture doc is extra", path: "docs/architecture.ja.md", want: SourceExtra},
		{name: "tests bin is extra", path: "tests/bin/gh", want: SourceExtra},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, ok := classifyPath(tt.path)
			if !ok {
				t.Fatalf("classifyPath(%q) ok = false, want true", tt.path)
			}
			if r.Source != tt.want {
				t.Errorf("classifyPath(%q) source = %v, want %v", tt.path, r.Source, tt.want)
			}
		})
	}
}

// TestRuleIDsNonEmpty guards against a typo leaving a rule without a stable ID,
// since the ID surfaces in the report and CI comment.
func TestRuleIDsNonEmpty(t *testing.T) {
	for path, r := range fileRules {
		if r.ID == "" {
			t.Errorf("fileRules[%q] has an empty ID", path)
		}
	}
	for _, pr := range prefixRules {
		if pr.rule.ID == "" {
			t.Errorf("prefixRules[%q] has an empty ID", pr.prefix)
		}
	}
}
