import { FileDiff, Virtualizer } from "@pierre/diffs/react";
import { memo, useCallback, useEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import { createPortal } from "react-dom";
import { useDiff } from "../hooks/useDiff";
import { useDiffWidth } from "../hooks/useDiffWidth";
import { useFocusTrap } from "../hooks/useFocusTrap";
import { useViewportWidth } from "../hooks/useViewportWidth";
import {
  useDiffLayout,
  useDiffTheme,
  useDiffView,
  useTheme,
  type DiffLayout,
  type Theme,
} from "../hooks/useSettings";
import { apiUrl } from "../lib/api";
import { coversBackground, panelWidthFor, COMPACT_FULL_WIDTH_PX } from "../lib/diffView";
import { blockBackground } from "../lib/inert";
import { lockDocumentScroll } from "../lib/scrollLock";
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
} from "../lib/diff";
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
} from "./icons";

/* patch 本文と file path は敵性入力。@pierre/diffs はテキストをトークン分解して
 * DOM API で組み立てる(patch を HTML として解釈しない)前提で採用しており、
 * その性質は diffOverlay.test.tsx の敵性 patch テストで固定している。
 * こちら側でも patch 由来の文字列を dangerouslySetInnerHTML に渡さない。 */

const EMPTY_OVERRIDES: ReadonlyMap<number, boolean> = new Map();

