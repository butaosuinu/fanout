import type { DiffView } from "../hooks/useSettings";

/* これ以下のビューポートでは、コンパクト表示も nav 下の全幅パネルになる
 * (style.css の @media (max-width:1100px) と同期させること)。 */
export const COMPACT_FULL_WIDTH_PX = 1100;

/* diff ビュアーが背面(一覧とドロワー)を覆っているか。
 *
 * 全画面は当然として、狭い帯ではコンパクトも CSS が全幅パネルにするので背面は
 * 一切見えない。覆っているかどうかは 2 か所の判断に効く:
 * - DiffOverlay: モーダルにして背面を inert にするか(見えない要素へ Tab が
 *   抜けないように)
 * - App: 見えない peek の 5 秒ポーリングを止めるか
 * 両方が同じ答えを出す必要があるので、判定はここに 1 つだけ置く。 */
export function isDiffCovering(view: DiffView, viewportWidth: number): boolean {
  return view === "full" || viewportWidth <= COMPACT_FULL_WIDTH_PX;
}
