# レビュー範囲の契約

自動 PR レビュー(codex connector / `codex:review` / `/code-review`)が
どこまでを指摘対象にするかの正典。対応環境を宣言していないと、レビュアーは
到達不能な環境の edge case を無限に列挙できてしまう。この文書はその境界を引く。

`AGENTS.md` の `Automated PR Review Scope` と `CLAUDE.md` はここを指す。

**この文書はレビューゲートの信頼源ではない。** `codex/skills/post-work-review`
のゲートは bootstrap 命令 (`AGENTS.md` / `AGENTS.override.md` / `.codex`) だけを
byte 単位で検証しており、`docs/` はその保護範囲にない。ゲートがマトリクスを
読むのは `AGENTS.md` の `Automated PR Review Scope` 節に限る。ここを信頼源に
すると、PR が自分でこの文書を書き換えて自分への blocker 指摘を棄却できてしまう。
この文書は人間向けの正典、`AGENTS.md` はゲート向けの正典として、
両方を同じ内容に保つ。

## 対応環境マトリクス

fanout が検証されているのはこの範囲だけ。マトリクス外で初めて壊れる挙動は
未検証であって既知の欠陥ではなく、レビューの指摘対象にしない。
「ここより古い環境で壊れる」ではなく「ここより外は動作を確かめていない」を意味する。

| 項目 | 検証範囲 | 根拠 |
|---|---|---|
| git | 2.39 以上 | 開発機の Apple Git-154(2.39.5)と CI の ubuntu-latest(2.43+)。これより古い git は未検証 |
| git object format | SHA-1 のみ | SHA-256 リポジトリは未検証。OID 長 40 桁を前提にしてよい |
| git index | non-sparse / non-split | sparse-checkout と split index は未検証 |
| OS | macOS / Linux | Windows は非対応(tmux 前提) |
| checkout | 単一ユーザーのローカル checkout | ネットワーク FS、共有 checkout、他ユーザーとの同時操作は対象外 |
| tmux | 3.3 以上 | `README.md` / `README.ja.md` の前提ツール |
| gh | 認証済み(`gh auth status`) | 同上 |
| ビルド | Go 1.26.5+ / Node.js 24+ / pnpm 11+ | チェックアウトからビルドする場合のみ |

前提ツールのユーザー向け記述は README にある。この表は README を実装レビュー用に
細かくしたもので、README を置き換えない。マトリクス外の環境で実際に動かない報告が
来たら、そのときに個別 issue として扱う。

## やらないこと(判断記録)

- **マトリクス外の環境対応**: SHA-256 リポジトリ、Git 2.38 以前、sparse / split
  index、`core.precomposeUnicode` や `core.ignoreCase` の非既定値、Windows。
  対応するとしても、それは実利用者からの報告を起点とした独立の issue で扱う
- **他プロセスとの worktree / branch 同時操作の完全な排他**: `.fanout/state.json`
  のロックが守るのは fanout 自身の launch 経路だけ。ユーザーや他ツールが同じ
  リポジトリで並行に `git worktree add` / `git branch -D` した場合の TOCTOU は
  非ゴール。fanout の経路内で閉じるレースは対象に含める
- **crash-resume の全窓での資源回収保証**: プロセスが任意の一点で落ちても
  worktree / branch / pane を必ず回収する、という保証はしない。回収できない
  場合に手動 cleanup へ落ちることを明示するのが要件で、窓を全て塞ぐことではない
- **設計ドキュメントの実装粒度の網羅性**: 下記の位置づけを参照

## 指摘の裁定

指摘を受け取ったら、軸は一つだけ。**対応環境マトリクス内で到達可能な trigger を
示せるか**。

1. **示せる** → 修正する。重大度(P1 / P2)では落とさない。
   マトリクス内で踏めるなら P2 でも直す
2. **示せない** → thread に理由を書いて decline する。
   無言でクローズしたり、黙って直したりしない。理由は
   「マトリクス外(SHA-256 リポジトリ)のため非対応」のように、
   この文書のどの行に当たるかが分かる形で書く
