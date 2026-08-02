import { useEffect, useReducer, useRef, useState } from "react";
import type { KeyboardEvent, PointerEvent } from "react";

const KEY_STEP = 24;
// この移動量(px)を超えたドラッグだけ「リサイズ操作」とみなす(無移動クリックや
// 微調整 2 連発の dblclick 誤爆で intent を書き換えないため)。
const CLICK_SLOP = 4;

export interface PanelWidthConfig {
  /* 保存キー。private mode の例外は握りつぶす */
  storageKey: string;
  /* 幅の既定値と、intent(ユーザー設定値)の静的な健全範囲 */
  defaultWidth: number;
  minWidth: number;
  maxWidth: number;
  /* 現在のビューポート/レイアウトで実際に描画できる最大幅。CSS 側の上限と必ず
   * 一致させること。呼び出しごとに評価するので、隣接要素の実測にも使える。 */
  viewportMax: () => number;
  /* グリップの aria-label */
  label: string;
  /* ドラッグ中に html へ付けるクラス(カーソルと選択抑止の CSS が拾う) */
  resizingClass: string;
}

// intent(ユーザーが設定した幅)の静的な健全範囲だけを守る。ビューポート由来の
// 上限は描画時に別途かける(rendered)。
function clampIntent(w: number, cfg: PanelWidthConfig): number {
  return Math.min(cfg.maxWidth, Math.max(cfg.minWidth, Math.round(w)));
}

function viewportMax(cfg: PanelWidthConfig): number {
  return Math.max(cfg.minWidth, Math.floor(cfg.viewportMax()));
}

/* intent を「いま描画できる幅」に落とした値。CSS 変数と aria-valuenow に使う。
 * intent 自体は保持したまま、狭い画面では描画だけ縮める(広げれば intent に戻る)。 */
function renderedWidth(intent: number, cfg: PanelWidthConfig): number {
  return Math.min(intent, viewportMax(cfg));
}

/* 初期 intent は localStorage から同期 read(パネルは remount されうるため、
 * 保存値さえ読めれば state はパネル内で完結する)。localStorage は private mode
 * で例外を投げるので try/catch で握りつぶす(useSettings と同じ)。
 * 不正値はデフォルトへ、範囲外は intent の静的範囲へ clamp。 */
function initialIntent(cfg: PanelWidthConfig): number {
  try {
    const raw = localStorage.getItem(cfg.storageKey);
    if (raw) {
      const n = Number(raw);
      if (Number.isFinite(n) && n > 0) return clampIntent(n, cfg);
    }
  } catch {
    /* private mode */
  }
  return cfg.defaultWidth;
}

function persist(w: number, cfg: PanelWidthConfig) {
  try {
    localStorage.setItem(cfg.storageKey, String(w));
  } catch {
    /* private mode */
  }
}

