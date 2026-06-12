import { setupServer } from "msw/node";

/* ネットワーク境界のモック。handler は各テストが server.use() で登録する。
 * /api/stream(SSE)だけは jsdom に EventSource が無いため MSW では代替できず、
 * FakeEventSource(fakeEventSource.ts)を global 注入して扱う。 */
export const server = setupServer();
