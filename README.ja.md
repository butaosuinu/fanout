# fanout

[English](README.md) | [日本語](README.ja.md)

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

GitHub の親 issue に紐づく OPEN のサブ issue を、子ごとに 1 つの tmux ペインへ
ファンアウトします。各ペインは独立した git worktree を持ち、issue ごとの
ブリーフィングファイルを参照するプロンプトでエージェント CLI が起動します。

## 常駐 TUI コンソール

引数なしの `fanout`、または `fanout tui` で常駐コンソールを起動します。素の
シェルから起動した場合は、現在のリポジトリ用の deterministic な fanout 管理
tmux session を作成または attach し、その session 内でコンソールを開始します。
tmux 内から起動した場合は、現在の pane をそのままコンソール画面にします。

コンソールは `<git-root>/.fanout/state.json` を読み、記録済み pane ID が tmux 上に
まだ存在するかを確認し、`fanout <parent> --status` と同じ GitHub CLI 経路で
issue / closed-by PR 状態を定期更新します。`q` でコンソールを離脱できますが、
tmux session と子 pane は残ります。

## 直接 tmux ランタイム

fanout は dmux を経由せずに子セッションを作ります。`git rev-parse --show-toplevel`
でリポジトリルートを解決し、選択された base branch を fresh 化し、
`.fanout/worktrees/<slug>/` を作成し、起動元 tmux pane を `tmux split-window`
で分割し、選択された agent CLI を 1 行 briefing prompt 付きで起動します。
作成した pane は `.fanout/state.json` に `(parent, issueNum)` キーで記録するため、
同じ親での再実行では記録済みの子を重複作成しません。

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
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | FANOUT_VERSION=v0.1.0 sh
```

配置パス:

- `$BIN_DIR/fanout`（既定は `~/.local/bin/fanout`）
- `$CLAUDE_DIR/commands/fanout.md`（既定は `~/.claude/commands/fanout.md`）
- `$CLAUDE_DIR/skills/fanout/`（既定は `~/.claude/skills/fanout/`）
- `$CLAUDE_DIR/skills/fanout-issues/`（既定は `~/.claude/skills/fanout-issues/`）
- `$CODEX_DIR/skills/fanout/`（既定は `~/.codex/skills/fanout/`）
- `$CODEX_DIR/skills/fanout-issues/`（既定は `~/.codex/skills/fanout-issues/`）

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
workflow は macOS 上で Go 1.23 の darwin バイナリをビルドするため、Go linker
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

チェックアウトからのビルドには **Go ツールチェイン**（Go 1.23+）が必要です。
`make install`・`make link`・`make build-go` はいずれも `go build ./cmd/fanout`
を実行します。上記の curl インストールは prebuilt バイナリを配置するので Go は
不要です。

## 開発

```bash
make test           # Go ユニットテスト + Tier 1 + Tier 2 黒箱テスト (bats-core 必須)
make test-tier1     # フラグ / prereq テストのみ
make test-tier2     # --dry-run ゴールデン出力テスト (fixture 駆動)
make lint           # go vet + gofmt + テスト用 shim の shellcheck
make build-go       # Go CLI を ./fanout-go としてビルド
```

bats: macOS は `brew install bats-core`、Debian/Ubuntu は `apt install bats`。
黒箱テストの各 Tier は `./fanout-go` をビルドし `FANOUT_BIN` 経由で実行します。
Tier 1 は CLI サーフェス (エラーメッセージ + exit code)、Tier 2 は `--dry-run`
の計画出力を `tests/fixtures/` 配下のシナリオ fixture に対して凍結します。
`--dry-run` 出力を意図的に変更した場合は
`FANOUT_GOLDEN_UPDATE=1 make test-tier2` で golden を再生成してください。
Tier 3 (live tmux E2E) は手動運用のままです。

## 前提条件

- 既定の fanout 作成フローでは `gh` CLI、`jq`、`git`、`tmux`、`gh-sub-issue`
  拡張（`gh extension install yahsan2/gh-sub-issue`）が必要です。`--status` と
  `--cleanup` は `gh`/`jq`/`git`、`--merge` と `--close` は `git` を使います
  （`--close`/`--cleanup` の tmux pane kill は pane が既に無い場合 stale として
  扱います）。fanout は必要な依存を起動時にチェックし、失敗時には
  インストールのヒントを表示します。子 issue は
  Sub-issues API 経由でも、親本文のタスクリスト（`- [ ] #NUM ...`）経由でも、
  あるいは両方で宣言されていても構いません。fanout は両ソースの和集合を取ります。
