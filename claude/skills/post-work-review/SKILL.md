---
name: post-work-review
description: 実装作業が一段落したコードを「仕上げ」モードに通すワークフロー。まず code-review プラグインで diff の bug をざっと洗い出して修正し、続けて codex:review を別視点として回し、指摘がなくなる (codex が approve / clean / 問題なし と返す) まで「review → 修正 → 再 review」をループする。ユーザーが「review して仕上げて」「post-review」「finalize」「コミット前にもう一度見て」「二重チェック」「codex review もかけて」等と言ったとき、または /post-work-review が明示的に呼ばれたときに、必ずこの skill を使う。実装後のチェックを口にしたら「dashboard」「visualization」のような明示語が無くてもこの skill を呼ぶこと — 同種の単一エージェントによるレビューより、二段構え + ループの方が見落としが少ない。
---

# post-work-review

実装が一段落したコードを、2 種類のレビュアーで仕上げるワークフロー skill。

## なぜこれが要るか

単発の code review は見落とすことがある。`code-review` プラグイン (内製のマルチエージェント) と `codex:review` (Codex CLI、別モデル別視点) を直列で通し、後者は「指摘なし」になるまでループさせることで、コミット直前に独立した二系統の最終チェックを掛けるのが狙い。本 skill はそのオーケストレーターであり、レビュー自体は実装しない。

## 適用範囲と前提

- **対象**: 現在の git 作業ツリーにある変更 (dirty でも commit 済みでも可)。`code-review` は内部で `git diff` 系を見るし、`codex:review` も `--scope auto` で同様に判定する。
- **git リポジトリ外なら早期終了**: `git rev-parse --is-inside-work-tree` で false なら、ユーザーに「ここは git リポジトリではないので post-work-review は使えない」と伝えて終了する。
- **diff が空なら早期終了**: `git status --porcelain` と `git diff main..HEAD --stat` (または default branch) が両方空なら、レビュー対象が無い旨を伝えて終了する。無駄なレビュー呼び出しはトークンの浪費。

## 全体フロー

```
[ユーザー: /post-work-review or 「review して仕上げて」]
        │
        ▼
[Pass 1] code-review プラグイン  ── 指摘を集める → 修正
        │
        ▼
[Pass 2] codex:review ループ (codex companion がある時のみ)
   ┌──── review 実行
   │       │
   │       ▼
   │   指摘あり? ── No ──→ 完了
   │       │ Yes
   │       ▼
   │   前回と同じ指摘集合? ── Yes ──→ ユーザーに判断を仰ぐ
   │       │ No
   │       ▼
   │   修正
   └──────┘
        │
        ▼
[Step 4] レビュー済みコミットを marker に記録 (PR ゲートの signal)
```

## Pass 1 — code-review プラグインで掃除

1. ユーザーに「Pass 1 として code-review を回します」と 1 文で宣言する。長い前置きは不要。
2. **Skill ツール経由で `code-review` を呼ぶ**: `Skill(skill="code-review")`。引数は付けない (デフォルトの effort で十分。`--comment` は付けない — PR が無いローカル作業中にも使う skill なので、コメント posting は本質ではない。レビュー本文の収集だけが目的)。
3. 返ってきた指摘を読み、修正すべき項目を選別する。**全ての指摘を機械的に直すのではなく**、明らかな bug / 規約違反 / セキュリティ問題を優先する。スタイル提案レベルは Pass 2 のあとに一括判断してよい (codex で同じことが再度挙がるなら直す価値がある)。
4. 修正する項目をユーザーに 1〜2 文で宣言してから Edit に入る (例:「null チェック漏れ 2 箇所と未使用 import を直します」)。修正後は短く完了報告。

### code-review-strict との区別 (重要)

このPCのメモリには「`/code-review-strict` 実行中は実装禁止、PR 外の不具合は issue 立てのみ」というルールがある。**そのルールは `code-review-strict` 専用** であり、本 skill が呼ぶプラグイン版 `code-review` には適用されない。プラグイン版は「diff の bug を挙げる→必要なら直す」までを含めて運用する想定なので、Pass 1 の修正フェーズは普通に Edit してよい。

混同しないために: `code-review-strict` をこの skill から呼ばないこと。呼ぶのはプラグインの `code-review` のみ。

## Pass 2 — codex:review ループ (codex が利用可能なときのみ)

`codex:review` は skill ツール一覧に出てこない (slash command のみ) ので、**Bash で companion スクリプトを直接叩く**。バージョン番号はパスに含まれるため glob で吸収する。

### 前提チェック: codex companion の有無

Pass 1 のあと、まず companion スクリプトの実体があるか確認する:

```bash
ls ~/.claude/plugins/cache/openai-codex/codex/*/scripts/codex-companion.mjs 2>/dev/null | head -1
```

- **見つかった場合**: 従来どおり下記「1 反復の手順」を回す (approve まで、oscillation セーフティ維持)。
- **見つからない場合**: Pass 2 を **skip** し、次の 1 行を Claude の応答に明記してから Step 4 へ進む:

  > ⚠️ codex companion 未検出のため second-pass review は skip。Pass 1 単独でのレビュー結果として扱う。

  この設計により codex 未導入環境でも skill は機能し、Pass 1 完了で Step 4 の marker が書かれる。

### 1 反復の手順

1. ユーザーに「Pass 2 反復 N: codex review を実行します」と 1 文宣言。
2. Bash を実行:

   ```bash
   node ~/.claude/plugins/cache/openai-codex/codex/*/scripts/codex-companion.mjs review --wait
   ```

   `--wait` を付けるのは、ループ制御のために stdout を同期的に受け取る必要があるため。`--background` だと `/codex:status` を別途ポーリングする羽目になり、ループの単純性が失われる。
