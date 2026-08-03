import { usePanelWidth, type PanelGripProps } from "./usePanelWidth";

export const DIFF_DEFAULT_WIDTH = 760;
export const DIFF_MIN_WIDTH = 360;
// 実効上限はビューポート由来(下の RATIO)。ここは保存値の健全範囲を守るだけの
// 静的な歯止めで、どんなモニタでも先に効かない大きさにしておく。
export const DIFF_MAX_WIDTH = 6000;

/* ビューポートに対して広げられる上限。全画面ではなく「パネル」だと分かる帯を
 * 右端に残す。 */
const VIEWPORT_RATIO = 0.95;

/* コンパクト表示の diff パネル幅。右端は詳細ドロワーの左端(--diff-anchor-right)
 * に貼り付くが、そこを越えて広げたぶんはドロワーに被さっていく(CSS 側で
 * right = min(ドロワー左端, 100vw - 幅) にしてある)。ここでの上限は
 * ビューポート幅だけで決まるので、ドロワーが開いていても 95% まで広げられる。 */
export function useDiffWidth(): { width: number; gripProps: PanelGripProps } {
  return usePanelWidth({
    storageKey: "fanout.diffWidth",
    defaultWidth: DIFF_DEFAULT_WIDTH,
    minWidth: DIFF_MIN_WIDTH,
    maxWidth: DIFF_MAX_WIDTH,
    viewportMax: () => window.innerWidth * VIEWPORT_RATIO,
    label: "diff パネルの幅を変更",
    resizingClass: "diff-resizing",
  });
}
