import { describe, expect, it } from "vitest";
import { makePane } from "../test/fixtures";
import { sortPanes } from "./sort";

const nums = (ps: { issueNum: number }[]) => ps.map((p) => p.issueNum);
const taskIds = (ps: { taskId?: string }[]) => ps.map((p) => p.taskId);

describe("sortPanes", () => {
  it("diff は +X/-Y の総量、パース不能は -1(先頭)", () => {
    const a = makePane({ issueNum: 1, diffSummary: "+10/-2" });
    const b = makePane({ issueNum: 2, diffSummary: "(worktree error)" });
    const c = makePane({ issueNum: 3, diffSummary: "+1/-0" });
    expect(nums(sortPanes([a, b, c], "diff", 1))).toEqual([2, 3, 1]);
  });

  it("ci は fail < pending < pass < なし", () => {
    const fail = makePane({ issueNum: 1, ciStatus: "fail" });
    const pass = makePane({ issueNum: 2, ciStatus: "pass" });
    const pending = makePane({ issueNum: 3, ciStatus: "pending" });
    const none = makePane({ issueNum: 4, ciStatus: "-" });
    expect(nums(sortPanes([pass, none, fail, pending], "ci", 1))).toEqual([1, 3, 2, 4]);
  });

  it("pr は MERGED < OPEN < CLOSED < none", () => {
    const merged = makePane({ issueNum: 1, prs: [{ number: 1, state: "MERGED", mergedAt: "x" }] });
    const open = makePane({ issueNum: 2, prs: [{ number: 2, state: "OPEN", mergedAt: null }] });
    const closed = makePane({ issueNum: 3, prs: [{ number: 3, state: "CLOSED", mergedAt: null }] });
    const none = makePane({ issueNum: 4, prs: null });
    expect(nums(sortPanes([none, closed, open, merged], "pr", 1))).toEqual([1, 2, 3, 4]);
  });

  it("一次キー同値は issueNum 昇順の安定二次ソート(降順でも)", () => {
    const a = makePane({ issueNum: 3, agent: "claude" });
    const b = makePane({ issueNum: 1, agent: "claude" });
    const c = makePane({ issueNum: 2, agent: "codex" });
    expect(nums(sortPanes([a, b, c], "agent", 1))).toEqual([1, 3, 2]);
    expect(nums(sortPanes([a, b, c], "agent", -1))).toEqual([2, 1, 3]);
  });

  it("issueNum が同じ task 行は taskId で安定ソートする", () => {
    const b = makePane({ issueNum: 0, taskId: "task-b", agent: "codex" });
    const a = makePane({ issueNum: 0, taskId: "task-a", agent: "codex" });
    expect(taskIds(sortPanes([b, a], "agent", 1))).toEqual(["task-a", "task-b"]);
  });

  it("dir 反転で順序が逆になる", () => {
    const a = makePane({ issueNum: 1 });
    const b = makePane({ issueNum: 2 });
    expect(nums(sortPanes([a, b], "issueNum", -1))).toEqual([2, 1]);
  });

  it("wave 未設定は 99 扱いで末尾", () => {
    const w1 = makePane({ issueNum: 1, wave: 1 });
    const none = makePane({ issueNum: 2 });
    expect(nums(sortPanes([none, w1], "wave", 1))).toEqual([1, 2]);
  });

  it("derived sort keys を優先する", () => {
    const highDiff = makePane({
      issueNum: 1,
      diffSummary: "+999/-0",
      derived: { sort: { diff: 1 } },
    });
    const lowDiff = makePane({
      issueNum: 2,
      diffSummary: "+1/-0",
      derived: { sort: { diff: 999 } },
    });
    expect(nums(sortPanes([lowDiff, highDiff], "diff", 1))).toEqual([1, 2]);
  });
});
