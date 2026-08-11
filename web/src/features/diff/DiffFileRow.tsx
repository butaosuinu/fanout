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

/* shadow DOM 内はライブラリの unsafeCSS からしか触れない。入れるのはこの固定文字列
 * だけで、patch 由来の値は一切混ぜない(敵性入力規約)。
 * split / stack それぞれの完成品を定数で持つ — render ごとに組み立てると文字列が
 * 変わったと見なされ、ライブラリが file を丸ごと描き直す。
 * 片側寄せは split のときだけ。stack も data-diff-type="single" になるので、
 * 渡したままだと本文が半分幅に潰れる。 */
const SPLIT_CSS = `${HEADER_EDGE_CSS}\n${SINGLE_SIDE_CSS}`;
const STACK_CSS = HEADER_EDGE_CSS;

/* ファイル名ヘッダの右側に並ぶ操作。ラベルに file 名を入れるのは、入れないと
 * 全 file のボタンとチェックが同名で並び、支援技術の要素一覧から区別できないため。 */
function FileActions({
  name,
  lineText,
  collapsed,
  viewed,
  onToggle,
  onToggleViewed,
}: {
  name: string;
  lineText: string;
  collapsed: boolean;
  viewed: boolean;
  onToggle: () => void;
  onToggleViewed: () => void;
}) {
  const { t } = useLingui();
  return (
    <span className="diff-file-acts">
      {/* 行数は「大きい file だ」という情報なので、畳んでいる間はボタンの外に
          テキストで残す */}
      {collapsed && <span className="diff-file-lines">{t`${{ lines: lineText }} 行`}</span>}
      <label className="diff-file-viewed">
        <input
          type="checkbox"
          checked={viewed}
          aria-label={t`${{ name }} — 確認済み`}
          onChange={onToggleViewed}
        />
        <span aria-hidden="true">{t`確認済み`}</span>
      </label>
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
  registerHost: (index: number, el: HTMLDivElement | null) => void;
  onToggle: (index: number) => void;
  onToggleViewed: (index: number) => void;
}) {
  /* memo 境界の内側で locale を購読する。props はロケールに依存しないので、
   * これが無いと言語を切り替えても行数ラベルとボタン名が古いまま残る。 */
  const { i18n } = useLingui();
  const { file, lines, highlight, inlineDiff } = entry;
  const lineText = lines.toLocaleString(i18n.locale);
  return (
    <div
      className="diff-file"
      data-collapsed={collapsed ? "" : undefined}
      data-viewed={viewed ? "" : undefined}
      ref={(el) => registerHost(index, el)}
    >
      {/* key を付けて作り直さないこと — shadow root に前の placeholder /
          buffer が残り、file の高さが倍になる(useDiffNudge を参照) */}
      <FileDiff
        fileDiff={file}
        options={{
          /* light/dark 両方を渡すとライブラリが両方の CSS 変数を出すので、
           * 外観モードの切り替えは themeType だけで即座に効く。 */
          theme: diffThemes,
          themeType: theme,
          collapsed,
          /* スクロール中もファイル名を上端に貼り付ける(GitHub の files changed と
           * 同じ)。ヘッダは file ごとの shadow root 内で sticky になるので、
           * 次の file に入れ替わる */
          stickyHeader: true,
          /* 長い行は横スクロールさせず折り返す。狭いコンパクト表示で必須で、
           * 全画面でも file ごとの横スクロールバーが消えて読みやすい。 */
          overflow: "wrap",
          diffStyle: stack ? "unified" : "split",
          unsafeCSS: stack ? STACK_CSS : SPLIT_CSS,
          tokenizeMaxLineLength: TOKENIZE_MAX_LINE_LENGTH,
          maxLineDiffLength: TOKENIZE_MAX_LINE_LENGTH,
          /* 内容量が多すぎる file は highlight / 行内 word 差分を切る。どちらも
           * 描画範囲ではなく file 全体に走る処理なので仮想化では有界にならない
           * (diff.ts 冒頭のコメントを参照)。 */
          ...(highlight ? {} : { tokenizeMaxLength: TOKENIZE_MAX_LENGTH_PLAIN }),
          ...(inlineDiff ? {} : { lineDiffType: LINE_DIFF_TYPE_PLAIN }),
        }}
        renderHeaderMetadata={() => (
          <FileActions
            name={file.name}
            lineText={lineText}
            collapsed={collapsed}
            viewed={viewed}
            onToggle={() => onToggle(index)}
            onToggleViewed={() => onToggleViewed(index)}
          />
        )}
      />
    </div>
  );
});