- **Project モード時のみ**: Project items を取得する GraphQL クエリのため、
  `gh` CLI に `read:project` スコープが必要です。`gh auth refresh -s read:project`
  で付与してください。issue モード（`fanout <N>`）では不要です。
- fanout は tmux セッション内から実行してください。子ペインは dmux 経由ではなく
  `tmux split-window` で直接作成し、`--session` 未指定時は起動元 pane を target
  にします。
- TUI モード（`fanout` または `fanout tui`）は素のシェルから起動できます。
  現在のリポジトリ用の fanout 管理 tmux session を作成または attach してから
  コンソールを開始します。tmux 内から起動した場合は現在の session / pane を使います。
- **エージェント名が解決できること**: `--agent claude` / `--agent codex` を渡すか、
  `FANOUT_AGENT` を設定してください。未知の agent はペイン作成前に失敗し、実行時
  には agent CLI がインストール済みかも確認します。
- 子 worktree は `.fanout/worktrees/<slug>/` に作成されます。分岐前に
  `git fetch --quiet --no-tags` と fast-forward で base branch を fresh 化します。
  base を変える場合は `--base-branch <branch>` を使います（bare な local branch 名と
  `origin/<branch>` に対応）。refresh を飛ばす場合は `--no-refresh` を使ってください。
  live 実行時には対象 repo の local `.git/info/exclude` に `.fanout/worktrees/` を
  追記し、生成 worktree が `git status` を汚さないようにします。

## 使い方

```
fanout [tui] # 常駐 tmux コンソールを起動
fanout <parent-issue|project-url>
       [--agent <name>] [--limit <N>] [--only <list>] [--skip <list>]
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
fanout <parent-issue> --status [--format json|table] [--post-dashboard]
                                      # 状態を読み、任意で dashboard を投稿
fanout <parent-issue> --merge <NUM> # 記録済み子 branch を ff-only merge
fanout <parent-issue> --close <NUM> # 記録済み子 worktree/pane を後始末
fanout <parent-issue> --cleanup     # merge/close 済みの記録済み子を後始末
fanout --check-update               # この binary と最新 release を比較
fanout update                       # install.sh 経由で binary + integrations を置換
fanout --help
```

第1引数は GitHub issue 番号（Sub-issues + タスクリストモード）または
Projects v2 URL（Project モード、上記参照）のいずれか。`--project-status`
は Project モードでのみ意味を持ち、issue モードでは無視されます。
`--popup-timeout` は旧ランタイム互換の deprecated flag で、direct tmux path
では受け付けるだけで無視されます。

### Codex Plan Mode

`--codex-plan-mode` は `--agent codex` 専用の opt-in 起動モードです。通常の
positional `codex "<prompt>"` ではなく、子ごとに Codex app-server を起動し、その
thread を collaboration mode `plan` で作成し、fanout prompt を app-server 経由で
initial turn として開始してから、その remote session に interactive Codex TUI を
attach します。子 briefing も Plan Mode
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
`closedByPullRequestsReferences` を取得して、既定では既存の JSON schema
（`children[].prs` / `summary.all_merged` など）で出力します。`--format table`
を渡すと、PR の差分バー、変更ファイル数、Conventional-Commit 種別、PR リンクを
含む人間向けの一覧を出力します。`--post-dashboard` を渡すと、親 issue に
marker 付きコメントを 1 つ upsert し、各子 PR の sub-issue 番号、PR リンク、
差分規模、Conventional-Commit 種別、TL;DR、`Review effort` score を集約します。
dashboard は GitHub の機械可読データと PR 本文だけから作り、LLM は呼びません。
JSON mode では `--post-dashboard` を併用しない限り PR 差分統計を取得しないため、
既定の schema と API call 数は変わりません。dmux や live tmux session は不要です。
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

