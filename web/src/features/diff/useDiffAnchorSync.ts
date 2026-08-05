import { useEffect, type Dispatch, type SetStateAction } from "react";
import type { DiffView } from "../settings/useSettings";

/* 幅 0 の矩形はレイアウトが無い(jsdom)か実際に場所を取っていないので、要素が
 * 無いのと同じ扱いにする。0 を左端として採ると、パネルが画面を覆い尽くしていると
 * 誤判定する。 */
function laidOutRect(el: HTMLElement | null): DOMRect | null {
  const rect = el?.getBoundingClientRect();
  return rect && rect.width > 0 ? rect : null;
}

/* 要素のリサイズとウィンドウのリサイズを 1 つの合図にまとめ、解除する関数を返す。
 * ResizeObserver だけでは足りない — 要素の大きさが変わらないまま位置だけ動く
 * (ウィンドウ幅の変化)ケースを拾えない。 */
function watchLayout(targets: (HTMLElement | null)[], onChange: () => void): () => void {
  const ro = new ResizeObserver(onChange);
  for (const el of targets) {
    if (el) ro.observe(el);
  }
  window.addEventListener("resize", onChange);
  return () => {
    ro.disconnect();
    window.removeEventListener("resize", onChange);
  };
}

/* コンパクト表示の右端を詳細ドロワーの左端に合わせる。ドロワーは幅可変
 * (--drawer-w のドラッグ)かつ選択が無ければ存在しないので実測する。
 * ドロワーが無いときはビューポート右端に寄せる。
 *
 * 背面コンテンツの左端も同じ合図で取り直す — ドロワーを広げると main-col が
 * 縮み、`.wrap` の中央寄せぶん左端も動く。
 *
 * 値を state で持たず setter を受け取るのは、実測する位置を動かせないため。
 * covering の判定にはレンダー開始時点で値が要る一方、実測自体は背面の document
 * スクロールを止めた後(= モーダル副作用の後、この hook の呼び出し位置)でなければ、
 * スクロールバーの有無だけずれた値を掴む。 */
export function useDiffAnchorSync({
  view,
  anchorKey,
  setAnchorRight,
  setContentLeft,
}: {
  view: DiffView;
  /* 実測をやり直す合図。ドロワーは key={selected} で作り直されるので、選択が
   * 変わると要素の実体も変わる。 */
  anchorKey: string | null;
  setAnchorRight: Dispatch<SetStateAction<number>>;
  setContentLeft: Dispatch<SetStateAction<number>>;
}) {
  useEffect(() => {
    if (view !== "compact") return;
    const drawer = document.getElementById("drawer");
    const content = document.getElementById("content");
    const sync = () => {
      const left = laidOutRect(drawer)?.left ?? window.innerWidth;
      setAnchorRight(Math.max(0, Math.round(window.innerWidth - left)));
      /* コンテンツ側も幅 0 は実測できなかった扱い。0 に倒すと「帯が 1px でも
       * あれば覆っていない」という以前の判定に戻るだけで、覆っていないものを
       * 覆ったと誤判定はしない。 */
      const rect = laidOutRect(content);
      setContentLeft(rect ? Math.max(0, Math.round(rect.left)) : 0);
    };
    sync();
    return watchLayout([drawer, content], sync);
  }, [anchorKey, setAnchorRight, setContentLeft, view]);
}
