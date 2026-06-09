# PR レビュー可視化 v2 — 「一行一行読まないレビュー」構想

ステータス: 提案(proposal)。本書は実装計画ではなく、複数案の構想カタログ。
作成: 2026-06。調査は 5 系統の Web 調査(出典 URL 付き)+ 3 視点での案生成
(15 案)+ 統合選別 + リポジトリ実コードでの実現性検証を経ている。

## 背景

fanout は親 issue の子 issue ごとに tmux ペイン + git worktree を作り、AI
エージェントを並列起動する。その帰結として「AI が大量にコードを書き、人間が
複数の子 PR を同時にレビューする」状況が日常になる。生成は並列化できたが、
レビューは人間 1 人に直列で積み上がる — これが本構想の出発点。

#121 系(構造化 PR 本文・Review effort スコア・ゲート付き Mermaid・親 issue
ダッシュボード・`--status --format table`・常駐 TUI)は実装済みだが、これらは
「読むのを楽にする」段階に留まる。読む行為そのものは残っており、レビュー総時間
は子 PR の本数に比例して増え続ける。本書の目標はその先 — **人間がコードを
一行一行読むこと自体をなくす**ための機能群の構想。

fanout には汎用レビューツールにない構造的優位が 4 つある。各案はこの優位の
どれかを梃子にしている。

1. **子 issue の意図(仕様)を CLI が既に持っている** — internal/ghissue が
   issue 本文を取得でき、「要求との照合」をゼロ配線で組める
2. **並列子 PR 群という比較対象がある** — state.json が兄弟集合を知っている。
   PR を独立に扱う汎用ツールには原理的に作れない検証(結合検証・横断レビュー)
   が可能
3. **書いた本人のエージェントがペインでまだ生きている** — 指摘を「文脈を全部
   持った張本人」に一打で差し戻せる
4. **briefing で子エージェントの行動を設計できる** — PR 本文の構造・証拠の
   提出形式・自己検証手順を親側から強制できる

アーキテクチャの鉄則(CLAUDE.md)は本書の全案で維持する: **CLI は LLM を
呼ばない**。LLM 文脈が要る要約・図・判断は skill / briefing 側の仕事で、CLI は
機械的な配線・集計・GitHub API 呼び出しのみ。

## 調査サマリ(2025-2026)

### 商用 AI コードレビューツール

2025 年前半まで「inline コメント自動投稿」で横並びだった各社は、2025 年後半〜
2026 年に二軸へ分化した。一つは **diff の提示そのものの再設計**(Devin Review /
cubic / Linear Diffs: 論理順への hunk 並べ替え・案内文・移動検出)、もう一つは
**組織からの学習ループ**(Cursor Bugbot の learned rules、Greptile の長期メモリ、
Baz の Discussion Memory)。アーキテクチャはほぼ全社が agentic 設計+検証
ステップ(誤検出フィルタ)へ移行し、Copilot のように LLM 指摘と決定的ツール
(CodeQL/ESLint)を一面に融合する流れが明確になった。

- CodeRabbit — TL;DR + walkthrough + 図 + Review effort という「定型レビュー
  出力」の確立者。fanout の構造化 PR 本文とほぼ同型(方向の答え合わせ)。
  https://www.coderabbit.ai/
- Qodo PR-Agent — Review effort スコア(1-5)の元祖。スコアは「どれから読むか」
  の相対比較用。 https://github.com/qodo-ai/pr-agent
- Cursor Bugbot — 指摘がマージ前に修正された率 = **resolution rate** を主要
  指標とし 52%→80% に改善。「指摘の良し悪しは修正行動で測る」。
  https://cursor.com/blog/bugbot-learning
- GitHub Copilot code review — LLM 指摘と CodeQL/ESLint の決定的シグナルを
  一つのレビュー面に融合(2025-10)。
  https://github.blog/changelog/2025-10-28-new-public-preview-features-in-copilot-code-review-ai-reviews-that-see-the-full-picture/
- OpenAI Codex review — P0/P1 のみ報告する絞り込み哲学。AGENTS.md の
  `## Review guidelines` でレビュー観点を統制。
  https://developers.openai.com/codex/integrations/github
- Claude Code code review — 発見→検証→重大度ランクの多段構成。check run に
  機械可読な severity counts JSON を埋め込む。 https://code.claude.com/docs/en/code-review
- Devin Review — diff をアルファベット順でなく**論理順に並べ替え、hunk ごとに
  案内文**を付ける(2026-01)。「一行一行読まないレビュー」の現時点の最先端。
  https://cognition.ai/blog/devin-review
- cubic — 同じく論理順 diff ソート。スタートアップ 2 社が同時に「読み順の
  再設計」に賭けている市場シグナル。 https://www.cubic.dev/
- Martian Code Review Bench — 初の独立系ベンチ。**最良ツールでも既知問題の
  63% しか検出できない** = AI レビューは人間レビューの代替ではなく「読む量と
  順序の最適化装置」。 https://withmartian.com/post/code-review-bench-v0
- CodeSee の顛末 — 可視化専業は事業として立たず 2024 年 GitKraken に吸収。
  「図は常設の独立価値ではなく、レビュー文脈の中で必要時だけ出す付属物」。
  https://www.gitkraken.com/blog/gitkraken-launches-devex-platform-acquires-codesee

### 「コードを読まずに変更を信頼する」パラダイム

収束点は「**diff という成果物ではなく、外部基準(仕様・テスト・不変条件・
リスクスコア)との一致を検証する**」。理論的警告が 2 本ある: 同族 LLM の生成と
レビューは誤りが相関し循環する(arXiv 2603.25773)、LLM の仕様適合判定自体に
系統的バイアスがある(arXiv 2508.12358)。よって LLM-as-judge を唯一のゲートに
せず、決定論的検証と異種モデル交差で脱相関させる必要がある。

