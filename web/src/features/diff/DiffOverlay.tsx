import type { MessageDescriptor } from "@lingui/core";
import { msg } from "@lingui/core/macro";
import { Trans, useLingui } from "@lingui/react/macro";
import { FileDiff, Virtualizer } from "@pierre/diffs/react";
import { memo, useCallback, useMemo, useRef, useState, type CSSProperties } from "react";
import { createPortal } from "react-dom";
import { useDiff } from "../../transport/useDiff";
import { useDiffAnchorSync } from "./useDiffAnchorSync";
import { useDiffCollapse } from "./useDiffCollapse";
import { useDiffOverlayModal, useEscapeToClose } from "./useDiffOverlayModal";
import {
  useBlankRepair,
  useDiffNudge,
  useNudgeOnLayoutChange,
  useScrollToFile,
} from "./useDiffScroller";
import { useDiffWidth } from "./useDiffWidth";
import { useViewportWidth } from "../../shared/useViewportWidth";
import {
  useDiffLayout,
  useDiffTheme,
  useDiffView,
  useTheme,
  type DiffLayout,
  type Theme,
} from "../settings/useSettings";
import { apiUrl } from "../../transport/api";
import { coversBackground, panelWidthFor, COMPACT_FULL_WIDTH_PX } from "./diffView";
import {
  diffMeta,
  diffWarning,
  indexDiffFilesByPath,
  LINE_DIFF_TYPE_PLAIN,
  parseDiffFiles,
  planDiffFiles,
  TOKENIZE_MAX_LENGTH_PLAIN,
  TOKENIZE_MAX_LINE_LENGTH,
  type DiffFilePlan,
} from "./diff";
import { DiffFileList } from "./DiffFileList";
import { DiffOmittedNote } from "./DiffOmittedNote";
import {
  IconButton,
  IconChevronDown,
  IconChevronUp,
  IconClose,
  IconLayoutAuto,
  IconLayoutSplit,
  IconLayoutStack,
  IconMaximize,
  IconMinimize,
  IconRefresh,
  IconTheme,
} from "../../ui/icons";

/* patch 本文と file path は敵性入力。@pierre/diffs はテキストをトークン分解して
 * DOM API で組み立てる(patch を HTML として解釈しない)前提で採用しており、
 * その性質は diffOverlay.test.tsx の敵性 patch テストで固定している。
 * こちら側でも patch 由来の文字列を dangerouslySetInnerHTML に渡さない。 */

/* 画面外に先読みしておく高さ。既定の 1,000px は 1 フレームで消費されてしまい、
 * 高速スクロール中に描画が追いつかない。 */
const VIRTUALIZER_CONFIG = { overscrollSize: 3000 };

/* auto レイアウトが左右 2 面(split)を選ぶ最小のパネル幅。ファイル一覧(288px)を
 * 引いた残りを半分に割っても 1 面 350px 前後は残る値。これを下回ると縦積みへ。 */
const AUTO_SPLIT_MIN_PX = 1000;
/* 並べ方ボタンは 1 個で auto -> split -> stack を巡回する */
const LAYOUT_CYCLE: Record<DiffLayout, DiffLayout> = {
  auto: "split",
  split: "stack",
  stack: "auto",
};
/* モジュール定数は import 時に一度だけ評価されるので、翻訳済み文字列ではなく
 * descriptor を置き、描画時に i18n._() で解決する。 */
const LAYOUT_LABELS: Record<DiffLayout, MessageDescriptor> = {
  auto: msg`自動`,
  split: msg`左右 2 面`,
  stack: msg`縦積み`,
};

/* 片側しかない file(新規追加・削除)は data-diff-type="single" になり、既定では
 * 全幅に広がる。GitHub の split 表示に合わせ、追加のみは右半分、削除のみは左半分へ
 * 寄せる。shadow DOM 内はライブラリの unsafeCSS からしか触れない。ここに入れるのは
 * この固定文字列だけで、patch 由来の値は一切混ぜない(敵性入力規約)。 */
const SINGLE_SIDE_CSS = [
  'pre[data-diff-type="single"] > code[data-additions]{ width:50%; margin-left:auto; }',
  'pre[data-diff-type="single"] > code[data-deletions]{ width:50%; margin-right:auto; }',
].join("\n");

