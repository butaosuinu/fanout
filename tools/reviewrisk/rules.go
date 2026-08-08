package main

import "strings"

// Rule values reused across several exact-match paths that share one doc row.
var (
	ruleCmdH       = Rule{ID: "cmd-h-files", Class: ClassH, Source: SourceDocTable, Note: "dispatch・runtime backend・self-exec・launch・state 書き換えを伴う cmd エントリ"}
	ruleDashboardM = Rule{ID: "dashboard-m-files", Class: ClassM, Source: SourceDocTable, Note: "state/runtime ポーリング・SSE・embed"}
	ruleTuiA       = Rule{ID: "tui-a-files", Class: ClassA, Source: SourceDocTable, Note: "TUI の View 層(描画・整形)"}
	ruleWebBundle  = Rule{ID: "extra-web-bundle", Class: ClassH, Source: SourceExtra, Note: "web 依存・埋め込みバンドルの生成系"}
	ruleWebConfig  = Rule{ID: "extra-web-config", Class: ClassM, Source: SourceExtra, Note: "web ビルド設定"}
	ruleLintConfig = Rule{ID: "extra-lint-config", Class: ClassM, Source: SourceExtra, Note: "Go lint 設定"}
	// 複雑度しきい値は hook と CI が共有する唯一のソース。数値を緩めると PR ゲートが
	// 静かに効かなくなるので、ゲート実体 (scripts/ と .github/workflows/ は H) の
	// 一段下として設定と同じ M に置く。
	ruleComplexityConfig = Rule{ID: "extra-complexity-config", Class: ClassM, Source: SourceExtra, Note: "複雑度しきい値"}
	ruleAgentGuide       = Rule{ID: "extra-agent-guide", Class: ClassM, Source: SourceExtra, Note: "エージェント作業規約"}
	ruleReadme           = Rule{ID: "extra-readme", Class: ClassNone, Source: SourceExtra, Note: "利用者向け説明"}
)

