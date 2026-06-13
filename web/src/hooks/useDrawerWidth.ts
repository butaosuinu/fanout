import { useEffect, useRef, useState } from "react";
import type { KeyboardEvent, PointerEvent } from "react";

export const DRAWER_DEFAULT_WIDTH = 840;
export const DRAWER_MIN_WIDTH = 320;
export const DRAWER_MAX_WIDTH = 1600;

const STORAGE_KEY = "fanout.drawerWidth";
const RESIZING_CLASS = "drawer-resizing";
const KEY_STEP = 24;
// この移動量(px)を超えたドラッグ直後の dblclick はリセットとして扱わない
// (微調整ドラッグ 2 連発が dblclick に化けて幅を吹き飛ばすのを防ぐ)
const CLICK_SLOP = 4;

/* CSS 側のビューポート上限(calc(100vw - 360px) / ≤1100px は 90vw)と同じ値。
 * 描画は CSS の clamp が常に守るが、操作値・保存値・aria-valuenow も実描画を
 * 超えないようここでも同じ上限でクランプする(超えると逆ドラッグにデッド
 * ゾーンができ、aria が実幅と乖離する)。 */
function viewportMax(): number {
  const vw = window.innerWidth;
  const cssMax = vw <= 1100 ? vw * 0.9 : vw - 360;
  return Math.min(DRAWER_MAX_WIDTH, Math.max(DRAWER_MIN_WIDTH, Math.floor(cssMax)));
}

function clampWidth(w: number): number {
  return Math.min(viewportMax(), Math.max(DRAWER_MIN_WIDTH, Math.round(w)));
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
  return Math.min(DRAWER_DEFAULT_WIDTH, viewportMax());
}

function persist(w: number) {
  try {
    localStorage.setItem(STORAGE_KEY, String(w));
  } catch {
    /* private mode */
  }
}

export interface DrawerGripProps {
  role: "separator";
  "aria-orientation": "vertical";
  "aria-label": string;
  "aria-valuenow": number;
  "aria-valuemin": number;
  "aria-valuemax": number;
  tabIndex: 0;
  onPointerDown: (e: PointerEvent<HTMLElement>) => void;
  onPointerMove: (e: PointerEvent<HTMLElement>) => void;
  onPointerUp: (e: PointerEvent<HTMLElement>) => void;
  onPointerCancel: (e: PointerEvent<HTMLElement>) => void;
  onLostPointerCapture: (e: PointerEvent<HTMLElement>) => void;
  onDoubleClick: () => void;
  onKeyDown: (e: KeyboardEvent<HTMLElement>) => void;
}

/* ドロワー幅(px)の管理。値は CSS 変数 --drawer-w としてだけ反映する。
 * 描画上のクランプは CSS の clamp が一次防衛で、ここでは操作値も同じ
 * ビューポート上限(viewportMax)に揃えて aria と保存値の正直さを保つ。
 * ドロワーは右アンカーなので左ドラッグで拡大。setPointerCapture は jsdom に
 * 存在しないため optional chain で呼ぶ(pointerup での解放は implicit release に
 * 任せる)。永続化は「幅が実際に変わった」pointerup / キー操作時のみ —
 * 無移動クリックで保存すると、保存値なし(=将来のデフォルト変更に追従)の
 * ユーザーを暗黙にオプトアウトさせてしまう。 */
export function useDrawerWidth(): { width: number; gripProps: DrawerGripProps } {
  const [width, setWidthState] = useState<number>(initialWidth);
  const widthRef = useRef(width);
  const dragRef = useRef<{ pointerId: number; startX: number; startWidth: number } | null>(null);
  // 直前のドラッグが CLICK_SLOP を超えて動いたか(dblclick リセット抑制用)
  const movedRef = useRef(false);

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

  /* ビューポート縮小で CSS が描画幅を clamp したら、内部値・aria-valuenow・
   * 永続対象も同じ上限へ追従させる。追従しないと widthRef.current が古い
   * ワイド値のまま残り、次ドラッグの startWidth が実描画より大きくなって
   * デッドゾーン(右へ大きく動かすまで縮まらない)を生み、aria も実幅と
   * 乖離する。 */
  useEffect(() => {
    const onResize = () => {
      const clamped = clampWidth(widthRef.current);
      if (clamped !== widthRef.current) setWidth(clamped);
    };
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
    // setWidth / widthRef は安定参照。マウント中ずっと同じリスナーでよい。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const endDrag = (e: PointerEvent<HTMLElement>): boolean => {
    const drag = dragRef.current;
    if (!drag || e.pointerId !== drag.pointerId) return false;
    dragRef.current = null;
    document.documentElement.classList.remove(RESIZING_CLASS);
    return true;
  };

  // 幅が変わったドラッグだけ保存する(終了理由を問わず共通)
  const finishDrag = (e: PointerEvent<HTMLElement>) => {
    const drag = dragRef.current;
    if (!drag) return;
    const changed = widthRef.current !== drag.startWidth;
    if (endDrag(e) && changed) persist(widthRef.current);
  };

  const gripProps: DrawerGripProps = {
    role: "separator",
    "aria-orientation": "vertical",
    "aria-label": "詳細パネルの幅を変更",
    "aria-valuenow": width,
    "aria-valuemin": DRAWER_MIN_WIDTH,
    "aria-valuemax": DRAWER_MAX_WIDTH,
    tabIndex: 0,
    onPointerDown: (e) => {
      if (e.button !== 0 || !e.isPrimary || dragRef.current) return;
      // resize イベントを取り逃していても現在ビューポートの上限から開始する
      const startWidth = clampWidth(widthRef.current);
      if (startWidth !== widthRef.current) setWidth(startWidth);
      dragRef.current = { pointerId: e.pointerId, startX: e.clientX, startWidth };
      movedRef.current = false;
      e.currentTarget.setPointerCapture?.(e.pointerId);
      document.documentElement.classList.add(RESIZING_CLASS);
    },
    onPointerMove: (e) => {
      const drag = dragRef.current;
      if (!drag || e.pointerId !== drag.pointerId) return;
      // capture が効かない環境でボタン解放を取り逃した場合の保険
      if (e.buttons === 0) {
        finishDrag(e);
        return;
      }
      const dx = drag.startX - e.clientX;
      if (Math.abs(dx) > CLICK_SLOP) movedRef.current = true;
      setWidth(clampWidth(drag.startWidth + dx));
    },
    onPointerUp: finishDrag,
    onPointerCancel: (e) => {
      endDrag(e); // OS 都合の中断は永続化しない(次回 remount は直前の保存値)
    },
    // capture 喪失(≤820px への resize で grip が display:none になる等)でも
    // ドラッグ状態と html クラスを残さない
    onLostPointerCapture: finishDrag,
    onDoubleClick: () => {
      if (movedRef.current) return; // 微調整ドラッグ 2 連発の誤爆を抑制
      setWidth(Math.min(DRAWER_DEFAULT_WIDTH, viewportMax()));
      try {
        localStorage.removeItem(STORAGE_KEY);
      } catch {
        /* private mode */
      }
    },
    onKeyDown: (e) => {
      if (e.altKey || e.metaKey || e.ctrlKey) return; // Alt+← は履歴等に譲る
      // 右アンカーなので ArrowLeft = セパレータを左へ = 拡大
      const delta = e.key === "ArrowLeft" ? KEY_STEP : e.key === "ArrowRight" ? -KEY_STEP : 0;
      if (!delta) return;
      e.preventDefault();
      const next = clampWidth(widthRef.current + delta);
      if (next === widthRef.current) return;
      setWidth(next);
      persist(next);
    },
  };

  return { width, gripProps };
}