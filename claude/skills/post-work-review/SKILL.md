---
name: post-work-review
description: 実装作業が一段落したコードを「仕上げ」モードに通すワークフロー。まず code-review プラグインで diff の bug を洗い出して修正し、続けて codex:review を別視点として回し、指摘がなくなる (codex が approve / clean / 問題なし と返す) まで「review → 修正 → 再 review」をループする。最終 branch では canonical full check と exact-HEAD marker も管理する。ユーザーが「review して仕上げて」「post-review」「finalize」「コミット前にもう一度見て」「二重チェック」「codex review もかけて」等と言ったとき、または /post-work-review が明示的に呼ばれたときに、必ずこの skill を使う。実装後のチェックを口にしたら「dashboard」「visualization」のような明示語が無くてもこの skill を呼ぶこと。
---

# post-work-review

実装が一段落したコードを、2 種類のレビュアーで仕上げるワークフロー skill。

## なぜこれが要るか

単発の code review は見落とすことがある。
`code-review` プラグイン (内製のマルチエージェント) と `codex:review` (Codex CLI、別モデル別視点) を直列で通し、後者は「指摘なし」になるまでループさせる。
push または PR 作成の直前に、独立した二系統の最終チェックを掛けるのが狙いである。
本 skill はそのオーケストレーターであり、レビュー自体は実装しない。

## 適用範囲と前提

- **対象**: 現在の git 作業ツリーにある変更 (dirty でも commit 済みでも可)。
  dirty tree は未コミットレビューとして扱い、最終 marker は clean な commit 済み branch をレビューしたときだけ書く。
  `code-review` は内部で `git diff` 系を見るし、`codex:review` も `--scope auto` で同様に判定する。
- **git リポジトリ外なら早期終了**: `git rev-parse --is-inside-work-tree` で false なら、ユーザーに「ここは git リポジトリではないので post-work-review は使えない」と伝えて終了する。
- **diff が空なら早期終了**: `git status --porcelain` と、リポジトリの既定 branch を基準にした committed diff が両方空なら、レビュー対象が無い旨を伝えて終了する。無駄なレビュー呼び出しはトークンの浪費。

## 全体フロー

```
[ユーザー: /post-work-review or 「review して仕上げて」]
        │
        ▼
[Pass 0] final branch は canonical full check、dirty tree は focused check
        │
        ▼
[Pass 1] code-review プラグイン  ── 指摘を集める → 修正
        │
        ▼
[Pass 2] codex:review ループ (codex companion がある時のみ)
   ┌──── review 実行
   │       │
   │       ▼
   │   指摘あり? ── No ──→ 完了
   │       │ Yes
   │       ▼
   │   前回と同じ指摘集合? ── Yes ──→ ユーザーに判断を仰ぐ
   │       │ No
   │       ▼
   │   修正
   └──────┘
        │
        ▼
[Step 4] 変更サマリ + ゲート付き図をチャットに出す (最終確認)
        │
        ▼
[Step 5] レビュー済みコミットを marker に記録 (PR ゲートの signal)
```

## Pass 0: プロジェクト検証

`git status --short` を確認し、次のどちらか一方だけを実行する。

1. **clean な commit 済み branch の最終ゲート**: リポジトリの指示またはビルド設定から canonical full check を解決する。
   包括的な単一コマンドが定義されている場合はそれを 1 回だけ実行し、内包される個別検証を重複して実行しない。
   失敗したら Pass 1 へ進まない。
   今回の branch に起因する失敗だけを直し、focused check を回して commit してから、新しい HEAD で本 skill を最初からやり直す。
   環境起因または既存の失敗でも、未検証の HEAD に marker は書かない。
2. **dirty tree の未コミットレビュー**: 変更範囲の focused check だけを実行する。
   marker を書けない対象に full check を使わない。
   レビュー後に候補を commit し、push 前に clean な branch scope で本 skill をやり直す。

検証手段が無い repo では、その旨を 1 行報告して Pass 1 へ進む。
golden またはスナップショットを更新した場合は、diff を目視してから commit 対象に含める。