- Meta RADAR(arXiv 2605.30208)— リスクスコアで「人間レビュー不要な diff」を
  仕分け。53.5 万 diff 中 33.1 万を自動承認、**revert 率は通常の 1/3**。
  https://arxiv.org/abs/2605.30208
- Ona — リスク判定を LLM でなく **6 つの機械的基準**(行数・migration・auth
  接触等)で行い、承認時間 -98%。「マージボタンは常に人間が押す」(承認と
  マージの分離)。 https://ona.com/stories/auto-approving-low-risk-prs
- Intercom — 約 19% の PR を完全自動承認。「大きすぎる PR は AI が承認拒否して
  分割を強制」という粒度への正のインセンティブ設計。
  https://www.intercom.com/blog/ai-is-approving-our-pull-requests-heres-how-we-made-it-safe/
- OpenAI「Verifying Code at Scale」— 検証専用モデル+実行能力で 1 日 10 万 PR。
  再現率より精度を優先して信頼を獲得。「検証は生成より少ない計算量で効く」。
  https://alignment.openai.com/scaling-code-verification/
- Augment Code「Review the spec, not the code」— レビューを「意図と実装の契約
  検証」へ転換。仕様を唯一のスケーラブルな検証基準と位置づける。
  https://www.augmentcode.com/blog/review-the-intent-not-the-code
- Latent.Space「How to Kill the Code Review」— 「We're not going to outread
  the machines」。人間の関与を仕様段階へ前倒しせよ。
  https://www.latent.space/p/reviews-dead
- 実証研究(arXiv 2509.14745)— エージェント PR 567 件中、無修正マージは
  54.9%。**45.1% は人間の追加修正が必要** = ノーチェック merge はまだ早い。
  https://arxiv.org/abs/2509.14745
- Anthropic ポストモーテム(2026-04)— ユニットテスト・E2E・自動検証・
  dogfooding の**全層を通過してもバグは抜けた**。レビューをマージで終わらせず
  ソーク・段階導入まで含める必要。 https://www.anthropic.com/engineering/april-23-postmortem
- Agentic PBT(arXiv 2510.09907)— LLM がプロパティを推論して property-based
  test を合成、上位指摘の 86% が有効。実装エージェント自身のテストの自己採点
  循環を破る素材。 https://arxiv.org/abs/2510.09907
- Sonar 調査(verification debt)— 開発者の 96% は AI コードを完全には信頼
  しないのに、コミット前にレビューするのは半数未満。
  https://www.itpro.com/software/development/software-developers-not-checking-ai-generated-code-verification-debt

### 可視化技術

生き残った可視化と死んだ可視化の線引きは一貫している。生き残るのは「**PR/変更
単位で、その瞬間のために生成され、既存のレビュー画面(GitHub コメント・端末)
に内接する使い捨ての可視化**」(SemanticDiff・Difftastic・walkthrough 系)。
死ぬのは「コードベース全体の常設地図」(CodeSee、CodeViz への HN 批判)。
GitHub 上の表現上限は事実上 Markdown 表 + Mermaid + `<details>` であり、専用
ビューアへ誘導した CodeSee は敗れた — fanout の Mermaid 一択 diagram gate は
この教訓に既に沿っている。いま最も伸びている軸は図ではなく「**順序**」。

- SemanticDiff / Difftastic — 挙動に影響しない変更を diff から消す言語認識
  diff。「行ではなく意味の変化」を見せる層。 https://semanticdiff.com/ /
  https://difftastic.wilfred.me.uk/
- Linear Diffs Guided Reviews — diff を意味順に再編成し、本質的変更と
  glue code を分離(2026 年商用最前線)。 https://linear.app/diffs
- narrated-diffs — 「物語として読める diff」の原型 OSS。作者の手作業ゆえ普及
  しなかった — **並べ替えと注釈を書いた本人の LLM が担えば実行コストはゼロに
  なる**。 https://github.com/tbroadley/narrated-diffs
- tldraw pr-walkthrough skill — 「理解の論理順に 6-10 セグメント」「ナレーション
  は常に具体コードにアンカー」を LLM に強制する skill 実例。
  https://raw.githubusercontent.com/tldraw/tldraw/main/skills/pr-walkthrough/SKILL.md
- CodeScene Delta Analysis — リスクプロファイルで推奨レビュー強度を変える。
  temporal coupling 違反(普段一緒に変わるファイルの片割れ欠落)は **git 履歴の
  純機械計算**で検出でき、LLM 禁止の CLI 側に置ける数少ない高付加価値機能。
  https://docs.enterprise.codescene.io/versions/6.6.15/guides/delta/automated-delta-analyses.html
- GitHub ネイティブ描画 — Mermaid/GeoJSON/STL のみ。D2/Graphviz は不可。
  https://docs.github.com/en/get-started/writing-on-github/working-with-advanced-formatting/creating-diagrams

### AI スケール時代のレビュー運用

業界は「人間がコードを読む」を「人間が判断する」に置き換える方向へ収束し、
判断は 3 層に分解されつつある: (1) 決定的・機械的ゲート、(2) AI 多段レビュー
(発見→検証→ランク付け)、(3) 人間の承認(高リスク箇所の精読と triage 済み
findings への accept/skip)。繰り返し現れる設計原則: **承認とマージの分離**
(Ona)、**大 PR は分割を強制**(Intercom)、**リスク判定は LLM でなく監査可能
な機械的基準で**(Ona — fanout の CLI-no-LLM 鉄則と同型)。

- Anthropic Code Review — 発見→検証→重大度ランクの多段。PR 規模でエージェント
  数をスケール。承認は人間の仕事と明言。 https://claude.com/blog/code-review
- Every/Cora「I Stopped Reading Code」— 13 種の専門レビュアー +
  `/triage` で「採用/スキップ/個別指示」の判断だけを行う個人ワークフロー。
  https://every.to/source-code/i-stopped-reading-code-my-code-reviews-got-better
