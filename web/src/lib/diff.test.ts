import { describe, expect, it } from "vitest";
import { makeDiffFile, makeDiffResponse } from "../test/fixtures";
import { diffMeta, diffTotals, diffWarning, parseDiffFiles } from "./diff";

describe("parseDiffFiles", () => {
  it("連結 patch をライブラリのパーサで file 単位に分解する", () => {
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
    const files = parseDiffFiles(patch);
    expect(files.map((f) => f.name)).toEqual(["a.ts", "b.ts"]);
  });

  it("空 patch は空配列", () => {
    expect(parseDiffFiles("")).toEqual([]);
  });
});

describe("diffWarning", () => {
  it("完全な patch では警告しない", () => {
    expect(diffWarning(makeDiffResponse())).toBeNull();
  });

  it("truncated なら省略件数なしでも警告する", () => {
    expect(diffWarning(makeDiffResponse({ truncated: true }))).toMatch(/揃っていません/);
  });

  it("patchIncluded=false の file があれば件数付きで警告する", () => {
    const d = makeDiffResponse({
      files: [
        makeDiffFile(),
        makeDiffFile({
          path: "big.bin",
          binary: true,
          patchIncluded: false,
          omittedReason: "binary",
        }),
      ],
    });
    expect(diffWarning(d)).toMatch(/1 file/);
  });
});

describe("diffTotals", () => {
  it("collectionLimit の null 統計は数えず合計する", () => {
    const { additions, deletions } = diffTotals([
      makeDiffFile({ additions: 3, deletions: 1 }),
      makeDiffFile({
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

describe("diffMeta", () => {
  it("merge-base 短縮 SHA・取得時刻・file 数・合計統計を並べる", () => {
    expect(diffMeta(makeDiffResponse())).toMatch(
      /^merge-base 0123456789 · captured \d{2}:\d{2}:\d{2} · 1 files \+1\/-1$/,
    );
  });
});
