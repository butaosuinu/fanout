import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { beforeEach, describe, expect, it } from "vitest";
import { App } from "../app/App";
import { DELETE_BRANCH_PATH, MERGE_PATH } from "../features/merge/merge";
import type { DiffResponse, PRRef } from "../transport/types";
import { installFakeEventSource, streamSnapshot } from "./fakeEventSource";
import { makeDiffResponse, makePane, makePr, makeSession, makeSnapshot } from "./fixtures";
import { server } from "./server";

/* マージボタンの統合テスト。モックはネットワーク境界のみ(/api/pr/merge と
 * /api/peek は MSW、SSE は FakeEventSource)。内部コンポーネント・hooks は
 * モックしない。 */

beforeEach(() => {
  installFakeEventSource();
  localStorage.clear();
  document.documentElement.dataset.theme = "light";
  /* マージは token gate 必須(サーバが --no-token では拒否する)。SPA は
   * ページ URL の ?token= を読むので、テストもそこに置く。 */
  window.history.replaceState({}, "", "/?token=t0ken");
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

interface MergeCall {
  url: URL;
  body: Record<string, unknown>;
}

/* POST を記録する handler。status を渡すとその応答を返し、body は {"error"} 形式。 */
function mergeHandler(
  calls: MergeCall[],
  reply?: { status: number; error?: string; code?: string },
) {
  return http.post(MERGE_PATH, async ({ request }) => {
    calls.push({
      url: new URL(request.url),
      body: (await request.json()) as Record<string, unknown>,
    });
    if (reply) {
      return HttpResponse.json(
        { error: reply.error ?? "nope", code: reply.code ?? "x" },
        { status: reply.status },
      );
    }
    return HttpResponse.json({
      prNumber: 701,
      method: "squash",
      merged: true,
      branchDeleted: false,
      refreshQueued: true,
    });
  });
}

function snapshotWithPR(over: Partial<PRRef> = {}) {
  return makeSnapshot([
    makeSession("142", [
      makePane({
        issueNum: 101,
        displayName: "Fix login",
        slug: "fix-login",
        paneId: "%1",
        branchName: "fanout/fix-login",
        prs: [makePr({ headRef: "fanout/fix-login", ...over })],
      }),
    ]),
  ]);
}

/* 開いていなければ開く。diff を開くとドロワーも開いたままなので、同じテストで
 * 両方を見るときに二重クリックしない。 */
const openDrawer = async (user: ReturnType<typeof userEvent.setup>) => {
  const existing = screen.queryByRole("complementary", { name: "ペイン詳細" });
  if (existing) return existing;
  await user.click(screen.getByRole("cell", { name: "Fix login" }));
  return await screen.findByRole("complementary", { name: "ペイン詳細" });
};

async function openDiff(
  user: ReturnType<typeof userEvent.setup>,
  diff: Partial<DiffResponse> = {},
) {
  server.use(http.get("/api/diff", () => HttpResponse.json(makeDiffResponse(diff))));
  const drawer = await openDrawer(user);
  await user.click(within(drawer).getByRole("button", { name: "変更を表示" }));
  return await screen.findByRole("dialog");
}

describe("マージボタンの出現条件", () => {
  it("PR の無い行にはドロワーにも diff ツールバーにも出ない", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(
      makeSnapshot([makeSession("142", [makePane({ issueNum: 101, displayName: "Fix login" })])]),
    );

    const drawer = await openDrawer(user);
    expect(within(drawer).queryByRole("button", { name: /をマージ/ })).toBeNull();

    const overlay = await openDiff(user);
    expect(within(overlay).queryByRole("button", { name: /をマージ/ })).toBeNull();
  });

  it("PR のある行ではドロワーと diff ツールバーの両方に出る", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR());

    const drawer = await openDrawer(user);
    expect(within(drawer).getByRole("button", { name: "#701 をマージ" })).toBeInTheDocument();

    const overlay = await openDiff(user);
    expect(within(overlay).getByRole("button", { name: "#701 をマージ" })).toBeInTheDocument();
  });

  it("Drawer 上部バーは Session 名 → 差分行数 → 変更を表示 → マージ → 閉じる の順に並べる", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR());

    const drawer = await openDrawer(user);
    const head = drawer.querySelector(".drawer-head");
    const ids = [...(head?.children ?? [])].map((el) => el.id || el.className);
    expect(ids).toEqual(["", "d-diff-stat", "d-diff-open", "merge-split", "drawer-close"]);
  });

  it("diff ツールバーではマージが再取得の直前に出る", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR());

    const overlay = await openDiff(user);
    const head = overlay.querySelector(".diff-head");
    const positions = [...(head?.children ?? [])].map((el) => el.id || el.className);
    expect(positions.indexOf("merge-split")).toBe(positions.indexOf("diff-reload") - 1);
  });
});

