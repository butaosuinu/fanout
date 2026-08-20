import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse, type RequestHandler } from "msw";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { App } from "../app/App";
import type { DiffResponse } from "../transport/types";
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
import { viewedScope } from "../features/diff/viewedStore";

/* diff オーバーレイの統合テスト。モックはネットワーク境界(/api/diff)のみで、
 * @pierre/diffs の描画は実物を通す — patch が HTML として注入されないこと
 * (敵性入力規約)をライブラリ更新に対して固定するため。
 *
 * 描画は <Virtualizer> 配下なので、mount されるのは各 file の「可視範囲」だけ。
 * jsdom は全要素の高さが 0 なので全 file が可視扱いになるが、file 内の描画行数は
 * 実ブラウザと同じくウィンドウ由来で有界になる — 行数の有界性はここで固定できる。 */

/* jsdom の既定は 1024。styles/responsive.css の @media と閾値をまたぐケースでだけ差し替え、
 * afterEach で必ず戻す。 */
const DEFAULT_INNER_WIDTH = window.innerWidth;
const setInnerWidth = (w: number) =>
  Object.defineProperty(window, "innerWidth", { value: w, configurable: true, writable: true });

afterEach(() => setInnerWidth(DEFAULT_INNER_WIDTH));

beforeEach(() => {
  installFakeEventSource();
  localStorage.clear();
  document.documentElement.dataset.theme = "light";
  /* 既定はコンパクトだが、本ファイルの大半は表示モードと無関係なので全画面に
   * 固定して読みやすくする。モード自体は下の 3 本で押さえる。 */
  localStorage.setItem("fanout.diffView", "full");
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

/* 行数 n の追加だけからなる 1 file 分の patch */
function linesPatch(path: string, lines: number, marker: string): string {
  return [
    `diff --git a/${path} b/${path}`,
    "new file mode 100644",
    "--- /dev/null",
    `+++ b/${path}`,
    `@@ -0,0 +1,${lines} @@`,
    Array.from({ length: lines }, () => `+${marker}`).join("\n"),
    "",
  ].join("\n");
}

/* 変更種別アイコンの修飾クラス(k-added など)。基底クラスや将来の付随クラスに
 * 依存させないため、k- 接頭辞の 1 個だけを取り出す。 */
function kindModifier(el: Element): string {
  return [...el.classList].find((c) => c.startsWith("k-")) ?? "";
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

/* 初期 mount コストの上限を固定するための実測 node 数(要素 + text node)。
 * 敵性 patch は highlight 段で 1 文字 1 span まで膨らむため、行数ではなく
 * 実際の node 数で有界性を確認する。 */
function countMountedNodes(): number {
  let n = 0;
  for (const host of document.querySelectorAll("diffs-container")) {
    const root = host.shadowRoot!;
    n += root.querySelectorAll("*").length;
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    while (walker.nextNode()) n++;
  }
  return n;
}

/* inline diff(行内 word 差分)の decoration 数。plaintext 描画時に 0 で
 * あることを固定するために数える。 */
function countDiffDecorations(): number {
  let n = 0;
  for (const host of document.querySelectorAll("diffs-container")) {
    n += host.shadowRoot!.querySelectorAll("[data-diff-span]").length;
  }
  return n;
}

/* patch 由来の 1 行が shadow DOM に何行描画されたか(仮想化の有界性の観測点) */
function countOccurrences(needle: string): number {
  const text = shadowText();
  let n = 0;
  let at = text.indexOf(needle);
  while (at !== -1) {
    n++;
    at = text.indexOf(needle, at + needle.length);
  }
  return n;
}

async function openOverlay(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByText("Fix thing"));
  await user.click(await screen.findByRole("button", { name: "変更を表示" }));
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

    /* dialog は fetch の完了を待たずに出るので、取得そのものを待ってから読む
       (即読みだと lazy chunk の解決コスト次第で取りこぼす) */
    await waitFor(() => expect(captured).not.toBeNull());
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

  it("ファイル名ヘッダを sticky にし、片側だけの file を寄せる CSS を渡す", async () => {
    const user = setup(
      http.get("/api/diff", () =>
        HttpResponse.json(
          twoFileDiff({
            patch: TWO_FILE_PATCH + linesPatch("src/added.ts", 3, "added_row"),
            files: [
              makeDiffFile(),
              makeDiffFile({ path: "src/util.ts", additions: 1, deletions: 0 }),
              makeDiffFile({ path: "src/added.ts", additions: 3, deletions: 0 }),
            ],
          }),
        ),
      ),
    );

    await openOverlay(user);
    await waitFor(() => {
      expect(shadowText()).toContain("added_row");
    });

    const roots = [...document.querySelectorAll("diffs-container")].map((el) => el.shadowRoot!);
    // GitHub の files changed と同じく、スクロール中もファイル名が上端に残る
    expect(roots.every((r) => r.querySelector("[data-diffs-header][data-sticky]"))).toBe(true);
    // 長い行は横スクロールさせず折り返す
    expect(roots.every((r) => r.querySelector('pre[data-overflow="wrap"]'))).toBe(true);
    // 追加のみの file は data-diff-type="single" になり、右寄せ CSS の対象になる
    expect(
      roots.some((r) => r.querySelector('pre[data-diff-type="single"] > code[data-additions]')),
    ).toBe(true);
    const injected = roots
      .flatMap((r) => [...r.querySelectorAll("style")])
      .map((s) => s.textContent ?? "");
    expect(
      injected.some((css) => css.includes("code[data-additions]") && css.includes("margin-left")),
    ).toBe(true);
    /* 横スクロールの封じ。スクロール箱を消すだけでは、はみ出したトークンが
       見えないまま切り落とされるので、列幅と折り返しまでが 1 組。jsdom に
       レイアウトは無いので押さえられるのは「渡していること」だけで、実際の
       効きは実機で見る。縦積み側にも載ることはレイアウト巡回のテストが見る。 */
    expect(injected.some((css) => css.includes("[data-code]{ overflow: clip;"))).toBe(true);
    expect(
      injected.some((css) => css.includes("--diffs-code-grid") && css.includes("minmax(0, 1fr)")),
    ).toBe(true);
    expect(injected.some((css) => css.includes("overflow-wrap: anywhere"))).toBe(true);
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

  it("truncated / patch 省略 file は警告帯を出し、常時見える一覧に理由付きで並べる", async () => {
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

    /* 省略 file はサイドバーではなく警告帯の直下に置く。サイドバーは本文が狭いと
     * 畳まれるので、そこにしか無いと「どの file がなぜレビューできないか」が
     * 狭い画面で丸ごと消える。 */
    const omitted = overlay.querySelector(".diff-omitted") as HTMLElement;
    expect(within(omitted).getByText("assets/logo.png")).toBeInTheDocument();
    expect(within(omitted).getByText(/バイナリ/)).toBeInTheDocument();
    expect(within(omitted).getByText("huge.ts")).toBeInTheDocument();
    expect(within(omitted).getByText(/収集上限/)).toBeInTheDocument();

    // サイドバーは「飛べる file」だけを並べる
    const sidebar = within(overlay).getByRole("region", { name: "変更ファイル" });
    expect(within(sidebar).getByText("hello.ts")).toBeInTheDocument();
    expect(within(sidebar).queryByText("logo.png")).toBeNull();
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

  it("典型的なレビュー diff は 20 file すべて展開された状態で出る", async () => {
    const files = Array.from({ length: 20 }, (_, i) => ({
      path: `src/f${i}.ts`,
      marker: `marker_${i}`,
    }));
    const user = setup(
      http.get("/api/diff", () =>
        HttpResponse.json(
          makeDiffResponse({
            patch: files.map((f) => linesPatch(f.path, 200, f.marker)).join(""),
            files: files.map((f) => makeDiffFile({ path: f.path, additions: 200, deletions: 0 })),
          }),
        ),
      ),
    );

    const overlay = await openOverlay(user);
    await waitFor(() => {
      expect(shadowText()).toContain("marker_0");
    });

    // 予算で打ち切られず、最後の file まで中身が描画される
    for (const f of files) expect(shadowText()).toContain(f.marker);
    expect(within(overlay).queryByRole("button", { name: /行 — 展開$/ })).toBeNull();
  });

  it("仮想化により、1 file の描画行数はウィンドウ内に収まる", async () => {
    /* 敵性 patch は契約内でも 26 万行を作れる。行数の有界性は予算ではなく
     * 仮想化で担保する — 1,600 行の file を展開しても、描画される行は
     * ビューポート由来のごく一部だけになる。 */
    const user = setup(
      http.get("/api/diff", () =>
        HttpResponse.json(
          makeDiffResponse({
            patch: linesPatch("bomb.txt", 1600, "payload_row"),
            files: [makeDiffFile({ path: "bomb.txt", additions: 1600, deletions: 0 })],
          }),
        ),
      ),
    );

    const overlay = await openOverlay(user);
    // 1,000 行超なので初期は折りたたみ
    const expand = await within(overlay).findByRole("button", { name: /1,600 行 — 展開$/ });
    expect(shadowText()).not.toContain("payload_row");

    await user.click(expand);
    await waitFor(() => {
      expect(shadowText()).toContain("payload_row");
    });
    // 1,600 行すべては描かない。上限は overscrollSize(3,000px)ぶんの先読みを
    // 見込んだ値で、実測は約 300 行。
    expect(countOccurrences("payload_row")).toBeLessThan(600);
    expect(countMountedNodes()).toBeLessThan(10_000);
  });

  it("折りたたまれていた file も、展開すればシンタックスハイライトが付く", async () => {
    const body = Array.from({ length: 1200 }, (_, i) => `+const big_marker${i} = ${i};`).join("\n");
    const patch = [
      "diff --git a/src/big.ts b/src/big.ts",
      "new file mode 100644",
      "--- /dev/null",
      "+++ b/src/big.ts",
      "@@ -0,0 +1,1200 @@",
      body,
      "",
    ].join("\n");
    const user = setup(
      http.get("/api/diff", () =>
        HttpResponse.json(
          makeDiffResponse({
            patch,
            files: [makeDiffFile({ path: "src/big.ts", additions: 1200, deletions: 0 })],
          }),
        ),
      ),
    );

    const overlay = await openOverlay(user);
    await user.click(await within(overlay).findByRole("button", { name: /1,200 行 — 展開$/ }));
    await waitFor(() => {
      expect(shadowText()).toContain("big_marker0");
    });

    // 展開後も Shiki のトークン span が出る(旧実装はここで highlight を切っていた)
    const tokens = [...document.querySelectorAll("diffs-container")].flatMap((el) => [
      ...el.shadowRoot!.querySelectorAll('span[style*="--diffs-token-light"]'),
    ]);
    expect(tokens.length).toBeGreaterThan(0);
  });

  it("複数の file を同時に展開したままにできる", async () => {
    const user = setup(
      http.get("/api/diff", () =>
        HttpResponse.json(
          makeDiffResponse({
            patch:
              linesPatch("bomb1.txt", 1600, "first_payload") +
              linesPatch("bomb2.txt", 1600, "second_payload"),
            files: [1, 2].map((n) =>
              makeDiffFile({ path: `bomb${n}.txt`, additions: 1600, deletions: 0 }),
            ),
          }),
        ),
      ),
    );

    const overlay = await openOverlay(user);
    const expandButtons = () => within(overlay).getAllByRole("button", { name: /行 — 展開$/ });
    await waitFor(() => {
      expect(expandButtons()).toHaveLength(2);
    });

    await user.click(expandButtons()[0]!);
    await waitFor(() => {
      expect(shadowText()).toContain("first_payload");
    });

    // 2 file 目を展開しても 1 file 目は開いたまま
    await user.click(within(overlay).getByRole("button", { name: /行 — 展開$/ }));
    await waitFor(() => {
      expect(shadowText()).toContain("second_payload");
    });
    expect(shadowText()).toContain("first_payload");

    // 明示的に折りたたむとその file だけ畳まれる
    await user.click(within(overlay).getAllByRole("button", { name: / — 折りたたむ$/ })[0]!);
    await waitFor(() => {
      expect(shadowText()).not.toContain("first_payload");
    });
    expect(shadowText()).toContain("second_payload");
  });

  it("すべて展開 / すべて折りたたむ が全 file に効く", async () => {
    const user = setup(
      http.get("/api/diff", () =>
        HttpResponse.json(
          twoFileDiff({
            patch: TWO_FILE_PATCH + linesPatch("bomb.txt", 1600, "payload_row"),
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

    await user.click(within(overlay).getByRole("button", { name: "すべて折りたたむ" }));
    await waitFor(() => {
      expect(shadowText()).not.toContain("hello_marker");
    });

    await user.click(within(overlay).getByRole("button", { name: "すべて展開" }));
    await waitFor(() => {
      expect(shadowText()).toContain("payload_row");
    });
    expect(shadowText()).toContain("hello_marker");
  });

  it("サイドバーは同じディレクトリの file をまとめ、ファイル名だけを並べる", async () => {
    const user = setup(
      http.get("/api/diff", () =>
        HttpResponse.json(
          twoFileDiff({
            patch: TWO_FILE_PATCH + linesPatch("README.md", 3, "readme_row"),
            files: [
              makeDiffFile(),
              makeDiffFile({ path: "src/util.ts", additions: 1, deletions: 0 }),
              makeDiffFile({ path: "README.md", additions: 3, deletions: 0 }),
            ],
          }),
        ),
      ),
    );

    const overlay = await openOverlay(user);
    const sidebar = await within(overlay).findByRole("region", { name: "変更ファイル" });
    expect(within(sidebar).getByText("3 files", { exact: false })).toBeInTheDocument();
    // ディレクトリ見出しは 1 つにまとまる(src/ が 2 回出ない)
    expect(
      within(sidebar)
        .getAllByRole("heading")
        .map((h) => h.textContent),
    ).toEqual(["src/", "(リポジトリ直下)"]);
    const rows = within(sidebar).getAllByRole("listitem");
    expect(rows.map((r) => r.textContent)).toEqual([
      "hello.ts+1-1",
      "util.ts+1-0",
      "README.md+3-0",
    ]);
  });

  /* group 見出しは移動先のディレクトリなので、行に移動元が出ないと
   * ディレクトリ再編のレビューで「どこから来たか」が丸ごと失われる。 */
  it("移動した file は行に移動元を出し、accessible name に両端を入れる", async () => {
    const patch = [
      "diff --git a/web/src/components/App.tsx b/web/src/app/App.tsx",
      "similarity index 87%",
      "rename from web/src/components/App.tsx",
      "rename to web/src/app/App.tsx",
      "--- a/web/src/components/App.tsx",
      "+++ b/web/src/app/App.tsx",
      "@@ -1 +1 @@",
      "-old",
      "+new",
      "",
    ].join("\n");
    const user = setup(
      http.get("/api/diff", () =>
        HttpResponse.json(
          makeDiffResponse({
            patch,
            files: [
              makeDiffFile({
                path: "web/src/app/App.tsx",
                oldPath: "web/src/components/App.tsx",
              }),
            ],
          }),
        ),
      ),
    );

    const overlay = await openOverlay(user);
    const sidebar = await within(overlay).findByRole("region", { name: "変更ファイル" });
    // 同名クロスディレクトリ移動でも移動元が分かる(旧ディレクトリを出す)。
    expect(within(sidebar).getByRole("listitem").textContent).toBe(
      "App.tsx← web/src/components/+1-1",
    );
    expect(
      within(sidebar).getByRole("button", {
        name: "web/src/components/App.tsx → web/src/app/App.tsx — 移動",
      }),
    ).toBeInTheDocument();
  });

  /* 種別は統計からは分からない — 空ファイルの追加は +0 -0、全行書き換えは +N -N に
   * なる。アイコンは aria-hidden なので、名乗るのは行の accessible name 側。 */
  it("サイドバーの行に変更種別のアイコンと名前を出す", async () => {
    const patch = [
      "diff --git a/src/added.ts b/src/added.ts",
      "new file mode 100644",
      "--- /dev/null",
      "+++ b/src/added.ts",
      "@@ -0,0 +1 @@",
      "+hello",
      "diff --git a/src/edited.ts b/src/edited.ts",
      "index 1111111..2222222 100644",
      "--- a/src/edited.ts",
      "+++ b/src/edited.ts",
      "@@ -1 +1 @@",
      "-old",
      "+new",
      "diff --git a/src/gone.ts b/src/gone.ts",
      "deleted file mode 100644",
      "--- a/src/gone.ts",
      "+++ /dev/null",
      "@@ -1 +0,0 @@",
      "-bye",
      "diff --git a/lib/moved.ts b/src/moved.ts",
      "similarity index 100%",
      "rename from lib/moved.ts",
      "rename to src/moved.ts",
      "",
    ].join("\n");
    const user = setup(
      http.get("/api/diff", () =>
        HttpResponse.json(
          makeDiffResponse({
            patch,
            files: [
              makeDiffFile({ path: "src/added.ts", additions: 1, deletions: 0 }),
              makeDiffFile({ path: "src/edited.ts" }),
              makeDiffFile({ path: "src/gone.ts", additions: 0, deletions: 1 }),
              makeDiffFile({
                path: "src/moved.ts",
                oldPath: "lib/moved.ts",
                additions: 0,
                deletions: 0,
              }),
            ],
          }),
        ),
      ),
    );

    const overlay = await openOverlay(user);
    const sidebar = await within(overlay).findByRole("region", { name: "変更ファイル" });
    // 種別は後置する — 支援技術の type-ahead が先頭から照合するので、パスが先頭
    for (const name of [
      "src/added.ts — 新規追加",
      "src/edited.ts — 変更",
      "src/gone.ts — 削除",
      "lib/moved.ts → src/moved.ts — 移動",
    ]) {
      expect(within(sidebar).getByRole("button", { name })).toBeInTheDocument();
    }
    /* 色は修飾クラスが決めるので、種別ごとに違う修飾が行順に付くことまで見る。
     * className の完全一致にはしない — 無関係なクラスが 1 個増えただけで
     * 見た目も意味も変わらないのにテストが落ちる。 */
    expect([...sidebar.querySelectorAll(".diff-file-kind")].map(kindModifier)).toEqual([
      "k-added",
      "k-modified",
      "k-deleted",
      "k-renamed",
    ]);
  });

  /* 行の左インデントはアイコン列が担っているので、種別が引けない行で列ごと畳むと
   * その行だけ左へ飛び出し、ディレクトリ見出しの下に属して見えなくなる。 */
  it("種別が引けない行でもアイコン列の幅は残し、名前に種別を足さない", async () => {
    const user = setup(
      http.get("/api/diff", () =>
        HttpResponse.json(
          makeDiffResponse({
            patch: linesPatch("src/known.ts", 2, "known_row"),
            files: [
              makeDiffFile({ path: "src/known.ts", additions: 2, deletions: 0 }),
              // patchIncluded だが patch にブロックが無い(= is-static 行)
              makeDiffFile({ path: "src/orphan.ts", additions: 1, deletions: 0 }),
            ],
          }),
        ),
      ),
    );

    const overlay = await openOverlay(user);
    const sidebar = await within(overlay).findByRole("region", { name: "変更ファイル" });
    expect([...sidebar.querySelectorAll(".diff-file-kind")].map(kindModifier)).toEqual([
      "k-added",
      "",
    ]);
    expect(within(sidebar).getByTitle("src/orphan.ts")).toBeInTheDocument();
  });

  it("サイドバーのファイル名クリックで、折りたたまれた file を開く", async () => {
    const user = setup(
      http.get("/api/diff", () =>
        HttpResponse.json(
          makeDiffResponse({
            patch: linesPatch("src/bomb.txt", 1600, "payload_row"),
            files: [makeDiffFile({ path: "src/bomb.txt", additions: 1600, deletions: 0 })],
          }),
        ),
      ),
    );

    const overlay = await openOverlay(user);
    const sidebar = await within(overlay).findByRole("region", { name: "変更ファイル" });
    expect(shadowText()).not.toContain("payload_row");
    const before = [...document.querySelectorAll("diffs-container")];

    await user.click(within(sidebar).getByRole("button", { name: /bomb\.txt/ }));
    await waitFor(() => {
      expect(shadowText()).toContain("payload_row");
    });
    // 飛び先も要素を作り直さずに描き直す(モード切替と同じ理由 — 下のテストを参照)
    expect([...document.querySelectorAll("diffs-container")]).toEqual(before);
  });

  it("サイドバーは patch を持たない file を並べない(飛び先が無いため)", async () => {
    const user = setup(
      http.get("/api/diff", () =>
        HttpResponse.json(
          twoFileDiff({
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
            ],
          }),
        ),
      ),
    );

    const overlay = await openOverlay(user);
    const sidebar = await within(overlay).findByRole("region", { name: "変更ファイル" });
    expect(within(sidebar).getByRole("button", { name: /hello\.ts/ })).toBeInTheDocument();
    expect(within(sidebar).queryByText(/logo\.png/)).toBeNull();
    // 代わりに常時見える省略一覧のほうに出る
    expect(overlay.querySelector(".diff-omitted")).toHaveTextContent("assets/logo.png");
  });

  it("行数が少なくても内容量が多い file は highlight と行内差分を切る", async () => {
    /* codex adversarial review の再現 shape: 各 side 65,340 文字・66 行の交互
     * トークン(`a+1+`…)を 2 file。wire contract は満たすが、highlight ありで
     * 描画すると実測 262,112 node / 11.9 秒かかる。行数では測れないので、
     * 描画対象の総文字数で highlight と inline diff を落とす。 */
    const denseLine = `+${"a+1+".repeat(248).slice(0, 990)}`;
    const denseBody = Array.from({ length: 66 }, () => denseLine).join("\n");
    const densePatch = (n: number) =>
      [
        `diff --git a/dense${n}.ts b/dense${n}.ts`,
        "new file mode 100644",
        "--- /dev/null",
        `+++ b/dense${n}.ts`,
        "@@ -0,0 +1,66 @@",
        denseBody,
        "",
      ].join("\n");
    const user = setup(
      http.get("/api/diff", () =>
        HttpResponse.json(
          makeDiffResponse({
            patch: densePatch(1) + densePatch(2),
            files: [1, 2].map((n) =>
              makeDiffFile({ path: `dense${n}.ts`, additions: 66, deletions: 0 }),
            ),
          }),
        ),
      ),
    );

    const overlay = await openOverlay(user);
    await waitFor(() => {
      expect(shadowText()).toContain("a+1+");
    });

    // 66 行なので折りたたまれず、そのまま plaintext で描かれる
    expect(within(overlay).queryByRole("button", { name: /行 — 展開$/ })).toBeNull();
    expect(countDiffDecorations()).toBe(0);
    expect(
      [...document.querySelectorAll("diffs-container")].flatMap((el) => [
        ...el.shadowRoot!.querySelectorAll('span[style*="--diffs-token-light"]'),
      ]),
    ).toHaveLength(0);
    expect(countMountedNodes()).toBeLessThan(20_000);
  });

  it("契約上限の 500 files でも初期 mount の実 node 数を有界に保つ", async () => {
    const tiny = (n: number) =>
      [
        `diff --git a/t${n}.ts b/t${n}.ts`,
        "new file mode 100644",
        "--- /dev/null",
        `+++ b/t${n}.ts`,
        "@@ -0,0 +1,2 @@",
        "+const a = 1;",
        "+const b = 2;",
        "",
      ].join("\n");
    const user = setup(
      http.get("/api/diff", () =>
        HttpResponse.json(
          makeDiffResponse({
            patch: Array.from({ length: 500 }, (_, i) => tiny(i)).join(""),
            files: Array.from({ length: 500 }, (_, i) =>
              makeDiffFile({ path: `t${i}.ts`, additions: 2, deletions: 0 }),
            ),
          }),
        ),
      ),
    );

    const overlay = await openOverlay(user);
    await waitFor(() => {
      expect(shadowText()).toContain("t0.ts");
    });
    // 500 件すべてがサイドバーと本文に並ぶ(取りこぼしなし)
    const sidebar = within(overlay).getByRole("region", { name: "変更ファイル" });
    expect(within(sidebar).getAllByRole("listitem")).toHaveLength(500);
    expect(countMountedNodes()).toBeLessThan(120_000);
  });

  it("再取得で入れ替わった file に、前の patch の展開状態を持ち込まない", async () => {
    /* @pierre/diffs は ref/layout 経路で同期 mount するので、展開状態のリセットが
     * passive effect だと「リセット前の 1 回」で新しい file を全展開できてしまう。 */
    let hit = 0;
    const user = setup(
      http.get("/api/diff", () => {
        hit++;
        return HttpResponse.json(
          makeDiffResponse({
            patch: linesPatch("one.ts", 1200, hit === 1 ? "first_marker" : "second_marker"),
            files: [makeDiffFile({ path: "one.ts", additions: 1200, deletions: 0 })],
          }),
        );
      }),
    );

    const overlay = await openOverlay(user);
    await user.click(await within(overlay).findByRole("button", { name: /1,200 行 — 展開$/ }));
    await waitFor(() => {
      expect(shadowText()).toContain("first_marker");
    });

    await user.click(within(overlay).getByRole("button", { name: "再取得" }));
    await waitFor(() => {
      expect(within(overlay).getByRole("button", { name: /1,200 行 — 展開$/ })).toBeInTheDocument();
    });
    expect(shadowText()).not.toContain("second_marker");
    expect(shadowText()).not.toContain("first_marker");
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
      expect(screen.getByRole("button", { name: "変更を表示" })).toHaveFocus();
    } finally {
      root.remove();
    }
  });

  it("オーバーレイからテーマ設定を開き、Escape では設定だけを閉じる", async () => {
    const user = setup(http.get("/api/diff", () => HttpResponse.json(makeDiffResponse())));

    const overlay = await openOverlay(user);
    expect(overlay).toHaveAttribute("data-theme", "light");

    await user.click(within(overlay).getByRole("button", { name: "テーマ設定" }));
    const settings = await screen.findByRole("dialog", { name: "設定" });
    // オーバーレイは開いたまま背面として inert になる
    expect(overlay.hasAttribute("inert")).toBe(true);

    await user.click(within(settings).getByRole("radio", { name: "ダーク" }));
    await waitFor(() => {
      expect(overlay).toHaveAttribute("data-theme", "dark");
    });

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog", { name: "設定" })).not.toBeInTheDocument();
    expect(screen.getByRole("dialog", { name: "worktree diff" })).toBeInTheDocument();
    expect(overlay.hasAttribute("inert")).toBe(false);
  });

  it("コンパクト表示ではモーダルを降りて背面を触れるようにし、選択は保存される", async () => {
    setInnerWidth(1600); // 1100px 以下だとコンパクトも全幅 = covering になる
    /* 実アプリ同様 #root 配下に mount する — 全画面だけが背面を inert にする、
     * という切り分けが本題。 */
    const root = document.createElement("div");
    root.id = "root";
    document.body.appendChild(root);
    try {
      server.use(http.get("/api/diff", () => HttpResponse.json(twoFileDiff())));
      const user = userEvent.setup();
      render(<App />, { container: root });
      streamSnapshot(issueSnapshot());

      const overlay = await openOverlay(user);
      expect(overlay).toHaveAttribute("data-mode", "full");
      expect(root.hasAttribute("inert")).toBe(true);
      const before = [...document.querySelectorAll("diffs-container")];

      await user.click(within(overlay).getByRole("button", { name: "コンパクト表示" }));

      /* 表示モードを変えても file の要素は作り直さない。React が要素を持つので
       * ライブラリの cleanUp は shadow root の placeholder / buffer を残したまま
       * 参照だけ捨て、新しい instance がその上に重ねて描く(実ブラウザで全 file の
       * 高さが 2 倍になり、広範囲が空白になるのを実測済み)。 */
      expect([...document.querySelectorAll("diffs-container")]).toEqual(before);

      // dialog ではなくなるので、role を変えて引き直す
      const compact = await screen.findByRole("complementary", { name: "worktree diff" });
      expect(compact).toHaveAttribute("data-mode", "compact");
      expect(compact).not.toHaveAttribute("aria-modal");
      expect(root.hasAttribute("inert")).toBe(false); // 背面(リスト・ドロワー)を触れる
      expect(localStorage.getItem("fanout.diffView")).toBeNull(); // 既定値はキーを残さない
      // 本文は作り直されても中身は同じ patch のまま
      await waitFor(() => {
        expect(shadowText()).toContain("hello_marker");
      });

      await user.click(within(compact).getByRole("button", { name: "全画面表示" }));
      const full = await screen.findByRole("dialog", { name: "worktree diff" });
      expect(full).toHaveAttribute("data-mode", "full");
      expect(root.hasAttribute("inert")).toBe(true);
      expect(localStorage.getItem("fanout.diffView")).toBe("full");
    } finally {
      root.remove();
    }
  });

  it("導線から開いた初回はコンパクト表示になり、グリップで幅を変えられる", async () => {
    localStorage.removeItem("fanout.diffView"); // 未設定 = 既定
    /* jsdom の既定 innerWidth(1024)は全幅パネルへ落ちる帯なのでグリップが出ない。
     * 幅を変えられる帯へ広げる(片付けは afterEach)。 */
    setInnerWidth(1600);
    /* Session リストの diff セルから開く(Drawer を開かないので、パネルの右端は
     * ビューポート右端 = --diff-anchor-right が 0 のまま)。 */
    server.use(http.get("/api/diff", () => HttpResponse.json(twoFileDiff())));
    render(<App />);
    streamSnapshot(
      makeSnapshot([
        makeSession("142", [
          makePane({ issueNum: 101, displayName: "Fix thing", diffSummary: "+2/-1" }),
        ]),
      ]),
    );
    const user = userEvent.setup();

    await user.click(screen.getByText("Fix thing"));
    await user.click(await screen.findByRole("button", { name: "変更を表示" }));
    const overlay = await screen.findByRole("complementary", { name: "worktree diff" });
    expect(overlay).toHaveAttribute("data-mode", "compact");
    expect(overlay.style.getPropertyValue("--diff-w")).toBe("760px");

    /* 右アンカーなので左ドラッグで拡大。実際に動いた pointerup だけを保存する。 */
    const grip = within(overlay).getByRole("separator", { name: "diff パネルの幅を変更" });
    fireEvent.pointerDown(grip, { pointerId: 1, button: 0, isPrimary: true, clientX: 600 });
    fireEvent.pointerMove(grip, { pointerId: 1, buttons: 1, clientX: 500 });
    expect(overlay.style.getPropertyValue("--diff-w")).toBe("860px");
    fireEvent.pointerUp(grip, { pointerId: 1, clientX: 500 });
    expect(localStorage.getItem("fanout.diffWidth")).toBe("860");
  });

  it("幅の上限はビューポートの 95%(ドロワーの左端では止めない)", async () => {
    setInnerWidth(1600); // 1100px 以下だとコンパクトも全幅 = covering になる
    localStorage.removeItem("fanout.diffView");
    localStorage.setItem("fanout.diffWidth", "99999"); // 上限を超える保存値
    const user = setup(http.get("/api/diff", () => HttpResponse.json(twoFileDiff())));

    await user.click(screen.getByText("Fix thing"));
    await user.click(await screen.findByRole("button", { name: "変更を表示" }));
    const overlay = await screen.findByRole("complementary", { name: "worktree diff" });
    /* ドロワーを開いた状態でも上限はビューポートだけで決まる。越えた分は
     * ドロワーに被さる(右端の算出は styles/diff.css 側)。 */
    const cap = Math.floor(window.innerWidth * 0.95);
    expect(overlay.style.getPropertyValue("--diff-w")).toBe(`${cap}px`);
  });

  it("レイアウトボタンは 1 個で 自動 → 左右 2 面 → 縦積み を巡回する", async () => {
    const user = setup(http.get("/api/diff", () => HttpResponse.json(twoFileDiff())));
    const overlay = await openOverlay(user);
    const layoutBtn = () => within(overlay).getByRole("button", { name: /^レイアウト:/ });
    const shadow = () => document.querySelector("diffs-container")!.shadowRoot!;

    /* 既定は自動。jsdom の innerWidth 1024 は閾値 1,000 を超えるので左右 2 面。 */
    expect(layoutBtn()).toHaveAccessibleName("レイアウト: 自動(クリックで左右 2 面)");
    expect(localStorage.getItem("fanout.diffLayout")).toBeNull(); // 既定値はキーを残さない

    await user.click(layoutBtn());
    expect(layoutBtn()).toHaveAccessibleName("レイアウト: 左右 2 面(クリックで縦積み)");
    expect(localStorage.getItem("fanout.diffLayout")).toBe("split");
    expect(overlay).toHaveAttribute("data-layout", "split");
    await waitFor(() => {
      expect(shadow().querySelector("pre > code[data-additions]")).toBeTruthy();
    });

    await user.click(layoutBtn());
    expect(layoutBtn()).toHaveAccessibleName("レイアウト: 縦積み(クリックで自動)");
    expect(localStorage.getItem("fanout.diffLayout")).toBe("stack");
    expect(overlay).toHaveAttribute("data-layout", "stack");
    /* 縦積みは列が 1 本になるので code[data-additions] / [data-deletions] は消え、
     * [data-unified] になる。 */
    await waitFor(() => {
      expect(shadow().querySelector("pre > code[data-unified]")).toBeTruthy();
      expect(shadow().querySelector("pre > code[data-additions]")).toBeNull();
    });
    /* 縦積みも data-diff-type="single" になるため、片側寄せ CSS を残すと本文が
     * 半分幅に潰れる。split のときだけ注入すること。 */
    const stackCss = [...document.querySelectorAll("diffs-container")]
      .flatMap((el) => [...el.shadowRoot!.querySelectorAll("style")])
      .map((s) => s.textContent ?? "");
    expect(stackCss.some((css) => css.includes("code[data-additions]"))).toBe(false);
    /* 横スクロールの封じは縦積みにも要る — スクロール箱が残るのはむしろこちら側
     * (split + wrap ではライブラリが箱を display:contents で消している)。 */
    expect(stackCss.some((css) => css.includes("[data-code]{ overflow: clip;"))).toBe(true);
    expect(
      stackCss.some((css) => css.includes("--diffs-code-grid") && css.includes("minmax(0, 1fr)")),
    ).toBe(true);
    expect(stackCss.some((css) => css.includes("overflow-wrap: anywhere"))).toBe(true);

    await user.click(layoutBtn());
    expect(layoutBtn()).toHaveAccessibleName("レイアウト: 自動(クリックで左右 2 面)");
    expect(localStorage.getItem("fanout.diffLayout")).toBeNull();
  });

  it("auto は本文の幅で split / stack を選ぶ", async () => {
    /* jsdom の既定 innerWidth は 1024 で閾値 1,000 を超えるため、既定の auto は
     * 左右 2 面(上のテスト)。ここは狭い側を見る。 */
    setInnerWidth(900);
    const user = setup(http.get("/api/diff", () => HttpResponse.json(twoFileDiff())));
    const overlay = await openOverlay(user);
    expect(overlay).toHaveAttribute("data-layout", "stack");
    await waitFor(() => {
      const shadow = document.querySelector("diffs-container")!.shadowRoot!;
      expect(shadow.querySelector("pre > code[data-unified]")).toBeTruthy();
    });
    // 明示指定は幅に関係なく優先する
    await user.click(within(overlay).getByRole("button", { name: /^レイアウト: 自動/ }));
    expect(overlay).toHaveAttribute("data-layout", "split");
  });

  it("全画面表示にはリサイズグリップを出さない", async () => {
    const user = setup(http.get("/api/diff", () => HttpResponse.json(twoFileDiff())));
    const overlay = await openOverlay(user);
    expect(overlay).toHaveAttribute("data-mode", "full");
    expect(within(overlay).queryByRole("separator")).toBeNull();
  });

  it("コンパクトで開くと peek のポーリングは止まらない(全画面のときだけ止める)", async () => {
    setInnerWidth(1600); // 1100px 以下だとコンパクトも全幅 = covering になる
    let peeks = 0;
    localStorage.removeItem("fanout.diffView");
    const user = setup(
      http.get("/api/diff", () => HttpResponse.json(twoFileDiff())),
      http.get("/api/peek", () => {
        peeks++;
        return HttpResponse.json({
          paneId: "%1",
          lines: 1,
          capturedAt: "2026-07-29T01:23:45Z",
          output: "peek output",
        });
      }),
    );

    await user.click(screen.getByText("Fix thing"));
    await screen.findByRole("complementary", { name: "ペイン詳細" });
    await waitFor(() => {
      expect(peeks).toBeGreaterThan(0);
    });

    await user.click(await screen.findByRole("button", { name: "変更を表示" }));
    await screen.findByRole("complementary", { name: "worktree diff" });
    // ドロワーは背面ではなく隣に並ぶので peek パネルは生きたまま
    expect(screen.getByText("peek output")).toBeInTheDocument();
  });

  it("全画面 diff の上で設定を開閉しても、背面の inert は解けない", async () => {
    /* 設定モーダルは自分が付けた inert だけを外す。無条件に外すと、diff が
     * 全面を覆ったままリストやドロワーへ Tab できてしまう(DiffOverlay の
     * inert effect は再実行されないので復旧しない)。 */
    const root = document.createElement("div");
    root.id = "root";
    document.body.appendChild(root);
    try {
      server.use(http.get("/api/diff", () => HttpResponse.json(makeDiffResponse())));
      const user = userEvent.setup();
      render(<App />, { container: root });
      streamSnapshot(issueSnapshot());

      const overlay = await openOverlay(user);
      expect(root.hasAttribute("inert")).toBe(true);

      await user.click(within(overlay).getByRole("button", { name: "テーマ設定" }));
      await screen.findByRole("dialog", { name: "設定" });
      expect(root.hasAttribute("inert")).toBe(true);
      expect(overlay.hasAttribute("inert")).toBe(true); // こちらは設定が付けた

      await user.keyboard("{Escape}");
      expect(screen.queryByRole("dialog", { name: "設定" })).not.toBeInTheDocument();
      expect(overlay.hasAttribute("inert")).toBe(false); // 設定が外す
      expect(root.hasAttribute("inert")).toBe(true); // diff の inert は残る
    } finally {
      root.remove();
    }
  });

  it("全幅パネルへ落ちる狭い帯ではリサイズグリップを出さない", async () => {
    setInnerWidth(900);
    localStorage.removeItem("fanout.diffView"); // 既定 = コンパクト
    const user = setup(http.get("/api/diff", () => HttpResponse.json(twoFileDiff())));
    await user.click(screen.getByText("Fix thing"));
    await user.click(await screen.findByRole("button", { name: "変更を表示" }));
    /* この帯ではコンパクトも全幅パネル = 背面を覆うので、モーダルとして扱う */
    const overlay = await screen.findByRole("dialog", { name: "worktree diff" });
    expect(overlay).toHaveAttribute("data-mode", "compact");
    expect(overlay).toHaveAttribute("aria-modal", "true");
    // 幅は CSS が全幅に固定するので、動かせない separator は出さない
    expect(within(overlay).queryByRole("separator")).toBeNull();
  });

  it("設定を先に開いてから diff が mount されても、diff は inert で focus を奪わない", async () => {
    /* diff は lazy chunk。解決前に Nav の設定を開くと、設定側からは #diff-overlay
     * が見えないので遮れない。diff が settingsOpen を見て自分で降りる。 */
    const root = document.createElement("div");
    root.id = "root";
    document.body.appendChild(root);
    try {
      server.use(http.get("/api/diff", () => HttpResponse.json(twoFileDiff())));
      const user = userEvent.setup();
      render(<App />, { container: root });
      streamSnapshot(issueSnapshot());

      await user.click(screen.getByText("Fix thing"));
      await user.click(await screen.findByRole("button", { name: "変更を表示" }));
      const overlay = await screen.findByRole("dialog", { name: "worktree diff" });

      await user.click(screen.getByRole("button", { name: "設定" }));
      const settings = await screen.findByRole("dialog", { name: "設定" });
      expect(overlay.hasAttribute("inert")).toBe(true);
      expect(settings.contains(document.activeElement)).toBe(true);

      await user.keyboard("{Escape}");
      expect(overlay.hasAttribute("inert")).toBe(false);
    } finally {
      root.remove();
    }
  });

  it("コンパクト表示の Escape は、フォーカスが背面にあるとき diff を閉じない", async () => {
    /* capture 段は React の handler より先に走る。無条件に閉じると背面で開いて
     * いる popup の Escape を横取りし、1 回のキーで 2 層が同時に閉じる。 */
    setInnerWidth(1600); // 1100px 以下だとコンパクトも covering になる
    localStorage.removeItem("fanout.diffView"); // 既定 = コンパクト
    const user = setup(http.get("/api/diff", () => HttpResponse.json(twoFileDiff())));

    await user.click(screen.getByText("Fix thing"));
    await user.click(await screen.findByRole("button", { name: "変更を表示" }));
    const overlay = await screen.findByRole("complementary", { name: "worktree diff" });

    // 背面のフィルタ popover を開いてから Escape
    await user.click(screen.getByRole("button", { name: "issue / runtime 状態で絞り込み" }));
    await screen.findByRole("listbox");
    await user.keyboard("{Escape}");

    expect(screen.queryByRole("listbox")).not.toBeInTheDocument(); // popover は閉じる
    expect(overlay).toBeInTheDocument(); // diff は残る

    // diff の中にフォーカスがあるときは従来どおり diff を閉じる
    overlay.focus();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("complementary", { name: "worktree diff" })).not.toBeInTheDocument();
  });

  it("モーダル中は Tab が中で循環し、閉じたらフォーカスが失われない", async () => {
    /* 背面を inert にしても、末尾から Tab / 先頭から Shift+Tab はブラウザ UI へ
     * 抜ける。aria-modal を名乗る以上は自前で折り返す。 */
    const root = document.createElement("div");
    root.id = "root";
    document.body.appendChild(root);
    try {
      server.use(http.get("/api/diff", () => HttpResponse.json(twoFileDiff())));
      const user = userEvent.setup();
      render(<App />, { container: root });
      streamSnapshot(issueSnapshot());

      const overlay = await openOverlay(user);
      const focusables = () => [...overlay.querySelectorAll<HTMLElement>("button")];

      // 先頭から Shift+Tab は末尾へ回る
      focusables()[0]!.focus();
      await user.tab({ shift: true });
      expect(document.activeElement).toBe(focusables().at(-1));

      // 末尾から Tab は先頭へ回る
      await user.tab();
      expect(document.activeElement).toBe(focusables()[0]);

      /* 起点が消えていてもフォーカスは失わない — 最後は Nav の歯車へ落とす */
      await user.keyboard("{Escape}");
      expect(document.activeElement).not.toBe(document.body);
    } finally {
      root.remove();
    }
  });

  it("同じ overlay のまま対象を切り替えると、新しい patch に入れ替わる", async () => {
    /* render 順(旧 patch が 1 コミット残らないこと)は useDiff.test.tsx が見る。
     * ここは overlay を作り直さずに対象を切り替えられることの担保。 */
    const patches: Record<string, string> = {
      "101": linesPatch("a.ts", 3, "patch_of_A"),
      "102": linesPatch("b.ts", 3, "patch_of_B"),
    };
    server.use(
      http.get("/api/diff", ({ request }) => {
        const issue = new URL(request.url).searchParams.get("issue") ?? "101";
        return HttpResponse.json(
          makeDiffResponse({
            patch: patches[issue]!,
            files: [makeDiffFile({ path: issue === "101" ? "a.ts" : "b.ts" })],
          }),
        );
      }),
    );
    render(<App />);
    streamSnapshot(
      makeSnapshot([
        makeSession("142", [
          makePane({ issueNum: 101, displayName: "Alpha", diffSummary: "+3/-0" }),
          makePane({ issueNum: 102, displayName: "Beta", diffSummary: "+3/-0" }),
        ]),
      ]),
    );
    const user = userEvent.setup();

    await user.click(await screen.findByRole("button", { name: /^変更を表示 #101 / }));
    const overlay = await screen.findByRole("dialog", { name: "worktree diff" });
    await waitFor(() => {
      expect(shadowText()).toContain("patch_of_A");
    });

    // B へ切り替える。B のタイトルが出た時点で A の patch は消えていること
    await user.click(screen.getByRole("button", { name: /^変更を表示 #102 / }));
    expect(within(overlay).getByText("#102")).toBeInTheDocument();
    expect(shadowText()).not.toContain("patch_of_A");

    await waitFor(() => {
      expect(shadowText()).toContain("patch_of_B");
    });
  });

  it("覆っているあいだは背面の document スクロールを止める", async () => {
    /* overlay は position:fixed でスクロールコンテナではなく、inert も scroll を
     * ロックしない。ヘッダ上のホイールや .diff-body 端でのチェーンが背面の一覧を
     * 動かし、閉じたときに読んでいた位置が変わってしまう。 */
    const user = setup(http.get("/api/diff", () => HttpResponse.json(twoFileDiff())));

    expect(document.documentElement.style.overflow).toBe("");
    const overlay = await openOverlay(user); // 既定は全画面 = covering
    expect(overlay).toHaveAttribute("data-mode", "full");
    expect(document.documentElement.style.overflow).toBe("hidden");

    await user.click(within(overlay).getByRole("button", { name: "diff を閉じる" }));
    expect(document.documentElement.style.overflow).toBe("");
  });

  it("lazy chunk 解決前に対象が消えても、フォーカスを失わない", async () => {
    /* この経路では DiffOverlay が一度も mount されない(Suspense fallback が
     * 消えるだけ)。復帰処理を overlay の cleanup に置いていると走らない。 */
    const root = document.createElement("div");
    root.id = "root";
    document.body.appendChild(root);
    try {
      server.use(http.get("/api/diff", () => HttpResponse.json(twoFileDiff())));
      const user = userEvent.setup();
      render(<App />, { container: root });
      streamSnapshot(issueSnapshot());

      await user.click(screen.getByText("Fix thing"));
      await user.click(await screen.findByRole("button", { name: "変更を表示" }));

      // 対象 pane が snapshot から消える
      streamSnapshot(makeSnapshot([makeSession("142", [])]));
      await waitFor(() => {
        expect(screen.queryByRole("dialog", { name: "worktree diff" })).not.toBeInTheDocument();
      });
      expect(document.activeElement).not.toBe(document.body);
    } finally {
      root.remove();
    }
  });

  it("背面が見えるコンパクト表示では document スクロールを止めない", async () => {
    setInnerWidth(1600); // 1100px 以下だとコンパクトも covering になる
    localStorage.removeItem("fanout.diffView"); // 既定 = コンパクト
    const user = setup(http.get("/api/diff", () => HttpResponse.json(twoFileDiff())));

    await user.click(screen.getByText("Fix thing"));
    await user.click(await screen.findByRole("button", { name: "変更を表示" }));
    await screen.findByRole("complementary", { name: "worktree diff" });
    expect(document.documentElement.style.overflow).toBe("");
  });

  it("同名 basename でも、移動ボタンと折りたたみボタンの名前で区別できる", async () => {
    /* 支援技術のボタン一覧から対象を特定できること。basename だけ / 「折りたたむ」
     * だけだと、別ディレクトリの同名 file や複数 file で名前が衝突する。 */
    const user = setup(
      http.get("/api/diff", () =>
        HttpResponse.json(
          makeDiffResponse({
            patch:
              linesPatch("src/index.ts", 3, "src_row") + linesPatch("test/index.ts", 3, "test_row"),
            files: [
              makeDiffFile({ path: "src/index.ts", additions: 3, deletions: 0 }),
              makeDiffFile({ path: "test/index.ts", additions: 3, deletions: 0 }),
            ],
          }),
        ),
      ),
    );

    const overlay = await openOverlay(user);
    const sidebar = within(overlay).getByRole("region", { name: "変更ファイル" });
    expect(
      within(sidebar).getByRole("button", { name: "src/index.ts — 新規追加" }),
    ).toBeInTheDocument();
    expect(
      within(sidebar).getByRole("button", { name: "test/index.ts — 新規追加" }),
    ).toBeInTheDocument();

    await waitFor(() => {
      expect(
        within(overlay).getByRole("button", { name: "src/index.ts — 折りたたむ" }),
      ).toBeInTheDocument();
    });
    expect(
      within(overlay).getByRole("button", { name: "test/index.ts — 折りたたむ" }),
    ).toBeInTheDocument();
  });

  it("同じ行キーのまま synthetic 行に置き換わったら diff を閉じる", async () => {
    /* 記録済み pane を cleanup すると sessionview は同じ rowKey のまま notStarted の
     * synthetic 行を作り直すので、行の存在だけを見ていると overlay が残り、
     * worktree も導線も無い行に cleanup 前の patch を出し続けてしまう。 */
    const user = setup(http.get("/api/diff", () => HttpResponse.json(twoFileDiff())));
    const overlay = await openOverlay(user);
    await waitFor(() => {
      expect(shadowText()).toContain("hello_marker");
    });

    // 同じ issue 番号のまま、worktree を持たない synthetic 行へ置き換わる
    streamSnapshot(
      makeSnapshot([
        makeSession("142", [makeQueuedPane({ issueNum: 101, displayName: "Fix thing" })]),
      ]),
    );
    await waitFor(() => {
      expect(overlay).not.toBeInTheDocument();
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
    expect(screen.queryByRole("button", { name: "変更を表示" })).not.toBeInTheDocument();

    await user.click(screen.getByText("Shell pane"));
    await screen.findByRole("complementary", { name: "ペイン詳細" });
    expect(screen.queryByRole("button", { name: "変更を表示" })).not.toBeInTheDocument();
  });
});

/* 確認済み(GitHub の Viewed 相当)。保存先は localStorage だけ — dashboard の
 * サーバーは GET-only で mutation endpoint を持たない。 */
describe("diff オーバーレイの確認済み", () => {
  /* 保存キーは projectRoot で分ける — ポート固定で別リポジトリを開くと rowKey が
     衝突する。fixture の projectRoot は "/tmp/repo"、行は parent 142 の issue 101。 */
  const VIEWED_KEY = `fanout.diffViewed.${viewedScope("/tmp/repo", "142#101")}`;

  /* 「src/hello.ts — 確認済み」のチェック。1 file に 1 個で、名前で引ける */
  const viewedBox = (overlay: HTMLElement, path: string) =>
    within(overlay).getByRole("checkbox", { name: `${path} — 確認済み` });

  it("チェックを入れるとその file を畳み、外すと開き直す", async () => {
    const user = setup(http.get("/api/diff", () => HttpResponse.json(twoFileDiff())));
    const overlay = await openOverlay(user);
    await waitFor(() => expect(shadowText()).toContain("hello_marker"));

    await user.click(viewedBox(overlay, "src/hello.ts"));
    await waitFor(() => expect(shadowText()).not.toContain("hello_marker"));
    // 他の file は畳まれない
    expect(shadowText()).toContain("util_marker");

    await user.click(viewedBox(overlay, "src/hello.ts"));
    await waitFor(() => expect(shadowText()).toContain("hello_marker"));
  });

  it("サイドバーに進捗とチェック済みの行を出す", async () => {
    const user = setup(http.get("/api/diff", () => HttpResponse.json(twoFileDiff())));
    const overlay = await openOverlay(user);
    const sidebar = await within(overlay).findByRole("region", { name: "変更ファイル" });
    expect(within(sidebar).getByText("0 / 2 確認済み")).toBeInTheDocument();

    await user.click(viewedBox(overlay, "src/hello.ts"));
    await waitFor(() => {
      expect(within(sidebar).getByText("1 / 2 確認済み")).toBeInTheDocument();
    });
    /* 行の accessible name にも状態を後置する — アイコンだけでは読み上げられない */
    expect(
      within(sidebar).getByRole("button", { name: "src/hello.ts — 変更 — 確認済み" }),
    ).toBeInTheDocument();
  });

  it("overlay を開き直しても確認済みが残る", async () => {
    const user = setup(http.get("/api/diff", () => HttpResponse.json(twoFileDiff())));
    const overlay = await openOverlay(user);
    await waitFor(() => expect(shadowText()).toContain("hello_marker"));
    await user.click(viewedBox(overlay, "src/hello.ts"));
    await waitFor(() => expect(localStorage.getItem(VIEWED_KEY)).not.toBeNull());

    /* Drawer は開いたままなので、行ではなく「変更を表示」から開き直す */
    await user.click(within(overlay).getByRole("button", { name: "diff を閉じる" }));
    await user.click(await screen.findByRole("button", { name: "変更を表示" }));
    const reopened = await screen.findByRole("dialog", { name: "worktree diff" });

    /* 復元した file は畳まれた状態で出る(展開ボタンが「展開」を名乗る) */
    await waitFor(() => {
      expect(viewedBox(reopened, "src/hello.ts")).toBeChecked();
    });
    expect(shadowText()).not.toContain("hello_marker");
    expect(shadowText()).toContain("util_marker");
  });

  it("再取得で中身が変わった file だけ確認済みを外す", async () => {
    /* patch を差し替えられるハンドラ。1 回目と 2 回目で hello.ts だけ中身が変わる */
    let payload = "hello_marker";
    const user = setup(
      http.get("/api/diff", () =>
        HttpResponse.json(
          twoFileDiff({
            patch: TWO_FILE_PATCH.replace("hello_marker", payload),
          }),
        ),
      ),
    );
    const overlay = await openOverlay(user);
    await waitFor(() => expect(shadowText()).toContain("hello_marker"));
    await user.click(viewedBox(overlay, "src/hello.ts"));
    await user.click(viewedBox(overlay, "src/util.ts"));
    await waitFor(() => expect(viewedBox(overlay, "src/util.ts")).toBeChecked());

    payload = "hello_changed";
    await user.click(within(overlay).getByRole("button", { name: "再取得" }));

    await waitFor(() => expect(shadowText()).toContain("hello_changed"));
    expect(viewedBox(overlay, "src/hello.ts")).not.toBeChecked();
    // 変わっていない file の確認済みは残る
    expect(viewedBox(overlay, "src/util.ts")).toBeChecked();
  });

  it("別の session 行へは確認済みが漏れない", async () => {
    server.use(
      http.get("/api/diff", ({ request }) => {
        const issue = new URL(request.url).searchParams.get("issue") ?? "101";
        return HttpResponse.json(
          makeDiffResponse({
            patch: linesPatch("a.ts", 3, `patch_of_${issue}`),
            files: [makeDiffFile({ path: "a.ts" })],
          }),
        );
      }),
    );
    render(<App />);
    streamSnapshot(
      makeSnapshot([
        makeSession("142", [
          makePane({ issueNum: 101, displayName: "Alpha", diffSummary: "+3/-0" }),
          makePane({ issueNum: 102, displayName: "Beta", diffSummary: "+3/-0" }),
        ]),
      ]),
    );
    const user = userEvent.setup();

    await user.click(await screen.findByRole("button", { name: /^変更を表示 #101 / }));
    const overlay = await screen.findByRole("dialog", { name: "worktree diff" });
    await waitFor(() => expect(shadowText()).toContain("patch_of_101"));
    await user.click(viewedBox(overlay, "a.ts"));
    await waitFor(() => expect(viewedBox(overlay, "a.ts")).toBeChecked());

    await user.click(screen.getByRole("button", { name: /^変更を表示 #102 / }));
    await waitFor(() => expect(shadowText()).toContain("patch_of_102"));
    expect(viewedBox(overlay, "a.ts")).not.toBeChecked();
  });

  /* 同じ worktree を指す行同士は patch が一致する。折りたたみの上書きを patch だけで
     区切ると、前の行で展開した状態が次の行へ持ち越され、確認済みで復元した file が
     開いたまま出る。 */
  it("同じ patch の別 session 行へ切り替えても、前の行の展開状態を持ち込まない", async () => {
    const patch = linesPatch("a.ts", 3, "shared_marker");
    server.use(
      http.get("/api/diff", () =>
        HttpResponse.json(makeDiffResponse({ patch, files: [makeDiffFile({ path: "a.ts" })] })),
      ),
    );
    render(<App />);
    streamSnapshot(
      makeSnapshot([
        makeSession("142", [
          makePane({ issueNum: 101, displayName: "Alpha", diffSummary: "+3/-0" }),
          makePane({ issueNum: 102, displayName: "Beta", diffSummary: "+3/-0" }),
        ]),
      ]),
    );
    const user = userEvent.setup();

    // #102 側で確認済みにしておく(= 開き直したら畳まれて出るはず)
    await user.click(await screen.findByRole("button", { name: /^変更を表示 #102 / }));
    const overlay = await screen.findByRole("dialog", { name: "worktree diff" });
    await waitFor(() => expect(shadowText()).toContain("shared_marker"));
    await user.click(viewedBox(overlay, "a.ts"));
    await waitFor(() => expect(shadowText()).not.toContain("shared_marker"));

    // #101 へ移って展開の上書きを作る(こちらは未確認なので開いている)
    await user.click(screen.getByRole("button", { name: /^変更を表示 #101 / }));
    await waitFor(() => expect(shadowText()).toContain("shared_marker"));
    await user.click(within(overlay).getByRole("button", { name: /^a\.ts — 折りたたむ$/ }));
    await waitFor(() => expect(shadowText()).not.toContain("shared_marker"));
    await user.click(within(overlay).getByRole("button", { name: /^a\.ts — .* — 展開$/ }));
    await waitFor(() => expect(shadowText()).toContain("shared_marker"));

    // #102 へ戻す。確認済みなので畳まれて出る(#101 の上書きを引きずらない)
    await user.click(screen.getByRole("button", { name: /^変更を表示 #102 / }));
    await waitFor(() => expect(viewedBox(overlay, "a.ts")).toBeChecked());
    expect(shadowText()).not.toContain("shared_marker");
  });

  it("確認済みを隠すと本文と一覧の両方から降ろす", async () => {
    const user = setup(http.get("/api/diff", () => HttpResponse.json(twoFileDiff())));
    const overlay = await openOverlay(user);
    await waitFor(() => expect(shadowText()).toContain("hello_marker"));
    await user.click(viewedBox(overlay, "src/hello.ts"));

    await user.click(within(overlay).getByRole("button", { name: "確認済みを隠す" }));

    await waitFor(() => {
      expect(within(overlay).queryByRole("checkbox", { name: /^src\/hello\.ts/ })).toBeNull();
    });
    const sidebar = within(overlay).getByRole("region", { name: "変更ファイル" });
    expect(within(sidebar).queryByText("hello.ts")).toBeNull();
    // 残りは触れる。分母は変わらない(確認済みの母集団は patch を持つ file 全部)
    expect(within(sidebar).getByText("1 / 2 確認済み")).toBeInTheDocument();
    expect(shadowText()).toContain("util_marker");

    await user.click(within(overlay).getByRole("button", { name: "確認済みも表示" }));
    await waitFor(() => expect(viewedBox(overlay, "src/hello.ts")).toBeChecked());
  });

  /* 隠す設定では、チェックした行ごと unmount される。overlay の一番上へ戻すと
     キーボードだけで読む人は file ごとにヘッダ全部を Tab で辿り直すことになる。 */
  it("隠す設定でチェックすると、次に読む file のチェックへフォーカスが移る", async () => {
    const user = setup(http.get("/api/diff", () => HttpResponse.json(twoFileDiff())));
    const overlay = await openOverlay(user);
    await waitFor(() => expect(shadowText()).toContain("hello_marker"));
    await user.click(within(overlay).getByRole("button", { name: "確認済みを隠す" }));

    await user.click(viewedBox(overlay, "src/hello.ts"));

    await waitFor(() => {
      expect(document.activeElement).toBe(viewedBox(overlay, "src/util.ts"));
    });
  });

  /* 「すべて」とは言わない — patch を持たない file は本文に出ないので、
     全部見たと読める文言は嘘になりうる(警告帯と省略一覧が残件を出している)。 */
  it("patch のある file を全部確認済みにして隠すと、空になった理由を出す", async () => {
    const user = setup(http.get("/api/diff", () => HttpResponse.json(twoFileDiff())));
    const overlay = await openOverlay(user);
    await waitFor(() => expect(shadowText()).toContain("hello_marker"));
    await user.click(within(overlay).getByRole("button", { name: "確認済みを隠す" }));
    await user.click(viewedBox(overlay, "src/hello.ts"));
    await user.click(viewedBox(overlay, "src/util.ts"));

    await waitFor(() => {
      expect(
        within(overlay).getByText("patch のあるファイルはすべて確認済みです"),
      ).toBeInTheDocument();
    });
    /* 隠した file ごと unmount されるので、フォーカスの引き取りが要る。DOM の
       入れ替わりを待つため次フレームで動くので、ここも待って確かめる。 */
    await waitFor(() => expect(document.activeElement).not.toBe(document.body));
  });

  /* 確認済みを外すのは「上書きの取り消し」であって「開け」ではない。false を
     書くと、1,000 行超で既定折りたたみだった file が開いたことすら無い状態から
     全開になり、読んでいた位置が飛ぶ。 */
  it("既定で畳まれる大きい file は、確認済みを外しても畳まれたまま", async () => {
    const user = setup(
      http.get("/api/diff", () =>
        HttpResponse.json(
          twoFileDiff({
            patch: TWO_FILE_PATCH + linesPatch("bomb.txt", 1600, "payload_row"),
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
    await waitFor(() => expect(shadowText()).toContain("hello_marker"));
    // 最初から畳まれている
    expect(shadowText()).not.toContain("payload_row");

    await user.click(viewedBox(overlay, "bomb.txt"));
    await waitFor(() => expect(viewedBox(overlay, "bomb.txt")).toBeChecked());
    await user.click(viewedBox(overlay, "bomb.txt"));
    await waitFor(() => expect(viewedBox(overlay, "bomb.txt")).not.toBeChecked());

    expect(shadowText()).not.toContain("payload_row");
  });

  /* 不正 UTF-8 の byte は正規化で U+FFFD へ潰れ、別 file が同じ key に集まる。
     チェックを出すと片方を読んだだけでもう片方も確認済みになる。 */
  it("正規化で衝突した file にはチェックを出さない", async () => {
    const patch = [
      'diff --git "a/docs/\\200.md" "b/docs/\\200.md"',
      '--- "a/docs/\\200.md"',
      '+++ "b/docs/\\200.md"',
      "@@ -1 +1 @@",
      "-one",
      "+first_marker",
      'diff --git "a/docs/\\201.md" "b/docs/\\201.md"',
      '--- "a/docs/\\201.md"',
      '+++ "b/docs/\\201.md"',
      "@@ -1 +1 @@",
      "-two",
      "+second_marker",
      "",
    ].join("\n");
    const user = setup(
      http.get("/api/diff", () =>
        HttpResponse.json(
          makeDiffResponse({ patch, files: [makeDiffFile({ path: "docs/�.md" })] }),
        ),
      ),
    );
    const overlay = await openOverlay(user);
    await waitFor(() => expect(shadowText()).toContain("first_marker"));
    expect(shadowText()).toContain("second_marker");
    expect(within(overlay).queryAllByRole("checkbox")).toHaveLength(0);
  });

  /* 確認済みの折りたたみは上書きではなく導出。上書きを書くと、別タブで外された
     ときにチェックだけ外れて本文が畳まれたまま残る。 */
  it("別タブで外されたら、チェックも折りたたみも一緒に戻る", async () => {
    const user = setup(http.get("/api/diff", () => HttpResponse.json(twoFileDiff())));
    const overlay = await openOverlay(user);
    await waitFor(() => expect(shadowText()).toContain("hello_marker"));
    await user.click(viewedBox(overlay, "src/hello.ts"));
    await waitFor(() => expect(shadowText()).not.toContain("hello_marker"));

    // 別タブが外した体。storage イベントは書いた本人には飛ばないので自前で撒く
    localStorage.removeItem(VIEWED_KEY);
    window.dispatchEvent(
      new StorageEvent("storage", { key: VIEWED_KEY, storageArea: localStorage }),
    );

    await waitFor(() => expect(viewedBox(overlay, "src/hello.ts")).not.toBeChecked());
    expect(shadowText()).toContain("hello_marker");
  });

  it("壊れた保存値は無視して、確認済み無しとして開く", async () => {
    localStorage.setItem(VIEWED_KEY, '{"v":1,"t":1,"files":["src/hello.ts"]}');
    const user = setup(http.get("/api/diff", () => HttpResponse.json(twoFileDiff())));
    const overlay = await openOverlay(user);
    await waitFor(() => expect(shadowText()).toContain("hello_marker"));
    expect(viewedBox(overlay, "src/hello.ts")).not.toBeChecked();
  });
});
