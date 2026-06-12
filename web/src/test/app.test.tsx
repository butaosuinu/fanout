import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../components/App";
import type { Snapshot } from "../lib/types";
import { FakeEventSource, installFakeEventSource, removeEventSource } from "./fakeEventSource";
import { makePane, makeRollup, makeSession, makeSnapshot } from "./fixtures";
import { server } from "./server";

/* App 全体の統合テスト。モックはネットワーク境界のみ:
 * - /api/snapshot, /api/peek → MSW handler
 * - /api/stream(SSE)→ FakeEventSource(jsdom に EventSource が無いため)
 * 内部コンポーネント・hooks はモックしない。 */

beforeEach(() => {
  installFakeEventSource();
  localStorage.clear();
  document.documentElement.dataset.theme = "light";
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

function streamSnapshot(snap: Snapshot) {
  const es = FakeEventSource.latest();
  act(() => {
    es.emitOpen();
    es.emitSnapshot(snap);
  });
}

/* 2 ペイン(claude / codex)1 セッションの定番 snapshot */
function basicSnapshot() {
  return makeSnapshot(
    [
      makeSession("142", [
        makePane({
          issueNum: 101,
          displayName: "Fix login",
          slug: "fix-login",
          agent: "claude",
          paneId: "%1",
          branchName: "fanout/fix-login",
          diffSummary: "+10/-2",
          agentState: "running",
        }),
        makePane({
          issueNum: 102,
          displayName: "Add docs",
          slug: "add-docs",
          agent: "codex",
          paneId: "%2",
          branchName: "fanout/add-docs",
          diffSummary: "+1/-0",
          wave: 2,
        }),
      ]),
    ],
    { rollup: makeRollup({ total: 2, live: 2, running: 1 }) },
  );
}

function peekHandler(outputFor: (pane: string | null) => string) {
  return http.get("/api/peek", ({ request }) => {
    const pane = new URL(request.url).searchParams.get("pane");
    return HttpResponse.json({
      paneId: pane ?? "?",
      lines: 80,
      capturedAt: "2026-06-13T01:23:50Z",
      output: outputFor(pane),
    });
  });
}

describe("snapshot 描画", () => {
  it("初回 snapshot でセッション・行・HUD・接続バッジが描画される", () => {
    const { container } = render(<App />);
    expect(screen.getByText("awaiting telemetry")).toBeInTheDocument();

    streamSnapshot(basicSnapshot());

    expect(screen.getByText("streaming")).toBeInTheDocument();
    expect(screen.getByText("Fix login")).toBeInTheDocument();
    expect(screen.getByText("Add docs")).toBeInTheDocument();
    expect(screen.getByText(/telemetry @ \d{2}:\d{2}:\d{2}/)).toBeInTheDocument();

    // セッション見出し・issue セルは GitHub リンク
    expect(screen.getByRole("link", { name: "#142" })).toHaveAttribute(
      "href",
      "https://github.com/octo/fanout/issues/142",
    );
    expect(screen.getByRole("link", { name: "#101" })).toHaveAttribute(
      "href",
      "https://github.com/octo/fanout/issues/101",
    );

    // HUD(running カウンタ含む)
    expect(container.querySelector("#s-total")).toHaveTextContent("2");
    expect(container.querySelector("#s-live")).toHaveTextContent("2");
    expect(container.querySelector("#s-running")).toHaveTextContent("1");

    // agent 実行状態バッジ
    expect(screen.getByText("running", { selector: ".tag" })).toBeInTheDocument();
  });

  it("repo が owner/name 形式でなければリンク化しない", () => {
    render(<App />);
    streamSnapshot(makeSnapshot([makeSession("142", [makePane({ issueNum: 101 })])], { repo: "" }));
    expect(screen.queryByRole("link", { name: "#101" })).not.toBeInTheDocument();
    expect(screen.getByText("#101")).toBeInTheDocument(); // 素テキストには落ちる
  });

  it("degraded フラグで banner を表示し、正常時は隠す", () => {
    render(<App />);
    streamSnapshot(
      makeSnapshot([], { degraded: { tmux: true, github: true } }),
    );
    const banner = screen.getByRole("status");
    expect(banner).toHaveTextContent("GitHub データ取得が不安定");
    expect(banner).toHaveTextContent("tmux が利用できません");

    streamSnapshot(
      makeSnapshot([], { degraded: { tmux: false, github: false, reason: "state broken" } }),
    );
    expect(screen.getByRole("status")).toHaveTextContent("state 読み込みに失敗: state broken");

    streamSnapshot(makeSnapshot([]));
    expect(screen.queryByRole("status")).not.toBeInTheDocument(); // hidden
  });

  it("セッションが無ければ空状態を表示する", () => {
    render(<App />);
    streamSnapshot(makeSnapshot([]));
    expect(screen.getByText("アクティブなセッションがありません")).toBeInTheDocument();
  });

  it("rise 入場アニメは前 snapshot に無かった parent のみ", () => {
    const { container } = render(<App />);
    streamSnapshot(basicSnapshot());
    expect(container.querySelector('section[data-parent="142"]')).toHaveClass("rise");

    const es = FakeEventSource.latest();
    act(() => {
      es.emitSnapshot(
        makeSnapshot([
          makeSession("142", [makePane({ issueNum: 101 })]),
          makeSession("200", [makePane({ issueNum: 201, displayName: "New wave" })]),
        ]),
      );
    });
    expect(container.querySelector('section[data-parent="142"]')).not.toHaveClass("rise");
    expect(container.querySelector('section[data-parent="200"]')).toHaveClass("rise");
  });
});

describe("フィルタ", () => {
  it("自由語/構造化トークン入力で行・件数・チップが追従し、チップ削除で戻る", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(basicSnapshot());
    expect(screen.getByText("2 / 2")).toBeInTheDocument();

    await user.type(screen.getByRole("searchbox"), "agent:codex");
    expect(screen.queryByText("Fix login")).not.toBeInTheDocument();
    expect(screen.getByText("Add docs")).toBeInTheDocument();
    expect(screen.getByText("1 / 2")).toBeInTheDocument();

    await user.click(screen.getByRole("listitem", { name: "フィルタ agent:codex を外す" }));
    expect(screen.getByText("Fix login")).toBeInTheDocument();
    expect(screen.getByText("2 / 2")).toBeInTheDocument();
  });

  it("何も一致しなければフィルタ用の空状態を表示する", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(basicSnapshot());
    await user.type(screen.getByRole("searchbox"), "issue:999");
    expect(screen.getByText("フィルタに一致するペインがありません")).toBeInTheDocument();
  });

  it("select はトークンを書き込み、同キーは上書き、選択後はプレースホルダーに戻る", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(basicSnapshot());

    const stateSel = screen.getByRole("combobox", { name: "issue / tmux 状態で絞り込み" });
    await user.selectOptions(stateSel, "open");
    expect(screen.getByRole("listitem", { name: "フィルタ state:open を外す" })).toBeInTheDocument();
    expect(stateSel).toHaveValue("");

    await user.selectOptions(stateSel, "closed");
    expect(screen.queryByRole("listitem", { name: "フィルタ state:open を外す" })).not.toBeInTheDocument();
    expect(screen.getByRole("listitem", { name: "フィルタ state:closed を外す" })).toBeInTheDocument();
  });

  it("agent / wave の動的 select は snapshot 由来の選択肢を持つ", () => {
    render(<App />);
    streamSnapshot(basicSnapshot());
    const agentSel = screen.getByRole("combobox", { name: "agent で絞り込み" });
    expect(within(agentSel).getByRole("option", { name: "claude" })).toBeInTheDocument();
    expect(within(agentSel).getByRole("option", { name: "codex" })).toBeInTheDocument();
    const waveSel = screen.getByRole("combobox", { name: "wave で絞り込み" });
    expect(within(waveSel).getByRole("option", { name: "w2" })).toBeInTheDocument();
  });
});

