# fanout

[English](README.md) | [日本語](README.ja.md)

GitHub の親 issue に紐づく OPEN のサブ issue を、子ごとに 1 つの dmux ペインへ
ファンアウトします。各ペインは独立した git worktree を持ち、issue ごとの
ブリーフィングファイルを参照するプロンプトでエージェント CLI が起動します。

## なぜこんな実装なのか（dmux HTTP API 調査、ポップアップ横取り）

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

`tmux send-keys` だけでも不十分です。dmux の new-pane プロンプトとエージェント
選択ダイアログは、いずれも `tmux display-popup -E 'node <script> <resultFile>'`
として起動されています（`dist/utils/popup.js` 参照）。display-popup は子コマンドを
独立した tmux client と pty で動かすので、pane ではなく、`send-keys -t <pane>`
の届かない別物です。ポップアップが開いている最中に `%0` へ文字を送っても、
ユーザーにはポップアップの裏で `%0` のバッファに溜まるだけで、ポップアップが
閉じると dmux はそれを破棄します。以前この script が「うまく動いているように
見えた」のは、ポップアップの表示だけは成功していたからです（プロンプトは
一切届いていませんでした）。

そこで採用しているのが **ポップアップ resultFile 横取り方式** です。dmux は
各ポップアップに `<tmpdir>/dmux-popup-<timestamp>.json` のパス（Linux では
典型的に `/tmp/`、macOS では `/var/folders/.../T/`）を渡し、ポップアップ
側はユーザー入力を JSON で書き込み、dmux の親プロセスはポップアップ子プロセスの
終了後にそのファイルを読みます。fanout は `Escape n` を send-keys で送って
ポップアップを起動させ、`pgrep` + `ps` でポップアップ本体の node プロセスと
resultFile パスを特定し、プロンプトポップアップ用には
`{"success":true,"data":"<prompt>"}`、ピッカー用には
`{"success":true,"data":["<agent>"]}` を atomic に書き込み、ポップアップ
プロセスを kill します。`display-popup -E` の仕組みで子コマンド終了時に
ポップアップが閉じ、dmux はこちらが書いた resultFile を読んで、人間が答えたのと
同じ経路でペイン作成を進めます。dmux が HTTP API を正式公開したら、この
スクリプトは `POST /api/panes` 数行に戻せます。

## インストール

fanout は 1 つの Bash スクリプトに、エージェント連携ファイルを加えた構成で
配布されます。Claude Code にはスラッシュコマンド + スキル群、Codex CLI には
スキル群を用意しています。すべて `Makefile` 経由で一括配置されます:

```bash
make install        # CLI + Claude/Codex 連携を ~/.local, ~/.claude, ~/.codex にコピー
make link           # チェックアウト先を指す symlink を作成（開発用）
make uninstall      # インストール済みのパスを削除

PREFIX=/usr/local sudo make install     # システム全体に CLI を配置; BINDIR を $PREFIX/bin に上書き
CLAUDE_DIR=/path/to/.claude make install # 既定以外の Claude データディレクトリを指定
CODEX_DIR=/path/to/.codex make install   # 既定以外の Codex データディレクトリを指定
```

配置パス:

- `$(BINDIR)/fanout`（既定は `~/.local/bin/fanout`）
- `$(CLAUDE_DIR)/commands/fanout.md`（既定は `~/.claude/commands/fanout.md`）
- `$(CLAUDE_DIR)/skills/fanout/`（既定は `~/.claude/skills/fanout/`）
- `$(CLAUDE_DIR)/skills/fanout-issues/`（既定は `~/.claude/skills/fanout-issues/`）
- `$(CODEX_DIR)/skills/fanout/`（既定は `~/.codex/skills/fanout/`）
- `$(CODEX_DIR)/skills/fanout-issues/`（既定は `~/.codex/skills/fanout-issues/`）

`make install` は安定しています — リポジトリを消しても、コピー済みのファイルで
動作し続けます。`make link` はチェックアウトを指すので、編集がすぐ反映され、
`git pull` だけで更新が終わります。どちらのターゲットも、親ディレクトリが
存在しなければ作成します。インストールまたはリンク後、実行中の Codex CLI
セッションがある場合は再起動すると新しいスキルを認識します。

