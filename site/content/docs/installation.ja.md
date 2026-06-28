---
title: インストール
linkTitle: インストール
description: "curl 一行で Go バイナリと Claude Code、Codex の連携が入る。何が配置され、どう検証と上書きが起き、更新をどう保つかをまとめる。"
weight: 10
kanji: 始
yomi: install
---

## 前提ツール

親 issue を子ごとのペインにファンアウトする既定のフローでは、役割の異なる次の 3 つのツールが `PATH` 上に必要です。

| ツール | 何のために要るか |
|---|---|
| `gh` | GitHub CLI。issue の取得、PR 状態の照会、Project の GraphQL クエリに使う |
| `git` | worktree の作成、branch の分岐、merge に使う |
| `tmux 3.3+` | 子ごとのペイン分割と TUI popup の表示に使う |

fanout は選択したモードに必要な依存だけを起動時にチェックし、足りなければインストールのヒントを表示します。`--status` と `--cleanup` は `gh` と `git` を、`--merge` と `--close` は `git` を使います。
ローカルの `fanout plan` 実行と、TUI から起動する手動ペインは、`git` と `tmux 3.3+`、選択した agent があれば動きます。`origin` remote や `gh` 認証が無い repository でも動作します。

> **Project モード時のみ**: Project items を取得する GraphQL クエリのため、`gh` CLI に `read:project` スコープが必要です。`gh auth refresh -s read:project` で付与してください。issue モード(`fanout <N>`)では不要です。

## 推奨: インストールスクリプト

推奨インストール経路は Release 済みの Go バイナリです。安定コマンド名 `fanout` と、同梱の Claude/Codex 連携ファイルをまとめて配置します:

```bash
# fanout + Claude/Codex 連携を ~/.local, ~/.claude, ~/.codex に配置
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | sh

# バイナリのみ
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | sh -s -- --no-skills

# 配置先や Release tag を指定
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | BIN_DIR=/usr/local/bin sh
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | FANOUT_VERSION=v0.8.0 sh
```

`install.sh` はまず macOS/Linux と amd64/arm64 を自動判定します。次に最新 GitHub Release(または `FANOUT_VERSION` で指定した tag)から `fanout_<os>_<arch>.tar.gz` を取得します。`sha256sum` または `shasum` があれば `SHA256SUMS` で検証します。再実行時は同じパスへ上書きします。シェル rc は自動編集しません。

### 配置パス

- `$BIN_DIR/fanout`(既定は `~/.local/bin/fanout`)
- `$CLAUDE_DIR/commands/fanout.md`(既定は `~/.claude/commands/fanout.md`)
- `$CLAUDE_DIR/commands/pr-watch.md`(既定は `~/.claude/commands/pr-watch.md`)
- `$CLAUDE_DIR/skills/fanout/`(既定は `~/.claude/skills/fanout/`)
- `$CLAUDE_DIR/skills/fanout-issues/`(既定は `~/.claude/skills/fanout-issues/`)
- `$CLAUDE_DIR/skills/fanout-plan/`(既定は `~/.claude/skills/fanout-plan/`)
- `$CLAUDE_DIR/skills/post-work-review/`(既定は `~/.claude/skills/post-work-review/`。PR レビューゲートを支える skill です。同名 skill を上書きするので、自前のものがあればバックアップしてください)
- `$CLAUDE_DIR/skills/pr-watch/`(既定は `~/.claude/skills/pr-watch/`)
- `$CODEX_DIR/skills/fanout/`(既定は `~/.codex/skills/fanout/`)
- `$CODEX_DIR/skills/fanout-issues/`(既定は `~/.codex/skills/fanout-issues/`)
- `$CODEX_DIR/skills/fanout-plan/`(既定は `~/.codex/skills/fanout-plan/`)
- `$CODEX_DIR/skills/post-work-review/`(既定は `~/.codex/skills/post-work-review/`)
- `$CODEX_DIR/skills/pr-watch/`(既定は `~/.codex/skills/pr-watch/`)

`~/.local/bin` が `PATH` に入っていることを確認してください:

```bash
echo $PATH | tr ':' '\n' | grep -F "$HOME/.local/bin"
```

