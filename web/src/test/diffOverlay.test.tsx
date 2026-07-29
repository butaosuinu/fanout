import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse, type RequestHandler } from "msw";
import { beforeEach, describe, expect, it } from "vitest";
import { App } from "../components/App";
import type { DiffResponse } from "../lib/types";
import { installFakeEventSource, streamSnapshot } from "./fakeEventSource";
import {
  makeDiffFile,
  makeDiffResponse,
  makePane,
  makeQueuedPane,
  makeSession,
  makeSnapshot,
} from "./fixtures";
import { server } from "./server";

/* diff オーバーレイの統合テスト。モックはネットワーク境界(/api/diff)のみで、
 * @pierre/diffs の描画は実物を通す — patch が HTML として注入されないこと
 * (敵性入力規約)をライブラリ更新に対して固定するため。 */

beforeEach(() => {
  installFakeEventSource();
  localStorage.clear();
  document.documentElement.dataset.theme = "light";
  /* Drawer を開くと PeekPanel が /api/peek を叩く(onUnhandledRequest:"error"
   * のため素通しできない)。本ファイルの関心対象ではないので常設で応える。 */
  server.use(
    http.get("/api/peek", () =>
      HttpResponse.json({
        paneId: "%1",
        lines: 1,
        capturedAt: "2026-07-29T01:23:45Z",
        output: "peek output",
      }),
    ),
  );
});

/* 2 file 分の連結 patch — 複数 file block の分解・描画も同時に固定する */
const TWO_FILE_PATCH = [
  "diff --git a/src/hello.ts b/src/hello.ts",
  "index 0123456..89abcde 100644",
  "--- a/src/hello.ts",
  "+++ b/src/hello.ts",
  "@@ -1,3 +1,3 @@",
  " export function hello(): string {",
  '-  return "hi";',
  '+  return "hello_marker";',
  " }",
  "diff --git a/src/util.ts b/src/util.ts",
  "index 1111111..2222222 100644",
  "--- a/src/util.ts",
  "+++ b/src/util.ts",
  "@@ -1 +1,2 @@",
  " export const n = 1;",
  "+export const util_marker = 2;",
  "",
].join("\n");

function twoFileDiff(over: Partial<DiffResponse> = {}): DiffResponse {
  return makeDiffResponse({
    patch: TWO_FILE_PATCH,
    files: [makeDiffFile(), makeDiffFile({ path: "src/util.ts", additions: 1, deletions: 0 })],
    ...over,
  });
}

function issueSnapshot() {
  return makeSnapshot([
    makeSession("142", [
      makePane({ issueNum: 101, displayName: "Fix thing", branchName: "fanout/fix-thing" }),
    ]),
  ]);
}

/* 定番の前置き: handler 登録 → render → snapshot 流し込み */
function setup(...handlers: RequestHandler[]) {
  server.use(...handlers);
  render(<App />);
  streamSnapshot(issueSnapshot());
  return userEvent.setup();
}

/* @pierre/diffs は open shadow root に描画する — 検証はそこを直接読む */
function shadowText(): string {
  return [...document.querySelectorAll("diffs-container")]
    .map((el) => el.shadowRoot?.textContent ?? "")
    .join("\n");
}

async function openOverlay(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByText("Fix thing"));
  await user.click(await screen.findByRole("button", { name: "diff を開く" }));
  return await screen.findByRole("dialog", { name: "worktree diff" });
}

