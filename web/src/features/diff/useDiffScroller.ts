import { useCallback, useEffect, useRef, type RefObject } from "react";
import { alignedScrollTop } from "./scrollAlign";

/* このモジュールは 1 つの関心の集まり — ライブラリの virtualizer が持つ file ごとの
 * オフセットは resize でしか更新されないので、スクロール中の空白も、サイドバーから
 * のジャンプも、確認済みを付けたあとの送りも、レイアウト変更も、すべて同じ nudge で
 * 直す。個々の hook を呼び出し側に並べると、どれか 1 本を張り忘れた瞬間に「行はある
 * のに画面が空白」に戻る。
 * useDiffScrolling(末尾)がその束ね役で、DiffOverlay はこれだけを呼ぶ。 */

/* 2 フレーム後に呼ぶ。resize の観測もレイアウトの確定もフレーム末なので、直前に
 * 書き換えた分がそこに乗るのを待ってから次を当てる。 */
function afterTwoFrames(fn: () => void): void {
  requestAnimationFrame(() => requestAnimationFrame(fn));
}

/* 空白の修復判定: この高さ以上が見えている file だけを対象にする。 */
const BLANK_REPAIR_MIN_PX = 120;
/* スクロールが止まった後の押さえの再検査(最後のフレームが空白で終わった場合用) */
const BLANK_REPAIR_SETTLE_MS = 120;

/* file ごとの host 要素(index -> .diff-file)。DiffOverlay が registerHost で
 * 出し入れし、空白判定とジャンプがそこから実要素を引く。 */
type Hosts = RefObject<Map<number, HTMLDivElement>>;

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
 * 184,042px に膨らんで広範囲が空白になった。
 *
 * 書き換えの間隔がこの hook の本体。resize が観測されるのはフレーム末なので、
 * 1px にしてから同じフレーム内で 0px へ戻すと border-box は元のままになり、
 * 「何も変わっていない」ことになってオフセットが古いまま残る。重なった要求は
 * 捨てずに、直前の書き換えが観測されてから当てる:
 *
 * - 重なるのは実際にある。「確認済みを隠す」では、隠れる集合が動いた
 *   useNudgeOnLayoutChange と、送り先を合わせる useAlignToFile が同じフレームで
 *   両方呼ぶ。
 * - 捨ててもいけない。背面タブは rAF も resize の観測も止まるので、その間に来た
 *   要求(別タブの確認済みが storage 経由で届く)がまるごと消え、表へ戻ったときに
 *   空白のまま残る。 */
function useDiffNudge(rootRef: RefObject<HTMLElement | null>): () => void {
  const nudgedRef = useRef(false);
  const busyRef = useRef(false);
  const pendingRef = useRef(false);
  const toggle = useCallback(() => {
    const scroller = rootRef.current?.querySelector<HTMLElement>(".diff-body");
    if (!scroller) return;
    nudgedRef.current = !nudgedRef.current;
    scroller.style.marginBottom = nudgedRef.current ? "1px" : "0px";
  }, [rootRef]);
  return useCallback(
    function run() {
      if (busyRef.current) {
        pendingRef.current = true;
        return;
      }
      busyRef.current = true;
      toggle();
      afterTwoFrames(() => {
        busyRef.current = false;
        if (!pendingRef.current) return;
        pendingRef.current = false;
        run();
      });
    },
    [toggle],
  );
}

/* 表示モードの切替も並べ方の切替も、全 file の高さが変わる。幅の変化だけでは
 * root の block size が動かないので、明示的に取り直させる。 */
function useNudgeOnLayoutChange(layoutKey: string, nudge: () => void) {
  const prevRef = useRef(layoutKey);
  useEffect(() => {
    if (prevRef.current === layoutKey) return;
    prevRef.current = layoutKey;
    nudge();
  }, [layoutKey, nudge]);
}

/* file の shadow root に実際に描かれている行の縦範囲。行は文書順なので、先頭と
 * 末尾で描画済みの範囲が分かる。1 行も描かれていなければ null。 */
function renderedBand(host: HTMLElement): { top: number; bottom: number } | null {
  const rows = host.querySelector("diffs-container")?.shadowRoot?.querySelectorAll("[data-line]");
  const first = rows?.item(0);
  const last = rows?.item(rows.length - 1);
  if (!first || !last) return null;
  return { top: first.getBoundingClientRect().top, bottom: last.getBoundingClientRect().bottom };
}

/* 見えている帯に実際の行があるか。sticky なファイル名ヘッダは数えない — 空白の
 * 画面でもヘッダだけは貼り付いて見えているため。 */
function hasVisibleRows(host: HTMLElement, port: DOMRect): boolean {
  const box = host.getBoundingClientRect();
  const top = Math.max(box.top, port.top);
  const bottom = Math.min(box.bottom, port.bottom);
  /* ほとんど見えていない file は判定に使わない(空白とは呼ばない) */
  if (bottom - top < BLANK_REPAIR_MIN_PX) return true;
  const band = renderedBand(host);
  return band !== null && band.bottom > top && band.top < bottom;
}

