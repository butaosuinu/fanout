# session 開始時 plan mode の統一設定(2026-07)

session 開始時に plan mode で始めるかを、レーン別の 3 つの bool 設定
`newSessionPlanMode` / `orchestratorPlanMode` / `childPlanMode` に統合し、
各設定を全エージェント(claude / codex / opencode)へ共通適用する。codex
専用だった `codexPlanMode` 設定・`--codex-plan-mode` / `--no-codex-plan-mode`
フラグ・`FANOUT_CODEX_PLAN_MODE` は廃止する(v0.x の意図した breaking)。
この文書は決定記録で、実装は末尾の issue ツリーで行う。先行検討 #472 の
決定(RepoEditable=false、CLI フラグなし、claude の非 plan = auto)を継承し、
「親は常に Plan Mode」を設定制へ改める。

## 背景

plan mode の配線は codex 専用で、しかも起動レーンごとにばらばらだった。
TUI の手動 codex ペインは無条件で Codex Plan Mode、issue 子は
`codexPlanMode` 設定準拠(codex のみ)、オーケストレーター・plan
coordinator・plan 子タスク・watcher は常に非 plan。claude と opencode には
plan mode の配線が存在しない。エージェントを差し替えると起動姿勢が変わる
この状態を、レーンで決まる 3 設定に揃える。

## 決定: 設定仕様

| キー | 既定 | Env |
|---|---|---|
| `newSessionPlanMode` | true | `FANOUT_NEW_SESSION_PLAN_MODE` |
| `orchestratorPlanMode` | true | `FANOUT_ORCHESTRATOR_PLAN_MODE` |
| `childPlanMode` | false | `FANOUT_CHILD_PLAN_MODE` |

- Group は Launch。`configKeys` では `runtimeBackend` の前に new →
  orchestrator → child の順で並べる(表示順 = 定義順)。