describe("マージの実行", () => {
  it("押すと現在の方式で POST し、identity と head sha を送る", async () => {
    const calls: MergeCall[] = [];
    server.use(mergeHandler(calls));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR());

    const drawer = await openDrawer(user);
    await user.click(within(drawer).getByRole("button", { name: "#701 をマージ" }));

    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]?.url.searchParams.get("parent")).toBe("142");
    expect(calls[0]?.url.searchParams.get("issue")).toBe("101");
    expect(calls[0]?.body).toEqual({
      prNumber: 701,
      headSha: "0123456789abcdef0123456789abcdef01234567",
      baseRef: "main",
      method: "squash",
    });
  });

  it("メニューから方式を選ぶとその方式で即実行し、既定として覚える", async () => {
    const calls: MergeCall[] = [];
    server.use(mergeHandler(calls));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR());

    const drawer = await openDrawer(user);
    await user.click(within(drawer).getByRole("button", { name: /マージ方式/ }));
    await user.click(await screen.findByRole("menuitemradio", { name: /rebase/ }));

    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]?.body["method"]).toBe("rebase");
    expect(localStorage.getItem("fanout.mergeMethod")).toBe("rebase");
  });

  it("方式の選択はドロワーと diff ツールバーで共有される", async () => {
    const calls: MergeCall[] = [];
    server.use(mergeHandler(calls));
    localStorage.setItem("fanout.mergeMethod", "merge");
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR());

    const overlay = await openDiff(user);
    expect(within(overlay).getByRole("button", { name: /merge commit/ })).toBeInTheDocument();
    const drawer = screen.getByRole("complementary", { name: "ペイン詳細" });
    expect(within(drawer).getByRole("button", { name: /merge commit/ })).toBeInTheDocument();
  });

  it("連打しても POST は 1 回だけ飛ぶ", async () => {
    const calls: MergeCall[] = [];
    server.use(mergeHandler(calls));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR());

    const drawer = await openDrawer(user);
    const button = within(drawer).getByRole("button", { name: "#701 をマージ" });
    await user.click(button);
    await user.click(button);
    await user.click(button);

    await waitFor(() => expect(calls.length).toBeGreaterThan(0));
    expect(calls).toHaveLength(1);
  });

  it("成功後は反映待ちで押せなくなり、MERGED の snapshot で解ける", async () => {
    server.use(mergeHandler([]));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR());

    const drawer = await openDrawer(user);
    await user.click(within(drawer).getByRole("button", { name: "#701 をマージ" }));
    await screen.findByRole("button", { name: /反映を待っています/ });

    streamSnapshot(snapshotWithPR({ state: "MERGED" }));
    await screen.findByRole("button", { name: /既にマージ済み/ });
  });
});

describe("マージの失敗", () => {
  it("GitHub の拒否理由を表示し、再試行できる", async () => {
    const calls: MergeCall[] = [];
    server.use(mergeHandler(calls, { status: 422, error: "Pull request is not mergeable" }));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR());

    const drawer = await openDrawer(user);
    await user.click(within(drawer).getByRole("button", { name: "#701 をマージ" }));

    await screen.findByText(/Pull request is not mergeable/);
    await user.click(within(drawer).getByRole("button", { name: "#701 をマージ" }));
    await waitFor(() => expect(calls).toHaveLength(2));
  });

  /* 表示された 127.0.0.1 ではなく localhost で開いたときに起きる。token は
   * 正しいので、token を疑わせる文言を出してはいけない。 */
  it("同一 origin ゲートの 403 は token ではなく URL の問題だと伝える", async () => {
    server.use(mergeHandler([], { status: 403, code: "host" }));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR());

    const drawer = await openDrawer(user);
    await user.click(within(drawer).getByRole("button", { name: "#701 をマージ" }));
    await screen.findByText(/127\.0\.0\.1 の URL で開き直して/);
  });

  /* merge を知らない旧バイナリの getOnly が返す実在ケース。 */
  it("405 はサーバーが merge 非対応だと伝える", async () => {
    server.use(mergeHandler([], { status: 405 }));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR());

    const drawer = await openDrawer(user);
    await user.click(within(drawer).getByRole("button", { name: "#701 をマージ" }));
    await screen.findByText(/merge に対応していません/);
  });
});

