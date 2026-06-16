# fanout

[English](README.md) | [日本語](README.ja.md)

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Latest release](https://img.shields.io/github/v/release/butaosuinu/fanout)](https://github.com/butaosuinu/fanout/releases)
[![Docs](https://img.shields.io/badge/docs-butaosuinu.github.io%2Ffanout-2b7a78)](https://butaosuinu.github.io/fanout/ja/)

**tmux 向けの並列 issue オーケストレーター。** GitHub の親 issue の OPEN な
サブ issue を、並列の tmux ペインへファンアウトします — 子ごとに 1 つの git
worktree、1 つの agent CLI(Claude Code / Codex)、issue 単位の briefing から
起動。再実行しても同じ子に 2 つ目のペインは作られません(`.fanout/state.json`
が記憶します)。

📖 **ドキュメント:** <https://butaosuinu.github.io/fanout/ja/> — インストール、
クイックスタート、完全な CLI リファレンス、設定、トラブルシューティング(英語 /
日本語)。

![fanout web dashboard](docs/assets/dashboard.jpg)

## 機能

- **冪等なファンアウト** — `.fanout/state.json` が `(parent, issue)` ごとに
  ペインを管理するので、再実行しても作業が重複しません。
- **Wave 進行** — `--unblocked-only` がブロッカーを読み取り、unblock 済みの子
  だけをファンアウト。PR が merge されたら再実行すれば次の wave が開きます。
- **常駐 TUI コンソール** — 引数なしの `fanout` で、ペイン / issue / PR を
  ライブ表示し、focus・peek・lifecycle キーを備えたコンソールを開きます。
- **Web ダッシュボード** — localhost で動く read-only のダッシュボード(ライブ
  更新)。どのペインからでも `prefix + D` でポップできます。
- **状態確認とレポート** — `--status` の JSON / table で PR review・CI 状態を
  確認でき、任意で親 issue にダッシュボードコメントを投稿します。
- **エージェント連携** — `/fanout` slash command と Claude Code & Codex 向けの
  skill をインストール時に同梱します。

## インストール

```bash
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | sh
```

`fanout` バイナリ(既定で `~/.local/bin`)と、同梱の Claude/Codex 連携ファイルを
配置します。バイナリのみのインストール、配置先の変更、バージョン固定、
アンインストール、チェックアウトからのビルドはすべて
[インストールドキュメント](https://butaosuinu.github.io/fanout/ja/docs/installation/)
に記載しています。

`~/.local/bin` が `PATH` に入っていることを確認してください。

## クイックスタート

tmux セッション内で、作業対象のリポジトリにて:

```bash
# 子の agent を一度だけ指定(各 run で --agent claude / --agent codex を渡してもよい)
export FANOUT_AGENT=claude

fanout 123 --dry-run    # 何も変更せず worktree / ペインをプレビュー
fanout 123              # issue #123 の OPEN な子をすべて個別ペインへファンアウト
fanout 123 --status     # ファンアウトした子の PR review + CI 状態を確認
```

引数なしの `fanout` で、現在のリポジトリの常駐 TUI コンソールを開きます。最初の
実行は
[5 分クイックスタート](https://butaosuinu.github.io/fanout/ja/docs/quickstart/)
を参照してください。

## 仕組み

3 幕構成 — **育てる → 開く → 収穫する**:

1. **木を育てる。** 親 issue と OPEN な子 issue を作成します。同梱の
   `fanout-issues` skill がブロッカーの wave を組み立ててくれます。
2. **扇を開く。** `fanout 123` を一振り: 子 1 つ = worktree 1 つ = tmux ペイン
   1 つ = agent 1 つ。
3. **果実を収穫する。** TUI または `--status` で進捗を見守り、`--merge` /
   `--cleanup` でペインを畳んで、次の wave を開きます。

`fanout plan <spec>` は GitHub の子 issue ではなくローカルの plan spec を
ファンアウトし、`--team` + `fanout msg` は兄弟ペインに軽量な peer messaging を
与えます — 詳細は
[ワークフローのドキュメント](https://butaosuinu.github.io/fanout/ja/docs/workflow/)
を参照してください。

## 日常コマンド

| コマンド | 内容 |
|---|---|
| `fanout 123 --agent claude` | 親の OPEN な子を並列ペインへファンアウト |
| `fanout 123 --unblocked-only` | ブロッカーが closed の子だけをファンアウト — 次の wave |
| `fanout 123 --dry-run` | git も tmux も触らず計画だけ表示 |
| `fanout plan spec.json --agent claude` | GitHub の子 issue でなくローカル plan spec をファンアウト |
| `fanout` | 常駐 TUI コンソールを起動(focus・peek・lifecycle キー) |
| `fanout 123 --status` | ペイン・PR review・CI 状態を JSON または table で |
| `fanout dashboard --web` | localhost で read-only Web ダッシュボードを配信 |
| `fanout 123 --merge 4` | 子 branch を fast-forward merge(`--close` / `--cleanup` でペインを畳む) |

ファンアウト系のコマンドは子の agent が必要です — `--agent claude` / `--agent codex`
を渡すか `FANOUT_AGENT` を設定してください(status・dashboard・lifecycle 系の
`--status` / `dashboard` / `--merge` には不要)。
すべての flag・環境変数・exit code は
[CLI リファレンス](https://butaosuinu.github.io/fanout/ja/docs/cli/)に記載しています。

## エージェント連携

`make install`(およびインストールスクリプト)は、`/fanout` slash command と
一連の skill を `~/.claude/` と `~/.codex/` に配置します。これにより Claude Code
と Codex は fanout が使える場面を認識し、`--dry-run` でプレビューしてから、確認の
あとに実行できます。fanout 自身は LLM を呼びません — skill が issue の文脈から
flag を生成します。詳しくは
[エージェント連携のドキュメント](https://butaosuinu.github.io/fanout/ja/docs/agents/)
を参照してください。

## 前提条件

- **git**、**tmux**、認証済みの **GitHub CLI(`gh`)**(`gh auth status`)。
- 一括ファンアウト(`fanout <parent>`)は tmux 内から実行する必要があります。
  引数なしの TUI コンソールは素の shell から起動できます。
- Project モードは `read:project` の gh スコープが必要です
  (`gh auth refresh -s read:project`)。
- チェックアウトからビルドする場合は、追加で Go 1.26+、Node.js 24+、pnpm 10+
  が必要です(curl インストールはビルド済みバイナリを配るのでどちらも不要)。

## 開発

```bash
make build-go   # web バンドル + ./fanout-go CLI をビルド
make test       # Go ユニットテスト + web vitest + bats ブラックボックス tier
make lint       # golangci-lint v2 + shellcheck(web UI は make lint-web)
```

リポジトリのアーキテクチャと保守メモは [CLAUDE.md](CLAUDE.md) を参照してください。

## ライセンス

本プロジェクトは [MIT License](LICENSE) の下でライセンスされています。
