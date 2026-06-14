# fanout

[English](README.md) | [日本語](README.ja.md)

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

**ドキュメントサイト:** <https://butaosuinu.github.io/fanout/ja/> —
インストールからワークフロー、CLI リファレンスまでの完全ガイド(日英)。

`fanout` は、並列 issue 作業のための standalone な tmux ベースのコンソール兼
ランチャーです。引数なしの `fanout` で現在のリポジトリ用の常駐 TUI コンソールを
開き、manual agent pane の作成、既存 child pane への focus、close / merge /
cleanup を行えます。既存の `fanout <parent-issue|project-url>` レーンでは、
既知の親 issue / Project を子ごとに 1 つの tmux ペインへファンアウトし、各ペインに
独立した git worktree と issue ごとの briefing file を渡した agent CLI を起動します。
さらに `fanout plan <spec|slug>` はローカルの plan spec から GitHub issue を作らずに
task pane を起動する issue-less レーンを、`--team` と `fanout msg` は per-parent の
SQLite バス上で兄弟ペイン同士が連絡を取り合う peer messaging を提供します（いずれも
下部に専用節があります）。

## 常駐 TUI コンソール

引数なしの `fanout` で常駐コンソールを起動します。素のシェルから起動した場合は、
現在のリポジトリ用の deterministic な fanout 管理 tmux session を作成または
attach し、その session 内でコンソールを開始します。tmux 内から起動した場合は、
現在の pane をそのままコンソール画面にします。

典型的な単体運用フロー:

1. 対象リポジトリで `fanout` を実行する。
2. `n` で manual agent pane を作成する。親 issue / Project を既存フラグ付きで
   まとめて展開したい場合は、tmux 内から `fanout <parent>` を使う。
3. `Enter` / `o` で live pane に focus し、`c` で close、`m` で branch を
   fast-forward merge、`x` で merged/closed sibling を cleanup する。
4. `q` でコンソールを離脱する。tmux session と子 pane は残る。

コンソールは `<git-root>/.fanout/state.json` を読み、記録済み pane ID が tmux 上に
まだ存在するかを確認し、`fanout <parent> --status` と同じ GitHub CLI 経路で
issue / closed-by PR 状態を定期更新します。各行には pane worktree の総作業量
`+X/-Y`（記録した base ブランチとの merge-base に対する `git diff --shortstat`。
コミット済み + 未 commit の合計で、base 未記録の旧行は `origin/HEAD` → `HEAD` に
fallback）と、`git status --porcelain` による `dirty`/`clean` も表示するため、
agent 側の instrumentation なしで未 commit 作業の有無を確認できます。
`/` でロード済み行をメモリ内検索し、
`state:open`、`agent:codex`、`wave:wave5` のような述語でも絞り込めます。
フィルタは追加 fetch を発生させず、フィルタ中も state / GitHub の自動更新は
継続します。記録済みの issue 親については親の子一覧も再読込し、
`--unblocked-only` と同じ `## Blocked by` / `(blocked by #N)` から wave / blocker
列を表示します。まだ fanout されていない blocked 子は `deferred` 行で表示され、
CLOSED blocker は resolved として区別されます。TUI と Web dashboard は同じ
Session snapshot model を読むため、label、filter 語彙、PR/CI summary、synthetic 行の
state が両 surface で揃います。ヘッダーには `total` / `merged` /
`pending` / `blocked` の集約 count を表示します。`n` で必須 prompt、`claude` / `codex`
の agent 選択、任意 slug を指定して manual agent pane を作成できます。manual pane
は synthetic な `@manual` state entry として記録され、起動後に一覧へ表示されます。
live 行で `Enter` または `o` を押すとその pane にフォーカスし、`p` で detail
panel の read-only 出力スナップショットを更新します。記録はあるものの tmux 上に
存在しない pane は `stale!` と表示し、focus / peek の対象から除外します。`q` で
コンソールを離脱できますが、tmux session と子 pane は残ります。
記録済み pane を選択して `c` で close、`m` で branch の fast-forward merge、
`x` で同じ親の merged/closed 子を cleanup できます。各 lifecycle 操作は確認を挟み、
対応する `--close` / `--merge` / `--cleanup` CLI コマンドと同じコア処理を使います。
コンソールは連続する GitHub refresh snapshot を比較し、子が merged になったとき、
PR の最新 CI が failing になったとき、または子が OPEN blocker 待ちになったときに
遷移ごとに 1 回通知します。既定は端末ベルです。設定で tmux status-line、ntfy、
Slack webhook POST を opt-in できます。

## 既存フラグでの一括 fan-out

親 issue や Project URL が分かっている場合は、`fanout <target>` が一括作成レーンです。
`git rev-parse --show-toplevel` でリポジトリルートを解決し、選択された base branch
を fresh 化し、`.fanout/worktrees/<slug>/` を作成し、起動元 tmux pane を
`tmux split-window` で分割し、選択された agent CLI を 1 行 briefing prompt 付きで
起動します。作成した pane は `.fanout/state.json` に `(parent, issueNum)` キーで
記録するため、同じ親での再実行では記録済みの子を重複作成しません。この経路は
tmux を直接使い、dmux は不要です。

## Plan spec

`fanout plan <spec.json|plan-slug>` は、GitHub child issue ではなくローカルの
task spec に分解済みの作業を起動する issue-less な一括作成レーンです。spec は
`version: 1`、`plan` オブジェクト（`slug`、`title`、任意の `base_branch`）と、
kebab-case の `id`、`title`、`briefing` を持つ `tasks` 配列で構成します。task には
任意で `slug`、`display_name`、`branch`、`wave`、`blocked_by` task ID を指定できます。
bare な `plan-slug` は `<git-root>/.fanout/plans/<plan-slug>.json` から読み込みます。
live run では後続の再実行用に、元 spec をそのディレクトリへコピーします。

Plan pane は parent `plan:<slug>`、`taskId`、`issueNum: 0` として記録されるため、
再実行時は `.fanout/state.json` や `.fanout/worktrees/` に既存の task pane があれば
重複作成しません。`--dry-run` で git/tmux/agent 操作を確認でき、`--only` / `--skip`
は task ID、`--limit` は wave の一部起動、`--unblocked-only` は `blocked_by` 依存が
明示 branch または生成 branch 上の merged PR で完了するまで deferred にするために
使います。生成される task briefing は issue-closing footer を避け、task PR の末尾に
`Plan: <slug> / Task: <id>` を置くよう指示します。
`fanout plan <spec|slug> --status [--format json|table]` で task の PR / blocker
状態を確認でき、`--close <task-id>` / `--merge <task-id>` は記録済み task 1 件を
対象にします。`--cleanup` は head branch に merged PR がある記録済み task pane を
削除します。

## Project モード

第1引数には親 issue 番号だけでなく、Projects v2 の URL —
`https://github.com/users/<owner>/projects/<n>` または
`https://github.com/orgs/<org>/projects/<n>` — も渡せます。正規形の
`/views/<id>` サフィックスやトレイリングのクエリ文字列付き URL も受け付ける
ので、ブラウザのアドレスバーからそのままコピペできます。Project モードでは
親 issue の Sub-issues + タスクリスト和集合の代わりに、Project items から
子 issue を取り出します。

- **既定フィルタは `Status == Todo`**。`--project-status "<name>"` で別の
  single-select 値（例: `"In Progress"`）に切り替え、`--project-status all`
  でフィルタを無効化して全 item（Done、Status 未設定なども含む）を対象に
  します。