// fileRules classify a repo-relative path by exact match. File-level rows in the
// package table land here (a doc row that pins individual .go files) alongside
// extra top-level files that have no package-table row.
var fileRules = map[string]Rule{
	// cmd/fanout H set: dispatch/runtime backend/self-exec/launch wiring and state rewrites.
	"cmd/fanout/main.go":            ruleCmdH,
	"cmd/fanout/runtime_backend.go": ruleCmdH,
	"cmd/fanout/tui_popup.go":       ruleCmdH,
	"cmd/fanout/tui_launch.go":      ruleCmdH,
	"cmd/fanout/worktree_action.go": ruleCmdH,
	"cmd/fanout/codex_plan_tui.go":  ruleCmdH,
	"cmd/fanout/codex_team_tui.go":  ruleCmdH,
	"cmd/fanout/tui_restore.go":     ruleCmdH,
	"cmd/fanout/tui_watch.go":       ruleCmdH,

	// dashboard H files (server mux / runfile trust gate / capture-pane chain).
	"internal/ui/dashboard/server.go":  {ID: "dashboard-server", Class: ClassH, Source: SourceDocTable, Note: "localhost web サーバの mux・token 検証"},
	"internal/ui/dashboard/runfile.go": {ID: "dashboard-runfile", Class: ClassH, Source: SourceDocTable, Note: "token を含む dashboard.json・reuse/trust ゲート"},
	"internal/ui/dashboard/diff.go":    {ID: "dashboard-diff", Class: ClassH, Source: SourceDocTable, Note: "worktree diff の identity 検証・read-only 配信"},
	"internal/ui/dashboard/peek.go":    {ID: "dashboard-peek-plan", Class: ClassH, Source: SourceDocTable, Note: "capture-pane 前の検証チェーン"},
	"internal/ui/dashboard/plan.go":    {ID: "dashboard-peek-plan", Class: ClassH, Source: SourceDocTable, Note: "capture-pane 前の検証チェーン(plan mode かつ codex 限定)"},

	// dashboard M files.
	"internal/ui/dashboard/poller.go": ruleDashboardM,
	"internal/ui/dashboard/sse.go":    ruleDashboardM,
	"internal/ui/dashboard/embed.go":  ruleDashboardM,

	// tui: actions.go is H, the view/format trio is A; the tui prefix rule
	// classifies every other tui file M.
	"internal/ui/tui/actions.go": {ID: "tui-actions", Class: ClassH, Source: SourceDocTable, Note: "lifecycle(close/merge/cleanup)実行の配線と確認フロー"},
	"internal/ui/tui/view.go":    ruleTuiA,
	"internal/ui/tui/compact.go": ruleTuiA,
	"internal/ui/tui/styles.go":  ruleTuiA,

	// web token-leak boundary.
	"web/index.html": {ID: "web-index", Class: ClassH, Source: SourceDocTable, Note: "no-referrer・外部 fetch 方針(token 漏洩境界)"},

	// web display files that carry a safety boundary: the display prefix is A,
	// but these two decide what a href may be and how big a patch may render.
	"web/src/shared/github.ts":      {ID: "web-github", Class: ClassM, Source: SourceDocTable, Note: "GitHub URL の検証つき生成(href 安全性境界)"},
	"web/src/features/diff/diff.ts": {ID: "web-diff-parse", Class: ClassM, Source: SourceDocTable, Note: "patch パースと描画上限(敵性 patch のガード)"},

	// Extra files with no package-table row.
	"go.mod":     {ID: "extra-gomod", Class: ClassH, Source: SourceExtra, Note: "依存サプライチェーン"},
	"go.sum":     {ID: "extra-gosum", Class: ClassM, Source: SourceExtra, Note: "依存 lock 追随"},
	"install.sh": {ID: "extra-install-sh", Class: ClassH, Source: SourceExtra, Note: "curl|sh 配布経路"},
	"Makefile":   {ID: "extra-makefile", Class: ClassH, Source: SourceExtra, Note: "test・install 定義"},

	"web/package.json":        ruleWebBundle,
	"web/pnpm-lock.yaml":      ruleWebBundle,
	"web/pnpm-workspace.yaml": ruleWebBundle,
	"web/vite.config.ts":      ruleWebBundle,

	"web/tsconfig.json":    ruleWebConfig,
	"web/lingui.config.ts": ruleWebConfig,
	"web/.nvmrc":           ruleWebConfig,
	"web/.oxlintrc.json":   ruleWebConfig,
	"web/.oxfmtrc.json":    ruleWebConfig,

	".golangci.yml":          ruleLintConfig,
	".golangci-lint-version": ruleLintConfig,

	".golangci-complexity.yml": ruleComplexityConfig,
	"web/eslint.config.js":     ruleComplexityConfig,

	// ゲート判定の実体。ここが緩むと Go・TS 双方の finding を丸ごと消せるので、
	// .github/ の catch-all (M) ではなく scripts/ のゲート実体と同じ H に置く。
	".github/scripts/complexity-diff.mjs": {
		ID: "complexity-gate", Class: ClassH, Source: SourceExtra, Note: "複雑度ゲートの判定実体",
	},

	".gitignore": {ID: "extra-gitignore", Class: ClassM, Source: SourceExtra, Note: "追跡除外設定"},

	"CLAUDE.md": ruleAgentGuide,
	"AGENTS.md": ruleAgentGuide,

	// architecture.ja.md is the class canon; it overrides the docs/ NONE prefix.
	"docs/architecture.ja.md": {ID: "extra-arch-doc", Class: ClassM, Source: SourceExtra, Note: "クラス正典(docs/ の NONE を上書き)"},

	"README.md":    ruleReadme,
	"README.ja.md": ruleReadme,
	"RELEASE.md":   {ID: "extra-release-doc", Class: ClassNone, Source: SourceExtra, Note: "リリース手順"},
	"LICENSE":      {ID: "extra-license", Class: ClassNone, Source: SourceExtra, Note: "ライセンス"},

	".git-blame-ignore-revs": {ID: "extra-blame-ignore", Class: ClassNone, Source: SourceExtra, Note: "blame 除外リビジョン"},
}

