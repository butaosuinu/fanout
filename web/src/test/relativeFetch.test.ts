import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
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
});
