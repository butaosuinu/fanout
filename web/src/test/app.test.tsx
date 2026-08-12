import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../app/App";
import {
  FakeEventSource,
  installFakeEventSource,
  removeEventSource,
  streamSnapshot,
} from "./fakeEventSource";
import {
  makeDiffResponse,
  makePane,
  makeQueuedPane,
  makeRollup,
  makeSession,
  makeSnapshot,
} from "./fixtures";
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

/* テーブルのデータ行(ヘッダ行を除く) */
const bodyRows = () => within(screen.getByRole("table")).getAllByRole("row").slice(1);

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
    expect(screen.getByRole("columnheader", { name: "runtime" })).toBeInTheDocument();

    const legacyRow = screen.getByText("Fix login").closest("tr")!;
    expect(within(legacyRow).getByText("tmux", { exact: true })).toBeInTheDocument();
    expect(within(legacyRow).getByText("%1", { exact: true })).toBeInTheDocument();

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

  it("6 値契約の hook 状態を行と drawer に表示する", async () => {
    const user = userEvent.setup();
    server.use(peekHandler(() => "plan output"));
    render(<App />);
    streamSnapshot(
      makeSnapshot([
        makeSession("142", [
          makePane({ issueNum: 101, displayName: "One", agentState: "working" }),
          makePane({ issueNum: 102, displayName: "Two", agentState: "plan" }),
          makePane({ issueNum: 103, displayName: "Three", agentState: "blocked" }),
          makePane({ issueNum: 104, displayName: "Four", agentState: "idle" }),
        ]),
      ]),
    );
    for (const state of ["working", "plan", "blocked", "idle"]) {
      expect(screen.getByText(state, { selector: ".tag" })).toBeInTheDocument();
    }

    await user.click(screen.getByText("Two"));
    const drawer = await screen.findByRole("complementary", { name: "ペイン詳細" });
    expect(drawer.querySelector("#d-run")).toHaveTextContent("plan");
  });

  it("repo が owner/name 形式でなければリンク化しない", () => {
    render(<App />);
    streamSnapshot(makeSnapshot([makeSession("142", [makePane({ issueNum: 101 })])], { repo: "" }));
    expect(screen.queryByRole("link", { name: "#101" })).not.toBeInTheDocument();
    expect(screen.getByText("#101")).toBeInTheDocument(); // 素テキストには落ちる
  });

  it("plan 行は #0 ではなく taskId を表示する", () => {
    render(<App />);
    streamSnapshot(
      makeSnapshot([
        makeSession("plan:alpha", [
          makePane({ issueNum: 0, taskId: "plan-lint", displayName: "Lint plan" }),
        ]),
      ]),
    );
    expect(screen.getByText("plan-lint")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "plan-lint" })).not.toBeInTheDocument();
    expect(screen.queryByText("#0")).not.toBeInTheDocument();
  });

  it("Prompt Session は branch に紐づく PR リンクと CI を表示する", async () => {
    const user = userEvent.setup();
    server.use(peekHandler(() => "manual output"));
    render(<App />);
    streamSnapshot(
      makeSnapshot([
        makeSession("@manual", [
          makePane({
            issueNum: -1,
            sourceKey: "manual-prompt",
            displayName: "Prompt session",
            slug: "manual-1-prompt-session-pane",
            agent: "codex",
            paneId: "%9",
            branchName: "fanout/manual-1-prompt-session-pane",
            issueState: "UNKNOWN",
            prs: [
              {
                number: 701,
                state: "OPEN",
                mergedAt: null,
                ci: "pass",
                reviewDecision: "APPROVED",
                mergeable: "CONFLICTING",
                comments: 12,
              },
            ],
            ciStatus: "pass",
          }),
        ]),
      ]),
    );

    const row = screen.getByText("Prompt session").closest("tr")!;
    expect(within(row).getByText("#-1")).toBeInTheDocument();
    expect(within(row).queryByRole("link", { name: "#-1" })).not.toBeInTheDocument();
    expect(row.querySelector('a[href$="/issues/-1"]')).toBeNull();
    // ラベルは DisplayState 語彙(TUI と同じ) — 生の OPEN ではなく approved。
    // conflict / コメント件数はリンクの外の兄弟なので、リンク名には混ざらない。
    expect(within(row).getByRole("link", { name: "#701 approved" })).toHaveAttribute(
      "href",
      "https://github.com/octo/fanout/pull/701",
    );
    expect(within(row).getByText("conflict", { selector: ".tag" })).toBeInTheDocument();
    expect(within(row).getByText("💬 12", { selector: ".tag" })).toBeInTheDocument();
    expect(within(row).getByText("pass", { selector: ".tag" })).toBeInTheDocument();

    await user.click(within(row).getByText("Prompt session"));
    const drawer = await screen.findByRole("complementary", { name: "ペイン詳細" });
    expect(within(drawer).getByRole("link", { name: "#701 approved" })).toHaveAttribute(
      "href",
      "https://github.com/octo/fanout/pull/701",
    );
    expect(within(drawer).getByText("ci pass", { selector: ".tag" })).toBeInTheDocument();
    expect(within(drawer).getByText("conflict", { selector: ".tag" })).toBeInTheDocument();
    expect(within(drawer).getByText("💬 12", { selector: ".tag" })).toBeInTheDocument();
    expect(within(drawer).getByText("approved", { selector: ".tag" })).toBeInTheDocument();
  });

  it("mergeable / comments が無い PR は conflict タグもコメント件数も出さない", async () => {
    const user = userEvent.setup();
    server.use(peekHandler(() => "manual output"));
    render(<App />);
    streamSnapshot(
      makeSnapshot([
        makeSession("@manual", [
          makePane({
            issueNum: -1,
            sourceKey: "manual-prompt",
            displayName: "Clean session",
            slug: "manual-2-clean-session-pane",
            branchName: "fanout/manual-2-clean-session-pane",
            issueState: "UNKNOWN",
            // MERGEABLE は「衝突なし」、comments 0 は omitempty で欠落した状態。
            prs: [
              { number: 702, state: "OPEN", mergedAt: null, mergeable: "MERGEABLE", comments: 0 },
            ],
          }),
        ]),
      ]),
    );

    const row = screen.getByText("Clean session").closest("tr")!;
    expect(within(row).getByRole("link", { name: "#702 open" })).toBeInTheDocument();
    expect(within(row).queryByText("conflict")).not.toBeInTheDocument();
    expect(within(row).queryByText(/💬/)).not.toBeInTheDocument();

    await user.click(within(row).getByText("Clean session"));
    const drawer = await screen.findByRole("complementary", { name: "ペイン詳細" });
    expect(within(drawer).queryByText("conflict")).not.toBeInTheDocument();
    expect(within(drawer).queryByText(/💬/)).not.toBeInTheDocument();
  });

  it("degraded フラグで banner を表示し、正常時は隠す", () => {
    render(<App />);
    streamSnapshot(makeSnapshot([], { degraded: { tmux: true, github: true } }));
    const banner = screen.getByRole("status");
    expect(banner).toHaveTextContent("GitHub データ取得が不安定");
    expect(banner).toHaveTextContent("runtime が利用できません");

    streamSnapshot(makeSnapshot([], { degraded: { runtime: true, tmux: true, github: false } }));
    expect(screen.getByRole("status")).toHaveTextContent("runtime の一部が利用できません");
    expect(screen.getByRole("status")).not.toHaveTextContent("runtime が利用できません");

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

  it("herdr row の live / stale / unknown / unsupported を runtime 状態として表示する", () => {
    render(<App />);
    streamSnapshot(
      makeSnapshot([
        makeSession("427", [
          makePane({
            issueNum: 101,
            displayName: "Herdr live",
            backend: "herdr",
            paneId: "w1:p1",
            alive: true,
          }),
          makePane({
            issueNum: 102,
            displayName: "Herdr stale",
            backend: "herdr",
            paneId: "w1:p2",
            alive: false,
            tmuxState: "stale",
          }),
          makePane({
            issueNum: 103,
            displayName: "Herdr unknown",
            backend: "herdr",
            paneId: "w1:p3",
            alive: false,
            tmuxState: "unknown",
          }),
          makePane({
            issueNum: 104,
            displayName: "Herdr unsupported",
            backend: "herdr",
            paneId: "w1:p4",
            alive: false,
            tmuxState: "unknown",
            runtimeState: "unsupported",
          }),
        ]),
      ]),
    );

    for (const [name, paneId, state] of [
      ["Herdr live", "w1:p1", "live"],
      ["Herdr stale", "w1:p2", "stale"],
      ["Herdr unknown", "w1:p3", "unknown"],
      ["Herdr unsupported", "w1:p4", "unsupported"],
    ] as const) {
      const row = screen.getByText(name).closest("tr");
      expect(row).not.toBeNull();
      expect(within(row!).getByText("herdr", { exact: true })).toBeInTheDocument();
      expect(within(row!).getByText(paneId, { exact: true })).toBeInTheDocument();
      expect(within(row!).getByText(state, { exact: true })).toBeInTheDocument();
    }
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

  it("backend dropdown で legacy tmux と herdr row を絞り込む", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(
      makeSnapshot([
        makeSession("428", [
          makePane({ issueNum: 101, displayName: "Legacy tmux", backend: "" }),
          makePane({
            issueNum: 102,
            displayName: "Herdr pane",
            backend: "herdr",
            paneId: "w1:p1",
          }),
        ]),
      ]),
    );

    await user.click(screen.getByRole("button", { name: "runtime backend で絞り込み" }));
    await user.click(screen.getByRole("option", { name: "herdr" }));
    expect(screen.getByText("Herdr pane")).toBeInTheDocument();
    expect(screen.queryByText("Legacy tmux")).not.toBeInTheDocument();
    expect(screen.getByRole("searchbox")).toHaveValue("backend:herdr");

    await user.click(screen.getByRole("button", { name: "runtime backend で絞り込み" }));
    await user.click(screen.getByRole("option", { name: "tmux" }));
    expect(screen.getByText("Legacy tmux")).toBeInTheDocument();
    expect(screen.queryByText("Herdr pane")).not.toBeInTheDocument();
    expect(screen.getByRole("searchbox")).toHaveValue("backend:tmux");
  });

  it("trigger クリックで popover が開き、option 選択でトークン書込・閉じて trigger に復帰、同キーは上書き", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(basicSnapshot());

    const trigger = screen.getByRole("button", { name: "issue / runtime 状態で絞り込み" });
    expect(trigger).toHaveAttribute("aria-haspopup", "listbox");
    expect(trigger).toHaveAttribute("aria-expanded", "false");

    await user.click(trigger);
    expect(trigger).toHaveAttribute("aria-expanded", "true");
    const listbox = screen.getByRole("listbox", { name: "issue / runtime 状態で絞り込み" });
    await user.click(within(listbox).getByRole("option", { name: "open" }));

    expect(
      screen.getByRole("listitem", { name: "フィルタ state:open を外す" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument(); // 選択で閉じる
    expect(trigger).toHaveFocus();

    await user.click(trigger);
    await user.click(screen.getByRole("option", { name: "closed" }));
    expect(
      screen.queryByRole("listitem", { name: "フィルタ state:open を外す" }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByRole("listitem", { name: "フィルタ state:closed を外す" }),
    ).toBeInTheDocument();
  });

  it("run ドロップダウンは 6 値契約の順で option を並べる", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(basicSnapshot());

    await user.click(screen.getByRole("button", { name: "agent 実行状態で絞り込み" }));
    const opts = within(
      screen.getByRole("listbox", { name: "agent 実行状態で絞り込み" }),
    ).getAllByRole("option");
    expect(opts.map((o) => o.textContent)).toEqual([
      "running",
      "working",
      "plan",
      "blocked",
      "idle",
      "done",
    ]);
  });

  it("アクティブ option は aria-selected で示し、再クリックでトグルオフする", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(basicSnapshot());

    const trigger = screen.getByRole("button", { name: "issue / runtime 状態で絞り込み" });
    await user.click(trigger);
    await user.click(screen.getByRole("option", { name: "open" }));
    expect(trigger).toHaveClass("on"); // 適用中スタイル

    await user.click(trigger);
    const opt = screen.getByRole("option", { name: "open" });
    expect(opt).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("option", { name: "closed" })).toHaveAttribute(
      "aria-selected",
      "false",
    );

    await user.click(opt);
    expect(
      screen.queryByRole("listitem", { name: "フィルタ state:open を外す" }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("searchbox")).toHaveValue("");
    expect(trigger).not.toHaveClass("on");
  });

  it("popover 外の pointerdown で閉じる(トークンは書かない)", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(basicSnapshot());

    await user.click(screen.getByRole("button", { name: "issue / runtime 状態で絞り込み" }));
    expect(screen.getByRole("listbox")).toBeInTheDocument();

    await user.click(screen.getByText("Fix login"));
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(screen.getByRole("searchbox")).toHaveValue("");
  });

  it("Esc は popover だけ閉じて trigger に復帰し、Drawer の document keydown へ漏らさない", async () => {
    server.use(peekHandler(() => "boot ok"));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(basicSnapshot());

    await user.click(screen.getByText("Fix login"));
    await screen.findByRole("complementary", { name: "ペイン詳細" });

    const trigger = screen.getByRole("button", { name: "issue / runtime 状態で絞り込み" });
    await user.click(trigger);
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
    expect(screen.getByRole("complementary", { name: "ペイン詳細" })).toBeInTheDocument(); // drawer 残存

    await user.keyboard("{Escape}"); // popover が閉じた後の Esc は drawer に届く
    expect(screen.queryByRole("complementary", { name: "ペイン詳細" })).not.toBeInTheDocument();
  });

  it("キーボード操作: ArrowDown/Up の roving tabindex で巡回し、Enter で選択する", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(basicSnapshot());

    const trigger = screen.getByRole("button", { name: "issue / runtime 状態で絞り込み" });
    act(() => trigger.focus());
    await user.keyboard("{ArrowDown}"); // trigger 上の ↓ で開く
    const opts = within(screen.getByRole("listbox")).getAllByRole("option");
    expect(opts.map((o) => o.textContent)).toEqual([
      "open",
      "closed",
      "live",
      "stale",
      "queued",
      "deferred",
    ]);
    expect(opts[0]).toHaveFocus();
    expect(opts[0]).toHaveAttribute("tabindex", "0");
    expect(opts[1]).toHaveAttribute("tabindex", "-1");

    await user.keyboard("{ArrowDown}");
    expect(opts[1]).toHaveFocus();
    expect(opts[1]).toHaveAttribute("tabindex", "0");
    expect(opts[0]).toHaveAttribute("tabindex", "-1");

    await user.keyboard("{ArrowUp}{ArrowUp}"); // 先頭から上へはラップして末尾(deferred)
    expect(opts[opts.length - 1]).toHaveFocus();

    await user.keyboard("{Enter}");
    expect(
      screen.getByRole("listitem", { name: "フィルタ state:deferred を外す" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });

  it("agent / wave は snapshot 由来の選択肢を検索 input で絞り込め、Enter で先頭を選択する", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(basicSnapshot());

    await user.click(screen.getByRole("button", { name: "agent で絞り込み" }));
    const listbox = screen.getByRole("listbox", { name: "agent で絞り込み" });
    expect(within(listbox).getByRole("option", { name: "claude" })).toBeInTheDocument();
    expect(within(listbox).getByRole("option", { name: "codex" })).toBeInTheDocument();

    const search = screen.getByRole("textbox", { name: "agent の選択肢を検索" });
    expect(search).toHaveFocus(); // searchable は開いた直後に検索へフォーカス
    await user.keyboard("cod");
    expect(within(listbox).queryByRole("option", { name: "claude" })).not.toBeInTheDocument();
    await user.keyboard("{Enter}");
    expect(
      screen.getByRole("listitem", { name: "フィルタ agent:codex を外す" }),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "wave で絞り込み" }));
    await user.click(screen.getByRole("option", { name: "w2" }));
    expect(screen.getByRole("listitem", { name: "フィルタ wave:2 を外す" })).toBeInTheDocument();
  });

  it("開いている popover の選択肢は freeze され、閉じて開き直すと新 option が反映される", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(basicSnapshot());

    await user.click(screen.getByRole("button", { name: "agent で絞り込み" }));
    const listbox = screen.getByRole("listbox", { name: "agent で絞り込み" });
    expect(within(listbox).getAllByRole("option")).toHaveLength(2);

    // 開いたまま snapshot 更新(gemini の pane 追加)— 閉じず・選択肢も不変
    act(() => {
      FakeEventSource.latest().emitSnapshot(
        makeSnapshot([
          makeSession("142", [
            makePane({ issueNum: 101, agent: "claude" }),
            makePane({ issueNum: 102, agent: "codex" }),
            makePane({ issueNum: 103, agent: "gemini" }),
          ]),
        ]),
      );
    });
    expect(within(listbox).getAllByRole("option")).toHaveLength(2);
    expect(within(listbox).queryByRole("option", { name: "gemini" })).not.toBeInTheDocument();

    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("button", { name: "agent で絞り込み" }));
    expect(screen.getByRole("option", { name: "gemini" })).toBeInTheDocument();
  });

  it("非 searchable は先頭文字 typeahead で option へジャンプする", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(basicSnapshot());

    await user.click(screen.getByRole("button", { name: "issue / runtime 状態で絞り込み" }));
    await user.keyboard("c"); // open → closed へジャンプ
    expect(screen.getByRole("option", { name: "closed" })).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(
      screen.getByRole("listitem", { name: "フィルタ state:closed を外す" }),
    ).toBeInTheDocument();
  });

  it("label 表記の別名トークン(wave:w2)もアクティブ表示・トグルオフできる", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(basicSnapshot());

    await user.type(screen.getByRole("searchbox"), "wave:w2");
    const trigger = screen.getByRole("button", { name: "wave で絞り込み" });
    expect(trigger).toHaveClass("on");
    await user.click(trigger);
    const opt = screen.getByRole("option", { name: "w2" });
    expect(opt).toHaveAttribute("aria-selected", "true");
    await user.click(opt);
    expect(screen.getByRole("searchbox")).toHaveValue("");
    expect(trigger).not.toHaveClass("on");
  });

  it("トグルオフは手打ちの同キー重複トークンも全て外す", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(basicSnapshot());

    await user.type(screen.getByRole("searchbox"), "state:open STATE:CLOSED");
    const trigger = screen.getByRole("button", { name: "issue / runtime 状態で絞り込み" });
    await user.click(trigger);
    // 最初の同キートークン(open)がアクティブ表示。クリックで同キーを一掃
    await user.click(screen.getByRole("option", { name: "open" }));
    expect(screen.getByRole("searchbox")).toHaveValue("");
    expect(trigger).not.toHaveClass("on");
  });
});