- ctx.rs「Why Coding Agents Need a Merge Queue」— 「**A 単体緑・B 単体緑・
  A+B 赤**」は一行一行読んでも原理的に発見できない欠陥クラス。1 人が並列
  エージェントで作り出した並行性から自分を守る 2 ゲート制。
  https://ctx.rs/blog/merge-queue-for-agents/
- Graphite stack-aware merge queue — 小 PR 多数戦略はマージ側インフラが対応
  しないと破綻する。スタック一括 CI + 失敗時二分探索。
  https://graphite.com/blog/the-first-stack-aware-merge-queue
- Cloudflare — 変更規模に応じてレビューエージェント数を変える effort routing
  の実運用(30 日で 13.1 万レビュー)。 https://blog.cloudflare.com/ai-code-review/
- GitHub Blog「Agent PR の 5 つの危険信号」— テスト削除等の CI ゲーミング・
  重複実装・hallucinated correctness 等。「10 分レビューフレームワーク」。
  https://github.blog/ai-and-ml/generative-ai/agent-pull-requests-are-everywhere-heres-how-to-review-them/
- Burak Dede「The Pull Request is Dead」— 1 体が書き、2 体目の敵対エージェント
  が壊しにかかり、3 体目が監査する三体構成。人間は構文でなく意図と制約を
  レビューする「制約の設計者」へ。
  https://burakdede.com/blog/the-pull-request-is-dead-surviving-the-ai-code-avalanche/
- GitHub Copilot /fleet — 公式も並列エージェントに踏み込んだ。共有 FS の
  「無言上書き」問題を fanout は worktree 分離で構造的に回避済み(差別化点)。
  https://github.blog/ai-and-ml/github-copilot/run-multiple-agents-at-once-with-fleet-in-copilot-cli/

### ターミナル / TUI

「TUI は配線、diff 描画は delta へパイプ」という委譲パターン(gh-dash と
diffnav の作者 dlvhdr が確立)が fanout の鉄則と同型。fanout console は構造化
PR 本文という独自資産(score/TL;DR/Changes by file)を持つため、描画部品を
足すだけで他ツールにない俯瞰が作れる。

- delta / diffnav — diff 描画の委譲先。`gh pr diff | diffnav` で PR 直接閲覧。
  https://github.com/dandavison/delta / https://github.com/dlvhdr/diffnav
- gh-dash — 複数 PR 俯瞰 TUI の最有力。スタックが fanout TUI と同一
  (bubbletea + lipgloss + delta + gh)。 https://github.com/dlvhdr/gh-dash
- claude-squad — tmux + worktree + エージェント管理 TUI で fanout の最近接
  同類。diff プレビュータブを内蔵。GitHub 側の issue/PR 構造との接続が薄い点が
  fanout との差。 https://github.com/smtg-ai/claude-squad
- gh-pr-review — レビュースレッドを LLM 向け決定的 JSON で読み書き・resolve
  できる gh 拡張。「指摘対応の往復はエージェント同士で閉じる」配線の既製品。
  https://github.com/agynio/gh-pr-review
- prr — レビューをプレーンテキストファイル化して一括 submit。Web UI 非依存
  レビュー動線の具体例。 https://github.com/danobi/prr
- mermaid-ascii — Diagram gate の Mermaid を端末描画できる Go 製ツール。
  https://github.com/AlexanderGrooff/mermaid-ascii
- ntcharts / lipgloss/tree — ヒートマップ・スパークライン・ツリー描画の既製
  部品。fanout TUI と同スタック。 https://github.com/NimbleMarkets/ntcharts /
  https://pkg.go.dev/github.com/charmbracelet/lipgloss/tree

## 案カタログ

7 案。tier は near-term(6 ヶ月内に確実に出荷可能)/ ambitious(挑戦的)/
moonshot(実験的)。スコアは 画期性 35 + fanout 適合性 25 + 実現可能性 20 +
リスク管理可能性 20 = 100 点満点。**全案がリポジトリ実コードでの実現性検証を
通過済み(verdict: viable-with-changes)**。各案の「実現性検証より」は検証で
判明した必須の設計修正。

---

### 案 1: 機械式リスクレーン 〔near-term / 86 点〕

関連 issue: (起票後に追記)

**コンセプト** — CLI が各子 PR を純機械シグナルのみで判定する: (a) 変更行数・
ファイル数、(b) riskPaths glob(migrations/auth/CI 設定/依存ファイル等)への
接触、(c) CI 結果(statusCheckRollup)、(d) テストファイル削除の検出。全基準
クリアなら `Lane=sample`(抜き取り検査で済む)、ひとつでも外れれば
`Lane=read`(人間精読)。ダッシュボード・`--format table`・TUI に Lane 列と
判定理由が並ぶ。マージボタンは常に人間が押す(承認とマージの分離)。

**なぜ読まなくなるか** — #121 の Review effort スコアは LLM 自己申告の「表示」
に留まり、意思決定に接続されていない。本案は機械基準を「この PR は読まなくて
よい」という明示的決定に格上げする。読む量の削減ではなく**読む対象の消去**で
あり、レビュー総時間が PR 件数に比例しなくなる。Meta RADAR(33.1 万 diff 自動
承認で revert 率 1/3)と Ona(6 機械基準で承認時間 -98%)の実証パターンの
fanout 移植。判定が決定論的なので、なぜ sample なのかを基準リストで監査できる。

**fanout 適合** — 判定・集計・列追加はすべて CLI の機械処理(鉄則適合)。
briefing 側には「sensitive path に触る変更は別 PR に分けよ」という粒度誘導を
追加(Intercom の分割強制の briefing 版)。