- **親 body が無いので暗黙の子参照サルベージは無い**。同梱の Claude/Codex
  スキルが通常拾う `Closes #N` / `Depends on #N` / 日本語の慣用句は
  Project モードでは検出対象になりません — Project が source of truth。
  Project が取りこぼしている子は `--include 4,7` で手動補完してください。
- **同一リポジトリ前提は維持**。`content.repository` が現在の git repository root
  の repo と一致しない item は warn ログを出してスキップします（fanout は今でも
  1 回 1 repo の前提）。
- **Project に Status フィールドが無い場合** は warn を出して
  `--project-status` を無視し、全 item を対象にフォールバックします。
- **冪等性と `--unblocked-only` は issue モードと共通**。action mode は
  `.fanout/state.json` に記録済みの同じ `(parent, issueNum)` の子をスキップします。
  移行用 fallback として、state に未記録でも既存の `.fanout/worktrees/<slug>`
  directory がある子もスキップします。同じ issue が別の親に記録済みの場合は、
  別親の default worktree は無視し、今回の run が作る slug と一致する worktree が
  既にある場合だけ中断復旧用にスキップします。Project モードでの blocker
  情報源は child body の `## Blocked by` セクションと
  `blocked` ラベルのみ（親 body の `(blocked by #X)` トレイラは存在しない）。

## インストール

推奨インストール経路は Release 済みの Go バイナリです。安定コマンド名
`fanout` と、同梱の Claude/Codex 連携ファイルをまとめて配置します:

```bash
# fanout + Claude/Codex 連携を ~/.local, ~/.claude, ~/.codex に配置
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | sh

# バイナリのみ
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | sh -s -- --no-skills

# 配置先や Release tag を指定
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | BIN_DIR=/usr/local/bin sh
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | FANOUT_VERSION=v0.2.0 sh
```

配置パス:

- `$BIN_DIR/fanout`（既定は `~/.local/bin/fanout`）
- `$CLAUDE_DIR/commands/fanout.md`（既定は `~/.claude/commands/fanout.md`）
- `$CLAUDE_DIR/commands/pr-watch.md`（既定は `~/.claude/commands/pr-watch.md`）
- `$CLAUDE_DIR/skills/fanout/`（既定は `~/.claude/skills/fanout/`）
- `$CLAUDE_DIR/skills/fanout-issues/`（既定は `~/.claude/skills/fanout-issues/`）
- `$CLAUDE_DIR/skills/fanout-plan/`（既定は `~/.claude/skills/fanout-plan/`）
- `$CLAUDE_DIR/skills/post-work-review/`（既定は `~/.claude/skills/post-work-review/`）
- `$CLAUDE_DIR/skills/pr-watch/`（既定は `~/.claude/skills/pr-watch/`）
- `$CODEX_DIR/skills/fanout/`（既定は `~/.codex/skills/fanout/`）
- `$CODEX_DIR/skills/fanout-issues/`（既定は `~/.codex/skills/fanout-issues/`）
- `$CODEX_DIR/skills/fanout-plan/`（既定は `~/.codex/skills/fanout-plan/`）
- `$CODEX_DIR/skills/post-work-review/`（既定は `~/.codex/skills/post-work-review/`）
- `$CODEX_DIR/skills/pr-watch/`（既定は `~/.codex/skills/pr-watch/`）

`install.sh` は macOS/Linux と amd64/arm64 を自動判定し、最新 GitHub Release
（または `FANOUT_VERSION` で指定した tag）から
`fanout_<os>_<arch>.tar.gz` を取得します。`sha256sum` または `shasum` が
あれば `SHA256SUMS` で検証し、再実行時は同じパスへ上書きします。シェル rc は
自動編集しません。削除は次で行えます:

```bash
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | sh -s -- --uninstall
```

`~/.local/bin` が `PATH` に入っていることを確認してください
（`echo $PATH | tr ':' '\n' | grep -F "$HOME/.local/bin"`）。
入っていない場合は、シェルの rc に `export PATH="$HOME/.local/bin:$PATH"` を追記してください。
スキルをインストールまたは更新した後、実行中の Codex CLI セッションがある場合は
再起動すると新しいファイルを認識します。

### macOS セキュリティメモ

curl/wget 経由のインストールでは通常 `com.apple.quarantine` 拡張属性が付かない
ため、Gatekeeper の「開発元を検証できません」GUI ブロックは基本的に発生しません。
ブラウザ経由でアーカイブを取得して quarantine が付いた場合は、展開後に次で
属性を削除してください:

```bash
xattr -d com.apple.quarantine /path/to/fanout
```

Apple Silicon では、すべての実行ファイルに最低限 ad-hoc 署名が必要です。Release
workflow は macOS 上で Go 1.26 の darwin バイナリをビルドするため、Go linker
がビルド時に署名します。Release package 作成後に外部 `strip` をかけると署名が
壊れることがあるので避けてください。ローカルコピーが壊れた場合は次で
ad-hoc 再署名できます:

```bash
codesign -s - /path/to/fanout
```

Apple Developer ID 署名や notarization は curl 配布経路では当面スコープ外です。
ブラウザ、dmg/pkg、管理対象 Mac 向け配布が必要になった段階で追加できます。

### チェックアウトから使う場合

ローカルの Makefile ターゲットは Go バイナリを安定コマンド名 `fanout` として
インストール / symlink し、同梱の連携ファイルも配置します:

```bash
make install        # Go 版を $(BINDIR)/fanout としてビルド + 連携をコピー
make link           # Go 版を $(BINDIR)/fanout として symlink + 連携を symlink
make uninstall      # インストール済みのパスを削除

PREFIX=/usr/local sudo make install     # システム全体に Go CLI を配置
CLAUDE_DIR=/path/to/.claude make install # 既定以外の Claude データディレクトリを指定
CODEX_DIR=/path/to/.codex make install   # 既定以外の Codex データディレクトリを指定
```

チェックアウトからのビルドには **Go ツールチェイン**（Go 1.26+）に加えて
**Node.js 24+ と pnpm 10+** が必要です。`make install`・`make link`・
`make build-go` はまずダッシュボード Web UI をビルドし(`make build-web`、
`web/` の Vite バンドル)、それを embed して `go build ./cmd/fanout` を実行
します。上記の curl インストールは prebuilt バイナリを配置するので Go も
Node も不要です。

## 開発

```bash
make test           # Go ユニットテスト + Web UI テスト + Tier 1 + Tier 2 黒箱テスト (bats-core 必須)
make test-tier1     # フラグ / prereq テストのみ
make test-tier2     # --dry-run ゴールデン出力テスト (fixture 駆動)
make test-web       # ダッシュボード Web UI テスト (vitest)
make lint           # pinned golangci-lint v2 (.golangci.yml) + テスト用 shim の shellcheck
make lint-web       # ダッシュボード Web UI の lint (oxlint + oxfmt --check + tsc --noEmit)
make fmt            # golangci-lint fmt による gofumpt/goimports 整形
make fmt-web        # ダッシュボード Web UI の整形 (oxfmt)
make fix            # go fix のイディオム更新 (適用後は make test を実行)
make vuln           # govulncheck (ネットワーク要。意図的に make lint には含めない)
make build-web      # ダッシュボード Web UI を internal/dashboard/static/ にビルド
make build-go       # Web バンドル + Go CLI を ./fanout-go としてビルド
```

bats: macOS は `brew install bats-core`、Debian/Ubuntu は `apt install bats`。
黒箱テストの各 Tier は `./fanout-go` をビルドし `FANOUT_BIN` 経由で実行します。
Tier 1 は CLI サーフェス (エラーメッセージ + exit code)、Tier 2 は `--dry-run`
の計画出力を `tests/fixtures/` 配下のシナリオ fixture に対して凍結します。
`--dry-run` 出力を意図的に変更した場合は
`FANOUT_GOLDEN_UPDATE=1 make test-tier2` で golden を再生成してください。
Tier 3 (live tmux E2E) は手動運用のままです。