## Pass 1 — code-review プラグインで掃除

1. ユーザーに「Pass 1 として code-review を回します」と 1 文で宣言する。長い前置きは不要。
2. **Skill ツール経由で `code-review` を呼ぶ**: `Skill(skill="code-review")`。引数は付けない (デフォルトの effort で十分。`--comment` は付けない — PR が無いローカル作業中にも使う skill なので、コメント posting は本質ではない。レビュー本文の収集だけが目的)。
3. 返ってきた指摘を読み、修正すべき項目を選別する。**全ての指摘を機械的に直すのではなく**、明らかな bug / 規約違反 / セキュリティ問題を優先する。スタイル提案レベルは Pass 2 のあとに一括判断してよい (codex で同じことが再度挙がるなら直す価値がある)。
4. repo にレビューチェックリストがあれば、diff に対して各項目を自己チェックし、取りこぼしを修正対象に加える。無ければ飛ばす。
5. 修正する項目をユーザーに 1〜2 文で宣言してから Edit に入る (例:「null チェック漏れ 2 箇所と未使用 import を直します」)。
   修正後は変更範囲の focused check を実行し、短く完了報告する。

### code-review-strict との区別 (重要)

`code-review-strict` 専用の運用ルールは、本 skill が呼ぶプラグイン版 `code-review` には適用しない。プラグイン版は「diff の bug を挙げる→必要なら直す」までを含めて運用する想定なので、Pass 1 の修正フェーズは普通に Edit してよい。

混同しないために: `code-review-strict` をこの skill から呼ばないこと。呼ぶのはプラグインの `code-review` のみ。

## Pass 2 — codex:review ループ (codex が利用可能なときのみ)

`codex:review` は skill ツール一覧に出てこない (slash command のみ) ので、**Bash で companion スクリプトを直接叩く**。バージョン番号はパスに含まれるため glob で吸収する。

### 前提チェック 0: エージェント指示ファイルの変更で fail closed にする

**companion を解決する前に**、レビュー対象の diff がリポジトリのエージェント指示
ファイルを変更していないか確認する。レビュアーは変更後の checkout の中で走るため、
指示ファイル自体が変わっていると、レビュー結果がその変更後の指示に影響される。
後段でこちらが base 側の scope で裁定し直しても、**出力されなかった blocker は
復元できない**。

対象は 3 つとも外さない。1 つでも落とすと素通り経路が残る:

1. **全階層** の指示ファイル。root だけを列挙すると `internal/ui/AGENTS.md` の
   ような scoped instruction を追加して配下の実装を変える branch が抜ける
2. **case-insensitive 照合**。macOS の既定 checkout は case-insensitive なので、
   branch が足した `sub/agents.md` を reviewer は `sub/AGENTS.md` として読める。
   Git の pathspec は既定 case-sensitive なので `:(icase,glob)` を使う
3. **untracked と ignored**。`.codex/*` は `.gitignore` で無視されるため、
   `.codex/config.toml` を置いて指示を注入しても committed diff には出ない

```bash
instr=(
  ':(icase,glob)AGENTS*.md'    ':(icase,glob)**/AGENTS*.md'
  ':(icase,glob)CLAUDE*.md'    ':(icase,glob)**/CLAUDE*.md'
  ':(icase,glob).codex'        ':(icase,glob).codex/**'
  ':(icase,glob)**/.codex/**'
  ':(icase,glob).claude'       ':(icase,glob).claude/**'
  ':(icase,glob)**/.claude/**'
  ':(icase,glob)claude/skills/post-work-review/**'
)
default_branch="$(git symbolic-ref --short refs/remotes/origin/HEAD 2>/dev/null || echo origin/main)"
base="$(git merge-base HEAD "$default_branch" 2>/dev/null || git merge-base HEAD main 2>/dev/null)"
if [ -z "$base" ]; then
  echo "BLOCKED: merge-base を解決できないため fail closed"
else
  git diff --name-only "$base"...HEAD -- "${instr[@]}"   # commit 済みの変更
  git diff --name-only -- "${instr[@]}"                  # worktree の未コミット変更
  git diff --name-only --cached -- "${instr[@]}"         # index の未コミット変更
  git ls-files --others -- "${instr[@]}"                 # untracked (ignored 含む)
  git ls-files -v -- "${instr[@]}" | grep -E '^([a-z]|S) '  # index flag で隠された変更
fi
```