// prefixRules classify by longest matching path prefix (see longestPrefixRule).
// Package rows in the doc land here; the trailing slash means only files under
// the directory match, never a bare directory token.
var prefixRules = []struct {
	prefix string
	rule   Rule
}{
	// meta: the only layer guard.
	{"internal/arch/", Rule{ID: "arch", Class: ClassH, Source: SourceDocTable, Note: "層ルールの CI 強制(唯一のガード)"}},

	// infra H.
	{"internal/infra/state/", Rule{ID: "infra-state", Class: ClassH, Source: SourceDocTable, Note: "state.json・Herdr intent journal と lock の読み書き"}},
	{"internal/infra/worktree/", Rule{ID: "infra-worktree", Class: ClassH, Source: SourceDocTable, Note: "base branch 解決・refresh・worktree add・branch ref の atomic 予約と checkout 観測"}},
	{"internal/infra/hooks/", Rule{ID: "infra-hooks", Class: ClassH, Source: SourceDocTable, Note: "ライフサイクルフック実行"}},
	{"internal/infra/selfupdate/", Rule{ID: "infra-selfupdate", Class: ClassH, Source: SourceDocTable, Note: "自己アップデート"}},
	{"internal/infra/team/", Rule{ID: "infra-team", Class: ClassH, Source: SourceDocTable, Note: "team SQLite バス"}},
	{"internal/infra/settings/", Rule{ID: "infra-settings", Class: ClassH, Source: SourceDocTable, Note: "設定解決の安全ゲート"}},
	{"internal/infra/herdrrun/", Rule{ID: "infra-herdrrun", Class: ClassH, Source: SourceDocTable, Note: "herdr version gate、owned session lifecycle、non-shell agent launcher、workspace/worktree mutation、snapshot 投影"}},

	// infra M.
	{"internal/infra/ghissue/", Rule{ID: "infra-ghissue", Class: ClassM, Source: SourceDocTable, Note: "GitHub issue/PR 読み書き"}},
	{"internal/infra/gitstat/", Rule{ID: "infra-gitstat", Class: ClassM, Source: SourceDocTable, Note: "git 差分・状態取得"}},
	{"internal/infra/tmuxrun/", Rule{ID: "infra-tmuxrun", Class: ClassM, Source: SourceDocTable, Note: "tmux 直接操作"}},
	{"internal/infra/tmuxbackend/", Rule{ID: "infra-tmuxbackend", Class: ClassM, Source: SourceDocTable, Note: "backend 契約から tmuxrun への薄い adapter"}},
	{"internal/infra/msgstore/", Rule{ID: "infra-msgstore", Class: ClassM, Source: SourceDocTable, Note: "send/post/inbox/board"}},
	{"internal/infra/notify/", Rule{ID: "infra-notify", Class: ClassM, Source: SourceDocTable, Note: "通知送出"}},
	{"internal/infra/runtime/", Rule{ID: "infra-runtime", Class: ClassM, Source: SourceDocTable, Note: "git root・選択済み backend の起動コンテキスト解決"}},
	{"internal/infra/displayname/", Rule{ID: "infra-displayname", Class: ClassM, Source: SourceDocTable, Note: "表示名生成"}},
	{"internal/infra/codexapp/", Rule{ID: "infra-codexapp", Class: ClassM, Source: SourceDocTable, Note: "Codex app-server クライアント"}},
	{"internal/infra/atomicfs/", Rule{ID: "infra-atomicfs", Class: ClassM, Source: SourceDocTable, Note: "原子的ファイル書き込み"}},
	{"internal/infra/gitroot/", Rule{ID: "infra-gitroot", Class: ClassM, Source: SourceDocTable, Note: "git root 探索"}},

	// infra A.
	{"internal/infra/log/", Rule{ID: "infra-log", Class: ClassA, Source: SourceDocTable, Note: "ロギング"}},
	{"internal/infra/tty/", Rule{ID: "infra-tty", Class: ClassA, Source: SourceDocTable, Note: "端末判定"}},
	{"internal/infra/execx/", Rule{ID: "infra-execx", Class: ClassA, Source: SourceDocTable, Note: "コマンド実行の薄いラッパ"}},
	{"internal/infra/browser/", Rule{ID: "infra-browser", Class: ClassA, Source: SourceDocTable, Note: "ブラウザ起動"}},

	// app H.
	{"internal/app/watch/", Rule{ID: "app-watch", Class: ClassH, Source: SourceDocTable, Note: "ラベル watcher の 1 サイクル"}},
	{"internal/app/briefing/", Rule{ID: "app-briefing", Class: ClassH, Source: SourceDocTable, Note: "エージェントに注入するプロンプト本文"}},
	{"internal/app/lifecycle/", Rule{ID: "app-lifecycle", Class: ClassH, Source: SourceDocTable, Note: "close/merge/cleanup"}},
	{"internal/app/panelaunch/", Rule{ID: "app-panelaunch", Class: ClassH, Source: SourceDocTable, Note: "tmux pane 生成と Herdr coordinator/worktree/agent launch"}},
	{"internal/app/sessionbinding/", Rule{ID: "app-sessionbinding", Class: ClassH, Source: SourceDocTable, Note: "遅延 Herdr agent session の初回束縛と state 保存"}},

	// app M.
	{"internal/app/panelayout/", Rule{ID: "app-panelayout", Class: ClassM, Source: SourceDocTable, Note: "ペインレイアウト計算"}},
	{"internal/app/sessionview/", Rule{ID: "app-sessionview", Class: ClassM, Source: SourceDocTable, Note: "state+runtime backend+gh の Snapshot"}},
	{"internal/app/run/", Rule{ID: "app-run", Class: ClassM, Source: SourceDocTable, Note: "executePlan の実行ロジック"}},
	{"internal/app/statusreport/", Rule{ID: "app-statusreport", Class: ClassM, Source: SourceDocTable, Note: "--status レポート生成"}},
	{"internal/app/peermsg/", Rule{ID: "app-peermsg", Class: ClassM, Source: SourceDocTable, Note: "fanout msg の実行層"}},
	{"internal/app/cliflags/", Rule{ID: "app-cliflags", Class: ClassM, Source: SourceDocTable, Note: "フラグ検証(main の分岐)"}},

	// core H.
	{"internal/core/backend/", Rule{ID: "core-backend", Class: ClassH, Source: SourceDocTable, Note: "runtime backend 契約・親 stickiness・選択の fail-closed 判定"}},

	// core M.
	{"internal/core/agent/", Rule{ID: "core-agent", Class: ClassM, Source: SourceDocTable, Note: "エージェント名解決・CLI 検証"}},
	{"internal/core/planspec/", Rule{ID: "core-planspec", Class: ClassM, Source: SourceDocTable, Note: "fanout plan の JSON スキーマ"}},
	{"internal/core/naming/", Rule{ID: "core-naming", Class: ClassM, Source: SourceDocTable, Note: "slug・branch 名生成"}},
	{"internal/core/parentref/", Rule{ID: "core-parentref", Class: ClassM, Source: SourceDocTable, Note: "親参照の正規化"}},
	{"internal/core/fanset/", Rule{ID: "core-fanset", Class: ClassM, Source: SourceDocTable, Note: "fan-out 対象集合の計算"}},
	{"internal/core/blockers/", Rule{ID: "core-blockers", Class: ClassM, Source: SourceDocTable, Note: "ブロッカー判定"}},

	// core A.
	{"internal/core/exitcode/", Rule{ID: "core-exitcode", Class: ClassA, Source: SourceDocTable, Note: "終了コード定義"}},
	{"internal/core/cliview/", Rule{ID: "core-cliview", Class: ClassA, Source: SourceDocTable, Note: "CLI 出力の整形"}},
	{"internal/core/errs/", Rule{ID: "core-errs", Class: ClassA, Source: SourceDocTable, Note: "エラーラップの共有ヘルパ"}},

	// tui catch-all: file rules above override actions.go(H) and the A trio.
	{"internal/ui/tui/", Rule{ID: "tui-rest", Class: ClassM, Source: SourceDocTable, Note: "キー処理・フォーム・ポーリングの配線"}},

	// dashboard embed bundle (generated on build).
	{"internal/ui/dashboard/static/", Rule{ID: "dashboard-static", Class: ClassM, Source: SourceExtra, Note: "embed 対象バンドル"}},

	// cmd catch-all: the doc's "上記以外" row.
	{"cmd/fanout/", Rule{ID: "cmd-rest", Class: ClassM, Source: SourceDocTable, Note: "フラグ検証・app 層への薄い dispatch"}},

	// tools meta-tooling (this tool). New tools stay unclassified -> high.
	{"tools/reviewrisk/", Rule{ID: "tools-reviewrisk", Class: ClassH, Source: SourceDocTable, Note: "PR review risk 判定(物差し。ルール変更はレビュー配線を変える)"}},

	// web transport vs display.
	{"web/src/transport/", Rule{ID: "web-transport", Class: ClassM, Source: SourceDocTable, Note: "SSE/polling transport・token 付き /api/* 呼び出し"}},
	{"web/src/", Rule{ID: "web-src", Class: ClassA, Source: SourceDocTable, Note: "表示(app/features/ui/shared/styles/tests)"}},
	// 製品コードではなく、複雑度チェック専用の隔離 ESLint パッケージ
	// (typescript-eslint が TS 7 で動かないため web/ 本体から分けてある)。
	{"web/tools/complexity/", ruleComplexityConfig},

	// tests: bin is the test yardstick (H); the suites are M (deletion -> S1/S2).
	{"tests/bin/", Rule{ID: "tests-bin", Class: ClassH, Source: SourceExtra, Note: "fake gh/tmux/git = テストの物差し"}},
	{"tests/bats/", Rule{ID: "tests-suite", Class: ClassM, Source: SourceExtra, Note: "bats テスト"}},
	{"tests/fixtures/", Rule{ID: "tests-suite", Class: ClassM, Source: SourceExtra, Note: "テストフィクスチャ"}},
	{"tests/golden/", Rule{ID: "tests-suite", Class: ClassM, Source: SourceExtra, Note: "ゴールデン出力"}},

	// CI and repo automation.
	{".github/workflows/", Rule{ID: "github-workflows", Class: ClassH, Source: SourceExtra, Note: "CI 定義"}},
	{".github/", Rule{ID: "github-rest", Class: ClassM, Source: SourceExtra, Note: "GitHub 設定"}},
	{".claude/", Rule{ID: "claude-settings", Class: ClassH, Source: SourceExtra, Note: "PR review gate 等の作業設定"}},
	{".codex/", Rule{ID: "codex-settings", Class: ClassH, Source: SourceExtra, Note: "Codex hooks 配線 (push gate / stop gate)"}},
	{"scripts/", Rule{ID: "agent-hooks", Class: ClassH, Source: SourceExtra, Note: "エージェント hook の品質ゲート実体"}},
	{"claude/", Rule{ID: "claude-prompts", Class: ClassM, Source: SourceExtra, Note: "配布エージェントプロンプト"}},
	{"codex/", Rule{ID: "codex-prompts", Class: ClassM, Source: SourceExtra, Note: "配布エージェントプロンプト"}},
	{"hack/", Rule{ID: "hack", Class: ClassM, Source: SourceExtra, Note: "補助スクリプト"}},

	// docs and docs site NONE (except architecture.ja.md above).
	{"docs/", Rule{ID: "docs", Class: ClassNone, Source: SourceExtra, Note: "ドキュメント"}},
	{"site/", Rule{ID: "site", Class: ClassNone, Source: SourceExtra, Note: "ドキュメントサイト"}},
}

