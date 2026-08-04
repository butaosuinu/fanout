import { i18n } from "@lingui/core";
import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { App } from "../app/App";
import { useLocale } from "../features/settings/useSettings";
import { activateLocale, detectLocale } from "../i18n";
import { installFakeEventSource, streamSnapshot } from "./fakeEventSource";
import { makePane, makeSession, makeSnapshot } from "./fixtures";

/* 表示言語の統合テスト。モックはネットワーク境界(SSE)と navigator.languages
 * だけで、i18n の内部・コンポーネントはモックしない。 */

/* setup.ts が ja-JP を仕込んでいるので、検出結果を変えたいケースだけ上書きする。 */
function setBrowserLanguages(tags: string[]) {
  Object.defineProperty(window.navigator, "languages", { value: tags, configurable: true });
  Object.defineProperty(window.navigator, "language", {
    value: tags[0] ?? "",
    configurable: true,
  });
}

function snapshotWithOnePane() {
  return makeSnapshot([
    makeSession("142", [
      makePane({
        issueNum: 101,
        displayName: "Fix login",
        slug: "fix-login",
        agent: "claude",
        paneId: "%1",
        branchName: "fanout/fix-login",
        diffSummary: "+10/-2",
      }),
    ]),
  ]);
}

const openSettings = async (user: ReturnType<typeof userEvent.setup>, name: string) => {
  await user.click(screen.getByRole("button", { name }));
  return screen.findByRole("dialog");
};

beforeEach(() => {
  installFakeEventSource();
  localStorage.clear();
  setBrowserLanguages(["ja-JP", "ja"]);
  activateLocale("ja");
});

afterEach(() => {
  /* 明示指定と検出結果の両方を既定へ戻す。vitest はファイル単位でモジュールを
   * 隔離するが、同一ファイル内のテスト順に依存させない。 */
  localStorage.removeItem("fanout.locale");
  setBrowserLanguages(["ja-JP", "ja"]);
  activateLocale("ja");
});

describe("表示言語の検出", () => {
  it("日本語のブラウザは ja、それ以外は en に落とす", () => {
    const cases: { name: string; tags: string[]; want: string }[] = [
      { name: "日本語が先頭なら ja", tags: ["ja-JP", "en-US"], want: "ja" },
      { name: "地域なしの ja も ja", tags: ["ja"], want: "ja" },
      { name: "英語は en", tags: ["en-GB"], want: "en" },
      // ja / en どちらでもない言語は英語へ寄せる(未対応言語の既定)
      { name: "未対応言語は en", tags: ["fr-FR", "de-DE"], want: "en" },
      // ja より前に en があれば en — languages は優先度順
      { name: "優先度順を尊重する", tags: ["en-US", "ja-JP"], want: "en" },
      { name: "空なら en", tags: [], want: "en" },
    ];
    for (const tc of cases) {
      setBrowserLanguages(tc.tags);
      expect(detectLocale(), `detectLocale(${JSON.stringify(tc.tags)}) [${tc.name}]`).toBe(tc.want);
    }
  });

  /* 起動時の解決(明示指定 → 無ければブラウザ言語)は設定ストアが持つ。
   * モジュール評価時の 1 回きりの activate はテストから再実行できないので、
   * ストアが返す解決結果を直接見る。 */
  function LocaleProbe() {
    const { mode, locale } = useLocale();
    return <output data-testid="locale">{`${mode}/${locale}`}</output>;
  }

  it("明示指定が無ければブラウザ言語から解決する", () => {
    setBrowserLanguages(["en-US"]);
    render(<LocaleProbe />);
    expect(screen.getByTestId("locale")).toHaveTextContent("auto/en");
  });

  it("英語ロケールでは全体が英語で描画される", async () => {
    activateLocale("en");
    render(<App />);
    streamSnapshot(makeSnapshot([])); // 空状態の文言を出すため snapshot は要る
    await screen.findByRole("button", { name: "Settings" });
    expect(screen.getByText("No active sessions")).toBeInTheDocument();
    expect(document.documentElement.lang).toBe("en");
  });

  it("自動のままブラウザ言語が変わったら追従する", async () => {
    render(<App />);
    streamSnapshot(makeSnapshot([]));
    await screen.findByRole("button", { name: "設定" });

    // ブラウザ / OS の言語設定は実行中に変わりうる。追従しないと再読み込みまで
    // 旧言語で取り残される(外観の matchMedia 追従と同じ契約)。
    setBrowserLanguages(["en-US"]);
    act(() => window.dispatchEvent(new Event("languagechange")));

    expect(screen.getByRole("button", { name: "Settings" })).toBeInTheDocument();
    expect(document.documentElement.lang).toBe("en");
    expect(localStorage.getItem("fanout.locale")).toBeNull(); // 自動のまま
  });

  it("明示指定中はブラウザ言語が変わっても動かない", async () => {
    localStorage.setItem("fanout.locale", "ja");
    activateLocale("ja");
    render(<App />);
    await screen.findByRole("button", { name: "設定" });

    setBrowserLanguages(["en-US"]);
    act(() => window.dispatchEvent(new Event("languagechange")));

    expect(screen.getByRole("button", { name: "設定" })).toBeInTheDocument();
    expect(document.documentElement.lang).toBe("ja");
  });

  it("明示指定はブラウザ言語より優先する", () => {
    setBrowserLanguages(["en-US"]);
    localStorage.setItem("fanout.locale", "ja");
    render(<LocaleProbe />);
    expect(screen.getByTestId("locale")).toHaveTextContent("ja/ja");
  });
});

