# 外部エージェント検出の決定記録

[#865](https://github.com/butaosuinu/fanout/issues/865) の検出方針と、[#866](https://github.com/butaosuinu/fanout/issues/866) で採取した範囲を記録する。
2026-10-27 採取。
この PR は文書と fixtures のみで、外部行・installer・状態 reporter は実装しない。
未確認の情報源は v1 では使わず、分類できる生存プロセスだけを `running` として表示する。

## 採取条件

macOS 15.6 arm64 で Claude Code 2.1.311、codex-cli 0.164.0、OpenCode 1.18.34 を使った。
Claude は issue 記載の 2.1.310 と異なる。2.1.310 での互換性は未確認。
Linux の採取元は [procscan fixtures](../internal/infra/procscan/testdata/README.md) に固定する。

空の専用ディレクトリに `HOME`、`XDG_CONFIG_HOME` / `XDG_DATA_HOME` / `XDG_STATE_HOME` / `XDG_CACHE_HOME` / `XDG_RUNTIME_DIR`、`TMPDIR`、`CLAUDE_CONFIG_DIR`、`CODEX_HOME`、`OPENCODE_CONFIG_DIR` を設定した。
環境変数は許可リストから構成し、認証情報を引き継がなかった。
対話起動・exec/run は追加の macOS sandbox で実 HOME のデータ読取、keychain service、隔離先外への書込を拒否した。
導入済み実行体と Node の配布ディレクトリだけは読取対象とした。
Claude / Codex は通信も拒否した。
OpenCode は初回の依存取得待ちを確認後、専用 config 内の公開依存取得だけのため通信を許可した。provider は空リストにし、存在しない `spike/missing` を指定した。

実会話、他 Session の履歴、実ユーザーの設定・hooks・trust・credentials は採取していない。
`ps -a` は実行せず、この作業が起動した `sleep` の PID を `-p` で指定した。
fixtures は今回生成したデータからの投影であり、会話本文を除き、パス・PID・Session ID を置換している。
親 issue の過去の観察と公開仕様は、今回の実測に含めない。

## 検出表

| agent | 今回確認した値 | identity の候補と v1 の制限 | 状態の fallback |
| --- | --- | --- | --- |
| Claude | `--version` は `2.1.311`。空 HOME の対話起動は通信拒否で exit 1。registry / hooks は 0 件 | `sessions/<pid>.json` の sessionId / cwd / kind / entrypoint / tmux は未採取。v1 では使わない | `busy→working` / `idle→idle` は未検証。v1 は生存・分類確認後の `running` のみ |
| Codex | 空 config の `features list` は `hooks stable true`。新規 exec の `session_meta` は `originator=codex_exec`, `source=exec`, `cli_version=0.164.0`。`task_started` を採取 | 対話 TUI の originator、wrapper と native の実プロセス対応、rollout と PID の結合は未確認。cwd と近い時刻だけで Session を選ばない。v1 の rollout identity fallback は使わない | `task_started` は開始の証拠に限る。`task_complete` と許可待ちを未採取のため、v1 の tail による working / idle / blocked 判定は使わない |
| OpenCode | 隔離 global `plugin/probe.js` がロードされ、`process.pid` と cwd を取得。plugin から `fanout --version` が exit 0。新規 `run` が DB に session と user message を作る | plugin の PID は取得可能。DB の directory / time_created と PID の一意な結合は未確認。v1 の DB identity fallback は使わない | エラー経路で `session.status: busy→idle` と `session.idle` を採取。最終 assistant message の completed 有無による判定は未確認なので v1 では使わない |

プロセス分類は独立した候補選択であり、Session identity や操作権限を証明しない。
Codex npm wrapper が native を選んで起動する構造は導入済み `bin/codex.js` で確認したが、プロセスツリーの実測とは区別する。
`comm` と `args` は macOS / Linux で異なるため、`comm` の絶対パスや表示名だけで分類しない。

親 issue が指定する除外候補は Claude の `daemon` / `bg-pty-host` / `bg-spare`、Codex の `app-server` / `exec-server` / `mcp-server` / `daemon` / `login`、OpenCode の `serve` / `web` / `auth` / `upgrade`。
Codex の導入済み `--help` には `app-server` / `exec-server` / `login`、OpenCode の `--help` には `serve` / `web` / `auth` / `upgrade` がある。
ここで列挙した除外は設計上の拒否候補であり、全モードの argv を実測した一覧ではない。
`exec` / `run` は対象候補に含めるが、未知の subcommand を対話 agent とみなさない。
除外語は argv 内の任意の文字列検索にせず、subcommand として解釈する。prompt 内の同名語は除外根拠にしない。

## hooks と非公式ログの測定範囲

| 項目 | 結果と v1 の判断 |
| --- | --- |
| Claude registry の `status`、permission 待ち、`kind` / `entrypoint` | 認証後まで進めていない。全値を v1 では使わない。過去の busy / idle 観察で blocked を推定しない |
| Claude `procStart` と `startedAt` の単位・時差、`tmux` の形 | 未採取。UTC ctime / ms や pane 文字列の規則を v1 の identity 検証には使わない |
| Claude hook stdin と `$PPID` | `SessionStart` / `UserPromptSubmit` / `Notification` / `Stop` / `SessionEnd` を専用 config に置いたが発火なし。`session_id` / `transcript_path` / `cwd` / `notification_type` / `source` と shell `$PPID == agent PID` は未検証。v1 では使わない |
| Claude transcript の非メッセージ行 | transcript 未採取。`permission-mode` を含む行種から状態を導出する処理は v1 では使わない |
| Codex hook 既定値 | 空 `CODEX_HOME` の `features list` で `hooks stable true` を確認。これは hook の trust 済みや発火を意味しない |
| Codex hook event / stdin | 公式文書には SessionStart / SessionEnd / UserPromptSubmit / PreToolUse / PostToolUse / Stop / PermissionRequest がある。今回の実行時 payload は未採取。v1 では使わない |
| Codex `Notification` | 候補キーを設定した exec は設定エラーなしに開始したが、dispatch は未観測。受理と発火を同一視せず、v1 では使わない |
| Codex 新 hook の trust UI | 無入力の隔離 TUI は `Operation not permitted (os error 1)` で終了。ダイアログの形・順序は未確認。trust 保存・回避は行っていない |
| Codex TUI / Desktop originator | 未採取。exec の `codex_exec` を TUI に流用しない。Desktop の過去観察も v1 では使わない |
| Codex rollout の承認待ち不在 | 許可待ちを生成していないため再確認できていない。短い exec tail に無いことを一般的な不在証明にせず、v1 はログから blocked を出さない |
| OpenCode plugin 自動ロード | `${XDG_CONFIG_HOME}/opencode/plugin/*.js` の単数形を 1.18.34 で確認。公開文書の `plugins/` 複数形は今回未試験なので v1 の自動ロード経路には使わない |
| OpenCode plugin からの子プロセス呼出 | `spawnSync` で導入済み `fanout --version` が成功。`__agent-state` は未実装で、書込・20 ms 目標・全 hook の exit 0 は測定していない |
| OpenCode event | `session.created`, `session.updated`, `message.updated`, `message.part.updated`, `session.status`, `session.error`, `session.idle` をエラー経路で採取。正常ターンの状態推移は未確認 |
| OpenCode `permission.asked` / `tool.execute.before` | 未発火。v1 の blocked / working 判定には使わない。公式例の `tool.execute.before` は名前付き hook で、`event` callback に同名 event が来る証拠ではない |
| OpenCode DB 最終 message | エラー終了後は user message だけが残った。assistant 不在から idle と判定できない。正常完了・中断・許可待ち時の completed / finish は未検証で v1 では使わない |

Codex の公式 [Hooks](https://learn.chatgpt.com/docs/hooks) は非管理 hook ごとの trust を要求する。
公開文書にある `PermissionRequest` と stdin の説明は参考仕様であり、この版の実測 fixture の代用にしない。
OpenCode の公開 [Plugins](https://opencode.ai/docs/plugins/) も、採取した版の値と分けて参照する。

## 状態の優先順位と結合

後続実装の優先順位は、新鮮で identity を検証できるサイドカー、検証済みのログ / registry、生存のみの `running` とする。
未測定の情報源は、この順位に参加させない。
サイドカーは PID 生存、分類 agent の一致、`updatedAt >= procStart` を検証し、PID 再利用時に古い報告を使わない。
時刻・プロセス開始値の比較を検証できない環境や、複数候補が残る結合は採用しない。
欠落・未知値・parse 失敗はクラッシュさせず `running` と `Degraded.Reason` に落とす。

`blocked` は確認済みの許可待ち報告だけが出せる。
今回その報告を採取していないため、3 agent とも v1 で blocked を推定しない。
外部行は live-only とし、プロセス消失時に行を消す。`done` と `plan` は作らない。
`working` / `idle` も操作を許可する根拠にはしない。

外部行は `Parent="@external"`, `Kind="external"`, `SourceKey="pid:<n>"` とする設計を維持する。
fanout 所有プロセスは、保存済み runtime identity と祖先プロセスの照合で除外する。
同名 pane、cwd、一時的な pane 不在では ownership を決めない。
branch / PR は同じ repository または linked worktree の cwd に限って読み取る。
他 repo は identity と状態だけを扱う。

## hooks の既定インストール方針

[#865](https://github.com/butaosuinu/fanout/issues/865) の `fanout integration install|uninstall|status [--agent claude|codex|opencode]` を採用する。
通常の install / link に統合し、`FANOUT_NO_AGENT_HOOKS=1` で opt-out、`FANOUT_EXTERNAL_AGENTS=0` で外部行を停止できる設計とする。
この方針は製品 installer の契約であり、今回の spike が実 HOME に hook を導入する許可ではない。
未検証の event と PID 結合は v1 のインストール内容に含めない。
Codex の trust は利用者が明示的に行い、未承認なら状態を劣化させる。trust を自動作成・複製しない。

reporter は `fanout __agent-state` を呼び、`XDG_STATE_HOME` 配下のサイドカーへ書く設計を維持する。
Claude / Codex の shell `$PPID` は今回未検証なので、そのまま agent PID として採用しない。
OpenCode で確認した `process.pid` と、子プロセスの PID を混同しない。
常に exit 0、stdout 無し、20 ms は後続実装の目標であり、今回の達成値ではない。

user 設定の変更は symlink の実体解決、初回 `.bak`、JSON 検証、fanout 所有項目の冪等な追加・撤去を前提とする。
不正 JSON では書かず snippet を示し、uninstall は他の設定を残す。
既存の起動時 `--settings` 注入は維持する。
実ユーザーの trust・資格情報の複製・実環境への導入が必要な追加測定は、操作・対象・理由を示して確認を求める。

## 既存判断と非目標

[herdr 競合分析](competitive-herdr.ja.md) の user hooks 不採用を、上の opt-out 既定方針へ改訂する。
`capture-pane` による状態推定の不採用は維持する。プロセス identity と Session metadata は pane の画面内容ではない。
[Session messaging](session-messaging-push.ja.md) の parent 境界はメッセージ transport の境界であり、表示用サイドカーに送信権限を与えない。
[architecture](architecture.ja.md) の peek 境界は後続の snapshot 実装で、保存済み行と検証済み外部行を対象として改訂する。今回の文書だけで現行 API の対象を広げない。

TUI 外部行、外部行への merge / focus / kill、pane なし peek、非所有 herdr session の peek、外部 plan、state.json への自動 adopt、外部 `fanout wait`、注入 hooks と user hooks の統合、repo 別グループ化、Windows は対象外。
後続 issue は coordinator がこの成果を確認し、人間が PR を merge するまで起動しない。
