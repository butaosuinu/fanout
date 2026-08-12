import { Trans, useLingui } from "@lingui/react/macro";
import { Virtualizer } from "@pierre/diffs/react";
import {
  memo,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type RefObject,
} from "react";
import { createPortal } from "react-dom";
import { useDiff } from "../../transport/useDiff";
import { useDiffAnchorSync } from "./useDiffAnchorSync";
import { useDiffCollapse } from "./useDiffCollapse";
import { useDiffOverlayModal, useEscapeToClose } from "./useDiffOverlayModal";
import { useDiffScrolling } from "./useDiffScroller";
import { useDiffWidth } from "./useDiffWidth";
import { useViewportWidth } from "../../shared/useViewportWidth";
import {
  useDiffHideViewed,
  useDiffLayout,
  useDiffTheme,
  useDiffView,
  useTheme,
  type Theme,
} from "../settings/useSettings";
import { apiUrl } from "../../transport/api";
import { coversBackground, panelWidthFor, COMPACT_FULL_WIDTH_PX } from "./diffView";
import { diffMeta, diffWarning, type DiffFilePlan } from "./diff";
import { useDiffPatch } from "./useDiffPatch";
import { useDiffViewed } from "./useDiffViewed";
import { indicesForPaths } from "./viewed";
import { useStableCallback } from "../../shared/useStableCallback";
import { DiffFileList } from "./DiffFileList";
import { DiffFileRow } from "./DiffFileRow";
import { DiffOmittedNote } from "./DiffOmittedNote";
import { DiffToolbar } from "./DiffToolbar";

/* patch 本文と file path は敵性入力。@pierre/diffs はテキストをトークン分解して
 * DOM API で組み立てる(patch を HTML として解釈しない)前提で採用しており、
 * その性質は diffOverlay.test.tsx の敵性 patch テストで固定している。
 * こちら側でも patch 由来の文字列を dangerouslySetInnerHTML に渡さない。 */

const NO_INDICES: ReadonlySet<number> = new Set();

/* 「確認済みを隠す」中にチェックを入れると、その行ごと unmount されてフォーカスが
 * body へ落ち、トラップの外に出る。次に読む file は残っているうちの先頭なので、
 * そのチェックへ渡す(無ければオーバーレイ自身が引き取る)。DOM は commit 後に
 * 入れ替わるので次フレームで探す。checkbox は shadow root へ slot されるが、実体は
 * .diff-file の light DOM の子なので querySelector で届く。 */
function useRefocusAfterHide(rootRef: RefObject<HTMLElement | null>): () => void {
  return useCallback(() => {
    requestAnimationFrame(() => {
      const root = rootRef.current;
      if (!root) return;
      const next = root.querySelector<HTMLElement>(".diff-file .diff-file-viewed input");
      (next ?? root).focus({ preventScroll: true });
    });
  }, [rootRef]);
}

/* 画面外に先読みしておく高さ。既定の 1,000px は 1 フレームで消費されてしまい、
 * 高速スクロール中に描画が追いつかない。 */
const VIRTUALIZER_CONFIG = { overscrollSize: 3000 };

/* auto レイアウトが左右 2 面(split)を選ぶ最小のパネル幅。ファイル一覧(288px)を
 * 引いた残りを半分に割っても 1 面 350px 前後は残る値。これを下回ると縦積みへ。 */
const AUTO_SPLIT_MIN_PX = 1000;

/* memo: Drawer は SSE snapshot tick(約 2s)ごとに再レンダーされるが、diff と
 * theme は変わらないので file 列全体をスキップさせる(library の FileDiff は
 * 非 memo で、素通しすると tick ごとに全 file の setOptions/render が走る)。
 * 折りたたみと確認済みの操作は Map / Set の差し替えで伝わり、実際に描き直すのは
 * 触った file だけ(DiffFileRow が memo なので)。 */
