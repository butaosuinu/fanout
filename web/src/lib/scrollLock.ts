/* モーダル表示中に背面(document)のスクロールを止める。
 *
 * `position: fixed` のオーバーレイはスクロールコンテナではなく、`inert` も
 * scroll をロックしない。そのため、モーダルの外(backdrop やヘッダ)でのホイールや
 * 内側のスクロールコンテナ端からのチェーンが背面のページを動かし、閉じたときに
 * 読んでいた位置が変わってしまう。
 *
 * diff オーバーレイと設定モーダルは重なって開くので、inert と同じく参照数で持つ
 * (先に開いたほうが先に閉じても解除しない)。 */
let holders = 0;
let restore = "";

export function lockDocumentScroll(): () => void {
  const el = document.documentElement;
  if (holders === 0) {
    restore = el.style.overflow;
    el.style.overflow = "hidden";
  }
  holders++;
  let released = false;
  return () => {
    if (released) return; // 二重解除で他の所有者の分を減らさない
    released = true;
    holders--;
    if (holders === 0) el.style.overflow = restore;
  };
}