describe("設定モーダルの言語切替", () => {
  it("English を選ぶと表示が英語になり、localStorage に保存する", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithOnePane());

    const dialog = await openSettings(user, "設定");
    await user.click(within(dialog).getByRole("radio", { name: "English" }));

    expect(localStorage.getItem("fanout.locale")).toBe("en");
    expect(document.documentElement.lang).toBe("en");
    expect(i18n.locale).toBe("en");
    // モーダル自身もその場で英語になる(再マウントを挟まない)
    expect(within(dialog).getByRole("heading", { name: "Settings" })).toBeInTheDocument();
    expect(within(dialog).getByRole("heading", { name: "Language" })).toBeInTheDocument();
  });

  it("「自動」に戻すとキーを消してブラウザ言語へ戻る", async () => {
    const user = userEvent.setup();
    localStorage.setItem("fanout.locale", "en");
    activateLocale("en");
    render(<App />);

    const dialog = await openSettings(user, "Settings");
    await user.click(within(dialog).getByRole("radio", { name: "Auto" }));

    // キー不在 = 追従。他の設定(fanout.theme 等)と同じ意味論。
    expect(localStorage.getItem("fanout.locale")).toBeNull();
    expect(i18n.locale).toBe("ja");
    expect(document.documentElement.lang).toBe("ja");
  });

  it("言語名は自国語のまま両ロケールで同じ表記にする", async () => {
    const user = userEvent.setup();
    render(<App />);
    const dialog = await openSettings(user, "設定");
    for (const name of ["日本語", "English"]) {
      expect(within(dialog).getByRole("radio", { name })).toBeInTheDocument();
    }
    await user.click(within(dialog).getByRole("radio", { name: "English" }));
    for (const name of ["日本語", "English"]) {
      expect(within(dialog).getByRole("radio", { name })).toBeInTheDocument();
    }
  });
});

describe("英語ロケールの描画", () => {
  it("意図的に英語のままの語彙は両ロケールで変えない", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithOnePane());
    const dialog = await openSettings(user, "設定");
    await user.click(within(dialog).getByRole("radio", { name: "English" }));
    await user.keyboard("{Escape}");

    // 表ヘッダ・HUD・タグはフィルタ構文と wire の語彙に揃えて英語固定
    const headers = within(screen.getByRole("table"))
      .getAllByRole("columnheader")
      // ソート中の列は名前に方向マーク(▴/▾)が付くので落とす
      .map((th) => th.textContent?.replace(/[▴▾]/g, "").trim());
    expect(headers).toEqual([
      "issue",
      "name",
      "agent",
      "wave",
      "blockers",
      "branch",
      "diff",
      "dirty",
      "ci",
      "runtime",
      "state",
      "pr",
    ]);
    const hud = screen.getByRole("region", { name: "Summary" });
    expect(
      within(hud)
        .getAllByText(/^[a-z ]+$/)
        .map((el) => el.textContent),
    ).toEqual(["panes", "live", "active", "not started", "merged", "pending", "blocked"]);
  });

  it("memo されたコンポーネントもロケール変更に追従する", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithOnePane());

    // FilterBar -> memo(FilterDropdown) の aria-label は prop 経由で伝わる
    expect(screen.getByRole("button", { name: "issue / runtime 状態で絞り込み" })).toBeTruthy();
    const dialog = await openSettings(user, "設定");
    await user.click(within(dialog).getByRole("radio", { name: "English" }));
    await user.keyboard("{Escape}");

    expect(screen.getByRole("button", { name: "Filter by issue / runtime state" })).toBeTruthy();
  });

  it("ドロワーの文言も切替後に英語になる", async () => {
    const user = userEvent.setup();
    render(<App />);
    streamSnapshot(snapshotWithOnePane());

    await user.click(within(screen.getByRole("table")).getAllByRole("row")[1]!);
    expect(await screen.findByLabelText("詳細を閉じる")).toBeInTheDocument();

    const dialog = await openSettings(user, "設定");
    await user.click(within(dialog).getByRole("radio", { name: "English" }));
    await user.keyboard("{Escape}");

    expect(screen.getByLabelText("Close details")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Show changes" })).toBeInTheDocument();
  });
});
