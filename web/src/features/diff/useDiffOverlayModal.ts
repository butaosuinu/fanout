import { useEffect, useRef, type RefObject } from "react";
import { blockBackground } from "../../shared/inert";
import { lockDocumentScroll } from "../../shared/scrollLock";
import { useFocusTrap } from "../../shared/useFocusTrap";

/* diff オーバーレイがモーダルとして振る舞うあいだの副作用一式。
 *
 * 並べ替えないこと — unmount 時の cleanup は宣言順に走るので、背面の inert 解除
 * (useBackgroundBlock)は親への通知(useCoveringNotice)より必ず先でなければ
 * ならない。inert な subtree への focus は実ブラウザで拒否される。 */
export function useDiffOverlayModal(
  rootRef: RefObject<HTMLDivElement | null>,
  {
    covering,
    suppressed,
    onCoveringChange,
  }: {
    /* 背面を覆っているか(判定は diffView.ts の coversBackground) */
    covering: boolean;
    /* 上に設定モーダルが重なっている */
    suppressed: boolean;
    onCoveringChange?: (covering: boolean) => void;
  },
) {
  useBackgroundBlock(rootRef, { covering, suppressed });
  useCoveringNotice(covering, onCoveringChange);
  useSuppressedInert(rootRef, suppressed);
  useMountFocus(rootRef, suppressed);
  useDocumentScrollLock(covering);
  useFocusHandoff(rootRef, covering && !suppressed);
}

/* 背面(#root 配下の Nav / テーブル / Drawer)を inert にしてフォーカスと操作を
 * 遮り、Tab を自分の中で循環させる。設定モーダルと所有者が重なるので inert は
 * 参照数で持つ(shared/inert.ts)。
 *
 * covering(全画面 / 狭い帯の全幅コンパクト)のときだけモーダル。背面を覆って
 * いないコンパクトは Tab で背面へ出られてよい。 */
function useBackgroundBlock(
  rootRef: RefObject<HTMLElement | null>,
  { covering, suppressed }: { covering: boolean; suppressed: boolean },
) {
  useFocusTrap(rootRef, covering && !suppressed);
  useEffect(() => {
    if (!covering) return;
    return blockBackground([document.getElementById("root")]);
  }, [covering]);
}

/* peek の停止判断は App が持つが、覆っているかはこちらでしか分からない */
function useCoveringNotice(covering: boolean, onCoveringChange?: (covering: boolean) => void) {
  /* レンダー中の ref 代入は意図的 — 最新のコールバックを、effect を再実行させずに
   * 参照するため(依存に入れると親が再レンダーするたびに通知が走る)。 */
  const onCoveringRef = useRef(onCoveringChange);
  onCoveringRef.current = onCoveringChange;
  useEffect(() => {
    onCoveringRef.current?.(covering);
    return () => onCoveringRef.current?.(false);
  }, [covering]);
}

/* 上に設定モーダルがある間は自分を inert にする(mount 順に依存しない) */
function useSuppressedInert(rootRef: RefObject<HTMLElement | null>, suppressed: boolean) {
  useEffect(() => {
    if (!suppressed) return;
    return blockBackground([rootRef.current]);
  }, [rootRef, suppressed]);
}

/* mount 時にフォーカスを引き取る。ただし上に設定モーダルが重なっていれば降りる
 * — lazy chunk の解決が設定を開いた後になると、設定側からは要素が見えず遮れない。
 * 抑止が解けた後の引き取りは useFocusHandoff が持つので、ここは mount 時だけ。 */
function useMountFocus(rootRef: RefObject<HTMLElement | null>, suppressed: boolean) {
  /* レンダー中の ref 代入は意図的 — 最新値を effect の再実行なしで参照するため
   * (suppressed を依存に入れると、抑止が解けるたび mount 時の判断がやり直される)。 */
  const suppressedRef = useRef(suppressed);
  suppressedRef.current = suppressed;
  useEffect(() => {
    if (!suppressedRef.current) rootRef.current?.focus();
  }, [rootRef]);
}

/* 覆っているあいだは背面の document スクロールも止める(理由は shared/scrollLock)。
 * 背面が見えるコンパクト表示では止めない — そこは背面を触るための表示。 */
function useDocumentScrollLock(covering: boolean) {
  useEffect(() => {
    if (!covering) return;
    return lockDocumentScroll();
  }, [covering]);
}

/* 「自分が最前面のモーダルになった」瞬間にフォーカスを引き取る。covering に
 * なった(ウィンドウを 1,100px 以下へ縮めた、コンパクトから全画面へ切り替えた)
 * ときも、上の設定モーダルが閉じて抑止が解けたときも同じ。背面はこの間 inert
 * なので、引き取らないとフォーカスが行き場を失う。
 *
 * 抑止側も見ること — lazy chunk の解決待ちに設定を開くと、mount 時点で covering
 * かつ suppressed になり、covering だけを見ていると遷移が起きない。 */
function useFocusHandoff(rootRef: RefObject<HTMLElement | null>, active: boolean) {
  const wasActive = useRef(active);
  useEffect(() => {
    const became = active && !wasActive.current;
    wasActive.current = active;
    if (!became) return;
    const root = rootRef.current;
    if (root && !root.contains(document.activeElement)) root.focus();
  }, [active, rootRef]);
}

/* フォーカスが自分の中にあるか。要素がまだ無ければ「自分の中には無い」。 */
function holdsFocus(root: HTMLElement | null): boolean {
  return root?.contains(document.activeElement) ?? false;
}

/* capture 段で preventDefault を立て、Drawer の document(bubble)listener に
 * Escape を渡さない — オーバーレイだけを閉じ、下の Drawer は開いたまま残す。
 *
 * ただし背面を覆っていないコンパクト表示では、フォーカスが自分の中にあるとき
 * だけ引き取る。capture は React の handler より先に走るので、無条件に閉じると
 * 背面で開いている popup(フィルタの dropdown 等)の Escape を横取りし、
 * 1 回のキーで 2 層が同時に閉じる。Escape は「いま居るもの」を閉じる。 */
export function useEscapeToClose(
  rootRef: RefObject<HTMLElement | null>,
  {
    covering,
    enabled,
    onClose,
  }: {
    covering: boolean;
    /* 上に設定モーダルが重なっている間は Escape を譲る(下の diff は開いたまま) */
    enabled: boolean;
    onClose: () => void;
  },
) {
  useEffect(() => {
    if (!enabled) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented) return;
      if (!covering && !holdsFocus(rootRef.current)) return;
      e.preventDefault();
      onClose();
    };
    document.addEventListener("keydown", onKeyDown, true);
    return () => document.removeEventListener("keydown", onKeyDown, true);
  }, [covering, enabled, onClose, rootRef]);
}