ダッシュボード Web UI は `web/`(React + Vite + TypeScript、pnpm)にあります。
ビルド成果物は**コミットしません**。`make build-web` が
`internal/dashboard/static/` にバンドルを出力し、`go:embed` がそれを取り込み
ます(バンドル無しのチェックアウトもコンパイルでき、その場合は「make
build-web を実行してください」ページを配信します)。ホットリロード付きで UI
を開発するには、データサーバと Vite dev サーバ(`/api/*` を proxy)を起動し
ます:

```bash
./fanout-go dashboard --web --port 7777 --no-token   # ターミナル 1
cd web && pnpm install && pnpm dev                   # ターミナル 2 → http://localhost:5173
```

## 前提条件

- 既定の fanout 作成フローでは `gh` CLI、`git`、`tmux` が必要です。`--status` と
  `--cleanup` は `gh`/`git`、`--merge` と `--close` は `git` を使います
  （`--close`/`--cleanup` の tmux pane kill は pane が既に無い場合 stale として
  扱います）。fanout は必要な依存を起動時にチェックし、失敗時には
  インストールのヒントを表示します。子 issue は
  Sub-issues API 経由でも、親本文のタスクリスト（`- [ ] #NUM ...`）経由でも、
  あるいは両方で宣言されていても構いません。fanout は両ソースの和集合を取ります。
- **Project モード時のみ**: Project items を取得する GraphQL クエリのため、
  `gh` CLI に `read:project` スコープが必要です。`gh auth refresh -s read:project`
  で付与してください。issue モード（`fanout <N>`）では不要です。
- **`--team` / `fanout msg`** は per-parent の SQLite データベースを使いますが、
  ドライバは pure-Go で binary に同梱されています。外部 `sqlite3` の追加
  インストールは**不要**です。
- 起動レーンを選んでください:
  - TUI モード（引数なしの `fanout`）は素のシェルから起動できます。現在の
    リポジトリ用の fanout 管理 tmux session を作成または attach してから
    コンソールを開始します。tmux 内から起動した場合は現在の session / pane を
    使います。
  - 一括 pane 作成モード（`fanout <parent-issue|project-url> ...`）は tmux
    セッション内から実行してください。子ペインは `tmux split-window` で直接作成し、
    `--session` 未指定時は起動元 pane を target にします。
- **エージェント名が解決できること**: `--agent claude` / `--agent codex` を渡すか、
  `FANOUT_AGENT` を設定してください。子 issue 1 件だけを変える場合は
  `--agent NUM=name`、`fanout plan` では `--agent task-id=name` を繰り返します。
  選択対象の未知 agent はペイン作成前に失敗し、実行時には agent CLI が
  インストール済みかも確認します。
- 子 worktree は `.fanout/worktrees/<slug>/` に作成されます。分岐前に
  `git fetch --quiet --no-tags` と fast-forward で base branch を fresh 化します。
  base を変える場合は `--base-branch <branch>` を使います（bare な local branch 名と
  `origin/<branch>` に対応）。refresh を飛ばす場合は `--no-refresh` を使ってください。
  live 実行時には対象 repo の local `.git/info/exclude` に `.fanout/worktrees/` を
  追記し、生成 worktree が `git status` を汚さないようにします。

## 使い方

```
fanout # 常駐 tmux コンソールを起動
fanout <parent-issue|project-url>  # 一括 pane 作成; tmux 内から実行
       [--agent <name|NUM=name>] [--limit <N>] [--only <list>] [--skip <list>]
       [--include <list>] [--unblocked-only] [--project-status <name>]
       [--name <NUM>=<slug>[|<display>[|<branch>]]]
       [--base-branch <branch>] [--branch-prefix <prefix>] [--no-refresh]
       [--session <tmux-session>] [--sleep <seconds>]
       [--popup-timeout <seconds>] [--dry-run] [--debug]
       [--auto-pr|--no-auto-pr] [--pr-review-gate|--no-pr-review-gate]
       [--briefing-code-review|--no-briefing-code-review]
       [--agent-teams-hint|--no-agent-teams-hint]
       [--codex-plan-mode|--no-codex-plan-mode]
       [--pr-visualization|--no-pr-visualization]
       [--team]
fanout plan <spec.json|plan-slug> [--agent <name|task-id=name>] [--dry-run]
       [--limit <N>] [--only <task-id[,id...]>] [--skip <task-id[,id...]>]
       [--unblocked-only] [--base-branch <branch>] [--branch-prefix <prefix>]
       [--no-refresh] [--session <tmux-session>] [--sleep <seconds>]
fanout plan <spec.json|plan-slug> --status [--format json|table]
fanout plan <spec.json|plan-slug> --merge <task-id>
fanout plan <spec.json|plan-slug> --close <task-id>
fanout plan <spec.json|plan-slug> --cleanup
fanout <parent-issue> --status [--format json|table] [--post-dashboard]
                                      # 状態を読み、任意で dashboard を投稿
fanout <parent-issue> --merge <NUM> # 記録済み子 branch を ff-only merge
fanout <parent-issue> --close <NUM> # 記録済み子 worktree/pane を後始末
fanout <parent-issue> --cleanup     # merge/close 済みの記録済み子を後始末
fanout dashboard --web              # 読み取り専用の localhost Web ダッシュボード（Session 表示）
fanout msg <verb> [options] [body...]  # 兄弟ペイン間の peer messaging（下記参照）
fanout --check-update               # この binary と最新 release を比較
fanout update                       # install.sh 経由で binary + integrations を置換
fanout --help
```

第1引数は GitHub issue 番号（Sub-issues + タスクリストモード）または
Projects v2 URL（Project モード、上記参照）のいずれか。`--project-status`
は Project モードでのみ意味を持ち、issue モードでは無視されます。
`--popup-timeout` は旧ランタイム互換の deprecated flag で、direct tmux path
では受け付けるだけで無視されます。

### Plan fan-out (issue-less)

`fanout plan <spec.json|plan-slug>` は、GitHub child issue ではなくローカル
JSON spec から task pane を起動する lane です。実装計画がすでにローカル task に
分解されていて、issue ツリーを作るとノイズになる場合に使います。path または
`*.json` 引数はそのまま読み、bare slug は
`<git-root>/.fanout/plans/<slug>.json` を読みます。live run は元 spec をそこへ
コピーするため、以後は slug だけで再実行できます。

spec フォーマットのリファレンス:

```json
{
  "version": 1,
  "plan": {
    "slug": "launch-plan",
    "title": "Launch plan",
    "source": "docs/launch.md",
    "base_branch": "main"
  },
  "tasks": [
    {
      "id": "base-types",
      "title": "Define base types",
      "briefing": "## Goal\nDefine the shared types.",
      "display_name": "Base types",
      "wave": "1"
    },
    {
      "id": "api-client",
      "title": "Extract API client",
      "briefing": "## Goal\nExtract the API client.",
      "blocked_by": ["base-types"],
      "wave": "2"
    }
  ]
}
```

必須 field は `version: 1`、`plan.slug`、`plan.title`、そして kebab-case
`id`、`title`、空でない `briefing` を持つ task 1 件以上です。任意 field は
`plan.source`、`plan.base_branch`、task ごとの `slug`、`display_name`、
`branch`、`wave`、`blocked_by` です。既定の worktree slug は plan 名で
qualify されます（上例なら `launch-plan-define-base-types-base-types`）。
生成 branch は `fanout/<slug>` で、task の `branch` があればそれをそのまま使います。