describe("無効化", () => {
  const blocked: { name: string; pr: Partial<PRRef>; reason: RegExp }[] = [
    { name: "merged", pr: { state: "MERGED" }, reason: /既にマージ済み/ },
    { name: "closed", pr: { state: "CLOSED" }, reason: /close 済み/ },
    { name: "draft", pr: { isDraft: true }, reason: /draft PR/ },
    { name: "conflicting", pr: { mergeable: "CONFLICTING" }, reason: /競合しています/ },
  ];

  for (const c of blocked) {
    it(`${c.name} はボタンが無効になり、押しても POST が飛ばない`, async () => {
      const calls: MergeCall[] = [];
      server.use(mergeHandler(calls));
      const user = userEvent.setup();
      render(<App />);
      streamSnapshot(snapshotWithPR(c.pr));

      const drawer = await openDrawer(user);
      const button = within(drawer).getByRole("button", { name: c.reason });
      expect(button).toHaveAttribute("aria-disabled", "true");
      await user.click(button);
      expect(calls).toHaveLength(0);
    });
  }

  /* merged PR は mergeable が常に欠落するので、警告を無効行にも出すと
   * 「競合の有無が不明」がマージ済みの行に必ず付き、押すとメニューまで開く。 */
  it("無効なボタンはメニューも開かない", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR({ state: "MERGED", mergeable: undefined }));

    const drawer = await openDrawer(user);
    await user.click(within(drawer).getByRole("button", { name: /既にマージ済み/ }));
    expect(screen.queryByRole("menu")).toBeNull();
  });

  /* 本体だけ塞いでも、caret から方式を選べば実行できてしまう。merged 行で
   * 二重にマージが飛ぶ経路なので、メニュー側にも同じゲートを通す。 */
  it("無効な行は caret も塞ぎ、メニュー経由でもマージできない", async () => {
    const calls: MergeCall[] = [];
    server.use(mergeHandler(calls));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR({ state: "MERGED" }));

    const drawer = await openDrawer(user);
    const caret = within(drawer).getByRole("button", { name: /マージ方式/ });
    expect(caret).toHaveAttribute("aria-disabled", "true");
    await user.click(caret);
    expect(screen.queryByRole("menu")).toBeNull();
    expect(calls).toHaveLength(0);
  });

  /* mergeable 欠落は「不明」であって「衝突あり」ではない。 */
  it("mergeable の欠落では無効化しない", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR({ mergeable: undefined }));

    const drawer = await openDrawer(user);
    expect(within(drawer).getByRole("button", { name: "#701 をマージ" })).not.toHaveAttribute(
      "aria-disabled",
    );
  });

  /* branch protection の内容は wire に無いので、押せるままにして警告を出す。 */
  it("CI 失敗は押せるが、本体を押すと警告付きのメニューが開く", async () => {
    const calls: MergeCall[] = [];
    server.use(mergeHandler(calls));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR({ ci: "fail" }));

    const drawer = await openDrawer(user);
    await user.click(within(drawer).getByRole("button", { name: "#701 をマージ" }));

    const menu = await screen.findByRole("menu");
    expect(within(menu).getByText(/CI が失敗しています/)).toBeInTheDocument();
    expect(calls).toHaveLength(0);

    await user.click(within(menu).getByRole("menuitemradio", { name: /squash/ }));
    await waitFor(() => expect(calls).toHaveLength(1));
  });
});