入っていない場合は、シェルの rc に `export PATH="$HOME/.local/bin:$PATH"` を追記してください。スキルをインストールまたは更新した後、実行中の Codex CLI セッションがある場合は再起動すると新しいファイルを認識します。

### アンインストール

```bash
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | sh -s -- --uninstall
```

## macOS セキュリティメモ

curl/wget 経由のインストールでは通常 `com.apple.quarantine` 拡張属性が付かないため、Gatekeeper の「開発元を検証できません」GUI ブロックは基本的に発生しません。ブラウザ経由でアーカイブを取得して quarantine が付いた場合は、展開後に次で属性を削除してください:

```bash
xattr -d com.apple.quarantine /path/to/fanout
```

Apple Silicon では、すべての実行ファイルに最低限 ad-hoc 署名が必要です。Release workflow は macOS 上で Go 1.26 の darwin バイナリをビルドするため、Go linker がビルド時に署名します。Release package 作成後に外部 `strip` をかけると署名が壊れることがあるので避けてください。ローカルコピーが壊れた場合は次で ad-hoc 再署名できます:

```bash
codesign -s - /path/to/fanout
```

Apple Developer ID 署名や notarization は curl 配布経路では当面スコープ外です。

## チェックアウトから使う場合

ローカルの Makefile ターゲットは Go バイナリを安定コマンド名 `fanout` としてインストール / symlink し、同梱の連携ファイルも配置します:

```bash
make install        # Go 版を $(BINDIR)/fanout としてビルド + 連携をコピー
make link           # Go 版を $(BINDIR)/fanout として symlink + 連携を symlink
make uninstall      # インストール済みのパスを削除

PREFIX=/usr/local sudo make install      # システム全体に Go CLI を配置
CLAUDE_DIR=/path/to/.claude make install # 既定以外の Claude データディレクトリを指定
CODEX_DIR=/path/to/.codex make install   # 既定以外の Codex データディレクトリを指定
```

チェックアウトからのビルドには **Go ツールチェイン**(Go 1.26+)に加えて **Node.js 24+ と pnpm 10+** が必要です。`make install`、`make link`、`make build-go` はまずダッシュボード Web UI をビルドし(`make build-web`、`web/` の Vite バンドル)、それを embed して `go build ./cmd/fanout` を実行します。上記の curl インストールは prebuilt バイナリを配置するので、Go も Node も要りません。

## 更新を保つ

### `fanout --check-update`

`fanout --check-update` は読み取り専用です。`butaosuinu/fanout` の最新 release tag を取得し、バイナリ埋め込みの version と比較して、更新の有無を表示します。サブコマンド形の `fanout check-update` も使えます。ローカルの dev build(`version == "dev"`、素の `make build-go` を含む)は `gh` を呼ばず、dev build である旨を表示して exit 0 します。

| Exit code | 意味 |
|---|---|
| `0` | 比較が完了した、または dev build |
| `2` | 現在の version か最新 tag が `MAJOR.MINOR.PATCH`(`v` prefix 可)でない |
| `3` | `gh release view -R butaosuinu/fanout` が失敗した |

### `fanout update`

`fanout update` は、上で説明した `install.sh` の経路をそのまま呼び出して実行中の release バイナリを置き換えます。OS/arch 判定、release ダウンロード、checksum 検証、アーカイブ展開、Claude/Codex skill のインストールは、すべて 1 つのスクリプトに集約したままです。

既定では最新 release を解決して埋め込み version と比較し、現在のバイナリパス(`EvalSymlinks` 解決後)を表示してから、すぐにインストーラを実行します。ローカルの dev build は置き換えを拒否します。

- `--version <tag>`: `FANOUT_VERSION=<tag>` を `install.sh` に渡し、指定 tag をインストールする。
- `--no-skills`: `--no-skills` を `install.sh` に渡し、バイナリのみ更新する。

| Exit code | 意味 |
|---|---|
| `0` | 更新が完了した、または既に最新 |
| `1` | 環境 / preflight の失敗: dev build、`curl`/`wget` 無し、書き込めない binary ディレクトリ、オプション値の欠落 |
| `2` | 未知のオプション、想定外の引数、比較不能な version 文字列 |
| `3` | 最新 release の取得に失敗した |

次は[クイックスタート]({{< relref "/docs/quickstart" >}})で、最初の親 issue を開いてみてください。