/* 空白の修復判定: この高さ以上が見えている file だけを対象にする。 */
const BLANK_REPAIR_MIN_PX = 120;
/* スクロールが止まった後の押さえの再検査(最後のフレームが空白で終わった場合用) */
const BLANK_REPAIR_SETTLE_MS = 120;
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
const LAYOUT_LABELS: Record<DiffLayout, string> = {
  auto: "自動",
  split: "左右 2 面",
  stack: "縦積み",
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
                buffer が残り、file の高さが倍になる(DiffOverlay の nudge を参照) */}
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
                    <span className="diff-file-lines">{lines.toLocaleString()} 行</span>
                  )}
                  {/* 名前に file 名を入れる — 入れないと、展開中の全ボタンが
                      「折りたたむ」で並び、支援技術から区別できない */}
                  <IconButton
                    label={
                      collapsed
                        ? `${file.name} — ${lines.toLocaleString()} 行 — 展開`
                        : `${file.name} — 折りたたむ`
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
  const { theme } = useTheme();
  const { light, dark } = useDiffTheme();
  const { view: viewMode, setView } = useDiffView();
  const { layout, setLayout } = useDiffLayout();
  /* auto レイアウトと covering 判定に使うビューポート幅。ResizeObserver は使わない
   * — タブが非表示のあいだ配信が止まるうえ、パネル幅はここで計算できる。 */
  const viewportWidth = useViewportWidth();
  /* パネル右端の位置(= ドロワーの左端)と、背面コンテンツの左端。実寸で覆って
   * いるかを判定するため state で持つ。ドロワーは幅可変で、選択が無ければ存在
   * しない。コンテンツ左端はドロワー幅に連動して動く(main-col が縮む)。 */
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
  /* covering(全画面 / 狭い帯の全幅コンパクト)のときだけモーダル。背面を
   * 覆っていないコンパクトは Tab で背面へ出られてよい。 */
  useFocusTrap(rootRef, covering && !suppressed);
  /* 背面(#root 配下の Nav / テーブル / Drawer)を inert にしてフォーカスと操作を
   * 遮る。設定モーダルと所有者が重なるので参照数で持つ(lib/inert.ts)。
   * unmount 時の cleanup 順は宣言順なので、下の onClosed より必ず先に走る
   * (inert な subtree への focus は実ブラウザで拒否されるため順序が本質)。 */
  useEffect(() => {
    if (!covering) return;
    return blockBackground([document.getElementById("root")]);
  }, [covering]);

  /* peek の停止判断は App が持つが、覆っているかはこちらでしか分からない */
  const onCoveringRef = useRef(onCoveringChange);
  onCoveringRef.current = onCoveringChange;
  useEffect(() => {
    onCoveringRef.current?.(covering);
    return () => onCoveringRef.current?.(false);
  }, [covering]);

  /* 上に設定モーダルがある間は自分を inert にする(mount 順に依存しない) */
  useEffect(() => {
    if (!suppressed) return;
    return blockBackground([rootRef.current]);
  }, [suppressed]);

  const suppressedRef = useRef(suppressed);
  suppressedRef.current = suppressed;
  useEffect(() => {
    if (!suppressedRef.current) rootRef.current?.focus();
  }, []);

  /* 覆っているあいだは背面の document スクロールも止める(理由は lib/scrollLock)。
   * 背面が見えるコンパクト表示では止めない — そこは背面を触るための表示。 */
  useEffect(() => {
    if (!covering) return;
    return lockDocumentScroll();
  }, [covering]);

  /* 「自分が最前面のモーダルになった」瞬間にフォーカスを引き取る。covering に
   * なった(ウィンドウを 1,100px 以下へ縮めた、コンパクトから全画面へ切り替えた)
   * ときも、上の設定モーダルが閉じて抑止が解けたときも同じ。背面はこの間 inert
   * なので、引き取らないとフォーカスが行き場を失う。
   *
   * 抑止側も見ること — lazy chunk の解決待ちに設定を開くと、mount 時点で
   * covering かつ suppressed になり、covering だけを見ていると遷移が起きない。 */
  const wasActive = useRef(covering && !suppressed);
  useEffect(() => {
    const active = covering && !suppressed;
    if (active && !wasActive.current) {
      const root = rootRef.current;
      if (root && !root.contains(document.activeElement)) root.focus();
    }
    wasActive.current = active;
  }, [covering, suppressed]);

  /* コンパクト表示の右端を詳細ドロワーの左端に合わせる。ドロワーは幅可変
   * (--drawer-w のドラッグ)かつ選択が無ければ存在しないので実測する。
   * ドロワーが無いときはビューポート右端に寄せる。
   *
   * 背面コンテンツの左端も同じ合図で取り直す — ドロワーを広げると main-col が
   * 縮み、`.wrap` の中央寄せぶん左端も動く。 */
  useEffect(() => {
    if (viewMode !== "compact") return;
    const drawer = document.getElementById("drawer");
    const content = document.getElementById("content");
    const sync = () => {
      /* 幅 0 の矩形はレイアウトが無い(jsdom)か実際に場所を取っていないので、
       * ドロワーが無いのと同じ扱いにする。0 を左端として採ると、パネルが画面を
       * 覆い尽くしていると誤判定する。 */
      const rect = drawer?.getBoundingClientRect();
      const left = rect && rect.width > 0 ? rect.left : window.innerWidth;
      setAnchorRight(Math.max(0, Math.round(window.innerWidth - left)));
      /* コンテンツ側も同じ理由で幅 0 は実測できなかった扱い。0 に倒すと
       * 「帯が 1px でもあれば覆っていない」という以前の判定に戻るだけで、
       * 覆っていないものを覆ったと誤判定はしない。 */
      const c = content?.getBoundingClientRect();
      setContentLeft(c && c.width > 0 ? Math.max(0, Math.round(c.left)) : 0);
    };
    sync();
    const ro = new ResizeObserver(sync);
    if (drawer) ro.observe(drawer);
    if (content) ro.observe(content);
    window.addEventListener("resize", sync);
    return () => {
      ro.disconnect();
      window.removeEventListener("resize", sync);
    };
  }, [viewMode, anchorKey]);

  /* capture 段で preventDefault を立て、Drawer の document(bubble)listener に
   * Escape を渡さない — オーバーレイだけを閉じ、下の Drawer は開いたまま残す。
   *
   * ただし背面を覆っていないコンパクト表示では、フォーカスが自分の中にあるとき
   * だけ引き取る。capture は React の handler より先に走るので、無条件に閉じると
   * 背面で開いている popup(フィルタの dropdown 等)の Escape を横取りし、
   * 1 回のキーで 2 層が同時に閉じる。Escape は「いま居るもの」を閉じる。 */
  useEffect(() => {
    if (!escapeEnabled) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented) return;
      if (!covering && !rootRef.current?.contains(document.activeElement)) return;
      e.preventDefault();
      onClose();
    };
    document.addEventListener("keydown", onKeyDown, true);
    return () => document.removeEventListener("keydown", onKeyDown, true);
  }, [onClose, escapeEnabled, covering]);

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

  /* 初期方針(planDiffFiles)に対するユーザーの上書き。複数 file を同時に展開した
   * ままにできる。patch 自体を key に持つ — 再取得で patch が変わると index の
   * 意味も変わるが、passive effect でのリセットでは間に合わない(@pierre/diffs は
   * ref/layout 経路で同期 mount するので、リセット前の 1 回で古い index の
   * 折りたたみ状態を新しい file に適用してしまう)。 */
  const [ov, setOv] = useState<{ patch: string; map: ReadonlyMap<number, boolean> }>({
    patch,
    map: EMPTY_OVERRIDES,
  });
  const overrides = ov.patch === patch ? ov.map : EMPTY_OVERRIDES;

  /* 描画位置の作り直し。ライブラリの virtualizer は各 file の scroll 内オフセットを
   * 一度しか計算せず、更新するのは「root か content の resize」を検知したときだけ
   * (`onRender(dirty)` の dirty 経路)。通常のスクロールでは前後の file が実描画で
   * 伸縮してもオフセットが古いままになり、行は描かれているのに画面外へ置かれて空白に
   * 見える。そこで root の border-box 高さを 1px 動かして resize を起こし、全 file の
   * オフセットを取り直させる(実測: 916px の空白が 2 フレームで 8px になる)。
   *
   * FileDiff を key で作り直す手も効くが採ってはいけない。React 側が要素を持つ
   * (isContainerManaged)ため cleanUp は shadow root の placeholder / buffer を
   * 残したまま参照だけ捨て、作り直した instance がその上に重ねて描く。実測で
   * 1 回の作り直しだけで全 file の高さが 2 倍になり、文書長が 92,098px →
   * 184,042px に膨らんで広範囲が空白になった。 */
  const nudgedRef = useRef(false);
  const nudge = useCallback(() => {
    const scroller = rootRef.current?.querySelector<HTMLElement>(".diff-body");
    if (!scroller) return;
    nudgedRef.current = !nudgedRef.current;
    scroller.style.marginBottom = nudgedRef.current ? "1px" : "0px";
  }, []);

  const onToggle = useCallback(
    (i: number) => {
      const collapsed = overrides.get(i) ?? plan[i]?.initiallyCollapsed ?? false;
      setOv((prev) => {
        const map = new Map(prev.patch === patch ? prev.map : EMPTY_OVERRIDES);
        map.set(i, !collapsed);
        return { patch, map };
      });
    },
    [overrides, plan, patch],
  );
  const setAll = useCallback(
    (collapsed: boolean) => {
      const map = new Map<number, boolean>();
      plan.forEach((_, i) => map.set(i, collapsed));
      setOv({ patch, map });
    },
    [plan, patch],
  );
  const onExpandAll = useCallback(() => setAll(false), [setAll]);
  const onCollapseAll = useCallback(() => setAll(true), [setAll]);

  const registerHost = useCallback((i: number, el: HTMLDivElement | null) => {
    if (el) hostsRef.current.set(i, el);
    else hostsRef.current.delete(i);
  }, []);

  /* サイドバーから本文へ飛ぶ。スクロールだけでは着地先の file が placeholder の
   * まま残るので、nudge で全 file のオフセットと描画範囲を取り直させる。高さが
   * 確定すると位置がずれるので、次フレームでもう一度合わせる。 */
  const onSelectFile = useCallback(
    (path: string) => {
      const i = byPath.get(path)?.[0];
      if (i === undefined) return;
      const target = hostsRef.current.get(i);
      const scroller = target?.closest<HTMLElement>(".diff-body");
      if (!target || !scroller) return;
      if (overrides.get(i) ?? plan[i]?.initiallyCollapsed) {
        setOv((prev) => {
          const map = new Map(prev.patch === patch ? prev.map : EMPTY_OVERRIDES);
          map.set(i, false);
          return { patch, map };
        });
      }
      const align = () => {
        scroller.scrollTop +=
          target.getBoundingClientRect().top - scroller.getBoundingClientRect().top;
      };
      align();
      nudge();
      requestAnimationFrame(() => requestAnimationFrame(align));
    },
    [byPath, overrides, plan, patch, nudge],
  );

  /* auto は本文領域の幅で決め、split / stack はユーザーの明示指定をそのまま使う。
   * ヘッダと本文は縦に積むだけなので、本文領域の幅 = パネルの幅(lib/diffView)。 */
  const panelWidth = panelWidthFor({ view: viewMode, viewportWidth, compactWidth });
  const stack = layout === "auto" ? panelWidth < AUTO_SPLIT_MIN_PX : layout === "stack";

  /* 表示モードの切替も並べ方の切替も、全 file の高さが変わる。幅の変化だけでは
   * root の block size が動かないので、明示的に取り直させる。 */
  const prevLayoutRef = useRef(`${viewMode}:${stack}`);
  useEffect(() => {
    const key = `${viewMode}:${stack}`;
    if (prevLayoutRef.current === key) return;
    prevLayoutRef.current = key;
    nudge();
  }, [viewMode, stack, nudge]);

  /* スクロール中の空白の修復。判定は「見えている帯に実際の行があるか」だけ。
   * sticky なファイル名ヘッダは数えない — 空白の画面でもヘッダだけは貼り付いて
   * 見えているため。折りたたみ中の file は行が無くて当然なので対象外。 */
  useEffect(() => {
    const scroller = rootRef.current?.querySelector<HTMLElement>(".diff-body");
    if (!scroller) return;
    let frame = 0;
    let settle = 0;
    const isBlank = () => {
      const port = scroller.getBoundingClientRect();
      for (const host of hostsRef.current.values()) {
        if (host.hasAttribute("data-collapsed")) continue;
        const box = host.getBoundingClientRect();
        const top = Math.max(box.top, port.top);
        const bottom = Math.min(box.bottom, port.bottom);
        if (bottom - top < BLANK_REPAIR_MIN_PX) continue;
        const rows = host
          .querySelector("diffs-container")
          ?.shadowRoot?.querySelectorAll("[data-line]");
        /* 行は文書順なので、先頭と末尾で描画済みの範囲が分かる */
        const first = rows?.item(0)?.getBoundingClientRect();
        const last = rows?.item(rows.length - 1)?.getBoundingClientRect();
        if (!first || !last) return true;
        if (last.bottom <= top || first.top >= bottom) return true;
      }
      return false;
    };
    const check = () => {
      frame = 0;
      if (isBlank()) nudge();
    };
    const onScroll = () => {
      if (!frame) frame = requestAnimationFrame(check);
      clearTimeout(settle);
      settle = window.setTimeout(check, BLANK_REPAIR_SETTLE_MS);
    };
    scroller.addEventListener("scroll", onScroll, { passive: true });
    return () => {
      cancelAnimationFrame(frame);
      clearTimeout(settle);
      scroller.removeEventListener("scroll", onScroll);
    };
  }, [patch, nudge]);

  /* #drawer(sticky/fixed)はスタッキングコンテキストを作るため、その子として
   * 描くと z-index が閉じ込められ nav の下に潜る。portal で body 直下に出す。 */
  return createPortal(
    <div
      className="diff-overlay"
      id="diff-overlay"
      role={covering ? "dialog" : "complementary"}
      aria-modal={covering || undefined}
      aria-label="worktree diff"
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
          label="再取得"
          disabled={state.phase === "loading"}
          onClick={refetch}
        >
          <IconRefresh />
        </IconButton>
        {/* 1 個で auto -> split -> stack を巡回する。ラベルは現在の設定と、
            押したときに何になるかの両方を名乗る。 */}
        <IconButton
          id="diff-layout"
          label={`レイアウト: ${LAYOUT_LABELS[layout]}(クリックで${LAYOUT_LABELS[LAYOUT_CYCLE[layout]]})`}
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
          label={viewMode === "full" ? "コンパクト表示" : "全画面表示"}
          onClick={() => setView(viewMode === "full" ? "compact" : "full")}
        >
          {viewMode === "full" ? <IconMinimize /> : <IconMaximize />}
        </IconButton>
        <IconButton id="diff-settings" label="テーマ設定" popup onClick={onOpenSettings}>
          <IconTheme />
        </IconButton>
        <IconButton id="diff-close" label="diff を閉じる" onClick={onClose}>
          <IconClose />
        </IconButton>
      </header>
      {view?.warning && (
        <div className="diff-banner" role="status">
          {view.warning}
        </div>
      )}
      {diff && <DiffOmittedNote files={diff.files} />}
      {state.phase === "loading" && <div className="diff-note">diff を取得中…(最大 10 秒)</div>}
      {state.phase === "error" && (
        <div className="diff-note diff-error" role="alert">
          {state.message}
        </div>
      )}
      {diff &&
        (diff.files.length === 0 ? (
          <div className="diff-note">merge-base からの変更はありません</div>
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