describe("表示中の差分との整合", () => {
  /* diff は明示的な再取得まで最初の patch を保持する。開いたまま agent が push
   * すると 3 段照合はすべて新しい head で一致してしまい、見ていない変更が通る。 */
  it("diff を開いたまま PR が進んだらツールバーのマージを塞ぐ", async () => {
    const calls: MergeCall[] = [];
    server.use(mergeHandler(calls));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR());

    const overlay = await openDiff(user);
    expect(within(overlay).getByRole("button", { name: "#701 をマージ" })).toBeInTheDocument();

    streamSnapshot(snapshotWithPR({ headSha: "1111111111111111111111111111111111111111" }));

    const blocked = await within(overlay).findByRole("button", { name: /再取得してから/ });
    expect(blocked).toHaveAttribute("aria-disabled", "true");
    await user.click(blocked);
    expect(calls).toHaveLength(0);

    /* compact 表示では Drawer が横に並ぶ。片方だけ塞いでも、隣のボタンから同じ
     * マージが撃ててしまうので、行単位で両方を塞ぐ。 */
    const drawer = screen.getByRole("complementary", { name: "ペイン詳細" });
    expect(within(drawer).getByRole("button", { name: /再取得してから/ })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
  });

  /* /api/diff は staged / unstaged / untracked も描く。dirty な worktree では、
   * 画面で確認した修正のうち commit 済みの分しかマージされない。 */
  it("未コミットの変更を含む差分を見ている間はツールバーから撃てない", async () => {
    const calls: MergeCall[] = [];
    server.use(mergeHandler(calls));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR());

    const overlay = await openDiff(user, { dirty: true });
    const blocked = await within(overlay).findByRole("button", { name: /未コミットの変更/ });
    expect(blocked).toHaveAttribute("aria-disabled", "true");
    await user.click(blocked);
    expect(calls).toHaveLength(0);
  });

  /* patch がまだ無い間(初回ロード・再取得・取得失敗)は、その patch について
   * 何も言えない。既定の facts はいちばん厳しい側に倒してある。 */
  it("差分を取得できていない間はツールバーから撃てない", async () => {
    const calls: MergeCall[] = [];
    server.use(mergeHandler(calls));
    server.use(http.get("/api/diff", () => HttpResponse.json({ error: "boom" }, { status: 500 })));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR());

    const drawer = await openDrawer(user);
    await user.click(within(drawer).getByRole("button", { name: "変更を表示" }));
    const overlay = await screen.findByRole("dialog");
    const blocked = await within(overlay).findByRole("button", {
      name: /差分を取得できていません/,
    });
    expect(blocked).toHaveAttribute("aria-disabled", "true");
    await user.click(blocked);
    expect(calls).toHaveLength(0);
  });

  /* `--base-branch origin/main` は記録どおり "origin/main" のまま state に載る一方、
   * PR の baseRef は "main"。書き方の違いで正当な diff を塞いではいけない。 */
  it("origin/ 付きで記録された base は PR の base と同じものとして扱う", async () => {
    server.use(mergeHandler([]));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(
      makeSnapshot([
        makeSession("142", [
          makePane({
            issueNum: 101,
            displayName: "Fix login",
            slug: "fix-login",
            paneId: "%1",
            branchName: "fanout/fix-login",
            baseBranch: "origin/main",
            prs: [makePr({ headRef: "fanout/fix-login" })],
          }),
        ]),
      ]),
    );

    const overlay = await openDiff(user);
    expect(within(overlay).getByRole("button", { name: "#701 をマージ" })).not.toHaveAttribute(
      "aria-disabled",
    );
  });

  /* base の記録が無い行では、サーバは origin/HEAD との差分を返す。別 base 向けの
   * PR を、既定 branch 基準の patch で承認させない。 */
  it("base が記録されていない行はツールバーから撃てない", async () => {
    const calls: MergeCall[] = [];
    server.use(mergeHandler(calls));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(
      makeSnapshot([
        makeSession("142", [
          makePane({
            issueNum: 101,
            displayName: "Fix login",
            slug: "fix-login",
            paneId: "%1",
            branchName: "fanout/fix-login",
            baseBranch: "",
            prs: [makePr({ headRef: "fanout/fix-login" })],
          }),
        ]),
      ]),
    );

    const overlay = await openDiff(user);
    const blocked = await within(overlay).findByRole("button", {
      name: /この PR のものではありません/,
    });
    expect(blocked).toHaveAttribute("aria-disabled", "true");
    await user.click(blocked);
    expect(calls).toHaveLength(0);
  });

  /* MergeBase はローカル base branch を先に選ぶので、未 push の commit があると
   * その分が patch から落ちる一方、マージでは base に入る。 */
  it("未 push の base を基準にした差分を見ている間はツールバーから撃てない", async () => {
    const calls: MergeCall[] = [];
    server.use(mergeHandler(calls));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR());

    const overlay = await openDiff(user, { basePushed: false });
    const blocked = await within(overlay).findByRole("button", { name: /未 push の base/ });
    expect(blocked).toHaveAttribute("aria-disabled", "true");
    await user.click(blocked);
    expect(calls).toHaveLength(0);
  });

  /* branch 名は照合材料として弱い。別の checkout から PR branch へ push されると、
   * ローカル worktree は遅れたまま名前だけ全部一致し、画面に無い commit が
   * マージされる。patch を取った commit そのものを見る。 */
  it("表示中の patch を取った commit と PR head が違えばツールバーから撃てない", async () => {
    const calls: MergeCall[] = [];
    server.use(mergeHandler(calls));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR());

    const overlay = await openDiff(user, {
      headCommit: "9999999999999999999999999999999999999999",
    });
    const blocked = await within(overlay).findByRole("button", {
      name: /この PR のものではありません/,
    });
    expect(blocked).toHaveAttribute("aria-disabled", "true");
    await user.click(blocked);
    expect(calls).toHaveLength(0);
  });

  it("patch の commit が PR head と同じなら塞がない", async () => {
    server.use(mergeHandler([]));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR());

    const overlay = await openDiff(user, {
      headCommit: "0123456789abcdef0123456789abcdef01234567",
    });
    expect(within(overlay).getByRole("button", { name: "#701 をマージ" })).not.toHaveAttribute(
      "aria-disabled",
    );
  });

  /* patch は worktree の base branch との差分。retarget は head を 1 commit も
   * 動かさないので、head だけを見る pin と 3 段照合はすべて素通りする。 */
  it("PR が別の base へ retarget されたらツールバーのマージを塞ぐ", async () => {
    const calls: MergeCall[] = [];
    server.use(mergeHandler(calls));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR());

    const overlay = await openDiff(user);
    expect(within(overlay).getByRole("button", { name: "#701 をマージ" })).toBeInTheDocument();

    streamSnapshot(snapshotWithPR({ baseRef: "release" }));

    const blocked = await within(overlay).findByRole("button", {
      name: /この PR のものではありません/,
    });
    expect(blocked).toHaveAttribute("aria-disabled", "true");
    await user.click(blocked);
    expect(calls).toHaveLength(0);
  });

  /* /api/diff は pane の worktree を読む。issue 行には fork の closing PR が
   * 載りうるので、それを掴むと画面の patch と別物をマージすることになる。head が
   * 動いたかを見る pin では、この初手のズレは捕まらない。 */
  it("表示中の worktree の branch を head に持たない PR はツールバーから撃てない", async () => {
    const calls: MergeCall[] = [];
    server.use(mergeHandler(calls));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR({ headRepo: "stranger/fork", headRef: "their-branch" }));

    const overlay = await openDiff(user);
    const blocked = await within(overlay).findByRole("button", {
      name: /この PR のものではありません/,
    });
    expect(blocked).toHaveAttribute("aria-disabled", "true");
    await user.click(blocked);
    expect(calls).toHaveLength(0);

    /* 塞ぐのは行単位。隣に並ぶ Drawer から同じマージが撃てては意味がない。 */
    const drawer = screen.getByRole("complementary", { name: "ペイン詳細" });
    expect(
      within(drawer).getByRole("button", { name: /この PR のものではありません/ }),
    ).toHaveAttribute("aria-disabled", "true");
  });
});