`~/.local/bin` が `PATH` に入っていることを確認してください
（`echo $PATH | tr ':' '\n' | grep -F "$HOME/.local/bin"`）。
入っていない場合は、シェルの rc に `export PATH="$HOME/.local/bin:$PATH"` を追記してください。

## 開発

```bash
make test           # Tier 1 — フラグ/prereq の黒箱テスト (bats-core 必須)
make lint           # shellcheck fanout + テスト用 shim
```

bats: macOS は `brew install bats-core`、Debian/Ubuntu は `apt install bats`。
Tier 1 では、今後の書き換えを挟んでも維持する CLI サーフェス (エラーメッセージ +
exit code) を凍結しています。Tier 2 (`--dry-run` ゴールデン出力) は後続 PR で
追加中。Tier 3 (live dmux E2E) は手動運用のままです。

## 前提条件

- `gh` CLI、`jq`、`tmux`、`pgrep`、`gh-sub-issue` 拡張
  （`gh extension install yahsan2/gh-sub-issue`）。fanout は起動時にこれらを
  チェックし、失敗時にはインストールのヒントを表示します。子 issue は
  Sub-issues API 経由でも、親本文のタスクリスト（`- [ ] #NUM ...`）経由でも、
  あるいは両方で宣言されていても構いません。fanout は両ソースの和集合を取ります。
- このマシン上で動作中の dmux セッション: `cd <repo> && dmux`。fanout は
  tmux セッションを走査して `@dmux_controller_pid` オプションを探し、PID が
  生きているかを確認することで dmux を検出します。
- **エージェント名が解決できること**: `--agent <name>` を渡すか、呼び出し元
  ペイン自身が dmux 管理下のペインで fanout が `dmux.config.json`
  （`.panes[].paneId` と `$TMUX_PANE` の突き合わせ）から自動判定できること。
  dmux v5.6.3 は、プロンプトポップアップの後に**有効エージェントが 1 つでも**
  必ずエージェント選択ポップアップを開くため、fanout は横取り用の agent 名を
  必要とします。エージェントセッション内から同梱の Claude/Codex 連携経由で
  呼び出す場合は追加の flag なしで動きます。素のシェルから叩く場合は
  `--agent` 必須です。
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
fanout <parent-issue> [--agent <name>] [--limit <N>] [--only <list>] [--skip <list>]
                     [--include <list>] [--unblocked-only]
                     [--name <NUM>=<slug>[|<display>]]
                     [--session <tmux-session>] [--sleep <seconds>]
                     [--popup-timeout <seconds>] [--dry-run] [--debug]
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

# 非連続な一部の子 issue だけをファンアウト（OPEN 子集合に無い番号は
# 警告付きで無視される。fanout が勝手に任意の issue を見に行くことは無い）
fanout 123 --only 4,7,8,10

# 指定した子 issue を除外して残りをファンアウト; --limit と組み合わせ可
fanout 123 --skip 6,9 --limit 3

# fanout の自動検出（Sub-issues API + タスクリスト）では拾われない子を強制追加する。
# 親本文で `Closes #N` / `Depends on #N` / 素の箇条書き / 「#N に関連」のような
# 日本語表現だけで言及されている子などが対象。同梱の Claude/Codex 連携経由で
# 呼ぶと、エージェントが親本文を読んで候補を提示し、承認された番号をこの flag
# に載せる。CLI 直接利用時はここに番号を明示する。CLOSED や存在しない番号は
# 警告してスキップ。--only / --skip と併用可（include で追加した後にフィルタ適用）。
fanout 123 --include 4,7

# ブロッカーがすべて CLOSED の子だけをファンアウト
fanout 123 --unblocked-only

# ブロッカー解除済みの次バッチを 3 件までに制限
fanout 123 --unblocked-only --limit 3

# 子ごとの branch/worktree slug とペインタイトルを指定
fanout 123 --name 4=fix-login-timeout --name 7='update-docs|Docs update'

# dmux インスタンスが複数動いているときに特定のセッションを指定
fanout 123 --session work-repo

# 低速マシン用に dmux に作成間 8 秒の猶予を与える
fanout 123 --sleep 8

