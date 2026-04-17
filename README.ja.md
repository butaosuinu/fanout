# fanout

[English](README.md) | [日本語](README.ja.md)

GitHub の親 issue に紐づく OPEN のサブ issue を、子ごとに 1 つの dmux ペインへ
ファンアウトします。各ペインは独立した git worktree を持ち、issue ごとの
ブリーフィングファイルを参照するプロンプトでエージェント CLI が起動します。

## なぜこんな実装なのか（dmux HTTP API 調査）

`dmux` のドキュメントでは HTTP API（`POST /api/panes` など）が、こうしたツールに
とって自然な入口として紹介されています。しかし調査したところ、**現行の
npm 公開版 dmux (v5.6.3) には HTTP サーバが同梱されていません**:

- `dist/**/*.js` に HTTP ルートも、`express`/`fastify`/`http.createServer` も、
  ポート検出ユーティリティ以外の `.listen(` も存在しません。
- `dist/server/` には `embedded-assets.js`（フロントエンドバンドル）しかありません。
- `utils/generated-agents-doc.js` は `curl http://localhost:$DMUX_SERVER_PORT/api/panes/...`
  を参照していますが、`dist` 内で `DMUX_SERVER_PORT` を設定する処理はありません。
  この機能は `main` ブランチの `context/API.md` に記載されていますが、まだ
  出荷されていません。

そのため fanout は、dmux TUI のコントロールペイン（ID は tmux セッション
オプション `@dmux_control_pane` に保存されている）に対して `tmux send-keys` を
投げることで dmux を操作します。dmux が正式に HTTP API を公開したら、この
スクリプトは `POST /api/panes` ベースに書き換え可能です。

## インストール

fanout は 1 つの Bash スクリプトに、Claude Code 連携ファイル（スラッシュコマンド +
スキル）を加えた構成で配布されます。3 つすべては `Makefile` 経由で一括配置されます:

```bash
make install        # CLI + コマンド + スキルを ~/.local と ~/.claude にコピー
make link           # チェックアウト先を指す 3 つの symlink を作成（開発用）
make uninstall      # 上記 3 つを削除

PREFIX=/usr/local sudo make install     # システム全体に CLI を配置; BINDIR を $PREFIX/bin に上書き
CLAUDE_DIR=/path/to/.claude make install # 既定以外の Claude データディレクトリを指定
```

配置パス:

- `$(BINDIR)/fanout`（既定は `~/.local/bin/fanout`）
- `$(CLAUDE_DIR)/commands/fanout.md`（既定は `~/.claude/commands/fanout.md`）
- `$(CLAUDE_DIR)/skills/fanout/SKILL.md`（既定は `~/.claude/skills/fanout/SKILL.md`）

`make install` は安定しています — リポジトリを消しても、コピー済みのファイルで
動作し続けます。`make link` はチェックアウトを指すので、編集がすぐ反映され、
`git pull` だけで更新が終わります。どちらのターゲットも、親ディレクトリが
存在しなければ作成します。

`~/.local/bin` が `PATH` に入っていることを確認してください
（`echo $PATH | tr ':' '\n' | grep -F "$HOME/.local/bin"`）。
入っていない場合は、シェルの rc に `export PATH="$HOME/.local/bin:$PATH"` を追記してください。

## 前提条件

- `gh` CLI、`jq`、`tmux`、`gh-sub-issue` 拡張
  （`gh extension install yahsan2/gh-sub-issue`）。fanout は起動時にこれらを
  チェックし、失敗時にはインストールのヒントを表示します。
- このマシン上で動作中の dmux セッション: `cd <repo> && dmux`。fanout は
  tmux セッションを走査して `@dmux_controller_pid` オプションを探し、PID が
  生きているかを確認することで dmux を検出します。
- **有効化されているエージェントが 1 つ**、もしくは `--agent` 指定、もしくは
  呼び出し元ペイン自身が dmux 管理下のペインであること。複数エージェントが
  有効な場合、dmux はポップアップを表示しますが fanout はエージェント名の
  1 文字目を送ってベストエフォートでナビゲートします。`--agent` 未指定時は
  `dmux.config.json` の `.panes[].paneId` と `$TMUX_PANE` を突き合わせて
  呼び出し元ペインのエージェントを自動判定します。そのため、エージェント
  セッションの中から `/fanout` を叩けば追加の flag なしで動きます。