```bash
# 生成される worktree、branch、tmux command、task briefing を確認
fanout plan /tmp/fanout-plan-launch-plan.json --agent claude --dry-run

# 現時点で unblock されている task を起動
fanout plan /tmp/fanout-plan-launch-plan.json --agent claude --unblocked-only

# 保存済み plan を slug で再実行し、この wave を 2 件に制限
fanout plan launch-plan --agent claude --unblocked-only --limit 2

# task の PR 状態を確認。既定は JSON、table は PR state / CI / type /
# files / diff bar / link を追加
fanout plan launch-plan --status
fanout plan launch-plan --status --format table

# lifecycle は issue 番号ではなく task ID を指定
fanout plan launch-plan --merge base-types
fanout plan launch-plan --close base-types
fanout plan launch-plan --cleanup
```

`--only` と `--skip` は issue 番号ではなく task ID を受け取ります。
`--unblocked-only` は各 task の `blocked_by` を、依存 task の明示 branch または
生成 branch に merge 済み PR があるかで判定します。依存に merge 済み PR がまだ
無い task は `deferred (blocked)` と報告され、その run では pane を作りません。
`--status` は plan task に GitHub issue 番号が無いため、issue closed-by ではなく
`gh pr list --head <branch>` を使います。Plan task の row は
`.fanout/state.json` に parent `plan:<slug>`、`taskId`、`issueNum: 0` で記録され、
再実行時は state または `.fanout/worktrees/` に既にある row をスキップします。

Plan task briefing は標準の fanout briefing と同じですが、issue-closing footer は
要求しません。auto-PR guidance は、GitHub issue が無くても task を識別できるよう、
PR body の末尾を `Plan: <slug> / Task: <id>` にするよう指示します。

同梱の agent 連携を使う場合、Claude Code の `/fanout plan ...` は
`~/.claude/skills/fanout-plan/` へ、Codex の `$fanout-plan` または "fanout plan"
依頼は `~/.codex/skills/fanout-plan/` へ routing されます。skill は spec を
作成または選択し、まず dry-run を実行し、task / wave / branch を要約してから、
wrapper で確認スキップを明示されていない限り確認後に live command を実行します。

exit code は既存 lane に従います。通常 / dry-run の `fanout plan` は、成功または
何もすることが無い場合 `0`、環境・spec・filter・preflight・launch の失敗で `1`、
不正な呼び出しで `2` を返します。`fanout plan <spec> --status` は、status 出力で
`0`、`git` や `gh` などの必須 dependency が無い場合 `1`、不正な呼び出し・
読めない / 壊れた spec/state・使えない project root で `2`、GitHub PR lookup
失敗で `3` です。Plan `--close` / `--merge` / `--cleanup` は、成功（cleanup
対象無しを含む）で `0`、環境・git・cleanup 失敗で `1`、記録済み task target が
不正な場合 `2`、cleanup が branch PR 状態を取得できない場合 `3` を返します。

### Codex Plan Mode

`--codex-plan-mode` は per-target の `--agent` 上書き適用後に `codex` へ解決される
子向けの opt-in 起動モードです。通常の positional `codex "<prompt>"` ではなく、
子ごとに Codex app-server を起動し、その thread を collaboration mode `plan` で
作成し、fanout prompt を app-server 経由で initial turn として開始してから、その
remote session に interactive Codex TUI を attach します。子 briefing も Plan Mode
向けに差し替わり、`<proposed_plan>` に包んだ実装計画を出すこと、最初の turn では
ファイル編集・commit・push・PR 作成をしないことを明示します。

この経路では tmux 経由で `/plan` や prompt text を送信しません。pane は interactive
Codex TUI session のまま残るため、ユーザーはその Plan Mode 会話から続行できます。
app-server Plan turn setup または TUI attach に失敗した場合は、state 記録前に
launch を失敗扱いにし、pane/worktree を cleanup するため、同じ child を再実行できます。

### `--status` / lifecycle

`fanout <parent> --status` は読み取り専用です。`<git-root>/.fanout/state.json`
（または `FANOUT_STATE_PATH` で指定した state file）から指定 parent の記録済み
子 issue を列挙し、各子について `gh api graphql` で issue state と
`closedByPullRequestsReferences` を取得して、PR の `reviewDecision` と最新 commit
の CI rollup も含む JSON（`children[].prs` / `summary.all_merged` など）で
出力します。`--format table` を渡すと、正規化した PR 状態（`open`、`draft`、
`review-required`、`approved`、`changes-requested`、`merged`、`closed`）、CI、
差分バー、変更ファイル数、Conventional-Commit 種別、PR リンクを含む人間向けの一覧を出力します。
`--post-dashboard` を渡すと、親 issue に marker 付きコメントを 1 つ upsert し、
各子 PR の sub-issue 番号、PR リンク、PR 状態、CI、差分規模、
Conventional-Commit 種別、TL;DR、`Review effort` score を集約します。dashboard は
GitHub の機械可読データと PR 本文だけから作り、LLM は呼びません。JSON mode では
`--post-dashboard` を併用しない限り PR 差分統計を取得しないため、review/CI 追加
field は同じ per-issue GraphQL lookup から取得します。dmux や live tmux session は不要です。
現在の JSON schema は issue parent 用なので、Projects v2 URL を parent にした
`--status` は拒否します。

`--post-dashboard` は `--status` 系で唯一 GitHub に書き込む option です。コメント
本文の先頭に `<!-- fanout:dashboard parent=N -->` を置き、
paginated GitHub REST comments endpoint で既存 marker コメントを探して、その
コメントだけを更新します。marker コメントが無い場合は
`gh issue comment --body-file -` で新規作成します。

Lifecycle コマンドも `.fanout/state.json` の記録を対象にします。任意の worktree を
filesystem scan で探すことはしません。

- `fanout <parent> --merge <NUM>` は、記録済み branch を
  `git -C <project-root> merge --ff-only <branch>` で取り込みます。fast-forward
  できない場合は報告だけ行い、vim 等の conflict 解決 UI は起動しません。
- `fanout <parent> --close <NUM>` は、記録済み worktree を
  `git worktree remove <path> --force` で削除し、記録済み tmux pane が残っていれば
  kill し、state entry を削除して `git worktree prune` を実行します。
- `fanout <parent> --cleanup` は、issue が `CLOSED`、または closed-by PR に
  `MERGED` がある記録済み子をまとめて `--close` 相当で後始末します。

### ダッシュボード（Web UI）

`fanout dashboard --web` は **読み取り専用**の Web ダッシュボードを起動し、
fanout の **Session**（`.fanout/state.json` に記録されたペインを親 issue 単位で
まとめたもの）をブラウザで常時可視化します。ペインの生存（`tmux list-panes`）・
issue 状態・PR マージ状態（`--status` と同じデータ源を、リポジトリ内の全親について
一度に再利用）をライブ表示します。リポジトリと GitHub の状態は一切変更せず、tmux は
*読み取る*だけです。唯一の意図的な tmux への副作用は、利便性のために起動中の tmux
サーバへ登録する `prefix + D` キーバインドです（下記で無効化可）。

```
fanout dashboard --web [--port N] [--open] [--no-token] [--no-keybind]
```

