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

/* aria-modal なコンテナの中でフォーカスを循環させる。
 *
 * 背面を inert にしても、末尾から Tab / 先頭から Shift+Tab を押すとフォーカスは
 * ブラウザ UI(アドレスバー等)へ抜ける。モーダルを名乗る以上は自前で折り返す。
 *
 * 可視判定はしない — jsdom は offsetParent が常に null で、実要素まで弾いて
 * しまう。ここで扱うモーダルは非表示のフォーカス可能要素を持たない。 */
export function useFocusTrap(ref: RefObject<HTMLElement | null>, active: boolean) {
  useEffect(() => {
    if (!active) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key !== "Tab" || e.defaultPrevented) return;
      const root = ref.current;
      if (!root) return;
      const items = [...root.querySelectorAll<HTMLElement>(FOCUSABLE)];
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