export interface PanelGripProps {
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

/* 右アンカーのパネル幅の管理(詳細ドロワーと、コンパクト表示の diff パネル)。
 * intent(ユーザー設定値・保存対象)と rendered(intent を現在ビューポートで
 * clamp した描画値)を分離する:
 * - 描画に渡す幅と aria-valuenow は rendered。CSS 側の上限と一致する。
 * - intent はビューポートが狭くなっても保持されるので、bottom sheet を経由
 *   したり画面を縮めて戻したりしても、設定した幅を失わず復元できる。
 * - ドラッグは「いま見えている幅(rendered)」を起点にするのでデッドゾーンが
 *   出ない。右アンカーなので左ドラッグで拡大。
 * setPointerCapture は jsdom に無いため optional chain で呼ぶ(解放は implicit
 * release に任せる)。永続化は実際に動いた pointerup / キー操作時のみ。 */
export function usePanelWidth(cfg: PanelWidthConfig): { width: number; gripProps: PanelGripProps } {
  const [intent, setIntentState] = useState<number>(() => initialIntent(cfg));
  const intentRef = useRef(intent);
  /* startIntent はドラッグ開始時点の intent を焼き付ける。ドラッグ中に
   * intentRef を書き換えるので、live な値を「開始時の intent」として読むと
   * 2 回目以降の pointermove を自分で弾いてしまう(下の拡大ガードを参照)。 */
  const dragRef = useRef<{
    pointerId: number;
    startX: number;
    startWidth: number;
    startIntent: number;
  } | null>(null);
  // 直前のドラッグが CLICK_SLOP を超えて動いたか(保存 / dblclick リセット判定)
  const movedRef = useRef(false);
  // ビューポート変化で rendered を再計算するための再描画トリガー(intent は不変)
  const [, bumpRender] = useReducer((c: number) => c + 1, 0);

  const setIntent = (w: number) => {
    intentRef.current = w;
    setIntentState(w);
  };

  /* ドラッグ中に unmount(pane 切替の remount)しても html に状態を残さない。
   * クラス名は呼び出しごとに固定なので、deps に入れても再実行されない。 */
  const { resizingClass } = cfg;
  useEffect(
    () => () => {
      document.documentElement.classList.remove(resizingClass);
    },
    [resizingClass],
  );

  /* ビューポートが変わったら rendered を再計算する(intent は触らない)。これに
   * より bottom sheet(≤820px で CSS が width:100%)を経由しても intent を縮め
   * ず、画面を広げれば設定値に戻る。 */
  useEffect(() => {
    const onResize = () => bumpRender();
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, []);

  const rendered = renderedWidth(intent, cfg);

  const endDrag = (e: PointerEvent<HTMLElement>): boolean => {
    const drag = dragRef.current;
    if (!drag || e.pointerId !== drag.pointerId) return false;
    dragRef.current = null;
    document.documentElement.classList.remove(cfg.resizingClass);
    return true;
  };

  // 実際に動いたドラッグだけ intent を保存する(終了理由を問わず共通)
  const finishDrag = (e: PointerEvent<HTMLElement>) => {
    if (!dragRef.current) return;
    if (endDrag(e) && movedRef.current) persist(intentRef.current, cfg);
  };

  const gripProps: PanelGripProps = {
    role: "separator",
    "aria-orientation": "vertical",
    "aria-label": cfg.label,
    "aria-valuenow": rendered,
    "aria-valuemin": cfg.minWidth,
    "aria-valuemax": viewportMax(cfg),
    tabIndex: 0,
    onPointerDown: (e) => {
      if (e.button !== 0 || !e.isPrimary || dragRef.current) return;
      // 起点は「いま見えている幅」= rendered。これで縮小後でもデッドゾーンなし。
      dragRef.current = {
        pointerId: e.pointerId,
        startX: e.clientX,
        startWidth: rendered,
        startIntent: intentRef.current,
      };
      movedRef.current = false;
      e.currentTarget.setPointerCapture?.(e.pointerId);
      document.documentElement.classList.add(cfg.resizingClass);
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
      const candidate = clampIntent(drag.startWidth + dx, cfg);
      // 描画上限に張り付いた状態(開始時 intent が見えている startWidth より
      // 大きい)でさらに拡大方向へ引いても描画は変わらない。ここで intent を
      // startWidth ベースに縮めると、広い画面で見えるはずの大きい保存値を失う。
      // 拡大方向は intent を維持し、見えている幅より縮めたときだけ追従する。
      // 判定は必ず startIntent で行う — live な intentRef を見ると、1 回目の
      // 拡大で intent > startWidth になった時点で以降の move を全部弾き、
      // ドラッグが 1 ステップで固まる。
      if (candidate >= drag.startWidth && drag.startIntent > drag.startWidth) {
        movedRef.current = true; // ジェスチャーはあったので click 扱いにしない
        return;
      }
      if (Math.abs(dx) > CLICK_SLOP) movedRef.current = true;
      setIntent(candidate);
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
      setIntent(cfg.defaultWidth);
      try {
        localStorage.removeItem(cfg.storageKey);
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
      const next = clampIntent(rendered + delta, cfg);
      // 描画上限に張り付いた状態の拡大はドラッグと同じく no-op にする。capped
      // な rendered から intent を再計算すると、広い画面で見えるはずの大きい
      // 保存値を縮めてしまうため。
      if (next >= rendered && intentRef.current > rendered) return;
      if (next === intentRef.current) return;
      setIntent(next);
      persist(next, cfg);
    },
  };

  return { width: rendered, gripProps };
}