describe("pin の張り直し", () => {
  /* compact 表示のまま別行の diff を開くと DiffOverlaySlot は再 mount されない。
   * pin を張り直さないと前の行の SHA が残り、正当なマージが塞がる。 */
  it("diff の対象を切り替えたら pin を張り直す", async () => {
    const calls: MergeCall[] = [];
    server.use(mergeHandler(calls));
    localStorage.setItem("fanout.diffView", "compact");
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(
      makeSnapshot([
        makeSession("142", [
          makePane({
            issueNum: 101,
            displayName: "Fix login",
            branchName: "fanout/fix-login",
            prs: [makePr({ number: 701, headRef: "fanout/fix-login" })],
          }),
          makePane({
            issueNum: 102,
            displayName: "Add docs",
            slug: "add-docs",
            paneId: "%2",
            branchName: "fanout/add-docs",
            prs: [
              makePr({
                number: 702,
                headRef: "fanout/add-docs",
                headSha: "2222222222222222222222222222222222222222",
              }),
            ],
          }),
        ]),
      ]),
    );

    const overlay = await openDiff(user);
    expect(within(overlay).getByRole("button", { name: "#701 をマージ" })).toBeInTheDocument();

    /* 2 行目の diff セルへ切り替える(overlay は開いたまま) */
    await user.click(screen.getByRole("cell", { name: "Add docs" }));
    const drawer2 = await screen.findByRole("complementary", { name: "ペイン詳細" });
    await user.click(within(drawer2).getByRole("button", { name: "変更を表示" }));

    const after = await screen.findByRole("dialog");
    await within(after).findByRole("button", { name: "#702 をマージ" });
  });
});