3. stdout を読む。codex の native review は markdown を返してくる。出力をユーザーに提示するときは `codex:codex-result-handling` skill のガイド (verdict / summary / findings / next_steps、severity 順、file:line を改変しない、推測と確定を分けて表示) に従って整形する。長文の場合は要点のみ提示し、フルテキストは折り畳むか「全文は codex の出力をそのまま添付」する形でよい。

### 終了判定

native review の markdown には機械可読な「0 findings」マーカーが (現状) 無い。文面から **以下のいずれかに合致したら clean と見なす**:

- "approve" / "approved" / "looks good" / "no issues" / "no findings" / "0 findings" 相当の英語表現
- 「指摘なし」「問題なし」「特になし」「修正不要」等の日本語表現
- findings セクションが空、または "(none)" / "なし" のみ

判断に迷うグレーゾーンが出たら **ユーザーに「これは clean と判断していい?」と一度だけ確認する**。勝手に clean 判定してループを早期終了すると、本来直すべき指摘を取りこぼす。

### Oscillation セーフティ (上限なしの暴走防止)

ユーザーは「clean になるまで上限なし」を選択しているが、codex が同じ指摘を直しきれずに繰り返すケースで暴走しないよう、以下を必ず守る:

- **前回反復の指摘集合を覚えておく**: 各 finding を `<file>:<行範囲>:<指摘要旨1行>` の形で正規化したリストとして保持する (会話メモリ内、メモリファイルには書かない)。
- **2 回連続で同一集合なら停止**: ジャッジに迷うときは「ファイルパスと指摘要旨が同じなら同一」と判断する。同一と判定したら修正には入らず、次の文面でユーザーに判断を仰ぐ:

  > codex が 2 反復連続で同じ指摘を返しています: [概要]。
  > 自分の修正で解消できていない可能性があります。次のうちどれにしますか?
  > (a) この指摘を無視してループを終了
  > (b) 別アプローチで自分が手動修正したいので一旦停止
  > (c) もう一度だけ違う方針で直してみる

  AskUserQuestion で 3 択を出す。

- **完全に同一でなくても、3 反復連続で同じファイル群に同種の指摘が出続けるなら警戒する**: 同様にユーザーに状況共有して判断を仰ぐ。これは「微妙に文言だけ変わるが本質は同じ」を捕まえるための保険。

### 修正フェーズ

指摘を直す手順は通常の Edit ベース実装と同じ。Pass 1 と同じく、修正に入る前に「これとこれを直します」と 1〜2 文で宣言する。修正後は次の反復に進む。

## Step 4 (final): record reviewed commit

Pass 1 (と、利用可能だった場合は Pass 2) が通り、修正コミットが全て commit 済みの状態で、現在のリポジトリのレビュー済みマーカーを書き出す。これは PR 作成ゲート (`.claude/hooks/pre-pr-review-gate.sh`) が「このコミットはレビュー済み」と認識する signal。

```bash
git rev-parse --is-inside-work-tree >/dev/null 2>&1 \
  && git rev-parse HEAD > "$(git rev-parse --git-dir)/post-work-review-passed" \
  || true
```

- git リポジトリ外で `/post-work-review` を呼ばれた場合は no-op。
- マーカーは worktree-local (`.git/worktrees/<name>/post-work-review-passed`) なので fanout の並列ペインで干渉しない。
- 修正コミットがまだ working tree に残っている (uncommitted) 状態で marker を書くと、その後コミットして HEAD が進んだ瞬間に marker は stale になりゲートが再び閉じる。**marker を書くのは「これ以上コミットしない」という最終状態に到達してから**にする。push やコミット自体は本 skill の責任外なので、ユーザーが別途コミット → このステップ、の順序を守る。

## 完了報告

ループ終了時 (clean 判定 / ユーザー停止指示 / oscillation 検知のいずれか) に、以下を 2〜3 文で報告する:

- Pass 1 で何件直したか
- Pass 2 が何反復回って終わったか (codex 未検出で skip した場合はその旨)
- 最終的に codex が approve したか、それともユーザー判断で停止したか
- Step 4 で marker を書いたか (現 HEAD)
- 残課題があれば箇条書きで列挙 (oscillation で残った指摘など)

長い総括は不要。ユーザーは diff を読めば中身は分かる。

## やらないこと

- **`code-review-strict` の呼び出し**: 別物。本 skill では使わない。
- **`--comment` フラグ付き code-review**: PR コメントを直接付けに行く動作は本 skill の責任範囲外 (専用に `/code-review --comment` を別途叩けばよい)。
- **コミットや push の自動実行**: 本 skill は仕上げレビューまで。コミットメッセージ作成や push はユーザー指示で別途行う (Step 4 の marker 書込はレビュー済み signal の記録であって、コミットや push ではない)。
- **`/codex:status` の自動ポーリング**: `--wait` で同期実行するため不要。
- **メモリ ([[feedback_reviewer_role]] 等) への新規エントリ追加**: 本 skill 自体がルールの保管庫。

## トラブルシューティング

- **`codex-companion.mjs` の glob が複数マッチ**: 通常 1 バージョンしか入っていない。複数あるならユーザーに通知し、`ls ~/.claude/plugins/cache/openai-codex/codex/` の結果を見せて指示を仰ぐ。
- **codex CLI 未認証 / レートリミット**: companion スクリプトがエラーを出す。`codex:setup` skill を案内する。
- **code-review が失敗した場合**: Pass 1 を諦めて Pass 2 から始めるか、ユーザーに継続判断を仰ぐ。Pass 1 失敗だけで全体を中止しないこと (Pass 2 単独でも価値はある)。
