import "@testing-library/jest-dom/vitest";
import { server } from "./server";

/* アプリの fetch は相対 URL(/api/…)のまま。素の Node fetch は相対 URL を
 * 受け付けないが、server.listen() が globalThis.fetch を MSW のインターセプタに
 * 差し替え、それが jsdom の location.origin を基準に相対 URL を解決するので、
 * 追加の絶対化シムは不要(入れても MSW の手前には来ない)。 */

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

beforeAll(() => server.listen({ onUnhandledRequest: "error" }));
afterEach(() => server.resetHandlers());
afterAll(() => server.close());
