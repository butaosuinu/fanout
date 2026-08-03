import { usePanelWidth, type PanelGripProps } from "./usePanelWidth";

export const DRAWER_DEFAULT_WIDTH = 840;
export const DRAWER_MIN_WIDTH = 320;
// ドロワーはコンテンツ領域(style.css の --maxw)より広げない。これより広い
// グリッドコンテナはそもそも無く、main 列を不当に潰すだけなので静的上限を
// コンテンツ幅に合わせる。--maxw を変えたらここも合わせること。
export const DRAWER_MAX_WIDTH = 1416;

// main 列に最低限残す幅(デスクトップ grid 時)。CSS の calc(100vw - 360px) と同期。
const MAIN_MIN = 360;

/* 現在のビューポートで実際に描画できる最大幅。CSS の clamp と必ず一致させる:
 * ≤1100px は overlay(グリッド外)なので 90vw、それ以上は grid 内なので main 列に
 * MAIN_MIN を残しつつコンテンツ幅(DRAWER_MAX_WIDTH)も超えない。 */
function drawerViewportMax(): number {
  const vw = window.innerWidth;
  return vw <= 1100 ? vw * 0.9 : Math.min(vw - MAIN_MIN, DRAWER_MAX_WIDTH);
}

export function useDrawerWidth(): { width: number; gripProps: PanelGripProps } {
  return usePanelWidth({
    storageKey: "fanout.drawerWidth",
    defaultWidth: DRAWER_DEFAULT_WIDTH,
    minWidth: DRAWER_MIN_WIDTH,
    maxWidth: DRAWER_MAX_WIDTH,
    viewportMax: drawerViewportMax,
    label: "詳細パネルの幅を変更",
    resizingClass: "drawer-resizing",
  });
}