- **いつでも任意に表示。** ターミナルから起動できるほか、tmux 内で **`prefix + D`**
  を押すだけで開けます。ライブ fan-out 後（およびダッシュボード起動時）に fanout が
  この tmux キーバインドを自動登録するため、どのペインからでも呼び出せます。キーは
  detached な `fanout-dashboard` ウィンドウでサーバを起動するのでキー押下後も生き続け、
  2 回目以降は既存 URL を開き直すだけです。fanout は起動したペインに owner
  project root を記録するため、Codex などの agent TUI で tmux の
  `pane_current_path` が stale でも正しい dashboard を開けます。自動登録は
  `--no-dashboard-keybind`（fan-out）/ `--no-keybind`（dashboard）・設定キー
  `dashboardKeybind`・`FANOUT_DASHBOARD_KEYBIND=0` で無効化できます。
- **Session テーブル + HUD。** 各ペイン行に issue・agent・wave と未解決
  blocker（親 issue グラフ由来）・branch・diff/dirty・CI 状態・tmux 生存と
  現在のペインタイトル・`running` / `done` の agent 実行状態バッジ（ペインの
  起動ラッパーがライブに報告。tmux 不通時は起動時の記録値に fallback するため
  stale になりえます）・PR 状態を表示し、上部の HUD には repo 全体の running /
  blocked 数も並びます。
- **詳細ドロワーとライブ peek。** 行クリックで右側ドロワーが開き、ペインの
  メタ情報・wave/blockers・worktree・CI 付き PR・元プロンプトに加え、ペインの
  直近出力を *peek* 表示します（`GET /api/peek`、読み取り専用の
  `tmux capture-pane`、表示中は 5 秒ごとに更新）。
- **Codex Plan Mode ペインの plan 表示。** `--codex-plan-mode` で起動した
  ペインでは、ドロワーに *plan* セクションが追加され、ペイン出力中の最後の
  完全な `<proposed_plan>` ブロックを表示します（`GET /api/plan`、こちらも
  読み取り専用の `tmux capture-pane`。開いたとき一度だけ取得し、再取得ボタンで
  手動更新）。長い plan は codex TUI の alternate screen からスクロールアウト
  して取得できないことがあり、その場合は未検出の旨を表示します。
- **構造化フィルタ。** フィルタ欄は自由語と
  `state:` / `run:` / `agent:` / `wave:` / `ci:` / `dirty:` / `live:` /
  `issue:` / `task:` / `pr:` の各 term を AND で組み合わせます — 例:
  `agent:claude wave:2 ci:fail run:running`。フィルタ欄の隣のドロップダウンは
  同じ token を書き込み、適用中の term はクリックで外せるチップとして並びます。
- **ライト / ダークテーマ。** docs サイトと揃えた PAPER BREEZE デザインで、
  ヘッダのトグル選択は `localStorage`（`fanout.theme`）に保存されます。既定は
  `prefers-color-scheme` に従います。
- **localhost 限定。** `127.0.0.1` にのみバインドし、GET 専用の endpoint
  （`/api/snapshot`、SSE の `/api/stream`、`/api/peek`、`/api/plan`、
  埋め込み UI）を公開します。
  `--port` は既定 `0`（OS 割り当ての ephemeral port）で、確定した URL を表示します。
  UI からの唯一の外部リクエストは Google Fonts の stylesheet で、`no-referrer`
  ポリシーにより token 付きダッシュボード URL が外部に漏れることはありません。
- **トークン既定 ON。** 起動毎にランダムトークンを生成して URL に埋め込み、`/api/*`
  をゲートします。同一ホストの他ユーザ/プロセスからループバックポート経由で
  issue/PR データを読まれるのを防ぎます。単一ユーザ端末では `--no-token` で外せます。
- **`--open`** は既定ブラウザで URL を開きます。既に起動中のサーバ
  （`.fanout/dashboard.json` に記録）があればそれを再利用し、二重起動しません。
- **グレースフルに縮退。** `gh` 未ログインならバナーを出して state のみ表示し、
  tmux 外でも生存不明として配信を継続します。

全フラグは `fanout dashboard --help` を参照してください。

### 兄弟協調（SQLite による peer messaging）

親 issue を複数ペインに fan-out すると、各ペインは互いを認識できない独立した
agent セッションになります。`--team` と `fanout msg` サブコマンドは、それらに
軽量な opt-in の協調手段を与えます ——「共有スキーマを今から触る」「自分の branch
は `feat/x`、それを base に rebase して」「ブロック中、#42 終わった人いる?」と
いったやり取りを、人間がペイン間を往復して伝える必要なく行えます。

**なぜ存在するのか（Agent Teams との違い）。** Claude Code の Agent Teams は
*1 セッション内のチームメイト*を協調させます。fanout のペインは*別プロセス*
（各々が独自の git worktree で動く `claude`/`codex` セッション）なので、
セッション跨ぎのチャネルが必要です。peer messaging がそのチャネルで、`claude`
ペインでも `codex` ペインでも同じく機能し、共有のモデルコンテキストを必要と
しません。

**何なのか。** parent ごとの SQLite メッセージバスです。同じ親の兄弟は全員が
同じデータベースに到達し、そこには 2 種類のトラフィックが流れます: 全兄弟への
ブロードキャストである共有**ボード**と、1 つの issue 番号宛の **1:1** メッセージ
です。各メッセージは自由記述の `kind` ラベルを持ちます（既定 `note`。固定語彙は
無く、`blocker` / `heads-up` などチームで有用なものを使えます）。データベースは
`/tmp/fanout-<repo>-<parent>.db` に置かれます（`FANOUT_DB_PATH` で上書き可）。

**有効化（`--team`）。** fan-out 実行に `--team` を足します:

```
fanout 123 --team --agent claude
```

これは 2 つのことを行い、いずれも best-effort です（レジストリの失敗が fan-out
を止めることはありません）: 「Coordinating with your sibling panes」節 ——
起動時点の roster と共有 DB パス、いくつかのチェックポイント —— を各子の通常
briefing に付け、バッチ起動後に作成済みペインを親の peer レジストリに seed します。

> [!NOTE]
> Codex Plan Mode の子（`--agent codex --codex-plan-mode`）は最小限の
> Plan Mode briefing を受け取るため、協調節は**付きません**。レジストリへの
> seed は行われ `fanout msg` も通常どおり使えます —— 注入される briefing 節
> だけがスキップされます。

**使い方（`fanout msg`）。** fanout したペイン内なら、`fanout msg` は自分が
どの子か（tmux pane と `.fanout/state.json` から）・どの親に属すかを自動検出
します:

```
fanout msg peers                      # live な兄弟 roster
fanout msg inbox [--mark-read]        # 未読の 1:1 メッセージ + 未読のボード投稿
fanout msg board [--all]              # 共有ボード
fanout msg send --to 42 "auth.go を触る前に feat/login を base に rebase して"
fanout msg post --kind heads-up "go.mod を編集中 — lockfile の編集は控えて"
fanout msg mark-read --all            # inbox を drain しボードカーソルを進める
fanout msg register                   # このペインを roster に（再）登録
fanout msg nudge 42                   # best-effort: agent が running なら peer #42 のペインを ping
```

`nudge` は DB を触らない通知専用 verb です。対象の agent が running でない（ペイン消失 / 状態不明 / done）
ときは何もせず success（no-op）で返します。

verb 共通のオプション: `--json`（機械可読出力）、`--self <N>` と `--parent <ref>`
（ペイン検出を上書き）、`--dry-run`（write・notify verb のみ —— `# would ...` の書き込み
内容を表示し何も触らない。`--json` とは併用不可）。exit code: `0` 成功、`2` 不正な
呼び出し、`4` SQLite バックエンド失敗。

協調は **pull ベース**です: メッセージは DB に永続し、兄弟は自分のチェックポイント
で読みます（`fanout msg` は忙しいペインに割り込みません）。メッセージは短く事実
ベースに保ってください。