`git ls-files --others` に `--exclude-standard` を付けないのは、ignored な
指示ファイルこそ検出したいため。

最後の `ls-files -v` が要るのは、`git update-index --assume-unchanged AGENTS.md`
を付けてから作業ツリーだけ書き換えると、上の `git diff` 3 本がどれも変更を出さず、
`--others` も追跡済みファイルを出さないため。reviewer は変更後の指示を読むのに
前提チェックは空になる。小文字のステータス文字が assume-unchanged、`S` が
skip-worktree で、どちらも 1 行でも出たら fail closed。

この確認が読むのはファイル**名**だけなので、対象リポジトリの中身に影響されない。
判定そのものは信頼できる。

pathspec に自分自身 (`claude/skills/post-work-review/**`) を含めるのは、この
skill を変更する branch で marker を書かせないため。ただしこれだけでは足りない
— 走っている手順自体が branch 側なら、規則ごと消せてしまう。

上の pathspec ベースの検査には原理的な穴が残る。`.codex/config.toml` や
ユーザー設定が `model_instructions_file` / `project_doc_fallback_filenames` で
指示ファイルの場所を差し替えている環境では、branch が参照先 (`REVIEW.md` など)
だけを変えても固定 pathspec に一致しない。TOML の escaped key
(`"model_instructions_file"`) を使えば単純な grep も抜ける。**この検査を
prose で再実装しない** — 下の信頼済み helper に委譲する。

### 前提チェック 0a: 信頼済み helper があればその判定を採る

Codex 版の post-work-review には、この判定を完全に行う helper が同梱されている
(`mark-reviewed-head.sh guard`)。checksum 検証されたリリースインストーラが所有し、
レビュー対象 checkout の外に実体で置かれ、TOML トークナイザによる escaped key の
検出、submodule、nested Git 境界、index flag まで見る。**あるなら必ずそれを使う。**

```bash
guard="${CODEX_HOME:-$HOME/.codex}/skills/post-work-review/scripts/mark-reviewed-head.sh"
base_branch="$(git symbolic-ref --short refs/remotes/origin/HEAD 2>/dev/null | sed 's|^origin/||')"
base_branch="${base_branch:-main}"
base_head="$(git rev-parse "origin/$base_branch" 2>/dev/null || git rev-parse "$base_branch")"
if [ -x "$guard" ] && [ ! -L "$guard" ]; then
  "$guard" guard "$(git rev-parse HEAD)" "$base_branch" "$base_head" || echo "BLOCKED"
fi
```

非ゼロ終了なら **marker を書かない**。helper が無い環境では上の pathspec 検査が
best-effort の代替になるが、動的 instruction source までは見られない。その旨を
完了報告に書く。

### 前提チェック 0b: gate 自身が信頼済みコピーであること

この skill が **レビュー対象 checkout の中を指す symlink から走っていないこと**
を確認する。Codex gate と同じ「gate は checkout の外の実体コピー」という条件で、
fanout の `make link` は post-work-review だけ symlink せずコピーする。

判定は 2 つ要る。**symlink であること自体の拒否**と、**実体パスの位置確認**。
片方だけでは足りない:

- 最終 component の `-L` だけだと、`CLAUDE_CONFIG_DIR` での差し替えや
  `~/.claude/skills` 自体が symlink の環境で、実際に読み込まれた gate を見ない
- 実体パスの包含確認だけだと、gate が**別の worktree**への symlink のときに
  「現在の repo の外」として通ってしまう。別 branch が変更した gate で
  現在の branch の marker を書けてしまい、「gate は常にコピー」の不変条件が壊れる

