import { useEffect, useState } from "react";

export type Theme = "light" | "dark";

const STORAGE_KEY = "fanout.theme";

function currentTheme(): Theme {
  return document.documentElement.dataset.theme === "dark" ? "dark" : "light";
}

/* テーマの初期値は index.html の FOUC ブートストラップが first paint 前に
 * <html data-theme> へ書き込み済み — ここではそれを引き継ぐだけ。toggle は
 * localStorage に永続化し、OS テーマ変更(matchMedia)はユーザーが明示選択して
 * いない場合のみ追従する。localStorage は private mode で例外を投げるので
 * try/catch で握りつぶす。 */
export function useTheme(): { theme: Theme; toggle: () => void } {
  const [theme, setTheme] = useState<Theme>(currentTheme);

  useEffect(() => {
    const mq = matchMedia("(prefers-color-scheme: dark)");
    const onChange = (e: MediaQueryListEvent) => {
      try {
        if (localStorage.getItem(STORAGE_KEY)) return;
      } catch {
        /* ignore */
      }
      const next: Theme = e.matches ? "dark" : "light";
      document.documentElement.dataset.theme = next;
      setTheme(next);
    };
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  }, []);

  const toggle = () => {
    const next: Theme = currentTheme() === "dark" ? "light" : "dark";
    document.documentElement.dataset.theme = next;
    try {
      localStorage.setItem(STORAGE_KEY, next);
    } catch {
      /* private mode */
    }
    setTheme(next);
  };

  return { theme, toggle };
}