const DiffFiles = memo(function DiffFiles({
  plan,
  isCollapsed,
  viewed,
  viewable,
  hidden,
  theme,
  diffThemes,
  stack,
  registerHost,
  onToggle,
  onToggleViewed,
}: {
  plan: DiffFilePlan[];
  /* 畳まれているかの判定は useDiffCollapse が持つ(同じ式を 2 箇所に書かない) */
  isCollapsed: (index: number) => boolean;
  viewed: ReadonlySet<number>;
  /* チェックを出せる index。identity が曖昧な path は持てない(viewed.ts) */
  viewable: ReadonlySet<number>;
  /* 「確認済みを隠す」で本文から降ろす index。plan の index は詰めない —
   * 折りたたみ・host 登録・飛び先の索引がすべて index を key にしているため。 */
  hidden: ReadonlySet<number>;
  theme: Theme;
  diffThemes: { light: string; dark: string };
  stack: boolean;
  registerHost: (index: number, el: HTMLDivElement | null) => void;
  onToggle: (index: number) => void;
  onToggleViewed: (index: number) => void;
}) {
  /* patch を持たない file(バイナリ・上限で省略)は plan に入らないので、
   * 「すべて」とは言わない — 警告帯と DiffOmittedNote がまだ残件を出している。 */
  if (plan.length > 0 && hidden.size === plan.length) {
    return (
      <div className="diff-note">
        <Trans>patch のあるファイルはすべて確認済みです</Trans>
      </div>
    );
  }
  return (
    <>
      {plan.map((entry, i) =>
        hidden.has(i) ? null : (
          <DiffFileRow
            // file type change は同 path が 2 entry になるため path 単独を key にしない
            key={`${i}:${entry.file.name}`}
            index={i}
            entry={entry}
            theme={theme}
            diffThemes={diffThemes}
            stack={stack}
            collapsed={isCollapsed(i)}
            viewed={viewed.has(i)}
            viewable={viewable.has(i)}
            registerHost={registerHost}
            onToggle={onToggle}
            onToggleViewed={onToggleViewed}
          />
        ),
      )}
    </>
  );
});

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
  const { hideViewed } = useDiffHideViewed();
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
  const refocusAfterHide = useRefocusAfterHide(rootRef);
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
  const patch = diff?.patch ?? "";
  /* snapshot tick ごとの再レンダーで files 全走査をやり直さない */
  const view = useMemo(
    () => (diff ? { warning: diffWarning(diff), meta: diffMeta(diff) } : null),
    [diff],
  );
  const { plan, byPath, selectable, kinds, paths, fingerprints } = useDiffPatch(patch);
  const { viewedPaths, setViewed } = useDiffViewed(scopeKey, fingerprints);
  const viewed = useMemo(() => indicesForPaths(paths, viewedPaths), [paths, viewedPaths]);
  /* チェックを出せる file。identity が曖昧な path は fingerprint を持たない
   * (viewed.ts の sameFileGroup)ので、チェックしても何も起きない = 出さない。 */
  const viewable = useMemo(() => indicesForPaths(paths, fingerprints), [paths, fingerprints]);
  /* 隠すのは描画から降ろすだけで、plan の index は詰めない(DiffFiles を参照)。 */
  const hidden = hideViewed ? viewed : NO_INDICES;
  /* 折りたたみの上書きが効く範囲。patch だけで区切ると、同じ worktree を指す行
   * (attached-agent など)は patch が一致するので、行を切り替えても前の行の
   * 上書きが残り、確認済みで復元した file が開いたままになる。 */
  const collapseScope = `${scopeKey}\n${patch}`;
  const { isCollapsed, onToggle, onExpandAll, onCollapseAll, expand, setCollapsed } =
    useDiffCollapse(collapseScope, plan, viewed);
  /* 確認済みの結果としての折りたたみは、上書きを**消す**ことで表す(付ける側も
   * 外す側も)。畳むかどうかは `collapsedAt` が確認済みから導くので、上書きを書く
   * 必要が無い。
   *
   * `true` を書いてはいけない。上書きは確認済みより優先するので、別タブで外された
   * ときにチェックだけ外れて本文が畳まれたまま残る。`false` も書けない — 1,000 行超で
   * 既定折りたたみだった file が、開いたことすら無い状態から全開になる。
   * 消すだけなら、明示的に展開済み(上書き `false`)の file を確認済みにしたときも
   * ちゃんと畳まれる。
   * file type change は同 path が 2 entry になるため、path の全 index に及ぼす。 */
  const onToggleViewed = useStableCallback((i: number) => {
    const path = paths[i];
    if (path === undefined) return;
    const next = !viewedPaths.has(path);
    setViewed(path, next);
    for (const j of byPath.get(path) ?? []) setCollapsed(j, null);
    if (next && hideViewed) refocusAfterHide();
  });
  /* 行が消える理由はローカル操作だけではない — 別タブが同じ scope でチェックすると
   * storage 経由でここでも消える。呼び出し点が無いので、隠れる集合が動いたあとに
   * 焦点が落ちていたら拾い直す。覆っているあいだだけ効かせる(コンパクトで背面を
   * 触っている人からフォーカスを奪わない)。 */
  useEffect(() => {
    if (covering && document.activeElement === document.body) refocusAfterHide();
  }, [covering, hidden, refocusAfterHide]);

  /* auto は本文領域の幅で決め、split / stack はユーザーの明示指定をそのまま使う。
   * ヘッダと本文は縦に積むだけなので、本文領域の幅 = パネルの幅(diffView.ts)。 */
  const panelWidth = panelWidthFor({ view: viewMode, viewportWidth, compactWidth });
  const stack = layout === "auto" ? panelWidth < AUTO_SPLIT_MIN_PX : layout === "stack";
  /* 隠す / 出すも全 file の高さを変えるので、並べ方の切替と同じく取り直させる。 */
  const { registerHost, onSelectFile } = useDiffScrolling({
    rootRef,
    byPath,
    expand,
    patch,
    layoutKey: `${viewMode}:${stack}:${hidden.size}`,
  });

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
      {diff &&
        (diff.files.length === 0 ? (
          <div className="diff-note">
            <Trans>merge-base からの変更はありません</Trans>
          </div>
        ) : (
          <div className="diff-main">
            <DiffFileList
              files={diff.files}
              selectable={selectable}
              kinds={kinds}
              viewedPaths={viewedPaths}
              viewableCount={fingerprints.size}
              hideViewed={hideViewed}
              onSelect={onSelectFile}
              onExpandAll={onExpandAll}
              onCollapseAll={onCollapseAll}
            />
            {/* Virtualizer の root がスクロールコンテナ、content が中身。画面外の
                file は高さだけ確保した placeholder になる。 */}
            <Virtualizer
              className="diff-body"
              contentClassName="diff-files"
              config={VIRTUALIZER_CONFIG}
            >
              <DiffFiles
                plan={plan}
                isCollapsed={isCollapsed}
                viewed={viewed}
                viewable={viewable}
                hidden={hidden}
                theme={theme}
                diffThemes={diffThemes}
                stack={stack}
                registerHost={registerHost}
                onToggle={onToggle}
                onToggleViewed={onToggleViewed}
              />
            </Virtualizer>
          </div>
        ))}
    </div>,
    document.body,
  );
}