```bash
skill_dir="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/skills/post-work-review"

# 1) path component に symlink があれば拒否
p="$skill_dir"
while [ -n "$p" ] && [ "$p" != "/" ]; do
  if [ -L "$p" ]; then echo "BLOCKED: symlink component: $p"; break; fi
  p="$(dirname "$p")"
done

# 2) 実体パスがレビュー対象 checkout の中なら拒否
real_skill="$(cd "$skill_dir" 2>/dev/null && pwd -P)" || real_skill=""
repo_real="$(cd "$(git rev-parse --show-toplevel)" && pwd -P)" || repo_real=""
if [ -z "$real_skill" ] || [ -z "$repo_real" ]; then
  echo "BLOCKED: gate かリポジトリの実体パスを解決できない"
else
  case "$real_skill" in
    "$repo_real"|"$repo_real"/*) echo "BLOCKED: gate がレビュー対象 checkout 内にある: $real_skill" ;;
  esac
fi
```

BLOCKED が出たら **marker を書かない**。`make link` を最新の Makefile で
回し直せば実体コピーに戻る。

出力が空でなければ **Step 5 の marker を書かない**。Pass 2 は参考情報として
回してよいが、完了報告で次を明示し、ゲートは閉じたままにする:

> ⚠️ この branch はエージェント指示ファイル(<paths>)を変更しているため、同じ checkout
> 内のレビューは信頼できない。marker は書かない。trusted checkout から起動した
> レビュアーか、人間のレビューが必要。

`git merge-base` が解決できない (リポジトリ外、shallow、既定 branch 不明) 場合も
fail closed にする。「判定できなかったから通す」は、この確認を無意味にする。

### 前提チェック 1: codex companion の解決

Pass 1 のあと、まず companion スクリプトの実体を確認し、**glob を1回だけ展開して単一パスに解決**してから後段で使い回す(以降の `node` 呼び出しで glob を再展開しない — 複数バージョンが cache に残っていると `node <script1> <script2> review …` のように展開されて `review` がサブコマンド位置からずれ、Pass 2 が壊れるため):

```bash
# read ループ (mapfile は bash 4+。macOS の bash 3.2 では未対応なので使わない)
companions=()
while IFS= read -r c; do companions+=("$c"); done \
  < <(ls ~/.claude/plugins/cache/openai-codex/codex/*/scripts/codex-companion.mjs 2>/dev/null)
companion="${companions[0]:-}"
```

- **0 件 (`${#companions[@]}` == 0)**: Pass 2 を **skip** し、次の 1 行を Claude の応答に明記してから Step 4 へ進む:

  > ⚠️ codex companion 未検出のため second-pass review は skip。Pass 1 単独でのレビュー結果として扱う。

  この設計により codex 未導入環境でも skill は機能し、Pass 1 完了で Step 4 の確認サマリを出し、Step 5 の marker が書かれる。
- **2 件以上 (`${#companions[@]}` >= 2)**: 複数バージョンが残っている。`head -1` で暗黙に1つを選ばず、トラブルシューティングに従い `ls ~/.claude/plugins/cache/openai-codex/codex/` の結果を提示してユーザーにどれを使うか確認する。確定後にそのパスを `$companion` として使う。
- **1 件**: 解決済みの `$companion` を使って下記「1 反復の手順」を回す (approve まで、oscillation セーフティ維持)。

### 1 反復の手順

1. ユーザーに「Pass 2 反復 N: codex review を実行します」と 1 文宣言。
2. Bash を実行(前提チェックで解決した単一パス `$companion` を使う。glob を再展開しないこと):

   ```bash
   node "$companion" review --wait
   ```

   `--wait` を付けるのは、ループ制御のために stdout を同期的に受け取る必要があるため。`--background` だと `/codex:status` を別途ポーリングする羽目になり、ループの単純性が失われる。
3. stdout を読む。codex の native review は markdown を返してくる。出力をユーザーに提示するときは `codex:codex-result-handling` skill のガイド (verdict / summary / findings / next_steps、severity 順、file:line を改変しない、推測と確定を分けて表示) に従って整形する。長文の場合は要点のみ提示し、フルテキストは折り畳むか「全文は codex の出力をそのまま添付」する形でよい。

### 終了判定

