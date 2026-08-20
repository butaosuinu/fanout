import { useLingui } from "@lingui/react/macro";
import { FileDiff } from "@pierre/diffs/react";
import { memo } from "react";
import type { Theme } from "../settings/useSettings";
import {
  LINE_DIFF_TYPE_PLAIN,
  TOKENIZE_MAX_LENGTH_PLAIN,
  TOKENIZE_MAX_LINE_LENGTH,
  type DiffFilePlan,
} from "./diff";
import { IconButton, IconChevronDown, IconChevronUp } from "../../ui/icons";

/* 片側しかない file(新規追加・削除)は data-diff-type="single" になり、既定では
 * 全幅に広がる。GitHub の split 表示に合わせ、追加のみは右半分、削除のみは左半分へ
 * 寄せる。 */
const SINGLE_SIDE_CSS = [
  'pre[data-diff-type="single"] > code[data-additions]{ width:50%; margin-left:auto; }',
  'pre[data-diff-type="single"] > code[data-deletions]{ width:50%; margin-right:auto; }',
].join("\n");

/* ファイル名ヘッダとコード本文の境界。ライブラリはどちらにも同じ --diffs-bg を
 * 敷き、罫線も引かないので、そのままでは切れ目が読めない(隣り合う file 同士も
 * 同じ)。色はシャドウスコープのトークンから取る — アプリ側の --suna などは
 * shadow root に無く、別々に選べる diff テーマにも追従しない。
 * ライブラリ CSS は @layer base、unsafeCSS は @layer unsafe に包まれて注入される
 * (レイヤ順 base, theme, rendered, unsafe)ので、詳細度に関係なく勝てる。 */
const HEADER_EDGE_CSS = [
  '[data-diffs-header="default"]{',
  "  background-color: var(--diffs-bg-context);",
  "  border-block: 1px solid var(--diffs-bg-separator);",
  "}",
].join("\n");

/* 横スクロールの封じ。本文はどこにも横スクロール箱を持たせず、長い行は折り返しで
 * 読ませる(アプリ側は styles/diff.css の .diff-body が対で閉じる)。3 つ要る:
 *
 * - コードセルはスクロール箱を持つ(既定 `overflow: scroll clip`)。箱が消えるのは
 *   split + 折り返しのときだけで、縦積み表示と片側しかない file には残り、
 *   スクロールバーはライブラリ側が潰しているので触るまで気付けない。
 *   `--diffs-overflow-override` を立てる手もあるが、その変数はライブラリ自身が
 *   Mobile Safari のスクロール中に inline で書き、戻すときに `auto` を残す
 *   (CodeView.js)。継承より強い足場を取って overflow を直接書く。
 * - コード列のトラックは既定が `1fr`(= minmax(auto,1fr))で、min は min-content。
 *   折り返さないトークンが 1 行あるだけでトラックごと container より広くなる。
 *   ライブラリが [data-dehydrated] にだけ与えている minmax(0,1fr) を常時にする。
 * - 行の折り返しはライブラリ側が `word-break:break-word` だけで指定している。
 *   仕様上これは `word-break:normal` + `overflow-wrap:anywhere` と同義だが、
 *   min-content 幅を縮めない実装があるので、その展開形を明示して書く。
 *   同じ指定は注釈(`[data-annotation-content]`)にも掛かっているが、fanout は
 *   注釈を渡していないので触らない。渡すようになったらここも要る。
 *
 * 後ろ 2 つは飾りではない。箱だけ消すと、はみ出したトークンが見えないまま
 * 切り落とされる。
 *
 * スクロールバーの取り置きも一緒に 0 にする。ライブラリはコードセルの下余白を
 * 「gap - スクロールバー実測」で出すが、その実測は connectedCallback 時点 —
 * unsafeCSS が刺さる前 — に走るので、箱を消しても実測値は残り、取り置きだけが
 * 消えて下余白が gap より狭くなる。 */
const NO_OVERFLOW_CSS = [
  "[data-code]{ overflow: clip; --diffs-scrollbar-gutter: 0px; }",
  "[data-diff],[data-file]{",
  "  --diffs-code-grid: var(--diffs-grid-number-column-width) minmax(0, 1fr);",
  "}",
  '[data-overflow="wrap"] [data-line]{ word-break: normal; overflow-wrap: anywhere; }',
].join("\n");

/* shadow DOM 内はライブラリの unsafeCSS からしか触れない。入れるのはこの固定文字列
 * だけで、patch 由来の値は一切混ぜない(敵性入力規約)。
 * split / stack それぞれの完成品を定数で持つ — render ごとに組み立てると文字列が
 * 変わったと見なされ、ライブラリが file を丸ごと描き直す。
 * 片側寄せは split のときだけ。stack も data-diff-type="single" になるので、
 * 渡したままだと本文が半分幅に潰れる。 */
const SPLIT_CSS = [HEADER_EDGE_CSS, NO_OVERFLOW_CSS, SINGLE_SIDE_CSS].join("\n");
const STACK_CSS = [HEADER_EDGE_CSS, NO_OVERFLOW_CSS].join("\n");

/* ファイル名ヘッダの右側に並ぶ操作。ラベルに file 名を入れるのは、入れないと
 * 全 file のボタンとチェックが同名で並び、支援技術の要素一覧から区別できないため。 */
