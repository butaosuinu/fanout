import { useSyncExternalStore } from "react";
import { activateLocale, detectLocale, isLocale, type Locale } from "../../i18n";
import { readLocal, writeLocal } from "../../shared/localStore";
import { normalizeDiffTheme } from "./diffThemes";

export type Theme = "light" | "dark";
export type Appearance = "system" | "light" | "dark";
/* 表示言語。auto = ブラウザ / OS の言語に追従(= キー不在)。 */
export type LocalePref = "auto" | Locale;
/* diff ビュアーの出し方。full = 全画面モーダル、compact = 詳細ドロワーの隣 */
export type DiffView = "full" | "compact";
/* 差分の並べ方。auto = 幅で split / stack を選ぶ、split = 左右 2 面、
 * stack = 削除と追加を縦積み */
export type DiffLayout = "auto" | "split" | "stack";
/* PR のマージ方式。gh pr merge の --squash / --merge / --rebase に 1:1 で対応する。 */
export type MergeMethod = "squash" | "merge" | "rebase";

const THEME_KEY = "fanout.theme";
const LOCALE_KEY = "fanout.locale";
const DIFF_LIGHT_KEY = "fanout.diffTheme.light";
const DIFF_DARK_KEY = "fanout.diffTheme.dark";
const DIFF_VIEW_KEY = "fanout.diffView";
const DIFF_LAYOUT_KEY = "fanout.diffLayout";
const DIFF_HIDE_VIEWED_KEY = "fanout.diffHideViewed";
const MERGE_METHOD_KEY = "fanout.mergeMethod";

/* 書き込み失敗(private mode / quota)時の退避は shared/localStore が持つ。退避が
 * 無いと snapshot が storage を読み直すたびに旧値へ戻り、設定操作が実質 no-op に
 * なる(外観に至っては data-theme だけ変わって radio が「システム」のまま残り、
 * 次の OS テーマ変更で上書きされる)。 */

/* 解決済みテーマの正は <html data-theme>。初期値は index.html の FOUC
 * ブートストラップが first paint 前に書き込み済み。 */
function currentTheme(): Theme {
  return document.documentElement.dataset.theme === "dark" ? "dark" : "light";
}

/* 明示選択がない(キー無し)= システム追従。FOUC ブートストラップと同じ意味論で、
 * キーそのものも値も従来のまま。 */
function currentMode(): Appearance {
  const stored = readLocal(THEME_KEY);
  return stored === "dark" || stored === "light" ? stored : "system";
}

/* 明示選択がない(キー無し)= ブラウザ / OS 追従。fanout.theme と同じ意味論。 */
function currentLocalePref(): LocalePref {
  const stored = readLocal(LOCALE_KEY);
  return isLocale(stored) ? stored : "auto";
}

function currentLocale(): Locale {
  const pref = currentLocalePref();
  return pref === "auto" ? detectLocale() : pref;
}

function currentDiffLight(): string {
  return normalizeDiffTheme(readLocal(DIFF_LIGHT_KEY), false);
}

function currentDiffDark(): string {
  return normalizeDiffTheme(readLocal(DIFF_DARK_KEY), true);
}

/* 既定は auto。キー無し = auto なので、未設定と明示 auto を区別しない。 */
function currentDiffLayout(): DiffLayout {
  const v = readLocal(DIFF_LAYOUT_KEY);
  return v === "split" || v === "stack" ? v : "auto";
}

/* 既定はコンパクト — 一覧や詳細を見ながら差分を追えるほうが導線として自然で、
 * 全画面はそこから広げる操作にする。キー無し = compact なので、未設定と明示
 * compact は区別しない。 */
function currentDiffView(): DiffView {
  return readLocal(DIFF_VIEW_KEY) === "full" ? "full" : "compact";
}

/* 確認済み file を本文と一覧から隠すか。既定は表示(キー無し)— 隠すのは
 * 読み進めたあとの操作で、開いた直後に file が消えている状態は事故に見える。 */
function currentDiffHideViewed(): boolean {
  return readLocal(DIFF_HIDE_VIEWED_KEY) === "hide";
}

/* マージ方式。既定は squash — fanout の子ブランチは 1 機能 1 ブランチで、
 * 途中のコミットを親の履歴に残す理由がない。キー無し = squash。 */
function currentMergeMethod(): MergeMethod {
  const v = readLocal(MERGE_METHOD_KEY);
  return v === "merge" || v === "rebase" ? v : "squash";
}

/* 設定は設定モーダルと diff オーバーレイから読まれるため module-level store で
 * 全インスタンスを同期する。OS テーマ変更(matchMedia)はユーザーが明示選択して
 * いない場合のみ追従し、listener は購読者がいる間だけ 1 本張る。
 *
 * 購読するのはどちらも「開いている間だけ」なので、常駐の購読者を App が持つ
 * (useSystemThemeSync)。無いと両方閉じた瞬間に listener が外れ、システム追従中に
 * OS の配色が変わってもページ全体が古い配色のまま取り残される。 */
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

