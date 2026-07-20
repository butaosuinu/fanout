---
title: ドキュメント
description: "最初のインストールから、最後のペインを畳むまで。fanout のすべてをここに。"
---

複数の機能を同時に進めたい開発者のための CLI が **fanout** です。GitHub 親 issue の OPEN な子 issue を子ごとの tmux ペインにファンアウトし、各ペインに専用の git worktree を割り当て、issue ごとの briefing を渡したエージェント CLI(Claude Code や Codex)を起動します。子は独立した worktree とブランチで動くので、同時に編集しても衝突しません。ある子が blocker の解消を待つ間も、別のペインで次の作業を進められます。

[インストール]({{< relref "/docs/installation" >}})では curl 一行でバイナリと skill を入れます。[クイックスタート]({{< relref "/docs/quickstart" >}})は親 issue から並列ペインまで数分で進む入口です。[ワークフロー]({{< relref "/docs/workflow" >}})は issue ツリーを育ててファンアウトし、子を選び、merge と後始末を経て次の wave へ進むループを示します。[モニタリング]({{< relref "/docs/monitoring" >}})では常駐 TUI、`--status`、Web ダッシュボードの 3 つの窓でファンアウトを見守ります。

[CLI リファレンス]({{< relref "/docs/cli" >}})はコマンド形式とフラグ、環境変数、exit code を 1 ページにまとめます。[herdr backend]({{< relref "/docs/herdr-backend" >}})は opt-in・観測専用の runtime backend と tmux との差分を扱います。[設定]({{< relref "/docs/settings" >}})では生成される briefing のスイッチと解決順序を制御します。[エージェント連携]({{< relref "/docs/agents" >}})は同梱 skill と `/fanout` スラッシュコマンド、Codex Plan Mode を扱います。詰まったら[トラブルシューティング]({{< relref "/docs/troubleshooting" >}})で原因と直し方を引き、各リリースの変更点は[変更履歴]({{< relref "/docs/changelog" >}})にあります。