Go 実装では、fanout が briefing に入れる opinionated な 5 つの挙動スイッチを
解決できます。deprecated な Bash 版 `./fanout` はこの新しい flag / ファイル /
env には未対応です。後方互換のため、既定値はすべて `true` です。

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
  "prVisualization": true
}
```

| 挙動 | ファイルキー | env | CLI flag | 既定値 |
|---|---|---|---|---|
| PR 自動作成指示 | `autoPullRequest` | `FANOUT_AUTO_PR` | `--auto-pr` / `--no-auto-pr` | `true` |
| PR レビューゲート通知 | `prReviewGate` | `FANOUT_PR_REVIEW_GATE` | `--pr-review-gate` / `--no-pr-review-gate` | `true` |
| Claude `/code-review` 指示 | `briefingCodeReview` | `FANOUT_BRIEFING_CODE_REVIEW` | `--briefing-code-review` / `--no-briefing-code-review` | `true` |
| Claude Agent Teams ヒント | `agentTeamsHint` | `FANOUT_AGENT_TEAMS_HINT` | `--agent-teams-hint` / `--no-agent-teams-hint` | `true` |
| 構造化 PR 本文とゲート付き Mermaid の briefing 指示 | `prVisualization` | `FANOUT_PR_VISUALIZATION` | `--pr-visualization` / `--no-pr-visualization` | `true` |

環境変数は `1/true/yes/on` と `0/false/no/off` を受け付けます（大小文字は無視）。
不正な env 値、設定ファイル内の未知キー、bool 以外の値は warn して無視します。
将来の設定追加で古い fanout が壊れないようにするためです。

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

# Codex 子ペインを app-server Plan Mode + interactive TUI で開始する
fanout 123 --agent codex --codex-plan-mode

# この run だけ、子 briefing から PR 自動作成指示を外す
fanout 123 --no-auto-pr

# この shell では Agent Teams ヒントを無効化
export FANOUT_AGENT_TEAMS_HINT=0

# .fanout/state.json に記録された子 issue と closed-by PR の merge 状態を読む。
# 既定は automation 向け JSON、table は PR 差分統計、任意で親 dashboard コメント。
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
一切触りません。`--agent` を渡すか `FANOUT_AGENT` を設定してください。

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

上記の CLI 前提条件はそのまま適用されます: tmux 内で実行すること、対象リポジトリの
worktree から実行すること、agent 名を明示すること。詳しくは **前提条件** と
**トラブルシューティング** を参照してください。

## fanout が実際にやること

1. `gh`、`jq`、`git`、`tmux`、`gh-sub-issue` がインストールされているかを確認。
2. `git rev-parse --show-toplevel` でリポジトリルートを、`tmux display-message -p
   '#{session_name}'` で現在の tmux セッションを、`$TMUX_PANE`（fallback は
   `#{pane_id}`）で起動元 pane を解決する。
3. `--agent` または `FANOUT_AGENT` から agent を解決する。live 実行では、その
   agent CLI が `PATH` 上にあることも確認する。
4. 2 つのソースの和集合で子を列挙する（いずれもプロジェクトルートから実行）:
   (a) `gh sub-issue list <parent>` で Sub-issues API に正式リンクされている
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

対象リポジトリの worktree で tmux セッションを開始または attach してから再実行して
ください。

### "agent is required"

`--agent claude` / `--agent codex` を渡すか、`FANOUT_AGENT` を設定してください。
未知の agent はペイン作成前に失敗し、live 実行では選択された CLI が `PATH` 上に
無い場合も失敗します。

### "prepare worktree"

git worktree の準備に失敗しています。内側の git エラーを確認してください。よくある
原因は、dirty な checked-out base branch、local base branch の diverge、既存の
branch 名、stale/missing remote branch です。base を変えるには
`--base-branch <branch>` を使ってください。remote-tracking ref から直接切る場合は
`origin/<branch>` を指定できます。意図的に現在の local base/ref から切る場合にだけ
`--no-refresh` を使ってください。

### "gh sub-issue list failed"

- `gh-sub-issue` 拡張が無い: `gh extension install yahsan2/gh-sub-issue`。
- 未認証: `gh auth status`。
- 親 issue が存在しない、または拡張経由で紐づけられたサブ issue が無い:
  fanout は `no sub-issues on #<parent>` と出して exit 0 する。

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
- 検出はコマンド文字列に対する正規表現ベースのベストエフォートです。`eval` / `xargs` /
  `sh -c "<文字列>"` のような間接実行や、コミットメッセージ・PR コメントの本文に
  シェル演算子と一緒に `gh pr create` という文字列を書いた場合などは取りこぼし／過検知
  し得ます。その場合は `FANOUT_SKIP_PR_REVIEW=1` で回避してください。
- jq が無い環境では fail-closed（PR 作成らしきコマンドを deny）になります。`jq` を
  インストールするか `export FANOUT_SKIP_PR_REVIEW=1` してください。
- `make install` は同名のグローバル `post-work-review` skill を上書きします。独自に
  管理しているコピーがある場合は事前にバックアップしてください。

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
