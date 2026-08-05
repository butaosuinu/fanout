import { useEffect, useState } from "react";

/* ビューポート幅を追う。resize イベントだけで足りる用途に使う —
 * ResizeObserver はタブが非表示のあいだ配信が止まるので、レイアウト判断の
 * 唯一の入力にはしない。 */
export function useViewportWidth(): number {
  const [width, setWidth] = useState(() => window.innerWidth);
  useEffect(() => {
    const onResize = () => setWidth(window.innerWidth);
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, []);
  return width;
}
