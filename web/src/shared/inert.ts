/* 背面を inert で遮る所有権の管理。
 *
 * diff オーバーレイと設定モーダルは重なって開くことがあり、同じ #root を
 * 両方が遮りたい。素朴に「mount で付けて unmount で外す」にすると、後から
 * 開いたほうが閉じたときに前から開いていたほうの inert まで外れる。逆に、
 * 先に開いていたほうが先に unmount されても外れてはいけない。
 * どちらの順序でも壊れないよう、要素ごとの参照数で持つ。 */
const counts = new WeakMap<HTMLElement, number>();

/* 渡した要素を inert にし、解除する関数を返す。null は無視する(呼び出し側で
 * getElementById の結果をそのまま渡せるように)。 */
export function blockBackground(elements: (HTMLElement | null)[]): () => void {
  const owned = elements.filter((el): el is HTMLElement => el != null);
  for (const el of owned) {
    const next = (counts.get(el) ?? 0) + 1;
    counts.set(el, next);
    if (next === 1) el.setAttribute("inert", "");
  }
  let released = false;
  return () => {
    if (released) return; // 二重解除で他の所有者の分を減らさない
    released = true;
    for (const el of owned) {
      const next = (counts.get(el) ?? 1) - 1;
      if (next <= 0) {
        counts.delete(el);
        el.removeAttribute("inert");
      } else {
        counts.set(el, next);
      }
    }
  };
}