function FileActions({
  name,
  lineText,
  collapsed,
  viewed,
  viewable,
  onToggle,
  onToggleViewed,
}: {
  name: string;
  lineText: string;
  collapsed: boolean;
  viewed: boolean;
  /* identity が曖昧な path はチェックを持てない(viewed.ts の sameFileGroup)。
     押しても何も起きないチェックを出すほうが害なので、丸ごと省く。 */
  viewable: boolean;
  onToggle: () => void;
  onToggleViewed: () => void;
}) {
  const { t } = useLingui();
  return (
    <span className="diff-file-acts">
      {/* 行数は「大きい file だ」という情報なので、畳んでいる間はボタンの外に
          テキストで残す */}
      {collapsed && <span className="diff-file-lines">{t`${{ lines: lineText }} 行`}</span>}
      {viewable && (
        <label className="diff-file-viewed">
          <input
            type="checkbox"
            checked={viewed}
            aria-label={t`${{ name }} — 確認済み`}
            onChange={onToggleViewed}
          />
          <span aria-hidden="true">{t`確認済み`}</span>
        </label>
      )}
      <IconButton
        /* JS の t マクロを使う(<Trans> は {変数} 前後の空白を落とすため、
           " — " 区切りのアクセシブル名が壊れる) */
        label={
          collapsed
            ? t`${{ name }} — ${{ lines: lineText }} 行 — 展開`
            : t`${{ name }} — 折りたたむ`
        }
        onClick={onToggle}
      >
        {collapsed ? <IconChevronDown /> : <IconChevronUp />}
      </IconButton>
    </span>
  );
}

/* この file をどう描くかの指定一式。`@pierre/diffs` の options は毎 render 作り
 * 直してよい(ライブラリ側が中身を比較する)が、unsafeCSS だけは文字列が変わると
 * file を丸ごと描き直すので、上の定数をそのまま渡す。 */
function fileDiffOptions({
  entry,
  theme,
  diffThemes,
  stack,
  collapsed,
}: {
  entry: DiffFilePlan;
  theme: Theme;
  diffThemes: { light: string; dark: string };
  stack: boolean;
  collapsed: boolean;
}) {
  const { highlight, inlineDiff } = entry;
  return {
    /* light/dark 両方を渡すとライブラリが両方の CSS 変数を出すので、外観モードの
     * 切り替えは themeType だけで即座に効く。 */
    theme: diffThemes,
    themeType: theme,
    collapsed,
    /* スクロール中もファイル名を上端に貼り付ける(GitHub の files changed と同じ)。
     * ヘッダは file ごとの shadow root 内で sticky になるので、次の file に
     * 入れ替わる */
    stickyHeader: true,
    /* 長い行は横スクロールさせず折り返す。狭いコンパクト表示で必須で、全画面でも
     * file ごとの横スクロールバーが消えて読みやすい。 */
    overflow: "wrap" as const,
    diffStyle: stack ? ("unified" as const) : ("split" as const),
    unsafeCSS: stack ? STACK_CSS : SPLIT_CSS,
    tokenizeMaxLineLength: TOKENIZE_MAX_LINE_LENGTH,
    maxLineDiffLength: TOKENIZE_MAX_LINE_LENGTH,
    /* 内容量が多すぎる file は highlight / 行内 word 差分を切る。どちらも描画範囲
     * ではなく file 全体に走る処理なので仮想化では有界にならない(diff.ts 冒頭)。 */
    ...(highlight ? {} : { tokenizeMaxLength: TOKENIZE_MAX_LENGTH_PLAIN }),
    ...(inlineDiff ? {} : { lineDiffType: LINE_DIFF_TYPE_PLAIN }),
  };
}

/* file 1 つぶんの描画。
 *
 * memo にするのは、確認済みや折りたたみの操作で 500 file 全部を描き直さないため
 * (ライブラリの FileDiff は非 memo なので、素通しすると全 file の
 * setOptions/render が走る)。props は primitive に寄せてある。 */
export const DiffFileRow = memo(function DiffFileRow({
  index,
  entry,
  theme,
  diffThemes,
  stack,
  collapsed,
  viewed,
  viewable,
  registerHost,
  onToggle,
  onToggleViewed,
}: {
  index: number;
  entry: DiffFilePlan;
  theme: Theme;
  diffThemes: { light: string; dark: string };
  /* 縦積み(unified)にするか。狭いパネルで左右 2 面に割ると 1 面が読めないので
   * 削除と追加を縦に積む。options 経由なので file は作り直さない。 */
  stack: boolean;
  collapsed: boolean;
  viewed: boolean;
  viewable: boolean;
  registerHost: (index: number, el: HTMLDivElement | null) => void;
  onToggle: (index: number) => void;
  onToggleViewed: (index: number) => void;
}) {
  /* memo 境界の内側で locale を購読する。props はロケールに依存しないので、
   * これが無いと言語を切り替えても行数ラベルとボタン名が古いまま残る。 */
  const { i18n } = useLingui();
  const { file, lines } = entry;
  const lineText = lines.toLocaleString(i18n.locale);
  return (
    <div
      className="diff-file"
      /* data-collapsed は飾りではない — useDiffScroller の空白判定が、行が
         無くて当然の file をこれで除外する。属性を足すならそこまで見ること。
         data-index は確認済みを付けたあとの焦点の行き先(DiffBody)。 */
      data-collapsed={collapsed ? "" : undefined}
      data-index={index}
      ref={(el) => registerHost(index, el)}
    >
      {/* key を付けて作り直さないこと — shadow root に前の placeholder /
          buffer が残り、file の高さが倍になる(useDiffNudge を参照) */}
      <FileDiff
        fileDiff={file}
        options={fileDiffOptions({ entry, theme, diffThemes, stack, collapsed })}
        renderHeaderMetadata={() => (
          <FileActions
            name={file.name}
            lineText={lineText}
            collapsed={collapsed}
            viewed={viewed}
            viewable={viewable}
            onToggle={() => onToggle(index)}
            onToggleViewed={() => onToggleViewed(index)}
          />
        )}
      />
    </div>
  );
});
