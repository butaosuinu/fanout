# Codex isolated exec fixture

採取日: 2026-10-27。macOS 15.6 arm64、codex-cli **0.164.0**、Node **24.22.1**。
`cli-observations.json` は CLI の観測結果、`exec-rollout.jsonl` は今回生成した rollout の metadata 投影。

空の専用 HOME / XDG roots / CODEX_HOME / TMPDIR と専用 cwd で採取した。
空 config の `codex features list` は `hooks stable true` だった。
npm wrapper のパス解決が追加 sandbox に拒否されたため、導入済み native binary で `exec --skip-git-repo-check -c 'cli_auth_credentials_store="file"' --json` と無害な新規入力を実行した。
追加 sandbox は実 HOME データ、keychain service、通信、隔離先外の書込を拒否した。
資格情報は渡していない。通信失敗の再接続中に 15 秒で終了し、同じ隔離 CODEX_HOME の新規ファイルだけを読んだ。

rollout は session_meta と task_started のみを残した。
session_meta は id / timestamp / cwd / originator / cli_version / source のみを保持し、全 response_item、turn_context、world_state、他の event_msg を除いた。
cwd は `/fixture/work`、Session / turn ID は固定 UUID に置換した。時刻と残した状態フィールドは実測値。
投影に無い event は「実行時に無かった」ことを意味しない。
会話本文・システム指示・tool 定義・ユーザー名・認証情報を含まない。

Notification キーだけを置いた別の新規 exec は設定エラーなしに始まったが、dispatch は未観測。
SessionStart / UserPromptSubmit / PreToolUse / PostToolUse / PermissionRequest / Stop / SessionEnd を置いた TUI は無入力のまま OS error 1 で終了した。
trust は承認していない。設定エラーの不在を event 対応の証明にしない。
TUI / Desktop originator、hook stdin、trust UI、正常完了・中断・許可待ち tail、PID と rollout の結合は未確認で v1 では使わない。