# 各 dmux ポップアップ出現待ちを長めにする（巨大リポジトリで worktree 作成が
# 遅く、ポップアップ間のギャップが既定 20 秒を超える環境向け）
fanout 123 --popup-timeout 45

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

Claude Code 向けの推奨連携 — これらのアセットはこのリポジトリの `claude/` 配下に
同梱されており、`make install` で配置されます:

- **スラッシュコマンド** → `claude/commands/fanout.md` が
  `~/.claude/commands/fanout.md` にインストールされ、`/fanout [parent-issue]
  [--go] [extra fanout flags]` として呼び出せます。まず `fanout <N> --dry-run`
  を走らせてターゲット一覧を表示し、ユーザーが確認した後（あるいは `--go`
  が渡されたとき）にのみ本物のコマンドを実行します。
- **スキル** → `claude/skills/fanout/SKILL.md` が
  `~/.claude/skills/fanout/SKILL.md` にインストールされ、エージェントが fanout
  を使うべき場面を認識し、勝手に実行せず `/fanout` を提案するよう働きます。
  加えてスキルは、`fanout` 本体がパースしない**暗黙の子参照** — `Closes #N`
  などのクローズキーワード、`Depends on #N` / `Related to #N` といった依存/関連
  表現、チェックボックス無しの素の箇条書き (`- #N`)、`#N に関連` / `#N を対応`
  のような日本語の慣用句 — を親本文から読み取り、候補をユーザーに提示して
  承認された番号を `--include` で fanout に渡します。
- **issue 作成スキル** → `claude/skills/fanout-issues/SKILL.md` が
  `~/.claude/skills/fanout-issues/SKILL.md` にインストールされ、計画を
  fanout 向けの GitHub 親 issue + 子 issue 群へ変換する場面で使われます。
  同一リポジトリ内の子 issue を作成し、GitHub Sub-issues と親本文のタスクリストの
  両方へ反映し、`fanout --unblocked-only` が読める `## Blocked by` /
  `(blocked by #N)` 形式で依存関係の wave も記録します。

Codex CLI 向けの推奨連携 — スキルはこのリポジトリの `codex/` 配下に同梱され、
`make install` で配置されます:

- **スキル** → `codex/skills/fanout/SKILL.md` が
  `~/.codex/skills/fanout/SKILL.md` にインストールされます。インストール後、
  実行中の Codex セッションがある場合は再起動してください。Codex に
  「#123 を fan out して」などと依頼するか、明示的に `$fanout` を指定すると
  このワークフローを使います。Claude のコマンドと同じく、まず dry-run で
  対象を確認し、ユーザー確認後に本実行します（確認不要と明示された場合を除く）。
  暗黙の子参照の scan と `--name` 生成もスキル側で行います。
- **issue 作成スキル** → `codex/skills/fanout-issues/SKILL.md` が
  `~/.codex/skills/fanout-issues/SKILL.md` にインストールされます。Codex に
  fanout 向けの GitHub issue ツリー作成、計画の親子 issue 化、
  `fanout --unblocked-only` 用の blocker wave 作成を依頼したときに使います。
  Claude 版と同じく、同一リポジトリ内の子 issue、GitHub Sub-issues のリンク、
  親本文のタスクリスト、`## Blocked by` 注記を揃えます。

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
4. 2 つのソースの和集合で子を列挙する（いずれもプロジェクトルートから実行）:
   (a) `gh sub-issue list <parent>` で Sub-issues API に正式リンクされている
   子、(b) 親本文中の GitHub タスクリスト参照 — `^\s*-\s+\[[ xX]\] ... #NUM`
   にマッチする行の `#NUM` を拾う（同一リポジトリ内のみ。`owner/repo#NUM`
   形式はスキップ）。本文由来の番号は `gh issue view` で本体情報を引く。
   `state == "OPEN"` の子のみを処理する。
5. 冪等性のため、`dmux.config.json` の `panes[].prompt` を走査し、
   `[fanout #<NUM>]` で始まるプロンプトを持つ issue はスキップする。
