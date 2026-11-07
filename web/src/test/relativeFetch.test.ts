import { http, HttpResponse } from "msw";
import { describe, expect, it, onTestFinished, vi } from "vitest";
import { server } from "./server";

/* setup.ts のコメントの実証テスト: アプリは相対 URL(/api/…)で fetch するが、
 * server.listen() 後の globalThis.fetch は MSW のインターセプタであり、jsdom の
 * location.origin を基準に相対 URL を解決する — 絶対化シムは不要。msw の
 * バージョン更新でこの性質が失われたら、統合テスト群より先にここが落ちる。 */
describe("テスト環境の前提", () => {
  it("MSW 配下では相対 URL の fetch が location.origin で解決される", async () => {
    server.use(http.get("/api/echo", () => HttpResponse.json({ ok: true })));
    const res = await fetch("/api/echo");
    expect(res.ok).toBe(true);
    expect(await res.json()).toEqual({ ok: true });
  });

  /* 統合テスト群は「handler の無い /api/… は素通しできない」を前提に常設 handler
   * を置いている。msw 3 で onUnhandledRequest は onUnhandledFrame に改名された。
   * 名前がずれて既定の warn に戻ると、取りこぼした fetch が黙って外へ出る。
   * 素通しでも localhost への接続拒否で reject するので、reject だけでは区別できない。
   * "error" strategy は console.error、"warn" は console.warn で報告する。 */
  it("handler の無いリクエストは onUnhandledFrame:error で失敗する", async () => {
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    onTestFinished(() => error.mockRestore());
    await expect(fetch("/api/no-handler")).rejects.toThrow();
    expect(error).toHaveBeenCalled();
  });
});