**段階的 MVP** — 第 1 段: Lane 判定エンジン + 保守的デフォルト riskPaths 同梱
(設定ゼロで動く)+ ダッシュボード/table の Lane 列。第 2 段: `.fanout/
config.json` での riskPaths カスタマイズと自己申告 findings マーカー。第 3 段:
`--merge` の確認ゲート(sample 以外は基準不一致理由を表示して確認)。自動
マージの解禁は運用実績を見てからの将来段として保留。

**リスクと緩和** — ラバースタンプ化 → 「自動承認」と呼ばず「抜き取り検査」と
命名し、sample 群からの定期サンプリング精読を README に明記。CI ゲーミング
(テスト削除・skip 追加で緑にする)→ 無条件で read レーンに落とす機械検出を
デフォルトに含める。riskPaths の保守負荷 → 保守的デフォルト同梱 + revert が
起きたら該当パスを基準に足す事後学習運用。

**実現性検証より** — MVP 第 1 段はブロッカーなしで着手可能。必須の設計修正:
(1) 「Claude Code check run の severity JSON」シグナルはこのリポジトリに check
run が存在しないため、**PR 本文内マーカー
`<!-- fanout:review-findings {...} -->` に差し替え**、かつ自己申告なので
「悪い方向にだけ効く片方向シグナル」(🔴>0 なら無条件 read 落ち)に限定する。
(2) **fail-closed を判定関数の不変条件にする**: シグナル取得失敗・files 100 件
制限超過・CI 未走行はすべて read。(3) glob は依存追加せず限定セマンティクスの
自前実装(約 40 行)。(4) CI 状態は `gh pr view --json` への相乗りで取得。

**参考** — Meta RADAR(arXiv 2605.30208)、Ona、Intercom、GitHub Blog 危険信号、
CodeScene Delta Analysis

---

### 案 2: 読み順ナレーション + アンカー検証ガイドツアー 〔near-term / 82 点〕

関連 issue: (起票後に追記)

**コンセプト** — 第 1 形態(Markdown): PR 本文の TL;DR 直下に **Reading
order** セクションを置き、変更ファイルを「1. エントリポイント → 2. コア変更 →
3. 波及・glue → 4. テスト」の理解順グループに並べ、各グループに賢い同僚が案内
するような 1 文を付ける。Changes by file 表に core / glue / no-behavior-change
のタグ列を追加し、レビュアーは core グループ(典型 2-4 ファイル)だけ精読、
残りはタグを信じて読み飛ばす。第 2 形態(端末再生): briefing で機械可読な台本
JSON(file + 行範囲アンカー + ナレーション + タグの 5-10 セグメント)を書かせ、
CLI は台本を解釈せず**アンカーの diff 実在とカバレッジだけを機械検証**、
`fanout --walk` が j/k でセグメントを delta 経由で順送り再生する。

**なぜ読まなくなるか** — #121 の Changes by file 表は「何が変わったか」の静的
一覧であり、「どの順で・どこまで読むか」を伝えない。Devin Review・cubic・
Linear Diffs の複数社が同時に賭けた「diff の提示順の再設計」が現在の最前線。
narrated-diffs が「作者の手作業」ゆえ普及しなかった並べ替え+注釈を、**書いた
本人のエージェントが briefing の強制力で無料で担う**ため今なら成立する。汎用
ツールは事後に別 LLM が diff から順序を推測するが、fanout は実装の意図を持つ
author 本人が台本を書き、diff が手元にある CLI がアンカー実在を無コストで検証
できる — 精度の前提が違う。

**fanout 適合** — 第 1 形態は briefing 層のみ(CLI 変更ゼロ)。第 2 形態の CLI
は JSON スキーマ検証と hunk 照合という純機械処理で鉄則適合。hunk 描画は delta
へのパイプに委譲し自作しない。

**段階的 MVP** — 第 1 段: briefing テンプレに 1 セクション + 表に 1 列を追加し
golden を更新するだけの 1 PR。**新トグルは作らず既存 prVisualization に同梱**
(7 面プラミング回避)。第 2 段: 台本 JSON スキーマ + `--walk` 非 TUI 版。
第 3 段: TUI に j/k 再生ビュー統合。

**リスクと緩和** — 誤タグによる読み飛ばし事故 → 「確信がなければ core」の保守
的指示 + no-behavior-change は「git diff -w で空になる diff」という機械検証
可能な定義に限定。ナラティブ誘導(心地よいツアーで批判的視点を失う)→ 「自信
のない箇所・設計上の妥協を最低 1 セグメント含めよ」を briefing で強制。行範囲
アンカーの rebase ズレ → push ごとの台本再生成を義務付け、検証失敗セグメントは
欠落表示。

**実現性検証より** — 第 1 形態は `prVisualizationSectionTemplate`
(internal/briefing/briefing.go)への追記のみで完全に成立。第 2 形態の必須
修正: (1) state.json は baseBranch を記録していないため、**台本 JSON 自体に
base/head SHA を必須フィールドで持たせる**(rebase ズレ検出も機械検証に乗る)。
(2) 台本の正本は PR 本文 `<details>` 内のフェンス付き JSON とし、ローカルは
キャッシュ扱い(cleanup 後・別マシンでも `--walk` が動く)。(3) delta は必須
依存にせず、発見時のみパイプ。

**参考** — Devin Review、cubic、Linear Diffs Guided Reviews、narrated-diffs、
tldraw pr-walkthrough skill、SemanticDiff

---

### 案 3: ウェーブ結合検証ゲート + 収束レビュアー 〔ambitious / 86 点〕

関連 issue: (起票後に追記)

