import { useCallback, useEffect, useState, type FocusEvent, type RefObject } from "react";

/* trigger + popover の開閉。FilterDropdown が先に確立した作法のうち、ロールや
 * 選択肢に依存しない部分だけを取り出したもの:
 *
 * - 外側の pointerdown で閉じる(フォーカスは奪わない)。別の popover の trigger
 *   押下も「外」なので、同時に 2 つ開くことはない。
 * - Tab 等でフォーカスがルート外へ出たら閉じる(外側 click は pointerdown が先)。
 *
 * Escape は呼び出し側の onKeyDown が持つ。preventDefault + stopPropagation を
 * どこまでやるかは、その popover が何の中に居るか(Drawer / diff オーバーレイ)で
 * 変わるため。
 *
 * FilterDropdown 自体をこの hook へ寄せるのは別変更 — あちらは選択肢の凍結と
 * typeahead を抱えていて、無関係な churn になる。 */
export function usePopover(
  rootRef: RefObject<HTMLElement | null>,
  triggerRef: RefObject<HTMLElement | null>,
): {
  open: boolean;
  setOpen: (open: boolean) => void;
  close: (refocus: boolean) => void;
  onBlur: (e: FocusEvent) => void;
} {
  const [open, setOpen] = useState(false);

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (e: PointerEvent) => {
      if (e.target instanceof Node && !rootRef.current?.contains(e.target)) setOpen(false);
    };
    document.addEventListener("pointerdown", onPointerDown);
    return () => document.removeEventListener("pointerdown", onPointerDown);
  }, [open, rootRef]);

  const close = useCallback(
    (refocus: boolean) => {
      setOpen(false);
      if (refocus) triggerRef.current?.focus();
    },
    [triggerRef],
  );

  const onBlur = useCallback(
    (e: FocusEvent) => {
      if (open && e.relatedTarget && !rootRef.current?.contains(e.relatedTarget as Node)) {
        setOpen(false);
      }
    },
    [open, rootRef],
  );

  return { open, setOpen, close, onBlur };
}
