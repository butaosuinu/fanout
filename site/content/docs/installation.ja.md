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

この 3 つは既定の tmux backend の前提です。opt-in の [herdr backend]({{< relref "/docs/herdr-backend" >}})(v1 は観測専用)では tmux の代わりに herdr 0.7.3 を使います。herdr は AGPL ライセンスで fanout には同梱されないので、別途インストールしてください。

## インストール

推奨経路は Release 済みの Go バイナリです。

```bash
# fanout + Claude/Codex 連携を ~/.local, ~/.claude, ~/.codex に配置
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | sh

# バイナリのみ
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | sh -s -- --no-skills

# 配置先や Release tag を指定
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | BIN_DIR=/usr/local/bin sh
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | FANOUT_VERSION=v0.12.0 sh
```

`install.sh` は OS/arch を自動判定し、最新 Release(または `FANOUT_VERSION` で指定した tag)から `fanout` バイナリと Claude/Codex 連携ファイルを取得して配置します。

### 配置先

各配置先はインストールコマンドの環境変数で上書きできます。
`BIN_DIR`(既定 `~/.local/bin`)、`CLAUDE_DIR`(既定 `~/.claude`)、`CODEX_DIR`(既定 `CODEX_HOME`、次に `~/.codex`)です。
integration の install または uninstall では、`CODEX_DIR` を実効 `CODEX_HOME` と同じ path にしてください。
custom destination を使う場合は両方に同じ path を指定します。

- `$BIN_DIR/fanout`(バイナリ本体)
- `$CLAUDE_DIR/commands/`(`fanout`、`pr-watch`、`session-retro` のスラッシュコマンド)
- `$CLAUDE_DIR/skills/`(`fanout`、`fanout-issues`、`fanout-plan`、`post-work-review`、`pr-watch`、`session-retro` の skill)
- `$CODEX_DIR/skills/`(`session-retro` を除く同じ skill 群。`post-work-review` の marker helper も skill 内に同梱)

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
make install        # Go 版をビルド + Codex gate 以外の連携をコピー
make link           # binary と Codex gate 以外の連携を symlink
make uninstall      # Codex review gate 以外を削除
```

checkout の Makefile は Codex の `post-work-review` package を配置、置換、削除しません。
この gate の配置、更新、削除には、上記の checksum 検証付き release installer を使います。
review target のコードから gate を配置しないでください。
`CODEX_DIR` または実効 `CODEX_HOME` に旧 driver が残る場合、`make install` と `make link` は binary の置換前に停止します。
release installer で旧 driver を移行してください。
gate を変更する branch は trusted checkout または人がレビューしてください。

ビルドには Go ツールチェイン(Go 1.26.5+)に加えて Node.js 24+ と pnpm 11+ が必要です(`make install` はダッシュボード Web UI を先にビルドして embed するため)。
curl インストールは prebuilt バイナリを配置するので、Go も Node も要りません。

## 更新を保つ

`fanout --check-update` は読み取り専用です。
最新 release tag と埋め込み version を比較して更新の有無を表示するだけで、何も変更しません。

`fanout update` は上の curl インストールと同じ経路を呼び出し、本体と Claude/Codex 連携をまとめて更新します。

- `--version <tag>`: 指定した tag をインストールする
- `--no-skills`: バイナリのみ更新する。`CODEX_DIR` または別指定の
  `CODEX_HOME` に廃止済みの Codex `post-work-review.sh` driver が残っている場合は
  置換前に停止するため、`--no-skills` を外して連携ファイルを移行する

> install と update は `~/.claude` と `~/.codex` 配下の同梱ファイル(`post-work-review` や `pr-watch` skill を含む)を上書きします。
> カスタマイズした copy は先に退避してください。
> Codex CLI は起動時に skill を読み込むため、更新後は実行中の Codex セッションを再起動してください。

exit code の一覧は[CLI リファレンス]({{< relref "/docs/cli" >}})を参照してください。

次は[クイックスタート]({{< relref "/docs/quickstart" >}})で、最初の親 issue を開いてみてください。