describe("ソート", () => {
  it("列ヘッダクリックで順序と aria-sort が切り替わる", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(basicSnapshot());

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
    expect(drawer.querySelector("#d-backend")).toHaveTextContent("tmux");
    expect(drawer.querySelector("#d-pane")).toHaveTextContent("%1");
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

  it.each([
    ["owned", true, 1],
    ["derived.canPeek=false", false, 0],
    ["derived.canPeek missing", undefined, 0],
  ])("live herdr pane は %s の peek gate に従う", async (_gate, canPeek, wantPeekCalls) => {
    let peekCalls = 0;
    let planCalls = 0;
    server.use(
      http.get("/api/peek", () => {
        peekCalls++;
        return HttpResponse.json({ paneId: "w1:p5", lines: 80, capturedAt: "", output: "" });
      }),
      http.get("/api/plan", () => {
        planCalls++;
        return HttpResponse.json({ paneId: "w1:p5", capturedAt: "", found: false, plan: "" });
      }),
    );
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(
      makeSnapshot([
        makeSession("142", [
          makePane({
            issueNum: 105,
            displayName: "Herdr child",
            agent: "codex",
            backend: "herdr",
            paneId: "w1:p5",
            alive: true,
            planMode: true,
            ...(canPeek === undefined ? {} : { derived: { canPeek } }),
          }),
        ]),
      ]),
    );

    await user.click(screen.getByText("Herdr child"));
    const drawer = await screen.findByRole("complementary", { name: "ペイン詳細" });
    expect(drawer.querySelector("#d-backend")).toHaveTextContent("herdr");
    expect(drawer.querySelector("#d-pane")).toHaveTextContent("w1:p5");
    expect(within(drawer).getByLabelText("plan disabled")).toHaveAttribute("aria-disabled", "true");
    if (wantPeekCalls > 0) {
      expect(
        within(drawer).getByText("herdr backend は plan capture に対応していません。"),
      ).toBeInTheDocument();
      await waitFor(() => expect(peekCalls).toBe(wantPeekCalls));
      expect(within(drawer).queryByLabelText("peek disabled")).not.toBeInTheDocument();
    } else {
      expect(within(drawer).getByLabelText("peek disabled")).toHaveAttribute(
        "aria-disabled",
        "true",
      );
      expect(peekCalls).toBe(0);
    }
    expect(planCalls).toBe(0);
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

describe("plan(Codex Plan Mode)", () => {
  /* %1 = planMode(codex)、%2 = 通常 codex、%3/%4 = 他 agent の planMode。 */
  function planModeSnapshot() {
    return makeSnapshot([
      makeSession("142", [
        makePane({
          issueNum: 101,
          displayName: "Fix login",
          agent: "codex",
          paneId: "%1",
          planMode: true,
        }),
        makePane({ issueNum: 102, displayName: "Add docs", agent: "codex", paneId: "%2" }),
        makePane({
          issueNum: 103,
          displayName: "Plan with Claude",
          agent: "claude",
          paneId: "%3",
          planMode: true,
        }),
        makePane({
          issueNum: 104,
          displayName: "Plan with OpenCode",
          agent: "opencode",
          paneId: "%4",
          planMode: true,
        }),
      ]),
    ]);
  }

  function planHandler(body: () => { found: boolean; plan: string }) {
    return http.get("/api/plan", ({ request }) => {
      const pane = new URL(request.url).searchParams.get("pane");
      return HttpResponse.json({
        paneId: pane ?? "?",
        capturedAt: "2026-06-13T01:23:55Z",
        ...body(),
      });
    });
  }

  it("codex の planMode ペインだけ Plan セクションを表示し、plan 本文を <pre> テキストで描画する", async () => {
    let planCalls = 0;
    server.use(
      peekHandler(() => "boot ok"),
      planHandler(() => {
        planCalls++;
        return { found: true, plan: "## Plan\n1. <b>not html</b>" };
      }),
    );
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(planModeSnapshot());

    await user.click(screen.getByText("Fix login"));
    const drawer = await screen.findByRole("complementary", { name: "ペイン詳細" });
    expect(within(drawer).getByText("plan — 提案中のプラン")).toBeInTheDocument();
    // capture 出力は敵性入力 — タグ混じりでもテキストノードとしてそのまま出る
    const pre = await within(drawer).findByText(/1\. <b>not html<\/b>/);
    expect(pre.tagName).toBe("PRE");
    expect(pre.querySelector("b")).toBeNull();
    expect(drawer.querySelector("#plan-meta")).toHaveTextContent(/captured \d{2}:\d{2}:\d{2}/);
    expect(planCalls).toBe(1);

    // 非 plan codex と、plan mode の claude / opencode は対象外。
    for (const name of ["Add docs", "Plan with Claude", "Plan with OpenCode"]) {
      await user.click(screen.getByText(name));
      await screen.findByRole("complementary", { name: "ペイン詳細" });
      expect(screen.queryByText("plan — 提案中のプラン")).not.toBeInTheDocument();
      expect(planCalls).toBe(1);
    }
  });

  it("found:false は未検出の説明文言を表示する", async () => {
    server.use(
      peekHandler(() => "boot ok"),
      planHandler(() => ({ found: false, plan: "" })),
    );
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(planModeSnapshot());

    await user.click(screen.getByText("Fix login"));
    expect(await screen.findByText(/plan が見つかりません/)).toBeInTheDocument();
  });

  it("再取得ボタンで /api/plan を再フェッチする", async () => {
    let calls = 0;
    server.use(
      peekHandler(() => "boot ok"),
      planHandler(() => {
        calls++;
        return { found: true, plan: `plan v${calls}` };
      }),
    );
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(planModeSnapshot());

    await user.click(screen.getByText("Fix login"));
    expect(await screen.findByText("plan v1")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "再取得" }));
    expect(await screen.findByText("plan v2")).toBeInTheDocument();
    expect(screen.queryByText("plan v1")).not.toBeInTheDocument();
  });

  it("plan はポーリングしない(時間経過では再フェッチされない)", async () => {
    let planCalls = 0;
    server.use(
      peekHandler(() => "boot ok"),
      planHandler(() => {
        planCalls++;
        return { found: true, plan: "stable plan" };
      }),
    );
    vi.useFakeTimers();
    render(<App />);
    streamSnapshot(planModeSnapshot());

    fireEvent.click(screen.getByText("Fix login"));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(screen.getByText("stable plan")).toBeInTheDocument();
    expect(planCalls).toBe(1);

    // peek の 5s ポーリング周期を 3 回分進めても plan は 1 回のまま
    await act(async () => {
      await vi.advanceTimersByTimeAsync(15000);
    });
    expect(planCalls).toBe(1);
  });
});

describe("drawer リサイズ", () => {
  beforeEach(() => {
    // hook はビューポート上限(innerWidth - 360)でも clamp する。jsdom 既定の
    // 1024px だと上限 921px になりドラッグ値が読みにくいので広い画面に固定。
    Object.defineProperty(window, "innerWidth", {
      value: 2000,
      configurable: true,
      writable: true,
    });
  });
  const openDrawer = async (user: ReturnType<typeof userEvent.setup>, name: string) => {
    await user.click(screen.getByText(name));
    return await screen.findByRole("complementary", { name: "ペイン詳細" });
  };
  const drawerWidthVar = (drawer: HTMLElement) => drawer.style.getPropertyValue("--drawer-w");

  it("localStorage の保存値が --drawer-w と grip の aria-valuenow に反映される", async () => {
    server.use(peekHandler(() => "boot ok"));
    localStorage.setItem("fanout.drawerWidth", "560");
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(basicSnapshot());

    const drawer = await openDrawer(user, "Fix login");
    expect(drawerWidthVar(drawer)).toBe("560px");
    const grip = within(drawer).getByRole("separator", { name: "詳細パネルの幅を変更" });
    expect(grip).toHaveAttribute("aria-orientation", "vertical");
    expect(grip).toHaveAttribute("aria-valuenow", "560");
  });

  it("グリップのドラッグで広がり、pane を切り替えても幅を維持する", async () => {
    server.use(peekHandler(() => "boot ok"));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(basicSnapshot());

    const drawer = await openDrawer(user, "Fix login");
    expect(drawerWidthVar(drawer)).toBe("840px"); // 初期幅 = 従来 420px の 2 倍

    const grip = within(drawer).getByRole("separator", { name: "詳細パネルの幅を変更" });
    fireEvent.pointerDown(grip, { button: 0, pointerId: 1, clientX: 800, isPrimary: true });
    expect(document.documentElement).toHaveClass("drawer-resizing");
    fireEvent.pointerMove(grip, { pointerId: 1, clientX: 680, buttons: 1 });
    expect(drawerWidthVar(drawer)).toBe("960px"); // 右アンカー: 左ドラッグで拡大
    fireEvent.pointerUp(grip, { pointerId: 1, clientX: 680 });
    expect(document.documentElement).not.toHaveClass("drawer-resizing");
    expect(localStorage.getItem("fanout.drawerWidth")).toBe("960");

    // Drawer は pane ごとに remount されるが、保存値からの同期初期化で幅は維持
    const drawer2 = await openDrawer(user, "Add docs");
    expect(within(drawer2).getByText("fanout/add-docs")).toBeInTheDocument();
    expect(drawerWidthVar(drawer2)).toBe("960px");
    expect(
      within(drawer2).getByRole("separator", { name: "詳細パネルの幅を変更" }),
    ).toHaveAttribute("aria-valuenow", "960");
  });
});

describe("未開始(queued)子 issue", () => {
  /* 起動済み 1 + 未開始 2(queued / deferred)の snapshot。Go 側 Build の
   * synthetic 行と同じワイヤ形。 */
  function queuedSnapshot() {
    return makeSnapshot(
      [
        makeSession(
          "142",
          [
            makePane({ issueNum: 101, displayName: "Fix login", agent: "claude", paneId: "%1" }),
            makeQueuedPane({ issueNum: 103, displayName: "Queued child" }),
            makeQueuedPane({
              issueNum: 104,
              displayName: "Deferred child",
              tmuxState: "deferred",
              blocked: true,
              blockers: [{ num: 103, state: "OPEN" }],
            }),
          ],
          { notStarted: 2 },
        ),
      ],
      { rollup: makeRollup({ total: 3, live: 1, notStarted: 2 }) },
    );
  }

  it("ghost 行として tmux 列に queued / deferred を描画し、HUD に未開始数が出る", () => {
    const { container } = render(<App />);
    streamSnapshot(queuedSnapshot());

    const queuedRow = screen.getByText("Queued child").closest("tr")!;
    expect(queuedRow).toHaveClass("ghost");
    expect(queuedRow).toHaveTextContent("queued");
    const deferredRow = screen.getByText("Deferred child").closest("tr")!;
    expect(deferredRow).toHaveClass("ghost");
    expect(deferredRow).toHaveTextContent("deferred");
    // 起動済みの行は ghost にならない
    expect(screen.getByText("Fix login").closest("tr")).not.toHaveClass("ghost");
    // HUD の queued カウンタ(rollup.notStarted)
    expect(container.querySelector("#s-queued")).toHaveTextContent("2");
  });

  it("state:queued フィルタで未開始行だけに絞れる", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(queuedSnapshot());

    // GitHub PR 風 popover(#249): trigger を開いて queued option を選ぶ
    await user.click(screen.getByRole("button", { name: "issue / runtime 状態で絞り込み" }));
    await user.click(screen.getByRole("option", { name: "queued" }));
    expect(screen.getByText("Queued child")).toBeInTheDocument();
    expect(screen.queryByText("Fix login")).not.toBeInTheDocument();
    expect(screen.queryByText("Deferred child")).not.toBeInTheDocument();
    expect(screen.getByText("1 / 3")).toBeInTheDocument();
  });

  it("未開始行の drawer は縮約表示になり /api/peek を呼ばない", async () => {
    let peekCalls = 0;
    server.use(
      http.get("/api/peek", () => {
        peekCalls++;
        return HttpResponse.json({ paneId: "?", lines: 80, capturedAt: "", output: "" });
      }),
    );
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(queuedSnapshot());

    await user.click(screen.getByText("Deferred child"));
    const drawer = await screen.findByRole("complementary", { name: "ペイン詳細" });
    expect(
      within(drawer).getByText("未開始 — open な blocker があるため待機中です。"),
    ).toBeInTheDocument();
    // wave/blockers と PR は出すが、pane / worktree / prompt / peek は出さない
    expect(within(drawer).getByText("wave / blockers")).toBeInTheDocument();
    expect(within(drawer).getByText("pull requests")).toBeInTheDocument();
    expect(within(drawer).getByRole("link", { name: "#103" })).toBeInTheDocument();
    expect(within(drawer).queryByText("worktree")).not.toBeInTheDocument();
    expect(within(drawer).queryByText("prompt")).not.toBeInTheDocument();
    expect(within(drawer).queryByText(/peek/)).not.toBeInTheDocument();
    expect(peekCalls).toBe(0);
  });

  it("pane 起動で同じ行キーのまま実 row に置き換わる", async () => {
    server.use(peekHandler(() => "booted"));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(queuedSnapshot());

    // 未開始行を選択 → 次の snapshot で同 issue の pane が起動
    await user.click(screen.getByText("Queued child"));
    await screen.findByRole("complementary", { name: "ペイン詳細" });

    act(() => {
      FakeEventSource.latest().emitSnapshot(
        makeSnapshot([
          makeSession("142", [
            makePane({ issueNum: 101, displayName: "Fix login" }),
            makePane({ issueNum: 103, displayName: "Queued child", paneId: "%3", alive: true }),
          ]),
        ]),
      );
    });
    // rowKey(142#103)が安定しているので drawer は開いたまま実 row 表示に切り替わる
    const drawer = screen.getByRole("complementary", { name: "ペイン詳細" });
    expect(within(drawer).getByText("worktree")).toBeInTheDocument();
    expect(await within(drawer).findByText("booted")).toBeInTheDocument();
    // 行側("Queued child" は drawer ヘッダにも出るので table 内に限定)
    const row = within(screen.getByRole("table")).getByText("Queued child").closest("tr");
    expect(row).not.toHaveClass("ghost");
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

describe("session リストの diff 列", () => {
  it("差分のある行はクリックで diff ビュアーへ直行する(Drawer は開かない)", async () => {
    server.use(http.get("/api/diff", () => HttpResponse.json(makeDiffResponse())));
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(
      makeSnapshot([
        makeSession("142", [
          makePane({ issueNum: 101, displayName: "Fix thing", diffSummary: "+10/-2" }),
        ]),
      ]),
    );

    await user.click(await screen.findByRole("button", { name: /^変更を表示 .* \+10\/-2$/ }));

    /* 導線から開いた既定はコンパクト(モーダルではないので role=complementary)。
       Drawer は開かない。 */
    const overlay = await screen.findByRole("complementary", { name: "worktree diff" });
    expect(overlay).toHaveAttribute("data-mode", "compact");
    expect(screen.queryByRole("complementary", { name: "ペイン詳細" })).not.toBeInTheDocument();
  });

  it("Drawer 上部バーは Session 名 → 差分行数 → 変更を表示 の順に並べる", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(
      makeSnapshot([
        makeSession("142", [
          makePane({ issueNum: 101, displayName: "Fix thing", diffSummary: "+10/-2" }),
        ]),
      ]),
    );

    await user.click(await screen.findByText("Fix thing"));
    const head = (await screen.findByRole("complementary", { name: "ペイン詳細" })).querySelector(
      ".drawer-head",
    ) as HTMLElement;
    expect(within(head).getByText("+10")).toBeInTheDocument();
    expect(within(head).getByText("-2")).toBeInTheDocument();
    expect([...head.children].map((c) => c.tagName + (c.id ? `#${c.id}` : ""))).toEqual([
      "H3",
      "SPAN",
      "BUTTON#d-diff-open",
      "BUTTON#drawer-close",
    ]);
  });

  /* 同じ統計の行が複数あると「変更を表示 +N/-M」だけでは支援技術から区別できない。
   * 対象(paneLabel)を名前に含める。 */
  it("diff 導線の名前で対象ペインを区別できる", async () => {
    render(<App />);
    streamSnapshot(
      makeSnapshot([
        makeSession("142", [
          makePane({
            issueNum: 101,
            displayName: "One",
            branchName: "fanout/one",
            diffSummary: "+10/-2",
          }),
          makePane({
            issueNum: 102,
            displayName: "Two",
            branchName: "fanout/two",
            diffSummary: "+10/-2",
          }),
        ]),
      ]),
    );

    expect(
      await screen.findByRole("button", { name: "変更を表示 #101 One fanout/one +10/-2" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "変更を表示 #102 Two fanout/two +10/-2" }),
    ).toBeInTheDocument();
  });

  /* paneLabel と表示名だけでは足りない。同じ parent の下で別の worktree が同じ
   * task を持つと(plan spec の branch 上書きなど)、統計まで同じなら 2 つの
   * ボタンの名前が完全に一致し、支援技術のボタン一覧から選び分けられない。 */
  it("同じ task を別 worktree が持つ行も名前で区別できる", async () => {
    render(<App />);
    streamSnapshot(
      makeSnapshot([
        makeSession("plan:alpha", [
          makePane({
            issueNum: 0,
            taskId: "lint",
            sourceKey: "plan:alpha/lint#a",
            displayName: "Lint",
            slug: "lint-a",
            branchName: "fanout/lint-a",
            diffSummary: "+10/-2",
          }),
          makePane({
            issueNum: 0,
            taskId: "lint",
            sourceKey: "plan:alpha/lint#b",
            displayName: "Lint",
            slug: "lint-b",
            branchName: "fanout/lint-b",
            diffSummary: "+10/-2",
          }),
        ]),
      ]),
    );

    expect(
      await screen.findByRole("button", { name: "変更を表示 lint Lint fanout/lint-a +10/-2" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "変更を表示 lint Lint fanout/lint-b +10/-2" }),
    ).toBeInTheDocument();
  });

  it("identity を組めない行はリンクにしない", async () => {
    render(<App />);
    streamSnapshot(
      makeSnapshot([
        makeSession("142", [
          makePane({
            issueNum: 0,
            kind: "shell",
            shellKey: "sh1",
            displayName: "Shell pane",
            diffSummary: "+3/-1",
          }),
        ]),
      ]),
    );

    await screen.findByText("Shell pane");
    expect(screen.queryByRole("button", { name: /変更を表示/ })).not.toBeInTheDocument();
  });

  /* diffSummary と /api/diff は同じ収集を共有するので行数は一致するが、
   * binary だけ / mode だけ / pure rename の変更は両方で +0/-0 になり、
   * commit 後は clean にもなる。/api/diff はそれらを patch として返すため、
   * 行数で「差分なし」と決めるとレビュー対象を開けない行ができる。 */
  it("+0/-0 かつ clean でも identity があればリンクにする", async () => {
    render(<App />);
    streamSnapshot(
      makeSnapshot([
        makeSession("142", [
          makePane({
            issueNum: 101,
            displayName: "Binary only",
            diffSummary: "+0/-0",
            dirtyState: "clean",
          }),
        ]),
      ]),
    );

    await screen.findByText("Binary only");
    expect(
      await screen.findByRole("button", { name: /^変更を表示 .* \+0\/-0$/ }),
    ).toBeInTheDocument();
  });
});

describe("設定モーダル", () => {
  it("外観 3 択が data-theme と localStorage に同期する", async () => {
    const user = userEvent.setup();
    render(<App />);
    await user.click(screen.getByRole("button", { name: "設定" }));
    const dialog = await screen.findByRole("dialog", { name: "設定" });

    // 未選択(キー無し)はシステム追従。jsdom の matchMedia スタブは light
    expect(within(dialog).getByRole("radio", { name: "システム" })).toBeChecked();

    await user.click(within(dialog).getByRole("radio", { name: "ダーク" }));
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(localStorage.getItem("fanout.theme")).toBe("dark");

    await user.click(within(dialog).getByRole("radio", { name: "ライト" }));
    expect(document.documentElement.dataset.theme).toBe("light");
    expect(localStorage.getItem("fanout.theme")).toBe("light");

    // システムに戻すとキーごと消える(FOUC ブートストラップと同じ意味論)
    await user.click(within(dialog).getByRole("radio", { name: "システム" }));
    expect(localStorage.getItem("fanout.theme")).toBeNull();
  });

  it("モーダルを閉じたあとも OS の配色変更に追従する", async () => {
    /* 外観 store を購読するのは設定モーダルと diff オーバーレイだけで、どちらも
     * 開いている間しか購読しない。App が常駐購読を張っていないと、両方閉じた
     * 瞬間に matchMedia listener が外れ、ページ全体が古い配色で取り残される。 */
    const changeListeners = new Set<(e: MediaQueryListEvent) => void>();
    let dark = false;
    vi.stubGlobal("matchMedia", (query: string) => ({
      get matches() {
        return dark;
      },
      media: query,
      onchange: null,
      addEventListener: (_t: string, cb: (e: MediaQueryListEvent) => void) =>
        changeListeners.add(cb),
      removeEventListener: (_t: string, cb: (e: MediaQueryListEvent) => void) =>
        changeListeners.delete(cb),
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }));
    const flipOsTheme = (toDark: boolean) => {
      dark = toDark;
      act(() => {
        for (const cb of changeListeners) cb({ matches: toDark } as MediaQueryListEvent);
      });
    };

    const user = userEvent.setup();
    render(<App />);
    // 一度も設定を開かないうちから追従する
    flipOsTheme(true);
    expect(document.documentElement.dataset.theme).toBe("dark");

    // 開いて閉じても購読が途切れない
    await user.click(screen.getByRole("button", { name: "設定" }));
    const dialog = await screen.findByRole("dialog", { name: "設定" });
    await user.click(within(dialog).getByRole("button", { name: "設定を閉じる" }));
    flipOsTheme(false);
    expect(document.documentElement.dataset.theme).toBe("light");

    // 明示選択中は OS 変更を無視する(従来どおり)
    await user.click(screen.getByRole("button", { name: "設定" }));
    const dialog2 = await screen.findByRole("dialog", { name: "設定" });
    await user.click(within(dialog2).getByRole("radio", { name: "ライト" }));
    await user.click(within(dialog2).getByRole("button", { name: "設定を閉じる" }));
    flipOsTheme(true);
    expect(document.documentElement.dataset.theme).toBe("light");
  });

  it("diff テーマの選択を localStorage に保存する", async () => {
    const user = userEvent.setup();
    render(<App />);
    await user.click(screen.getByRole("button", { name: "設定" }));
    const dialog = await screen.findByRole("dialog", { name: "設定" });

    const lightSel = within(dialog).getByLabelText("ライトテーマ");
    expect(lightSel).toHaveValue("pierre-light");
    await user.selectOptions(lightSel, "github-light");
    expect(localStorage.getItem("fanout.diffTheme.light")).toBe("github-light");

    const darkSel = within(dialog).getByLabelText("ダークテーマ");
    await user.selectOptions(darkSel, "tokyo-night");
    expect(localStorage.getItem("fanout.diffTheme.dark")).toBe("tokyo-night");
  });

  it("diff テーマの見本を実物の FileDiff で light / dark 2 枚描く", async () => {
    const user = userEvent.setup();
    render(<App />);
    await user.click(screen.getByRole("button", { name: "設定" }));
    const dialog = await screen.findByRole("dialog", { name: "設定" });

    /* 見本は遅延 chunk なので解決を待つ。中身は shadow root に出る — 見本と本番の
     * 配色が必ず一致することが要点なので、自前描画に差し替えないこと。 */
    await waitFor(() => {
      expect(dialog.querySelectorAll("diffs-container")).toHaveLength(2);
    });
    const shadow = [...dialog.querySelectorAll("diffs-container")].map(
      (el) => el.shadowRoot?.textContent ?? "",
    );
    for (const text of shadow) expect(text).toContain("#00A3AF");
  });

  it("保存値が許可リスト外なら既定へ落とす(未登録テーマ名は解決時に throw する)", async () => {
    localStorage.setItem("fanout.diffTheme.light", "../etc/passwd");
    const user = userEvent.setup();
    render(<App />);
    await user.click(screen.getByRole("button", { name: "設定" }));
    const dialog = await screen.findByRole("dialog", { name: "設定" });
    expect(within(dialog).getByLabelText("ライトテーマ")).toHaveValue("pierre-light");
  });

  it("設定を開いているあいだ背面 document のスクロールを止める", async () => {
    /* backdrop 上のホイールや modal 端からのチェーンで背面の一覧が動くと、
     * 閉じたときに位置が変わってしまう。 */
    const user = userEvent.setup();
    render(<App />);
    expect(document.documentElement.style.overflow).toBe("");

    await user.click(screen.getByRole("button", { name: "設定" }));
    await screen.findByRole("dialog", { name: "設定" });
    expect(document.documentElement.style.overflow).toBe("hidden");

    await user.click(screen.getByRole("button", { name: "設定を閉じる" }));
    expect(document.documentElement.style.overflow).toBe("");
  });

  it("diff テーマの見本は読み上げ対象から外す", async () => {
    /* 伝えたいのは配色だけ。テーマ名と現在値は直後のラベル付き select が持つ */
    const user = userEvent.setup();
    render(<App />);
    await user.click(screen.getByRole("button", { name: "設定" }));
    const dialog = await screen.findByRole("dialog", { name: "設定" });

    await waitFor(() => {
      expect(dialog.querySelectorAll("diffs-container")).toHaveLength(2);
    });
    const previews = dialog.querySelectorAll(".set-theme-preview");
    expect(previews).toHaveLength(2);
    for (const el of previews) expect(el).toHaveAttribute("aria-hidden", "true");
  });

  it("Escape で閉じ、起点の歯車へフォーカスを戻す", async () => {
    const root = document.createElement("div");
    root.id = "root";
    document.body.appendChild(root);
    try {
      const user = userEvent.setup();
      render(<App />, { container: root });
      await user.click(screen.getByRole("button", { name: "設定" }));
      await screen.findByRole("dialog", { name: "設定" });
      expect(root.hasAttribute("inert")).toBe(true);

      await user.keyboard("{Escape}");

      expect(screen.queryByRole("dialog", { name: "設定" })).not.toBeInTheDocument();
      expect(root.hasAttribute("inert")).toBe(false);
      expect(screen.getByRole("button", { name: "設定" })).toHaveFocus();
    } finally {
      root.remove();
    }
  });
});
