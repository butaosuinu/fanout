# 子 Session のモデル / effort 選択の決定記録(2026-09)

ステータス: 採択(accepted)。作成: 2026-09-16。ADR 形式の決定記録で、実装は
epic #368 の子 issue(#362〜#367、#790〜#792)で行う。マージまでは #368 本文の
「設計」節と本書が同内容。前提は [roadmap](roadmap.ja.md) の二層構造 — CLI は
決定論(LLM を呼ばない)、モデル選択の判断は skill 層と人間に置く。

## 背景

fanout は子ペインごとに agent CLI(claude / codex / opencode)を選べるが、
モデルと reasoning effort は各 CLI のユーザー設定任せで、全子が同じモデル・
同じ effort で走る。狙いはタスク難易度で使い分けてトークン消費と quota を
下げること。想定する配置は次の 4 行。

- オーケストレーター & 計画役: `claude:fable:xhigh`
- 子 Session フロントエンド: `claude:opus:xhigh`
- 子 Session 低難度バックエンド: `codex:gpt-6-astra:medium`
- 子 Session 高難易度タスク: `codex:gpt-6-astra:xhigh`

モデルルーティングで 40〜60% のコスト削減報告がある一方、LLM の自己ルーティングは
信頼性が低い(自己コスト予測の相関 ≤0.39)。fanout の CLI-no-LLM 鉄則に合わせ、
「skill が推奨 → 人間が承認 → CLI は指定どおり起動」の分業にする。

### 現状コードの事実

- `--agent` は issue lane(`internal/app/cliflags` の `parseAgentArg`、`NUM=name`)と
  plan lane(`cmd/fanout/plancmd.go` の `parsePlanAgentArg`、`task-id=name`)の 2 パーサ。
  担い手は `cliflags.Config.Agent` + `AgentOverrides`、解決は `EffectiveAgent(target)`。
- 起動 argv は `internal/core/agent` の `registry` と `launchArgsForBackend`
  (`LaunchArgs + BackendLaunchArgs + ModeArgs`)が組む。model / effort の口はない。
  唯一の外部注入点は `BuildResolvedLaunchSpecWithBackendArgs`(Herdr の telemetry 用)。
- claude の `--settings`(hook JSON)は argv 先頭固定で、`panelaunch/request.go` と
  `core/telemetry` が `args[0] == "--settings"` を前提に起動を束縛する。
- tmux 子ペインに env を渡す経路はなく、`PATH=` / `FANOUT_BIN=` のインライン前置のみ。
  Herdr は `LaunchCapsule` に argv を永続化する。
- `state.Pane` は agent 名のみ記録し、restore は `ResumeArgs` に mode を再付与しない
  ([session plan mode](session-plan-mode.ja.md))。
- `planspec.Task` に `agent` はなく、fanout-plan SKILL.md は「schema に agent を足すな、
  CLI フラグのみ」と明記する。
- settings は flat スカラー JSON のみ(nested object は型エラーで無視)。per-agent 設定は
  なく、既定エージェントは `FANOUT_AGENT` env だけ。lane 別の前例は plan mode 3 キー
  (`newSessionPlanMode` / `orchestratorPlanMode` / `childPlanMode`、すべて RepoEditable=false)。
