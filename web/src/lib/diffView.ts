import type { DiffView } from "../hooks/useSettings";

/* これ以下のビューポートでは、コンパクト表示も nav 下の全幅パネルになる
 * (style.css の @media (max-width:1100px) と同期させること)。 */
export const COMPACT_FULL_WIDTH_PX = 1100;

export interface DiffPanelGeometry {
  view: DiffView;
  viewportWidth: number;
  /* パネル右端の位置(= 詳細ドロワーの左端からビューポート右端までの距離)。
   * ドロワーが無ければ 0。 */
  anchorRight: number;
  /* コンパクト表示のパネル幅 */
  width: number;
}

/* diff ビュアーが背面(Session 一覧)を覆い尽くしているか。
 *
 * 覆っているかどうかは 2 か所の判断に効く:
 * - DiffOverlay: モーダルにして背面を inert にするか(見えない要素へ Tab が
 *   抜けないように)
 * - App: 見えない peek の 5 秒ポーリングを止めるか
 * 両方が同じ答えを出す必要があるので、判定はここに 1 つだけ置く。
 *
 * モードだけでは決まらない。パネルは右端がドロワーの左端に貼り付き、そこを
 * 越えた分は左へ伸びるので、狭い画面 + 広いドロワー + 広いパネルでは
 * 1,100px を超えていても一覧が 1px も残らないことがある(例: 1,200px の画面で
 * ドロワー 840px・パネル 760px なら右端は 440px、パネルは x=0-760px を占め、
 * 一覧の 0-360px は完全に隠れる)。実寸で見る。 */
export function coversBackground({
  view,
  viewportWidth,
  anchorRight,
  width,
}: DiffPanelGeometry): boolean {
  if (view === "full") return true;
  if (viewportWidth <= COMPACT_FULL_WIDTH_PX) return true; // CSS が全幅パネルにする
  // style.css の right: max(0, min(--diff-anchor-right, 100vw - --diff-w)) と同じ
  const right = Math.max(0, Math.min(anchorRight, viewportWidth - width));
  return viewportWidth - right - width <= 0; // 左に 1px も残らない
}

/* 本文領域(= パネル)の幅。
 *
 * coversBackground と混同しないこと。covering は「一覧が 1px も残らない配置か」で
 * あって幅ではない。ドロワーが広くて一覧が隠れるだけのとき、パネルは compactWidth
 * のままなので、covering でビューポート幅に置き換えると狭い本文を左右 2 面に
 * 割ってしまう。ビューポート幅になるのは全画面と、CSS が全幅パネルにする
 * 1,100px 以下だけ。 */
export function panelWidthFor({
  view,
  viewportWidth,
  compactWidth,
}: {
  view: DiffView;
  viewportWidth: number;
  compactWidth: number;
}): number {
  if (view === "full" || viewportWidth <= COMPACT_FULL_WIDTH_PX) return viewportWidth;
  return compactWidth;
}
