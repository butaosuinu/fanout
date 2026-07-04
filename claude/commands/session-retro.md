---
description: 過去の Claude Code セッションのツールエラー・CI 失敗・レビュー指摘をマイニングし、前回スナップショットとの差分を報告して再発防止策を提案する。
---

Invoke the `session-retro` skill. See the `session-retro` skill
(`~/.claude/skills/session-retro/SKILL.md`) for the mining recipes, snapshot
schema, and guardrails.

- 収集は read-only。書くのはスナップショット (`.fanout/retro/session-<date>.json`
  か、ignore されない repo では `${CLAUDE_CONFIG_DIR:-$HOME/.claude}/fanout-retro/<repo-slug>/`) のみ。
- 改善は提案止まり。repo ファイルへの適用はユーザー承認後にブランチ + PR、
  memory feedback だけは承認後に直接追記できる。
- briefing テンプレ・settings の自動書き換えと GitHub への書き込みはしない。