/* 見えている file のどれかが行を 1 つも出していなければ空白。折りたたみ中の
 * file は行が無くて当然なので対象外。 */
function isBlank(scroller: HTMLElement, hosts: Iterable<HTMLElement>): boolean {
  const port = scroller.getBoundingClientRect();
  for (const host of hosts) {
    if (!host.hasAttribute("data-collapsed") && !hasVisibleRows(host, port)) return true;
  }
  return false;
}

/* スクロール中の空白の修復。判定は「見えている帯に実際の行があるか」だけ。 */
function useBlankRepair({
  rootRef,
  hostsRef,
  patch,
  nudge,
}: {
  rootRef: RefObject<HTMLElement | null>;
  hostsRef: Hosts;
  /* patch が入れ替われば scroller ごと張り直す */
  patch: string;
  nudge: () => void;
}) {
  useEffect(() => {
    const scroller = rootRef.current?.querySelector<HTMLElement>(".diff-body");
    if (!scroller) return;
    let frame = 0;
    let settle = 0;
    const check = () => {
      frame = 0;
      if (isBlank(scroller, hostsRef.current.values())) nudge();
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
  }, [hostsRef, nudge, patch, rootRef]);
}

/* file の上端を本文スクロール領域の上端へ。スクロールだけでは着地先の file が
 * placeholder のまま残るので、nudge で全 file のオフセットと描画範囲を取り直させる。
 * 高さが確定すると位置がずれるので、次フレームでもう一度合わせる。
 * サイドバーからのジャンプと確認済みの送りが同じ経路を通る。 */
function useAlignToFile({ hostsRef, nudge }: { hostsRef: Hosts; nudge: () => void }) {
  return useCallback(
    (index: number) => {
      const target = hostsRef.current.get(index);
      const scroller = target?.closest<HTMLElement>(".diff-body");
      if (!target || !scroller) return;
      const align = () => {
        scroller.scrollTop = alignedScrollTop({
          scrollTop: scroller.scrollTop,
          targetTop: target.getBoundingClientRect().top,
          scrollerTop: scroller.getBoundingClientRect().top,
        });
      };
      align();
      nudge();
      afterTwoFrames(align);
    },
    [hostsRef, nudge],
  );
}

/* サイドバーから本文へ飛ぶ。 */
function useScrollToFile({
  byPath,
  expand,
  alignToFile,
}: {
  byPath: Map<string, number[]>;
  /* 着地先が折りたたまれていたら開く */
  expand: (i: number) => void;
  alignToFile: (index: number) => void;
}): (path: string) => void {
  return useCallback(
    (path: string) => {
      const i = byPath.get(path)?.[0];
      if (i === undefined) return;
      expand(i);
      alignToFile(i);
    },
    [alignToFile, byPath, expand],
  );
}

/* 確認済みを付けた直後の送り。押した file はその場で畳まれ(「確認済みを隠す」なら
 * 行ごと消え)、文書の高さが縮む。何もしないとブラウザのスクロールアンカリング任せに
 * なって読んでいた位置が飛ぶので、呼び出し側が決めた「次に読む file」の上端へ明示的に
 * 合わせる。行が入れ替わるのは commit 後なので、測るのは次フレーム。
 *
 * どの file へ送るかを決めるのはここではない — 焦点の拾い直しも同じ答えを要るので、
 * 選ぶのは DiffBody で 1 回だけ(scrollAlign の nextFileToRead)。 */
function useAlignAfterCommit({
  alignToFile,
}: {
  alignToFile: (index: number) => void;
}): (index: number) => void {
  return useCallback(
    (index) => {
      requestAnimationFrame(() => alignToFile(index));
    },
    [alignToFile],
  );
}

/* 上をまとめて張る。host の台帳(index -> .diff-file)もここが持つ —
 * 空白判定もジャンプも同じ台帳を見るので、持ち主を呼び出し側に置く理由がない。 */
export function useDiffScrolling({
  rootRef,
  byPath,
  expand,
  patch,
  layoutKey,
}: {
  rootRef: RefObject<HTMLElement | null>;
  byPath: Map<string, number[]>;
  expand: (i: number) => void;
  patch: string;
  /* 全 file の高さが変わる操作の合成キー(表示モード・並べ方・確認済みの絞り込み) */
  layoutKey: string;
}): {
  registerHost: (index: number, el: HTMLDivElement | null) => void;
  onSelectFile: (path: string) => void;
  alignAfterCommit: (index: number) => void;
} {
  const hostsRef = useRef(new Map<number, HTMLDivElement>());
  const nudge = useDiffNudge(rootRef);
  const registerHost = useCallback((i: number, el: HTMLDivElement | null) => {
    if (el) hostsRef.current.set(i, el);
    else hostsRef.current.delete(i);
  }, []);
  const alignToFile = useAlignToFile({ hostsRef, nudge });
  const onSelectFile = useScrollToFile({ byPath, expand, alignToFile });
  const alignAfterCommit = useAlignAfterCommit({ alignToFile });
  useNudgeOnLayoutChange(layoutKey, nudge);
  useBlankRepair({ rootRef, hostsRef, patch, nudge });
  return { registerHost, onSelectFile, alignAfterCommit };
}
