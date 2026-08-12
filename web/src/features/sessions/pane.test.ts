import { describe, expect, it } from "vitest";
import { makePane, makeQueuedPane } from "../../test/fixtures";
import type { PRRef } from "../../transport/types";
import {
  diffQuery,
  paneBackend,
  paneIssueURL,
  paneLabel,
  prDisplayState,
  prHasConflict,
  prReviewValue,
  rowKey,
} from "./pane";

/* ghissue.PRRef のミラー群。Go 側の DisplayState / HasConflict /
 * reviewFilterValue と同じ判断をすることを固定する。 */
describe("PR ref mirrors of the Go helpers", () => {
  const pr = (over: Partial<PRRef>): PRRef => ({
    number: 1,
    state: "OPEN",
    mergedAt: null,
    ...over,
  });

  const displayStateCases: { name: string; in: PRRef; want: string }[] = [
    {
      name: "merged が reviewDecision より優先",
      in: pr({ state: "MERGED", reviewDecision: "APPROVED" }),
      want: "merged",
    },
    // state が OPEN のままでも mergedAt があれば merged 扱い(Go と同じ)
    {
      name: "mergedAt があれば state が OPEN でも merged",
      in: pr({ mergedAt: "2026-06-01T00:00:00Z" }),
      want: "merged",
    },
    {
      name: "closed が draft より優先",
      in: pr({ state: "CLOSED", isDraft: true }),
      want: "closed",
    },
    {
      name: "draft が reviewDecision より優先",
      in: pr({ isDraft: true, reviewDecision: "APPROVED" }),
      want: "draft",
    },
    {
      name: "APPROVED を approved に潰す",
      in: pr({ reviewDecision: "APPROVED" }),
      want: "approved",
    },
    {
      name: "CHANGES_REQUESTED をハイフン化",
      in: pr({ reviewDecision: "CHANGES_REQUESTED" }),
      want: "changes-requested",
    },
    {
      name: "REVIEW_REQUIRED をハイフン化",
      in: pr({ reviewDecision: "REVIEW_REQUIRED" }),
      want: "review-required",
    },
    {
      name: "未知の decision は state に落ちる",
      in: pr({ reviewDecision: "SOMETHING_NEW" }),
      want: "open",
    },
    { name: "decision 無しは state 小文字", in: pr({}), want: "open" },
    { name: "state 空は空", in: pr({ state: "" }), want: "" },
  ];

  for (const tt of displayStateCases) {
    it(`prDisplayState: ${tt.name}`, () => {
      expect(prDisplayState(tt.in)).toBe(tt.want);
    });
  }

  it("prHasConflict は CONFLICTING だけを衝突とみなす", () => {
    expect(prHasConflict(pr({ mergeable: "CONFLICTING" }))).toBe(true);
    expect(prHasConflict(pr({ mergeable: "MERGEABLE" }))).toBe(false);
    // 欠落 = MERGED/CLOSED か GitHub 再計算中。「衝突なし」ではないが出しもしない
    expect(prHasConflict(pr({}))).toBe(false);
  });

  it("prReviewValue は merged でも decision を返す(DisplayState と直交)", () => {
    expect(prReviewValue(pr({ reviewDecision: "CHANGES_REQUESTED" }))).toBe("changes-requested");
    expect(prReviewValue(pr({ state: "MERGED", reviewDecision: "APPROVED" }))).toBe("approved");
    expect(prReviewValue(pr({ reviewDecision: "" }))).toBe("none");
    expect(prReviewValue(null)).toBe("none");
  });
});

