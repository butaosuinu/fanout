import "@testing-library/jest-dom/vitest";
import { configure } from "@testing-library/react";
import { activateLocale } from "../i18n";
import { server } from "./server";

/* findBy* の既定待ち時間(1s)は、遅延 chunk を待つケースには足りない。diff
 * オーバーレイの chunk は @pierre/diffs(Shiki 込み)を引くので初回解決が重く、
 * ファイル並列でワーカーが CPU を取り合うと 1s を超えて false negative になる。
 * 正常時は待たずに解決するので、上げても実行時間は伸びない。 */
configure({ asyncUtilTimeout: 5000 });

/* アプリの fetch は相対 URL(/api/…)のまま。素の Node fetch は相対 URL を
 * 受け付けないが、server.listen() が globalThis.fetch を MSW のインターセプタに
 * 差し替え、それが jsdom の location.origin を基準に相対 URL を解決するので、
 * 追加の絶対化シムは不要(入れても MSW の手前には来ない)。 */

/* jsdom の navigator.language は既定で "en-US"。表示ロケールはブラウザ言語から
 * 決まるので、そのままだと日本語で書かれた既存アサーションが英語カタログで落ちる。
 * ブラウザ言語を ja に固定する — 各テストの beforeEach が localStorage.clear() を
 * 呼ぶため、localStorage に fanout.locale を仕込む手は使えない。英語表示を見たい
 * テストは設定モーダルの操作か activateLocale("en") で明示的に切り替えること。 */
Object.defineProperty(window.navigator, "languages", {
  value: ["ja-JP", "ja"],
  configurable: true,
});
Object.defineProperty(window.navigator, "language", { value: "ja-JP", configurable: true });

/* すべてのテストファイルでロケールを活性化しておく。I18nProvider は locale が
 * 未設定だと children を描かないので、useSettings を import しないファイル
 * (hook 単体テストなど)では、これが無いと空の DOM だけが残る。 */
activateLocale("ja");

/* jsdom は matchMedia を実装しない。テーマ初期値の判定(useTheme / FOUC 相当)
 * が動くだけの最小スタブを入れる(常に light)。 */
if (typeof window.matchMedia !== "function") {
  window.matchMedia = (query: string): MediaQueryList =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }) as MediaQueryList;
}

/* jsdom は ResizeObserver を実装しない。@pierre/diffs(diff オーバーレイの描画)の
 * ResizeManager が参照するため、観測しない最小スタブを入れる。 */
if (typeof globalThis.ResizeObserver !== "function") {
  class ResizeObserverStub {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  globalThis.ResizeObserver = ResizeObserverStub as unknown as typeof ResizeObserver;
}

/* jsdom は IntersectionObserver も実装しない。@pierre/diffs の Virtualizer が
 * setup で 1 個生成するため、観測しない最小スタブを入れる。可視判定自体は
 * Virtualizer.isInstanceVisible(スクロール位置ベース)にも冗長化されているので、
 * observer が何も通知しなくても初期描画は成立する。 */
if (typeof globalThis.IntersectionObserver !== "function") {
  class IntersectionObserverStub {
    readonly root = null;
    readonly rootMargin = "";
    readonly thresholds: readonly number[] = [];
    observe() {}
    unobserve() {}
    disconnect() {}
    takeRecords(): IntersectionObserverEntry[] {
      return [];
    }
  }
  globalThis.IntersectionObserver =
    IntersectionObserverStub as unknown as typeof IntersectionObserver;
}

/* jsdom はレイアウトを持たないので scrollIntoView も実装しない。サイドバーから
 * 本文へ飛ぶ導線が例外で落ちないよう no-op を入れる。 */
if (typeof Element.prototype.scrollIntoView !== "function") {
  Element.prototype.scrollIntoView = () => {};
}

beforeAll(() => server.listen({ onUnhandledRequest: "error" }));
afterEach(() => server.resetHandlers());
afterAll(() => server.close());