> [!WARNING]
> データベースは `/tmp` 配下の**平文** SQLite ファイルです。fanout は `0600`
> （自分のみ）で作成し、group/world 可読や別ユーザ所有のものは開かず拒否します
> が、`/tmp` は共有スクラッチ領域です。**秘密情報・トークン・認証情報をメッセージ
> に載せないでください。** DB と briefing roster は使い捨てです。終わったら
> `/tmp/fanout-<repo>-<parent>.db*` を削除してください。

全サーフェスは `fanout msg --help` を参照してください。

### `--check-update`

`fanout --check-update` は読み取り専用です。`butaosuinu/fanout` の最新 release
tag を取得し、この binary に埋め込まれた version と比較して、更新の有無を表示します。
`fanout check-update` という subcommand 形式でも呼べます。ローカル dev build
（`version == "dev"`、通常の `make build-go` を含む）は `gh` を呼ばず、dev build
向けメッセージを出して exit 0 で終了します。

exit code:

- `0` — 比較完了、または dev build。
- `2` — 現行 version または最新 tag が `MAJOR.MINOR.PATCH`
  （任意の `v` prefix 可）ではなく比較不能。
- `3` — `gh release view -R butaosuinu/fanout` が失敗。

### `update`

`fanout update` は Installation で説明している同じ `install.sh` 経路を呼び、
実行中の release binary を置換します。OS/arch 検出、release download、checksum
検証、tar 展開、Claude/Codex skill 配置は `install.sh` に集約したままにします。

既定では最新 release を解決し、埋め込み version と比較し、`EvalSymlinks` 後の
現在の binary path を表示してから installer を即時実行します。ローカル dev build
（`version == "dev"`、通常の `make build-go` を含む）は置換を拒否します。

Options:

- `--version <tag>` — `FANOUT_VERSION=<tag>` を `install.sh` に渡し、pin した release
  tag を install します。
- `--no-skills` — `install.sh` に `--no-skills` を渡し、binary だけ更新します。

exit code:

- `0` — 更新完了、または既に最新。
- `1` — dev build、`curl`/`wget` 不在、書込不可 binary directory、option 値不足などの
  環境/preflight 失敗。
- `2` — unknown option、想定外の argument、または version string 比較不能。
- `3` — 最新 release lookup 失敗。

### Settings

Go 実装では opinionated な 6 つの挙動（briefing の 5 トグル＋ダッシュボードの
キーバインド）をオン/オフでき、TUI 通知 channel も選択できます。deprecated な
Bash 版 `./fanout` はこの新しい flag / ファイル / env には未対応です。後方互換の
ため、bool の既定値はすべて `true`、通知の既定値は `bell` です。

優先順位は **CLI flag > 環境変数 > リポジトリ設定ファイル > ユーザー設定ファイル >
ビルトイン既定値** です。fanout は git リポジトリルートを解決した後、逆順に
1 回だけ重ねて解決します。リポジトリ設定は `<project_root>/.fanout/config.json`
です。この `project_root` は親リポジトリルートで、子 worktree ではありません。
ユーザー設定は `$XDG_CONFIG_HOME/fanout/config.json`、`XDG_CONFIG_HOME` が無い
場合は `~/.config/fanout/config.json` です。

```json
{
  "autoPullRequest": false,
  "prReviewGate": true,
  "briefingCodeReview": true,
  "agentTeamsHint": false,
  "prVisualization": true,
  "dashboardKeybind": true,
  "notifications": "bell",
  "ntfyURL": "https://ntfy.sh/my-topic",
  "slackWebhookURL": "https://hooks.slack.com/services/..."
}
```

| 挙動 | ファイルキー | env | CLI flag | 既定値 |
|---|---|---|---|---|
| PR 自動作成指示 | `autoPullRequest` | `FANOUT_AUTO_PR` | `--auto-pr` / `--no-auto-pr` | `true` |
| PR レビューゲート通知 | `prReviewGate` | `FANOUT_PR_REVIEW_GATE` | `--pr-review-gate` / `--no-pr-review-gate` | `true` |
| Claude `/code-review` 指示 | `briefingCodeReview` | `FANOUT_BRIEFING_CODE_REVIEW` | `--briefing-code-review` / `--no-briefing-code-review` | `true` |
| Claude Agent Teams ヒント | `agentTeamsHint` | `FANOUT_AGENT_TEAMS_HINT` | `--agent-teams-hint` / `--no-agent-teams-hint` | `true` |
| 構造化 PR 本文とゲート付き Mermaid の briefing 指示 | `prVisualization` | `FANOUT_PR_VISUALIZATION` | `--pr-visualization` / `--no-pr-visualization` | `true` |
| ダッシュボード `prefix + D` tmux キーバインド | `dashboardKeybind` | `FANOUT_DASHBOARD_KEYBIND` | `--dashboard-keybind` / `--no-dashboard-keybind` | `true` |
| TUI 状態遷移通知 | `notifications` | `FANOUT_NOTIFICATIONS` | n/a | `bell` |
| ntfy POST URL | `ntfyURL` | `FANOUT_NTFY_URL` | n/a | 未設定 |
| Slack webhook POST URL | `slackWebhookURL` | `FANOUT_SLACK_WEBHOOK_URL` | n/a | 未設定 |

bool の環境変数は `1/true/yes/on` と `0/false/no/off` を受け付けます（大小文字は無視）。
不正な bool env 値、設定ファイル内の未知キー、JSON type が合わない値は warn して
無視します。将来の設定追加で古い fanout が壊れないようにするためです。

`notifications` は comma または空白区切りの selector です。指定できる値は
`bell`、`tmux`、`ntfy`、`slack`、`none` です。`ntfy` は `ntfyURL`、`slack` は
`slackWebhookURL` が必要です。どちらの HTTP channel も outbound POST のみで、
inbound socket は開きません。repository-controlled な外部送信を避けるため、repo
config で選択できるのは `bell`、`tmux`、`none` だけです。`ntfy`、`slack`、
`ntfyURL`、`slackWebhookURL` は user config または環境変数からだけ有効になります。

`prVisualization=false` は、子 briefing から構造化 PR 本文とゲート付き Mermaid の
指示を外します。この指示は子が PR 本文を書く場合のものなので、`autoPullRequest`
も true のときだけ注入されます。

`prReviewGate=false` だけは、子 Claude Code の hook を強制的に無効化する設定では
ありません。代わりに Claude briefing へ、`/post-work-review` 前に `PreToolUse`
hook が PR 作成を止めた場合は `FANOUT_SKIP_PR_REVIEW=1 gh pr create ...` を
使ってよい、という通知を入れます。

### 例

