---
title: ドキュメント
description: "最初のインストールから、最後のペインを畳むまで。fanout のすべてをここに。"
---

fanout は、GitHub 親 issue の OPEN なサブ issue を子ごとに tmux ペインへ扇状に展開します。各ペインは専用の git worktree を持ち、issue ごとの briefing を渡されたエージェント CLI が起動します。このドキュメントでは、インストール、最初のファンアウト、wave 駆動のワークフロー、モニタリング、全フラグ、設定、エージェント連携、トラブルシューティングまでを扱います。
