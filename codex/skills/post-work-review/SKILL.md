---
name: post-work-review
description: "Use from Codex CLI to run a finish-review loop on current git work before commit or PR: inspect the diff, run codex review with an explicit scope, fix actionable findings, rerun until clean, and record the review marker when the reviewed commit is clean. Use when the user says review して仕上げて, post-review, finalize, コミット前に確認, 二重チェック, codex review もかけて, or asks for a final pre-PR review pass."
metadata:
  short-description: Run a final Codex review loop before PR
---

# post-work-review

Codex で実装後の差分を仕上げるためのレビュー・修正ループ。

Claude 版の `post-work-review` は Claude の `code-review` plugin と Codex
companion を組み合わせるが、Codex 版ではそれらを使わない。Codex CLI の
native review を明示スコープ付きで実行し、指摘を修正して、指摘なしになるまで
同じ作業ツリーで回す。

## Scope

- 対象は現在の git リポジトリの作業ツリー、HEAD、または現在ブランチの差分。
- git リポジトリ外なら、`post-work-review` は使えないと伝えて終了する。
- レビュー対象が空なら、レビュー対象がないと伝えて終了する。
- PR 作成、push、merge はこの skill の責任外。ユーザーが別途明示したときだけ行う。

## Preflight

1. `git rev-parse --is-inside-work-tree` で git リポジトリ内か確認する。
2. `git status --porcelain` と `git diff --stat` を確認する。
3. clean tree で branch diff を見る必要があるときは default branch を解決する。
   まず `gh repo view --json defaultBranchRef -q .defaultBranchRef.name` を使い、
   失敗したら `origin/HEAD`、最後に `main` を候補にする。

## Review Target

`codex review` は必ず明示スコープ付きで呼ぶ。裸の `codex review` は使わない。

- 未コミット変更をレビューする: `codex review --uncommitted`
- clean tree の現在コミットだけを見る: `codex review --commit HEAD`
- clean tree のブランチ差分を見る: `codex review --base <default-branch>`

未コミット変更があるときは、まず `--uncommitted` を使う。clean tree で、直前に
レビュー済みとして marker を書く目的なら、原則として `--base <default-branch>` を使い、
PR 全体相当の branch diff をレビューする。`--commit HEAD` だけで marker を書いてよい
のは、HEAD が base からの唯一の未レビュー commit だと確認できる場合、またはユーザーが
単一 commit の再レビューだけを明示した場合に限る。複数 commit の feature branch で
`--commit HEAD` だけを通して marker を書くと、古い commit が未レビューのまま PR gate を
通す可能性がある。

## Review Loop

1. レビュー対象とコマンドを 1 文でユーザーに伝える。
2. `codex review ...` を 1 つの blocking shell command として実行する。完了まで
   一切何もしない。途中 stdout、Review Session、`/codex:status`、tmux pane、
   `exec_command` の `session_id` などを見に行かない。command が終了してから
   final output を 1 回だけ読む。
3. final output を findings として読む。重大度、ファイル、行、指摘内容を保持する。
4. actionable な指摘だけ修正する。明らかな bug、規約違反、セキュリティ問題、
   テスト不整合を優先する。単なる好みの提案は理由を添えて見送ってよい。
5. 修正したら、必要な focused test や formatting check を実行する。
6. 同じスコープで `codex review ...` を再実行する。
7. clean 判定まで 2-6 を繰り返す。

### Clean 判定

次の両方を満たすときだけ clean と見なす。

- 肯定的な verdict がある: `approved`, `looks good`, `no issues`,
  `no findings`, `0 findings`, `指摘なし`, `問題なし`, `修正不要` など。
- 否定表現や残 findings がない: `not approved`, `cannot approve`,
  `request changes`, `要修正`, `approve できない` などがあれば clean ではない。

肯定語と否定語が混在する、findings があるのに approve 風の文面がある、などの
曖昧な出力なら勝手に終了せず、ユーザーに clean と扱ってよいか確認する。

### Oscillation Safety

同じ指摘集合が 2 回連続で返ったら、自動修正を続けない。各 finding を
`path:line:summary` に正規化して比較し、同一なら次を短く報告してユーザー判断を
仰ぐ。

- 繰り返している指摘
- 直した内容
- まだ残っている理由の仮説

同一ではなくても、3 回連続で同じファイル群に同種の指摘が出る場合も停止して
相談する。

## Marker

レビュー済み marker は、レビューが実質的に成功し、かつ working tree が clean
なときだけ書く。dirty tree では書かない。未コミットの修正が PR に乗らないのに
HEAD だけをレビュー済み扱いにするのを防ぐため。さらに、marker 前の成功レビューは
PR 全体相当の branch/base scope であることを確認する。`--commit HEAD` の成功だけを
根拠に marker を書くのは、base からの差分がその commit だけだと確認できる場合に限る。

```bash
if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "git repository not found: marker not written"
elif [ -n "$(git status --porcelain)" ]; then
  echo "working tree is dirty: marker not written"
else
  git rev-parse HEAD > "$(git rev-parse --git-dir)/post-work-review-passed"
  echo "marker recorded: $(git rev-parse HEAD)"
fi
```

この marker はこのリポジトリの Claude PR gate も読む。Codex 自体は
`.claude/hooks` を実行しないが、同じ worktree を Claude Code から PR 作成に使う
場合に一貫した signal になる。

## Finish Report

最後に 2-4 文で報告する。

- どの scope をレビューしたか
- 何回 `codex review` を回し、何件修正したか
- 実行したテストやチェック
- clean 判定で終えたか、ユーザー判断で止めたか
- marker を書いたか、書かなかった場合は理由
