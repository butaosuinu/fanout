---
title: インストール
linkTitle: インストール
description: "curl 一行で fanout 本体と Claude Code、Codex の連携を導入する手順。配置先の確認、アンインストール、macOS でブロックされた場合の対処、更新の保ち方をまとめる。"
weight: 10
kanji: 始
yomi: install
---

## 前提ツール

親 issue を子ごとのペインにファンアウトする既定のフローでは、次の 3 つのツールが `PATH` 上に必要です。

| ツール | 何のために要るか |
|---|---|
| `gh` | GitHub CLI。issue の取得、PR 状態の照会、Project の GraphQL クエリに使う |
| `git` | worktree の作成、branch の分岐、merge に使う |
| `tmux 3.3+` | 子ごとのペイン分割と TUI popup の表示に使う |

> **Project モード時のみ**: Project items を取得する GraphQL クエリのため、`gh` CLI に `read:project` スコープが必要です。`gh auth refresh -s read:project` で付与してください。issue モード(`fanout <N>`)では不要です。

## インストール

推奨経路は Release 済みの Go バイナリです。

```bash
# fanout + Claude/Codex 連携を ~/.local, ~/.claude, ~/.codex に配置
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | sh

# バイナリのみ
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | sh -s -- --no-skills

# 配置先や Release tag を指定
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | BIN_DIR=/usr/local/bin sh
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | FANOUT_VERSION=v0.10.0 sh
```

`install.sh` は OS/arch を自動判定し、最新 Release(または `FANOUT_VERSION` で指定した tag)から `fanout` バイナリと Claude/Codex 連携ファイルを取得して配置します。

### 配置先

各配置先はインストールコマンドの環境変数で上書きできます。`BIN_DIR`(既定 `~/.local/bin`)、`CLAUDE_DIR`(既定 `~/.claude`)、`CODEX_DIR`(既定 `~/.codex`)です。

- `$BIN_DIR/fanout`(バイナリ本体)
- `$CLAUDE_DIR/commands/`(`fanout`、`pr-watch`、`session-retro` のスラッシュコマンド)
- `$CLAUDE_DIR/skills/`(`fanout`、`fanout-issues`、`fanout-plan`、`post-work-review`、`pr-watch`、`session-retro` の skill)
- `$CODEX_DIR/skills/`(`session-retro` を除く同じ skill 群)と `$CODEX_DIR/agents/`(post-work reviewer / verifier)、`$CODEX_DIR/tools/`

install と update はこれらすべてを上書きします。

`~/.local/bin` が `PATH` に入っていることを確認してください。

```bash
echo $PATH | tr ':' '\n' | grep -F "$HOME/.local/bin"
```

入っていない場合は、シェルの rc に `export PATH="$HOME/.local/bin:$PATH"` を追記してください。

### アンインストール

```bash
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | sh -s -- --uninstall
```

## macOS でブロックされたら

curl/wget 経由のインストールでは通常 quarantine 属性が付かないため、Gatekeeper のブロックは基本的に起きません。
ブラウザ経由で取得して quarantine が付いた場合は、次で属性を削除してください。

```bash
xattr -d com.apple.quarantine /path/to/fanout
```

ローカルコピーの署名が壊れた場合は、次で ad-hoc 再署名できます。

```bash
codesign -s - /path/to/fanout
```

## チェックアウトから使う場合

```bash
make install        # Go 版を $(BINDIR)/fanout としてビルド + 連携をコピー
make link           # Go 版を $(BINDIR)/fanout として symlink + 連携を symlink
make uninstall      # インストール済みのパスを削除
```

ビルドには Go ツールチェイン(Go 1.26.5+)に加えて Node.js 24+ と pnpm 10+ が必要です(`make install` はダッシュボード Web UI を先にビルドして embed するため)。
curl インストールは prebuilt バイナリを配置するので、Go も Node も要りません。

## 更新を保つ

`fanout --check-update` は読み取り専用です。
最新 release tag と埋め込み version を比較して更新の有無を表示するだけで、何も変更しません。

`fanout update` は上の curl インストールと同じ経路を呼び出し、本体と Claude/Codex 連携をまとめて更新します。

- `--version <tag>`: 指定した tag をインストールする
- `--no-skills`: バイナリのみ更新する

> install と update は `~/.claude` と `~/.codex` 配下の同梱ファイル(`post-work-review` や `pr-watch` skill を含む)を上書きします。
> カスタマイズした copy は先に退避してください。
> Codex CLI は起動時に skill を読み込むため、更新後は実行中の Codex セッションを再起動してください。

exit code の一覧は[CLI リファレンス]({{< relref "/docs/cli" >}})を参照してください。

次は[クイックスタート]({{< relref "/docs/quickstart" >}})で、最初の親 issue を開いてみてください。
