import { Trans, useLingui } from "@lingui/react/macro";
import { useMemo, useRef, useState, type CSSProperties } from "react";
import { createPortal } from "react-dom";
import { useDiff } from "../../transport/useDiff";
import { useDiffAnchorSync } from "./useDiffAnchorSync";
import { useDiffOverlayModal, useEscapeToClose } from "./useDiffOverlayModal";
import { useDiffWidth } from "./useDiffWidth";
import { useViewportWidth } from "../../shared/useViewportWidth";
import { useDiffLayout, useDiffTheme, useDiffView, useTheme } from "../settings/useSettings";
import { apiUrl } from "../../transport/api";
import { coversBackground, panelWidthFor, COMPACT_FULL_WIDTH_PX } from "./diffView";
import { diffMeta, diffWarning } from "./diff";
import { DiffBody } from "./DiffBody";
import { DiffOmittedNote } from "./DiffOmittedNote";
import { DiffToolbar } from "./DiffToolbar";

/* auto レイアウトが左右 2 面(split)を選ぶ最小のパネル幅。ファイル一覧(288px)を
 * 引いた残りを半分に割っても 1 面 350px 前後は残る値。これを下回ると縦積みへ。 */
const AUTO_SPLIT_MIN_PX = 1000;

export function DiffOverlay({
  title,
  query,
  token,
  scopeKey,
  anchorKey = null,
  suppressed = false,
  escapeEnabled = true,
  onCoveringChange,
  onOpenSettings,
  onClose,
}: {
  title: string;
  query: Record<string, string>;
  token: string;
  /* 確認済みの保存単位。session 行の rowKey なので、別の行の diff を開いても
   * 混ざらない(App の diffTarget.key と同じ値)。 */
  scopeKey: string;
  /* コンパクト表示の左端を決める #drawer を取り直す合図。ドロワーは
   * key={selected} で作り直されるので、選択が変わると要素の実体も変わる。 */
  anchorKey?: string | null;
  /* 上に設定モーダルが重なっている。自分を inert にし、mount 時の focus 奪取も
   * やめる — lazy chunk の解決が設定を開いた後になると、設定側からは要素が
   * 見えず遮れないため、こちらが自分で降りる。 */
  suppressed?: boolean;
  /* 設定モーダルが上に重なっている間は Escape を譲る(下の diff は開いたまま) */
  escapeEnabled?: boolean;
  /* 背面を覆っているかの通知。App は隠れた peek のポーリング停止に使う */
  onCoveringChange?: (covering: boolean) => void;
  /* 全画面表示中は Nav が inert なので、テーマ設定への入口をここにも置く */
  onOpenSettings: () => void;
  onClose: () => void;
}) {
  const { i18n, t } = useLingui();
  const { theme } = useTheme();
  const { light, dark } = useDiffTheme();
  const { view: viewMode } = useDiffView();
  const { layout } = useDiffLayout();
  /* auto レイアウトと covering 判定に使うビューポート幅。ResizeObserver は使わない
   * — タブが非表示のあいだ配信が止まるうえ、パネル幅はここで計算できる。 */
  const viewportWidth = useViewportWidth();
  /* パネル右端の位置(= ドロワーの左端)と、背面コンテンツの左端。実寸で覆って
   * いるかを判定するため state で持つ。ドロワーは幅可変で、選択が無ければ存在
   * しない。コンテンツ左端はドロワー幅に連動して動く(main-col が縮む)。
   * 実測は下の useDiffAnchorSync が行う(呼ぶ位置に意味がある)。 */
  const [anchorRight, setAnchorRight] = useState(0);
  const [contentLeft, setContentLeft] = useState(0);
  const diffThemes = useMemo(() => ({ light, dark }), [light, dark]);
  const { state, refetch } = useDiff(apiUrl("/api/diff", token, query));
  const rootRef = useRef<HTMLDivElement>(null);
  const { width: compactWidth, gripProps } = useDiffWidth();
  /* 背面を覆っているならモーダル。全画面はもちろん、狭い帯や、ドロワーが広くて
   * パネルが一覧を食い尽くす配置でも覆う。覆っているのに非モーダルだと、見えない
   * 背面へ Tab が抜け、隠れた peek のポーリングも続く(判定は diffView.ts)。 */
  const covering = coversBackground({
    view: viewMode,
    viewportWidth,
    anchorRight,
    width: compactWidth,
    contentLeft,
  });
  useDiffOverlayModal(rootRef, { covering, suppressed, onCoveringChange });
  useDiffAnchorSync({ view: viewMode, anchorKey, setAnchorRight, setContentLeft });
  useEscapeToClose(rootRef, { covering, enabled: escapeEnabled, onClose });

  const diff = state.phase === "ready" ? state.diff : null;
  /* snapshot tick ごとの再レンダーで files 全走査をやり直さない */
  const view = useMemo(
    () => (diff ? { warning: diffWarning(diff), meta: diffMeta(diff) } : null),
    [diff],
  );
  /* auto は本文領域の幅で決め、split / stack はユーザーの明示指定をそのまま使う。
   * ヘッダと本文は縦に積むだけなので、本文領域の幅 = パネルの幅(diffView.ts)。 */
  const panelWidth = panelWidthFor({ view: viewMode, viewportWidth, compactWidth });
  const stack = layout === "auto" ? panelWidth < AUTO_SPLIT_MIN_PX : layout === "stack";
  /* #drawer(sticky/fixed)はスタッキングコンテキストを作るため、その子として
   * 描くと z-index が閉じ込められ nav の下に潜る。portal で body 直下に出す。 */
  return createPortal(
    <div
      className="diff-overlay"
      id="diff-overlay"
      role={covering ? "dialog" : "complementary"}
      aria-modal={covering || undefined}
      aria-label={t`worktree diff`}
      data-theme={theme}
      data-mode={viewMode}
      data-layout={stack ? "stack" : "split"}
      /* 幅は CSS 変数だけで制御する(inline width にすると狭い画面の media query が
         cascade で勝てなくなる — ドロワーと同じ理由)。全画面では使われない。 */
      style={
        {
          "--diff-w": `${compactWidth}px`,
          "--diff-anchor-right": `${anchorRight}px`,
        } as CSSProperties
      }
      ref={rootRef}
      tabIndex={-1}
    >
      {/* role / aria / tabIndex は hook が幅と一体で提供する(スプレッド漏れで
          セパレータ意味論だけ落ちる事故を防ぐ)。狭い帯ではコンパクトも CSS で
          全幅パネルになり幅を変えられないので、グリップ自体を出さない。 */}
      {viewMode === "compact" && viewportWidth > COMPACT_FULL_WIDTH_PX && (
        <div className="diff-grip" {...gripProps} />
      )}
      <DiffToolbar
        title={title}
        branches={diff ? { head: diff.branchName, base: diff.baseBranch } : null}
        meta={view?.meta ?? ""}
        loading={state.phase === "loading"}
        stack={stack}
        onRefetch={refetch}
        onOpenSettings={onOpenSettings}
        onClose={onClose}
      />
      {view?.warning && (
        <div className="diff-banner" role="status">
          {i18n._(view.warning)}
        </div>
      )}
      {diff && <DiffOmittedNote files={diff.files} />}
      {state.phase === "loading" && (
        <div className="diff-note">
          <Trans>diff を取得中…(最大 10 秒)</Trans>
        </div>
      )}
      {state.phase === "error" && (
        <div className="diff-note diff-error" role="alert">
          {i18n._(state.message)}
        </div>
      )}
      {diff && (
        <DiffBody
          diff={diff}
          scopeKey={scopeKey}
          theme={theme}
          diffThemes={diffThemes}
          stack={stack}
          covering={covering}
          rootRef={rootRef}
          layoutKey={`${viewMode}:${stack}`}
        />
      )}
    </div>,
    document.body,
  );
}