describe("diff オーバーレイ", () => {
  it("GitHub issue 行は parent+issue で取得し patch を shadow DOM に描画する", async () => {
    let captured: URLSearchParams | null = null;
    const user = setup(
      http.get("/api/diff", ({ request }) => {
        captured = new URL(request.url).searchParams;
        return HttpResponse.json(twoFileDiff());
      }),
    );

    const overlay = await openOverlay(user);

    expect(captured).not.toBeNull();
    expect(captured!.get("parent")).toBe("142");
    expect(captured!.get("issue")).toBe("101");
    expect(captured!.get("task")).toBeNull();
    expect(captured!.get("source")).toBeNull();

    // ヘッダ: branch → base、merge-base 短縮 SHA、file 数と合計統計
    await waitFor(() => {
      expect(within(overlay).getByText("fanout/fix-thing")).toBeInTheDocument();
    });
    expect(within(overlay).getByText("main")).toBeInTheDocument();
    expect(overlay.querySelector("#diff-meta")).toHaveTextContent(/merge-base 0123456789/);
    expect(overlay.querySelector("#diff-meta")).toHaveTextContent(/2 files \+2\/-1/);

    // patch 本文は file block ごとにライブラリの shadow DOM 側に入る
    await waitFor(() => {
      expect(shadowText()).toContain("hello_marker");
      expect(shadowText()).toContain("util_marker");
    });
    expect(shadowText()).toContain("src/hello.ts");
    expect(shadowText()).toContain("src/util.ts");
    // 完全な patch では警告帯を出さない
    expect(overlay.querySelector(".diff-banner")).toBeNull();

    // syntax highlight: Shiki が light/dark 両テーマのトークン色 CSS 変数を
    // 付けた span を生成する(highlight + テーマは本 issue の必須要件)
    await waitFor(() => {
      const tokens = [...document.querySelectorAll("diffs-container")].flatMap((el) => [
        ...el.shadowRoot!.querySelectorAll('span[style*="--diffs-token-light"]'),
      ]);
      expect(tokens.length).toBeGreaterThan(0);
      expect(
        tokens.some((t) => (t.getAttribute("style") ?? "").includes("--diffs-token-dark")),
      ).toBe(true);
    });
  });

  it("plan task 行は parent+task+source で取得する", async () => {
    let captured: URLSearchParams | null = null;
    server.use(
      http.get("/api/diff", ({ request }) => {
        captured = new URL(request.url).searchParams;
        return HttpResponse.json(makeDiffResponse({ branchName: "fanout/plan-lint" }));
      }),
    );
    render(<App />);
    streamSnapshot(
      makeSnapshot([
        makeSession("plan:alpha", [
          makePane({
            issueNum: 0,
            taskId: "plan-lint",
            sourceKey: "wt1",
            displayName: "Fix thing",
          }),
        ]),
      ]),
    );

    await openOverlay(userEvent.setup());

    expect(captured).not.toBeNull();
    expect(captured!.get("parent")).toBe("plan:alpha");
    expect(captured!.get("task")).toBe("plan-lint");
    expect(captured!.get("source")).toBe("wt1");
    expect(captured!.get("issue")).toBeNull();
  });

  it("タグを含む patch を DOM 要素として注入しない(敵性入力規約)", async () => {
    const hostile = [
      'diff --git "a/<img src=x onerror=alert(1)>.ts" "b/<img src=x onerror=alert(1)>.ts"',
      '--- "a/<img src=x onerror=alert(1)>.ts"',
      '+++ "b/<img src=x onerror=alert(1)>.ts"',
      "@@ -1 +1,2 @@",
      " ctx",
      '+<script>window.__pwned = "yes"</script><img src=x onerror=alert(2)>',
      "",
    ].join("\n");
    const user = setup(
      http.get("/api/diff", () =>
        HttpResponse.json(
          makeDiffResponse({
            files: [makeDiffFile({ path: "<img src=x onerror=alert(1)>.ts", deletions: 0 })],
            patch: hostile,
          }),
        ),
      ),
    );

    await openOverlay(user);
    await waitFor(() => {
      expect(shadowText()).toContain("__pwned");
    });

    // light DOM にも shadow root にも script/img が生成されていないこと
    expect(document.querySelector("img")).toBeNull();
    expect(document.querySelector("script:not([src])")).toBeNull();
    for (const el of document.querySelectorAll("diffs-container")) {
      expect(el.shadowRoot!.querySelector("img")).toBeNull();
      expect(el.shadowRoot!.querySelector("script")).toBeNull();
    }
    expect((window as unknown as { __pwned?: string }).__pwned).toBeUndefined();
  });

  it("truncated / patch 省略 file は警告帯と省略一覧を出す", async () => {
    const user = setup(
      http.get("/api/diff", () =>
        HttpResponse.json(
          makeDiffResponse({
            truncated: true,
            files: [
              makeDiffFile(),
              makeDiffFile({
                path: "assets/logo.png",
                additions: 0,
                deletions: 0,
                binary: true,
                patchIncluded: false,
                omittedReason: "binary",
              }),
              makeDiffFile({
                path: "huge.ts",
                additions: null,
                deletions: null,
                patchIncluded: false,
                omittedReason: "collectionLimit",
              }),
            ],
          }),
        ),
      ),
    );

    const overlay = await openOverlay(user);

    expect(await within(overlay).findByRole("status")).toHaveTextContent(/揃っていません.*2 file/);
    const omitted = within(overlay).getByRole("region", { name: "patch が省略されたファイル" });
    expect(within(omitted).getByText("assets/logo.png")).toBeInTheDocument();
    expect(within(omitted).getByText(/バイナリ/)).toBeInTheDocument();
    expect(within(omitted).getByText("huge.ts")).toBeInTheDocument();
    expect(within(omitted).getByText(/収集上限/)).toBeInTheDocument();
  });

  it("404 は worktree 記録なしのエラーメッセージを出す", async () => {
    const user = setup(
      http.get("/api/diff", () =>
        HttpResponse.json({ error: "no recorded worktree" }, { status: 404 }),
      ),
    );

    const overlay = await openOverlay(user);
    const alert = await within(overlay).findByRole("alert");
    expect(alert).toHaveTextContent(/worktree の記録が見つかりません/);
    expect(alert).toHaveTextContent("no recorded worktree");
  });

  it("502 はサーバー拒否のエラーメッセージを出す", async () => {
    const user = setup(
      http.get("/api/diff", () =>
        HttpResponse.json({ error: "git command failed" }, { status: 502 }),
      ),
    );

    const overlay = await openOverlay(user);
    const alert = await within(overlay).findByRole("alert");
    expect(alert).toHaveTextContent(/安全に生成できませんでした/);
    expect(alert).toHaveTextContent("git command failed");
  });

  it("行数予算を超える file は collapsed で mount し、クリック展開に委ねる(敵性 patch の DOM 爆発対策)", async () => {
    const bombBody = Array.from({ length: 1600 }, () => "+payload_row").join("\n");
    const bombPatch = [
      "diff --git a/bomb.txt b/bomb.txt",
      "new file mode 100644",
      "--- /dev/null",
      "+++ b/bomb.txt",
      "@@ -0,0 +1,1600 @@",
      bombBody,
      "",
    ].join("\n");
    const user = setup(
      http.get("/api/diff", () =>
        HttpResponse.json(
          twoFileDiff({
            patch: TWO_FILE_PATCH + bombPatch,
            files: [
              makeDiffFile(),
              makeDiffFile({ path: "src/util.ts", additions: 1, deletions: 0 }),
              makeDiffFile({ path: "bomb.txt", additions: 1600, deletions: 0 }),
            ],
          }),
        ),
      ),
    );

    const overlay = await openOverlay(user);
    await waitFor(() => {
      expect(shadowText()).toContain("hello_marker");
    });

    // collapsed: ヘッダ(ファイル名)は出るが 1,600 行の中身は mount されない
    expect(shadowText()).toContain("bomb.txt");
    expect(shadowText()).not.toContain("payload_row");
    const mounted = [...document.querySelectorAll("diffs-container")].reduce(
      (n, el) => n + el.shadowRoot!.querySelectorAll("*").length,
      0,
    );
    expect(mounted).toBeLessThan(3000); // 予算内の初期 mount は有界

    // 展開はユーザーの明示クリック — 展開後に中身が mount される
    await user.click(within(overlay).getByRole("button", { name: /1,600 行 — 展開/ }));
    await waitFor(() => {
      expect(shadowText()).toContain("payload_row");
    });
  });

  it("Escape はオーバーレイだけを閉じ、inert 解除後に起点へフォーカスを戻す", async () => {
    /* 実アプリ同様 #root 配下に mount し、モーダル中の inert と解除順序を検証する
     * (inert なままの focus 復帰は実ブラウザで拒否される — 順序が本質)。 */
    const root = document.createElement("div");
    root.id = "root";
    document.body.appendChild(root);
    try {
      server.use(http.get("/api/diff", () => HttpResponse.json(makeDiffResponse())));
      const user = userEvent.setup();
      render(<App />, { container: root });
      streamSnapshot(issueSnapshot());

      await openOverlay(user);
      expect(root.hasAttribute("inert")).toBe(true); // モーダル中は背面が inert

      await user.keyboard("{Escape}");

      expect(screen.queryByRole("dialog", { name: "worktree diff" })).not.toBeInTheDocument();
      expect(screen.getByRole("complementary", { name: "ペイン詳細" })).toBeInTheDocument();
      // inert が解除された後に、フォーカスは起点のボタンへ戻る
      expect(root.hasAttribute("inert")).toBe(false);
      expect(screen.getByRole("button", { name: "diff を開く" })).toHaveFocus();
    } finally {
      root.remove();
    }
  });

  it("Nav のテーマ切替がオーバーレイを開いたまま反映される", async () => {
    const user = setup(http.get("/api/diff", () => HttpResponse.json(makeDiffResponse())));

    const overlay = await openOverlay(user);
    expect(overlay).toHaveAttribute("data-theme", "light");

    await user.click(screen.getByRole("button", { name: "ライト / ダーク切替" }));
    await waitFor(() => {
      expect(overlay).toHaveAttribute("data-theme", "dark");
    });
  });

  it("未開始(synthetic)行と shell 行には diff ボタンを出さない", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(
      makeSnapshot([
        makeSession("142", [
          makeQueuedPane({ issueNum: 103, displayName: "Queued child" }),
          makePane({
            issueNum: 0,
            kind: "shell",
            shellKey: "sh1",
            displayName: "Shell pane",
            worktreePath: "/tmp/repo",
          }),
        ]),
      ]),
    );

    await user.click(screen.getByText("Queued child"));
    await screen.findByRole("complementary", { name: "ペイン詳細" });
    expect(screen.queryByRole("button", { name: "diff を開く" })).not.toBeInTheDocument();

    await user.click(screen.getByText("Shell pane"));
    await screen.findByRole("complementary", { name: "ペイン詳細" });
    expect(screen.queryByRole("button", { name: "diff を開く" })).not.toBeInTheDocument();
  });
});