- **RepoEditable は 3 キーとも false**。起動コマンドと権限姿勢を変える
  設定のため、checkout した repo の `.fanout/config.json` からは on/off
  どちらにも動かせない(`repoOverrides()` で strip + 警告)。操作面は TUI
  設定フォーム("s"、user スコープのみ)・user config・env の 3 つで、
  **CLI フラグは追加しない**(#472 の決定を継承)。
- 既定 ON の帰結として、新規 Session とオーケストレーターは初回から plan
  起動になる。TUI 手動 codex ペインの無条件 Plan Mode は設定準拠に変わる
  (`newSessionPlanMode=false` で素の codex になる)。

### レーン × 設定の適用マトリクス

| 起動レーン | 従う設定 |
|---|---|
| TUI 新規 Session(プロンプトモード手動ペイン) | `newSessionPlanMode` |
| plan fan-out coordinator(fanout-plan skill 実行ペイン) | `newSessionPlanMode` |
| TUI attach(既存ペインへのエージェント起動) | `newSessionPlanMode` |
| issue mode オーケストレーター親ペイン | `orchestratorPlanMode` |
| issue mode の子 Session | `childPlanMode` |
| `fanout plan` の子タスク | `childPlanMode` |
| watcher 起動ペイン | `childPlanMode` |

coordinator が plan mode で始まると、fanout-plan skill の `fanout plan`
実行は plan 承認を待ってから走る。これは意図した挙動で、fan-out 前に計画を
レビューする機会になる。watcher レーンは無人運用のため、`childPlanMode=true`
にすると起動した Session が承認待ちで止まる点に注意する。

## エージェント別の mode 実装

| エージェント | plan mode | 非 plan mode |
|---|---|---|
| claude | `--permission-mode plan` | `--permission-mode auto` |
| codex | `__codex-plan-tui`(Codex Plan Mode 制御) | 素の `codex` |
| opencode | `--agent plan` | `--agent build` |

- `internal/core/agent` の `Definition` に `ModeArgs`(mode → フラグ列の
  純テーブル)を足し、Build 系関数に mode 引数を通す。claude の
  `--settings`(hooks)とは単純併記で干渉しない。codex は両モードとも
  ModeArgs なし — plan の実体は app 層の plan TUI 経路のまま。
- 非 plan を `--permission-mode auto` / `--agent build` と明示するため、
  既存の全 claude / opencode 起動コマンド文字列が変わる。Tier 2 goldens は
  全面再生成になる。
- resume にも mode を通す。restore は state に記録した plan mode から
  フラグを再付与し(claude の `--continue --permission-mode plan` は有効な
  組み合わせ)、plan mode を維持したまま復元する。
- `internal/app/panelaunch` は `Request.CodexPlanMode` を `PlanMode` に
  一般化し、`PlanMode && Agent=="codex"` のときだけ plan TUI 経路、他は
  mode 付き builder を呼ぶ。codex 以外を拒む既存のハードエラーと
  `run/agents.go` の codex-only ガードは削除する。
- claude の `--permission-mode auto` には利用条件がある(Anthropic API は
  全ユーザー可、Bedrock / Vertex / Foundry は `CLAUDE_CODE_ENABLE_AUTO_MODE`
  等の明示有効化が必要)。非対応環境では claude が起動時にエラーで終了し、
  launch wrapper が exit status を表示して shell へ戻すことで表面化する
  (fail loud)。v1 は capability 検査や素起動フォールバックを持たず
  (非ゴール参照)、前提は利用者向けドキュメントに明記する。
- plan mode の issue 子 briefing は合成にする。現行の `briefing.Render` は
  codex plan 時に plan 専用 briefing を即 return し、auto PR・review
  gate・base branch などの完了契約を落とす。claude / opencode の plan 子は
  plan-first の前置き + 通常の work 契約を合成した briefing にし、codex
  plan TUI 子は既存の `<proposed_plan>` 契約付き briefing を維持する。

## precedence と競合

1. **plan が team に勝つ**。`childPlanMode=true` + `--team` + codex 子は
   plan TUI で起動し、idle-turn メッセージブリッジ(team TUI)は付かない。
   roster 登録と inbox への蓄積は残り、codexapp が `plan` 状態を報告する
   ので `nudge` による督促は機能する。launch ログに警告 1 行を出す。
   issue lane の既存 precedence(`request.go` の CodexPlanMode 先行判定)を
   全レーンに統一した形。
2. **オーケストレーターの codex は plan TUI にしない**。オーケストレーターは
   `AgentStartGate`(子 fan-out 完了までエージェント起動を留める gate)を
   使うが、plan TUI の起動 handshake は gate release より先に走るため
   deadlock する(現行コードもこの組み合わせをハードエラーにしている)。
   `orchestratorPlanMode=true` の codex オーケストレーターは警告 1 行を
   出して通常 codex で起動する。claude / opencode はフラグ注入だけなので
   gate と両立し、そのまま plan mode になる。

## 状態記録と表示

- `state.Pane` の Go フィールドは `PlanMode` にリネームし、JSON キーは
  `codexPlanMode` を据え置く(`ShellKey` の歴史名と同じ扱い)。旧 state
  行は無変換で読める。旧バイナリが新 state の claude plan 行を読むと
  codex 復元を試みて失敗するが、バージョン混在は非サポート。
- `sessionview` の `PaneView.PlanMode` は「plan mode 起動ペイン(全
  エージェント)」に意味を広げる。JSON キー `planMode` は変更なし。
- web dashboard の `GET /api/plan` と SPA の Plan セクションは
  `planMode && agent=="codex"` にゲートを変える。`<proposed_plan>`
  ブロックは codex plan briefing の出力契約で、claude / opencode の plan
  出力に capture 可能なフォーマットはないため。`tools/reviewrisk/rules.go`
  の note と `docs/architecture.ja.md` の該当行は docsync テストが連動する
  ので同一 PR で更新する。
- `@fanout_agent_state` の `plan` 値の生成源は引き続き codexapp のみ
  (非ゴール参照)。`nudge` の allowlist は変更しない。

## 移行

- **旧キーの repo 遮断は一般化と同時に行う**。panelaunch 一般化(#544)の
  時点で旧 `codexPlanMode` は claude / opencode の権限姿勢も動かすように
  なるため、同 PR で `RepoEditable=false` 化 + `repoOverrides()` の
  strip+warn を前倒しする。repo config が user 設定を上書きして子を auto
  へ落とせる中間状態を作らない。キーの全廃は #545。
- config.json の旧キー `codexPlanMode` は専用警告(3 キーへの置き換え案内)
  を出して無視する。`FANOUT_CODEX_PLAN_MODE` も同様に検出して警告する。
- 旧 CLI フラグは unknown option エラーになる(意図した breaking)。Tier 1
  bats の該当ケースと、Tier 2 の codex-plan シナリオ(flag / user-config /
  env の 3 経路)は新設定での検証に置き換える。
- **旧フラグを転送する配布 integration はフラグ削除と同一 PR(#545)で
  更新する**: `claude/commands/fanout.md`、`claude/skills/fanout/SKILL.md`、
  `codex/skills/fanout/`(SKILL.md / references/batch-workflow.md /
  references/cli-modes.md)、fanout-plan SKILL.md(claude / codex)の
  `--codex-plan-mode` 言及。#545 だけが入った main から `make install`
  した integration が unknown option なコマンドを生成する窓を作らない。
- 残りの利用者向けドキュメント(README ペア、site の settings / agents /
  cli / workflow / monitoring / changelog / herdr-backend の en+ja)は
  #548 で一括同期する。repository instruction は該当実装と同じ PR で
  更新する: CLAUDE.md / AGENTS.md の「never Codex Plan Mode」(#546)、
  CLAUDE.md の「`/api/plan` は codexPlanMode 記録ペイン限定」(#543)。

## 非ゴール

- claude / opencode の `@fanout_agent_state` `plan` 報告。claude の launch
  hooks に plan 提示を判別できるイベントがなく、承認プロンプトは既存の
  `blocked` 遷移で可視化される。「`plan` 値の生成源は codexapp のみ」の
  契約を維持し、peermsg allowlist・TUI glyph・dashboard への波及を避ける。
- codex オーケストレーターの gate 対応 plan TUI(handshake の再構成)。
- herdr backend での plan TUI 起動。
- claude auto mode の capability 検査と素起動フォールバック設定。非対応
  環境の起動失敗は fail loud で表面化し、必要になったら follow-up で
  検討する。

## 実装分解

親 issue #539 に Sub-issues + `## Blocked by` で wave を張る。1 issue =
1 PR。デザイン / フロントエンド作業は #543(ロジックのみ)を除き不要。

| Wave | issue | 内容 | クラス |
|---|---|---|---|
| 1 | #540 | CodexPlanMode → PlanMode の機械的リネーム(挙動不変、JSON キー据え置き) | H(機械的) |
| 1 | #541 | core/agent の mode-aware builder 追加(additive) | M |
| 1 | #542 | settings 3 キー追加(消費なし、repoOverrides gate) | H |
| 2 | #543 | dashboard / web の codex 限定ゲート + docsync(← #540) | H + web |
| 2 | #544 | panelaunch 一般化 + 非 plan 明示化 + goldens 再生成(← #540 #541) | H |
| 3 | #545 | childPlanMode 消費 + codexPlanMode 全廃(← #542 #544) | H |
| 3 | #546 | newSessionPlanMode 消費 + CLAUDE.md / SKILL.md 更新(← #542 #544) | H |
| 3 | #547 | orchestratorPlanMode 消費 + codex gate フォールバック(← #542 #544) | M |
| 4 | #548 | 利用者向け docs 一括同期(← #543 #545 #546 #547) | 文書 |

## 参照

- 先行検討: issue #472(childPlanMode の初期設計。CLOSED、本文書が
  supersede)
- 実装の中心: `internal/core/agent/agent.go`、
  `internal/app/panelaunch/{request.go,panelaunch.go}`、
  `internal/infra/settings/settings.go`、`cmd/fanout/tui_launch.go`、
  `cmd/fanout/tui_orchestrator.go`
