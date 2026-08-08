---
title: ドキュメント
description: "最初のインストールから、最後のペインを畳むまで。fanout のすべてをここに。"
---

複数の機能を同時に進めたい開発者のための CLI が **fanout** です。
GitHub 親 issue の OPEN な子 issue を子ごとの tmux ペインにファンアウトし、各ペインに専用の git worktree を割り当て、issue ごとの briefing を渡したエージェント CLI を起動します。
子は独立した worktree とブランチで動くので、同時に編集しても衝突しません。
ある子が blocker の解消を待つ間も、別のペインで次の作業を進められます。

子ペインを担当できるエージェントは 3 つです。

| エージェント | `--agent` | 一言で |
|---|---|---|
| Claude Code | `claude` | skill と `/fanout` を同梱。`--team` のメッセージを届いた瞬間に受け取る |
| Codex CLI | `codex` | skill を同梱。Plan Mode は app-server の plan TUI で動く |
| OpenCode | `opencode` | skill 同梱なし。`AGENTS.md` をネイティブに読む |

3 つの違いは[エージェント連携]({{< relref "/docs/agent-integrations" >}})の能力マトリクスで一覧できます。

[インストール]({{< relref "/docs/installation" >}})では curl 一行でバイナリと skill を入れます。
[クイックスタート]({{< relref "/docs/quickstart" >}})は親 issue から並列ペインまで数分で進む入口です。
[ワークフロー]({{< relref "/docs/workflow" >}})は issue ツリーを育ててファンアウトし、子を選び、merge と後始末を経て次の wave へ進むループを示します。
[モニタリング]({{< relref "/docs/monitoring" >}})では常駐 TUI、`--status`、Web ダッシュボードの 3 つの窓でファンアウトを見守ります。
起動まで任せたくなったら [watcher]({{< relref "/docs/watcher" >}}) の出番です — TUI コンソールを開いている間、`fanout:auto` ラベルの issue がひとりでにセッションになります。

[CLI リファレンス]({{< relref "/docs/cli" >}})はコマンド形式とフラグ、環境変数、exit code を 1 ページにまとめます。
[設定]({{< relref "/docs/settings" >}})では生成される briefing のスイッチと解決順序を制御します。
[エージェント連携]({{< relref "/docs/agent-integrations" >}})は同梱 skill と `/fanout` スラッシュコマンド、agent ごとの Plan Mode の挙動を扱います。
[herdr backend]({{< relref "/docs/herdr-backend" >}})は opt-in の owned runtime と tmux の差分を扱います。
詰まったら[トラブルシューティング]({{< relref "/docs/troubleshooting" >}})で原因と直し方を引き、各リリースの変更点は[変更履歴]({{< relref "/docs/changelog" >}})にあります。