// classifyPath resolves a repo-relative, slash-separated file path to its Rule.
// Evaluation order (first hit wins): exact file rule, Go test pairing
// (foo_test.go inherits foo.go's file rule but never drops below the package's
// prefix class), web test override, then longest-prefix rule. An unmatched path
// returns ok=false so the caller fails closed to high (S9 unclassified-path).
func classifyPath(p string) (Rule, bool) {
	if r, ok := fileRules[p]; ok {
		return r, true
	}
	if base, ok := strings.CutSuffix(p, "_test.go"); ok {
		if r, ok := fileRules[base+".go"]; ok {
			// Keep the test at least as heavy as its directory: a test paired
			// to an A file inside an M package stays M.
			if pr, ok := longestPrefixRule(p); ok && pr.Class > r.Class {
				return pr, true
			}
			return r, true
		}
	}
	if isWebTestFile(p) {
		return Rule{ID: "web-test", Class: ClassA, Source: SourceDocTable, Note: "web テスト(表示クラス扱い)"}, true
	}
	return longestPrefixRule(p)
}

// isWebTestFile reports whether p is a web test under web/src/: either the
// web/src/test/ harness dir or a *.test/*.spec .ts(x) file. Vitest collects both
// the .test and .spec suffixes (web/vite.config.ts does not override
// test.include), so both count. Per the doc these are class A (display),
// overriding the web/src/transport prefix. This is the single
// web-test-shape predicate shared by classifyPath's A override, S3 skip
// detection, and S1's isTestShape.
func isWebTestFile(p string) bool {
	if !strings.HasPrefix(p, "web/src/") {
		return false
	}
	if strings.HasPrefix(p, "web/src/test/") {
		return true
	}
	return strings.HasSuffix(p, ".test.ts") || strings.HasSuffix(p, ".test.tsx") ||
		strings.HasSuffix(p, ".spec.ts") || strings.HasSuffix(p, ".spec.tsx")
}

// longestPrefixRule returns the prefixRules entry with the longest prefix that
// is a prefix of p, scanning all entries so the result never depends on slice
// order.
func longestPrefixRule(p string) (Rule, bool) {
	best := -1
	var rule Rule
	for _, pr := range prefixRules {
		if len(pr.prefix) > best && strings.HasPrefix(p, pr.prefix) {
			best = len(pr.prefix)
			rule = pr.rule
		}
	}
	if best < 0 {
		return Rule{}, false
	}
	return rule, true
}
