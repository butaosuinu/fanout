import { useEffect, useRef, useState } from "react";
import type { KeyboardEvent, PointerEvent } from "react";

export const DRAWER_DEFAULT_WIDTH = 840;
export const DRAWER_MIN_WIDTH = 320;
export const DRAWER_MAX_WIDTH = 1600;

const STORAGE_KEY = "fanout.drawerWidth";
const RESIZING_CLASS = "drawer-resizing";
const KEY_STEP = 24;

function clampWidth(w: number): number {
  return Math.min(DRAWER_MAX_WIDTH, Math.max(DRAWER_MIN_WIDTH, Math.round(w)));
}

/* 初期値は localStorage から同期 read(Drawer は key={selected} で remount される
 * ため、保存値さえ読めれば state は Drawer 内で完結する)。localStorage は
 * private mode で例外を投げるので try/catch で握りつぶす(useTheme と同じ)。
 * 不正値はデフォルトへ、範囲外は clamp。 */
function initialWidth(): number {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw) {
      const n = Number(raw);
      if (Number.isFinite(n) && n > 0) return clampWidth(n);
    }
  } catch {
    /* private mode */
  }
  return DRAWER_DEFAULT_WIDTH;
}

function persist(w: number) {
  try {
    localStorage.setItem(STORAGE_KEY, String(w));
  } catch {
    /* private mode */
  }
}

export interface DrawerGripProps {
  onPointerDown: (e: PointerEvent<HTMLElement>) => void;
  onPointerMove: (e: PointerEvent<HTMLElement>) => void;
  onPointerUp: (e: PointerEvent<HTMLElement>) => void;
  onPointerCancel: (e: PointerEvent<HTMLElement>) => void;
  onDoubleClick: () => void;
  onKeyDown: (e: KeyboardEvent<HTMLElement>) => void;
}

/* ドロワー幅(px)の管理。値は CSS 変数 --drawer-w としてだけ反映し、ビューポート
 * 上限(calc(100vw - 360px) / 90vw / bottom sheet)のクランプは CSS 側の仕事 —
 * ここでは保存値・操作値の静的な健全範囲(320–1600px)だけを守る。
 * ドロワーは右アンカーなので左ドラッグで拡大。setPointerCapture は jsdom に
 * 存在しないため optional chain で呼ぶ(pointerup での解放は implicit release に
 * 任せる)。永続化は pointerup / ダブルクリック / キー操作時のみ。 */
export function useDrawerWidth(): { width: number; gripProps: DrawerGripProps } {
  const [width, setWidthState] = useState<number>(initialWidth);
  const widthRef = useRef(width);
  const dragRef = useRef<{ pointerId: number; startX: number; startWidth: number } | null>(null);

  const setWidth = (w: number) => {
    widthRef.current = w;
    setWidthState(w);
  };

  /* ドラッグ中に unmount(pane 切替の remount)しても html に状態を残さない */
  useEffect(
    () => () => {
      document.documentElement.classList.remove(RESIZING_CLASS);
    },
    [],
  );

  const endDrag = (e: PointerEvent<HTMLElement>): boolean => {
    const drag = dragRef.current;
    if (!drag || e.pointerId !== drag.pointerId) return false;
    dragRef.current = null;
    document.documentElement.classList.remove(RESIZING_CLASS);
    return true;
  };

  const gripProps: DrawerGripProps = {
    onPointerDown: (e) => {
      if (e.button !== 0) return;
      dragRef.current = { pointerId: e.pointerId, startX: e.clientX, startWidth: widthRef.current };
      e.currentTarget.setPointerCapture?.(e.pointerId);
      document.documentElement.classList.add(RESIZING_CLASS);
    },
    onPointerMove: (e) => {
      const drag = dragRef.current;
      if (!drag || e.pointerId !== drag.pointerId) return;
      setWidth(clampWidth(drag.startWidth + (drag.startX - e.clientX)));
    },
    onPointerUp: (e) => {
      if (endDrag(e)) persist(widthRef.current);
    },
    onPointerCancel: (e) => {
      endDrag(e); // OS 都合の中断は永続化しない(次回 remount は直前の保存値)
    },
    onDoubleClick: () => {
      setWidth(DRAWER_DEFAULT_WIDTH);
      try {
        localStorage.removeItem(STORAGE_KEY);
      } catch {
        /* private mode */
      }
    },
    onKeyDown: (e) => {
      // 右アンカーなので ArrowLeft = セパレータを左へ = 拡大
      const delta = e.key === "ArrowLeft" ? KEY_STEP : e.key === "ArrowRight" ? -KEY_STEP : 0;
      if (!delta) return;
      e.preventDefault();
      const next = clampWidth(widthRef.current + delta);
      setWidth(next);
      persist(next);
    },
  };

  return { width, gripProps };
}