/* memo: Drawer は SSE snapshot tick(約 2s)ごとに再レンダーされるが、diff と
 * theme は変わらないので file 列全体をスキップさせる(library の FileDiff は
 * 非 memo で、素通しすると tick ごとに全 file の setOptions/render が走る)。
 * 折りたたみ操作は overrides Map の差し替えで伝わる。 */
const DiffFiles = memo(function DiffFiles({
  plan,
  overrides,
  theme,
  diffThemes,
  stack,
  registerHost,
  onToggle,
}: {
  plan: DiffFilePlan[];
  overrides: ReadonlyMap<number, boolean>;
  theme: Theme;
  diffThemes: { light: string; dark: string };
  /* 縦積み(unified)にするか。狭いパネルで左右 2 面に割ると 1 面が読めないので
   * 削除と追加を縦に積む。options 経由なので file は作り直さない。 */
  stack: boolean;
  registerHost: (index: number, el: HTMLDivElement | null) => void;
  onToggle: (index: number) => void;
}) {
  /* memo 境界の内側で locale を購読する。props はロケールに依存しないので、
   * これが無いと言語を切り替えても行数ラベルと展開ボタン名が古いまま残る。 */
  const { i18n, t } = useLingui();
  return (
    <>
      {plan.map(({ file, lines, initiallyCollapsed, highlight, inlineDiff }, i) => {
        const collapsed = overrides.get(i) ?? initiallyCollapsed;
        return (
          // file type change は同 path が 2 entry になるため path 単独を key にしない
          <div
            className="diff-file"
            key={`${i}:${file.name}`}
            data-collapsed={collapsed ? "" : undefined}
            ref={(el) => registerHost(i, el)}
          >
            {/* key を付けて作り直さないこと — shadow root に前の placeholder /
                buffer が残り、file の高さが倍になる(useDiffNudge を参照) */}
            <FileDiff
              fileDiff={file}
              options={{
                /* light/dark 両方を渡すとライブラリが両方の CSS 変数を出すので、
                 * 外観モードの切り替えは themeType だけで即座に効く。 */
                theme: diffThemes,
                themeType: theme,
                collapsed,
                /* スクロール中もファイル名を上端に貼り付ける(GitHub の
                 * files changed と同じ)。ヘッダは file ごとの shadow root 内で
                 * sticky になるので、次の file に入れ替わる */
                stickyHeader: true,
                /* 長い行は横スクロールさせず折り返す。狭いコンパクト表示で必須で、
                 * 全画面でも file ごとの横スクロールバーが消えて読みやすい。 */
                overflow: "wrap",
                diffStyle: stack ? "unified" : "split",
                /* 片側寄せは split のときだけの話。unified には single 面が無い */
                ...(stack ? {} : { unsafeCSS: SINGLE_SIDE_CSS }),
                tokenizeMaxLineLength: TOKENIZE_MAX_LINE_LENGTH,
                maxLineDiffLength: TOKENIZE_MAX_LINE_LENGTH,
                /* 内容量が多すぎる file は highlight / 行内 word 差分を切る。
                 * どちらも描画範囲ではなく file 全体に走る処理なので仮想化では
                 * 有界にならない(lib/diff.ts 冒頭のコメントを参照)。 */
                ...(highlight ? {} : { tokenizeMaxLength: TOKENIZE_MAX_LENGTH_PLAIN }),
                ...(inlineDiff ? {} : { lineDiffType: LINE_DIFF_TYPE_PLAIN }),
              }}
              renderHeaderMetadata={() => (
                <span className="diff-file-acts">
                  {/* 行数は「大きい file だ」という情報なので、畳んでいる間は
                      ボタンの外にテキストで残す */}
                  {collapsed && (
                    <span className="diff-file-lines">
                      {t`${{ lines: lines.toLocaleString(i18n.locale) }} 行`}
                    </span>
                  )}
                  {/* 名前に file 名を入れる — 入れないと、展開中の全ボタンが
                      「折りたたむ」で並び、支援技術から区別できない */}
                  <IconButton
                    /* JS の t マクロを使う(<Trans> は {変数} 前後の空白を落とすため、
                       " — " 区切りのアクセシブル名が壊れる) */
                    label={
                      collapsed
                        ? t`${{ name: file.name }} — ${{ lines: lines.toLocaleString(i18n.locale) }} 行 — 展開`
                        : t`${{ name: file.name }} — 折りたたむ`
                    }
                    onClick={() => onToggle(i)}
                  >
                    {collapsed ? <IconChevronDown /> : <IconChevronUp />}
                  </IconButton>
                </span>
              )}
            />
          </div>
        );
      })}
    </>
  );
});

