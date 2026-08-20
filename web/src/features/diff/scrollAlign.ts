/* diff 本文のスクロール位置合わせの「計算だけ」。DOM を触る側(useDiffScroller)
 * から切り離してあるのは、jsdom にレイアウトエンジンが無く、実要素を通しては
 * 何も固定できないため — 式と file の選び方はここで単体テストが押さえる。 */

/* 対象の上端を本文スクロール領域の上端へ合わせたときの scrollTop。
 * top は両方ともビューポート基準(getBoundingClientRect)で渡すこと。 */
export function alignedScrollTop({
  scrollTop,
  targetTop,
  scrollerTop,
}: {
  scrollTop: number;
  targetTop: number;
  scrollerTop: number;
}): number {
  return scrollTop + targetTop - scrollerTop;
}

/* 確認済みを付けた直後に本文から降りている index。`setViewed` の結果が `hidden`
 * に出るのは次の render なので、いま隠れている集合にこの path の index を足して
 * 先取りする(file type change は同 path が 2 entry になるので複数入る)。
 * 「確認済みを隠す」が off なら畳まれるだけで本文には残る。 */
export function hiddenAfterViewing({
  hidden,
  hideViewed,
  group,
}: {
  hidden: ReadonlySet<number>;
  hideViewed: boolean;
  group: readonly number[];
}): ReadonlySet<number> {
  if (!hideViewed) return hidden;
  return new Set([...hidden, ...group]);
}

/* 「次の file」を探す条件。from は起点の plan index、count は plan の件数、
 * hidden は本文から降りている index。 */
export interface NextFileTarget {
  from: number;
  count: number;
  hidden: ReadonlySet<number>;
}

/* 上端を合わせにいく次の file。plan の index 順で、本文に残っている最初の後続
 * file。最後の file を確認済みにしたときは null — 送る先が無いので動かさない。 */
export function nextVisibleFileIndex({ from, count, hidden }: NextFileTarget): number | null {
  for (let i = from + 1; i < count; i++) {
    if (!hidden.has(i)) return i;
  }
  return null;
}