describe("ソート", () => {
  it("列ヘッダクリックで順序と aria-sort が切り替わる", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(basicSnapshot());

    const bodyRows = () => within(screen.getByRole("table")).getAllByRole("row").slice(1);
    // 初期は issueNum 昇順
    expect(bodyRows()[0]).toHaveTextContent("Fix login");

    const diffTh = screen.getByRole("columnheader", { name: /^diff/ });
    await user.click(diffTh);
    expect(diffTh).toHaveAttribute("aria-sort", "ascending");
    expect(bodyRows()[0]).toHaveTextContent("Add docs"); // +1/-0 < +10/-2

    await user.click(diffTh);
    expect(diffTh).toHaveAttribute("aria-sort", "descending");
    expect(bodyRows()[0]).toHaveTextContent("Fix login");
  });
});

describe("drawer + peek", () => {
  it("行クリックで開き、リンククリックでは開かず、Escape / ✕ で閉じる", async () => {
    server.use(peekHandler(() => "boot ok"));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(basicSnapshot());

    // リンククリックは行選択にしない
    await user.click(screen.getByRole("link", { name: "#101" }));
    expect(screen.queryByRole("complementary", { name: "ペイン詳細" })).not.toBeInTheDocument();

    await user.click(screen.getByText("Fix login"));
    const drawer = await screen.findByRole("complementary", { name: "ペイン詳細" });
    expect(within(drawer).getByText("fanout/fix-login")).toBeInTheDocument();
    expect(await within(drawer).findByText("boot ok")).toBeInTheDocument();
    expect(within(drawer).getByText(/80 lines · 5s ごとに更新/)).toBeInTheDocument();

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("complementary", { name: "ペイン詳細" })).not.toBeInTheDocument();

    await user.click(screen.getByText("Fix login"));
    await screen.findByRole("complementary", { name: "ペイン詳細" });
    await user.click(screen.getByRole("button", { name: "詳細を閉じる" }));
    expect(screen.queryByRole("complementary", { name: "ペイン詳細" })).not.toBeInTheDocument();
  });

  it("ペイン切替で peek が新しいペインの出力に置き換わる", async () => {
    server.use(peekHandler((pane) => `output of ${pane}`));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(basicSnapshot());

    await user.click(screen.getByText("Fix login"));
    expect(await screen.findByText("output of %1")).toBeInTheDocument();

    await user.click(screen.getByText("Add docs"));
    expect(await screen.findByText("output of %2")).toBeInTheDocument();
    expect(screen.queryByText("output of %1")).not.toBeInTheDocument();
  });

  it("peek は 5 秒ごとに再取得する", async () => {
    let calls = 0;
    server.use(
      http.get("/api/peek", () => {
        calls++;
        return HttpResponse.json({
          paneId: "%1",
          lines: 80,
          capturedAt: "2026-06-13T01:23:50Z",
          output: `tick ${calls}`,
        });
      }),
    );
    vi.useFakeTimers();
    render(<App />);
    streamSnapshot(basicSnapshot());

    fireEvent.click(screen.getByText("Fix login"));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(screen.getByText("tick 1")).toBeInTheDocument();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(screen.getByText("tick 2")).toBeInTheDocument();
  });

  it("dead pane は capture せず終了メッセージを出す", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(
      makeSnapshot([
        makeSession("142", [
          makePane({ issueNum: 101, displayName: "Fix login", alive: false, tmuxState: "stale" }),
        ]),
      ]),
    );
    await user.click(screen.getByText("Fix login"));
    expect(
      await screen.findByText("(pane output unavailable — ペインは終了しています)"),
    ).toBeInTheDocument();
  });

  it("選択中ペインが snapshot から消えたら閉じて status に通知する", async () => {
    server.use(peekHandler(() => "boot ok"));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(basicSnapshot());

    await user.click(screen.getByText("Fix login"));
    await screen.findByRole("complementary", { name: "ペイン詳細" });

    act(() => {
      FakeEventSource.latest().emitSnapshot(
        makeSnapshot([makeSession("142", [makePane({ issueNum: 102, displayName: "Add docs" })])]),
      );
    });
    expect(screen.queryByRole("complementary", { name: "ペイン詳細" })).not.toBeInTheDocument();
    expect(
      screen.getByText(/142#101 は snapshot から消えたため詳細を閉じました/),
    ).toBeInTheDocument();
  });
});