- fanout 実行時、**dmux TUI がペイン一覧画面に居ること**（モーダルも、開いたプロンプトも
  無いこと）。fanout は各ペイン作成シーケンスの前に `Escape` を 1 回送って
  迷子のポップアップから復帰させますが、対話的な $EDITOR や確認ダイアログからは
  抜け出せません。
- **リポジトリの HEAD は、子を作る際のベースにしたいコミットに合わせておく**こと。
  dmux TUI は外部呼び出し側からペインごとにベース ref を指定する手段を
  提供していません。worktree は dmux がペインを作成した時点の HEAD から分岐します。
  親 issue が既定ブランチ以外を前提にしているなら、fanout を呼ぶ前に
  `git checkout <target>` してください。

## 使い方

```
fanout <parent-issue> [--agent <name>] [--limit <N>] [--session <tmux-session>]
                     [--sleep <seconds>] [--dry-run]
fanout --help
```

### 例

```bash
# #123 のすべての OPEN サブ issue をファンアウト
fanout 123

# 実際に dmux を動かさず、何が起こるかをプレビュー
fanout 123 --dry-run

# 今回の呼び出しを 3 件までに制限; 残り分の再実行コマンドが表示される
fanout 123 --limit 3

# dmux インスタンスが複数動いているときに特定のセッションを指定
fanout 123 --session work-repo

# 低速マシン用に dmux に作成間 8 秒の猶予を与える
fanout 123 --sleep 8

# 自動判定されたエージェントを上書きする（親ペインとは別のエージェントで
# 子ペインを立てたいときなど）。通常は不要 — fanout が dmux.config.json の
# 呼び出し元ペイン `.panes[].agent` を自動で拾う。
fanout 123 --agent codex
```

## エージェントセッション内から呼び出す

fanout は、自身が dmux ペイン内で動いているエージェントセッション（Claude Code、
Codex など）から呼び出しても安全です。fanout は `$TMUX` や cwd ではなく、tmux
セッションオプションから dmux を検出します。作るのは子用の新規ペインだけなので、
呼び出し元のペインは一切触りません。

Claude Code 向けの推奨連携 — どちらのアセットもこのリポジトリの `claude/` 配下に
同梱されており、`make install` で配置されます:

- **スラッシュコマンド** → `claude/commands/fanout.md` が
  `~/.claude/commands/fanout.md` にインストールされ、`/fanout [parent-issue]
  [--go] [extra fanout flags]` として呼び出せます。まず `fanout <N> --dry-run`
  を走らせてターゲット一覧を表示し、ユーザーが確認した後（あるいは `--go`
  が渡されたとき）にのみ本物のコマンドを実行します。
- **スキル** → `claude/skills/fanout/SKILL.md` が
  `~/.claude/skills/fanout/SKILL.md` にインストールされ、エージェントが fanout
  を使うべき場面を認識し、勝手に実行せず `/fanout` を提案するよう働きます。

上記の CLI 前提条件はそのまま適用されます: dmux セッションが生きていること、
TUI がペイン一覧画面にあること。エージェントは、呼び出し元ペイン自身が
dmux 管理下なら自動判定されるので明示不要。詳しくは **前提条件** と
**トラブルシューティング** を参照してください。

## fanout が実際にやること

1. `gh`、`jq`、`tmux`、`gh-sub-issue` がインストールされているかを確認。
2. tmux セッションを列挙。`@dmux_controller_pid` が設定されていて、その PID が
   生きているセッションのみを dmux 管理下のものとみなす。
3. セッションの `@dmux_control_pane`、`@dmux_config_path`、`@dmux_project_root`
   オプションを読み、TUI のペイン、`.dmux/dmux.config.json` ファイル、
   リポジトリルートを特定する。
4. `gh sub-issue list <parent> --json number,title,body,state` を
   プロジェクトルートから実行して子を列挙。`state == "OPEN"` のものだけを処理する。
5. 冪等性のため、`dmux.config.json` の `panes[].prompt` を走査し、
   `[fanout #<NUM>]` で始まるプロンプトを持つ issue はスキップする。