- Codex Plan Mode 制御(`internal/infra/codexapp/plantui.go`)は app-server の
  `config/read` と `model/list` から model / effort を解決し、`thread/settings/update` と
  `turn/start` の `collaborationMode.settings` に載せる(PR #339)。fanout からの
  明示指定を受ける口はない。
- TUI は `LaunchRequest.Agents []string` / `AgentOverrides map[string]string` /
  `WorkerAgent string` と文字列を素通しし、coordinator briefing も `--agent <workerAgent>`
  を文字列で埋め込む。

### 各 CLI の実機確認(2026-09-16、ローカル binary)

| CLI | version | model | effort |
|---|---|---|---|
| claude | 2.1.272 | `--model <alias\|id>`(`fable` / `opus` / `sonnet` / `claude-fable-5-1`) | `--effort <low\|medium\|high\|xhigh\|max>` |
| codex | 0.154.0 | `-m/--model <id>`(`gpt-6-astra` / `gpt-6-pro` / `gpt-5.6-*`) | `-c model_reasoning_effort=<v>`。専用フラグなし。enum 超集合は none / minimal / low / medium / high / xhigh / max / ultra / persistent、対応値はモデル別に `model/list` の `supportedReasoningEfforts` |
| opencode | 1.18.13 | `-m/--model provider/model` | 対話 TUI にはない(`opencode run --variant` のみ) |

codex の `[profiles.*]` は 0.154 で書込廃止(`-p <name>` = `$CODEX_HOME/<name>.config.toml`)。
claude は settings JSON の `model` / `effortLevel` と env `ANTHROPIC_MODEL` /
`CLAUDE_CODE_EFFORT_LEVEL` も持つが、後述の理由で使わない。

## 決定

### 1. 文法は一つ: `name[:model[:effort]]`

全サーフェス(`--agent` の bare 既定と `NUM=` / `task-id=`、`FANOUT_AGENT`、plan spec の
`agent`、settings、TUI、briefing)で同じ文字列文法を使う。

```
claude                     名前のみ(従来どおり)
claude:opus                モデルのみ
claude:fable:xhigh         モデル + effort
codex::xhigh               effort のみ(model は未指定。下位層に codex の
                           エントリがあればその model、無ければ codex CLI の既定)
```

`internal/core/agent` に `Selection{Name, Model, Effort}`、`ParseSelection(raw)`、
`(Selection) String()` を置く。`:` で最大 3 分割し、空の model / effort は「指定なし」。
パーサはこの 1 箇所だけ。TUI と briefing は選択文字列を素通しするが、名前で分岐・照合・
検証する既存箇所には `Selection.Name` だけを渡す。対象は `ValidateKnown` /
`ValidateInstalled` を呼ぶ全 TUI 入口(`cmd/fanout/tui.go` の `tuiAgentOrDefault`、
`cmd/fanout/tui_issue.go` の `validateTUIAgentSelection`、`cmd/fanout/tui_launch.go` の
plan coordinator / manual / attach の各入口、`internal/ui/tui` の `newModel` /
`newNewPaneForm` が行う DefaultAgent 検証)と、`Request.CodexPlanMode` の
`Agent == "codex"`、`coordinatorLaunchMode` / `planSkillPrompt` / `configureTeamRequest`、
briefing の agent 分岐、`state.Pane.Agent`。完全な `Selection` は起動まで保持し、model /
effort を読むのは起動コマンドの組み立てと state の `model` / `effort` だけ。
`FANOUT_AGENT=codex:gpt-6-astra:xhigh` で引数なしの TUI を開いても codex と認識され、
Codex Plan Mode と team bridge が外れないことを回帰テストで固定する(#363)。

形式チェックのみ行う。name は `ValidateKnown`、model / effort は空白と `:` を含まない、
effort 非対応の agent に effort が付いたらペイン作成前にエラー(Unknown agent と同じ
fail-fast)。`opus` が有効か、`xhigh` がそのモデルにあるかは agent CLI に委ねる。

### 2. 注入は `agent.Definition` の純テーブル

`Definition` に `ModelFlag string`(空 = 非対応)と `EffortArgs func(effort string) []string`
(nil = 非対応)を足し、`launchArgsForBackend` が mode 引数の後ろに連結する。

| agent | model | effort |
|---|---|---|
| claude | `--model <model>` | `--effort <effort>` |
| codex | `--model <model>` | `-c model_reasoning_effort=<effort>` |
| opencode | `--model <provider/model>` | 非対応(指定はエラー) |

連結順は `LaunchArgs + BackendLaunchArgs + ModeArgs + model/effort`。claude の `--settings`
が argv 先頭に残り、束縛チェックの前提を崩さない。

`launchArgsForBackend` を通る Build 系の全入口に `Selection` を受ける変種を足す。
起動側は dry-run の `BuildCommandForBackendWithMode`、live の
`BuildResolvedCommandForBackendWithMode`、Herdr の `BuildResolvedLaunchSpecWithBackendArgs`
(claude、telemetry 引数つき)と `BuildResolvedLaunchSpec`(codex / opencode。
`internal/app/panelaunch/managed_launch.go` の `buildManagedLaunchSpec` が直接呼ぶ)の 4 つ。
復元側は `BuildResumeCommandForBackend` と `BuildResolvedResumeCommandForBackend`
(`cmd/fanout/tui_restore.go` が `BuildResolvedResumeCommand` 経由で呼ぶ)の 2 つ。
既存シグネチャは名前のみの薄いラッパとして残し、入口の取りこぼしは `agent_test.go` で
全 Build 関数を同じ `Selection` で回す表駆動テストで防ぐ。

### 3. 解決順はフィールド単位

対象(issue N / task T)ごとに name → model → effort の順に「最初の非空」を採る。
model / effort は name が一致する層からのみ採る(`--agent 5=codex` に spec の
`claude:opus` のモデルが混ざらない)。

1. per-target フラグ `--agent N=<sel>` / `--agent task-id=<sel>`
2. plan spec の task `agent`(plan lane のみ)
3. bare `--agent <sel>` / `FANOUT_AGENT`
4. settings の lane 別既定(name で引く。model / effort の空欄だけ埋める)
5. なし(フラグを付けない = agent CLI の既定)

空欄は常に下位層で補う。`--agent 5=codex::xhigh` でも `childModels` に `codex:gpt-6-astra:…`
があれば model は `gpt-6-astra` になり、settings を持ったまま CLI 既定へ明示的に戻す
記法はない(戻すなら settings のエントリを外す)。

`internal/core/agent` の `ResolveSelection(layers ...Selection)` 1 関数に集約し、
issue / plan / TUI / watcher の全 lane が呼ぶ。「`--agent` 必須」の条件は「選択対象の
全 task に spec `agent` がある」場合も満たすように広げる。plan lane では現状
`cmd/fanout/plancmd.go` が spec を読む前に `resolveLaunchRuntime` を呼び、
`run.ResolveRuntime`(`internal/app/run/runtime.go`)と Herdr の
`configHasLaunchAgent`(`cmd/fanout/runtime_backend.go`)が空の agent を即拒否する。
この 2 つの事前ゲートを spec 読み込み後まで遅らせるか、spec の agent 有無を runtime
解決前に投影し、tmux / Herdr 両 backend の回帰テストで `fanout plan <spec>`(`--agent`
なし)が通ることを固定する(#364)。

### 4. settings は lane 別 3 キー

plan mode 3 キーと同じ lane 区分。値は決定 1 の文法を空白区切りで並べた文字列で、
flat スカラー制約と per-agent 既定を両立する。

| キー | Env | 例 |
|---|---|---|
| `newSessionModels` | `FANOUT_NEW_SESSION_MODELS` | `claude:fable:xhigh codex:gpt-6-astra:xhigh` |
| `orchestratorModels` | `FANOUT_ORCHESTRATOR_MODELS` | `claude:fable:xhigh` |
| `childModels` | `FANOUT_CHILD_MODELS` | `claude:opus:xhigh codex:gpt-6-astra:medium` |

- Group は Launch、`configKeys` では `childPlanMode` の直後。RepoEditable は 3 キーとも
  false(起動コマンドと quota 消費を変える設定。`repoOverrides()` で strip + 警告)。
- 操作面は TUI 設定フォーム("s")・user config・env の 3 つ。CLI フラグは足さない
  (#472 / plan mode の決定を継承。明示は `--agent` 文法で足りる)。
- 検証は 3 つの入力経路すべてで同じリスト検証(各エントリを `ParseSelection`、name の
  重複を検出)を使う。TUI 保存(`SaveEditable` → `validateEditableValue`)は不正エントリと
  name の重複をエラーにして保存を拒む。user config(`loadFile`)と env(`envOverrides`)は
  既存の不正値の扱いに揃え、不正なエントリと 2 つ目以降の重複 name を警告して無視する
  (最初のエントリを採り、他の有効なエントリは残す)。
  `FANOUT_CHILD_MODELS='claude:opus claude:sonnet'` なら `claude:opus` が効き、`claude:sonnet`
  は警告付きで捨てる。どの経路でも不正値を起動時まで流さず、どちらが選ばれるかを実装に
  委ねない。3 経路とも回帰テストで固定する。
- agent 名の既定は変えない。name は従来どおり `--agent` / `FANOUT_AGENT` / TUI の
  選択から来る。
- `watcherAgent`(RepoEditable=true)は name-only のまま。`ValidateKnown` で検証し、`:` を
  含む値は拒む。watcher lane の model / effort は `childModels` からだけ来る。repo config が
  `watcherAgent: "claude:opus:xhigh"` で上の RepoEditable=false を迂回する穴を塞ぐため。

| 起動レーン | 従うキー |
|---|---|
| TUI 新規 Session(プロンプトモード手動ペイン) | `newSessionModels` |
| plan fan-out coordinator | `newSessionModels` |
| TUI attach | `newSessionModels` |
| issue mode オーケストレーター親ペイン | `orchestratorModels` |
| issue mode の子 Session(Project mode の子を含む) | `childModels` |
| OPEN 子なし issue の単独 Session | `childModels` |
| `fanout plan` の子タスク | `childModels` |
| watcher 起動ペイン | `childModels` |

背景の 1 行目「オーケストレーター & 計画役」は `newSessionModels` と
`orchestratorModels` に `claude:fable:xhigh` を置けば TUI から起動する親ペイン全部に
効く。子 3 行は決定 8 の skill 推奨が `--agent NUM=<sel>` と spec `agent` に書く。

### 5. plan spec に `agent`

```json
{ "id": "web-form", "title": "...", "briefing": "...", "agent": "claude:opus:xhigh" }
```

`planspec.Task.Agent string`(`json:"agent,omitempty"`)。validate で `ParseSelection` を
通す。`agent` を 1 つでも含む spec は `version` を 2 にする。新 binary は 1 と 2 を受理し、
`agent` 付きで version 1 のままの spec は validate で拒む(version 2 が要ると案内)。旧
binary は version 2 を拒否するので、`agent` を黙って無視して全 task を bare `--agent` の
名前で起動する事故を防ぐ。`agent` なしの既存 spec は version 1 のまま無変更。fanout-plan SKILL.md の
「schema に agent を足すな」は撤回する — spec が推奨の永続成果物になり、CLI フラグと
同じ文字列をそのまま貼れる。

### 6. 記録と resume

`state.Pane` に `model` / `effort`(`omitempty`、SchemaVersion 据え置き)。
`sessionview.PaneView` と `web/src/transport/types.ts` に同名フィールドを足し、TUI と
web の agent セルを `Selection.String()` 形式にする(未指定なら name のみ)。

restore は記録値を再注入する。`claude --continue` は `--model` なしだと既定モデルに戻る。安価に
起動した子が復元時に格上げされて quota を食うのを防ぐ。codex の `--model` / `-c` を
`resume --last` の前に置くか後に置くかは #363 の PR 内で実機確認して決め(誤った語順の
復元処理を先にマージしない)、結果を本書に追記する。Herdr の再起動(同じ capsule
の再実行)は `LaunchCapsule.Args` に選択が載るので追加配線はないが、server restart 後の
cold restart(`internal/app/panelaunch/managed_restart_resume.go` の
`newManagedResumeIntent`)は新しい capsule の `Args` を `resume <ref>` に固定し、保存済み
Args を再利用しない。この経路は `LaunchCapsule` に `Model` / `Effort` を持たせて resume
argv を組み直す。あわせて `internal/infra/state/launch_intents.go` の `validResumeLaunch`
(現状は `resume <ref>` の 2 引数を厳密に要求し、外れると `journal.Save()` が intent を拒む)を
「選択込みの canonical resume argv と session ref の両方を検証する」形に更新し、journal の
round-trip テストと recovery テストで固定する(#363)。OpenCode の履歴と再指定の優先関係は
下記の #362 実測結果を参照する。

### 7. codex app-server lane

`__codex-plan-tui` / `__codex-team-tui` の子ペインは argv が `fanout` 自身なので
決定 2 では届かない。`codexapp.PlanLaunchSpec` / `TeamLaunchSpec` に `--model` /
`--effort` を通して `TUIConfig` / `TeamTUIConfig` に載せ、(1) app-server 起動に
`-c model=… -c model_reasoning_effort=…` を付ける(team lane はこれで新規 thread の
既定が変わる)、(2) plan lane は `resolveCodexSettings` の最後に明示値で上書きする
(ユーザー config の `plan_mode_reasoning_effort` に負けない。`supportedReasoningEffort`
のクランプは明示値には掛けない)。明示値がないときは PR #339 の解決順を維持する。

#363 と #791 は別 PR なので、その間の main では codex の Plan Mode lane と `--team` の
team bridge lane が `Build*` を通らず、受理した model / effort を黙って捨てる。#363 は
この 2 lane(`Request.CodexPlanMode()`、`CodexTeamMode`)に model / effort が付いたら
ペイン作成前に「codexapp lane は未対応(#791)」のエラーで拒否し、#791 がその拒否を
外す。受理した指定が無視される中間状態を作らない。

Codex Plan Mode の復元(`cmd/fanout/tui_restore.go` → `codexapp.ResumeLaunchCommand`)は
thread 再開で model は thread に付くため `--model` の再注入は不要。0.155.1 では Plan turn
完了後の effort も app-server 再起動と remote TUI の resume 後に保持された(#362、下記)。
この条件では effort 消失を理由とする `--effort` の再注入は不要。旧版と turn 未実行時は未確認。

### 8. skill の推奨

- fanout-issues(claude / codex): 子 issue の `## Notes` に推奨と根拠
  (`推奨: codex:gpt-6-astra:medium — 機械的変更のため。上書き可`)を残し、親の
  Suggested command に `--agent NUM=<sel>` 列を出す。CLI は issue 本文を読まない。
- fanout-plan(claude / codex): spec の task `agent` に推奨を書き、根拠を task briefing
  末尾の 1 行に残す。実行例は `fanout plan <spec> --agent claude` のまま。TUI の既定
  plan mode では plan 承認が spec 作成より先に来るため、task ごとの選択と根拠を plan 本文に
  含めて承認前に提示する。承認後に選択を変えた場合は `--dry-run` の出力(起動コマンド)を
  見せて再確認してから live 実行する。fanout-issues は親 issue の Suggested command に
  選択が並ぶので、そこが承認点。
- 目安: docs・軽微修正・機械的変更は小モデル / 低 effort、コア実装・設計判断・大規模
  リファクタは大モデル / 高 effort。異種モデル分業の型(Fable 計画 + codex ワーカー)は
  #458 の担当で、重複させない。

## 不採用案

- **`--model` / `--effort` の別フラグ**。per-target の対応付けが二重になる
  (`--agent 5=codex --model 5=gpt-6-astra`)。1 文字列にまとめれば TUI / spec / settings /
  briefing を文字列素通しで貫ける。
- **claude の `--settings` JSON に `model` / `effortLevel`**。byte 固定の hook JSON と
  telemetry の起動束縛ハッシュを壊す。tmux backend 限定でもある。
- **env(`ANTHROPIC_MODEL` / `CLAUDE_CODE_EFFORT_LEVEL`)**。tmux 子ペインに env 経路が
  なく、codex / opencode に相当がない。
- **settings の nested `agents: {claude: {…}}`**。loader が flat スカラーしか受けない。
  3 lane × 3 agent × 2 値の flat キー(18 個)も多すぎるので不採用。
- **plan spec の `model` 単独フィールド**(#364 旧案)。name / effort を表せず、CLI
  フラグと文字列が揃わない。
- **effort 値の enum 検証**。集合が CLI ごと・モデルごとに違い(claude 5 値、codex 9 値の
  超集合、opencode は per-model)、fanout が追随する価値がない。
- **子 issue 本文の `## Model` 節を CLI が読む**。本文パースと `--agent` との優先順位が
  増える。推奨は skill が Notes と Suggested command に書けば足りる。
- **TUI 新規 Session フォームの per-launch 入力欄**。settings で賄えるため見送り。
  必要になったら別 issue。
- **LLM によるモデル自動ルーティング**。roadmap の不変条件に反する。

## 帰結

- 4 つの指定経路(フラグ / spec / settings / skill 推奨)が同じ文字列で揃い、dry-run
  の起動コマンドに agent ごとの実際の argv が現れる(claude は `--model` / `--effort`、
  codex は `--model` / `-c model_reasoning_effort=`、opencode は `--model`)。
- 起動コマンドを固定する Tier 2 golden(`scenario-sub-issue-only*` /
  `scenario-plan-basic*` / `scenario-settings-*` / `scenario-herdr-dry-run`)は全部
  再生成になる。各子 PR 内で行う。
- state に 2 フィールド増え、sessionview と web の手書き契約(`types.ts`)を同時に
  更新する。フィルタは agent 名のまま。
- 値の妥当性は CLI 任せで、不正値を通知した後も対話や別の設定値での実行が続く場合がある。
  起動成功と指定値の有効性は別に扱う(#362、下記)。
- opencode は model のみ。effort を付けると起動前にエラーになる。
- fanout-plan skill の「schema に agent を足すな」ルールを撤回し、codex 側 skill の
  parity テスト(`internal/arch/codex_integrations_test.go`)を満たして更新する。
- `agent` 付き spec は version 2 になるため、新 binary で作った spec を旧 binary に渡すと
  version エラーで止まる。黙って全 task を同じ agent で起動するより安全側。

### #362 実機検証(2026-10-04)

Claude Code 2.1.287(native binary)、Codex CLI 0.155.1、OpenCode 1.18.31 で確認した。
専用 cwd に新規作成した所有 session だけを再開し、ユーザー設定は直接編集していない。
結果はこの版と検証時の設定に限定する。調査記録は [#362](https://github.com/butaosuinu/fanout/issues/362)。

1. continue の model / effort 再指定。Claude は `--model sonnet --effort low` で
   作った会話を `--continue --model opus --effort medium` で再開すると、同じ履歴で
   `Opus 5.5 / medium` と表示した。同じ所有 ID を `--resume <id> --model opus --effort medium
   --print --output-format stream-json --verbose` で再開した実応答も `claude-opus-5-5` だった。
   OpenCode は `--model opencode-go/glm-5.3-flash --continue` を受理しても、保存済みの
   `opencode-go/kimi-k3 / max` に戻り、次の実応答も Kimi だった(`opencode export <id>` で確認)。
   固定版の [履歴復元処理](https://github.com/anomalyco/opencode/blob/014614d35b397775e5d397a490fc72368c894ec2/packages/tui/src/component/prompt/index.tsx#L311-L330)
   も最後の user message の model / variant を再設定する。#363 の再注入だけで OpenCode の
   履歴モデルを上書きできるとは保証しない。Codex の resume 語順は引き続き #363 の検証範囲。
2. 不正値と起動成功。下表の `w1-362-invalid` は存在しない値。終了コードはユーザー操作で
   終了した結果と区別する。

   | CLI / 入力 | 観測 |
   |---|---|
   | Claude `--model w1-362-invalid --effort low` | 起動後、実 turn で不存在・アクセス不可を通知。TUI は継続。 |
   | Claude `--model sonnet --effort w1-362-invalid` | 不正 effort を無視して既定値を使う警告。TUI は継続。 |
   | Codex `--model w1-362-invalid` / `-c model_reasoning_effort=w1-362-invalid` | 両方とも起動し、実 turn で HTTP 400。エラー表示後も TUI は継続。 |
   | OpenCode `--model w1-362-invalid/model` | 不正モデルの通知後も TUI は継続し、`opencode-go/kimi-k3 / max` で実応答。 |
   | OpenCode `--variant high` | 通常 TUI にはフラグがなく、help を表示して exit 1。 |

   fanout 側はコード確認による推論: tmux の `splitPane` は pane 作成と ID、Herdr の
   `matchManagedAgentProcess` は process / argv identity を確認する。provider が指定値を
   受理した証明にはならない。特に tmux wrapper は agent が即終了しても shell を残す。
   #363 の形式検証・非対応 effort の事前拒否と、CLI / provider の値検証を混同しない。
   fanout 自体で不正値の子 pane を作る live 検証は行っていない。
3. app-server の `-c`。`codex app-server -c model=gpt-6-luna
   -c model_reasoning_effort=low` で `config/read` と model 無指定の `thread/start` は
   luna / low を返し、実 turn も完了した(astra / low も確認)。#791 の team lane の既定反映を
   支持する。一方、`plan_mode_reasoning_effort` は既存設定の xhigh のままなので、Plan lane の
   明示上書きは別に必要。不正 model / effort も設定読取と thread 作成では受理される。
   `config/read` の値だけでは有効なモデル・effort での実行成功を証明できない。
4. Plan effort の復元。`thread/settings/update` と `turn/start` の
   `collaborationMode.settings` に `model=gpt-6-astra` / `reasoning_effort=medium` を設定して
   turn を完了した。app-server を終了し、既定 low の別プロセスで `thread/resume {threadId}`
   を送っても応答は medium。さらに、fanout と同じ `app-server --listen ws://127.0.0.1:<port>`
   と `codex --remote <address> resume <owned-id>` の経路でも medium と同じ履歴を確認した。
   #791 では、この版・完了済み turn の effort 保持を前提にできる。fanout の controller 全体、
   旧版、turn 未実行時の保持までは検証していない。
5. OpenCode 通常 TUI の variant。1.18.31 の help と
   [TUI の引数定義](https://github.com/anomalyco/opencode/blob/014614d35b397775e5d397a490fc72368c894ec2/packages/opencode/src/cli/cmd/tui.ts)
   に直接指定するフラグはなく、上表のとおり `--variant` は拒否された。`opencode run --variant`
   は別の入口。保存済み variant の復元や設定経由の選択と、fanout が行うプロセスごとの argv
   注入は区別し、通常 TUI への effort 指定を事前拒否する方針を維持する。

## 実装分解

親 issue #368 に Sub-issues + `## Blocked by` で wave を張る。1 issue = 1 PR。
フラグ形式は本書で確定済みなので spike は #363 をゲートしない。

| Wave | issue | 内容 | クラス |
|---|---|---|---|
| 1 | #790 | 本決定記録と roadmap / advisor doc の参照更新 | 文書 |
| 1 | #362 | spike: 未確認事項の実機検証(codex resume 語順は #363 側) | — |
| 1 | #363 | core: `Selection` / 文法 / `Definition` 拡張 / Build 全入口(起動 4 + 復元 2) / cliflags・plancmd / state 記録 / resume 再注入 / dry-run / goldens / codexapp lane への model・effort 指定は #791 まで明示拒否 | H |
| 2 | #364 | plan spec `agent`(version 2)+ 解決順の plan lane 配線 + `--agent` 必須ゲートの spec 後置(両 backend の回帰テスト)+ skill の schema 記述改訂(← #363) | M |
| 2 | #365 | settings 3 キー + 全 lane の消費 + RepoEditable gate(← #363) | H |
| 2 | #791 | codexapp lane: #363 の拒否を解除し `--model` / `--effort` 通過、app-server `-c`、plan lane の明示上書き、Plan 復元の effort(← #362 #363) | H(`cmd/fanout/codex_plan_tui.go` / `codex_team_tui.go`) |
| 2 | #792 | 表示: sessionview / TUI / web(← #363) | M + web(`internal/app/sessionview`、`web/src/transport`) |
| 3 | #366 | skills 推奨(fanout-issues / fanout-plan、claude + codex)+ plan 承認前の選択提示(← #363 #364) | M(`claude/` / `codex/` の配布プロンプト) |
| 4 | #367 | README ペア / site / CLAUDE.md / AGENTS.md(← #363 #364 #365 #366 #791 #792) | 文書 |

epic #452(異種モデル協調)の #455 / #457 / #458 は #363 に依存し、#457 と #458 は
#365 にも依存する。#458 の「coordinator のモデル指定」は決定 4 の `newSessionModels` で
満たすため。

## 参照

- epic #368、先行設計: [advisor / orchestrator](advisor-orchestrator.ja.md)(#452)
- lane 区分と RepoEditable の前例: [session plan mode](session-plan-mode.ja.md)
- 二層構造(CLI 決定論 / skill 判断): [roadmap](roadmap.ja.md)
- Codex Plan Mode の既存 model / effort 解決: PR #339
- 実装の中心: `internal/core/agent/agent.go`、`internal/app/cliflags/cliflags.go`、
  `cmd/fanout/plancmd.go`、`internal/app/panelaunch/{request.go,panelaunch.go}`、
  `internal/infra/settings/settings.go`、`internal/core/planspec/planspec.go`、
  `internal/infra/codexapp/{launch.go,plantui.go,server.go}`