/* 外観の matchMedia と対になる、表示言語のシステム追従。ブラウザの優先言語は実行中に
 * 変わりうる(OS の言語設定や、ブラウザの言語リスト並べ替え)。明示選択していない
 * あいだは追従しないと、表示と <html lang> が再読み込みまで旧言語で取り残される。 */
function onLanguageChange() {
  if (currentLocalePref() !== "auto") return;
  activateLocale(currentLocale());
  emit();
}

function subscribe(cb: () => void): () => void {
  if (listeners.size === 0) {
    mq = matchMedia("(prefers-color-scheme: dark)");
    mq.addEventListener("change", onSystemChange);
    window.addEventListener("languagechange", onLanguageChange);
  }
  listeners.add(cb);
  return () => {
    listeners.delete(cb);
    if (listeners.size === 0) {
      mq?.removeEventListener("change", onSystemChange);
      mq = null;
      window.removeEventListener("languagechange", onLanguageChange);
    }
  };
}

function setMode(mode: Appearance) {
  if (mode === "system") {
    writeLocal(THEME_KEY, null);
    document.documentElement.dataset.theme = matchMedia("(prefers-color-scheme: dark)").matches
      ? "dark"
      : "light";
  } else {
    writeLocal(THEME_KEY, mode);
    document.documentElement.dataset.theme = mode;
  }
  emit();
}

function setLocalePref(pref: LocalePref) {
  writeLocal(LOCALE_KEY, pref === "auto" ? null : pref);
  activateLocale(currentLocale());
  emit();
}

function setDiffLight(name: string) {
  writeLocal(DIFF_LIGHT_KEY, normalizeDiffTheme(name, false));
  emit();
}

function setDiffDark(name: string) {
  writeLocal(DIFF_DARK_KEY, normalizeDiffTheme(name, true));
  emit();
}

function setDiffLayout(layout: DiffLayout) {
  writeLocal(DIFF_LAYOUT_KEY, layout === "auto" ? null : layout);
  emit();
}

function setDiffView(view: DiffView) {
  writeLocal(DIFF_VIEW_KEY, view === "full" ? "full" : null);
  emit();
}

function setDiffHideViewed(hide: boolean) {
  writeLocal(DIFF_HIDE_VIEWED_KEY, hide ? "hide" : null);
  emit();
}

function setMergeMethod(method: MergeMethod) {
  writeLocal(MERGE_METHOD_KEY, method === "squash" ? null : method);
  emit();
}

/* App が張る常駐購読。値は使わない — matchMedia listener をアプリの生存期間ぶん
 * 保つためだけに購読する。解決済みテーマを snapshot にしてあるので、OS の配色が
 * 変わったときだけ App が再レンダーする。 */
export function useSystemThemeSync(): void {
  useSyncExternalStore(subscribe, currentTheme);
}

/* 表示言語。mode は設定モーダルの選択(auto を含む)、locale は解決済みの表示言語。 */
export function useLocale(): {
  mode: LocalePref;
  locale: Locale;
  setLocale: (pref: LocalePref) => void;
} {
  const mode = useSyncExternalStore(subscribe, currentLocalePref);
  const locale = useSyncExternalStore(subscribe, currentLocale);
  return { mode, locale, setLocale: setLocalePref };
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

/* 確認済み file を隠すか。オーバーレイのヘッダから直接触るので、並べ方や表示
 * モードと同じくここに載せて次回の表示にも引き継ぐ。 */
export function useDiffHideViewed(): {
  hideViewed: boolean;
  setHideViewed: (hide: boolean) => void;
} {
  return {
    hideViewed: useSyncExternalStore(subscribe, currentDiffHideViewed),
    setHideViewed: setDiffHideViewed,
  };
}

/* マージ方式。マージボタンから直接触るので設定モーダルには出さず、
 * 値だけこの store に載せる(diff の表示モードと同じ扱い)。ドロワーと diff
 * ツールバーの 2 か所に同じボタンが出るため、共有はこの store が担う。 */
export function useMergeOptions(): {
  method: MergeMethod;
  setMethod: (method: MergeMethod) => void;
} {
  return {
    method: useSyncExternalStore(subscribe, currentMergeMethod),
    setMethod: setMergeMethod,
  };
}

/* 初期ロケールの適用。I18nProvider は locale 未設定だと children を描かないので、
 * 最初の render より前に済ませておく必要がある。設定を持つのはこのモジュールだけ
 * なので、エントリポイント(main.tsx / テストの setup)に覚えさせず、ここで一度だけ
 * 走らせる — App は必ずこのモジュールを import するため、どの経路から render しても
 * 解決済みロケールで始まる。解決規則は index.html のブートストラップと同じ。 */
activateLocale(currentLocale());
