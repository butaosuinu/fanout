import { useCallback, useEffect, useRef, type RefObject } from "react";

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
 * 184,042px に膨らんで広範囲が空白になった。 */
export function useDiffNudge(rootRef: RefObject<HTMLElement | null>): () => void {
  const nudgedRef = useRef(false);
  return useCallback(() => {
    const scroller = rootRef.current?.querySelector<HTMLElement>(".diff-body");
    if (!scroller) return;
    nudgedRef.current = !nudgedRef.current;
    scroller.style.marginBottom = nudgedRef.current ? "1px" : "0px";
  }, [rootRef]);
}

/* 表示モードの切替も並べ方の切替も、全 file の高さが変わる。幅の変化だけでは
 * root の block size が動かないので、明示的に取り直させる。 */
export function useNudgeOnLayoutChange(layoutKey: string, nudge: () => void) {
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
export function useBlankRepair({
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

/* サイドバーから本文へ飛ぶ。スクロールだけでは着地先の file が placeholder の
 * まま残るので、nudge で全 file のオフセットと描画範囲を取り直させる。高さが
 * 確定すると位置がずれるので、次フレームでもう一度合わせる。 */
export function useScrollToFile({
  byPath,
  hostsRef,
  expand,
  nudge,
}: {
  byPath: Map<string, number[]>;
  hostsRef: Hosts;
  /* 着地先が折りたたまれていたら開く */
  expand: (i: number) => void;
  nudge: () => void;
}): (path: string) => void {
  return useCallback(
    (path: string) => {
      const i = byPath.get(path)?.[0];
      if (i === undefined) return;
      const target = hostsRef.current.get(i);
      const scroller = target?.closest<HTMLElement>(".diff-body");
      if (!target || !scroller) return;
      expand(i);
      const align = () => {
        scroller.scrollTop +=
          target.getBoundingClientRect().top - scroller.getBoundingClientRect().top;
      };
      align();
      nudge();
      requestAnimationFrame(() => requestAnimationFrame(align));
    },
    [byPath, expand, hostsRef, nudge],
  );
}
