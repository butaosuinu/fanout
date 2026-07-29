import { useSyncExternalStore } from "react";

export type Theme = "light" | "dark";

const STORAGE_KEY = "fanout.theme";

function currentTheme(): Theme {
  return document.documentElement.dataset.theme === "dark" ? "dark" : "light";
}

/* 正は <html data-theme>。初期値は index.html の FOUC ブートストラップが
 * first paint 前に書き込み済み。hook は複数箇所(Nav の toggle と diff
 * オーバーレイ)で使われるため module-level store で全インスタンスを同期する。
 * OS テーマ変更(matchMedia)はユーザーが明示選択していない場合のみ追従し、
 * listener は購読者がいる間だけ 1 本張る。localStorage は private mode で
 * 例外を投げるので try/catch で握りつぶす。 */
const listeners = new Set<() => void>();
let mq: MediaQueryList | null = null;

function emit() {
  for (const l of listeners) l();
}

function onSystemChange(e: MediaQueryListEvent) {
  try {
    if (localStorage.getItem(STORAGE_KEY)) return;
  } catch {
    /* ignore */
  }
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

function toggle() {
  const next: Theme = currentTheme() === "dark" ? "light" : "dark";
  document.documentElement.dataset.theme = next;
  try {
    localStorage.setItem(STORAGE_KEY, next);
  } catch {
    /* private mode */
  }
  emit();
}

export function useTheme(): { theme: Theme; toggle: () => void } {
  const theme = useSyncExternalStore(subscribe, currentTheme);
  return { theme, toggle };
}
