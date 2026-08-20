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

/* 「次に読む file」を決める材料。 */
export interface NextFileTarget {
  /* いま確認済みにした file の plan index */
  from: number;
  /* plan の件数 */
  count: number;
  /* いま確認済みにした path の plan index 全部。file type change は同じ path が
   * 2 entry になるので、隣がその片割れということがある。 */
  group: readonly number[];
  /* 「確認済みを隠す」で本文から降りている index */
  hidden: ReadonlySet<number>;
}

/* 上端を合わせにいく次の file。plan の index 順で、いま確認済みにした path 自身と、
 * 本文から降りている file を飛ばした最初の後続 file。無ければ null — 最後の file を
 * 確認済みにしたときは送る先が無いので動かさない。
 *
 * group を必ず飛ばすのが肝で、これが `hidden` の 1 render 遅れも兼ねる。`setViewed`
 * の結果が `hidden` に出るのは次の render なので、「確認済みを隠す」が on のときは
 * まだ入っていない。off のときは本文に残るが、たったいま読み終えた file の片割れへ
 * 送るのは誤りなので、どちらでも飛ばすのが正しい。 */
export function nextFileToRead({ from, count, group, hidden }: NextFileTarget): number | null {
  for (let i = from + 1; i < count; i++) {
    if (!hidden.has(i) && !group.includes(i)) return i;
  }
  return null;
}
