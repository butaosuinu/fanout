# ドキュメント執筆スタイル（AI 臭・冗長の抑制）

fanout のユーザー向けドキュメント（`README.md` / `README.ja.md` /
`site/content/docs/**` / `RELEASE.md` / `docs/**`）を書く・更新するときの規約。
CLAUDE.md / AGENTS.md の「Documentation Writing」節の詳細版で、禁止語・日本語の
AI 臭カタログ・セルフチェック手順をまとめる。**コードコメント・briefing 文言・
`.fanout/` 出力は対象外**。

このドキュメント自体がスタイルの実例。直す前に隣のドキュメントを読み、トーンを合わせる。

## 大原則

1. **結論先頭**。前置き・ウォームアップ・依頼文の言い換えを書かない。事実を述べてから詳細。
2. **平易語**。飾り語・定型句を平易な語に置換する（下の禁止語テーブル）。
3. **具体**。「強力」「柔軟」より数値・例・コマンドで示す。
4. **既存トーンへの追従**。簡潔・命令調・能動態。用語は一度定義して使い回す（言い換えない）。
5. **書き終えたらセルフチェック**（末尾の手順）。

## House style（既存ドキュメントの実例）

- 簡潔・命令調・能動態。`README.md`: "Fan a GitHub parent issue's OPEN sub-issues
  out into parallel tmux panes …" 主語省略の命令文。
- 用語は一度定義して使い回す。`pane` / `child` を毎回 `pane` / `child` と書く。
  言い換え（synonym cycling）はしない。
- 見出しは sentence case（文の主タイトルのみ Title Case 可）。
- 技術用語（`tmux` / `worktree` / `blocker` / `wave` / `briefing`）はそのまま使い、
  インラインで説明しない。読者は既知か、用語集を引ける前提。
- `—`（em-dash）は挿入・言い添えに**意図的に**使う house style。`README.ja.md` の
  「…ファンアウトします — 子ごとに 1 つの git worktree…」がその用法。禁止しない。

## 構成・リズム

- **結論先頭**。"In today's…" / "まず最初に" / "本ドキュメントでは" のような前置きを書かない。
- **文長・段落長を揃えない**。ただし既存が簡潔なので主眼は「冗長化させない」こと。
  1 文だけの段落も、やや長い複合文も混ぜてよい。すべて 3〜5 文の均質な段落にしない。
- **過剰構造化を避ける**。300 語未満に見出し 4 つ以上、200 語未満に箇条書き 8 つ以上は
  「整って見せようとする AI」のサイン。散文か小さなリストに畳む。
- **太字は乱用しない**。セクションに 1 つ以下、または無し。
- **rule of three を強迫的に使わない**。項目は実際の数で。三語・三例の畳みかけを避ける。

## 禁止語テーブル（英語）

### Tier 1 — 常に置換

| 禁止 | 置換 |
|------|------|
| delve / delve into | explore, look at, dig into |
| leverage（動詞） | use（※ API 文脈で「てこにする」意味が正確なら可） |
| utilize | use |
| seamless / seamlessly | smooth, easy, without friction |
| robust | strong, reliable, solid（※技術的に正確なら可） |
| comprehensive | thorough, complete, full（※技術的に正確なら可） |
| cutting-edge | latest, newest |
| game-changer / game-changing | 何がどう変わったかを具体的に書く |
| embark | start, begin |
| underscore / underscores | highlights, shows |
| meticulous / meticulously | careful, precise, detailed |
| pivotal | important, key, critical |
| testament to | shows, proves, demonstrates |
| in order to | to |
| due to the fact that | because |
| serves as | is |
| boasts / features（動詞） | has, includes |
| deep dive / dive into | look at, examine |
| unpack / unpacking | explain, break down |

### Tier 2 — 同一段落に 2 つ以上なら見直す

`harness` → use / `foster` → encourage, build / `streamline` → simplify, speed up /
`empower` → enable, let / `facilitate` → enable, help（※技術文脈で可）/
`ecosystem`（比喩）→ system, community（※技術的に正確なら可）/ `myriad` / `plethora`
→ many / `crucial` → important, key / `nuanced` → specific, subtle /
`cornerstone` → foundation, basis / `paramount` → most important.

