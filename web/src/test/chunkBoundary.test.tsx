import { render, screen } from "@testing-library/react";
import { Suspense, lazy } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ChunkBoundary } from "../ui/ChunkBoundary";

/* lazy chunk の取得は失敗しうる(サーバ更新で古い chunk が 404、ネットワーク断)。
 * Suspense は解決待ちしか扱わないので、境界が無いと reject した例外がルートまで
 * 抜け、React が木ごと unmount する = ダッシュボードが白紙になる。 */
describe("ChunkBoundary", () => {
  beforeEach(() => {
    // 境界が拾ったことは fallback で見る。React の重複ログは出させない
    vi.spyOn(console, "error").mockImplementation(() => {});
  });
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("chunk の取得に失敗しても兄弟を巻き添えにしない", async () => {
    const Broken = lazy(() =>
      Promise.reject(new Error("Failed to fetch dynamically imported module")),
    );
    render(
      <>
        <p>背面はそのまま</p>
        <ChunkBoundary fallback={<p>読み込めませんでした</p>}>
          <Suspense fallback={<p>読込中</p>}>
            <Broken />
          </Suspense>
        </ChunkBoundary>
      </>,
    );

    expect(await screen.findByText("読み込めませんでした")).toBeInTheDocument();
    expect(screen.getByText("背面はそのまま")).toBeInTheDocument();
  });

  it("解決すれば素通しする", async () => {
    const Ok = lazy(() => Promise.resolve({ default: () => <p>中身</p> }));
    render(
      <ChunkBoundary fallback={<p>読み込めませんでした</p>}>
        <Suspense fallback={<p>読込中</p>}>
          <Ok />
        </Suspense>
      </ChunkBoundary>,
    );

    expect(await screen.findByText("中身")).toBeInTheDocument();
    expect(screen.queryByText("読み込めませんでした")).not.toBeInTheDocument();
  });
});