6. 各対象 issue について:
   - `/tmp/fanout-<repo>-<NUM>.md` に、issue の本文と短い Requirements チェックリスト
     からなるブリーフィングを書き出す。
   - コントロールペインに `Escape`、`n`、1 行プロンプト
     `[fanout #<NUM>] <TITLE>: read /tmp/fanout-<repo>-<NUM>.md and begin.`、
     続けて `Enter` を送る。
   - `--agent X` が渡されていれば（あるいは呼び出し元ペインの
     `.panes[].agent` から自動判定できれば）、X の 1 文字目と `Enter` を
     送ってエージェントポップアップのナビゲートを試みる。
   - `dmux.config.json` をポーリングし、`panes[].length` が増えるまで待つ（60 秒でタイムアウト）。
   - 次の処理に入る前に `--sleep` 秒（既定 4）だけスリープする。
7. 作成済み / スキップ / 保留 / 失敗の件数サマリを表示する。

## トラブルシューティング

### "no active dmux session found"

tmux セッション内でまだ `dmux` を実行していないか、コントローラープロセスが
死んでいます。確認: `tmux show-options -v -t <session> @dmux_controller_pid`。
空ならそのセッションで dmux を起動したことがありません。値はあるのに
`kill -0 <pid>` が失敗するなら、dmux がクラッシュしています — 再起動してください。

### "multiple dmux sessions active"

`--session <name>` を渡してください。
`tmux list-sessions -F '#{session_name}'` で一覧できます。

### ペイン作成がタイムアウトする（"timed out after 60s waiting for config.json to grow"）

fanout がキーシーケンスを送った時点で、TUI がペイン一覧画面に居なかった
可能性が高いです。次を確認してください:

- ポップアップ（確認ダイアログ、エージェントピッカー等）が開きっぱなし —
  dmux ペインで `Esc` を押して一覧に戻し、再実行する。
- dmux が本当に遅い（コールドクローン、巨大リポジトリ、npm インストールフック）。
  `--sleep` を増やして再試行する。new-pane 待ちは 1 件あたり 60 秒タイムアウト。
- エージェントポップアップは出たが、ナビが違うエージェントに着地した。
  有効なエージェントを 1 つだけに絞る（dmux 設定の `useHooks` / `enabledAgents`）
  か、`--agent <name>` を渡して「1 文字目 → Enter」が意図通りのエージェントを
  選ぶようにする。

### "gh sub-issue list failed"

- `gh-sub-issue` 拡張が無い: `gh extension install yahsan2/gh-sub-issue`。
- 未認証: `gh auth status`。
- 親 issue が存在しない、または拡張経由で紐づけられたサブ issue が無い:
  fanout は `no sub-issues on #<parent>` と出して exit 0 する。

### dmux TUI にプロンプトが文字化けして見える

`tmux send-keys -l` はバイトをそのまま送ります。キー再マップが激しい端末や、
ロケールの異なるリモート tmux サーバー上にコントロールペインがある場合、
UTF-8 文字が化けることがあります。issue タイトルはできるだけ ASCII にするか、
まず `--dry-run` で実際に送られる文字列を確認してください。

### 書いた覚えのない `.dmux/` 行が `.gitignore` に入っている

それは dmux 自身が起動時に追加しています（リポジトリディレクトリで `dmux --help`
を実行した直後から観測できます）。fanout のバグではありません。

## 設計メモ

- **プロンプトは 1 行のみ**。dmux TUI の ink-text-input は Enter を送信として
  扱うので、複数行プロンプトは早すぎて送信されてしまいます。fanout は完全な
  ブリーフィングを `/tmp/fanout-<repo>-<NUM>.md` に保存し、エージェントに
  それを読むよう指示します。これは同時に、worktree ディレクトリ名のキーに
  なる dmux の `slug()` が扱う文字列を短く保つ効果もあります。
- **`[fanout #NUM]` タグが冪等性のプリミティブ**。dmux はプロンプトをそのまま
  `dmux.config.json` に永続化するので、fanout はこのプレフィックスを grep
  することで作成済みペインを検出できます。fanout に再作成させたいときは、
  dmux TUI 経由でそのペイン（と worktree）を削除してください。
- **HTTP なし、ソケットなし、名前付きパイプなし**。すべての IPC は tmux
  セッションオプションと TUI 経由で行います。意図的に不格好な作りですが、
  これが現状の dmux 表面で可能な唯一のやり方です。
- **`--sleep` によるレート制限**。dmux の `usePaneCreation` は内部的に
  上限付きの並列キューを使いますが、TUI 側からは「新規ペイン」ダイアログは
  同時に 1 つしか開けません。sleep は、次の `n` を送る前に dmux が worktree
  作成フェーズを終えるための時間的余裕を与えます。