native review の markdown には機械可読な「0 findings」マーカーが (現状) 無い。文面から判定するが、**approve という語が含まれているだけで clean と即断してはいけない**。次の両方を満たしたときのみ clean と見なす:

1. **肯定的な verdict がある**: "approved" / "looks good" / "no issues" / "no findings" / "0 findings"、または「指摘なし」「問題なし」「特になし」「修正不要」等。
2. **否定・拒否の表現が無い**: "not approved" / "cannot approve" / "can't approve" / "do not approve" / "request changes"、または「承認しない」「approve できない」「却下」「要修正」等が含まれていたら **clean ではない**(これらは "approve" という語を含むが拒否なので、単純な部分一致で clean 判定すると取りこぼす)。かつ findings セクションが空 / "(none)" / 「なし」であること。

「approve できない理由は…」のように肯定語と否定語が同居する文面、findings が残っているのに approve 風の語がある、等の **グレーゾーンが出たら勝手に clean 判定せず、ユーザーに「これは clean と判断していい?」と一度だけ確認する**。早期にループを終了すると、本来直すべき指摘を取りこぼす。

### 指摘の裁定 (直す前に分類する)

clean でなかった指摘は、直す前に分類する。リポジトリが対応環境マトリクスと
非ゴールを宣言している場合、基準は **マトリクス内で到達可能な trigger を示せ、
かつ明示された非ゴールに当たらないこと**。到達可能性だけでは足りない —
非ゴール (他プロセスとの完全排他、crash-resume の全窓回収など) は定義上
到達可能なので、一軸で見ると必ず修正対象になってしまう。

**マトリクスは対象 branch の文書からは読まない。** リポジトリのエージェント指示
ファイル (fanout では `AGENTS.md` の `Automated PR Review Scope`) を、**レビュー
対象の base 側** で参照する。branch 自身が書き換えた scope 文書を信頼すると、
その PR が自分への blocker 指摘を「範囲外」と判定して marker まで到達できる。
diff が scope 文書自体を変える場合は base 側で裁定し、拡大の試みそのものを
指摘として扱う。

| 分類 | 対応 |
| --- | --- |
| 到達可能 かつ 非ゴール外 | 直す。重大度では落とさない |
| 到達不能 または 非ゴール | 直さない。理由と該当する scope 行を報告する |
| 判断が割れる / 今回のスコープを超える | ユーザーに渡す。勝手に直さない |

**その反復の指摘が範囲外のものだけだったら、直さずにループを終える**。
範囲外の指摘を直すとコードが増え、次の反復のレビュー面が広がる。
これがラウンドを収束させない主因である。

ループをここで終える場合、次の反復は存在しないので、棄却した指摘とその根拠は
**Step 5 の marker を書く前に、完了報告へ必ず出す**。「次の反復で言う」に
先送りすると理由がユーザーに届かず、無言で握り潰したのと同じになる。

スコープ文書が無いリポジトリでは従来どおり全件を検討してよい。

### Oscillation セーフティ (上限なしの暴走防止)

clean になるまで反復するが、codex が同じ指摘を解消できずに繰り返す場合は、以下を必ず守る:

- **前回反復の指摘集合を覚えておく**: 各 finding を `<file>:<行範囲>:<指摘要旨1行>` の形で正規化したリストとして保持する (会話メモリ内、メモリファイルには書かない)。
- **2 回連続で同一集合なら停止**: ジャッジに迷うときは「ファイルパスと指摘要旨が同じなら同一」と判断する。同一と判定したら修正には入らず、次の文面でユーザーに判断を仰ぐ:

  > codex が 2 反復連続で同じ指摘を返しています: [概要]。
  > 自分の修正で解消できていない可能性があります。次のうちどれにしますか?
  > (a) この指摘を無視してループを終了
  > (b) 別アプローチで自分が手動修正したいので一旦停止
  > (c) もう一度だけ違う方針で直してみる

  AskUserQuestion で 3 択を出す。

