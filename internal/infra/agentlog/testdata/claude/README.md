# Claude Code startup observation

採取日: 2026-10-27。macOS 15.6 arm64、Claude Code **2.1.311**。
Issue 記載の 2.1.310 は採取していない。
`startup-observation.json` は新規起動の観測結果であり、registry や hook payload の代用品ではない。

空の専用 HOME / XDG roots / `CLAUDE_CONFIG_DIR` / TMPDIR と専用 cwd で、`claude --setting-sources user --no-chrome` を PTY 起動した。
追加 sandbox は実 HOME データ、keychain service、通信、隔離先外の書込を拒否した。
専用 settings.json には metadata だけを記録する SessionStart / UserPromptSubmit / Notification / Stop / SessionEnd hooks を置いた。
キー入力、trust 承認、認証情報の複製は行っていない。
通信失敗で exit 1、registry と hook 出力は 0 件だった。

registry の status / kind / entrypoint / procStart / startedAt / tmux、hook stdin / shell PPID、transcript 非メッセージ行は未測定で、v1 では使わない。
実会話、実ユーザー HOME の registry / transcript は読んでいない。
未知の状態を `running` へ劣化させる受入境界の記録として使い、成功系パーサの golden として使わない。