describe("token gate", () => {
  /* --no-token のダッシュボードではサーバが必ず 403 を返す。押すたびに失敗させず、
   * 押せない理由を先に見せる。 */
  it("token の無い URL ではボタンを無効化する", async () => {
    window.history.replaceState({}, "", "/");
    const calls: MergeCall[] = [];
    server.use(mergeHandler(calls));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR());

    const drawer = await openDrawer(user);
    const button = within(drawer).getByRole("button", { name: /--no-token/ });
    expect(button).toHaveAttribute("aria-disabled", "true");
    await user.click(button);
    expect(calls).toHaveLength(0);
  });
});

describe("マージ対象の PR", () => {
  /* prPrimary は「最初の MERGED を優先」なので、そのまま使うと merged PR と
   * open PR が並ぶ行で永久に無効なボタンを描いてしまう。 */
  it("merged PR と open PR が並ぶ行では open な方を対象にする", async () => {
    const calls: MergeCall[] = [];
    server.use(mergeHandler(calls));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(
      makeSnapshot([
        makeSession("142", [
          makePane({
            issueNum: 101,
            displayName: "Fix login",
            branchName: "fanout/fix-login",
            prs: [
              makePr({ number: 700, state: "MERGED", headRef: "fanout/fix-login" }),
              makePr({ number: 701, state: "OPEN", headRef: "fanout/fix-login" }),
            ],
          }),
        ]),
      ]),
    );

    const drawer = await openDrawer(user);
    const button = within(drawer).getByRole("button", { name: "#701 をマージ" });
    expect(button).not.toHaveAttribute("aria-disabled");
    await user.click(button);
    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]?.body["prNumber"]).toBe(701);
  });
});

describe("merge queue", () => {
  /* `gh pr merge` は queue 必須の base で 0 終了する。merged 扱いにすると嘘に
   * なる。かといって押せる状態に戻すのも誤り — auto-merge は既に武装済みで、
   * サーバも同じ理由で claim に載せるので、二度目は必ず 409 になる。 */
  it("queued はマージ済みにせず、決着まで塞ぐ", async () => {
    server.use(
      http.post(MERGE_PATH, () =>
        HttpResponse.json({
          prNumber: 701,
          method: "squash",
          merged: false,
          queued: true,
          branchDeleted: false,
          refreshQueued: true,
        }),
      ),
    );
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR());

    const drawer = await openDrawer(user);
    await user.click(within(drawer).getByRole("button", { name: "#701 をマージ" }));
    await screen.findByText(/merge queue 待ち/);
    const button = within(drawer).getByRole("button", { name: /反映を待っています/ });
    expect(button).toHaveAttribute("aria-disabled", "true");
  });
});

/* PR を続けて操作したときに、前の hold が落ちないこと。落ちると、決着していない
 * PR のボタンが押せる状態に戻り、押しても必ずサーバに 409 で弾かれる。 */
describe("複数 PR の同時待機", () => {
  it("2 本目の queued は 1 本目の hold を落とさない", async () => {
    server.use(
      http.post(MERGE_PATH, async ({ request }) => {
        const body = (await request.json()) as { prNumber: number };
        return HttpResponse.json({
          prNumber: body.prNumber,
          method: "squash",
          merged: false,
          queued: true,
          refreshQueued: true,
        });
      }),
    );
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(
      makeSnapshot([
        makeSession("142", [
          makePane({
            issueNum: 101,
            displayName: "Fix login",
            slug: "fix-login",
            paneId: "%1",
            branchName: "fanout/fix-login",
            prs: [makePr({ number: 701, headRef: "fanout/fix-login" })],
          }),
          makePane({
            issueNum: 102,
            displayName: "Add docs",
            slug: "add-docs",
            paneId: "%2",
            branchName: "fanout/add-docs",
            prs: [makePr({ number: 702, headRef: "fanout/add-docs" })],
          }),
        ]),
      ]),
    );

    await user.click(screen.getByRole("cell", { name: "Fix login" }));
    const drawer = await screen.findByRole("complementary", { name: "ペイン詳細" });
    await user.click(within(drawer).getByRole("button", { name: "#701 をマージ" }));
    await screen.findByText(/merge queue 待ち/);

    await user.click(screen.getByRole("cell", { name: "Add docs" }));
    const second = await screen.findByRole("complementary", { name: "ペイン詳細" });
    await user.click(within(second).getByRole("button", { name: "#702 をマージ" }));
    await waitFor(() =>
      expect(
        within(second).getByRole("button", { name: /反映を待っています/ }),
      ).toBeInTheDocument(),
    );

    /* 1 本目へ戻ると、まだ塞がっている。 */
    await user.click(screen.getByRole("cell", { name: "Fix login" }));
    const back = await screen.findByRole("complementary", { name: "ペイン詳細" });
    expect(within(back).getByRole("button", { name: /反映を待っています/ })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
  });
});

/* サーバは auto-merge の取り消しで claim を解除できる。クライアントが塞いだままだと
 * その経路に到達できず、リロードするまでボタンが死ぬ。 */
describe("queue の取り消し", () => {
  it("保留が消えたら queued の hold を解除する", async () => {
    server.use(
      http.post(MERGE_PATH, () =>
        HttpResponse.json({
          prNumber: 701,
          method: "squash",
          merged: false,
          queued: true,
          refreshQueued: true,
        }),
      ),
    );
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR());

    const drawer = await openDrawer(user);
    await user.click(within(drawer).getByRole("button", { name: "#701 をマージ" }));
    await waitFor(() =>
      expect(
        within(drawer).getByRole("button", { name: /反映を待っています/ }),
      ).toBeInTheDocument(),
    );

    /* マージ前の snapshot はまだ armed ではない。これを取り消しと読むと hold を
     * 取った直後に落ちるので、観測前の false では解除しない。 */
    streamSnapshot(snapshotWithPR({ autoMerge: false }));
    expect(within(drawer).getByRole("button", { name: /反映を待っています/ })).toHaveAttribute(
      "aria-disabled",
      "true",
    );

    /* poll が保留を観測し、そのあと消えたときだけ取り消し。checks 済みで直接
     * queue に入った場合は auto-merge ではなく queue entry が保留の印になる。 */
    streamSnapshot(snapshotWithPR({ queued: true }));
    streamSnapshot(snapshotWithPR({ queued: false }));
    await waitFor(() =>
      expect(within(drawer).getByRole("button", { name: "#701 をマージ" })).toBeInTheDocument(),
    );
  });
});