describe("pane identity helpers", () => {
  it("normalizes a missing legacy backend to tmux", () => {
    expect(paneBackend(makePane())).toBe("tmux");
    expect(paneBackend(makePane({ backend: "" }))).toBe("tmux");
    expect(paneBackend(makePane({ backend: " HERDR " }))).toBe("herdr");
    expect(paneBackend(makePane({ backend: "", notStarted: true }))).toBe("");
  });

  it("keys issue rows by parent and issue number", () => {
    const pane = makePane({ issueNum: 42 });

    expect(rowKey("100", pane)).toBe("100#42");
    expect(paneLabel(pane)).toBe("#42");
  });

  it("keys task rows by parent and task id", () => {
    const pane = makePane({ issueNum: 0, taskId: "api-client" });

    expect(rowKey("plan:launch-plan", pane)).toBe("plan:launch-plan@api-client");
    expect(paneLabel(pane)).toBe("api-client");
  });

  it("labels attached agents by their source issue or task", () => {
    const issueAgent = makePane({
      kind: "attached-agent",
      issueNum: -1,
      sourceIssueNum: 42,
    });
    const taskAgent = makePane({
      kind: "attached-agent",
      issueNum: -2,
      sourceTaskId: "api-client",
    });

    expect(paneLabel(issueAgent)).toBe("#42");
    expect(paneIssueURL("octo/fanout", issueAgent)).toBe(
      "https://github.com/octo/fanout/issues/42",
    );
    expect(paneLabel(taskAgent)).toBe("api-client");
    expect(paneIssueURL("octo/fanout", taskAgent)).toBe("");
  });

  it("disambiguates worktree-local rows by sourceKey", () => {
    const a = makePane({ issueNum: 0, taskId: "api", sourceKey: "aaaa" });
    const b = makePane({ issueNum: 0, taskId: "api", sourceKey: "bbbb" });

    // Same plan task in two worktrees must not collide on the row key.
    expect(rowKey("plan:launch", a)).toBe("plan:launch@api~aaaa");
    expect(rowKey("plan:launch", b)).toBe("plan:launch@api~bbbb");
    expect(rowKey("plan:launch", a)).not.toBe(rowKey("plan:launch", b));

    // GitHub issue rows carry no sourceKey, so the key is unchanged.
    expect(rowKey("100", makePane({ issueNum: 42 }))).toBe("100#42");
  });
});

/* /api/diff の行 identity クエリ — rowKey と同じ識別規則ファミリー */
describe("diffQuery", () => {
  const tests = [
    {
      name: "GitHub issue 行は parent+issue のみ(source は付けない)",
      parent: "142",
      pane: makePane({ issueNum: 101 }),
      want: { parent: "142", issue: "101" },
    },
    {
      name: "plan task 行は parent+task+source",
      parent: "plan:alpha",
      pane: makePane({ issueNum: 0, taskId: "plan-lint", sourceKey: "wt1" }),
      want: { parent: "plan:alpha", task: "plan-lint", source: "wt1" },
    },
    {
      name: "負の synthetic issue 行(@manual)は parent+issue+source",
      parent: "@manual",
      pane: makePane({ issueNum: -1, sourceKey: "manual-prompt" }),
      want: { parent: "@manual", issue: "-1", source: "manual-prompt" },
    },
    {
      name: "worktree-local な plan 行に sourceKey が無ければ identity を組めない",
      parent: "plan:alpha",
      pane: makePane({ issueNum: 0, taskId: "plan-lint" }),
      want: null,
    },
    {
      name: "負の issue 行に sourceKey が無ければ identity を組めない",
      parent: "@manual",
      pane: makePane({ issueNum: -1 }),
      want: null,
    },
    {
      name: "shell 行は対象外",
      parent: "142",
      pane: makePane({ kind: "shell", issueNum: 0, shellKey: "sh1" }),
      want: null,
    },
    {
      name: "未開始(synthetic)行は対象外",
      parent: "142",
      pane: makeQueuedPane(),
      want: null,
    },
    {
      name: "worktree 記録の無い行は対象外",
      parent: "142",
      pane: makePane({ issueNum: 101, worktreePath: "" }),
      want: null,
    },
  ];
  for (const tt of tests) {
    it(tt.name, () => {
      expect(diffQuery(tt.parent, tt.pane)).toEqual(tt.want);
    });
  }
});