```bash
# この examples では子ペインに Claude を使う
export FANOUT_AGENT=claude

# #123 のすべての OPEN サブ issue をファンアウト
fanout 123

# git worktree + tmux コマンド列を実行せずにプレビュー
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

# 子ごとの worktree slug stem、ペインタイトル、git branch 名を指定する。
# `--name NUM=<slug>[|<display>[|<branch>]]` の 3 セグメント。最低 1 つ非空であれば
# 残りは空でよい。<slug> に issue 番号 suffix が無い場合、fanout は -<NUM> を
# 付ける。rerun の冪等性は `.fanout/state.json` が担う。3 つ目の <branch> は
# 生成 branch 名を上書きする。
# 同梱の Claude/Codex 連携経由なら issue タイトル/本文から各 hint を会話内で
# 生成して自動で渡す。
fanout 123 --name 4=fix-login-timeout --name 7='update-docs|Docs update'
fanout 123 --name 8='feat-x|Feature X|feat/issue-8-x'   # 3 セグメント全部
fanout 123 --name 9='||release/v2.0'                    # branch のみ上書き

# base branch と生成 branch prefix を上書き
fanout 123 --base-branch release/v2 --branch-prefix fanout/release/

# worktree 作成前の base branch refresh をスキップ
fanout 123 --no-refresh

# 起動元 pane ではなく特定の tmux セッション名を target にする
fanout 123 --session work-repo

# 作成間に 8 秒待つ
fanout 123 --sleep 8

# 子ペインで起動する agent CLI を選ぶ
fanout 123 --agent codex

# 一部の子 issue だけ agent を変え、他はデフォルトを使う
fanout 123 --agent codex --agent 456=claude

# Codex 子ペインを app-server Plan Mode + interactive TUI で開始する
fanout 123 --agent codex --codex-plan-mode

# agent wrapper から issue-less な実装計画を分解して起動する。
# Claude Code は /fanout plan、Codex は $fanout-plan または "fanout plan"
# 依頼を使う。どちらも live 起動前に fanout plan --dry-run で preview する。
/fanout plan /tmp/implementation-plan.md

# 既存 spec から CLI を直接使い、blocked_by 依存は前提 task branch の PR が
# merge されるまで deferred にする
fanout plan /tmp/fanout-plan-launch-plan.json --agent claude --dry-run
fanout plan /tmp/fanout-plan-launch-plan.json --agent claude --unblocked-only
fanout plan /tmp/fanout-plan-launch-plan.json --agent claude --agent api-client=codex
fanout plan launch-plan --status --format table
fanout plan launch-plan --merge base-types
fanout plan launch-plan --cleanup

# この run だけ、子 briefing から PR 自動作成指示を外す
fanout 123 --no-auto-pr

# この shell では Agent Teams ヒントを無効化
export FANOUT_AGENT_TEAMS_HINT=0

# この run で兄弟ペイン間の peer messaging を有効化（briefing roster + peer
# レジストリを per-parent SQLite バス上に）。その後 fanout したペイン内で:
fanout 123 --team --agent claude
fanout msg peers                       # この fan-out に他に誰がいるか
fanout msg post --kind heads-up "go.mod を編集中 — lockfile の編集は控えて"
fanout msg send --to 4 "auth.go を触る前に feat/login を base に rebase して"
fanout msg inbox --mark-read           # 自分宛のメッセージを読み drain する

# .fanout/state.json に記録された子 issue と closed-by PR/review/CI 状態を読む。
# 既定は automation 向け JSON、table は PR/CI/差分確認、任意で親 dashboard コメント。
fanout 123 --status
fanout 123 --status | jq '.summary.all_merged'
fanout 123 --status --format table
fanout 123 --status --post-dashboard

# 記録済み子 branch を parent worktree に fast-forward merge し、不要になった
# child worktree/pane を後始末する
fanout 123 --merge 4
fanout 123 --close 4

# issue が CLOSED または closed-by PR が MERGED の記録済み子をまとめて後始末
fanout 123 --cleanup

# release 済み fanout binary が最新 GitHub Release より古いか確認する。
# dev build は、更新確認が release version 向けであることを表示する。
fanout --check-update

# 親 issue ではなく Projects v2 ボードの OPEN issue をファンアウトする。
# 既定は Status=Todo フィルタ、同一リポジトリのみ。`gh auth refresh -s
# read:project` が必要。詳しいルールは上の「Project モード」節を参照。
fanout https://github.com/users/<owner>/projects/<n>

# 別の Status 列を指定（任意の single-select 値が使える）
fanout https://github.com/orgs/<org>/projects/<n> --project-status "In Progress"

# Status フィルタを無効化して全 item を対象に（Done / Status 未設定も含む）
fanout https://github.com/users/<owner>/projects/<n> --project-status all
```

## エージェントセッション内から呼び出す

fanout は、Claude Code や Codex などのエージェントセッション内から呼び出しても
安全です。作るのは子用の新規 tmux ペインだけなので、呼び出し元のペインは
一切触りません。`--agent` を渡すか `FANOUT_AGENT` を設定してください。issue /
Project の子ごとの上書きには `--agent NUM=name`、`fanout plan` の task ごとの
上書きには `--agent task-id=name` を繰り返し指定します。

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
- **plan fan-out スキル** → `claude/skills/fanout-plan/SKILL.md` が
  `~/.claude/skills/fanout-plan/SKILL.md` にインストールされ、`/fanout plan`
  を支えます。承認済みまたはローカルの実装計画を `fanout plan` spec に変換し、
  dry-run preview を実行して task / wave を要約し、確認後に issue-less task pane
  を起動します。
- **PR watch スラッシュコマンド** → `claude/commands/pr-watch.md` が
  `~/.claude/commands/pr-watch.md` にインストールされ、`/pr-watch [pr-number|pr-url]`
  として呼び出せます。pr-watch スキルを呼び出します。
- **post-work review スキル** → `claude/skills/post-work-review/SKILL.md` が
  `~/.claude/skills/post-work-review/SKILL.md` にインストールされ、ローカルの
  PR review gate を支えます。最終レビュー loop（code-review プラグイン → codex:review
  ループ）を回し、`.claude/hooks/pre-pr-review-gate.sh` が読む reviewed HEAD marker を
  記録します。
- **PR watch スキル** → `claude/skills/pr-watch/SKILL.md` が
  `~/.claude/skills/pr-watch/SKILL.md` にインストールされます。PR 作成後に
  mergeability・失敗 CI・レビューコメントを見張り、安全に直せるものを修正 / push
  します。`/loop /pr-watch` で `ScheduleWakeup` self-pacing しながら回す前提です。

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
- **plan fan-out スキル** → `codex/skills/fanout-plan/SKILL.md` が
  `~/.codex/skills/fanout-plan/SKILL.md` にインストールされます。`$fanout-plan`
  または `fanout plan` の依頼で使います。ローカル spec を作成または選択し、
  `fanout plan ... --dry-run` を preview してから、確認後に live の issue-less
  task fan-out を実行します（確認スキップが明示された場合を除く）。
- **post-work review スキル** → `codex/skills/post-work-review/SKILL.md` が
  `~/.codex/skills/post-work-review/SKILL.md` にインストールされます。Codex に
  コミット前・PR前の最終レビューを依頼したときに使い、明示 scope 付きの
  `codex review` を回し、actionable な指摘を直し、clean になるまで再レビューします。
  reviewed HEAD が clean な場合は Claude の PR gate と同じ marker も記録します。
- **PR watch スキル** → `codex/skills/pr-watch/SKILL.md` が
  `~/.codex/skills/pr-watch/SKILL.md` にインストールされます。PR 作成後に
  mergeability、失敗 CI、レビューコメントを確認し、安全に直せるものを修正し、
  guarded `--force-with-lease` で push して、green / reviewer 待ち / blocked の
  状態を報告します。

上記の CLI 前提条件はそのまま適用されます: TUI は対象リポジトリの worktree から
起動し、一括 pane 作成では tmux 内で実行し、agent 名を明示してください。詳しくは
**前提条件** と **トラブルシューティング** を参照してください。必要に応じて
issue 子ごとの `--agent NUM=name` や plan task ごとの `--agent task-id=name` で
global agent を上書きできます。

## fanout が実際にやること

1. `gh`、`git`、`tmux` がインストールされているかを確認。
2. `git rev-parse --show-toplevel` でリポジトリルートを、`tmux display-message -p
   '#{session_name}'` で現在の tmux セッションを、`$TMUX_PANE`（fallback は
   `#{pane_id}`）で起動元 pane を解決する。