/* 行には別 repository の PR も載る(`Fixes owner/repo#N`)。番号だけで反映済みと
 * 判定すると、まだ反映されていないマージのボタンが押せる状態に戻る。 */
describe("同番号の別 repository PR", () => {
  it("よその merged #701 では反映待ちを解除しない", async () => {
    server.use(mergeHandler([]));
    const user = userEvent.setup();
    render(<App />);
    /* よその merged #701 が先に載っている行。番号だけで探すと、こちらの OPEN な
     * #701 ではなくそちらを掴んで「反映済み」と読んでしまう。 */
    streamSnapshot(
      makeSnapshot([
        makeSession("142", [
          makePane({
            issueNum: 101,
            displayName: "Fix login",
            slug: "fix-login",
            paneId: "%1",
            branchName: "fanout/fix-login",
            prs: [
              makePr({ number: 701, state: "MERGED", baseRepo: "other/repo" }),
              makePr({ number: 701, headRef: "fanout/fix-login" }),
            ],
          }),
        ]),
      ]),
    );

    const drawer = await openDrawer(user);
    await user.click(within(drawer).getByRole("button", { name: "#701 をマージ" }));
    await waitFor(() =>
      expect(
        within(drawer).getByRole("button", { name: /反映を待っています/ }),
      ).toBeInTheDocument(),
    );
    expect(within(drawer).getByRole("button", { name: /反映を待っています/ })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
  });
});

/* GitHub が既に保持しているマージ(UI や別の gh で武装/投入されたもの)は、
 * こちらから送り直しても早くならない。サーバも 409 で拒否する。 */
describe("GitHub が保留中の PR", () => {
  it("auto-merge 武装済みの PR はボタンから撃てない", async () => {
    const calls: MergeCall[] = [];
    server.use(mergeHandler(calls));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR({ autoMerge: true }));

    const drawer = await openDrawer(user);
    const blocked = within(drawer).getByRole("button", { name: /マージを保留中/ });
    expect(blocked).toHaveAttribute("aria-disabled", "true");
    await user.click(blocked);
    expect(calls).toHaveLength(0);
  });

  it("merge queue に入っている PR もボタンから撃てない", async () => {
    const calls: MergeCall[] = [];
    server.use(mergeHandler(calls));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR({ queued: true }));

    const drawer = await openDrawer(user);
    expect(within(drawer).getByRole("button", { name: /マージを保留中/ })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
    expect(calls).toHaveLength(0);
  });
});