3. **判断が割れる / 直すと PR のスコープを超える** → 止めてユーザーに確認する。
   到達可能な欠陥を follow-up issue へ切り出すかはユーザーの判断であり、
   エージェントが単独で決めない。決まるまで PR は未完了のままにする

黙って全件直すのは禁止。修正はコードを増やし、次のレビューラウンドの面になる。
実測(下記)では、指摘への返信で「修正しました」が 92 行に対し
decline の表明は 6 行しかなく、ほぼ全件を直していた。これがラウンドを
収束させない直接の原因である。

decline 済みの thread は、同じ内容で再提起しない。再提起されたら理由を
指し直すだけでよく、方針を変える必要はない。

## 設計ドキュメントの位置づけ

`docs/` 配下の ADR / spike 文書は**決定記録**であって実装仕様ではない。
「何をなぜそう決めたか」を残すのが目的で、実装が満たすべき条件の網羅表ではない。

- 実装粒度の網羅性でレビューしない
- 書いていない条件は「未決定」であって「仕様の欠落」ではない
- 逆に、実装粒度まで書き下すと、レビュアーはそれを仕様として整合性を検査する。
  spike 文書を細かく書くほど指摘は増える。決定に必要な粒度で止める

契約として拘束力を持つのは、`docs/architecture.ja.md` の層ルールとレビュークラス、
`CLAUDE.md` / `AGENTS.md` の Behavior Boundaries、この文書のマトリクスだけ。

## 実測ベースライン(2026-08)

直近 60 PR の codex connector inline comment 239 件。

| 指標 | 値 |
|---|---|
| 重大度 | P1 54 / P2 183 / P3 2 |
| 収束しない PR | #596(+7343 行 → 49 指摘 / 19 ラウンド)、#590(docs 676 行 → 35 指摘)、#573(+8256 行 → 26 指摘)、#597(+1891 行 → 21 指摘 / 15 ラウンド) |
| 収束する PR | #601〜#603(約 300 行)は 1 ラウンド 1〜3 指摘 |
| 対応 | 返信行のうち「修正しました」92 / decline の表明 6 |
| 再レビュー契機 | push ごと(#596 は 19 commit に 19 レビュー) |

多かった指摘クラスは、マトリクス外の環境(24 件)、path / traversal(22 件)、
サイズ上限とバイト境界(21 件)、エラー処理(30 件)、context / deadline 伝播
(15 件)。前者二つがこの文書の対象で、後者は
`docs/review-checklist.ja.md` の自己チェック項目にした。

### 再測定

```bash
gh pr list --state all --limit 60 --json number --jq '.[].number' > /tmp/prs.txt
while read -r pr; do
  gh api "repos/butaosuinu/fanout/pulls/$pr/comments?per_page=100" --paginate \
    --jq ".[] | select(.user.login==\"chatgpt-codex-connector[bot]\") | {pr: $pr, path, body}"
done < /tmp/prs.txt > /tmp/codex_comments.jsonl

jq -r '.pr' /tmp/codex_comments.jsonl | sort -n | uniq -c | sort -rn | head
jq -r '.body' /tmp/codex_comments.jsonl | grep -oE 'P[0-9] Badge' | sort | uniq -c
```

ラウンド数は `gh api repos/butaosuinu/fanout/pulls/<N>/reviews --paginate` を
同じ bot login で絞って数える。

## レビュー頻度

codex connector は現状 push ごとに再レビューする。トリガー設定は ChatGPT 側
(`chatgpt.com/codex/cloud/settings/general`)にあり、リポジトリからは変えられない。
ラウンド数そのものを減らしたい場合は、そこで「ready for review 時のみ」へ
切り替えるのが唯一の手段。

こちら側では `@codex review` を手動で再トリガーしない。1 回叩くたびに
変更面が全て再列挙される(#596 では 6 回叩いている)。

## この文書の更新

マトリクスを広げる / 狭める判断をしたら、この表を更新して `AGENTS.md` の
`Automated PR Review Scope` も同じ内容に揃える。
実測が乖離したら `/session-retro` の再発分類が検出するので、
ベースラインの表を測り直す。