- **完全に同一でなくても、3 反復連続で同じファイル群に同種の指摘が出続けるなら警戒する**: 同様にユーザーに状況共有して判断を仰ぐ。これは「微妙に文言だけ変わるが本質は同じ」を捕まえるための保険。
- **毎回違う指摘が出続ける場合もセーフティを効かせる**: 修正のたびに新しい指摘が出ると指摘集合が毎回変わり、上の 2 条件はどちらも発火しない。3 反復連続で新規指摘が出続け、かつ裁定で範囲外に落ちる割合が高いなら、同じ 3 択をユーザーに出して止める。

### 修正フェーズ

指摘を直す手順は通常の Edit ベース実装と同じ。
Pass 1 と同じく、修正に入る前に「これとこれを直します」と 1〜2 文で宣言する。
修正後は変更範囲の focused check を実行してから次の反復に進む。
full check はここで重ねて実行しない。

## Step 4: show final change summary in chat

Pass 2 ループが clean 判定 / ユーザー停止指示 / oscillation 検知のいずれかで終了したら、または codex companion 未検出で Pass 2 を skip したら、marker 記録の前に、レビュー対象 diff の最終確認として **チャットに** 変更サマリを出す。これは PR body 生成ではなく、ローカル作業者に「何を変えたか」を最後に確認させるための応答である。ファイルを書いたり、`gh pr create` の本文を作ったりしない。

出力は、チャットで読める次の軽量な形式にする:

1. **TL;DR**: 1〜2 文で、今回の diff の意図と実装結果を要約する。直後に単独行で `Review effort: <0-5>` を書く (0=機械的、5=要熟読)。
2. **変更ファイル表**: `File | What changed | Why` の表を出す。実際に触れたファイルだけを載せ、base から来た無関係変更や未確認の推測は混ぜない。
3. **リスク**: 残る注意点がある場合だけ `> [!WARNING]` ブロックで書く。低リスクなら省略し、「リスクなし」の埋め草は書かない。
4. **ゲート付き Mermaid**: 挙動 / 呼び出しフロー / スキーマが変わった場合だけ ```mermaid を最大 1 つ出す。refactor / rename / docs / format / config / test-only では出さない。図に含める関数名、ファイル名、設定名、コマンド名などは、diff または現在の worktree に実在することを `rg` 等で自己検証する。辿れないシンボルは図から落とし、薄い図しか作れないなら図ごと省く。

挙動を断定するときは、可能な範囲で `file:line` を添える。根拠が diff から辿れない主張は書かない。

このステップは確認サマリであり、レビュー verdict ではない。サマリや図の生成に失敗しても **marker をブロックしない** し、Pass 1 / Pass 2 の clean 判定を変更しない。失敗時は「変更サマリは生成できなかった」と短く伝え、Step 5 の marker 前提条件を満たすなら Step 5 へ進む。

## Step 5 (final): record reviewed commit

レビュー済みマーカーを書き出す。PR 作成ゲートがこの marker を参照する環境では「このコミットはレビュー済み」と認識する signal なので、**レビューが実質的に行われ、かつ修正が全て commit 済みのときだけ**書く。次の前提を**いずれか欠いたら marker を書かない**:

1. **最低 1 つのレビューパスが成功している**: Pass 1 (code-review) が正常完了したか、Pass 2 (codex) が少なくとも 1 反復回って結果を返している。Pass 1 がエラーで、かつ Pass 2 も codex 未検出 / エラーで実行できなかった場合は、成功したレビューが 0 件なので **marker を書かず**、レビュー未完了である旨をユーザーに伝えて終了する(ゲートは閉じたまま)。
2. **working tree が clean**: Pass 1/Pass 2 の修正が全て commit 済みであること。dirty なまま marker を書くと、未コミットの修正は PR (= push 済みコミット) に乗らないのに HEAD が「レビュー済み」とマークされ、ゲートが unreviewed なコードの PR 作成を通してしまう。
3. **現在の HEAD が canonical full check を通過している**: Pass 1 / Pass 2 の修正でファイルが変わったら focused check だけを実行し、marker は書かない。
   修正を commit して新しい HEAD で本 skill をやり直し、canonical full check とレビューを通す。
   検証手段が無い repo ではこの前提を課さない。
4. **エージェント指示ファイルを変更していない**: 前提チェック 0 が空でなかった場合、
   または判定自体ができなかった場合は marker を書かない。この branch のレビューは
   trusted checkout または人間が行う。
5. **gate 自身が信頼済みコピーである**: 前提チェック 0b で symlink を検出したら
   marker を書かない。

```bash
if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "git リポジトリ外: marker は書きません"
elif [ -n "$(git status --porcelain)" ]; then
  echo "⚠️ 未コミットの変更があります。修正を commit してから再度この手順を実行してください (dirty tree では marker を記録しません)。"