### Tier 3 — 高密度なときだけ

`significant` / `innovative` / `effective` / `dynamic` / `scalable` /
`compelling` / `seamless` / `sophisticated` / `state-of-the-art` /
`best-in-class`。普通の語なので飽和したときだけ。数値・比較・例に置換する。

### 定型句・接続詞

削るか書き換える: `Moreover` / `Furthermore` / `Additionally` / `In today's …` /
`It's worth noting that` / `Notably` / `In conclusion` / `In summary` /
`When it comes to` / `That said` / `Let's dive in` / `Let's take a look`。

### `docs` トレランス

技術ドキュメントなので、**意味が正確な場面に限り**次は許容する:
`robust`（耐障害性の文脈）/ `comprehensive`（網羅が事実の文脈。例: cli.md が CLI の
全コマンド・フラグ・環境変数・終了コードを列挙する）/ `leverage`（API・機構を
「てこにする」意味）/ `ecosystem`（実際の連携系）/ `facilitate` / `streamline`。
飾りとして使うなら不可。

## 日本語の AI 臭カタログ（Before → After）

英語リストの移植ではなく、日本語固有の崩し方を直す。

- **冗長敬体**: 「実行することができます」→「実行できます」。「設定を行う」→「設定する」。
  「〜となっています」→「〜です」。
- **機械翻訳調**: 「あなたは〜する必要があります」→「〜してください / 〜が必要」。
  無生物主語の直訳「このコマンドは〜を可能にします」→「このコマンドで〜できます」。
- **体言止めの乱用**: 箇条書きで全項目を名詞で締めない。動詞のある主張に変える。
- **過剰なヘッジ**: 「〜と思われます」「〜かもしれません」「基本的には」を、事実なら断定に。
- **無意味な強調**: 「魅力的な」「シームレスな」「パワフルな」「画期的な」は何が良いかを
  具体で。`—` 以外の飾りは削る。
- **定型の前置き・締め**: 「いかがでしたか」「まとめると」「最後に」「〜していきましょう」を削る。
- **読点・係り受け**: 一文に主語述語を詰め込みすぎない。係り受けが曖昧な長文は分割する。
  読点で誤読を防ぐ（が、句点で切れるなら切る）。
- **「など」「といった」の多用**: 列挙が網羅でないときの逃げ。実際の項目を書くか 1 つに絞る。

例:
- Before: 「fanout を活用することで、効率的に並列作業を行うことが可能になります。」
- After: 「fanout で子 issue を並列ペインに展開できます。」

## 置換の落とし穴

- **技術用語は置換しない**: `tmux` / `worktree` / `blocker` / `wave` / `briefing` /
  `idempotent` などはそのまま。Tier 表は飾り語だけに適用する。
- **`—` は温存**: house style の意図的な挿入。「em-dash ゼロ」を機械適用しない。
- **過剰研磨をしない**: すべての不規則さを均すと、かえって AI 統計プロファイルに寄る。
  既存ドキュメントの良い文（簡潔な複合文・断定）を「直し過ぎ」で壊さない。
- **既存の良い文を誤検知しない**: 禁止語が技術的に正確な意味で使われていれば残す。

## ペア更新

`README.md` を変えたら `README.ja.md` を、`site/content/docs/foo.md` を変えたら
`site/content/docs/foo.ja.md` を対で更新する。README とサイトで同じ事実がずれないようにする。

## セルフチェック（書き終えたら通す）

1. **treadmill test**: 各段落に「新しい事実・主張・展開が 1 つあるか」を問う。前段の
   言い換えだけの段落は削る。
2. **read-aloud test**: 声に出して読む。不自然に均質（同じ長さ・同じ語頭）なら文長を変える。
3. **paragraph-reshuffle test**: 段落を入れ替えても破綻しないなら、議論ではなく箇条の羅列。
   つなぎを足すか順序に意味を持たせる。
4. **禁止語照合**: 上の Tier 1 / 定型句 / 日本語カタログと突き合わせる。
5. **ペア確認**: README ↔ site、EN ↔ JA の対が揃っているか。
