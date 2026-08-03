import { useEffect, type RefObject } from "react";

/* Tab で移動できる要素。disabled と tabindex="-1" は除く。 */
const FOCUSABLE = [
  "a[href]",
  "button:not([disabled])",
  "input:not([disabled])",
  "select:not([disabled])",
  "textarea:not([disabled])",
  "summary",
  '[tabindex]:not([tabindex="-1"])',
].join(",");

/* 実際に Tab で到達できるか。
 *
 * 非表示の要素を境界にすると、最後に「見えている」要素から Tab を押しても
 * current === last が成立せず、そのままブラウザ UI へ抜ける。diff ビュアーの
 * サイドバーは container query で display:none になるので、実際に起こる。
 *
 * 可視性は checkVisibility に聞く。offsetParent や getClientRects は position:
 * fixed やレイアウトを持たない環境で当てにならない。jsdom は checkVisibility を
 * 実装しないので、答えられない環境では弾かない(全要素が消えるほうが害が大きい)。 */
function isVisible(el: HTMLElement): boolean {
  return typeof el.checkVisibility !== "function" || el.checkVisibility();
}

/* aria-modal なコンテナの中でフォーカスを循環させる。
 *
 * 背面を inert にしても、末尾から Tab / 先頭から Shift+Tab を押すとフォーカスは
 * ブラウザ UI(アドレスバー等)へ抜ける。モーダルを名乗る以上は自前で折り返す。 */
export function useFocusTrap(ref: RefObject<HTMLElement | null>, active: boolean) {
  useEffect(() => {
    if (!active) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key !== "Tab" || e.defaultPrevented) return;
      const root = ref.current;
      if (!root) return;
      const items = [...root.querySelectorAll<HTMLElement>(FOCUSABLE)].filter(isVisible);
      const first = items.at(0);
      const last = items.at(-1);
      if (!first || !last) {
        e.preventDefault();
        root.focus();
        return;
      }
      const current = document.activeElement;
      const inside = current instanceof Node && root.contains(current);
      if (e.shiftKey && (!inside || current === first || current === root)) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && (!inside || current === last)) {
        e.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", onKeyDown, true);
    return () => document.removeEventListener("keydown", onKeyDown, true);
  }, [ref, active]);
}
