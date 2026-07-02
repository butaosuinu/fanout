# 開発ロードマップ（2026-07-02 棚卸し）

2026-07-02 の open issue / PR 棚卸しで確定した方針と着手順。判断と順序だけをここに置き、実装詳細は各 issue に書く。次回棚卸しで本ファイルを更新する。

## 方針: fan-out ツールから閉ループハーネスへ

コーディングエージェントの価値はモデル単体ではなく、その周りの運用基盤 — ツールオーケストレーション・検証ループ・コンテキスト・ガードレール・観測性の 5 層 — に移っている。この 5 層で fanout を見ると、オーケストレーション（tmux + worktree 隔離、wave、team msg）、検証ループ（post-work-review gate、pr-watch）、ガードレール（fail-fast、read-only dashboard、opt-in watcher）は既に強く、**観測性（トークン/コスト計測が無い）と着信イベントへの応答が弱い**。着手順はこのギャップを埋める順に組む。

3 段のループを内側から閉じる:

1. 内側 — 子エージェント単体の完結性。bounded review gate（PR #349）→ PR レビュー追従（#106）→ Wave 自動進行（#59）
2. 外側 — 無人化。label watcher（#220 済）→ skill ループ（#107）→ webhook（#292）
3. メタ — ハーネス自身の改善。retro（#373）

横串は 2 本。経済性（#105 可観測性 + #368 モデル指定）と人間帯域（#175 / #180 でレビュー時間を PR 数に対して劣線形にする）。

設計上の不変条件: CLI は決定論（LLM を呼ばない）、LLM 判断は skill 層に置く。モデルルーティングの外部報告でも、LLM の自己ルーティングは信頼できず（自己コスト予測の相関 ≤0.39）、明示指定 + 計測の組み合わせが有効とされる。この二層構造は崩さない。

## 着手順

| ティア | 時期目安 | 対象 |
|---|---|---|
| P0 | 即時 | open PR の消化（#349 → #353 → #352 → #350。TUI 系 3 本はマージ毎に残りを rebase） |
| P1 | 2026 Q3 前半 | #361 OpenCode 対応 / #105 トークン可観測性 / #313 横断ダッシュボード wave1（#304-306） |
| P2 | 2026 Q3 後半 | #368 モデル指定 / #106 レビュー追従 / #175 + #180 レビュー可視化 v2 MVP / #107 + #292 自動トリガー |
| P3 | 2026 Q4 | #373 retro / #313 wave2+ / #241 cursor/copilot / #59 Wave 自動進行 |
| Icebox | — | #321 Wails アプリ / #183-187（v2 後段）/ #58 Linear / #9 Windows。小粒は随時: #13 #14 #254 #8 #7 |

#105 を P1 に置くのは #368 の効果測定が依存するため。#361 を #241 より先行させるのは、opencode が provider-agnostic で、1 エージェントのままマルチ provider を検証できるため（#368 の土台になる）。

## エピック一覧（2026-07-02 時点）

| 親 | 内容 | 子 |
|---|---|---|
| #361 | OpenCode 対応（`--agent opencode`） | #357-360。#243 を blocker として借用 |
| #368 | モデル細粒度指定（`--agent NUM=name:model` + skill 自動推奨） | #362-367 |
| #373 | fanout retro — メトリクス収集とハーネス改善ループ | #369-372 |
| #313 | 全リポジトリ横断 Session 一覧（Web + TUI） | #304-312 |
| #241 | cursor / copilot エージェント対応 | #242-247 |
| #292 | webhook 無人起動（opt-in） | #287-291 |
| #175 / #180 | レビュー可視化 v2（リスクレーン / 読み順） | #176-179 / #181-182 |
| #321 | Wails ネイティブアプリ（オプション配布） | #314-320 |

#361 / #368 / #373 は 2026-07-02 の新設。#373 は「収集 = CLI（決定論）/ 分析・提案 = skill（LLM）」の分業で、briefing の自動書き換えはしない（提案 PR + 人間レビュー必須）。

## 判断記録

- #207 gh ネイティブ化: No-Go で close（2026-07-02）。gh は認証・ページネーション・GraphQL を肩代わりする妥当な必須依存。gh 起動コストが poller 等で実測ボトルネックになったら、その経路だけの部分ネイティブ化を再検討する
- #47 Claude GitHub Actions workflow: close。PR レビューは post-work-review + codex review のループで運用しており重複する
- #59: `--agent NUM=name` 多重化は実装済みのため、Wave 自動進行に絞って本文改訂
- #65 `--pick`: 常駐 TUI が実質代替。TUI に issue 一覧からの選択が入った時点で close 判断

## 参考

- [Faros — Harness Engineering](https://www.faros.ai/blog/harness-engineering)（5 層の整理）
- [Unblocked — Model Routing for Coding Agents](https://getunblocked.com/blog/model-routing-coding-agents/)（自己ルーティングの信頼性データ）
- [Self-Harness 解説](https://explainx.ai/blog/self-harness-agents-improve-themselves-arxiv-2026)（弱点マイニング → 提案 → 検証のループ。#373 の下敷き）
