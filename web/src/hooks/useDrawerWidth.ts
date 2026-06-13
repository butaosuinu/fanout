import { useEffect, useReducer, useRef, useState } from "react";
import type { KeyboardEvent, PointerEvent } from "react";

export const DRAWER_DEFAULT_WIDTH = 840;
export const DRAWER_MIN_WIDTH = 320;
// ドロワーはコンテンツ領域(style.css の --maxw)より広げない。これより広い
// グリッドコンテナはそもそも無く、main 列を不当に潰すだけなので静的上限を
// コンテンツ幅に合わせる。--maxw を変えたらここも合わせること。
export const DRAWER_MAX_WIDTH = 1416;

// main 列に最低限残す幅(デスクトップ grid 時)。CSS の calc(100vw - 360px) と同期。
const MAIN_MIN = 360;
const STORAGE_KEY = "fanout.drawerWidth";
const RESIZING_CLASS = "drawer-resizing";
const KEY_STEP = 24;
// この移動量(px)を超えたドラッグだけ「リサイズ操作」とみなす(無移動クリックや
// 微調整 2 連発の dblclick 誤爆で intent を書き換えないため)。
const CLICK_SLOP = 4;

// intent(ユーザーが設定した幅)の静的な健全範囲だけを守る。ビューポート由来の
// 上限は描画時に別途かける(rendered)。
function clampIntent(w: number): number {
  return Math.min(DRAWER_MAX_WIDTH, Math.max(DRAWER_MIN_WIDTH, Math.round(w)));
}

/* 現在のビューポート/レイアウトで実際に描画できる最大幅。CSS の clamp と必ず
 * 一致させる: ≤1100px は overlay(グリッド外)なので 90vw、それ以上は grid 内
 * なので main 列に MAIN_MIN を残しつつコンテンツ幅(DRAWER_MAX_WIDTH)も超えない。 */
function viewportMax(): number {
  const vw = window.innerWidth;
  const cap = vw <= 1100 ? vw * 0.9 : Math.min(vw - MAIN_MIN, DRAWER_MAX_WIDTH);
  return Math.max(DRAWER_MIN_WIDTH, Math.floor(cap));
}

/* intent を「いま描画できる幅」に落とした値。--drawer-w と aria-valuenow に使う。
 * intent 自体は保持したまま、狭い画面では描画だけ縮める(広げれば intent に戻る)。 */
function renderedWidth(intent: number): number {
  return Math.min(intent, viewportMax());
}

/* 初期 intent は localStorage から同期 read(Drawer は key={selected} で remount
 * されるため、保存値さえ読めれば state は Drawer 内で完結する)。localStorage は
 * private mode で例外を投げるので try/catch で握りつぶす(useTheme と同じ)。
 * 不正値はデフォルトへ、範囲外は intent の静的範囲へ clamp。 */
function initialIntent(): number {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw) {
      const n = Number(raw);
      if (Number.isFinite(n) && n > 0) return clampIntent(n);
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

/* ドロワー幅の管理。intent(ユーザー設定値、静的 320–1416px・保存対象)と
 * rendered(intent を現在ビューポートで clamp した描画値)を分離する:
 * - 描画 (--drawer-w) と aria-valuenow は rendered。CSS の clamp と一致する。
 * - intent はビューポートが狭くなっても保持されるので、bottom sheet を経由
 *   したり画面を縮めて戻したりしても、設定した幅を失わず復元できる。
 * - ドラッグは「いま見えている幅(rendered)」を起点にするのでデッドゾーンが
 *   出ない。右アンカーなので左ドラッグで拡大。
 * setPointerCapture は jsdom に無いため optional chain で呼ぶ(解放は implicit
 * release に任せる)。永続化は実際に動いた pointerup / キー操作時のみ。 */
export function useDrawerWidth(): { width: number; gripProps: DrawerGripProps } {
  const [intent, setIntentState] = useState<number>(initialIntent);
  const intentRef = useRef(intent);
  const dragRef = useRef<{ pointerId: number; startX: number; startWidth: number } | null>(null);
  // 直前のドラッグが CLICK_SLOP を超えて動いたか(保存 / dblclick リセット判定)
  const movedRef = useRef(false);
  // ビューポート変化で rendered を再計算するための再描画トリガー(intent は不変)
  const [, bumpRender] = useReducer((c: number) => c + 1, 0);

  const setIntent = (w: number) => {
    intentRef.current = w;
    setIntentState(w);
  };

  /* ドラッグ中に unmount(pane 切替の remount)しても html に状態を残さない */
  useEffect(
    () => () => {
      document.documentElement.classList.remove(RESIZING_CLASS);
    },
    [],
  );

  /* ビューポートが変わったら rendered を再計算する(intent は触らない)。これに
   * より bottom sheet(≤820px で CSS が width:100%)を経由しても intent を縮め
   * ず、画面を広げれば設定値に戻る。 */
  useEffect(() => {
    const onResize = () => bumpRender();
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, []);

  const rendered = renderedWidth(intent);

  const endDrag = (e: PointerEvent<HTMLElement>): boolean => {
    const drag = dragRef.current;
    if (!drag || e.pointerId !== drag.pointerId) return false;
    dragRef.current = null;
    document.documentElement.classList.remove(RESIZING_CLASS);
    return true;
  };

  // 実際に動いたドラッグだけ intent を保存する(終了理由を問わず共通)
  const finishDrag = (e: PointerEvent<HTMLElement>) => {
    if (!dragRef.current) return;
    if (endDrag(e) && movedRef.current) persist(intentRef.current);
  };

  const gripProps: DrawerGripProps = {
    role: "separator",
    "aria-orientation": "vertical",
    "aria-label": "詳細パネルの幅を変更",
    "aria-valuenow": rendered,
    "aria-valuemin": DRAWER_MIN_WIDTH,
    "aria-valuemax": viewportMax(),
    tabIndex: 0,
    onPointerDown: (e) => {
      if (e.button !== 0 || !e.isPrimary || dragRef.current) return;
      // 起点は「いま見えている幅」= rendered。これで縮小後でもデッドゾーンなし。
      dragRef.current = { pointerId: e.pointerId, startX: e.clientX, startWidth: rendered };
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
      if (dx === 0) return; // 無移動では intent を触らない(rendered への巻き戻り防止)
      if (Math.abs(dx) > CLICK_SLOP) movedRef.current = true;
      setIntent(clampIntent(drag.startWidth + dx));
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
      setIntent(DRAWER_DEFAULT_WIDTH);
      try {
        localStorage.removeItem(STORAGE_KEY);
      } catch {
        /* private mode */
      }
    },
    onKeyDown: (e) => {
      if (e.altKey || e.metaKey || e.ctrlKey) return; // Alt+← は履歴等に譲る
      // 右アンカーなので ArrowLeft = セパレータを左へ = 拡大。キーボードは
      // 「いま見えている幅」起点で調整するので rendered を基準にする。
      const delta = e.key === "ArrowLeft" ? KEY_STEP : e.key === "ArrowRight" ? -KEY_STEP : 0;
      if (!delta) return;
      e.preventDefault();
      const next = clampIntent(rendered + delta);
      if (next === intentRef.current) return;
      setIntent(next);
      persist(next);
    },
  };

  return { width: rendered, gripProps };
}
