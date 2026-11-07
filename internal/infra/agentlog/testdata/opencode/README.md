# OpenCode isolated run fixtures

採取日: 2026-10-27。macOS 15.6 arm64、OpenCode **1.18.34**。
対象は空の専用 HOME / XDG roots / OPENCODE_CONFIG_DIR / TMPDIR に生成した DB と plugin 出力だけ。
`debug paths` で home / data / bin / log / repos / cache / config / state / tmp がすべて隔離先を指すことを確認した。
実 HOME データ・keychain service・隔離先外書込は追加 sandbox で拒否した。

`${XDG_CONFIG_HOME}/opencode/plugin/probe.js` の単数形を使った。
plugin は初期化時に `process.pid` / directory を記録し、Node `spawnSync` で導入済み `fanout --version` を呼び出した。exit 0、出力 `fanout dev (none)` を確認した。
`event` callback は type / propertyKeys と、info の id / sessionID / role / time、status だけを JSONL に記録した。
これは raw event 全体ではない。message content / part / error 本文を保存していない。

`run --model spike/missing --title isolated-spike --format json` に新規の無害な入力を渡した。
config は `enabled_providers: []`, `autoupdate: false`。
環境で MODELS_FETCH / DEFAULT_PLUGINS / AUTOUPDATE / CLAUDE_CODE / EXTERNAL_SKILLS / LSP_DOWNLOAD / SHARE を `OPENCODE_DISABLE_<name>=1` とした。
通信拒否時は依存取得待ちで 20 秒後に終了し、session / message は 0 行だった。
その終了を確認後、隔離 config 内の公開依存取得を許して新規 run を実行した。資格情報は渡していない。
存在しないモデルで exit 1、session と user message が各 1 行残った。

| ファイル | 採取内容と加工 |
| --- | --- |
| `schema.sql` | 空 HOME で生成した DB の sqlite_master から session / message / permission の CREATE TABLE を抽出。型・制約を保持。他テーブル・index・データを含まない |
| `run-db.json` | session は id / directory / version / agent / model / time_created / time_updated、message は id / session_id / time_created / time_updated / data を投影。data は元と同じ JSON 文字列型で role / time / agent / model を保持 |
| `plugin-events.jsonl` | 上記 callback の投影と初期化の観測値。PID は 42001、cwd は `/fixture/work`、Session / message ID は `ses_fixture001` / `msg_fixture001` に置換 |

schema.sql 単体は DB 全体の復元用ではない。外部キー参照先の project / workspace は含まない。
user message だけでも busy→idle と session.idle が発火したため、最後の assistant が無い状態を completed の有無では判定できない。
正常な assistant message、permission.asked、tool.execute.before、正常完了・中断時の状態、PID と DB の一意な結合は未測定で v1 では使わない。
`__agent-state` は未実装。plugin からの version 呼出を reporter の実行時間や状態書込の検証として扱わない。
