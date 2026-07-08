# セッション間メッセージング改善案

`fanout msg` は parent ごとの SQLite バスで兄弟ペインの協調を扱う。
現状でも `peers`、`inbox`、`board`、`send`、`post`、`mark-read`、`register`、`nudge` があり、メッセージ本文は DB に残る。
改善対象は「読む順序」と「伝えたことに気付く手掛かり」であり、別のメッセンジャーを fanout に載せることではない。

## 現状の弱点

兄弟ペインは、作業の節目で `fanout msg inbox` を読む前提になっている。
そのため、長い作業のあとに参加したペインや、途中で context を失ったペインが parent 全体の会話を読み直す導線が弱い。

送信と `nudge` も分かれている。
本文は DB に永続化されるが、相手へ「読むべき inbox がある」と知らせたい場合は `send` または `post` のあとに別の操作が必要になる。
この二段階は安全だが、エージェントが省略しやすい。

## 方針

**履歴読み取り**を追加する。
`fanout msg history` は parent に属するメッセージを時系列で返し、既読状態を変えない。
既定では直近の一定件数だけを返し、`--limit` と `--before-id` でページングできる形にする。
issue や Project の run では issue 番号を、`fanout plan --team` では task ID を表示できるようにする。

**送信時の通知補助**を追加する。
`send` と `post` は本文を DB に保存したあと、任意フラグで既存の `nudge` 相当の短い hint を送れるようにする。
hint は本文を tmux に流さず、受信側に inbox 確認を促すだけにする。
保存が成功したあとに hint が失敗しても、メッセージ送信自体は成功として扱い、human-readable 出力と JSON に警告を載せる。

**briefing の利用リズム**を明確にする。
各子は開始直後、共有ファイルを触る前、PR を開く前に `inbox` を確認する。
途中参加、長時間停止、context recovery の場面では `history` を使う。
共有インターフェースを変更したペインは `post` で短く知らせ、特定の兄弟だけが影響を受ける場合は `send` を使う。

## 非目標

- daemon、socket、broker、常時 monitor loop は足さない。
- fanout の parent 境界を越える global team registry は作らない。
- メッセージ本文を tmux の入力として流さない。
- `blocked` 状態のペインへ Enter を送らない。
- メッセージ本文を読んで自動判断するループは作らない。
- 外部ツールの固有名、command vocabulary、installer 構造を issue やユーザー向け文書へ持ち込まない。

## 実装単位

1. `msgstore` に parent-scoped な履歴取得を追加する。
2. `cmd/fanout msg` に `history` の parse と validation を追加する。
3. `peermsg` に履歴出力を追加し、JSON schema を固定する。
4. `send` と `post` の保存後に、任意の hint を best-effort で送る経路を追加する。
5. briefing、skills、CLI docs の説明を更新する。
6. Go unit test、bats、golden で read-only history、ページング、hint 失敗時の成功扱い、plan task ID 表示を固定する。

## 受け入れ条件

- `history` は parent の DB だけを読み、既読状態を変えない。
- `history --limit N --before-id M` は安定した順序で返る。
- `send` と `post` の通知補助は、本文保存後にだけ動く。
- 通知補助は `blocked`、`done`、状態不明、消えた pane を no-op として扱う。
- JSON 出力はメッセージ本体と通知結果を分けて返す。
- `fanout plan --team` では task ID を使った表示と指定が壊れない。
- user-facing docs は fanout の既存用語だけで説明する。

## レビュー上の注意

実装で触る中心は `cmd/fanout/msg.go`、`internal/app/peermsg`、`internal/infra/msgstore` になる。
briefing を変える場合は `internal/app/briefing` が class H なので、人間レビューを必須にする。
`nudge` の gating を広げる変更は許可待ち prompt を誤操作するリスクがあるため、既存の agent state 条件を狭めずに使う。
