import { useSyncExternalStore } from "react";
import { normalizeDiffTheme } from "../lib/diffThemes";

export type Theme = "light" | "dark";
export type Appearance = "system" | "light" | "dark";
/* diff ビュアーの出し方。full = 全画面モーダル、compact = 詳細ドロワーの隣 */
export type DiffView = "full" | "compact";
/* 差分の並べ方。auto = 幅で split / stack を選ぶ、split = 左右 2 面、
 * stack = 削除と追加を縦積み */
export type DiffLayout = "auto" | "split" | "stack";

const THEME_KEY = "fanout.theme";
const DIFF_LIGHT_KEY = "fanout.diffTheme.light";
const DIFF_DARK_KEY = "fanout.diffTheme.dark";
const DIFF_VIEW_KEY = "fanout.diffView";
const DIFF_LAYOUT_KEY = "fanout.diffLayout";

/* localStorage は private mode で例外を投げるので、読み書きとも握りつぶす。 */
function read(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function write(key: string, value: string | null) {
  try {
    if (value === null) localStorage.removeItem(key);
    else localStorage.setItem(key, value);
  } catch {
    /* private mode */
  }
}

/* 解決済みテーマの正は <html data-theme>。初期値は index.html の FOUC
 * ブートストラップが first paint 前に書き込み済み。 */
function currentTheme(): Theme {
  return document.documentElement.dataset.theme === "dark" ? "dark" : "light";
}

/* 明示選択がない(キー無し)= システム追従。FOUC ブートストラップと同じ意味論で、
 * キーそのものも値も従来のまま。 */
function currentMode(): Appearance {
  const stored = read(THEME_KEY);
  return stored === "dark" || stored === "light" ? stored : "system";
}

function currentDiffLight(): string {
  return normalizeDiffTheme(read(DIFF_LIGHT_KEY), false);
}

function currentDiffDark(): string {
  return normalizeDiffTheme(read(DIFF_DARK_KEY), true);
}

/* 既定は auto。キー無し = auto なので、未設定と明示 auto を区別しない。 */
function currentDiffLayout(): DiffLayout {
  const v = read(DIFF_LAYOUT_KEY);
  return v === "split" || v === "stack" ? v : "auto";
}

/* 既定はコンパクト — 一覧や詳細を見ながら差分を追えるほうが導線として自然で、
 * 全画面はそこから広げる操作にする。キー無し = compact なので、未設定と明示
 * compact は区別しない。 */
function currentDiffView(): DiffView {
  return read(DIFF_VIEW_KEY) === "full" ? "full" : "compact";
}

/* 設定は Nav の歯車、設定モーダル、diff オーバーレイの 3 か所から読まれるため
 * module-level store で全インスタンスを同期する。OS テーマ変更(matchMedia)は
 * ユーザーが明示選択していない場合のみ追従し、listener は購読者がいる間だけ
 * 1 本張る。 */
const listeners = new Set<() => void>();
let mq: MediaQueryList | null = null;

function emit() {
  for (const l of listeners) l();
}

function onSystemChange(e: MediaQueryListEvent) {
  if (currentMode() !== "system") return;
  document.documentElement.dataset.theme = e.matches ? "dark" : "light";
  emit();
}

function subscribe(cb: () => void): () => void {
  if (listeners.size === 0) {
    mq = matchMedia("(prefers-color-scheme: dark)");
    mq.addEventListener("change", onSystemChange);
  }
  listeners.add(cb);
  return () => {
    listeners.delete(cb);
    if (listeners.size === 0) {
      mq?.removeEventListener("change", onSystemChange);
      mq = null;
    }
  };
}

function setMode(mode: Appearance) {
  if (mode === "system") {
    write(THEME_KEY, null);
    document.documentElement.dataset.theme = matchMedia("(prefers-color-scheme: dark)").matches
      ? "dark"
      : "light";
  } else {
    write(THEME_KEY, mode);
    document.documentElement.dataset.theme = mode;
  }
  emit();
}

function setDiffLight(name: string) {
  write(DIFF_LIGHT_KEY, normalizeDiffTheme(name, false));
  emit();
}

function setDiffDark(name: string) {
  write(DIFF_DARK_KEY, normalizeDiffTheme(name, true));
  emit();
}

function setDiffLayout(layout: DiffLayout) {
  write(DIFF_LAYOUT_KEY, layout === "auto" ? null : layout);
  emit();
}

function setDiffView(view: DiffView) {
  write(DIFF_VIEW_KEY, view === "full" ? "full" : null);
  emit();
}

/* 解決済みの light/dark だけが要る箇所用(diff オーバーレイの themeType など)。 */
export function useTheme(): { theme: Theme } {
  return { theme: useSyncExternalStore(subscribe, currentTheme) };
}

export function useAppearance(): {
  mode: Appearance;
  theme: Theme;
  setMode: (mode: Appearance) => void;
} {
  const mode = useSyncExternalStore(subscribe, currentMode);
  const theme = useSyncExternalStore(subscribe, currentTheme);
  return { mode, theme, setMode };
}

export function useDiffTheme(): {
  light: string;
  dark: string;
  setLight: (name: string) => void;
  setDark: (name: string) => void;
} {
  const light = useSyncExternalStore(subscribe, currentDiffLight);
  const dark = useSyncExternalStore(subscribe, currentDiffDark);
  return { light, dark, setLight: setDiffLight, setDark: setDiffDark };
}

/* diff の全画面 / コンパクトは切替ボタンから直接触るので、設定モーダルではなく
 * オーバーレイのヘッダに置く。値は同じ store に載せて次回の表示にも引き継ぐ。 */
export function useDiffView(): { view: DiffView; setView: (view: DiffView) => void } {
  return { view: useSyncExternalStore(subscribe, currentDiffView), setView: setDiffView };
}

/* 差分の並べ方。auto は幅で決めるので、解決は描画側(DiffOverlay)が持つ。 */
export function useDiffLayout(): {
  layout: DiffLayout;
  setLayout: (layout: DiffLayout) => void;
} {
  return { layout: useSyncExternalStore(subscribe, currentDiffLayout), setLayout: setDiffLayout };
}