3. per-target の `--agent NUM=name` / `--agent task-id=name`、global `--agent`、
   `FANOUT_AGENT` の順に各子の agent を解決する。live 実行では、選択された
   agent CLI が `PATH` 上にあることも確認する。
4. 2 つのソースの和集合で子を列挙する（いずれもプロジェクトルートから実行）:
   (a) GitHub Sub-issues API
   （`gh api repos/{owner}/{repo}/issues/<N>/sub_issues`）で正式リンクされている
   子、(b) 親本文中の GitHub タスクリスト参照 — `^\s*-\s+\[[ xX]\] ... #NUM`
   にマッチする行の `#NUM` を拾う（同一リポジトリ内のみ。`owner/repo#NUM`
   形式はスキップ）。本文由来の番号は `gh issue view` で本体情報を引く。
   `state == "OPEN"` の子のみを処理する。
5. action-mode 冪等性として `.fanout/state.json` を読み、同じ
   `(parent, issueNum)` が記録済みの child はスキップする。pre-state run や
   中断された launch から残った未記録の `.fanout/worktrees/<slug>` directory も、
   移行用 fallback としてスキップする。同じ issue が別の親に記録済みの場合は
   別親の default worktree を無視し、今回の run が作る slug と一致する worktree が
   既にあるときだけ中断復旧用にスキップする。
6. 各対象 issue について:
   - `/tmp/fanout-<repo>-<NUM>.md` に、issue の本文と短い Requirements チェックリスト
     からなるブリーフィングを書き出す。内容は解決済み settings で出し分ける。
   - base branch を解決する（`gh repo view defaultBranchRef`、`origin/HEAD`、
     `main` の順。`--base-branch` 指定時はそれを使う）。
   - `--no-refresh` が無ければ `git fetch --quiet --no-tags` と fast-forward で
     base branch を fresh 化する。
   - `git worktree add -b <branch> <path> <base>` で `.fanout/worktrees/<slug>/`
     を作成する。
   - `tmux split-window -t <invoking-pane> -d -h -P -F '#{pane_id}' -c <worktree> <launch-command>`
     で子ペインを選択せずに作る（`--session` 指定時は指定 session 名を target
     にする）。起動コマンドは POSIX wrapper 経由で実行し、agent 終了後は
     ユーザーの shell に戻る。
   - ペインタイトルを設定し、`tmux select-layout tiled` を適用する。
   - 次の処理に入る前に `--sleep` 秒（既定 4）だけスリープする。
7. 作成済み / スキップ / 保留 / 失敗の件数サマリを表示する。

## トラブルシューティング

### "fanout must be run inside tmux"

これは一括 pane 作成モード（`fanout <parent-issue|project-url>`）だけの制約です。
対象リポジトリの worktree で tmux セッションを開始または attach してから、一括作成
コマンドを再実行してください。素のシェルから常駐コンソールを開く場合は、引数なしの
`fanout` を実行します。

### "agent is required"

`--agent claude` / `--agent codex` を渡すか、`FANOUT_AGENT` を設定してください。
未知の agent はペイン作成前に失敗し、live 実行では選択された CLI が `PATH` 上に
無い場合も失敗します。混在実行では、`--agent codex --agent 123=claude` のように
global default と上書きを併用するか、選択対象すべてに上書きを指定してください。

### "prepare worktree"

git worktree の準備に失敗しています。内側の git エラーを確認してください。よくある
原因は、dirty な checked-out base branch、local base branch の diverge、既存の
branch 名、stale/missing remote branch です。base を変えるには
`--base-branch <branch>` を使ってください。remote-tracking ref から直接切る場合は
`origin/<branch>` を指定できます。意図的に現在の local base/ref から切る場合にだけ
`--no-refresh` を使ってください。

### "sub-issues fetch failed"

- 未認証: `gh auth status`。
- 親 issue が存在しない: Sub-issues API が HTTP 404 を返し、fanout は exit 1
  する。issue 番号を確認すること。
- リンクされたサブ issue がゼロなのはエラーではない: fanout は
  `no sub-issues on #<parent>` と出して exit 0 する。

### slug や branch 名が意図と違う

既定では `slugify(title)-<issueNum>` と `fanout/<slug>` を使います。特定 issue は
`--name <NUM>=<slug>|<display>|<branch>`、run 全体の branch prefix は
`--branch-prefix <prefix>` で上書きできます。

### `gh pr create` が deny される（"post-work-review が未実施です"）

`PreToolUse(Bash)` hook（`.claude/hooks/pre-pr-review-gate.sh`、コミット済みの
`.claude/settings.json` に登録）が、現在の HEAD が `/post-work-review` を通過する
まで `gh pr create` をブロックします。`/post-work-review` を実行すると最終ステップ
でレビュー済みコミットが記録されるので、その後 `gh pr create` を再実行してください。
一度だけバイパスしたいとき（例: このゲート自体を導入する PR は、放置すると自分自身の
作成を deny してしまう）は、コマンドの先頭に付けます: `FANOUT_SKIP_PR_REVIEW=1 gh pr create ...`。
fanout settings で `prReviewGate=false` になっている場合、子 Claude briefing にも
この bypass 許可が入ります。ただし、コミット済み hook 自体は変更されません。

メモ:
- ゲートは HEAD に固定されます。新しいコミットを積むと再武装されるので、PR の前に
  もう一度レビューしてください。marker は worktree ローカルなので、fanout の並列ペイン
  同士が干渉することはありません。
- 検出はシェルトークナイザ（Python 製のコンパニオンパーサ `pre-pr-review-gate.py`）を
  通します。コマンド語と引用された引数値を区別するので、コミットメッセージに
  `gh pr create` と書いただけでは引っかかりません。`eval` / `xargs` /
  `sh -c "<文字列>"` のような間接実行はすり抜けることがありますが、fanout の通常
  フローでは許容範囲としています。
- `python3` が無い環境では fail-closed になり、PR 作成らしきコマンドを粗い判定で
  deny します。`python3` をインストールするか、`export FANOUT_SKIP_PR_REVIEW=1` して
  ください。
- `make install` は Claude / Codex 配下の同名グローバル `post-work-review` /
  `pr-watch` skill を上書きします。独自に管理しているコピーがある場合は事前に
  バックアップしてください。

## 設計メモ

- **プロンプトは 1 行のみ**。完全な issue 本文は `/tmp/fanout-<repo>-<NUM>.md`
  に保存し、pane 起動 prompt は agent にその briefing を読むよう短く伝えます。
- **状態ストア冪等性**。`.fanout/state.json` には `schemaVersion` と、
  `parent` / `issueNum` / `slug` / `branchName` / `paneId` / `agent` /
  `displayName` / `worktreePath` / `prompt` / `createdAt` を持つ pane 行を
  保存します。書き込みは sibling temp file + rename で atomic に行い、live run
  中は `.fanout/state.json.lock` を保持して plan と起動を直列化するため、並列実行
  でも同じ `(parent, issueNum)` を二重作成しません。state row の無い既存 worktree
  directory は、移行用 fallback として引き続き fanned 済み扱いにします。同じ child
  issue が別の親や Project ですでに記録されている場合は、デフォルトの slug/branch
  生成で issue suffix の前に parent token を足し、2 回目の run が 1 回目の
  worktree と衝突しないようにします。今回の run が作る slug と一致する worktree が
  既にある場合は、中断復旧用にその run を skip します。
- **直接 tmux IPC**。`tmux split-window -t <invoking-pane> -d -P -F '#{pane_id}'`
  が子ペインを選択せずに pane id を同期的に返すため、popup 横取りや
  完了ポーリングは不要です。

## ライセンス

本プロジェクトは MIT License で配布されています。詳細は [LICENSE](LICENSE) を参照してください。