else
  marker="$(git rev-parse --git-dir)/post-work-review-passed"
  if ! rm -f "${marker}.meta"; then
    echo "⚠️ 古い marker metadata を削除できないため marker は書きません"
  elif git rev-parse HEAD > "$marker"; then
    echo "marker 記録: $(git rev-parse HEAD)"
  else
    echo "⚠️ marker を書き込めませんでした"
  fi
fi
```

- git リポジトリ外で `/post-work-review` を呼ばれた場合は no-op。
- marker は `git rev-parse --git-dir` が返すディレクトリ配下に置くため、worktree ごとに分離される。
- legacy marker を記録する前に sibling `.meta` を削除する。
- HEAD が進めば marker は自動的に stale になりゲートが再び閉じる。
  marker を書くのは、修正を全て commit し、canonical full check とレビューを通した最終状態だけ。
  push やコミット自体は本 skill の責任外なので、review で修正した場合は別途 commit してから本 skill をやり直す。

## 完了報告

ループ終了時 (clean 判定 / ユーザー停止指示 / oscillation 検知のいずれか) に、以下を 2〜3 文で報告する:

- Pass 0 で実行した canonical full check または focused check と結果 (検証手段が無く skip した場合はその旨)
- Pass 1 で何件直したか
- Pass 2 が何反復回って終わったか (codex 未検出で skip した場合はその旨)
- 最終的に codex が approve したか、それともユーザー判断で停止したか
- Step 4 で変更サマリを提示したか (提示できなかった場合はその理由)
- Step 5 で marker を書いたか (現 HEAD)
- 裁定で範囲外として棄却した指摘があれば、1 件ずつ要旨と適用した scope 行を列挙する (件数だけの要約にしない)
- 残課題があれば箇条書きで列挙 (oscillation で残った指摘など)

長い総括は不要。ユーザーは diff を読めば中身は分かる。

## やらないこと

- **`code-review-strict` の呼び出し**: 別物。本 skill では使わない。
- **`--comment` フラグ付き code-review**: PR コメントを直接付けに行く動作は本 skill の責任範囲外 (専用に `/code-review --comment` を別途叩けばよい)。
- **コミットや push の自動実行**: 本 skill は仕上げレビューまで。コミットメッセージ作成や push はユーザー指示で別途行う (Step 5 の marker 書込はレビュー済み signal の記録であって、コミットや push ではない)。
- **`/codex:status` の自動ポーリング**: `--wait` で同期実行するため不要。
- **メモリへの新規エントリ追加**: 本 skill 自体がルールの保管庫。

## トラブルシューティング

- **`codex-companion.mjs` の glob が複数マッチ**: 通常 1 バージョンしか入っていない。複数あるならユーザーに通知し、`ls ~/.claude/plugins/cache/openai-codex/codex/` の結果を見せて指示を仰ぐ。
- **codex CLI 未認証 / レートリミット**: companion スクリプトがエラーを出す。`codex:setup` skill を案内する。
- **code-review が失敗した場合**: Pass 1 を諦めて Pass 2 から始めるか、ユーザーに継続判断を仰ぐ。Pass 1 失敗だけで全体を中止しないこと (Pass 2 単独でも価値はある)。ただし **Pass 1 が失敗し、かつ Pass 2 も codex 未検出 / エラーで実行できなかった場合は、成功したレビューパスが 0 件なので Step 5 の marker を書かない** (ゲートは閉じたまま)。レビュー未完了をユーザーに伝えて終了する。