6. 各対象 issue について:
   - `/tmp/fanout-<repo>-<NUM>.md` に、issue の本文と短い Requirements チェックリスト
     からなるブリーフィングを書き出す。
   - コントロールペインに `Escape` と `n` を送り、dmux の new-pane ポップアップ
     （インラインモーダルではなく `tmux display-popup` の子プロセス）を起動させる。
   - `pgrep -f 'newPanePopup.js'` でポップアップ node プロセスを特定し、
     `ps -o args=` から `<tmpdir>/dmux-popup-*.json` の resultFile パスを抽出、
     `{"success":true,"data":"[fanout #<NUM>] <TITLE>: read /tmp/fanout-<repo>-<NUM>.md and begin."}`
     を atomic に書き込み、dmux が横取りした答えを読むようにポップアップを kill する。
   - 続けて dmux が起動するエージェント選択ポップアップについても同様に横取りし、
     `{"success":true,"data":["<agent>"]}` を書き込む。`--agent` 指定、もしくは
     呼び出し元ペインから自動判定したエージェント名を使う。
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

fanout がキーシーケンスを送った時点で TUI がペイン一覧画面に居なかったか、
ポップアップ横取りに失敗しています。次を確認してください:

- ポップアップ（確認ダイアログ、エージェントピッカー等）が開きっぱなし —
  dmux ペインで `Esc` を押して一覧に戻し、再実行する。
- dmux が本当に遅い（コールドクローン、巨大リポジトリ、npm インストールフック）。
  `--sleep` を増やして再試行する。new-pane 待ちは 1 件あたり 60 秒タイムアウト。
- `--debug` を付けて再実行し、どの段階で失敗しているかを確認する。よくあるケース:
  - `newPanePopup did not appear within 20s` — `n` に dmux が反応していない。
    別のポップアップが既に開いていることが多いので、手動で `Esc` を押してから再実行。
  - `agentChoicePopup did not appear within 20s` — プロンプトポップアップは閉じたが、
    既定の待ち時間内にエージェント選択ポップアップが出てこない。低速マシンや
    大きな worktree ではこのギャップが既定を超えることがあるので、
    `--popup-timeout 45` のように増やして再試行する。それでも出てこない場合は、
    dmux 設定で少なくとも 1 つのエージェントが有効になっているかを確認する。
- dmux を v5.6.x より上に更新した結果、ポップアップスクリプト名や
  result JSON の形が変わっている。`~/.../dmux/dist/utils/popup.js` と
  `dist/components/popups/shared/PopupWrapper.js` を確認する。fanout は
  `{"success":true,"data":...}` という形を前提にしているので、dmux 側で
  変更があれば issue を上げてほしい。

### "gh sub-issue list failed"

- `gh-sub-issue` 拡張が無い: `gh extension install yahsan2/gh-sub-issue`。
- 未認証: `gh auth status`。
- 親 issue が存在しない、または拡張経由で紐づけられたサブ issue が無い:
  fanout は `no sub-issues on #<parent>` と出して exit 0 する。

### dmux TUI にプロンプトが文字化けして見える

プロンプト本文は `send-keys -l` ではなくポップアップ resultFile への JSON
書き込み経由で渡しているので、UTF-8 タイトルは dmux まできれいに往復します。
それでも文字化けするなら、呼び出し側の `jq` が妥当な JSON を生成しているか
（`echo "<title>" | jq -Rs` がクォート済みの文字列を返すか）と、
`dmux.config.json` にそのまま保存されているかを確認してください。書き込まれる
JSON を事前に確認するには `--dry-run` を使います。

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
- **IPC の経路**。検出には tmux セッションオプション
  （`@dmux_controller_pid`、`@dmux_control_pane`、`@dmux_config_path`、
  `@dmux_project_root`）を、ペイン作成の誘発と回答投入には dmux ポップアップの
  resultFile（`<tmpdir>/dmux-popup-*.json`）への atomic 書き込みを使います。
  HTTP なし、ソケットなし、名前付きパイプなし。意図的に不格好な作りですが、
  これが現状の dmux 表面で可能な唯一のやり方です。
- **`--sleep` によるレート制限**。dmux の `usePaneCreation` は内部的に
  上限付きの並列キューを使いますが、TUI 側からは「新規ペイン」ダイアログは
  同時に 1 つしか開けません。sleep は、次の `n` を送る前に dmux が worktree
  作成フェーズを終えるための時間的余裕を与えます。
