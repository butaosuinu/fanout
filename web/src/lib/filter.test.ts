import { describe, expect, it } from "vitest";
import { makePane } from "../test/fixtures";
import {
  filterTokens,
  matches,
  parseQuery,
  removeToken,
  replaceToken,
  stripKey,
  tokenForKey,
} from "./filter";

describe("parseQuery", () => {
  it("key:value トークンと自由語を区別する", () => {
    expect(parseQuery("state:open claude")).toEqual([
      { kind: "key", key: "state", value: "open" },
      { kind: "word", word: "claude" },
    ]);
  });

  it("未知キーは自由語に降格する", () => {
    expect(parseQuery("foo:bar")).toEqual([{ kind: "word", word: "foo:bar" }]);
  });

  it("大文字キー・値は小文字化する", () => {
    expect(parseQuery("STATE:OPEN")).toEqual([{ kind: "key", key: "state", value: "open" }]);
  });

  it("空文字・空白のみは空", () => {
    expect(parseQuery("")).toEqual([]);
    expect(parseQuery("   ")).toEqual([]);
  });
});

const q = (s: string) => parseQuery(s);

describe("matches", () => {
  it("自由語は haystack(名前・ブランチ・wave 等)を部分一致で見る", () => {
    const p = makePane({ displayName: "Fix Login", branchName: "fanout/fix-login" });
    expect(matches(p, q("login"))).toBe(true);
    expect(matches(p, q("logout"))).toBe(false);
  });

  it("state: は tmux 状態に一致すればそれを、さもなくば issue 状態を見る", () => {
    const live = makePane({ tmuxState: "live", issueState: "CLOSED" });
    expect(matches(live, q("state:live"))).toBe(true);
    expect(matches(live, q("state:closed"))).toBe(true); // tmuxState !== "closed" → issueState
    expect(matches(live, q("state:open"))).toBe(false);
    const stale = makePane({ tmuxState: "stale", issueState: "OPEN" });
    expect(matches(stale, q("state:stale"))).toBe(true);
    expect(matches(stale, q("state:open"))).toBe(true);
  });

  it("run: は agentState と厳密比較(未設定は空文字)", () => {
    expect(matches(makePane({ agentState: "running" }), q("run:running"))).toBe(true);
    expect(matches(makePane({ agentState: "plan" }), q("run:plan"))).toBe(true);
    expect(matches(makePane({ agentState: "blocked" }), q("run:blocked"))).toBe(true);
    expect(matches(makePane({ agentState: "done" }), q("run:running"))).toBe(false);
    expect(matches(makePane({}), q("run:running"))).toBe(false);
  });

  it("run: は 6 値契約の hook 値(working/plan/blocked/idle)にも一致する", () => {
    for (const state of ["working", "plan", "blocked", "idle"]) {
      expect(matches(makePane({ agentState: state }), q(`run:${state}`))).toBe(true);
      expect(matches(makePane({ agentState: state }), q("run:running"))).toBe(false);
    }
  });

  it("agent: は部分一致", () => {
    expect(matches(makePane({ agent: "claude" }), q("agent:clau"))).toBe(true);
    expect(matches(makePane({ agent: "codex" }), q("agent:claude"))).toBe(false);
  });

  it("wave: は数値・waveLabel・wN 表記の 3 通りで一致", () => {
    const p = makePane({ wave: 2 });
    expect(matches(p, q("wave:2"))).toBe(true);
    expect(matches(p, q("wave:w2"))).toBe(true);
    expect(matches(p, q("wave:3"))).toBe(false);
    const labeled = makePane({ wave: 1, waveLabel: "W1*" });
    expect(matches(labeled, q("wave:w1*"))).toBe(true);
  });

  it("derived waveText が full label のときも compact wN alias で一致する", () => {
    const p = makePane({ wave: 2, derived: { waveText: "W2 ready", dependencyWave: "wave2" } });
    expect(matches(p, q("wave:w2"))).toBe(true);
  });

  it("ci: は paneCI(primary-PR の ciStatus、'-' は CI なし)を見る", () => {
    expect(matches(makePane({ ciStatus: "fail" }), q("ci:fail"))).toBe(true);
    expect(matches(makePane({ ciStatus: "-" }), q("ci:fail"))).toBe(false);
    // ciStatus 不在の旧 snapshot は worst-of-prs に fallback
    expect(
      matches(
        makePane({ prs: [{ number: 1, state: "OPEN", mergedAt: null, ci: "fail" }] }),
        q("ci:fail"),
      ),
    ).toBe(true);
  });

  it("dirty:/live: は yes/no の XOR 論理", () => {
    expect(matches(makePane({ dirtyState: "dirty" }), q("dirty:yes"))).toBe(true);
    expect(matches(makePane({ dirtyState: "clean" }), q("dirty:no"))).toBe(true);
    expect(matches(makePane({ dirtyState: "unknown" }), q("dirty:no"))).toBe(true);
    expect(matches(makePane({ alive: true }), q("live:yes"))).toBe(true);
    expect(matches(makePane({ alive: false }), q("live:no"))).toBe(true);
  });

  it("issue: は完全一致", () => {
    expect(matches(makePane({ issueNum: 12 }), q("issue:12"))).toBe(true);
    expect(matches(makePane({ issueNum: 123 }), q("issue:12"))).toBe(false);
    expect(matches(makePane({ issueNum: 0, taskId: "plan-lint" }), q("issue:plan-lint"))).toBe(
      true,
    );
  });

  it("taskId は自由語検索の対象になる", () => {
    const p = makePane({ issueNum: 0, taskId: "plan-dashboard" });
    expect(matches(p, q("dashboard"))).toBe(true);
    expect(matches(p, q("plan-review"))).toBe(false);
  });

  it("task: は taskId の完全一致で、自由語にも taskId が入る", () => {
    expect(matches(makePane({ issueNum: 0, taskId: "api-client" }), q("task:api-client"))).toBe(
      true,
    );
    expect(matches(makePane({ issueNum: 0, taskId: "api-client" }), q("api-client"))).toBe(true);
    expect(matches(makePane({ issueNum: 0, taskId: "api-client" }), q("task:base-types"))).toBe(
      false,
    );
  });

  it("pr: は primary PR の状態、無ければ none", () => {
    expect(matches(makePane({ prs: null }), q("pr:none"))).toBe(true);
    const prs = [
      { number: 1, state: "OPEN", mergedAt: null },
      { number: 2, state: "MERGED", mergedAt: "2026-06-01T00:00:00Z" },
    ];
    // MERGED 優先が primary
    expect(matches(makePane({ prs }), q("pr:merged"))).toBe(true);
    expect(matches(makePane({ prs }), q("pr:open"))).toBe(false);
  });

  it("複数 term は AND", () => {
    const p = makePane({ agent: "claude", dirtyState: "dirty" });
    expect(matches(p, q("agent:claude dirty:yes"))).toBe(true);
    expect(matches(p, q("agent:claude dirty:no"))).toBe(false);
  });

  it("derived の filterText / filterValues を優先する", () => {
    const p = makePane({
      displayName: "fallback",
      agentState: "",
      dirtyState: "clean",
      derived: {
        filterText: "shared haystack",
        filterValues: { run: "running", dirty: "yes", live: "no", issue: "777", pr: "merged" },
      },
    });
    expect(matches(p, q("shared run:running dirty:yes live:no issue:777 pr:merged"))).toBe(true);
    expect(matches(p, q("fallback"))).toBe(false);
  });
});