**コンセプト** — レビューの単位を「1 PR」から「**同一親 issue の子 PR 群
(ウェーブ)**」に変える。機械層: `fanout --verify-integration` が state.json
の全子ブランチを使い捨て一時 worktree に固定順で順次マージし、(1) テキスト
コンフリクトの発生ペア、(2) 結合状態での verifyCommand(例: make test)の実行
結果、(3) 2 つ以上の子が触ったファイルの重複表 + git 履歴からの temporal
coupling 違反を記録し、「Integration: 緑 / conflict(#12×#15) / test-red」として
ダッシュボード・TUI に表示。LLM 層: 全子 PR が出揃ったら**収束レビュアー**
ペインを 1 枚だけ起動し、ヘルパー重複実装・兄弟間の規約不整合・設計方向の衝突
という横断観点専用レポート 1 本を親 issue に投稿させる。人間は結合結果と横断
レポートだけを見て判断する。

**なぜ読まなくなるか** — #121 の可視化はすべて「PR 単体」の表示であり、並列
fanout 固有の最大リスク — 1 人が並列生成した子 PR 間の意図の衝突 — はどの層に
も映らない。ctx.rs が指摘する「A 単体緑・B 単体緑・A+B 赤」は**人間が一行一行
読んでも原理的に発見できない**欠陥クラスで、本案は読むレビューの省力化では
なく、読むレビューでは到達不可能な品質保証を機械実行で足す。人間が並列 PR を
通読する最大の動機である統合不安を機械的に潰し、N 本の diff の代わりに 1 本の
レポートを読ませる。兄弟集合を state.json で知る **fanout の独占領域**(汎用
ツールは PR を独立に扱うため原理的に作れない)。

**fanout 適合** — 順次マージ・コマンド実行・集計は CLI の機械処理。横断観点の
解釈は収束レビュアー(LLM)に分離。temporal coupling は git 履歴の純機械計算。

**段階的 MVP** — 第 1 段: `--verify-integration` が順次マージしてコンフリクト
ペア報告 + verifyCommand 実行(未設定なら merge 検証のみで安全に no-op)。
第 2 段: ファイル重複表・temporal coupling 警告・ダッシュボード列。第 3 段:
収束レビュアー `--converge` とペイン送り返し。fanout 自身の開発ウェーブが毎回
テストケースになる。

**リスクと緩和** — テストが重いリポジトリでの実行コスト → 自動実行せず手動
起動・opt-in に限定し、lint のみの軽量モードを用意。「勝手なマージ」事故 →
コンフリクト解決は絶対にしない(報告のみ)と仕様で固定し、一時 worktree は
終了時に必ず破棄。マージ順依存の揺れ → 結合順を固定し再現可能に。横断レポート
のハルシネーション → 指摘には両 PR のファイルパス引用を briefing で強制し、
CLI がパス実在を機械検証。

**実現性検証より** — ブロッカーなし。必須の設計修正: (1) マージ順は PR 番号順
でなく **issue 番号順**(state.json は PR 番号を持たず、オフライン再現性のため)。
(2) 結果は state.json に書かず **`.fanout/integration.json` を atomicfs で別
ファイル化**(state のロック規律を壊さない)。(3) 一時 worktree は
`.fanout/worktrees/` 配下に置かない — migration fallback 判定(slug サフィックス
一致)に誤マッチするため **`.fanout/verify/` を新設**し、defer で必ず
`worktree remove --force` + prune。(4) verifyCommand は任意コマンド実行になる
ため `--verify-cmd` フラグ明示を一級とし、config 記載時も opt-in を明示。
(5) 第 1 段の失敗報告は二分探索でなく「失敗ステップ + コンフリクトファイルを
過去に触った既マージブランチの機械特定」に留める(O(N²) の厳密特定は第 3 段)。
(6) ペイン送り返しは peer messaging (#68) 待ちが実質必須なので第 3 段に分離。

**参考** — ctx.rs merge queue for agents、Graphite stack-aware merge queue、
CodeScene temporal coupling、Anthropic ポストモーテム

---

### 案 4: Findings 裁可コンソール 〔ambitious / 84 点〕

関連 issue: (起票後に追記)

**コンセプト** — 常駐 TUI を「監視画面」から「**裁可装置**」に進化させる。CLI
が全子 PR の未解決レビュースレッド・CI 失敗・子エージェントが書き出す
`.fanout/findings/<slug>.json` を機械集計し、severity × Review effort 降順の
単一トリアージキューとして表示する。人間は各 finding カードに **a=accept**
(該当子ペインの生きているエージェントに「このスレッドの指摘のみに対応し、
push と resolve せよ。他は触るな」の固定テンプレを tmux 経由で送信)/
**s=skip**(既読化)/ **d=深掘り**(delta/diffnav へパイプして該当 hunk 表示)
の三択だけを行う。後続の `--status` でスレッド resolve 状態を再集計し、「掲載
指摘のうち修正 push に至った率」= **fanout 版 resolution rate** と skip 率を
ダッシュボードに常時表示する。

**なぜ読まなくなるか** — #121 のダッシュボードは「どれから読むか」を助けるが、
指摘→修正の往復は依然として人間が各 PR 画面と各ペインを行き来する最大の時間
泥棒のまま。本案は Every/Cora が実証した「読む → ランク付けされた findings
への二値判断」への責務転換を、fanout だけが持つ構造的優位 — **文脈を全部持った
張本人のエージェントが各 worktree ペインでまだ生きている** — に接続する。
CodeRabbit の Fix-with-AI は Web 上でエージェントを新規起動するが、fanout は
「書いた本人に一打で差し戻せる」。人間の単位作業が「diff を読む」から「判断を
1 つ下す」に変わり、Cursor Bugbot が resolution rate を 52%→80% に改善した
自己改善ループの入口(計測)も同時に手に入る。

**fanout 適合** — 集計・キュー構築・tmux send・resolve 率計算は CLI の機械
処理。findings の生成は briefing 指示(LLM)。TUI は既存 bubbletea 基盤の拡張。

**段階的 MVP** — 第 1 段: briefing に findings JSON 書き出し指示を追加し、
`fanout --status --findings` が全子の JSON を集計して severity 順テーブルを
端末表示(TUI 無改修)。第 2 段: TUI に a/s/d トリアージビュー。第 3 段:
resolve 配線と resolution rate 表示。

**リスクと緩和** — s 連打によるラバースタンプ化 → severity 🔴 は s 不可で d か
明示理由入力を要求する摩擦設計 + skip 率と resolution rate を同じ画面に常時
表示して自己監視可能にする。エージェント送信の暴走 → 送信文言を「このスレッド
のみ・他は触るな」の固定テンプレに固定。findings の偽陽性による信頼崩壊 →
briefing で件数上限と確信度フィルタを指示し、skip 率で指示文を較正。

**実現性検証より** — 必須の設計修正が 1 つ(安全性): fanout のペインは
エージェント終了後に `exec $SHELL -l` でシェルに戻るため、「**ペインが live ≠
エージェントが生きている**」。エージェント終了後のペインに send-keys すると
修正指示文が**シェルコマンドとして実行される**。送信前に
`#{pane_current_command}` でエージェントプロセスを確認し、不在なら PR コメント
投稿にフォールバックするガードを必須要件に昇格。その他: findings の書き出し先
は worktree 内でなく `<projectRoot>/.fanout/findings/`(子 PR の git status を
汚さない)、新スイッチは default-off で導入(golden の全面再生成を初回から
切り離す)、resolution rate の状態は state.json に混ぜず別ファイル。

**参考** — Every/Cora、CodeRabbit Fix with AI、Cursor Bugbot resolution rate、
gh-pr-review、claude-squad、dlvhdr 流「TUI は配線、描画は委譲」

---

### 案 5: 受け入れ基準エビデンス・ゲート 〔ambitious / 83 点〕

関連 issue: (起票後に追記)

**コンセプト** — fanout-issues skill が子 issue 作成時に **Acceptance
criteria**(検証可能な箇条書き 3-7 個、AC-1.. の機械可読 ID 付き)を必須
セクション化し、**人間はウェーブ開始前にこの基準だけを承認する** — ここが人間
レビューの主戦場になる。briefing は子エージェントに PR 本文へ「AC ID × 検証
コマンド × 実行ログ抜粋」の **Evidence 表**(基準本文は issue から逐語引用、
生ログは `<details>` にコピー&ペーストのみ・要約禁止)を義務付ける。CLI は
LLM を呼ばずに照合する: issue 本文から AC ID を抽出して PR 本文の Evidence 表
と突き合わせて欠落を検出し、表の検証コマンドを再実行して green/red を記録、
ダッシュボードに「Spec n/m ✓」列を出す。人間が PR で見るのは diff ではなく
「自分が承認した基準が、どのテストで、どんな実行結果で証明されたか」の対応表
になる。

**なぜ読まなくなるか** — #121 の Test plan 欄は「何をするつもりか」の予定で
あり証明ではない。本案は読む対象を **diff から仕様に置換**するため、契約が
充足されている限り diff を開く理由そのものが消える。「同族モデルの生成と
レビューの誤り相関」(arXiv 2603.25773)を外部仕様への固定で、「LLM の仕様
適合判定バイアス」(arXiv 2508.12358)を CLI によるコマンド再実行という決定論
的証拠で回避する — 理論的警告 2 本に正面から答える構成。fanout は子 issue の
意図テキストと検証を走らせる worktree を最初から管理下に持つため、Greptile が
MCP 連携でやっと実現した「要求との照合」をゼロ配線で再現できる。

**fanout 適合** — AC 起草・Evidence 記述は skill/briefing(LLM)。AC ID 抽出・
表突合・コマンド再実行・列追加は CLI の機械処理。briefing には「codex 子に
codex review を課す」既存前例があり、claude 実装 × codex レビューの異種交差も
briefing パターンの組み換えで実現可能。

**段階的 MVP** — 第 1 段: fanout-issues SKILL と briefing の文言追加のみで CLI
無改修(人間が目視で表を見る)。第 2 段: `--status` に AC 突き合わせと
「Spec n/m」列。第 3 段: `fanout --verify <issue>` が Evidence 表のコマンドを
再実行して green/red を記録。第 4 段(任意): 異種交差レビュー。各段が独立に
出荷可能。

**リスクと緩和** — 最大リスクは hallucinated correctness(契約を満たす最弱
実装・弱いテストでの緑化)→ CLI が実際にコマンドを再実行 + テスト削除/skip
追加の機械検出 + 「変更対象のプロパティを 1 つ推論し property-based test を
書け」を基準テンプレに含める(arXiv 2510.09907: 上位指摘の 86% が有効)。
コマンドで検証できない基準 → 人間裁可に明示的に残す。issue 作成コスト増 →
fanout-issues がドラフトを生成し人間は添削のみ。

**実現性検証より** — 必須の設計修正: (1) 第 3 段の「PR 本文由来コマンドの再
実行」は **Markdown 経由の任意コード実行**であり、`.fanout/config.json` の
allowlist(前方一致 "go test" / "make " 等)に通ったものだけ実行し、不一致は
red でなく "unverifiable (manual)" として人間裁可に残す。settings 機構の
bool 専用制約の拡張が前提。(2) `--verify` の実行先は live worktree でなく
**PR head SHA の一時 detached worktree**(稼働中エージェントとの競合と cleanup
後の worktree 消失を同時に回避)。(3) 異種交差の AGENTS.md worktree 配置案は
追跡済み AGENTS.md と衝突して子 PR を汚染するため不可 — **briefing 文字列で
交差させる**(codex→claude 方向は配線未検証なので moonshot に切り離し)。
(4) Evidence 表は「AC が issue に存在する場合のみ」のゲート付きで後方互換。

**参考** — Augment Code、arXiv 2603.25773 / 2508.12358 / 2603.17150 /
2510.09907、Latent.Space、Swimm、OpenAI Codex AGENTS.md

---

### 案 6: 挙動差分証明書 〔moonshot / 80 点〕

関連 issue: (起票後に追記)

**コンセプト** — レビューの一次成果物をコード diff から「**観測可能な挙動の
diff**」に置換する。CLI が base と head の両方に対して同一のプローブ群(既存
テストスイート + golden + CLI 出力比較)を機械的に実行し、「変わらなかった
挙動 N 件 / 変わった挙動 M 件」という**証明書**を生成、変わった M 件の
before/after 出力だけを人間に提示する。refactor 型 PR は「挙動差分ゼロ証明書」
(プローブ範囲内)が発行された時点で diff を読む理由が原理的に消滅する。挙動
変更を伴う PR では briefing が「意図した挙動差分の宣言」を PR 本文へ書かせ、
CLI が実測差分と機械的に突合して「宣言どおり / 宣言外の差分あり」を判定 —
宣言外が 1 件でもあれば赤バッジ、それが人間の唯一の精読ポイントになる。fanout
自身が Tier 2 goldens で自分に対して行っている検証の、子 PR への一般化。

**なぜ読まなくなるか** — Devin Review や cubic の最先端ですら「diff の提示順と
粒度の再設計」に留まるが、本案は**提示物そのものをテキストから実行証拠に置き
換える**。#121 の可視化は「コードを要約して見せる」、本案は「コードを見せない」。
LLM の要約と違い証明書は再実行可能で、信じる必要がなく検算できる — 信頼では
なく検証に基づく唯一の moonshot。

**fanout 適合** — CLI が主役になれる数少ない案: プローブ実行・出力比較・証明書
生成は完全に決定論的で LLM 不要、internal/worktree の基盤を転用できる。
briefing 側は「意図差分の宣言」記述という LLM の仕事で分業が明確。

**段階的 MVP** — 第 1 段: `fanout --verify <issue>` を fanout 自身でドッグ
フード(base/head 両方で make test + golden 比較、証明書を PR コメント投稿)。
第 2 段: 宣言突合 + ダッシュボード列。第 3 段: 低 effort スコア PR への限定
実行スイッチ。

**リスクと緩和** — 最大リスクはプローブ網羅性が低いときの偽りの安心(Anthropic
ポストモーテムが示す通り全層検証をすり抜けるバグは存在する)→ 証明書に実行
プローブの一覧と件数を必ず明記し、「差分ゼロ」を「プローブ範囲内で差分ゼロ」
と表現する規律を仕様で固定。非決定的出力 → 正規化フィルタと再実行リトライ。
実行コスト 2 倍 → refactor 型 PR 限定スイッチ。

**実現性検証より** — 検証で**設計の根幹に関わる穴**が判明: プローブの実体
(テスト・golden・config)は head ブランチでエージェント自身が書き換えられる
ため、挙動を変えつつ golden も更新すれば差分ゼロに見える — **監査対象が物差し
を動かせる**。対策を MVP 第 1 段から仕様に入れること: (1) **物差しを base に
固定**(probe 宣言は merge-base 時点のものを使う)、(2) head がプローブ関連
パス(tests/, golden/, config)に触れていたら証明書に「⚠ probe inputs
modified」を必須表記し、差分ゼロでも緑バッジを出さない(git 操作のみで実装
可能)。その他: 宣言突合は probe ID の集合比較のみに固定(散文との照合は LLM
仕事になり鉄則違反)、検証は `.fanout/verify/` の使い捨てチェックアウトで実行、
件数の語彙は「実行プローブ N 件中差分 M 件(一覧添付)」と正直に。

**参考** — Swimm behavioral equivalence、fanout 自身の Tier 2 goldens、
SemanticDiff、Agentic PBT、Anthropic ポストモーテム

---

### 案 7: クロスモデル相互レビュー法廷 〔moonshot / 75 点〕

関連 issue: (起票後に追記)

**コンセプト** — 各子 PR に対し、実装エージェントとは**異種モデルの「検察
ペイン」**を fanout が自動で隣に split する(claude 実装なら codex 検察、逆も
同様)。検察は diff を読むだけでなく worktree で実行能力を持ち、エッジケース
テストを書いて実際に壊しにかかる。「**赤いテストを提示できた指摘」だけが起訴
として成立**し、実装側(弁護)は修正 push かテストでの反駁で応じる。規定
ラウンド後、判決 JSON(罪状ごとに 有罪=未修正バグ / 和解=修正済み / 誤認=
検察のテストが不当)が PR にコメントされ、人間が読むのは「起訴 3 件、2 件和解、
1 件係争中」という数段落の判決文だけになる。

**なぜ読まなくなるか** — #121 は実装エージェント自身の自己申告を整形する仕組み
で、**書き手と語り手が同一**という弱点が残る。本案は (1) 異種モデル交差で誤り
の相関を脱し(arXiv 2603.25773)、(2) 指摘に「実行して赤くなるテスト」という
決定論的証拠を必須化することで LLM 指摘のハルシネーションを構造的に排除する。
人間の役割は読解から、証拠が揃った係争への裁定だけに縮む。7 案中唯一「指摘の
真正性」そのものを機械証拠で担保する案。

**fanout 適合** — ペイン split・判決 JSON の機械パース・ダッシュボード列は CLI
の配線。訴追手続き・証拠要件・判決フォーマットの規定は skill/briefing(LLM)。

**段階的 MVP** — まず非対話の一審制から: briefing に「PR 作成後、異種モデルで
レビューし、指摘は再現テストを書いて赤を示せたものだけ JSON で PR コメント
せよ」を追加。CLI は判決 JSON を拾い `--status` に列を足す。対審ラウンドと検察
専用ペインは第 2 段。検察起動は高リスク PR に限定する effort routing と組み
合わせる。

**リスクと緩和** — 計算コスト約 2 倍 → 高リスク PR 限定の起動。両エージェント
のなれ合いによるラバースタンプ化 → 起訴ゼロの判決には「試みたが壊せなかった
攻撃テスト一覧」の提出を義務付け、空の判決を許さない。係争の空転 → ラウンド
上限 2 で打ち切り人間へエスカレーション。対審プロトコル全体が briefing 文言の
遵守に依存する点が最大の不確実性。

**実現性検証より** — MVP(briefing 経由 + 判決 JSON + ダッシュボード列)は
既存レールに載る。フル法廷(検察ペイン自動 split)の障害: (1) state.json の
冪等キーが (parent, issueNum) で 1 issue 1 ペイン前提 — 検察ペインは合成
parent(`@prosecutor` 等)で回避。(2) fanout はワンショットプロセスでポスト
PR トリガ機構がない — 自動 watcher でなく明示コマンド `--prosecute <N>` から
始める。(3) worktree は PR head の fetch + 検察サフィックス付きブランチの新
モードが必要(同一ブランチ二重 checkout 禁止)。(4) effort routing は自己申告
の Review effort 単独でなく **CLI が計算する客観値(diff 規模)と合成**(低
申告による起訴回避を塞ぐ)。(5) 対審チャネルは SQLite (#68) でなく **PR
コメント/レビューを正**とする(監査可能で #68 依存が外れる)。(6) 赤テストは
検察ブランチに commit させ判決 JSON に SHA + コマンドを記録(opt-in の CLI
リプレイで決定論的に検算可能)。

**参考** — Burak Dede 三体構成、OpenAI Verifying Code at Scale、Cloudflare
effort routing、OpenAI Codex AGENTS.md Review guidelines、arXiv 2603.25773

---

## 横断設計原則

7 案に共通して現れた原則。個別案の採否に関わらず、今後のレビュー系機能は
これらを満たすこと。

1. **fail-closed** — 機械判定はシグナルが欠けたら必ず安全側(read / 人間裁可)
   に倒す。「全シグナルが揃って全部クリア」のときだけ読まない側に倒せる。
2. **承認とマージの分離** — どの案も自動マージはしない。マージボタンは常に
   人間が押す(Ona)。自動マージの解禁は運用実績が立ってからの別判断。
3. **自己申告シグナルは悪い方向にだけ効かせる** — 実装エージェントの自己申告
   (Review effort、findings 数)は「read 落ち」の根拠にはできるが、「読まなく
   てよい」の根拠にはしない。読まない判定は CLI の客観値のみで行う。
4. **物差し汚染への防御** — エージェントはテスト・golden・config を書き換え
   られる。検証系の機能は「プローブ関連パスへの接触」を機械検出し、検証通過
   でも緑バッジを出さない。CI ゲーミング(テスト削除・skip 追加)は無条件で
   人間精読に落とす。
5. **GitHub ネイティブ + 端末内で完結** — 専用 Web ビューアは作らない(CodeSee
   の教訓)。表現は Markdown 表 + Mermaid + `<details>` と、TUI + delta 委譲の
   範囲に収める。
6. **計測を最初から仕込む** — 指摘の良し悪しは修正行動で測る(resolution rate、
   Cursor Bugbot / Martian Bench の手法)。「レビュー時間半減」を主張する機能は
   その実測手段を同梱する。
7. **settings 機構の制約** — internal/settings のローダーは bool 専用。文字列・
   配列設定(riskPaths、verifyCommands 等)が要る案は settings の schema 拡張
   か専用ローダーの新設が前提になる(既存 bool 5 スイッチの規律は壊さない)。
8. **任意コマンド実行の封じ込め** — PR 本文・config 由来のコマンド再実行は
   allowlist + opt-in + 確認プロンプトなしには出荷しない。

## 落選案

15 案から 5 組の重複統合と 2 案の不採用を行った。

- **エージェント信用台帳**(エージェントごとの実績スコアで与信)— 個人〜小
  規模運用ではサンプル数が立たず統計的与信が機能しない。計測要素(post-merge
  fix 追跡・resolution rate)は案 1 と案 4 が事実上カバーする。
- **無人マージレーン** — 内容はほぼ全量を案 1(機械基準ルーティング)と案 3
  (結合検証)に吸収。固有要素である「自動マージの解禁」は両案の運用実績が
  立つまで保留(承認とマージの分離の原則とも整合)。

## 採用判断の指針

推奨着手順:

1. **案 2 第 1 段(読み順ナレーション)** — briefing 文言のみ・CLI 変更ゼロ・
   1 PR で出荷可能。次の fanout 実行から全子 PR に効く。
2. **案 1 第 1 段(機械式リスクレーン)** — 「読まない PR」を初めて明示的に
   作る一手。fail-closed の判定エンジンと Lane 列まで。
3. **案 5 第 1 段(エビデンス・ゲート)** — fanout-issues / briefing の文言
   追加のみで「仕様を読むレビュー」の運用を試せる。CLI 突合(第 2 段)は運用
   の手応えを見てから。
4. **案 3 第 1 段(結合検証ゲート)** — fanout 自身の開発ウェーブをドッグ
   フードに `--verify-integration` を実装。
5. 案 4(裁可コンソール)は #68(peer messaging)や TUI の成熟と歩調を合わせ、
   案 6・案 7 は上記の運用実績から得た学びで仕様を固めてから着手する。

レビュー時間は「案 1 で読む対象を減らす → 案 2 で読むものの読み方を変える →
案 5 で読む対象を diff から仕様に変える → 案 3 で読んでも見つからないものを
機械に任せる」の順に複利で減る設計になっている。

## 関連資料

- 親 issue #121(変更内容ビジュアライゼーション v1、実装済み): 構造化 PR 本文・
  Diagram gate・ダッシュボードの設計の正典
- 本書の各案の issue: 各案見出し下の「関連 issue」を参照
