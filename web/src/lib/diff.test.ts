import { describe, expect, it } from "vitest";
import { makePane, makeQueuedPane } from "../test/fixtures";
import { diffQuery, diffTotals, diffWarning, splitPatch } from "./diff";
import type { DiffFileEntry, DiffResponse } from "./types";

function makeFile(over: Partial<DiffFileEntry> = {}): DiffFileEntry {
  return {
    path: "src/a.ts",
    additions: 1,
    deletions: 0,
    binary: false,
    patchIncluded: true,
    omittedReason: "",
    ...over,
  };
}

function makeDiff(over: Partial<DiffResponse> = {}): DiffResponse {
  return {
    paneId: "%1",
    branchName: "fanout/fix-thing",
    baseBranch: "main",
    mergeBase: "0123456789abcdef0123456789abcdef01234567",
    capturedAt: "2026-07-29T01:23:45Z",
    files: [makeFile()],
    patch: "",
    truncated: false,
    totalBytes: 0,
    ...over,
  };
}

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

describe("splitPatch", () => {
  it("連結 patch を diff --git 境界で file block に分割する", () => {
    const patch = [
      "diff --git a/a.ts b/a.ts",
      "--- a/a.ts",
      "+++ b/a.ts",
      "@@ -1 +1 @@",
      "-old",
      "+new",
      "diff --git a/b.ts b/b.ts",
      "--- a/b.ts",
      "+++ b/b.ts",
      "@@ -1 +1 @@",
      "-x",
      "+y",
      "",
    ].join("\n");
    const blocks = splitPatch(patch);
    expect(blocks).toHaveLength(2);
    expect(blocks[0]).toContain("a/a.ts");
    expect(blocks[0]).toContain("+new");
    expect(blocks[1]).toContain("a/b.ts");
    expect(blocks[1]).toContain("+y");
  });

  it("内容行の 'diff --git' 風テキストでは分割しない(行頭一致のみ)", () => {
    const patch = [
      "diff --git a/a.md b/a.md",
      "--- a/a.md",
      "+++ b/a.md",
      "@@ -1 +1,2 @@",
      " context",
      "+text mentioning diff --git a/x b/x",
      "",
    ].join("\n");
    expect(splitPatch(patch)).toHaveLength(1);
  });

  it("空 patch は空配列", () => {
    expect(splitPatch("")).toEqual([]);
  });
});

describe("diffWarning", () => {
  it("完全な patch では警告しない", () => {
    expect(diffWarning(makeDiff())).toBeNull();
  });

  it("truncated なら省略件数なしでも警告する", () => {
    expect(diffWarning(makeDiff({ truncated: true }))).toMatch(/揃っていません/);
  });

  it("patchIncluded=false の file があれば件数付きで警告する", () => {
    const d = makeDiff({
      files: [
        makeFile(),
        makeFile({ path: "big.bin", binary: true, patchIncluded: false, omittedReason: "binary" }),
      ],
    });
    expect(diffWarning(d)).toMatch(/1 file/);
  });
});

describe("diffTotals", () => {
  it("collectionLimit の null 統計は数えず合計する", () => {
    const { additions, deletions } = diffTotals([
      makeFile({ additions: 3, deletions: 1 }),
      makeFile({
        path: "skipped.ts",
        additions: null,
        deletions: null,
        patchIncluded: false,
        omittedReason: "collectionLimit",
      }),
    ]);
    expect(additions).toBe(3);
    expect(deletions).toBe(1);
  });
});