export function DiffOverlay({
  title,
  query,
  token,
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
  const { view: viewMode, setView } = useDiffView();
  const { layout, setLayout } = useDiffLayout();
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
  const hostsRef = useRef(new Map<number, HTMLDivElement>());
  const { width: compactWidth, gripProps } = useDiffWidth();
  /* 背面を覆っているならモーダル。全画面はもちろん、狭い帯や、ドロワーが広くて
   * パネルが一覧を食い尽くす配置でも覆う。覆っているのに非モーダルだと、見えない
   * 背面へ Tab が抜け、隠れた peek のポーリングも続く(判定は lib/diffView)。 */
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
  const parsed = useMemo(() => parseDiffFiles(patch), [patch]);
  const plan = useMemo(() => planDiffFiles(parsed), [parsed]);
  const byPath = useMemo(() => indexDiffFilesByPath(parsed), [parsed]);
  const selectable = useMemo(() => new Set(byPath.keys()), [byPath]);

  const { overrides, onToggle, onExpandAll, onCollapseAll, expand } = useDiffCollapse(patch, plan);
  const nudge = useDiffNudge(rootRef);
  const registerHost = useCallback((i: number, el: HTMLDivElement | null) => {
    if (el) hostsRef.current.set(i, el);
    else hostsRef.current.delete(i);
  }, []);
  const onSelectFile = useScrollToFile({ byPath, hostsRef, expand, nudge });

  /* auto は本文領域の幅で決め、split / stack はユーザーの明示指定をそのまま使う。
   * ヘッダと本文は縦に積むだけなので、本文領域の幅 = パネルの幅(lib/diffView)。 */
  const panelWidth = panelWidthFor({ view: viewMode, viewportWidth, compactWidth });
  const stack = layout === "auto" ? panelWidth < AUTO_SPLIT_MIN_PX : layout === "stack";
  useNudgeOnLayoutChange(`${viewMode}:${stack}`, nudge);
  useBlankRepair({ rootRef, hostsRef, patch, nudge });

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
      <header className="diff-head">
        <h3>
          <span className="diff-title">{title}</span>
          {diff && (
            <span className="diff-branches">
              <code>{diff.branchName}</code> → <code>{diff.baseBranch}</code>
            </span>
          )}
        </h3>
        <span className="diff-meta" id="diff-meta">
          {view?.meta ?? ""}
        </span>
        {/* ラベルは aria-label とツールチップ(data-tip)が持つ。ボタン本体は
            アイコンだけにして、狭いコンパクト表示でもヘッダを 1 行に収める。 */}
        <IconButton
          id="diff-reload"
          className="diff-reload"
          label={t`再取得`}
          disabled={state.phase === "loading"}
          onClick={refetch}
        >
          <IconRefresh />
        </IconButton>
        {/* 1 個で auto -> split -> stack を巡回する。ラベルは現在の設定と、
            押したときに何になるかの両方を名乗る。 */}
        <IconButton
          id="diff-layout"
          label={t`レイアウト: ${{ current: i18n._(LAYOUT_LABELS[layout]) }}(クリックで${{
            next: i18n._(LAYOUT_LABELS[LAYOUT_CYCLE[layout]]),
          }})`}
          onClick={() => setLayout(LAYOUT_CYCLE[layout])}
        >
          {layout === "auto" ? (
            <IconLayoutAuto stack={stack} />
          ) : layout === "split" ? (
            <IconLayoutSplit />
          ) : (
            <IconLayoutStack />
          )}
        </IconButton>
        <IconButton
          id="diff-view-mode"
          label={viewMode === "full" ? t`コンパクト表示` : t`全画面表示`}
          onClick={() => setView(viewMode === "full" ? "compact" : "full")}
        >
          {viewMode === "full" ? <IconMinimize /> : <IconMaximize />}
        </IconButton>
        <IconButton id="diff-settings" label={t`テーマ設定`} popup onClick={onOpenSettings}>
          <IconTheme />
        </IconButton>
        <IconButton id="diff-close" label={t`diff を閉じる`} onClick={onClose}>
          <IconClose />
        </IconButton>
      </header>
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
                overrides={overrides}
                theme={theme}
                diffThemes={diffThemes}
                stack={stack}
                registerHost={registerHost}
                onToggle={onToggle}
              />
            </Virtualizer>
          </div>
        ))}
    </div>,
    document.body,
  );
}