describe("transport フォールバック", () => {
  it("SSE 断でポーリングに移行し、更新が継続する", async () => {
    const polled = makeSnapshot([
      makeSession("142", [makePane({ issueNum: 101, displayName: "From polling" })]),
    ]);
    server.use(http.get("/api/snapshot", () => HttpResponse.json(polled)));

    render(<App />);
    streamSnapshot(basicSnapshot());
    expect(screen.getByText("streaming")).toBeInTheDocument();

    act(() => {
      FakeEventSource.latest().emitError();
    });
    expect(await screen.findByText("polling")).toBeInTheDocument();
    expect(await screen.findByText("From polling")).toBeInTheDocument();
  });

  it("EventSource 未対応環境は最初からポーリングする", async () => {
    removeEventSource();
    const polled = makeSnapshot([
      makeSession("142", [makePane({ issueNum: 101, displayName: "From polling" })]),
    ]);
    server.use(http.get("/api/snapshot", () => HttpResponse.json(polled)));

    render(<App />);
    expect(await screen.findByText("polling")).toBeInTheDocument();
    expect(await screen.findByText("From polling")).toBeInTheDocument();
  });
});

describe("テーマ", () => {
  it("切替で data-theme / localStorage / aria-pressed が同期する", async () => {
    const user = userEvent.setup();
    render(<App />);
    const btn = screen.getByRole("button", { name: "ライト / ダーク切替" });
    expect(btn).toHaveAttribute("aria-pressed", "false");

    await user.click(btn);
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(localStorage.getItem("fanout.theme")).toBe("dark");
    expect(btn).toHaveAttribute("aria-pressed", "true");

    await user.click(btn);
    expect(document.documentElement.dataset.theme).toBe("light");
    expect(localStorage.getItem("fanout.theme")).toBe("light");
  });
});
