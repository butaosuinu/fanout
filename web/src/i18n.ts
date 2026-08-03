import { i18n } from "@lingui/core";
import { messages as en } from "./locales/en.po";
import { messages as ja } from "./locales/ja.po";

export const LOCALES = ["ja", "en"] as const;
export type Locale = (typeof LOCALES)[number];

/* ソースロケール。カタログに無いメッセージはここへ落ちる。 */
export const SOURCE_LOCALE: Locale = "ja";

/* ロケールは 2 つで数百メッセージなので静的 import で載せる。動的 import にすると
 * activate が非同期になり、I18nProvider が locale 未設定のあいだ null を返す
 * (= 初回描画が空になる)ため、同期で完結させる。 */
i18n.load({ ja, en });

export function isLocale(v: unknown): v is Locale {
  return LOCALES.includes(v as Locale);
}

/* ブラウザ / OS の言語から表示ロケールを決める。日本語系以外はすべて英語。 */
export function detectLocale(): Locale {
  const tags = typeof navigator === "undefined" ? [] : (navigator.languages ?? []);
  const candidates = tags.length ? tags : [navigator?.language ?? ""];
  for (const tag of candidates) {
    const primary = String(tag).toLowerCase().split("-")[0];
    if (primary === "ja") return "ja";
    if (primary === "en") return "en";
  }
  return "en";
}

/* 表示ロケールを切り替える。<html lang> は支援技術の読み上げ言語と行分割に効くので
 * 必ず一緒に動かす (first paint 前の初期値は index.html のブートストラップが書く)。 */
export function activateLocale(locale: Locale): void {
  i18n.activate(locale);
  document.documentElement.lang = locale;
}

/* import しただけで locale が決まっている状態にする。I18nProvider は
 * i18n.locale が空だと children を描画しないため、activate 前に render が走る
 * 経路 (テストの render(<App/>) など) を作らない。 */
i18n.activate(SOURCE_LOCALE);