describe("トークン操作", () => {
  it("filterTokens は空白区切り", () => {
    expect(filterTokens("  a  b:c ")).toEqual(["a", "b:c"]);
  });

  it("replaceToken は同キーを上書きする", () => {
    expect(replaceToken(["state:open", "claude"], "state", "closed")).toEqual([
      "claude",
      "state:closed",
    ]);
  });

  it("removeToken は完全一致のみ外す", () => {
    expect(removeToken(["state:open", "state:o"], "state:open")).toEqual(["state:o"]);
  });

  it("tokenForKey は指定キーの最初のトークン値を小文字で返す", () => {
    expect(tokenForKey(["claude", "state:open"], "state")).toBe("open");
    // 手打ちの大文字も小文字に正規化して返す(選択肢との比較用)
    expect(tokenForKey(["STATE:Open"], "state")).toBe("open");
  });

  it("tokenForKey は該当キーが無ければ null(自由語・他キー・前方一致違いは見ない)", () => {
    expect(tokenForKey([], "state")).toBeNull();
    expect(tokenForKey(["open", "agent:claude", "statex:open"], "state")).toBeNull();
  });

  it("stripKey は同キーのトークンを重複・大文字違いごと全て外す", () => {
    expect(stripKey(["state:open", "STATE:closed", "agent:claude"], "state")).toEqual([
      "agent:claude",
    ]);
    expect(stripKey(["claude"], "state")).toEqual(["claude"]);
  });
});