describe("結果不明", () => {
  /* merge コマンドは通っているので、確認できないことを失敗として見せると再送を
   * 誘う。塞いだうえで状態確認を促す。 */
  it("結果を確認できなかったマージは再送させない", async () => {
    const calls: MergeCall[] = [];
    server.use(
      http.post(MERGE_PATH, async ({ request }) => {
        calls.push({
          url: new URL(request.url),
          body: (await request.json()) as Record<string, unknown>,
        });
        return HttpResponse.json({
          prNumber: 701,
          method: "squash",
          merged: false,
          queued: false,
          unknown: true,
          branchDeleted: false,
          refreshQueued: true,
        });
      }),
    );
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR());

    const drawer = await openDrawer(user);
    await user.click(within(drawer).getByRole("button", { name: "#701 をマージ" }));
    await screen.findByText(/結果を確認できませんでした/);

    const blocked = within(drawer).getByRole("button", { name: /反映を待っています/ });
    expect(blocked).toHaveAttribute("aria-disabled", "true");
    await user.click(blocked);
    expect(calls).toHaveLength(1);

    /* 時間では解けない。決着は GitHub の新しい状態だけ。 */
    streamSnapshot(snapshotWithPR());
    expect(within(drawer).getByRole("button", { name: /反映を待っています/ })).toBeInTheDocument();

    streamSnapshot(snapshotWithPR({ state: "MERGED" }));
    await screen.findByRole("button", { name: /既にマージ済み/ });
  });
});

describe("マージ後のブランチ削除", () => {
  /* GitHub 自身と同じで、マージが終わってから現れる別ボタン。マージ要求には
   * 一切乗らない。 */
  it("マージ済みの PR にだけ出て、専用の endpoint を叩く", async () => {
    const calls: { url: URL; body: Record<string, unknown> }[] = [];
    server.use(
      http.post(DELETE_BRANCH_PATH, async ({ request }) => {
        calls.push({
          url: new URL(request.url),
          body: (await request.json()) as Record<string, unknown>,
        });
        return HttpResponse.json({ prNumber: 701, branch: "fanout/fix-login", deleted: true });
      }),
    );
    const user = userEvent.setup();
    render(<App />);

    /* 未マージのうちは出ない。 */
    streamSnapshot(snapshotWithPR());
    const drawer = await openDrawer(user);
    expect(within(drawer).queryByRole("button", { name: "ブランチを削除" })).toBeNull();

    streamSnapshot(snapshotWithPR({ state: "MERGED" }));
    const remove = await within(drawer).findByRole("button", { name: "ブランチを削除" });
    await user.click(remove);

    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]?.body).toEqual({
      prNumber: 701,
      headSha: "0123456789abcdef0123456789abcdef01234567",
    });
    /* 消えた後はボタンごと引っ込む。 */
    await waitFor(() =>
      expect(within(drawer).queryByRole("button", { name: "ブランチを削除" })).toBeNull(),
    );
  });

  /* fork の head を base 側の同名 branch として消さないための表示側ガード。 */
  it("fork の head PR には出さない", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR({ state: "MERGED", headRepo: "stranger/fork" }));

    const drawer = await openDrawer(user);
    expect(within(drawer).queryByRole("button", { name: "ブランチを削除" })).toBeNull();
  });

  it("失敗したら理由を出す", async () => {
    server.use(
      http.post(DELETE_BRANCH_PATH, () =>
        HttpResponse.json(
          { error: "the branch was not deleted", detail: "branch is protected", code: "x" },
          { status: 409 },
        ),
      ),
    );
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR({ state: "MERGED" }));

    const drawer = await openDrawer(user);
    await user.click(await within(drawer).findByRole("button", { name: "ブランチを削除" }));
    await screen.findByText(/branch is protected/);
  });
});

describe("メニューのキー操作", () => {
  it("Escape はメニューだけ閉じ、ドロワーは開いたまま", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR());

    const drawer = await openDrawer(user);
    await user.click(within(drawer).getByRole("button", { name: /マージ方式/ }));
    await screen.findByRole("menu");

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu")).toBeNull();
    expect(screen.getByRole("complementary", { name: "ペイン詳細" })).toBeInTheDocument();
  });

  /* 回帰テスト: useEscapeToClose は document の capture 段に居るので、譲らないと
   * diff の中でメニューを開いた直後の Escape がオーバーレイごと閉じる。 */
  it("全画面 diff の中でも Escape はメニューだけ閉じ、diff は開いたまま", async () => {
    localStorage.setItem("fanout.diffView", "full");
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithPR());

    const overlay = await openDiff(user);
    await user.click(within(overlay).getByRole("button", { name: /マージ方式/ }));
    await screen.findByRole("menu");

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu")).toBeNull();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });
});
